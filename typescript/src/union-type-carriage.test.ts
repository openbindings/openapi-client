import { describe, expect, it } from "vitest";
import { buildMultipartBody, buildURLEncodedBody, planRequestBodies } from "./media.js";
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
//   - The contentEncoding column stopped changing any cell whose collapsed
//     type is not `string` (2026-08-17). Every 3.1 edition scopes the
//     encoded-string row to `type: string` — 3.1.0 §4.8.14.5 "a `type:
//     string` with a `contentEncoding`", the 3.1.1/3.1.2 §4.8.15.1.1 table
//     rows — and 3.1.1/3.1.2 add in their own note that "an n/a in the
//     contentEncoding column means that the presence or value of
//     contentEncoding is irrelevant", n/a being what the
//     number/integer/boolean, object and array rows hold. So `integer-null`,
//     `object-null` and `array-null` now read the same in both columns. A
//     part that declares NO type is a different cell and still refuses:
//     3.1.1/3.1.2 give it application/octet-stream, for which this revision
//     defines no JSON-to-octet part boundary.

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
  try {
    planRequestBodies(operation(media, part), {
      profile: OPENAPI_PROFILE_FULL,
      openapiVersion: edition,
    });
  } catch {
    return "refused";
  }
  const value = await emission(edition, media, part, probeValue(base));
  const nulled = await emission(edition, media, part, null);
  return `admitted;value=${value};null=${nulled}`;
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
  it("reads the literal boolean true as the structural spelling does", async () => {
    for (const edition of EDITIONS) {
      for (const media of MEDIAS) {
        const literal = await decision(edition, media, true as unknown as Record<string, unknown>, false);
        const structural = await decision(edition, media, { anyOf: [{}, { not: {} }] }, false);
        expect(literal).toBe(structural);
      }
    }
  });
});

const EXPECTED: Record<string, string> = {
  "3.0.4|application/x-www-form-urlencoded|absent-type|contentEncoding": "admitted;value=p=x;null=error",
  "3.0.4|application/x-www-form-urlencoded|absent-type|plain": "admitted;value=p=x;null=error",
  "3.0.4|application/x-www-form-urlencoded|array-null|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|array-null|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|boolean-true|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|boolean-true|plain": "admitted;value=p=x;null=error",
  "3.0.4|application/x-www-form-urlencoded|empty-array|contentEncoding": "admitted;value=p=x;null=error",
  "3.0.4|application/x-www-form-urlencoded|empty-array|plain": "admitted;value=p=x;null=error",
  "3.0.4|application/x-www-form-urlencoded|integer-null|contentEncoding": "refused",
  "3.0.4|application/x-www-form-urlencoded|integer-null|plain": "refused",
  "3.0.4|application/x-www-form-urlencoded|memberless|contentEncoding": "admitted;value=p=x;null=error",
  "3.0.4|application/x-www-form-urlencoded|memberless|plain": "admitted;value=p=x;null=error",
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
  "3.0.4|multipart/form-data|absent-type|contentEncoding": "admitted;value=text/plain:x;null=error",
  "3.0.4|multipart/form-data|absent-type|plain": "admitted;value=text/plain:x;null=error",
  "3.0.4|multipart/form-data|array-null|contentEncoding": "refused",
  "3.0.4|multipart/form-data|array-null|plain": "refused",
  "3.0.4|multipart/form-data|boolean-true|contentEncoding": "refused",
  "3.0.4|multipart/form-data|boolean-true|plain": "admitted;value=text/plain:x;null=error",
  "3.0.4|multipart/form-data|empty-array|contentEncoding": "admitted;value=text/plain:x;null=error",
  "3.0.4|multipart/form-data|empty-array|plain": "admitted;value=text/plain:x;null=error",
  "3.0.4|multipart/form-data|integer-null|contentEncoding": "refused",
  "3.0.4|multipart/form-data|integer-null|plain": "refused",
  "3.0.4|multipart/form-data|memberless|contentEncoding": "admitted;value=text/plain:x;null=error",
  "3.0.4|multipart/form-data|memberless|plain": "admitted;value=text/plain:x;null=error",
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
  "3.1.2|application/x-www-form-urlencoded|absent-type|plain": "admitted;value=p=x;null=error",
  "3.1.2|application/x-www-form-urlencoded|array-null|contentEncoding": "admitted;value=p=%5B%22a%22%5D;null=elided",
  "3.1.2|application/x-www-form-urlencoded|array-null|plain": "admitted;value=p=%5B%22a%22%5D;null=elided",
  "3.1.2|application/x-www-form-urlencoded|boolean-true|contentEncoding": "refused",
  "3.1.2|application/x-www-form-urlencoded|boolean-true|plain": "admitted;value=p=x;null=error",
  "3.1.2|application/x-www-form-urlencoded|empty-array|contentEncoding": "refused",
  "3.1.2|application/x-www-form-urlencoded|empty-array|plain": "admitted;value=p=x;null=error",
  "3.1.2|application/x-www-form-urlencoded|integer-null|contentEncoding": "admitted;value=p=7;null=elided",
  "3.1.2|application/x-www-form-urlencoded|integer-null|plain": "admitted;value=p=7;null=elided",
  "3.1.2|application/x-www-form-urlencoded|memberless|contentEncoding": "refused",
  "3.1.2|application/x-www-form-urlencoded|memberless|plain": "admitted;value=p=x;null=error",
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
  "3.1.2|multipart/form-data|absent-type|contentEncoding": "refused",
  "3.1.2|multipart/form-data|absent-type|plain": "admitted;value=text/plain:x;null=error",
  "3.1.2|multipart/form-data|array-null|contentEncoding": "admitted;value=text/plain:a;null=elided",
  "3.1.2|multipart/form-data|array-null|plain": "admitted;value=text/plain:a;null=elided",
  "3.1.2|multipart/form-data|boolean-true|contentEncoding": "refused",
  "3.1.2|multipart/form-data|boolean-true|plain": "admitted;value=text/plain:x;null=error",
  "3.1.2|multipart/form-data|empty-array|contentEncoding": "refused",
  "3.1.2|multipart/form-data|empty-array|plain": "admitted;value=text/plain:x;null=error",
  "3.1.2|multipart/form-data|integer-null|contentEncoding": "admitted;value=text/plain:7;null=elided",
  "3.1.2|multipart/form-data|integer-null|plain": "admitted;value=text/plain:7;null=elided",
  "3.1.2|multipart/form-data|memberless|contentEncoding": "refused",
  "3.1.2|multipart/form-data|memberless|plain": "admitted;value=text/plain:x;null=error",
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
