import { readFileSync } from "node:fs";
import { describe, expect, it, vi } from "vitest";
import { OpenAPIClient, type OpenAPICallInput, type OpenAPIClientOptions } from "./client.js";
import type { OpenAPIDocument } from "./types.js";

interface SharedCase {
  name: string;
  document: OpenAPIDocument;
  operationId: string;
  auth?: Record<string, string>;
  input: OpenAPICallInput & { bodyBase64?: string };
  response: { status: number; headers: Record<string, string>; body?: unknown; bodyBase64?: string };
  expect: {
    /**
     * When set, states the conformance README's first boundary: the call is
     * refused before dispatch and the refusal names the artifact defect. No
     * request reaches the transport, so the remaining request and result
     * members are not read.
     */
    refuse?: string;
    method: string;
    path: string;
    query: string;
    headers: Record<string, string>;
    bodyJSON?: unknown;
    bodyBase64?: string;
    ok: boolean;
    value?: unknown;
    valueBase64?: string;
    valueOmitted?: boolean;
    responseKey?: string;
    mediaType?: string;
  };
}

const cases = JSON.parse(readFileSync(
  new URL("../../conformance/cases/native-wire.json", import.meta.url),
  "utf8",
)) as SharedCase[];

describe("language-neutral native wire conformance", () => {
  for (const fixture of cases) {
    it(fixture.name, async () => {
      const document = structuredClone(fixture.document);
      const fetchFn = vi.fn<typeof fetch>(async (input, init) => {
        const request = new Request(input, init);
        const url = new URL(request.url);
        expect(request.method).toBe(fixture.expect.method);
        expect(url.pathname).toBe(fixture.expect.path);
        expect(url.search.slice(1)).toBe(fixture.expect.query);
        for (const [name, value] of Object.entries(fixture.expect.headers)) {
          expect(request.headers.get(name)).toBe(value);
        }
        if (Object.prototype.hasOwnProperty.call(fixture.expect, "bodyJSON")) {
          expect(await request.json()).toEqual(fixture.expect.bodyJSON);
        }
        if (fixture.expect.bodyBase64 !== undefined) {
          expect(new Uint8Array(await request.arrayBuffer())).toEqual(
            Uint8Array.from(atob(fixture.expect.bodyBase64), (character) => character.charCodeAt(0)),
          );
        }
        const body = fixture.response.bodyBase64 !== undefined
          ? Uint8Array.from(atob(fixture.response.bodyBase64), (character) => character.charCodeAt(0))
          : fixture.response.body === null || fixture.response.body === undefined
            ? null
            : JSON.stringify(fixture.response.body);
        return new Response(body, {
          status: fixture.response.status,
          headers: fixture.response.headers,
        });
      });
      const options: OpenAPIClientOptions = { fetch: fetchFn };
      if (fixture.auth) options.auth = fixture.auth;
      const client = await OpenAPIClient.load(document, options);
      const input = structuredClone(fixture.input);
      if (input.bodyBase64 !== undefined) {
        input.body = Uint8Array.from(atob(input.bodyBase64), (character) => character.charCodeAt(0));
        delete input.bodyBase64;
      }
      if (fixture.expect.refuse !== undefined) {
        await expect(client.call(fixture.operationId, input)).rejects.toThrow(fixture.expect.refuse);
        expect(fetchFn).not.toHaveBeenCalled();
        return;
      }
      const result = await client.call(fixture.operationId, input);
      expect(result.ok).toBe(fixture.expect.ok);
      if (fixture.expect.valueBase64 !== undefined) {
        expect(result.ok ? result.data : result.error).toEqual(
          Uint8Array.from(atob(fixture.expect.valueBase64), (character) => character.charCodeAt(0)),
        );
      } else if (fixture.expect.valueOmitted) {
        expect(result.ok ? result.data : result.error).toBeUndefined();
      } else {
        expect(result.ok ? result.data : result.error).toEqual(fixture.expect.value);
      }
      if (fixture.expect.responseKey) expect(result.openapi.responseKey).toBe(fixture.expect.responseKey);
      if (fixture.expect.mediaType) expect(result.openapi.mediaType).toBe(fixture.expect.mediaType);
      expect(fetchFn).toHaveBeenCalledOnce();
    });
  }
});
