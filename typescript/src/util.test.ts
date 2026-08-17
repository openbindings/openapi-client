import { describe, it, expect } from "vitest";
import { parseRef, buildJsonPointerRef, sanitizeKey, uniqueKey, mergeParameters, loadOpenAPIDocument } from "./util.js";

describe("parseRef", () => {
  it("parses a standard JSON pointer ref", () => {
    const result = parseRef("#/paths/~1users/get");
    expect(result).toEqual({ path: "/users", method: "get" });
  });

  // OAPI-D-03: the ref MUST be a JSON Pointer of the exact form
  // #/paths/<escaped-path>/<method>. A prefix-less spelling was previously
  // accepted leniently; that acceptance was non-conformant.
  it("refuses a ref without the #/paths/ prefix (OAPI-D-03)", () => {
    expect(() => parseRef("paths/~1users~1{id}/delete")).toThrow("must be a JSON Pointer");
  });

  // OAPI-D-03: the path segment carries RFC 6901 escaping, so a conformant
  // ref has exactly one path token. Unescaped multi-token spellings were
  // previously accepted leniently; that acceptance was non-conformant.
  it("refuses unescaped path tokens (OAPI-D-03)", () => {
    expect(() => parseRef("#/paths/users/posts/get")).toThrow("must be a JSON Pointer");
  });

  it("handles tilde escaping correctly", () => {
    const result = parseRef("#/paths/~1a~0b~1c/post");
    expect(result).toEqual({ path: "/a~b/c", method: "post" });
  });

  // OAPI-D-03: the method is lowercase exactly as the artifact spells it —
  // acceptance never case-folds. (This flips the previous lenient
  // lower-casing pin, which was non-conformant.)
  it("refuses an uppercase method, never case-folds (OAPI-D-03)", () => {
    expect(() => parseRef("#/paths/~1users/GET")).toThrow("lowercase");
  });

  it("throws for too few parts", () => {
    expect(() => parseRef("#/paths")).toThrow("must be a JSON Pointer");
  });

  it("throws for non-paths prefix", () => {
    expect(() => parseRef("#/components/schemas/get")).toThrow("must be a JSON Pointer");
  });

  it("throws for invalid HTTP method", () => {
    expect(() => parseRef("#/paths/~1users/connect")).toThrow("invalid HTTP method");
  });
});

describe("buildJsonPointerRef", () => {
  it("builds a ref from path and method", () => {
    expect(buildJsonPointerRef("/users", "get")).toBe("#/paths/~1users/get");
  });

  it("handles nested paths", () => {
    expect(buildJsonPointerRef("/users/{id}/posts", "post")).toBe(
      "#/paths/~1users~1{id}~1posts/post",
    );
  });

  it("round-trips with parseRef", () => {
    const original = { path: "/a~b/c", method: "put" };
    const ref = buildJsonPointerRef(original.path, original.method);
    const parsed = parseRef(ref);
    expect(parsed).toEqual(original);
  });
});

describe("sanitizeKey", () => {
  it("passes through clean keys", () => {
    expect(sanitizeKey("getUser")).toBe("getUser");
  });

  it("replaces special characters with underscores", () => {
    expect(sanitizeKey("get /users/{id}")).toBe("get__users__id");
  });

  it("strips leading/trailing underscores", () => {
    expect(sanitizeKey("__foo__")).toBe("foo");
  });

  it("returns 'unnamed' for empty result", () => {
    expect(sanitizeKey("!!!")).toBe("unnamed");
  });

  it("replaces an astral-plane character with one underscore, not one per surrogate half", () => {
    expect(sanitizeKey("t-😀-a")).toBe("t-_-a");
  });

  it("preserves dots and hyphens", () => {
    expect(sanitizeKey("users.get-all")).toBe("users.get-all");
  });

  it("prefixes keys that would start with a non-letter (OBI-D-03, Go parity)", () => {
    expect(sanitizeKey("2fa.enable")).toBe("_2fa.enable");
    expect(sanitizeKey("42")).toBe("_42");
    expect(sanitizeKey("123 go")).toBe("_123_go");
    expect(sanitizeKey(".hidden")).toBe("_.hidden");
    expect(sanitizeKey("-flag")).toBe("_-flag");
  });
});

describe("uniqueKey", () => {
  it("returns key directly when not used", () => {
    expect(uniqueKey("foo", new Set())).toBe("foo");
  });

  it("appends _2 on first collision", () => {
    expect(uniqueKey("foo", new Set(["foo"]))).toBe("foo_2");
  });

  it("increments until unique", () => {
    expect(uniqueKey("foo", new Set(["foo", "foo_2", "foo_3"]))).toBe("foo_4");
  });
});

describe("mergeParameters", () => {
  it("returns opParams when pathParams empty", () => {
    const op = [{ name: "id", in: "query" }];
    expect(mergeParameters([], op)).toEqual(op);
  });

  it("returns pathParams when opParams empty", () => {
    const path = [{ name: "id", in: "path" }];
    expect(mergeParameters(path, [])).toEqual(path);
  });

  it("operation params override path params by in+name", () => {
    const pathParams = [
      { name: "id", in: "path", required: true },
      { name: "format", in: "query" },
    ];
    const opParams = [
      { name: "format", in: "query", description: "overridden" },
    ];
    const result = mergeParameters(pathParams, opParams);
    expect(result).toHaveLength(2);
    expect(result[0]).toEqual({ name: "id", in: "path", required: true });
    expect(result[1]).toEqual({ name: "format", in: "query", description: "overridden" });
  });

  it("handles undefined inputs gracefully", () => {
    expect(mergeParameters(undefined, undefined)).toEqual([]);
    expect(mergeParameters(undefined, [{ name: "x", in: "query" }])).toHaveLength(1);
  });
});

describe("loadOpenAPIDocument reference closure", () => {
  it("keeps fragment-only references inside an external Path Item scoped to its document", async () => {
    const root = {
      openapi: "3.1.2",
      info: { title: "t", version: "1" },
      paths: { "/items": { $ref: "./path-item.json" } },
    };
    const external = {
      post: {
        parameters: [{ $ref: "#/Trace" }],
        requestBody: { $ref: "#/Create" },
        responses: { "200": { $ref: "#/Created" } },
      },
      Trace: { name: "trace", in: "query", schema: { type: "string" } },
      Create: {
        required: true,
        content: { "application/json": { schema: { type: "object" } } },
      },
      Created: { description: "ok" },
    };
    const fetch: typeof globalThis.fetch = async (input) => {
      return String(input) === "https://description.example/path-item.json"
        ? new Response(JSON.stringify(external), { status: 200 })
        : new Response("missing", { status: 404 });
    };

    const loaded = await loadOpenAPIDocument(
      "https://description.example/openapi.json",
      root,
      undefined,
      fetch,
    );
    const post = loaded.paths?.["/items"]?.post;
    expect(post?.parameters?.[0]).toMatchObject({ name: "trace", in: "query" });
    expect(post?.requestBody).toMatchObject({ required: true });
    expect(post?.responses?.["200"]).toMatchObject({ description: "ok" });
  });
});

// String content parses under YAML 1.2.2, and its plain scalars resolve by
// §10.3.2's core tag resolution: the null/bool/int/float patterns, with
// everything else — date- and time-shaped scalars included — a string. The
// restriction is the artifact authority's: every accepted OAS edition
// requires that "Tags MUST be limited to those allowed by [YAML's] JSON
// schema ruleset" (§4.2), and YAML 1.1's timestamp tag is outside that set.
// Resolving one anyway destroyed the declared value silently, because a
// canonical-JSON writer renders a Date — an object with no own enumerable
// properties — as {}.
describe("YAML 1.2.2 core tag resolution (OAS §4.2, YAML 1.2.2 §10.3.2)", () => {
  const document = (spelling: string) => `openapi: 3.0.0
info:
  title: scalars
  version: 1.0.0
paths:
  /probe:
    get:
      operationId: probe
      parameters:
        - name: value
          in: query
          schema:
            type: string
            example: ${spelling}
      responses:
        "200":
          description: ok
`;

  const example = async (spelling: string): Promise<unknown> => {
    const loaded = await loadOpenAPIDocument(undefined, document(spelling));
    return (loaded.paths?.["/probe"]?.get?.parameters?.[0] as {
      schema?: { example?: unknown };
    }).schema?.example;
  };

  // No YAML 1.2 schema carries a timestamp type; every one of these matches
  // none of §10.3.2's patterns and falls through to tag:yaml.org,2002:str.
  // The bare-date and space-separated spellings are the sensitive ones: a
  // timestamp resolution does not even round-trip their text.
  it.each([
    "2020-01-01T12:00:00Z",
    "2020-01-01",
    "2020-01-01 12:00:00",
    "2001-12-14 21:59:43.10 -5",
    "12:30:45",
    "190:20:30",
  ])("resolves the date- or time-shaped scalar %s as a string", async (spelling) => {
    expect(await example(spelling)).toBe(spelling);
  });

  // The remaining YAML 1.1 implicit types DEFAULT_SCHEMA carries are absent
  // for the same reason: their tags are outside the OAS's permitted set.
  it.each(["yes", "no", "on", "off", "y", "n"])(
    "resolves the YAML 1.1 boolean word %s as a string",
    async (spelling) => {
      expect(await example(spelling)).toBe(spelling);
    },
  );

  it("refuses an explicit YAML 1.1 tag rather than resolving it", async () => {
    await expect(loadOpenAPIDocument(undefined, document("!!timestamp 2020-01-01")))
      .rejects.toThrow(/unknown tag/u);
  });

  // §10.3.2's own patterns keep resolving, and each keeps its JSON type.
  it.each([
    ["null", null],
    ["NULL", null],
    ["~", null],
    ["true", true],
    ["True", true],
    ["FALSE", false],
    ["017", 17],
    ["0o17", 15],
    ["0x1F", 31],
    ["-19", -19],
    ["+12.3", 12.3],
    [".5", 0.5],
    ["5.", 5],
    ["12e03", 12000],
    ["1_000", "1_000"],
  ] as const)("resolves %s by §10.3.2's patterns", async (spelling, want) => {
    expect(await example(spelling)).toStrictEqual(want);
  });

  // The spellings this parser used to resolve by YAML 1.1 rules the accepted
  // OAS editions do not admit — F-O1-7, closed. §10.3.2's table has no binary
  // row at all, its `0o` and `0x` rows carry no sign, no row admits a `_`
  // separator, and its float row signs the whole group, so js-yaml's own
  // CORE_SCHEMA (an alias of its JSON_SCHEMA, whose header says it "allows
  // numbers in binary notaion") was wrong on each. The conformant resolution
  // now comes from ./yaml-core-schema.ts.
  it.each([
    ["0b101", "0b101"],
    ["-0b101", "-0b101"],
    ["-0o17", "-0o17"],
    ["-0x1F", "-0x1F"],
    ["0x_1F", "0x_1F"],
    ["1_000.5", "1_000.5"],
    ["-.5", -0.5],
    ["-017", -17],
    ["0120", 120],
  ] as const)("resolves %s by §10.3.2 rather than by js-yaml's own table", async (spelling, want) => {
    expect(await example(spelling)).toStrictEqual(want);
  });

  // A magnitude the double domain cannot hold keeps its source text. §10.3.2
  // resolves it to a float, but §10.2.1.4 says "The supported range and
  // accuracy depends on the implementation", which makes refusing, carrying
  // ±Infinity and clamping all permitted — a choice, not a deduction. Both
  // twins keep the text; filed as F-O1-15. Underflow is not in that class.
  it.each([
    ["1e400", "1e400"],
    ["600e27371700", "600e27371700"],
    ["1e-400", 0],
  ] as const)("keeps %s as the artifact spelled it (F-O1-15)", async (spelling, want) => {
    expect(await example(spelling)).toStrictEqual(want);
  });

  // ±.inf and .nan resolve to floats JSON cannot spell. The operation value
  // domain is JSON (core §5) and the OAS admits YAML to "preserve the
  // ability to round-trip between YAML and JSON formats" (§4.2), so the
  // artifact is refused loudly here rather than reaching the boundary as a
  // null the author never wrote. The Go twin refuses the same documents.
  it.each([".inf", "-.inf", ".nan", ".NaN"])(
    "refuses the artifact whose scalar %s has no JSON image",
    async (spelling) => {
      await expect(loadOpenAPIDocument(undefined, document(spelling)))
        .rejects.toThrow(/no JSON representation/u);
    },
  );

  // §3's duplicate-key pin is unaffected by the schema restriction, in the
  // JSON spelling too — which JSON.parse would silently last-wins.
  it("still refuses duplicate mapping keys", async () => {
    await expect(loadOpenAPIDocument(
      undefined,
      "openapi: 3.0.0\ninfo:\n  title: t\n  title: u\n  version: 1\npaths: {}\n",
    )).rejects.toThrow(/duplicated mapping key/u);
  });

  // Anchors and aliases are syntax, not schema: they survive the
  // restriction, so a shared declaration still resolves.
  it("still resolves anchors and aliases", async () => {
    const loaded = await loadOpenAPIDocument(undefined, `openapi: 3.0.0
info:
  title: t
  version: 1.0.0
components:
  schemas:
    Shared: &shared
      type: string
    Alias: *shared
paths: {}
`);
    const schemas = (loaded.components as { schemas?: Record<string, unknown> } | undefined)?.schemas;
    expect(schemas?.["Alias"]).toEqual({ type: "string" });
  });
});
