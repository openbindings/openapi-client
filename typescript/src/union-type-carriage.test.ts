import { describe, expect, it } from "vitest";
import { buildMultipartBody, buildURLEncodedBody } from "./media.js";
import { planResolvedRequestBodies, plansRequirePropertyMedia } from "./resolved-media.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";
import type { OpenAPIDocument, OpenAPIMediaType, OpenAPIOperation } from "./types.js";

// The union-type carriage table. Every `type` spelling a Schema Object can
// present, crossed with the two form media types and both accepted OAS
// lines, decided end to end: is the candidate admitted, what does one part
// carry, and what does a JSON null do.
//
// This is the SAME table the Go twins assert
// (openbindings-go/formats/openapi/union_type_carriage_expectations_test.go
// and openapi-client/go/union_type_carriage_expectations_test.go), cell for
// cell and string for string. Two engines that both assert it cannot drift
// on the question silently, which is the point: the defect it exists to pin
// was two engines reading `{"type": ["string", "null"]}` differently — Go as
// a typeless schema with no part carriage, TypeScript as every one of its
// members at once.
//
// Authority for the expectations:
//   - JSON Schema 2020-12 §6.1.1 — an array-valued `type` is a union: "an
//     instance validates successfully if its type matches any of the types
//     indicated by the strings in the array". A union with one non-"null"
//     member asserts exactly what the collapsing anyOf/oneOf spelling
//     asserts, so it takes the same §9.2 collapse; two or more non-null
//     members declare value-dependent alternatives and refuse.
//   - OAS 3.0.0–3.0.4, Schema Object — "type - Value MUST be a string.
//     Multiple types via an array are not supported." Under that line an
//     array-valued `type` is not a union declaration at all, and every
//     multi-member array spelling refuses.
//   - EIGHT cells moved from admitted to refused on 2026-08-17, all at 3.1.2,
//     all in the |plain column: `absent-type`, `memberless` and `boolean-true`
//     (the structural true spelling) declare no `type` at all, and every
//     accepted 3.1 edition states that part's default Content-Type as
//     application/octet-stream -- 3.1.1 and 3.1.2 as the Encoding Object
//     default table's `type`-absent first row, 3.1.0 through the total
//     catch-all closing its prose enumeration -- which this revision defines
//     no JSON-to-octet part boundary to cross. `empty-array` refuses on a
//     narrower ground: its `type` is present, so no stated row reaches it at
//     all, and JSON Schema 2020-12's meta-schema requires an array-valued
//     `type` to carry at least one member. Those eight stayed admitted at
//     3.0.4 until 2026-08-20, where no stated row reached a declaration
//     carrying no `type` and openbindings.openapi@1 Section 9.2's own
//     convention answered.
//   - FOURTEEN cells moved from admitted to refused on 2026-08-20 (stage-3
//     block 5, escalation M2), all at 3.0.4: `absent-type`, `memberless`,
//     `empty-array` and `boolean-true|plain`, on both media. The 3.0-line
//     value-keyed convention is deleted from Section 9.2, so a resolved part
//     schema declaring no `type` refuses on EVERY accepted edition -- the
//     3.1 editions state a default this revision defines no boundary to
//     cross, the 3.0 editions state no row at all and this revision authors
//     none. Each of the fourteen now reads exactly as its 3.1.2 twin above,
//     which is what one rule governing both lines means.

const SPELLINGS: Array<[string, Record<string, unknown>]> = [
  ["string", { type: "string" }],
  ["string-array-1", { type: ["string"] }],
  ["string-null", { type: ["string", "null"] }],
  ["null-string", { type: ["null", "string"] }],
  ["array-null", { type: ["array", "null"], items: { type: "string" } }],
  ["object-null", { type: ["object", "null"] }],
  ["integer-null", { type: ["integer", "null"] }],
  ["string-object", { type: ["string", "object"] }],
  ["string-integer", { type: ["string", "integer"] }],
  ["null-only", { type: ["null"] }],
  ["empty-array", { type: [] }],
  ["absent-type", { description: "probe" }],
  ["memberless", {}],
  ["boolean-true", { anyOf: [{}, { not: {} }] }],
];

const EDITIONS = ["3.0.4", "3.1.2"];
const MEDIAS = ["multipart/form-data", "application/x-www-form-urlencoded"];

/**
 * The canonical value for a spelling: whatever JSON type the declaration's
 * single non-null member admits. Deterministic so the twins probe
 * identically.
 */
function probeValue(schema: Record<string, unknown>): unknown {
  const encoded = JSON.stringify(schema);
  if (encoded.includes(`"object"`)) return { k: "v" };
  if (encoded.includes(`"array"`)) return ["a"];
  if (encoded.includes(`"integer"`)) return 7;
  if (encoded.includes(`"boolean"`)) return true;
  return "x";
}

function partSchema(base: Record<string, unknown>, encoded: boolean): Record<string, unknown> {
  return encoded ? { ...base, contentEncoding: "base64" } : { ...base };
}

function bodyMedia(part: Record<string, unknown>): OpenAPIMediaType {
  return { schema: { type: "object", properties: { p: part } } };
}

function operation(media: string, part: Record<string, unknown>): OpenAPIOperation {
  return { requestBody: { required: true, content: { [media]: bodyMedia(part) } } };
}

async function emission(
  edition: string,
  media: string,
  part: Record<string, unknown>,
  value: unknown,
): Promise<string> {
  const doc: OpenAPIDocument = { openapi: edition };
  try {
    if (media === "application/x-www-form-urlencoded") {
      const encoded = buildURLEncodedBody(bodyMedia(part), { p: value }, true, edition, false);
      return encoded === "" ? "elided" : encoded;
    }
    const form = buildMultipartBody(doc, bodyMedia(part), { p: value }, true, false);
    const rendered: string[] = [];
    for (const entry of form.getAll("p")) {
      if (typeof entry === "string") {
        // A bare FormData string field is the text/plain part with no
        // parameters; the Go twin reads the same wire fact off the emitted
        // part's Content-Type header.
        rendered.push(`text/plain:${entry}`);
      } else {
        rendered.push(`${entry.type}:${await entry.text()}`);
      }
    }
    return rendered.length === 0 ? "elided" : rendered.join("&");
  } catch {
    return "error";
  }
}

async function decision(
  edition: string,
  media: string,
  base: Record<string, unknown>,
  encoded: boolean,
): Promise<string> {
  const part = partSchema(base, encoded);
  let plans;
  try {
    plans = planResolvedRequestBodies(operation(media, part), {
      profile: OPENAPI_PROFILE_FULL,
      openapiVersion: edition,
    });
  } catch {
    return "refused";
  }
  // R4: a cell whose item-type default defines no serialization for the
  // container is dispatchable once one `propertyMedia` choice is supplied.
  // This table supplies none, so the cell is reported as the required choice.
  if (plansRequirePropertyMedia(plans)) return "missing-required-choice";
  const value = await emission(edition, media, part, probeValue(base));
  const nulled = await emission(edition, media, part, null);
  return `admitted;value=${value};null=${nulled}`;
}

/**
 * The boolean-literal probe, kept off `partSchema` on purpose: that helper
 * spreads its argument into an object, which turns `true` into `{}` and loses
 * the very declaration this probe is about.
 */
async function booleanLiteralDecision(edition: string, media: string): Promise<string> {
  const bodyMedia = { schema: { type: "object", properties: { p: true } } } as unknown as OpenAPIMediaType;
  const op = { requestBody: { required: true, content: { [media]: bodyMedia } } } as OpenAPIOperation;
  let plans;
  try {
    plans = planResolvedRequestBodies(op, { profile: OPENAPI_PROFILE_FULL, openapiVersion: edition });
  } catch {
    return "refused";
  }
  if (plansRequirePropertyMedia(plans)) return "missing-required-choice";
  const doc: OpenAPIDocument = { openapi: edition };
  const render = async (value: unknown): Promise<string> => {
    try {
      if (media === "application/x-www-form-urlencoded") {
        const encoded = buildURLEncodedBody(bodyMedia, { p: value }, true, edition, false);
        return encoded === "" ? "elided" : encoded;
      }
      const form = buildMultipartBody(doc, bodyMedia, { p: value }, true, false);
      const parts: string[] = [];
      for (const entry of form.getAll("p")) {
        parts.push(typeof entry === "string" ? `text/plain:${entry}` : `${entry.type}:${await entry.text()}`);
      }
      return parts.length === 0 ? "elided" : parts.join("&");
    } catch {
      return "error";
    }
  };
  return `admitted;value=${await render("x")};null=${await render(null)}`;
}

describe("union-type carriage — the twin case table", () => {
  it("decides every type spelling identically to the Go twins", async () => {
    const got: Record<string, string> = {};
    for (const edition of EDITIONS) {
      for (const media of MEDIAS) {
        for (const [name, schema] of SPELLINGS) {
          for (const encoded of [false, true]) {
            const key = `${edition}|${media}|${name}|${encoded ? "contentEncoding" : "plain"}`;
            got[key] = await decision(edition, media, schema, encoded);
          }
        }
      }
    }
    expect(Object.keys(got).length).toBe(Object.keys(EXPECTED).length);
    expect(got).toEqual(EXPECTED);
  });

  // The literal boolean `true` and its structural encoding are the same
  // unconstrained declaration; the shared table carries the structural
  // spelling because that is the only one the Go Schema Object can hold.
  // Repaired 2026-09-01. This assertion previously read a boolean `true`
  // through `partSchema`, whose object spread turns `true` into `{}` — so it
  // compared a typeless object schema against the structural spelling and
  // never exercised a boolean literal at all. It now passes the literal
  // through and asserts what the documents say about it: on the 3.0 line a
  // boolean-literal Schema Object is outside the closed 3.0 dialect and
  // refuses on both lanes; on the 3.1 line it is the always-true schema, so a
  // multipart part is typeless and takes the application/octet-stream default
  // while the urlencoded lane, which has no octet boundary, refuses.
  it("reads a boolean-literal property under each line's own dialect", async () => {
    const got: Record<string, string> = {};
    for (const edition of EDITIONS) {
      for (const media of MEDIAS) {
        got[`${edition}|${media}`] = await booleanLiteralDecision(edition, media);
      }
    }
    expect(got).toEqual({
      "3.0.4|multipart/form-data": "refused",
      "3.0.4|application/x-www-form-urlencoded": "refused",
      "3.1.2|multipart/form-data": "admitted;value=error;null=error",
      "3.1.2|application/x-www-form-urlencoded": "refused",
    });
  });
});

// The frozen twin table. The identical expectations are asserted by both Go
// engines in union_type_carriage_expectations_test.go, whose header carries
// the per-row basis.
//
// FIVE cells moved from refused to admitted on 2026-08-17 — 3.1.2 x
// {integer-null, object-null} x both media, plus
// 3.1.2|urlencoded|array-null|contentEncoding. A `contentEncoding` sibling on
// a union that collapses to a NON-string member is inert: [JSON Schema
// 2020-12] Section 8.1 makes the Content vocabulary annotations that "do not
// function as validation assertions" and Section 8.3 conditions the keyword on
// a string instance, while 3.1.1 and 3.1.2 hold `n/a` in the contentEncoding
// column of the `number, integer, or boolean`, `object` and `array` rows —
// "the presence or value of contentEncoding is irrelevant", in the table's own
// words. Each of the five now reads exactly as its |plain twin.
  // R4 (ratified 2026-09-01): TWO cells moved from admitted to
  // missing-required-choice — 3.1.2|urlencoded|array-null on both spellings.
  // A `type: ["array", "null"]` property collapses to `array`, and on the
  // content lane the whole array rides one field. The editions derive an
  // array's default from its ITEMS, here `text/plain`, under which no edition
  // states an array's bytes; the prior `application/json` emission read the
  // default off the container instead. The remedy is the §9.3 `propertyMedia`
  // choice, which this table supplies for no cell. The multipart sibling is
  // unaffected: there the array expands into one part per item.
  //
  // NINE cells were corrected on 2026-09-01. The table calls itself shared
  // byte-for-byte with the Go twins, and it was not: this copy carried
  // `refused` where the Go copy carried `missing-required-choice` (3.0.4
  // multipart typeless, which the 3.0 document's section 9.3 configuration
  // point makes a required choice) and `admitted;value=error` (3.1.2 multipart
  // typeless, where the octet-stream default admits the source and the probe
  // value fails the canonical-Base64 boundary at emission). The divergence
  // survived because this runner planned through the BASE planner, which does
  // not carry the resolved view's propertyMedia facts, so the engine agreed
  // with the wrong expectation. The runner now plans through
  // planResolvedRequestBodies, as its Go twins always did, and the engines
  // agree on all 112 cells.
const EXPECTED: Record<string, string> = {
  "3.0.4|application/x-www-form-urlencoded|absent-type|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|absent-type|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|array-null|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|array-null|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|boolean-true|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|boolean-true|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|empty-array|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|empty-array|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|integer-null|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|integer-null|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|memberless|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|memberless|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|null-only|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|null-only|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|null-string|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|null-string|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|object-null|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|object-null|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|string-array-1|contentEncoding": "admitted;value=p=x;null=p=",
  "3.0.4|application/x-www-form-urlencoded|string-array-1|plain": "admitted;value=p=x;null=p=",
  "3.0.4|application/x-www-form-urlencoded|string-integer|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|string-integer|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|string-null|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|string-null|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|string-object|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|string-object|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|string|contentEncoding": "admitted;value=p=x;null=p=",
  "3.0.4|application/x-www-form-urlencoded|string|plain": "admitted;value=p=x;null=p=",
  "3.0.4|multipart/form-data|absent-type|contentEncoding": "missing-required-choice",
  "3.0.4|multipart/form-data|absent-type|plain": "missing-required-choice",
  "3.0.4|multipart/form-data|array-null|contentEncoding": "refused",
  "3.0.4|multipart/form-data|array-null|plain": "refused",
  "3.0.4|multipart/form-data|boolean-true|contentEncoding": "refused",
  "3.0.4|multipart/form-data|boolean-true|plain": "refused",
  "3.0.4|multipart/form-data|empty-array|contentEncoding": "refused",
  "3.0.4|multipart/form-data|empty-array|plain": "refused",
  "3.0.4|multipart/form-data|integer-null|contentEncoding": "refused",
  "3.0.4|multipart/form-data|integer-null|plain": "refused",
  "3.0.4|multipart/form-data|memberless|contentEncoding": "missing-required-choice",
  "3.0.4|multipart/form-data|memberless|plain": "missing-required-choice",
  "3.0.4|multipart/form-data|null-only|contentEncoding": "refused",
  "3.0.4|multipart/form-data|null-only|plain": "refused",
  "3.0.4|multipart/form-data|null-string|contentEncoding": "refused",
  "3.0.4|multipart/form-data|null-string|plain": "refused",
  "3.0.4|multipart/form-data|object-null|contentEncoding": "refused",
  "3.0.4|multipart/form-data|object-null|plain": "refused",
  "3.0.4|multipart/form-data|string-array-1|contentEncoding": "admitted;value=text/plain:x;null=text/plain:",
  "3.0.4|multipart/form-data|string-array-1|plain": "admitted;value=text/plain:x;null=text/plain:",
  "3.0.4|multipart/form-data|string-integer|contentEncoding": "refused",
  "3.0.4|multipart/form-data|string-integer|plain": "refused",
  "3.0.4|multipart/form-data|string-null|contentEncoding": "refused",
  "3.0.4|multipart/form-data|string-null|plain": "refused",
  "3.0.4|multipart/form-data|string-object|contentEncoding": "refused",
  "3.0.4|multipart/form-data|string-object|plain": "refused",
  "3.0.4|multipart/form-data|string|contentEncoding": "admitted;value=text/plain:x;null=text/plain:",
  "3.0.4|multipart/form-data|string|plain": "admitted;value=text/plain:x;null=text/plain:",
  "3.1.2|application/x-www-form-urlencoded|absent-type|contentEncoding": "refused",
  "3.1.2|application/x-www-form-urlencoded|absent-type|plain": "refused",
  "3.1.2|application/x-www-form-urlencoded|array-null|contentEncoding": "missing-required-choice",
  "3.1.2|application/x-www-form-urlencoded|array-null|plain": "missing-required-choice",
  "3.1.2|application/x-www-form-urlencoded|boolean-true|contentEncoding": "refused",
  "3.1.2|application/x-www-form-urlencoded|boolean-true|plain": "refused",
  "3.1.2|application/x-www-form-urlencoded|empty-array|contentEncoding": "refused",
  "3.1.2|application/x-www-form-urlencoded|empty-array|plain": "refused",
  "3.1.2|application/x-www-form-urlencoded|integer-null|contentEncoding": "admitted;value=p=7;null=elided",
  "3.1.2|application/x-www-form-urlencoded|integer-null|plain": "admitted;value=p=7;null=elided",
  "3.1.2|application/x-www-form-urlencoded|memberless|contentEncoding": "refused",
  "3.1.2|application/x-www-form-urlencoded|memberless|plain": "refused",
  "3.1.2|application/x-www-form-urlencoded|null-only|contentEncoding": "refused",
  "3.1.2|application/x-www-form-urlencoded|null-only|plain": "refused",
  "3.1.2|application/x-www-form-urlencoded|null-string|contentEncoding": "admitted;value=p=x;null=elided",
  "3.1.2|application/x-www-form-urlencoded|null-string|plain": "admitted;value=p=x;null=elided",
  "3.1.2|application/x-www-form-urlencoded|object-null|contentEncoding": "admitted;value=p=%7B%22k%22%3A%22v%22%7D;null=elided",
  "3.1.2|application/x-www-form-urlencoded|object-null|plain": "admitted;value=p=%7B%22k%22%3A%22v%22%7D;null=elided",
  "3.1.2|application/x-www-form-urlencoded|string-array-1|contentEncoding": "admitted;value=p=x;null=error",
  "3.1.2|application/x-www-form-urlencoded|string-array-1|plain": "admitted;value=p=x;null=p=",
  "3.1.2|application/x-www-form-urlencoded|string-integer|contentEncoding": "refused",
  "3.1.2|application/x-www-form-urlencoded|string-integer|plain": "refused",
  "3.1.2|application/x-www-form-urlencoded|string-null|contentEncoding": "admitted;value=p=x;null=elided",
  "3.1.2|application/x-www-form-urlencoded|string-null|plain": "admitted;value=p=x;null=elided",
  "3.1.2|application/x-www-form-urlencoded|string-object|contentEncoding": "refused",
  "3.1.2|application/x-www-form-urlencoded|string-object|plain": "refused",
  "3.1.2|application/x-www-form-urlencoded|string|contentEncoding": "admitted;value=p=x;null=error",
  "3.1.2|application/x-www-form-urlencoded|string|plain": "admitted;value=p=x;null=p=",
  "3.1.2|multipart/form-data|absent-type|contentEncoding": "admitted;value=error;null=error",
  "3.1.2|multipart/form-data|absent-type|plain": "admitted;value=error;null=error",
  "3.1.2|multipart/form-data|array-null|contentEncoding": "admitted;value=text/plain:a;null=elided",
  "3.1.2|multipart/form-data|array-null|plain": "admitted;value=text/plain:a;null=elided",
  "3.1.2|multipart/form-data|boolean-true|contentEncoding": "refused",
  // KNOWN TWIN DIVERGENCE, left open deliberately on 2026-09-01. This engine
  // refuses; the Go engines admit and reach an emission error. Section 5.2 of
  // both 3.x documents skips only a branch "whose resolved declaration
  // declares only `null`" and supplies a single resolved member "only when
  // exactly one candidate remains", and `{}` and `{not: {}}` are two
  // candidates, so refusing is the reading the documents state. The Go side is
  // also internally inconsistent here: its |contentEncoding twin refuses,
  // which its own header rule says cannot differ. Fixing it touches Go's
  // choice-resolution for every ambiguous branch and is queued as its own
  // change rather than folded into R4.
  "3.1.2|multipart/form-data|boolean-true|plain": "refused",
  "3.1.2|multipart/form-data|empty-array|contentEncoding": "refused",
  "3.1.2|multipart/form-data|empty-array|plain": "refused",
  "3.1.2|multipart/form-data|integer-null|contentEncoding": "admitted;value=text/plain:7;null=elided",
  "3.1.2|multipart/form-data|integer-null|plain": "admitted;value=text/plain:7;null=elided",
  "3.1.2|multipart/form-data|memberless|contentEncoding": "admitted;value=error;null=error",
  "3.1.2|multipart/form-data|memberless|plain": "admitted;value=error;null=error",
  "3.1.2|multipart/form-data|null-only|contentEncoding": "refused",
  "3.1.2|multipart/form-data|null-only|plain": "refused",
  "3.1.2|multipart/form-data|null-string|contentEncoding": "admitted;value=application/octet-stream:x;null=elided",
  "3.1.2|multipart/form-data|null-string|plain": "admitted;value=text/plain:x;null=elided",
  "3.1.2|multipart/form-data|object-null|contentEncoding": "admitted;value=application/json:{\"k\":\"v\"};null=elided",
  "3.1.2|multipart/form-data|object-null|plain": "admitted;value=application/json:{\"k\":\"v\"};null=elided",
  "3.1.2|multipart/form-data|string-array-1|contentEncoding": "admitted;value=application/octet-stream:x;null=error",
  "3.1.2|multipart/form-data|string-array-1|plain": "admitted;value=text/plain:x;null=text/plain:",
  "3.1.2|multipart/form-data|string-integer|contentEncoding": "refused",
  "3.1.2|multipart/form-data|string-integer|plain": "refused",
  "3.1.2|multipart/form-data|string-null|contentEncoding": "admitted;value=application/octet-stream:x;null=elided",
  "3.1.2|multipart/form-data|string-null|plain": "admitted;value=text/plain:x;null=elided",
  "3.1.2|multipart/form-data|string-object|contentEncoding": "refused",
  "3.1.2|multipart/form-data|string-object|plain": "refused",
  "3.1.2|multipart/form-data|string|contentEncoding": "admitted;value=application/octet-stream:x;null=error",
  "3.1.2|multipart/form-data|string|plain": "admitted;value=text/plain:x;null=text/plain:",
};
