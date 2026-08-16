import { describe, expect, it } from "vitest";
import { componentSchemaNames, decycleSchema, type AddressedNode } from "./util.js";

/**
 * The cut-point SET, at the surface that decides it.
 *
 * The end-to-end twin obligation lives in the shared case table
 * (openbindings-ts/packages/openapi/src/testdata/cut-point-cases.json, byte
 * identical to the Go engine's copy). These pin the two pieces this package
 * owns: which nodes `componentSchemaNames` admits as nameable, and that
 * `decycleSchema` cuts a cycle at the node the artifact addressed rather than at
 * whichever member the walk met first.
 */
describe("addressed nodes as cut points", () => {
  const wrapperDocument = () => {
    const inner: Record<string, unknown> = { type: "object", properties: {} };
    (inner.properties as Record<string, unknown>).back = inner; // dereferenced back edge
    const root: Record<string, unknown> = {
      components: { schemas: { Wrapper: { type: "object", properties: { inner } } } },
    };
    return { root, inner };
  };

  it("names a node the artifact addressed by its own pointer token", () => {
    const { root, inner } = wrapperDocument();
    const addressed: AddressedNode[] = [
      { node: inner, declaringRoot: root, pointer: "/components/schemas/Wrapper/properties/inner" },
    ];
    const names = componentSchemaNames(root, undefined, addressed);
    expect(names.get(inner)).toEqual({
      name: "inner",
      document: undefined,
      pointer: "/components/schemas/Wrapper/properties/inner",
    });
  });

  it("keeps a component's own declaration when a longer pointer also reaches it", () => {
    const root: Record<string, unknown> = {
      components: { schemas: { Node: { type: "object" }, Holder: { type: "object" } } },
    };
    const node = ((root.components as Record<string, unknown>).schemas as Record<string, unknown>).Node as object;
    const names = componentSchemaNames(root, undefined, [
      { node, declaringRoot: root, pointer: "/components/schemas/Holder/properties/alias" },
    ]);
    expect(names.get(node)?.name).toBe("Node");
  });

  it("orders addressed nodes by identity, not by resolution order", () => {
    const shared: Record<string, unknown> = { type: "object" };
    const root: Record<string, unknown> = { components: { schemas: {} } };
    const first: AddressedNode = { node: shared, declaringRoot: root, pointer: "/a/alpha" };
    const second: AddressedNode = { node: shared, declaringRoot: root, pointer: "/b/beta" };
    expect(componentSchemaNames(root, undefined, [first, second]).get(shared)?.name).toBe("alpha");
    expect(componentSchemaNames(root, undefined, [second, first]).get(shared)?.name).toBe("alpha");
  });

  it("cuts an addressed non-component node and emits no unresolvable reference", () => {
    const { root, inner } = wrapperDocument();
    const wrapper = ((root.components as Record<string, unknown>).schemas as Record<string, unknown>).Wrapper;
    const names = componentSchemaNames(root, undefined, [
      { node: inner, declaringRoot: root, pointer: "/components/schemas/Wrapper/properties/inner" },
    ]);
    const emitted = decycleSchema(wrapper, names, "#/operations/op/output") as Record<string, unknown>;
    expect(Object.keys(emitted.$defs as Record<string, unknown>)).toEqual(["inner"]);
    expect((emitted.properties as Record<string, unknown>).inner)
      .toEqual({ $ref: "#/operations/op/output/$defs/inner" });
    // Every emitted reference resolves inside the emitted document (OBI-D-16).
    const refs = new Set<string>();
    const walk = (node: unknown): void => {
      if (node === null || typeof node !== "object") return;
      if (Array.isArray(node)) { node.forEach(walk); return; }
      for (const [key, value] of Object.entries(node)) {
        if (key === "$ref" && typeof value === "string") refs.add(value);
        else walk(value);
      }
    };
    walk(emitted);
    expect([...refs]).toEqual(["#/operations/op/output/$defs/inner"]);
  });

  it("falls back to a shape-derived name only when nothing on the cycle was addressed", () => {
    const anonymous: Record<string, unknown> = { type: "object", properties: {} };
    (anonymous.properties as Record<string, unknown>).self = anonymous;
    const emitted = decycleSchema(
      { type: "object", properties: { held: anonymous } },
      new Map(),
      "#/operations/op/output",
    ) as Record<string, unknown>;
    const keys = Object.keys(emitted.$defs as Record<string, unknown>);
    expect(keys).toHaveLength(1);
    expect(keys[0]).toMatch(/^cycle_[0-9a-f]{8}$/u);
  });
});
