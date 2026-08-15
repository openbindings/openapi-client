import { describe, it, expect } from "vitest";
import {
  assignCutPointNames,
  componentSchemaNames,
  decycleSchema,
  relativeDocumentName,
  shapeDigest,
  type DeclaredComponent,
} from "./util.js";

// The analysis-layer half of the cut-point naming convention. The convention
// itself, its rationale, and its Go twin are documented in util.ts; the
// end-to-end pins live in openbindings-ts/packages/openapi and in
// openbindings-go/formats/openapi.

const ARTIFACT = "https://api.example/root.yaml";

describe("relativeDocumentName", () => {
  it("expresses a composed document relative to the artifact, extension stripped", () => {
    expect(relativeDocumentName(ARTIFACT, "https://api.example/shared/node.yaml"))
      .toBe("shared/node");
    expect(relativeDocumentName(ARTIFACT, "https://api.example/one.json")).toBe("one");
  });

  it("keeps a foreign origin distinguishable", () => {
    expect(relativeDocumentName(ARTIFACT, "https://other.example/one.yaml"))
      .toBe("other.example/one");
  });

  it("is independent of how the artifact was reached", () => {
    // The same two documents laid out the same way qualify identically from a
    // checkout and from a server: that is the point of keeping it relative.
    expect(relativeDocumentName("file:///checkout/api/root.yaml", "file:///checkout/api/shared/node.yaml"))
      .toBe(relativeDocumentName("https://api.example/root.yaml", "https://api.example/shared/node.yaml"));
  });
});

describe("assignCutPointNames", () => {
  const declared = (name: string, document?: string): DeclaredComponent =>
    ({ name, document, pointer: `/components/schemas/${name}` });

  it("uses the declaring document's own name when nothing contests it", () => {
    expect(assignCutPointNames(
      [declared("Node"), declared("Team", "https://api.example/shared/model.yaml")],
      ARTIFACT,
    )).toEqual(["Node", "Team"]);
  });

  it("qualifies every contesting composed document and leaves the artifact's own name alone", () => {
    expect(assignCutPointNames(
      [
        declared("Node"),
        declared("Node", "https://api.example/one.yaml"),
        declared("Node", "https://api.example/two.yaml"),
      ],
      ARTIFACT,
    )).toEqual(["Node", "one_Node", "two_Node"]);
  });

  it("assigns over the set, so reordering the input reorders only the output", () => {
    const components = [
      declared("Node", "https://api.example/one.yaml"),
      declared("Node", "https://api.example/two.yaml"),
    ];
    const forward = assignCutPointNames(components, ARTIFACT);
    const reversed = assignCutPointNames([...components].reverse(), ARTIFACT);
    expect(forward).toEqual(["one_Node", "two_Node"]);
    expect([...reversed].reverse()).toEqual(forward);
  });

  it("replaces characters a member name cannot carry", () => {
    expect(assignCutPointNames(
      [declared("Node", "https://api.example/a b/c.yaml"), declared("Node", "https://api.example/d.yaml")],
      ARTIFACT,
    )).toEqual(["a_b_c_Node", "d_Node"]);
  });
});

describe("componentSchemaNames", () => {
  it("names components declared by every document the load composed", () => {
    const artifactNode = { type: "object" };
    const composedNode = { type: "object" };
    const doc = { components: { schemas: { Local: artifactNode } } } as Record<string, unknown>;
    const names = componentSchemaNames(doc, [
      { root: doc },
      { root: { components: { schemas: { Remote: composedNode } } }, baseURI: "https://api.example/shared.yaml" },
    ]);
    expect(names.get(artifactNode)).toEqual({
      name: "Local",
      document: undefined,
      pointer: "/components/schemas/Local",
    });
    expect(names.get(composedNode)).toEqual({
      name: "Remote",
      document: "https://api.example/shared.yaml",
      pointer: "/components/schemas/Remote",
    });
  });
});

describe("anonymous cut points", () => {
  it("names a cycle through no declared component from its shape, not from a counter", () => {
    // The Go engine does not hoist this case at all — that divergence is a
    // hoisting question and is tracked separately. What is pinned here is that
    // the key this engine mints is a function of what it emits.
    const inner: Record<string, unknown> = { type: "object", properties: {} };
    (inner.properties as Record<string, unknown>).back = inner;
    const schema = { type: "object", properties: { inner } };

    const first = decycleSchema(schema, new Map(), "#/operations/get/output") as Record<string, unknown>;
    const second = decycleSchema(schema, new Map(), "#/operations/get/output") as Record<string, unknown>;
    const keys = Object.keys(first["$defs"] as Record<string, unknown>);
    expect(keys).toHaveLength(1);
    expect(keys[0]).toMatch(/^cycle_[0-9a-f]{8}$/u);
    expect(Object.keys(second["$defs"] as Record<string, unknown>)).toEqual(keys);
  });

  it("digests a cyclic shape without diverging on key order", () => {
    const a: Record<string, unknown> = { type: "object", title: "t" };
    a.self = a;
    const b: Record<string, unknown> = { title: "t", type: "object" };
    b.self = b;
    expect(shapeDigest(a)).toBe(shapeDigest(b));
    expect(shapeDigest({ type: "string" })).not.toBe(shapeDigest({ type: "number" }));
  });
});
