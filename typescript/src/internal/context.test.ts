import { describe, expect, it } from "vitest";
import {
  contextSatisfies,
  normalizeEndpoint,
  storeContextResolver,
  type ContextStore,
} from "./context.js";
import type { ContextRequiredDetails } from "./invocation.js";

// config.value schema satisfaction and the store-resolver keying rule
// (context-scope model 2026-08-19; config.value schema ratification
// 2026-08-20). Twin of the SDK's bec tests for the same members; this
// standalone package enforces only the schema's `enum` member (see the
// twin-divergence note on configValueMatchesSchema).

class MemoryStore implements ContextStore {
  private data = new Map<string, Record<string, unknown>>();

  async get(key: string): Promise<Record<string, unknown> | null> {
    return this.data.get(key) ?? null;
  }

  async set(key: string, value: Record<string, unknown>): Promise<void> {
    this.data.set(key, value);
  }

  async delete(key: string): Promise<void> {
    this.data.delete(key);
  }
}

function configChallenge(schema?: unknown): ContextRequiredDetails {
  return {
    target: "https://example.com/specs/orders.yaml",
    alternatives: [{ requirements: [
      { type: "config.value", point: "server", path: "/url", durable: true, ...(schema === undefined ? {} : { schema }) },
    ] }],
  };
}

describe("config.value schema satisfaction (enum-only twin)", () => {
  const stored = { configuration: { server: { url: "https://a.example.test" } } };

  it("treats an absent schema as unconstrained", () => {
    expect(contextSatisfies(stored, configChallenge())).toBe(true);
  });

  it("validates against a declared enum (closed admissible set)", () => {
    expect(contextSatisfies(stored, configChallenge({ enum: ["https://a.example.test", "https://b.example.test"] }))).toBe(true);
    expect(contextSatisfies(stored, configChallenge({ enum: ["https://b.example.test"] }))).toBe(false);
    expect(contextSatisfies(stored, configChallenge({ enum: [] }))).toBe(false);
  });

  it("passes non-enum schema keywords through unenforced (divergence by necessity)", () => {
    // The SDK twin would evaluate the whole schema; this package carries no
    // JSON Schema validator, so a keyword-only schema does not constrain.
    expect(contextSatisfies(stored, configChallenge({ type: "number" }))).toBe(true);
  });

  it("fails closed on a schema or enum it cannot read", () => {
    expect(contextSatisfies(stored, configChallenge(["https://a.example.test"]))).toBe(false);
    expect(contextSatisfies(stored, configChallenge({ enum: "https://a.example.test" }))).toBe(false);
  });
});

describe("storeContextResolver keying rule", () => {
  it("looks up an all-config.value alternative by the exact asserted target", async () => {
    const store = new MemoryStore();
    await store.set("https://example.com/specs/orders.yaml", {
      configuration: { server: { url: "https://a.example.test" } },
    });
    const resolve = storeContextResolver(store);
    await expect(resolve(configChallenge())).resolves.toEqual({
      configuration: { server: { url: "https://a.example.test" } },
    });
  });

  it("does not derive an origin key for an all-config.value alternative", async () => {
    const store = new MemoryStore();
    await store.set(normalizeEndpoint("https://example.com/specs/orders.yaml"), {
      configuration: { server: { url: "https://a.example.test" } },
    });
    const resolve = storeContextResolver(store);
    await expect(resolve(configChallenge())).resolves.toBeNull();
  });

  it("keeps the endpoint-normalized key for a credential-bearing alternative carrying config.value", async () => {
    const store = new MemoryStore();
    await store.set("api.example.com", {
      bearerToken: "stored-tok",
      configuration: { approval: { value: "yes" } },
    });
    const resolve = storeContextResolver(store);
    const details: ContextRequiredDetails = {
      target: "https://api.example.com/v1",
      alternatives: [{ requirements: [
        { type: "auth.bearer", durable: true },
        { type: "config.value", point: "approval", path: "", durable: true },
      ] }],
    };
    await expect(resolve(details)).resolves.toEqual({
      bearerToken: "stored-tok",
      configuration: { approval: { value: "yes" } },
    });
  });
});
