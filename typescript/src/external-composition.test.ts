import { describe, expect, it } from "vitest";
import { loadOpenAPIDocument } from "./util.js";

/**
 * Owning-layer pins for pointer-scoped external composition
 * (`openbindings.openapi@1` §6, "Reference scope"): a reference that leaves the
 * current document composes the value at the referenced JSON Pointer together
 * with that value's transitive closure of references, and nothing else.
 *
 * The end-to-end verdicts are executed from the shared twin table in
 * `openbindings-ts/packages/openapi/src/external-composition.test.ts` and in
 * both Go engines. These pin the load itself, in the package that owns the
 * dereferencer.
 */

const ENTRY = "https://composition.example/openapi.yaml";

function serve(documents: Record<string, string>): {
  fetch: typeof globalThis.fetch;
  retrieved: Set<string>;
} {
  const retrieved = new Set<string>();
  const fetchFn: typeof globalThis.fetch = async (input) => {
    const address = input instanceof Request ? input.url : String(input);
    retrieved.add(address);
    const body = documents[address];
    if (body === undefined) return new Response("no such document", { status: 404 });
    return new Response(body, { status: 200 });
  };
  return { fetch: fetchFn, retrieved };
}

const entryReferencing = (ref: string, edition = "3.1.2") => `openapi: ${edition}
info: {title: Entry, version: "1"}
paths:
  /items:
    post:
      operationId: createItem
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: "${ref}"}
      responses: {"204": {description: ok}}
`;

describe("external composition is pointer-scoped", () => {
  it("composes the referenced value and leaves the rest of the resource unresolved", async () => {
    const { fetch, retrieved } = serve({
      [ENTRY]: entryReferencing("./library.yaml#/components/schemas/Used"),
      "https://composition.example/library.yaml": `openapi: 3.1.2
info: {title: Library, version: "1"}
components:
  schemas:
    Used: {type: string}
    Unused: {$ref: "./never-published.yaml#/components/schemas/Gone"}
`,
    });
    const document = await loadOpenAPIDocument(ENTRY, undefined, {}, fetch);
    const schema = (document as Record<string, any>)
      .paths["/items"].post.requestBody.content["application/json"].schema;
    expect(schema).toEqual({ type: "string" });
    // A resource reached only from outside the composed closure is never
    // retrieved, so its availability cannot decide the artifact.
    expect(retrieved.has("https://composition.example/never-published.yaml")).toBe(false);
  });

  it("refuses when the unresolvable reference is inside the composed closure", async () => {
    const { fetch } = serve({
      [ENTRY]: entryReferencing("./library.yaml#/components/schemas/Used"),
      "https://composition.example/library.yaml": `openapi: 3.1.2
info: {title: Library, version: "1"}
components:
  schemas:
    Used:
      type: object
      properties:
        name: {$ref: "#/components/schemas/NeverDeclared"}
`,
    });
    await expect(loadOpenAPIDocument(ENTRY, undefined, {}, fetch))
      .rejects.toThrow("#/components/schemas/NeverDeclared");
  });

  it("names the resource it could not parse: parsing is not scoping", async () => {
    const { fetch } = serve({
      [ENTRY]: entryReferencing("./broken.yaml#/components/schemas/Used"),
      "https://composition.example/broken.yaml": `openapi: 3.1.2
components:
  schemas:
    Used:
      type: array
      items:
      type: integer
`,
    });
    await expect(loadOpenAPIDocument(ENTRY, undefined, {}, fetch))
      .rejects.toThrow("broken.yaml");
  });

  /**
   * Reference traversal, per edition line. See `followsPointerBelowReference`
   * in `util.ts` for the authorities: the 3.0 line processes `$ref` as per JSON
   * Reference and follows; the 3.1 line makes the fragment a JSON-Pointer over
   * the referenced document's literal contents, where JSON Schema 2020-12's
   * applicator `$ref` substitutes nothing, and the reference is unresolvable.
   */
  const aliasLibrary = (edition: string) => `openapi: ${edition}
info: {title: Library, version: "1"}
components:
  schemas:
    Alias: {$ref: "#/components/schemas/Real"}
    Real:
      type: object
      properties:
        name: {type: string}
    Unused: {$ref: "#/components/schemas/NeverDeclared"}
`;

  for (const edition of ["3.0.0", "3.0.4"]) {
    it(`follows a reference standing in a pointer's path under OAS ${edition}`, async () => {
      const { fetch } = serve({
        [ENTRY]: entryReferencing("./library.yaml#/components/schemas/Alias/properties/name", edition),
        "https://composition.example/library.yaml": aliasLibrary(edition),
      });
      const document = await loadOpenAPIDocument(ENTRY, undefined, {}, fetch);
      const schema = (document as Record<string, any>)
        .paths["/items"].post.requestBody.content["application/json"].schema;
      expect(schema).toEqual({ type: "string" });
    });
  }

  for (const edition of ["3.1.0", "3.1.2"]) {
    it(`refuses a reference standing in a pointer's path under OAS ${edition}`, async () => {
      const { fetch } = serve({
        [ENTRY]: entryReferencing("./library.yaml#/components/schemas/Alias/properties/name", edition),
        "https://composition.example/library.yaml": aliasLibrary(edition),
      });
      await expect(loadOpenAPIDocument(ENTRY, undefined, {}, fetch))
        .rejects.toThrow(
          `reference "./library.yaml#/components/schemas/Alias/properties/name" is unresolvable under OAS ${edition}`,
        );
    });
  }

  it("evaluates a same-resource fragment below a reference under the same rule", async () => {
    const { fetch } = serve({
      [ENTRY]: `openapi: 3.1.2
info: {title: Entry, version: "1"}
paths:
  /items:
    post:
      operationId: createItem
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: "#/components/schemas/Alias/properties/name"}
      responses: {"204": {description: ok}}
components:
  schemas:
    Alias: {$ref: "#/components/schemas/Real"}
    Real:
      type: object
      properties:
        name: {type: string}
`,
    });
    await expect(loadOpenAPIDocument(ENTRY, undefined, {}, fetch))
      .rejects.toThrow('token "properties" identifies no member');
  });

  it("takes a sibling member literally rather than as a traversal", async () => {
    const { fetch } = serve({
      [ENTRY]: entryReferencing("./library.yaml#/components/schemas/Alias/properties/name"),
      "https://composition.example/library.yaml": `openapi: 3.1.2
info: {title: Library, version: "1"}
components:
  schemas:
    Alias:
      $ref: "#/components/schemas/Real"
      properties:
        name: {type: integer}
    Real:
      type: object
      properties:
        name: {type: string}
`,
    });
    const document = await loadOpenAPIDocument(ENTRY, undefined, {}, fetch);
    const schema = (document as Record<string, any>)
      .paths["/items"].post.requestBody.content["application/json"].schema;
    expect(schema).toEqual({ type: "integer" });
  });
});
