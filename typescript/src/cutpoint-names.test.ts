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
  // The naming rule's own unit surface, pinned cell for cell in
  // openbindings-go/formats/openapi/cutpoint_names_test.go. Both engines must
  // derive the same qualified spelling from the same two addresses. The whole
  // base x document matrix is here because the twin claim was once made by
  // three absolute http cases, and the two engines disagreed on every RELATIVE
  // and every OPAQUE address without a test noticing.
  //
  // Read down a column to see one document qualified from every artifact
  // address; read across a row to see one artifact address qualify every
  // document form. Rule 3 in util.ts states the three cases.
  const DOCUMENTS = [
    "https://api.example/shared/node.yaml",
    "https://api.example/one.json",
    "https://other.example/one.yaml",
    "https://api.example:8443/v1/defs.yaml",
    "file:///checkout/api/shared/node.yaml",
    "/checkout/api/shared/node.yaml",
    "defs.yaml",
    "schemas/defs.yaml",
    "./defs.yaml",
    "urn:example:one",
    "https://api.example/a b/c.yaml",
    "https://api.example/a%20b/c.yaml",
    "https://api.example/v1/defs",
  ];
  const MATRIX: ReadonlyArray<readonly [string, readonly string[]]> = [
    ["https://api.example/root.yaml", [
      "shared/node",
      "one",
      "other.example/one",
      "api.example:8443/v1/defs",
      "checkout/api/shared/node",
      "checkout/api/shared/node",
      "defs",
      "schemas/defs",
      "defs",
      "urn:example:one",
      "a b/c",
      "a b/c",
      "v1/defs",
    ]],
    ["https://api.example/v1/root.yaml", [
      "shared/node",
      "one",
      "other.example/one",
      "api.example:8443/v1/defs",
      "checkout/api/shared/node",
      "checkout/api/shared/node",
      "defs",
      "schemas/defs",
      "defs",
      "urn:example:one",
      "a b/c",
      "a b/c",
      "defs",
    ]],
    ["https://api.example/root", [
      "shared/node",
      "one",
      "other.example/one",
      "api.example:8443/v1/defs",
      "checkout/api/shared/node",
      "checkout/api/shared/node",
      "defs",
      "schemas/defs",
      "defs",
      "urn:example:one",
      "a b/c",
      "a b/c",
      "v1/defs",
    ]],
    ["https://api.example:8443/v1/root.yaml", [
      "api.example/shared/node",
      "api.example/one",
      "other.example/one",
      "defs",
      "checkout/api/shared/node",
      "checkout/api/shared/node",
      "defs",
      "schemas/defs",
      "defs",
      "urn:example:one",
      "api.example/a b/c",
      "api.example/a b/c",
      "api.example/v1/defs",
    ]],
    ["file:///checkout/api/root.yaml", [
      "api.example/shared/node",
      "api.example/one",
      "other.example/one",
      "api.example:8443/v1/defs",
      "shared/node",
      "shared/node",
      "defs",
      "schemas/defs",
      "defs",
      "urn:example:one",
      "api.example/a b/c",
      "api.example/a b/c",
      "api.example/v1/defs",
    ]],
    ["/checkout/api/root.yaml", [
      "api.example/shared/node",
      "api.example/one",
      "other.example/one",
      "api.example:8443/v1/defs",
      "shared/node",
      "shared/node",
      "defs",
      "schemas/defs",
      "defs",
      "urn:example:one",
      "api.example/a b/c",
      "api.example/a b/c",
      "api.example/v1/defs",
    ]],
    ["root.yaml", [
      "api.example/shared/node",
      "api.example/one",
      "other.example/one",
      "api.example:8443/v1/defs",
      "checkout/api/shared/node",
      "checkout/api/shared/node",
      "defs",
      "schemas/defs",
      "defs",
      "urn:example:one",
      "api.example/a b/c",
      "api.example/a b/c",
      "api.example/v1/defs",
    ]],
    ["https://api.example/", [
      "shared/node",
      "one",
      "other.example/one",
      "api.example:8443/v1/defs",
      "checkout/api/shared/node",
      "checkout/api/shared/node",
      "defs",
      "schemas/defs",
      "defs",
      "urn:example:one",
      "a b/c",
      "a b/c",
      "v1/defs",
    ]],
    ["urn:example:root", [
      "api.example/shared/node",
      "api.example/one",
      "other.example/one",
      "api.example:8443/v1/defs",
      "checkout/api/shared/node",
      "checkout/api/shared/node",
      "defs",
      "schemas/defs",
      "defs",
      "urn:example:one",
      "api.example/a b/c",
      "api.example/a b/c",
      "api.example/v1/defs",
    ]],
  ];

  it.each(MATRIX)("qualifies every document form against %s", (base, want) => {
    expect(DOCUMENTS.map((document) => relativeDocumentName(base, document))).toEqual(want);
  });

  it("is independent of how the artifact was reached", () => {
    // The same two documents laid out the same way qualify identically from a
    // checkout and from a server: that is the point of keeping it relative.
    expect(relativeDocumentName("file:///checkout/api/root.yaml", "file:///checkout/api/shared/node.yaml"))
      .toBe(relativeDocumentName("https://api.example/root.yaml", "https://api.example/shared/node.yaml"));
  });
});

describe("assignCutPointNames", () => {
  // Every claimant shape the assignment rule admits, pinned identically in
  // openbindings-go/formats/openapi/cutpoint_names_test.go. The rule must be
  // TOTAL (every claimant gets a key) and INJECTIVE (no two get the same one):
  // `$defs` is a map, so a repeated key drops one definition and silently
  // resolves the other cut point's `$ref` to the survivor.
  //
  // A claimant is either the artifact's own component (no declaring document)
  // or one reached through an external reference, whose declaring document may
  // be a real address or, where the resolver recorded none, empty.
  const CASES: ReadonlyArray<readonly [string, readonly DeclaredComponent[], readonly string[]]> = [
    [
      "uncontested names are the declaring document's own",
      [
        { name: "Node", pointer: "/components/schemas/Node" },
        { name: "Team", document: "https://api.example/shared/model.yaml", pointer: "/components/schemas/Team" },
      ],
      ["Node", "Team"],
    ],
    [
      "the artifact's own component keeps the name, composed documents qualify",
      [
        { name: "Node", pointer: "/components/schemas/Node" },
        { name: "Node", document: "https://api.example/one.yaml", pointer: "/components/schemas/Node" },
        { name: "Node", document: "https://api.example/two.yaml", pointer: "/components/schemas/Node" },
      ],
      ["Node", "one_Node", "two_Node"],
    ],
    [
      "two claimants with no declaring document to qualify by",
      [
        { name: "Node", document: "", pointer: "/components/schemas/Node" },
        { name: "Node", document: "", pointer: "/definitions/Node" },
      ],
      ["Node", "Node_2"],
    ],
    [
      "three claimants with no declaring document to qualify by",
      [
        { name: "Node", document: "", pointer: "/a/Node" },
        { name: "Node", document: "", pointer: "/b/Node" },
        { name: "Node", document: "", pointer: "/c/Node" },
      ],
      ["Node", "Node_2", "Node_3"],
    ],
    [
      "the artifact's own component outranks an unqualifiable claimant",
      [
        { name: "Node", pointer: "/components/schemas/Node" },
        { name: "Node", document: "", pointer: "/definitions/Node" },
      ],
      ["Node", "Node_2"],
    ],
    [
      "three-way collision between composed documents",
      [
        { name: "Node", document: "https://api.example/one.yaml", pointer: "/components/schemas/Node" },
        { name: "Node", document: "https://api.example/two.yaml", pointer: "/components/schemas/Node" },
        { name: "Node", document: "https://api.example/three.yaml", pointer: "/components/schemas/Node" },
      ],
      ["one_Node", "two_Node", "three_Node"],
    ],
    [
      "two documents that qualify to one name",
      [
        { name: "Node", document: "https://api.example/one.yaml", pointer: "/components/schemas/Node" },
        { name: "Node", document: "https://api.example/one.json", pointer: "/components/schemas/Node" },
      ],
      ["one_Node_2", "one_Node"],
    ],
    [
      "a qualified name the artifact already spells itself",
      [
        { name: "one_Node", pointer: "/components/schemas/one_Node" },
        { name: "Node", document: "https://api.example/one.yaml", pointer: "/components/schemas/Node" },
        { name: "Node", document: "https://api.example/two.yaml", pointer: "/components/schemas/Node" },
      ],
      ["one_Node", "one_Node_2", "two_Node"],
    ],
    [
      "unknown and absent declaring documents together",
      [
        { name: "Node", document: "", pointer: "/a/Node" },
        { name: "Node", document: "", pointer: "/b/Node" },
        { name: "Node", pointer: "/components/schemas/Node" },
      ],
      ["Node_2", "Node_3", "Node"],
    ],
    [
      "opaque and relative declaring documents",
      [
        { name: "Node", document: "urn:example:one", pointer: "/components/schemas/Node" },
        { name: "Node", document: "defs.yaml", pointer: "/components/schemas/Node" },
        { name: "Node", document: "./defs.yaml", pointer: "/components/schemas/Node" },
      ],
      ["urn_example_one_Node", "defs_Node_2", "defs_Node"],
    ],
  ];

  it.each(CASES)("assigns a distinct key to every claimant: %s", (_label, claimants, want) => {
    const got = assignCutPointNames(claimants, ARTIFACT);
    expect(got).toEqual([...want]);
    // Totality and injectivity, asserted rather than inferred from the table.
    expect(got.filter((name) => name === undefined || name === "")).toEqual([]);
    expect(new Set(got).size).toBe(claimants.length);
  });

  it.each(CASES)("assigns over the SET, not the order: %s", (_label, claimants, want) => {
    const reversed = assignCutPointNames([...claimants].reverse(), ARTIFACT);
    expect([...reversed].reverse()).toEqual([...want]);
  });

  it("gives the artifact's own component the name even when it is claimed twice with no document", () => {
    // The defect this pins: the pre-fix rule handed the bare name to EVERY
    // claimant presenting without a document, so two of them collided.
    const both = assignCutPointNames(
      [{ name: "Node", pointer: "/components/schemas/Node" }, { name: "Node", pointer: "/components/schemas/Node" }],
      ARTIFACT,
    );
    expect(new Set(both).size).toBe(2);
    expect(both).toEqual(["Node", "Node_2"]);
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
