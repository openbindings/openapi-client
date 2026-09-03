import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { buildRequestBody, buildURLEncodedBody, finalizeRequestBody, planRequestBodies } from "./media.js";
import {
  buildResolvedMultipartBody,
  planResolvedRequestBodies,
  plansRequirePropertyMedia,
} from "./resolved-media.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";
import { loadOpenAPIDocument } from "./util.js";
import type { OpenAPIDocument, OpenAPIMediaType, OpenAPIOperation } from "./types.js";

// The identical file is executed by openbindings-go/formats/openapi, by
// openapi-client/go, and by openbindings-ts/packages/openapi against this
// package's BUILT dist; changing it in one engine without the others fails
// here.
export const PART_CONTENT_ENCODING_CASES_DIGEST =
  "a8812162d3d51b1d869821a733e29b47e240f8a8e00319c721d3fbdcdf52fa64";

export interface PartContentEncodingCase {
  name: string;
  openapi: string;
  media: string;
  lane: string;
  kind: string;
  keyword: string;
  declaresString: boolean;
  encodingContentType: string;
  propertyName: string;
  propertySchema: unknown;
  value: unknown;
  expect: string;
  basisKey: string;
}

export interface PartContentEncodingTable {
  bases: Record<string, string>;
  cases: PartContentEncodingCase[];
}

export function loadPartContentEncodingTable(raw: Buffer): PartContentEncodingTable {
  const digest = createHash("sha256").update(raw).digest("hex");
  if (digest !== PART_CONTENT_ENCODING_CASES_DIGEST) {
    throw new Error(
      `case table digest = ${digest}, want ${PART_CONTENT_ENCODING_CASES_DIGEST} (the table is shared byte-for-byte with the twin engines)`,
    );
  }
  const table = JSON.parse(raw.toString("utf8")) as PartContentEncodingTable;
  if (table.cases.length === 0) throw new Error("case table is empty");
  return table;
}

/**
 * Render one case as a WHOLE OpenAPI document. The document, and not a
 * hand-built media object, is what the engine has to be given: the shipped
 * loader normalizes the raw tree before anything downstream sees it, and a
 * harness that hands `media.ts` a literal object measures an engine the project
 * does not ship. The `$comment` in the shared table asserts exactly this of
 * every harness that executes it, this one included.
 */
function documentFor(c: PartContentEncodingCase): Record<string, unknown> {
  const media: Record<string, unknown> = {
    schema: { type: "object", properties: { [c.propertyName]: c.propertySchema } },
  };
  if (c.encodingContentType !== "") {
    media.encoding = { [c.propertyName]: { contentType: c.encodingContentType } };
  }
  return {
    openapi: c.openapi,
    info: { title: "part content encoding case table", version: "1.0.0" },
    paths: {
      "/form": {
        post: {
          operationId: "postForm",
          requestBody: { required: true, content: { [c.media]: media } },
          responses: { "200": { description: "ok" } },
        },
      },
    },
  };
}

async function emission(
  c: PartContentEncodingCase,
  doc: OpenAPIDocument,
  media: OpenAPIMediaType,
): Promise<string> {
  const fields = { [c.propertyName]: c.value };
  try {
    if (c.media === "application/x-www-form-urlencoded") {
      const encoded = buildURLEncodedBody(media, fields, true, c.openapi, false);
      return encoded === "" ? "elided" : encoded;
    }
    const form = buildResolvedMultipartBody(doc, media, fields, true, false);
    const rendered: string[] = [];
    for (const entry of form.getAll(c.propertyName)) {
      if (typeof entry === "string") {
        // A bare FormData string field emits a part with NO Content-Type
        // header, which [RFC7578] Section 4.4 makes the same wire fact as an
        // explicit text/plain. The Go twins emit the header; both spellings are
        // inside the permitted set, so the rendering normalizes them together.
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
  let doc: OpenAPIDocument;
  try {
    doc = await loadOpenAPIDocument(undefined, documentFor(c), { allowExternalRefs: false });
  } catch {
    return "source-refused";
  }
  const op = (doc as unknown as Record<string, any>).paths?.["/form"]?.post as OpenAPIOperation | undefined;
  if (!op) throw new Error(`${c.name}: loaded document has no form operation`);
  try {
    const plans = planResolvedRequestBodies(op, {
      profile: OPENAPI_PROFILE_FULL,
      openapiVersion: c.openapi,
    });
    if (plansRequirePropertyMedia(plans)) return "missing-required-choice";
  } catch {
    return "refused";
  }
  const media = (op.requestBody as any)?.content?.[c.media] as OpenAPIMediaType | undefined;
  if (!media) throw new Error(`${c.name}: loaded document has no ${c.media} media type`);
  return `admitted;emit=${await emission(c, doc, media)}`;
}

/**
 * The claim the table exists for, stated in its own right rather than left
 * implicit in 768 cells, and EXECUTED rather than read off the table's own
 * expectations: adding `contentEncoding` to a part changes its decision ONLY
 * where the part declares `type: string` — three of 192 pairs, being multipart
 * on each accepted 3.1 edition with no Encoding Object contentType, where the
 * text/plain row gives way to application/octet-stream. Adding
 * `contentMediaType` changes NO decision anywhere, which is 3.1.1 and 3.1.2
 * saying "the Encoding Object's contentType defaulting rules do not take the
 * Schema Object's contentMediaType into account" and [JSON Schema 2020-12]
 * Section 8.4 conditioning the keyword on a string instance.
 */
export async function assertContentEncodingChangesOnlyTheDeclaredStringRow(
  table: PartContentEncodingTable,
): Promise<{ encodingPairs: number; encodingChanged: number; mediaTypePairs: number; mediaTypeChanged: number }> {
  const plain = new Map<string, PartContentEncodingCase>();
  for (const c of table.cases) {
    if (c.keyword === "plain") {
      plain.set(`${c.openapi}|${c.media}|${c.kind}|${c.encodingContentType}`, c);
    }
  }
  let encodingPairs = 0;
  let encodingChanged = 0;
  let mediaTypePairs = 0;
  let mediaTypeChanged = 0;
  const offenders: string[] = [];
  for (const c of table.cases) {
    if (c.keyword !== "contentEncoding" && c.keyword !== "contentMediaType") continue;
    const base = plain.get(`${c.openapi}|${c.media}|${c.kind}|${c.encodingContentType}`);
    if (!base) throw new Error(`${c.name}: no |plain twin`);
    const changed = (await partContentEncodingDecision(c)) !== (await partContentEncodingDecision(base));
    if (c.keyword === "contentEncoding") {
      encodingPairs += 1;
      if (changed) {
        encodingChanged += 1;
        if (!c.declaresString) {
          throw new Error(`${c.name}: contentEncoding changed the decision on a part that declares no string`);
        }
      }
      continue;
    }
    mediaTypePairs += 1;
    if (changed) {
      mediaTypeChanged += 1;
      offenders.push(c.name);
    }
  }
  if (offenders.length > 0) {
    throw new Error(
      `contentMediaType changed the decision on ${offenders.length} cells, which no accepted edition's defaulting rules do: ${offenders.join(", ")}`,
    );
  }
  return { encodingPairs, encodingChanged, mediaTypePairs, mediaTypeChanged };
}

describe("part content-encoding case table", () => {
  const table = loadPartContentEncodingTable(
    readFileSync(new URL("./testdata/part-content-encoding-cases.json", import.meta.url)),
  );

  it("has the 768 shared cells", () => {
    expect(table.cases).toHaveLength(768);
  });

  for (const c of table.cases) {
    it(`${c.name} -> ${c.expect}`, async () => {
      const got = await partContentEncodingDecision(c);
      expect(got, table.bases[c.basisKey]).toBe(c.expect);
    });
  }

  // The one part header the shared table deliberately excludes, because it is
  // not reachable through FormData: Content-Transfer-Encoding. R5 (ratified
  // 2026-09-01) settles it for every declared type at once. OAS states the
  // `contentEncoding` ⇄ Content-Transfer-Encoding relation as an equivalence
  // describing what the declaration MEANS, not as an instruction to a sender,
  // and RFC 7578 Section 4.7 — incorporated by all three 3.x documents — says
  // "Senders SHOULD NOT generate any parts with a Content-Transfer-Encoding
  // header field". So no part carries the field regardless of the declared
  // type. Before R5 this lane emitted it and the Go twin did not; the ruling
  // closed the divergence in the direction RFC 7578 requires.
  it("never emits Content-Transfer-Encoding from contentEncoding", async () => {
    const doc = await loadOpenAPIDocument(undefined, {
      openapi: "3.1.1",
      info: { title: "content transfer encoding", version: "1.0.0" },
      paths: {
        "/form": {
          post: {
            operationId: "postForm",
            requestBody: {
              required: true,
              content: {
                "multipart/form-data": {
                  schema: {
                    type: "object",
                    properties: {
                      text: { type: "string", contentEncoding: "base64" },
                      count: { type: "integer", contentEncoding: "base64" },
                      shape: { type: "object", contentEncoding: "base64" },
                      many: { type: "array", items: { type: "string", contentEncoding: "base64" } },
                    },
                  },
                },
              },
            },
            responses: { "200": { description: "ok" } },
          },
        },
      },
    }, { allowExternalRefs: false });
    const op = (doc as unknown as Record<string, any>).paths["/form"].post as OpenAPIOperation;
    const plans = planRequestBodies(op, { profile: OPENAPI_PROFILE_FULL, openapiVersion: "3.1.1" });
    const plan = plans.find((candidate) => candidate.mediaType === "multipart/form-data")!;
    const wire = buildRequestBody(doc, plan, {
      bodySet: true,
      bodyValue: undefined,
      bodyFields: { text: "YWJj", count: 7, shape: { k: "v" }, many: ["YWJj"] },
    });
    const finalized = await finalizeRequestBody(wire);
    const bytes = finalized.body instanceof Uint8Array
      ? finalized.body
      : new Uint8Array(await new Response(finalized.body as BodyInit).arrayBuffer());
    const rendered = new TextDecoder().decode(bytes);
    expect(rendered).toContain('name="text"');
    expect(rendered.toLowerCase()).not.toContain("content-transfer-encoding");
  });

  it("lets contentEncoding change only the declared-string row", async () => {
    expect(await assertContentEncodingChangesOnlyTheDeclaredStringRow(table)).toEqual({
      encodingPairs: 192,
      encodingChanged: 3,
      mediaTypePairs: 192,
      mediaTypeChanged: 0,
    });
  });
});
