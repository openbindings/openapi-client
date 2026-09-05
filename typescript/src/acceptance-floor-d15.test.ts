// Block 8d-3: the registry-scoped class D15 -- a Schema Object keyword whose
// value violates the governing dialect's declared JSON type for it.
//
// The first four cases are the reviewed owning-unit census's own D15 mechanism
// fixtures (`corpus-lab/scripts/census-oas-owning-units.mjs`, mechanismTest),
// carried here verbatim so each engine is measured against the one answer
// rather than against itself; the last two extend the same edition-scoped
// clause in the direction the census does not fixture. The Go engines carry
// the identical six.
//
// These bite in both directions: deleting the detector reddens the four
// positive cases, widening it past its edition guards reddens the two negative
// ones, and neither depends on the shared 66-cell case table, which carries no
// D15 cell at all.

import { describe, expect, it } from "vitest";
import { computeAcceptanceFloor, type FloorDefect } from "./acceptance-floor.js";

interface D15Case {
  name: string;
  doc: unknown;
  positions: string[];
  method: string;
  disposition: "represented" | "invalid" | "excluded-request-media";
  invalidAlternatives: number;
}

const cases: D15Case[] = [
  {
    // The basset shape: the defect is inside the request alternative's schema
    // closure, and a request media alternative never climbs.
    name: "a boolean `required` in a request media schema invalidates the alternative only",
    doc: {
      openapi: "3.0.0",
      paths: { "/a": { post: { requestBody: { content: { "multipart/form-data": { schema: { type: "object", properties: { f: { type: "string", required: true } } } } } }, responses: { 200: { description: "ok" } } } } },
    },
    positions: ["#/paths/~1a/post/requestBody/content/multipart~1form-data/schema/properties/f/required"],
    method: "post",
    disposition: "represented",
    invalidAlternatives: 1,
  },
  {
    // The stoatchat shape: the tuple form is not the 3.0 line's `items`.
    name: "an array-valued `items` in a response schema stays confined",
    doc: {
      openapi: "3.0.0",
      paths: { "/a": { post: { responses: { 200: { description: "ok", content: { "application/json": { schema: { type: "object", additionalProperties: { items: [{ type: "string" }, { type: "number" }] } } } } } } } } },
    },
    positions: ["#/paths/~1a/post/responses/200/content/application~1json/schema/additionalProperties/items"],
    method: "post",
    disposition: "represented",
    invalidAlternatives: 0,
  },
  {
    // The nexu-io shape; edition-scoped, because the boolean spelling is the
    // 3.0 line's own correct draft-4 form.
    name: "a boolean `exclusiveMinimum` fires on the 3.1 line",
    doc: {
      openapi: "3.1.0",
      paths: { "/a": { get: { responses: { 200: { description: "ok", content: { "application/json": { schema: { type: "number", exclusiveMinimum: true } } } } } } } },
    },
    positions: ["#/paths/~1a/get/responses/200/content/application~1json/schema/exclusiveMinimum"],
    method: "get",
    disposition: "represented",
    invalidAlternatives: 0,
  },
  {
    // The edition guard, proven in the direction that does not fire.
    name: "the same boolean `exclusiveMinimum` on the 3.0 line is not this class",
    doc: {
      openapi: "3.0.0",
      paths: { "/a": { get: { responses: { 200: { description: "ok", content: { "application/json": { schema: { type: "number", minimum: 0, exclusiveMinimum: true } } } } } } } },
    },
    positions: [],
    method: "get",
    disposition: "represented",
    invalidAlternatives: 0,
  },
  {
    name: "a boolean `properties` member is a schema on the 3.1 line",
    doc: {
      openapi: "3.1.0",
      paths: { "/a": { get: { responses: { 200: { description: "ok", content: { "application/json": { schema: { type: "object", properties: { f: true } } } } } } } } },
    },
    positions: [],
    method: "get",
    disposition: "represented",
    invalidAlternatives: 0,
  },
  {
    // The 3.0 line's Schema Object is not the 2020-12 dialect and a boolean is
    // not a Schema Object there, so a boolean `properties` member IS this
    // class -- and the defect confines to the smallest unit that owns it
    // (F-O1-13, ruled 2026-08-20; here a response schema, so it remains on the
    // response projection, exactly as the string member below).
    //
    // This case read "is REFERRED, not this class" until then. The referral
    // rested on `openbindings.openapi@1` §9.2 ascribing an interpretation to a
    // boolean-valued schema on this line, and §9.2 says of that clause that it
    // governs a Media Type Object's own `schema` and does not descend to a
    // form part -- so it never reached a `properties` member. Against it stood
    // pinned authority: JSON Schema draft-wright-json-schema-00 Section 4.4,
    // "A JSON schema MUST be an object"; its validation companion Section
    // 5.16, each value of `properties` "MUST be an object"; and OAS 3.0.4
    // Section 4.7.24, "Property definitions MUST be a Schema Object and not a
    // standard JSON Schema", with `additionalProperties` the only position
    // that line grants a boolean.
    name: "a boolean `properties` member on the 3.0 line is this class",
    doc: {
      openapi: "3.0.0",
      paths: { "/a": { get: { responses: { 200: { description: "ok", content: { "application/json": { schema: { type: "object", properties: { f: true } } } } } } } } },
    },
    positions: ["#/paths/~1a/get/responses/200/content/application~1json/schema/properties/f"],
    method: "get",
    disposition: "represented",
    invalidAlternatives: 0,
  },
  {
    // The clause that DOES fire at a `properties` member on the 3.0 line: the
    // bohr-io shape, a bare string where a Schema Object belongs.
    name: "a string `properties` member is this class on the 3.0 line",
    doc: {
      openapi: "3.0.0",
      paths: { "/a": { get: { responses: { 200: { description: "ok", content: { "application/json": { schema: { type: "object", properties: { f: "array" } } } } } } } } },
    },
    positions: ["#/paths/~1a/get/responses/200/content/application~1json/schema/properties/f"],
    method: "get",
    disposition: "represented",
    invalidAlternatives: 0,
  },
];

describe("acceptance floor: D15, a schema keyword's declared JSON type", () => {
  for (const testCase of cases) {
    it(testCase.name, () => {
      const floor = computeAcceptanceFloor(testCase.doc);
      expect(floor).toBeDefined();
      const op = floor!.ops.get(`#/paths/~1a/${testCase.method}`);
      expect(op, "ladder verdict").toBeDefined();
      expect(op!.disposition).toBe(testCase.disposition);
      expect(op!.invalidAlternatives.size).toBe(testCase.invalidAlternatives);

      const seen: FloorDefect[] = [...op!.defects];
      for (const ds of op!.invalidAlternatives.values()) seen.push(...ds);
      for (const ds of op!.projections.values()) seen.push(...ds);
      const d15 = seen.filter((d) => d.class === "D15").map((d) => d.position).sort();
      expect(d15).toEqual([...testCase.positions].sort());
      for (const d of seen.filter((x) => x.class === "D15")) {
        expect(d.authority).toContain(testCase.doc && (testCase.doc as { openapi: string }).openapi.startsWith("3.1") ? "3.1 line" : "3.0 line");
      }
    });
  }
});
