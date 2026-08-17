import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { buildMultipartBody, buildRequestBody, buildURLEncodedBody, planRequestBodies } from "./media.js";
import { routeInput, serializeParamContent } from "./params.js";
import { planAbstractInputRoutes } from "./input-routes-v2.js";
import { jsonImage } from "./json-image.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";
import type {
  OpenAPIDocument,
  OpenAPIMediaType,
  OpenAPIOperation,
  OpenAPIParameter,
} from "./types.js";

// The identical file is executed by openbindings-go/formats/openapi and by
// openapi-client/go; changing it in one engine without the others fails here.
const CASES_DIGEST = "620a14d1f8f3572ff087e5dbc2f84603a2b79513cc9661f83f357db463792e80";

interface JSONImageCase {
  name: string;
  openapi: string;
  lane: string;
  cell: string;
  propertyName: string;
  value: Record<string, unknown>;
  expect: string;
  basis: string;
}

interface JSONImageTable {
  cells: string[];
  lanes: string[];
  cases: JSONImageCase[];
}

function loadTable(): JSONImageTable {
  const raw = readFileSync(new URL("./testdata/json-image-cases.json", import.meta.url));
  const digest = createHash("sha256").update(raw).digest("hex");
  if (digest !== CASES_DIGEST) {
    throw new Error(
      `case table digest = ${digest}, want ${CASES_DIGEST} (the table is shared byte-for-byte with the two Go engines)`,
    );
  }
  const table = JSON.parse(raw.toString("utf8")) as JSONImageTable;
  if (table.cases.length === 0) throw new Error("case table is empty");
  return table;
}

const OBJECT_SCHEMA = { type: "object" };

function bodyMedia(c: JSONImageCase): OpenAPIMediaType {
  if (c.lane === "json-body") return { schema: OBJECT_SCHEMA } as unknown as OpenAPIMediaType;
  return {
    schema: { type: "object", properties: { [c.propertyName]: OBJECT_SCHEMA } },
  } as unknown as OpenAPIMediaType;
}

function mediaKey(lane: string): string {
  if (lane === "json-body") return "application/json";
  if (lane === "multipart-part") return "multipart/form-data";
  return "application/x-www-form-urlencoded";
}

function operation(c: JSONImageCase): OpenAPIOperation {
  if (c.lane === "parameter-content") {
    return {
      parameters: [{
        name: c.propertyName,
        in: "query",
        content: { "application/json": { schema: OBJECT_SCHEMA } },
      }],
    } as unknown as OpenAPIOperation;
  }
  return {
    requestBody: { required: true, content: { [mediaKey(c.lane)]: bodyMedia(c) } },
  } as unknown as OpenAPIOperation;
}

async function renderFormData(body: unknown): Promise<string> {
  if (!(body instanceof FormData)) return String(body);
  const rendered: string[] = [];
  for (const [, part] of body.entries()) {
    if (typeof part === "string") rendered.push(part);
    else rendered.push(`${part.type}:${await part.text()}`);
  }
  return rendered.join("&");
}

async function emission(c: JSONImageCase): Promise<string> {
  if (c.lane === "parameter-content") {
    const p = (operation(c).parameters as OpenAPIParameter[])[0] as OpenAPIParameter;
    try {
      return serializeParamContent(p, c.value);
    } catch {
      return "error";
    }
  }
  const doc = { openapi: c.openapi } as OpenAPIDocument;
  let plans;
  try {
    plans = planRequestBodies(operation(c), { profile: OPENAPI_PROFILE_FULL, openapiVersion: c.openapi });
  } catch {
    return "refused";
  }
  if (plans.length !== 1) return "refused";

  if (c.lane === "json-body") {
    // Both JSON-body branches are covered without the table having to know
    // which one this plan takes: the synthetic/whole-object branch reads
    // bodyValue and the field branch reads bodyFields, and both are given the
    // same value, so either branch owes the same bytes.
    const wire = buildRequestBody(doc, plans[0] ?? null, {
      bodySet: true,
      bodyValue: c.value,
      bodyFields: c.value,
    });
    return String(wire.body);
  }

  const fields = { [c.propertyName]: c.value };
  if (c.lane === "urlencoded-content" || c.lane === "urlencoded-style") {
    try {
      return buildURLEncodedBody(bodyMedia(c), fields, true, c.openapi, false);
    } catch {
      return "error";
    }
  }
  try {
    return await renderFormData(buildMultipartBody(doc, bodyMedia(c), fields, true, false));
  } catch {
    return "error";
  }
}

// The expectations are authored from three rules and never from an engine:
// RFC 8259 Section 7 plus the implementations' stated literal-character
// convention for the JSON image itself; RFC 1866 Section 8.2.1 / RFC 1738
// Section 2.2 (the WHATWG form-urlencoded serializer set) for the content
// lane's escaper; and RFC 6570 with allowReserved not in effect — RFC 3986's
// unreserved set — for the style lane's. See the table's own $comment.
describe("json image case table", () => {
  const table = loadTable();

  for (const c of table.cases) {
    it(c.name, async () => {
      const got = await emission(c);
      if (got !== c.expect) {
        throw new Error(`${c.name}: emission = ${JSON.stringify(got)}, want ${JSON.stringify(c.expect)}\nbasis: ${c.basis}`);
      }
    });
  }

  // Fails if the table stops exercising a lane or a character class. A cell
  // silently dropping out of a table that is the only guard on a convention is
  // the failure mode this asserts against.
  it("covers every lane and every character class", () => {
    const wantLanes = [
      "3.0.3|json-body",
      "3.0.3|urlencoded-style",
      "3.0.4|urlencoded-content",
      "3.1.1|json-body",
      "3.1.1|multipart-part",
      "3.1.1|parameter-content",
      "3.1.1|urlencoded-content",
    ];
    const wantCells = ["all-three", "ampersand", "greater-than", "key-and-value", "less-than", "none"];
    expect([...table.lanes].sort()).toStrictEqual(wantLanes);
    expect([...table.cells].sort()).toStrictEqual(wantCells);
    const seen = new Set(table.cases.map((c) => `${c.openapi}|${c.lane}|${c.cell}`));
    expect(seen.size).toBe(wantLanes.length * wantCells.length);
    for (const lane of wantLanes) {
      for (const cell of wantCells) expect(seen.has(`${lane}|${cell}`), `${lane}|${cell}`).toBe(true);
    }
  });

  // The control, as a claim in its own right. The two urlencoded lanes are
  // SUPPOSED to disagree — a space is `+` on the content lane and %20 on the
  // style lane, which is audited and closed — and the style lane never forms a
  // JSON image at all.
  it("leaves the style lane free of any JSON image", () => {
    const style = table.cases.filter((c) => c.lane === "urlencoded-style");
    expect(style).toHaveLength(6);
    for (const c of style) {
      expect(c.expect, c.name).not.toContain("%7B");
      expect(c.expect, c.name).not.toContain("%22");
      expect(c.expect, c.name).not.toContain("+");
    }
  });
});

// The unit pinned directly, so a caller reverting to a host default fails here
// as well as in the lane cells. RFC 8259 Section 7's MUST-escape set is
// quotation mark, reverse solidus and U+0000 through U+001F; those are still
// escaped, and only those.
describe("jsonImage", () => {
  it("emits the literal characters and escapes only what RFC 8259 requires", () => {
    expect(jsonImage({ "a&b<c>": 'x&y<z>"\\\n' })).toBe('{"a&b<c>":"x&y<z>\\"\\\\\\n"}');
  });

  // The one emission this engine makes that is not a wire byte: the revision-2
  // routing transform expression, which carries artifact-derived parameter and
  // property names into a synthesized OBI. Its Go twin is
  // openbindings-go/formats/openapi/input_routes_v2.go.
  it("carries artifact-derived names literally into the routing transform", () => {
    const op = {
      parameters: [{ name: "a&b<c>", in: "query", schema: { type: "string" } }],
      requestBody: {
        required: true,
        content: { "application/json": { schema: { type: "object", properties: { "x&y": { type: "string" } } } } },
      },
    } as unknown as OpenAPIOperation;
    const plans = planRequestBodies(op, { profile: OPENAPI_PROFILE_FULL, openapiVersion: "3.1.1" });
    const routes = planAbstractInputRoutes(
      op.parameters as OpenAPIParameter[],
      plans,
      OPENAPI_PROFILE_FULL,
    );
    const expression = routes.transformExpression();
    expect(expression).toContain('"a&b<c>"');
    expect(expression).toContain('"x&y"');
    for (const codePoint of ["&", "<", ">"]) {
      // Built rather than written, so the six-character sequence cannot be
      // silently un-escaped by an editor on its way into this file.
      const escaped = `\\u${codePoint.codePointAt(0)!.toString(16).padStart(4, "0")}`;
      expect(expression, escaped).not.toContain(escaped);
    }
  });

  // Keeps the citation the convention rests on in front of anyone who changes
  // it: the pinned RFC 8259 digest appears in the engine's own doc comment, so
  // a reader can re-run the audit.
  it("documents the authority it rests on", () => {
    const source = readFileSync(new URL("./json-image.ts", import.meta.url), "utf8");
    for (const needle of [
      "RFC 8259 Section 7",
      "61a5378f4255c720beb2a4b4a63b29540147c140f36988bf086291989b4cd2d7",
      "SetEscapeHTML",
    ]) {
      expect(source, needle).toContain(needle);
    }
  });
});

// Establishes the path the parameter-content cells measure. The table calls the
// serializer that serializeParamContent wraps; the invoker reaches the SAME
// function through routeInput at the full profile, and the two profiles differ
// only in media-type parsing strictness, never in the JSON branch. Asserted by
// execution rather than by reading, because a table that measures a function
// the product does not call measures nothing.
describe("json image on the shipped parameter path", () => {
  it("percent-encodes the literal characters, never a six-character escape", () => {
    const p = {
      name: "address",
      in: "query",
      content: { "application/json": { schema: { type: "object" } } },
    } as unknown as OpenAPIParameter;
    const routed = routeInput(
      [p],
      { address: { street: "1 A&B <c> d" } },
      "/probe",
      null,
      OPENAPI_PROFILE_FULL,
    );
    expect(routed.queryUnits).toHaveLength(1);
    const unit = routed.queryUnits[0] as string;
    for (const want of ["%26", "%3C", "%3E"]) expect(unit, want).toContain(want);
    for (const codePoint of ["&", "<", ">"]) {
      const escaped = `%5Cu${codePoint.codePointAt(0)!.toString(16).padStart(4, "0")}`;
      expect(unit.toUpperCase(), escaped).not.toContain(escaped.toUpperCase());
    }
  });
});
