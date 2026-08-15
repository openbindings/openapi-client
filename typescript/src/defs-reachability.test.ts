import { describe, it, expect } from "vitest";
import { decycleSchema, type DeclaredComponent } from "./util.js";

// The runtime-side half of F-O1-5b's invariant. decycleSchema is the layer
// that mints `$defs` entries, so the closure it must hold is: it hoists a
// definition only for a cycle participant it actually reached while walking
// the schema it was handed. A subtree the caller removed before calling —
// OpenAPI direction projection removes readOnly request properties and
// writeOnly response properties — contributes no definition, and every
// emitted `$ref` into `$defs` resolves inside the emitted schema (OBI-D-16).

/** Every `$ref` string value anywhere in a node, at any position. */
function refStrings(node: unknown): string[] {
  const out: string[] = [];
  const walk = (value: unknown): void => {
    if (Array.isArray(value)) {
      for (const item of value) walk(item);
      return;
    }
    if (value === null || typeof value !== "object") return;
    for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
      if (key === "$ref" && typeof child === "string") {
        out.push(child);
        continue;
      }
      walk(child);
    }
  };
  walk(node);
  return out;
}

const REF_BASE = "#/operations/createRecord/input";

/** The artifact's own declaration of a component, as componentSchemaNames reports it. */
function declared(name: string): DeclaredComponent {
  return { name, pointer: `/components/schemas/${name}` };
}

/** A self-recursive component object graph, as full dereference produces it. */
function auditEntry(): Record<string, unknown> {
  const entry: Record<string, unknown> = {
    type: "object",
    properties: { actor: { type: "string" } },
  };
  (entry.properties as Record<string, unknown>).previous = {
    anyOf: [{ type: "string" }, entry],
  };
  return entry;
}

describe("decycleSchema $defs reachability (F-O1-5b)", () => {
  it("mints no definition for a cycle participant outside the schema it walks", () => {
    const entry = auditEntry();
    const names = new Map<object, DeclaredComponent>([[entry, declared("AuditEntry")]]);

    // The caller already removed the only branch that reached the component.
    const projected = { type: "object", properties: { title: { type: "string" } } };
    const decycled = decycleSchema(projected, names, REF_BASE) as Record<string, unknown>;

    expect(decycled.$defs).toBeUndefined();
    expect(refStrings(decycled)).toEqual([]);
  });

  it("mints exactly one referenced definition for a cycle participant it reaches", () => {
    const entry = auditEntry();
    const names = new Map<object, DeclaredComponent>([[entry, declared("AuditEntry")]]);

    const reached = {
      type: "object",
      properties: { title: { type: "string" }, audit: { anyOf: [{ type: "string" }, entry] } },
    };
    const decycled = decycleSchema(reached, names, REF_BASE) as Record<string, unknown>;

    const defs = decycled.$defs as Record<string, unknown>;
    expect(Object.keys(defs)).toEqual(["AuditEntry"]);

    const want = `${REF_BASE}/$defs/AuditEntry`;
    const { $defs: _hoisted, ...root } = decycled;
    expect(refStrings(root)).toEqual([want]);
    expect(refStrings(defs.AuditEntry)).toEqual([want]);
  });

  it("emits no `$ref` that fails to resolve inside the emitted schema (OBI-D-16)", () => {
    const entry = auditEntry();
    const names = new Map<object, DeclaredComponent>([[entry, declared("AuditEntry")]]);
    const decycled = decycleSchema(
      { type: "object", properties: { audit: entry, alsoAudit: entry } },
      names,
      REF_BASE,
    ) as Record<string, unknown>;

    const defs = (decycled.$defs ?? {}) as Record<string, unknown>;
    const prefix = `${REF_BASE}/$defs/`;
    for (const ref of refStrings(decycled)) {
      expect(ref.startsWith(prefix), `unexpected ref ${ref}`).toBe(true);
      expect(Object.hasOwn(defs, ref.slice(prefix.length))).toBe(true);
    }
    expect(Object.keys(defs)).toEqual(["AuditEntry"]);
  });
});
