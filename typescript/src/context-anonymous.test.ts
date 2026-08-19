import { describe, expect, it } from "vitest";
import {
  buildAuthHeaders,
  contextAnonymous,
  contextSatisfies,
  scopeContext,
  storeContextResolver,
  single,
  type ContextRequiredDetails,
  type ContextStore,
} from "./internal/index.js";
import { credentialPlacements } from "./invoke.js";
import { OpenAPIRuntime } from "./runtime.js";
import type { OpenAPIDocument, OpenAPIOperation } from "./types.js";
import { OPENAPI_PROFILE_BASE } from "./profile.js";

// Caller-asserted anonymous invocation, and the oauth2 flat-bearer
// satisfaction rule. Twin of openbindings-go's contextstore_test.go and the
// TS SDK's bec.test.ts; the engine-side half (no credential reaches the wire)
// has no SDK counterpart because the SDK owns no scheme-driven placement.

const BASE = "https://api.example.test";

const BEARER_OR_APIKEY: ContextRequiredDetails = {
  target: BASE,
  alternatives: [
    { requirements: [{ type: "auth.bearer", name: "tokenAuth" }] },
    { requirements: [{ type: "auth.apiKey", name: "keyAuth" }] },
  ],
};

// ---------------------------------------------------------------------------
// contextAnonymous
// ---------------------------------------------------------------------------

describe("contextAnonymous", () => {
  it("reads only a literal boolean true", () => {
    expect(contextAnonymous({ anonymous: true })).toBe(true);
    expect(contextAnonymous({ anonymous: false })).toBe(false);
    // A truthy non-boolean is not the assertion: asserting anonymity is an
    // explicit act, so a stray string or number must not stand in for it.
    expect(contextAnonymous({ anonymous: "true" })).toBe(false);
    expect(contextAnonymous({ anonymous: 1 })).toBe(false);
    expect(contextAnonymous({})).toBe(false);
    expect(contextAnonymous(null)).toBe(false);
    expect(contextAnonymous(undefined)).toBe(false);
  });

  it("is a top-level field, not a configuration point", () => {
    // `anonymous` qualifies the whole invocation and sits beside
    // `configuration`; nesting it inside is not the assertion.
    expect(contextAnonymous({ configuration: { anonymous: true } })).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Satisfaction under an anonymous invocation
// ---------------------------------------------------------------------------

describe("contextSatisfies under an anonymous invocation", () => {
  it("answers an auth requirement with no credential present", () => {
    expect(contextSatisfies({ anonymous: true }, BEARER_OR_APIKEY)).toBe(true);
    expect(contextSatisfies({}, BEARER_OR_APIKEY)).toBe(false);
  });

  it("answers every auth.* family, including one with no built-in resolver", () => {
    // The rule is the `auth.` prefix, not the standard family table: a
    // surfaced-but-unmapped credential family is still a credential family,
    // and "I have none" is a truthful answer to it.
    for (const type of [
      "auth.bearer",
      "auth.apiKey",
      "auth.basic",
      "auth.oauth2",
      "auth.http.digest",
    ]) {
      expect(
        contextSatisfies({ anonymous: true }, {
          target: BASE,
          alternatives: [{ requirements: [{ type, name: "scheme" }] }],
        }),
      ).toBe(true);
    }
  });

  it("answers ANDed named auth requirements one flat credential could not", () => {
    const details: ContextRequiredDetails = {
      target: BASE,
      alternatives: [{
        requirements: [
          { type: "auth.bearer", name: "first" },
          { type: "auth.bearer", name: "second" },
        ],
      }],
    };
    expect(contextSatisfies({ anonymous: true }, details)).toBe(true);
  });

  it("never answers a config.value requirement", () => {
    // Which server to talk to, which request media to send: not credentials,
    // and there is no anonymous reading of them.
    const details: ContextRequiredDetails = {
      target: BASE,
      alternatives: [{ requirements: [{ type: "config.value", point: "server", path: "/url" }] }],
    };
    expect(contextSatisfies({ anonymous: true }, details)).toBe(false);
  });

  it("still has to answer the configuration half of a mixed alternative", () => {
    const details: ContextRequiredDetails = {
      target: BASE,
      alternatives: [{
        requirements: [
          { type: "auth.basic" },
          { type: "config.value", point: "server", path: "/url" },
        ],
      }],
    };
    expect(contextSatisfies({ anonymous: true }, details)).toBe(false);
    expect(
      contextSatisfies(
        { anonymous: true, configuration: { server: { url: BASE } } },
        details,
      ),
    ).toBe(true);
  });

  it("does not answer a non-auth extension requirement", () => {
    const details: ContextRequiredDetails = {
      target: BASE,
      alternatives: [{ requirements: [{ type: "transport.session" }] }],
    };
    expect(contextSatisfies({ anonymous: true }, details)).toBe(false);
  });

  it("is not asserted by a merely truthy value", () => {
    expect(contextSatisfies({ anonymous: "yes" }, BEARER_OR_APIKEY)).toBe(false);
    expect(contextSatisfies({ anonymous: false }, BEARER_OR_APIKEY)).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// buildAuthHeaders — the generic derivation site
// ---------------------------------------------------------------------------

describe("buildAuthHeaders", () => {
  it("derives Authorization from the well-known credential fields", () => {
    expect(buildAuthHeaders({ bearerToken: "t" })["Authorization"]).toBe("Bearer t");
    expect(buildAuthHeaders({ apiKey: "k" })["Authorization"]).toBe("ApiKey k");
    expect(buildAuthHeaders({ basic: { username: "u", password: "p" } })["Authorization"])
      .toBe(`Basic ${btoa("u:p")}`);
  });

  it("derives no credential at all under an anonymous invocation", () => {
    const stale = {
      anonymous: true,
      bearerToken: "left-over-from-an-earlier-call",
      apiKey: "left-over-key",
      accessToken: "left-over-access-token",
      basic: { username: "u", password: "p" },
    };
    const headers = buildAuthHeaders(stale);
    expect(headers["Authorization"]).toBeUndefined();
    // Nothing derived from a credential field reaches any header, under any
    // name — not just the one Authorization slot.
    const emitted = JSON.stringify(headers);
    for (const secret of [
      "left-over-from-an-earlier-call",
      "left-over-key",
      "left-over-access-token",
      btoa("u:p"),
    ]) {
      expect(emitted).not.toContain(secret);
    }
  });

  it("still merges headers and cookies the caller placed by hand", () => {
    // Those are carriage the caller supplied explicitly, not credentials this
    // helper derived, so anonymity does not silently drop them.
    const headers = buildAuthHeaders({
      anonymous: true,
      bearerToken: "derived-and-suppressed",
      headers: { "X-Trace": "abc" },
      cookies: { session: "s1" },
    });
    expect(headers["X-Trace"]).toBe("abc");
    expect(headers["Cookie"]).toBe("session=s1");
    expect(headers["Authorization"]).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// credentialPlacements — the engine's scheme-driven wire application
// ---------------------------------------------------------------------------

const SECURED_DOC: OpenAPIDocument = {
  openapi: "3.0.3",
  info: { title: "t", version: "1" },
  servers: [{ url: BASE }],
  security: [{ tokenAuth: [] }, { keyAuth: [] }, { oauth: [] }],
  paths: {
    "/public": {
      get: { operationId: "publicRead", responses: { "200": { description: "ok" } } },
    },
  },
  components: {
    securitySchemes: {
      tokenAuth: { type: "http", scheme: "bearer" },
      keyAuth: { type: "apiKey", in: "header", name: "X-Api-Key" },
      oauth: {
        type: "oauth2",
        flows: { clientCredentials: { tokenUrl: `${BASE}/token`, scopes: {} } },
      },
    },
  },
} as unknown as OpenAPIDocument;

const SECURED_OP = (SECURED_DOC.paths!["/public"] as Record<string, unknown>)["get"] as OpenAPIOperation;

describe("credentialPlacements under an anonymous invocation", () => {
  it("places every declared credential when the caller supplies one", () => {
    // The control: without the assertion, the same context does reach the wire.
    expect(credentialPlacements(SECURED_DOC, SECURED_OP, { bearerToken: "t" }, BASE, []))
      .toEqual([{ channel: "header", name: "Authorization", value: "Bearer t" }]);
  });

  it("places nothing, even with credentials left in context by an earlier call", () => {
    // The security-relevant half: the assertion has to reach the wire, not
    // only the negotiation. A stale credential must not ride along on an
    // invocation the caller declared anonymous.
    const stale = {
      anonymous: true,
      bearerToken: "left-over-bearer",
      apiKey: "left-over-key",
      apiKeys: { keyAuth: "left-over-named-key" },
      accessToken: "left-over-access-token",
      credentials: { tokenAuth: "left-over-scoped-bearer" },
    };
    const placements = credentialPlacements(SECURED_DOC, SECURED_OP, stale, BASE, []);
    expect(placements).toEqual([]);
    // Not merely "no Authorization": no channel carries any of it.
    const emitted = JSON.stringify(placements);
    for (const secret of [
      "left-over-bearer",
      "left-over-key",
      "left-over-named-key",
      "left-over-access-token",
      "left-over-scoped-bearer",
    ]) {
      expect(emitted).not.toContain(secret);
    }
  });
});

// ---------------------------------------------------------------------------
// End to end: a public read under a blanket document-level requirement
// ---------------------------------------------------------------------------

interface CapturedRequest {
  url: string;
  headers: Headers;
}

function mockFetch(): { fetch: typeof globalThis.fetch; requests: CapturedRequest[] } {
  const requests: CapturedRequest[] = [];
  const fn = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    requests.push({
      url: input instanceof Request ? input.url : String(input),
      headers: new Headers(init?.headers),
    });
    return new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  };
  return { fetch: fn, requests };
}

const SECURED_SOURCE = {
  profile: OPENAPI_PROFILE_BASE,
  content: {
    ...SECURED_DOC,
    paths: {
      "/public": {
        get: {
          operationId: "publicRead",
          responses: { "200": { description: "ok", content: { "application/json": {} } } },
        },
      },
    },
  },
};

describe("a public read under a blanket document-level requirement", () => {
  it("is unreachable without the assertion", async () => {
    // The document says the API ACCEPTS these schemes; with no credential and
    // no assertion the invoker challenges rather than guessing.
    const details = await new OpenAPIRuntime().prepareBinding({
      source: SECURED_SOURCE,
      ref: "#/paths/~1public/get",
    });
    expect(details).not.toBeNull();
    expect(details!.alternatives.length).toBeGreaterThan(0);
  });

  it("dispatches with no Authorization header once the caller asserts anonymity", async () => {
    const { fetch, requests } = mockFetch();
    const call = new OpenAPIRuntime().invokeBinding({
      source: SECURED_SOURCE,
      ref: "#/paths/~1public/get",
      context: { anonymous: true, bearerToken: "left-over-bearer" },
      fetch,
    });
    await expect(single(call.outputs)).resolves.toEqual({ ok: true });
    expect(requests).toHaveLength(1);
    expect(requests[0]!.headers.get("Authorization")).toBeNull();
    expect(requests[0]!.headers.get("X-Api-Key")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// auth.oauth2 accepts a flat bearer token
// ---------------------------------------------------------------------------

const OAUTH2_ONLY: ContextRequiredDetails = {
  target: BASE,
  alternatives: [{ requirements: [{ type: "auth.oauth2", name: "oauth", durable: true }] }],
};

describe("auth.oauth2 satisfaction", () => {
  // An OAuth2 access token reaches the wire AS a Bearer credential, and
  // credential application already falls back to the flat bearer token when no
  // accessToken is present; the satisfaction check was the only half that
  // disagreed, which made every oauth2-declaring artifact a dead end.
  it("is satisfied by a flat bearerToken, not only accessToken", () => {
    expect(contextSatisfies({ bearerToken: "t" }, OAUTH2_ONLY)).toBe(true);
    expect(contextSatisfies({ accessToken: "a" }, OAUTH2_ONLY)).toBe(true);
    expect(contextSatisfies({ bearerToken: "" }, OAUTH2_ONLY)).toBe(false);
    expect(contextSatisfies({}, OAUTH2_ONLY)).toBe(false);
  });

  it("does not apply a flat bearerToken to an ambiguous auth.oauth2 challenge", () => {
    // Same unambiguity gate the other auth families use: two named oauth2
    // schemes leave no way to tell which one the flat token belongs to.
    const details: ContextRequiredDetails = {
      target: BASE,
      alternatives: [
        { requirements: [{ type: "auth.oauth2", name: "schemeA" }] },
        { requirements: [{ type: "auth.oauth2", name: "schemeB" }] },
      ],
    };
    expect(contextSatisfies({ bearerToken: "ambiguous" }, details)).toBe(false);
    expect(contextSatisfies({ accessToken: "ambiguous" }, details)).toBe(false);
    expect(
      contextSatisfies({ credentials: { schemeB: { accessToken: "specific" } } }, details),
    ).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// The satisfaction rule and the scoping rule have to agree
// ---------------------------------------------------------------------------

describe("scopeContext admits what requirementSatisfied accepted", () => {
  // The contradiction this guards: the challenge validated against a stored
  // bearer token and then scoping admitted nothing, so the caller supplied
  // exactly what the error asked for, the scope gate dropped it, and the
  // invoker re-challenged forever. A rule that says a value satisfies a
  // requirement has to let that value through.
  it("admits a flat bearerToken for an auth.oauth2 requirement", () => {
    expect(scopeContext({ bearerToken: "t" }, OAUTH2_ONLY)).toEqual({ bearerToken: "t" });
  });

  it("still admits the rest of the oauth2 family alongside it", () => {
    expect(
      scopeContext(
        { accessToken: "a", refreshToken: "r", clientSecret: "s", unrelated: "x" },
        OAUTH2_ONLY,
      ),
    ).toEqual({ accessToken: "a", refreshToken: "r", clientSecret: "s" });
  });

  it("closes the re-challenge loop through the store-backed resolver", async () => {
    // The whole round trip a caller actually walks: an oauth2 challenge, a
    // bearerToken stored in answer to it, and a resolver that must hand that
    // token back rather than declining or admitting an empty scope.
    const store: ContextStore = {
      get: async (key) => (key === "api.example.test" ? { bearerToken: "stored" } : null),
      set: async () => {},
      delete: async () => {},
    };
    const resolved = await storeContextResolver(store)(OAUTH2_ONLY);
    expect(resolved).toEqual({ bearerToken: "stored" });
  });
});
