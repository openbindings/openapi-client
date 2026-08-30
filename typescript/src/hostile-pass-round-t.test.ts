import { describe, expect, it } from "vitest";
import { OpenAPIClient, OpenAPIClientError } from "./client.js";
import {
  serializeHeaderValue,
  validateParameterSerialization,
  validateResolvedParameterSerialization,
} from "./params.js";
import { jsonCarriesLoneSurrogate } from "./util.js";
import type { OpenAPIDocument } from "./types.js";

// Regressions for the OpenAPI hostile-pass engine round (adjudication
// 2026-08-29). Each case pins a rule the merged sibling documents state and
// the shipped engine did not implement.

// ---------------------------------------------------------------------------
// Style-table admission reads the RESOLVED declaration (3.1#7 / P1-D)
// ---------------------------------------------------------------------------

describe("compound-capable style admission consults the resolved declaration", () => {
  const parameter = {
    name: "q",
    in: "query",
    style: "spaceDelimited",
    explode: false,
    schema: { anyOf: [{ type: "array", items: { type: "string" } }, { type: "null" }] },
  };

  it("admits an anyOf whose only non-null-declaring branch is an array", () => {
    expect(() => validateResolvedParameterSerialization(parameter)).not.toThrow();
  });

  it("the raw-schema reading cannot see the branch and is not the invoke-path gate", () => {
    // Kept as the documented contrast: `parameterSchemaTypes` reads direct
    // `type` plus `allOf` only, so the compound branch is invisible to it.
    expect(() => validateParameterSerialization(parameter)).toThrow(/spaceDelimited/u);
  });

  it("still rejects a resolved declaration that proves no compound member", () => {
    expect(() => validateResolvedParameterSerialization({
      name: "q", in: "query", style: "spaceDelimited", explode: false, schema: { type: "string" },
    })).toThrow(/arrays or objects/u);
  });
});

// ---------------------------------------------------------------------------
// Header values are carried as UTF-8 octets (A16 / P1-I)
// ---------------------------------------------------------------------------

const OCTETS = (value: string): string[] =>
  [...value].map((character) => character.charCodeAt(0).toString(16).padStart(2, "0"));

describe("a supplied header value is carried as UTF-8 octets", () => {
  it("U+00E9 leaves as C3 A9, not as the Latin-1 octet E9", () => {
    expect(OCTETS(serializeHeaderValue("café", "simple", false))).toEqual(["63", "61", "66", "c3", "a9"]);
  });

  it("carries a code point the substrate's ByteString conversion would reject outright", () => {
    expect(OCTETS(serializeHeaderValue("中", "simple", false))).toEqual(["e4", "b8", "ad"]);
    expect(OCTETS(serializeHeaderValue("\u{1F600}", "simple", false))).toEqual(["f0", "9f", "98", "80"]);
  });

  it("leaves an all-ASCII value byte-identical", () => {
    expect(serializeHeaderValue("plain-token", "simple", false)).toBe("plain-token");
  });

  it("reaches the wire through the client's request headers", async () => {
    let sent: string | null = null;
    const document: OpenAPIDocument = {
      openapi: "3.1.0",
      info: { title: "Header octets", version: "1" },
      servers: [{ url: "https://api.example" }],
      paths: {
        "/notes": {
          get: {
            operationId: "notes",
            parameters: [{ name: "X-Note", in: "header", schema: { type: "string" } }],
            responses: { "204": { description: "ok" } },
          },
        },
      },
    } as unknown as OpenAPIDocument;
    const client = await OpenAPIClient.load(document, {
      fetch: async (input, init) => {
        sent = new Headers(input instanceof Request ? input.headers : init?.headers).get("X-Note");
        return new Response(null, { status: 204 });
      },
    });
    await client.operation("notes").call({ parameters: { header: { "X-Note": "café" } } });
    expect(OCTETS(sent ?? "")).toEqual(["63", "61", "66", "c3", "a9"]);
  });
});

// ---------------------------------------------------------------------------
// Response JSON strictness: BOM ignored (A3b / P1-G), lone surrogate is loud
// (A3d / P1-H)
// ---------------------------------------------------------------------------

function jsonResponseDocument(): OpenAPIDocument {
  return {
    openapi: "3.1.0",
    info: { title: "JSON lane", version: "1" },
    servers: [{ url: "https://api.example" }],
    paths: {
      "/x": {
        get: {
          operationId: "x",
          responses: {
            "200": { description: "ok", content: { "application/json": { schema: {} } } },
          },
        },
      },
    },
  } as unknown as OpenAPIDocument;
}

async function readJSONBody(body: Uint8Array): Promise<unknown> {
  const client = await OpenAPIClient.load(jsonResponseDocument(), {
    fetch: async () => new Response(body as unknown as BodyInit, {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  });
  const result = await client.operation("x").call({});
  return (result as { data: unknown }).data;
}

describe("strict JSON response profile", () => {
  it("ignores a leading byte-order mark", async () => {
    const body = new Uint8Array([0xef, 0xbb, 0xbf, ...new TextEncoder().encode('{"a":1}')]);
    await expect(readJSONBody(body)).resolves.toEqual({ a: 1 });
  });

  it("makes a lone high-surrogate escape a loud protocol error", async () => {
    const body = new TextEncoder().encode('{"a":"\\ud800"}');
    await expect(readJSONBody(body)).rejects.toBeInstanceOf(OpenAPIClientError);
  });

  it("makes a lone low-surrogate escape a loud protocol error", async () => {
    const body = new TextEncoder().encode('{"a":"\\udc00"}');
    await expect(readJSONBody(body)).rejects.toBeInstanceOf(OpenAPIClientError);
  });

  it("still decodes a well-formed surrogate pair", async () => {
    const body = new TextEncoder().encode('{"a":"\\ud83d\\ude00"}');
    await expect(readJSONBody(body)).resolves.toEqual({ a: "\u{1F600}" });
  });

  it("detects an unpaired surrogate in an object member name", () => {
    expect(jsonCarriesLoneSurrogate('{"\\ud800":1}', JSON.parse('{"\\ud800":1}'))).toBe(true);
  });

  it("skips the value walk when the source carries no surrogate escape", () => {
    expect(jsonCarriesLoneSurrogate('{"a":"plain"}', { a: "plain" })).toBe(false);
  });
});
