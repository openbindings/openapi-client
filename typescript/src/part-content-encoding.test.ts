import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, it } from "vitest";
import { buildMultipartBody, buildURLEncodedBody, planRequestBodies } from "./media.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";
import type { OpenAPIDocument, OpenAPIMediaType, OpenAPIOperation } from "./types.js";

// The identical file is executed by openbindings-go/formats/openapi, by
// openapi-client/go, and by openbindings-ts/packages/openapi against this
// package's BUILT dist; changing it in one engine without the others fails
// here.
export const PART_CONTENT_ENCODING_CASES_DIGEST =
  "06647fe967dbc2d7f6739fa718b79c1f7bb45bcc8ccc7faf4113836fb469b605";

export interface PartContentEncodingCase {
  name: string;
  openapi: string;
  media: string;
  part: string;
  contentEncoding: boolean;
  propertySchema: Record<string, unknown>;
  encodingContentType: string | null;
  propertyName: string;
  value: unknown;
  expect: string;
  basis: string;
}

export function loadPartContentEncodingCases(raw: Buffer): PartContentEncodingCase[] {
  const digest = createHash("sha256").update(raw).digest("hex");
  if (digest !== PART_CONTENT_ENCODING_CASES_DIGEST) {
    throw new Error(
      `case table digest = ${digest}, want ${PART_CONTENT_ENCODING_CASES_DIGEST} (the table is shared byte-for-byte with the twin engines)`,
    );
  }
  const table = JSON.parse(raw.toString("utf8")) as { cases: PartContentEncodingCase[] };
  if (table.cases.length === 0) throw new Error("case table is empty");
  return table.cases;
}

function bodyMedia(c: PartContentEncodingCase): OpenAPIMediaType {
  const media: Record<string, unknown> = {
    schema: { type: "object", properties: { [c.propertyName]: c.propertySchema } },
  };
  if (c.encodingContentType !== null) {
    media.encoding = { [c.propertyName]: { contentType: c.encodingContentType } };
  }
  return media as OpenAPIMediaType;
}

function operation(c: PartContentEncodingCase): OpenAPIOperation {
  return { requestBody: { required: true, content: { [c.media]: bodyMedia(c) } } } as OpenAPIOperation;
}

async function emission(c: PartContentEncodingCase): Promise<string> {
  const doc = { openapi: c.openapi } as OpenAPIDocument;
  const fields = { [c.propertyName]: c.value };
  try {
    if (c.media === "application/x-www-form-urlencoded") {
      const encoded = buildURLEncodedBody(bodyMedia(c), fields, true, c.openapi, false);
      return encoded === "" ? "elided" : encoded;
    }
    const form = buildMultipartBody(doc, bodyMedia(c), fields, true, false);
    const rendered: string[] = [];
    for (const entry of form.getAll(c.propertyName)) {
      if (typeof entry === "string") {
        // A bare FormData string field emits a part with NO Content-Type
        // header, which [RFC7578] Section 4.4 makes the same wire fact as an
        // explicit text/plain. The Go twins emit the header; both spellings
        // are inside the permitted set, so the rendering normalizes them
        // together, exactly as the array-items table does.
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

// Refusal messages are each implementation's own surface, so only the decision
// itself crosses the twin boundary.
export async function partContentEncodingDecision(c: PartContentEncodingCase): Promise<string> {
  try {
    planRequestBodies(operation(c), { profile: OPENAPI_PROFILE_FULL, openapiVersion: c.openapi });
  } catch {
    return "refused";
  }
  return `admitted;emit=${await emission(c)}`;
}

/**
 * The invariant the table exists for, stated as a claim in its own right: with
 * no Encoding Object, adding `contentEncoding` to a part schema changes the
 * part's decision ONLY where an accepted edition says it does — on the 3.1
 * line, for `type: string` (the encoded-string row) and for a typeless part
 * (whose 3.1.1/3.1.2 row is application/octet-stream, a boundary this revision
 * does not define); and nowhere at all under the 3.0 line, whose Schema Object
 * dialect does not carry the keyword.
 */
export async function assertContentEncodingChangesOnlyTheStringRow(
  cases: PartContentEncodingCase[],
): Promise<void> {
  // EXECUTED, not read off the table's own expectations: the claim is about
  // the engine, so a revert of the implementation has to turn this red too.
  const decisions = new Map<string, string>();
  for (const c of cases) {
    if (c.encodingContentType !== null) continue;
    decisions.set(c.name, await partContentEncodingDecision(c));
  }
  const editions = ["3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4", "3.1.0", "3.1.1", "3.1.2"];
  const kinds = ["string", "integer", "number", "boolean", "typeless", "array-of-string"];
  let pairs = 0;
  let moved = 0;
  for (const edition of editions) {
    const is31 = edition.startsWith("3.1");
    for (const media of ["multipart", "urlencoded"]) {
      for (const kind of kinds) {
        const plain = decisions.get(`${edition}|${media}|${kind}|plain`);
        const encoded = decisions.get(`${edition}|${media}|${kind}|contentEncoding`);
        if (plain === undefined || encoded === undefined) {
          throw new Error(`case table is missing the ${edition}|${media}|${kind} pair`);
        }
        pairs += 1;
        if (plain === encoded) continue;
        moved += 1;
        if (!(is31 && (kind === "string" || kind === "typeless"))) {
          throw new Error(
            `${edition}|${media}|${kind}: contentEncoding changed the decision (${plain} -> ${encoded}), but no accepted edition keys that kind's row on it`,
          );
        }
      }
    }
  }
  if (pairs !== 96) throw new Error(`compared ${pairs} pairs, want 96 (8 editions x 2 media x 6 kinds)`);
  // multipart `string` on each 3.1 edition, plus the typeless cell on both
  // media on each 3.1 edition. On the urlencoded lane a string part carries
  // its characters either way, so the row is not observable there.
  if (moved !== 9) throw new Error(`contentEncoding moved ${moved} of ${pairs} pairs, want 9`);
  for (const edition of ["3.1.0", "3.1.1", "3.1.2"]) {
    const plain = decisions.get(`${edition}|multipart|string|plain`) ?? "";
    const encoded = decisions.get(`${edition}|multipart|string|contentEncoding`) ?? "";
    if (!plain.includes("text/plain") || !encoded.includes("application/octet-stream")) {
      throw new Error(
        `${edition} multipart string: plain = ${plain}, encoded = ${encoded}; the encoded-string row is not being exercised`,
      );
    }
  }
}

describe("part content encoding — the twin case table", () => {
  const cases = loadPartContentEncodingCases(
    readFileSync(new URL("./testdata/part-content-encoding-cases.json", import.meta.url)),
  );

  for (const c of cases) {
    it(c.name, async () => {
      const got = await partContentEncodingDecision(c);
      if (got !== c.expect) {
        throw new Error(`${c.name}: decision = ${got}, want ${c.expect}\nbasis: ${c.basis}`);
      }
    });
  }

  it("lets contentEncoding change only the rows an accepted edition keys on it", async () => {
    await assertContentEncodingChangesOnlyTheStringRow(cases);
  });
});
