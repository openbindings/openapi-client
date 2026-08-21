/**
 * The acceptance floor's Schema Object dialect verdict (the D15 class): does a
 * Schema Object's own keyword value carry the JSON type the governing edition
 * line's Schema Object dialect declares for it?
 *
 * There are two ways to answer that from an authority. One POINTS at the
 * authority's own artifact so its specificity does the discriminating; the
 * other COPIES the artifact's contents into our code, which forks it and makes
 * us the maintainer of a drifting duplicate. A fork fails in both directions —
 * it misses real cases and can invent false ones — and it wears the authority's
 * voice while quietly taking the authority's decision away from it.
 *
 * This module answers per line, because the two lines have different Schema
 * Object dialects and only one of them publishes a machine-consultable
 * artifact:
 *
 * - The 3.1 line DELEGATES. Absent `jsonSchemaDialect` (and every accepted 3.1
 *   edition says so in the same words) "the OAS dialect schema id MUST be used
 *   for these Schema Objects", and that dialect —
 *   `https://spec.openapis.org/oas/3.1/dialect/base` — is a published JSON
 *   Schema. It is vendored verbatim under `authority/`, together with the OAS
 *   base vocabulary and the 2020-12 meta-schemas its `allOf` branches name.
 *   Nothing about which keywords exist, or what types they take, is stated in
 *   this file. That is what makes the ROOT 2020-12 meta-schema's deliberate
 *   legacy guards reachable: it re-declares `$recursiveAnchor` as an anchor
 *   STRING ("to prevent incompatible extensions as they remain in common
 *   use"), so `$recursiveAnchor: true` fails while `$recursiveAnchor: "foo"`
 *   and `$recursiveRef: "#"` pass, and `required`'s `uniqueItems` fails a
 *   duplicated entry. No enumeration in this package could have known either.
 *
 * - The 3.0 line TRANSCRIBES, because there is nothing to point at. OAS
 *   3.0.x's Schema Object is "an extended subset of the JSON Schema
 *   Specification Draft Wright-00", and that draft published no meta-schema:
 *   `https://json-schema.org/draft-05/schema` is 404. The OpenAPI Initiative
 *   does publish a convenience JSON Schema for whole 3.0 documents, but its
 *   `Schema` definition is `additionalProperties: false` over an enumerated
 *   keyword list, a closure the OAS prose never states — adopting it would
 *   refuse what the edition admits. So the 3.0 cells are a labeled
 *   transcription of the edition's own sentences, and they carry the guard a
 *   transcription owes: the shared case table pins every cell, and the Go twin
 *   pins each cell to the sentence it restates.
 *
 * Positions are reported at the granularity the floor's own walk uses: a
 * keyword of the node, or a member of a keyword whose value is a map or list of
 * schemas. Nothing below the schema position is reported, so two engines using
 * different validators name the same position (one reports a `uniqueItems`
 * failure at `/required`, the other at `/required/2`).
 */
import { compileSchema, draft2020 } from "json-schema-library";
import oasDialectBase from "./authority/oas-3.1-dialect-base.json" with { type: "json" };
import oasMetaBase from "./authority/oas-3.1-meta-base.json" with { type: "json" };
import jsonSchema202012 from "./authority/json-schema-2020-12.json" with { type: "json" };
import metaCore from "./authority/json-schema-2020-12-meta-core.json" with { type: "json" };
import metaApplicator from "./authority/json-schema-2020-12-meta-applicator.json" with { type: "json" };
import metaValidation from "./authority/json-schema-2020-12-meta-validation.json" with { type: "json" };
import metaMetaData from "./authority/json-schema-2020-12-meta-meta-data.json" with { type: "json" };
import metaFormatAnnotation from "./authority/json-schema-2020-12-meta-format-annotation.json" with { type: "json" };
import metaContent from "./authority/json-schema-2020-12-meta-content.json" with { type: "json" };
import metaUnevaluated from "./authority/json-schema-2020-12-meta-unevaluated.json" with { type: "json" };

type Obj = Record<string, unknown>;

const isObj = (v: unknown): v is Obj => v !== null && typeof v === "object" && !Array.isArray(v);

/**
 * The Schema Object keyword inventory the floor's own walk follows. Kept here
 * beside the shape reduction that depends on it; `acceptance-floor.ts` imports
 * these so the two never drift.
 */
export const SCHEMA_SUB_SINGLE = [
  "items", "not", "additionalProperties", "propertyNames", "contains", "if", "then", "else",
  "unevaluatedItems", "unevaluatedProperties", "additionalItems",
];
export const SCHEMA_SUB_LIST = ["allOf", "anyOf", "oneOf", "prefixItems"];
export const SCHEMA_SUB_MAP = ["properties", "patternProperties", "definitions", "$defs", "dependentSchemas"];

const SUB_SINGLE = new Set(SCHEMA_SUB_SINGLE);
const SUB_LIST = new Set(SCHEMA_SUB_LIST);
const SUB_MAP = new Set(SCHEMA_SUB_MAP);

/**
 * Whether a value is spelled as a schema at all under the governing edition
 * line: an object on either line, and a boolean on the 3.1 line only. Asks only
 * the JSON type the position declares, never whether the schema is otherwise
 * well-formed.
 *
 * The 3.1 line admits the boolean schemas outright: its Schema Object IS a JSON
 * Schema 2020-12 schema, whose own meta-schema is
 * `{"$dynamicAnchor": "meta", "type": ["object", "boolean"]}` — which is why
 * the 3.1 line never asks this question here and lets the dialect answer it.
 * The 3.0 line's Schema Object is the Wright Draft 00 subset, where every
 * Schema Object is an object and the boolean-literal schemas are not in the
 * dialect, so a boolean at a schema position there violates the dialect's
 * declared JSON type for it.
 *
 * This REFERRED the 3.0 spelling until 2026-08-20 (F-O1-13), on the ground that
 * §9.2 ascribed a part interpretation to a boolean-valued multipart part there.
 * Escalation M2 deleted the interpretation that referral rested on — a typeless
 * part now refuses on every accepted edition — and the ruled outcome is that
 * the spelling confines as an accounted `invalid` at the smallest owning unit
 * rather than refusing the whole source.
 */
export const isSchemaValued = (v: unknown, line: string): boolean => isObj(v) || (typeof v === "boolean" && line === "3.1");

/** RFC 6901 escaping, the one spelling the floor's positions use. */
const esc = (s: string): string => s.replace(/~/g, "~0").replace(/\//g, "~1");

/**
 * The OBI boundary draft: 2020-12 with an empty format registry, so `format`
 * annotates rather than asserts. The same draft the SDK core decides schema
 * well-formedness with, which is what makes the floor's verdict and the
 * downstream document rule's verdict the same verdict.
 */
const BOUNDARY_DRAFT = {
  ...draft2020,
  formats: {},
  keywords: draft2020.keywords.filter((k) => k.keyword !== "dependencies"),
};

type CompiledDialect = { validate(value: unknown): { valid: boolean; errors?: Array<{ data?: { pointer?: string } }> } };

let compiledDialect: CompiledDialect | null = null;

/**
 * Compiles the vendored OAS 3.1 dialect once, as a compound document: the
 * dialect's two `allOf` branches and the 2020-12 root's own vocabulary
 * references resolve against the absolute `$id`s of the artifacts embedded
 * under `$defs`. The artifacts are byte copies of the published ones and are
 * never fetched.
 */
function oas31Dialect(): CompiledDialect {
  if (compiledDialect) return compiledDialect;
  const embedded = [
    jsonSchema202012, oasMetaBase, metaCore, metaApplicator, metaValidation,
    metaMetaData, metaFormatAnnotation, metaContent, metaUnevaluated,
  ].map((artifact) => {
    // Each artifact's own `$schema` names 2020-12, which is the dialect being
    // compiled; dropping it avoids a self-referential dialect lookup while
    // leaving every constraint the artifact states intact.
    const copy: Obj = { ...(artifact as unknown as Obj) };
    delete copy["$schema"];
    return copy;
  });
  const compound: Obj = { ...(oasDialectBase as unknown as Obj) };
  delete compound["$schema"];
  compound["$defs"] = Object.fromEntries(embedded.map((artifact, index) => [`authority${index}`, artifact]));
  compiledDialect = compileSchema(compound, { drafts: [BOUNDARY_DRAFT] }) as unknown as CompiledDialect;
  return compiledDialect;
}

/**
 * Returns the positions, relative to one Schema Object node, whose values are
 * defective under the governing line's Schema Object dialect. An empty string
 * in the result names the node itself.
 */
export function schemaObjectDefects(node: Obj, line: "3.0" | "3.1"): string[] {
  return line === "3.1" ? oas31DialectDefects(node) : oas30SchemaObjectDefects(node);
}

/**
 * Asks the vendored dialect. The node is reduced to its own SHAPE first — every
 * subschema at a position this floor walks on its own is replaced by `true`,
 * which is well-formed under every vocabulary — so the answer is exactly this
 * node's contribution and each node is decided once. A value that is not in
 * schema form is left exactly where it sits, so a malformed `items: [ ... ]` or
 * `properties: {"a": 3}` still fails at the node that declares it. Positions
 * the floor does not walk (`contentSchema`) are likewise left in place and
 * decided where they sit.
 */
function oas31DialectDefects(node: Obj): string[] {
  const result = oas31Dialect().validate(schemaNodeShape(node));
  if (result.valid) return [];
  const positions = new Set<string>();
  for (const failure of result.errors ?? []) {
    positions.add(schemaPosition(failure.data?.pointer));
  }
  return [...positions].sort();
}

/**
 * The 3.0 line's TRANSCRIPTION, one cell per sentence the edition states:
 *
 *   `required`   — OAS 3.0.x §4.7.24.1 lists `required` among the keywords
 *                  "taken directly from the JSON Schema definition and follow
 *                  the same specifications", and that definition
 *                  (draft-wright-json-schema-validation-00 §6.17) is "an
 *                  array. Elements of this array, if any, MUST be strings, and
 *                  MUST be unique."
 *   `enum`       — the same "taken directly" list; the JSON Schema definition
 *                  (§6.23) is "an array".
 *   `items`      — OAS 3.0.x §4.7.24: "items - Value MUST be an object and not
 *                  an array."
 *   `properties` — OAS 3.0.x §4.7.24: "properties - Property definitions MUST
 *                  be a Schema Object and not a standard JSON Schema", and a
 *                  Schema Object on this line is an object (the boolean schema
 *                  literals are not in this dialect).
 *
 * Deliberately NOT transcribed, and why: `type` is the D1/D1s class with its
 * own citation; `exclusiveMinimum`/`exclusiveMaximum` are BOOLEAN on this line,
 * which is why the 3.1 delegation is scoped to the 3.1 line; and keywords this
 * line declares "strictly unsupported" decide as if absent per this
 * specification's stated reading, so their values are not judged here.
 */
function oas30SchemaObjectDefects(node: Obj): string[] {
  const positions: string[] = [];
  if ("required" in node) {
    const declared = node["required"];
    if (!Array.isArray(declared) || hasDuplicateMembers(declared)) positions.push("/required");
  }
  if ("enum" in node && !Array.isArray(node["enum"])) positions.push("/enum");
  if (Array.isArray(node["items"])) positions.push("/items");
  const properties = node["properties"];
  if (isObj(properties)) {
    for (const key of Object.keys(properties)) {
      if (isSchemaValued(properties[key], "3.0")) continue;
      positions.push(`/properties/${esc(key)}`);
    }
  }
  return positions.sort();
}

/** JSON-value equality between any two members, the comparison `uniqueItems` names. */
function hasDuplicateMembers(list: unknown[]): boolean {
  const seen = new Set<string>();
  for (const member of list) {
    const encoded = JSON.stringify(member) ?? "undefined";
    if (seen.has(encoded)) return true;
    seen.add(encoded);
  }
  return false;
}

/**
 * Renders a validator's instance pointer at the floor's own position
 * granularity: one keyword of the node, plus the member name when that
 * keyword's value is a map or list OF SCHEMAS. Everything deeper is inside one
 * schema position and names no separate position.
 */
function schemaPosition(pointer: string | undefined): string {
  if (!pointer || pointer === "#") return "";
  const body = pointer.startsWith("#") ? pointer.slice(1) : pointer;
  const tokens = body.split("/").filter((token) => token.length > 0).map((token) => token.replace(/~1/g, "/").replace(/~0/g, "~"));
  const head = tokens[0];
  if (head === undefined) return "";
  const keep = tokens.length > 1 && (SUB_MAP.has(head) || SUB_LIST.has(head)) ? 2 : 1;
  return tokens.slice(0, keep).map((token) => `/${esc(token)}`).join("");
}

/**
 * Replaces every subschema at a position this floor walks on its own with
 * `true`. `true` is a well-formed schema under every vocabulary, so the shape's
 * verdict is exactly the node's own contribution and the replaced subschemas
 * are decided when the walk reaches them.
 */
function schemaNodeShape(node: Obj): Obj {
  const shape: Obj = {};
  for (const [keyword, value] of Object.entries(node)) {
    if (SUB_SINGLE.has(keyword) && isSchemaShaped(value)) {
      shape[keyword] = true;
    } else if (SUB_LIST.has(keyword) && Array.isArray(value)) {
      shape[keyword] = value.map((member) => (isSchemaShaped(member) ? true : member));
    } else if (SUB_MAP.has(keyword) && isObj(value)) {
      const members: Obj = {};
      for (const [name, member] of Object.entries(value)) members[name] = isSchemaShaped(member) ? true : member;
      shape[keyword] = members;
    } else {
      shape[keyword] = value;
    }
  }
  return shape;
}

/**
 * The two forms a schema position admits at all. Only these are lifted out of a
 * node's shape, because only these are positions the floor's own walk visits
 * and decides.
 */
function isSchemaShaped(value: unknown): boolean {
  return typeof value === "boolean" || isObj(value);
}
