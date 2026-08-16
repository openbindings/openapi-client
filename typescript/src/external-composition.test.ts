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

const entryReferencing = (ref: string) => `openapi: 3.1.2
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

  it("reaches a position below a reference, composing the reference it passes through", async () => {
    const { fetch } = serve({
      [ENTRY]: entryReferencing("./library.yaml#/components/schemas/Alias/properties/name"),
      "https://composition.example/library.yaml": `openapi: 3.1.2
info: {title: Library, version: "1"}
components:
  schemas:
    Alias: {$ref: "#/components/schemas/Real"}
    Real:
      type: object
      properties:
        name: {type: string}
    Unused: {$ref: "#/components/schemas/NeverDeclared"}
`,
    });
    const document = await loadOpenAPIDocument(ENTRY, undefined, {}, fetch);
    const schema = (document as Record<string, any>)
      .paths["/items"].post.requestBody.content["application/json"].schema;
    expect(schema).toEqual({ type: "string" });
  });
});
