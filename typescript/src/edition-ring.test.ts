import { describe, expect, it, vi } from "vitest";
import { OpenAPIClient } from "./client.js";
import type { OpenAPIDocument } from "./types.js";

// The four cells of the 2026-09-01 edition-gating round, twinned with the Go
// engine's edition_ring_test.go. Each was byte-observable, each was reached by
// no scenario, and two of the four were not edition-gated at all but simply
// absent. The table runs every accepted 3.x line so a rule stated identically
// by three documents cannot be re-implemented on one of them.

const EDITIONS = ["3.0.0", "3.0.4", "3.1.0", "3.1.2"] as const;

function document(edition: string, parameter: Record<string, unknown> | null, serverURL = "https://api.example"): OpenAPIDocument {
  return {
    openapi: edition,
    info: { title: "edition ring", version: "1" },
    servers: [{ url: serverURL }],
    paths: {
      "/p": {
        get: {
          operationId: "g",
          ...(parameter ? { parameters: [parameter] } : {}),
          responses: { "204": { description: "ok" } },
        },
      },
    },
  } as unknown as OpenAPIDocument;
}

async function dispatch(doc: OpenAPIDocument, input: unknown): Promise<Request> {
  const seen: Request[] = [];
  const client = await OpenAPIClient.load(doc, {
    fetch: vi.fn<typeof fetch>(async (info: any, init?: any) => {
      seen.push(new Request(info, init));
      return new Response(null, { status: 204 });
    }),
  });
  await client.call("g", input as never);
  return seen[0]!;
}

// Appendix C.4.2's pre-encoding set. All three 3.x documents state it
// identically: "`[`, `]`, `#`, `&`, `=`, and `+` are pre-percent-encoded where
// Appendix C requires". Gated to the 3.2 line, a supplied "a#b" left the
// engine as "a" — the literal "#" made the rest of the URL a fragment, so the
// value was silently truncated on 3.0 and 3.1.
describe("allowReserved pre-encodes the query-structural bytes", () => {
  for (const edition of EDITIONS) {
    it(edition, async () => {
      const request = await dispatch(
        document(edition, { name: "q", in: "query", allowReserved: true, schema: { type: "string" } }),
        { parameters: { query: { q: "a#b[c]d&e=f+g" } } },
      );
      const url = new URL(request.url);
      expect(url.search).toBe("?q=a%23b%5Bc%5Dd%26e%3Df%2Bg");
      expect(url.hash).toBe("");
    });
  }
});

// The content-form lane's byte rule is its own pin and is unconditional:
// "Percent-encoding a content-form parameter leaves RFC 3986 unreserved bytes
// literal and encodes every other UTF-8 byte as uppercase `%HH`". Honoring
// `allowReserved` here — a `schema`-path control — leaked the whole reserved
// set on every edition.
describe("a content-form parameter ignores allowReserved", () => {
  for (const edition of EDITIONS) {
    it(edition, async () => {
      const request = await dispatch(
        document(edition, {
          name: "q", in: "query", allowReserved: true,
          content: { "text/plain": { schema: { type: "string" } } },
        }),
        { parameters: { query: { q: "a:/?@$,;b~c" } } },
      );
      // `~` is RFC 3986 unreserved and stays literal; every other byte here is
      // not, and rides as uppercase %HH.
      expect(new URL(request.url).search).toBe("?q=a%3A%2F%3F%40%24%2C%3Bb~c");
    });
  }
});

// "A declared cookie parameter serialized on the `schema` path is
// percent-encoded by ordinary RFC 6570 expansion" (openbindings.openapi-3.1@1
// §8.2, which states the rule and refutes the Appendix D reading the engines
// had adopted). Unencoded, a supplied ";" split one contribution into several
// on the wire.
describe("a form-style cookie parameter percent-encodes", () => {
  for (const edition of EDITIONS) {
    it(edition, async () => {
      const request = await dispatch(
        document(edition, { name: "q", in: "cookie", schema: { type: "string" } }),
        { parameters: { cookie: { q: "a;evil=1" } } },
      );
      expect(request.headers.get("cookie")).toBe("q=a%3Bevil%3D1");
    });
  }
});

// `style: cookie` is OAS 3.2's own, so no earlier line serializes it.
describe("style: cookie is not defined before 3.2", () => {
  for (const edition of EDITIONS) {
    it(edition, async () => {
      await expect(dispatch(
        document(edition, { name: "q", in: "cookie", style: "cookie", schema: { type: "string" } }),
        { parameters: { cookie: { q: "plain" } } },
      )).rejects.toThrow();
    });
  }
});

// "A completed target whose scheme is not `http` or `https` refuses before
// dispatch" (openbindings.openapi-3.0@1 §10, openbindings.openapi-3.2@1 §10;
// -3.1@1 §10 states the same restriction as a static exclusion). No engine
// implemented any of them: ftp://, file:// and ws:// all reached the
// transport, on every line.
describe("a non-http(s) completed target refuses before dispatch", () => {
  for (const edition of EDITIONS) {
    it(edition, async () => {
      for (const scheme of ["ftp", "file", "ws", "wss", "gopher"]) {
        await expect(dispatch(document(edition, null, `${scheme}://api.example`), {}))
          .rejects.toThrow(/is not http or https/);
      }
      for (const scheme of ["http", "https"]) {
        await expect(dispatch(document(edition, null, `${scheme}://api.example`), {}))
          .resolves.toBeDefined();
      }
    });
  }
});
