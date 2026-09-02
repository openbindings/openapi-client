// The invalid-artifact acceptance floor (block 8d-1): one pure instrument
// over the ENTRY DOCUMENT's raw JSON image, applied identically by every
// engine.
//
// This file is a PORT of the reviewed corpus-lab reference implementation
// (`corpus-lab/scripts/census-oas-owning-units.mjs`, `censusOne` +
// `projectLadder`; per-class routing from
// `corpus-lab/scripts/oas-owning-units-policy.mjs`, block 8b record), not a
// re-derivation, and the byte-for-byte twin of `acceptance_floor.go` in
// the adapter and standalone Go implementations. The ladder, its
// three propagation predicates, and the per-class answers are the ratified
// Scenario-B ruling: every defect confines to the smallest unit that owns it.
//
// Rungs, narrowest first: request media alternative -> response -> operation
// -> source. Only the request media alternative and the operation are UNITS
// under interface-synthesizer 0.2's inventory; response-side rungs are
// locating rungs whose defects attribute to the containing operation.
//
// Propagation predicates -- the ONLY three ways a defect climbs a rung:
//
//   P1 input:   a defect in an effective Parameter Object, or in its schema
//               closure, makes the OPERATION invalid.
//   P2 output:  a defect leaving a success response declaration that DECLARED
//               a body with no surviving representable media alternative
//               makes the OPERATION invalid.
//   P3 address: a defect in the Paths Object key or in the HTTP-method
//               member makes the OPERATION invalid.
//
// Nothing else climbs. A defective REQUEST media alternative owns its own
// entry and never invalidates its operation.
//
// Whole-source refusal follows the family document's three-part acceptance floor:
// part 1 (the closed load gates) is owned by the load path and never asked
// here; part 2 -- exactly one derived refusal -- is computed by this
// instrument: the source refuses only when no addressable target remains.
// Excluded operations are ADDRESSED targets and never trigger it. An
// artifact that conformantly declares no target is accepted with an empty
// interface. A Paths Object member that is an EXTERNAL Reference Object
// defers the question to resolution.
//
// The shipped defect roster is the 8b-reviewed class table (D1-D13 + D1s;
// D1n/D1a referred, never emitted), plus the registry-scoped class D15 -- a
// Schema Object keyword whose value violates the governing dialect's declared
// JSON type for it, whose answer is stated once in the reviewed owning-unit
// policy and is the same as D1's. Additionally, an internal reference that
// identifies no location in the entry document invalidates each unit whose
// closure reaches the referencing site (P1/P2 per position); a pointer whose
// traversal stops ON another Reference Object is the edition-split traversal
// question and is never a defect here.

export const INVALID_UNIT_REASON_CODE = "openapi.invalid_unit";

const EDITIONS_30 = new Set(["3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4"]);
const EDITIONS_31 = new Set(["3.1.0", "3.1.1", "3.1.2"]);

// The 3.0 line: OAS 3.0.4 §4.4 enumerates the six type names and directs
// "null" to `nullable`; 3.0.0-3.0.3 state "null is not supported as a type".
const TYPES_30 = new Set(["boolean", "object", "array", "number", "string", "integer"]);
// The 3.1 line: JSON Schema Validation 2020-12 §6.1.1.
const TYPES_31 = new Set(["null", "boolean", "object", "array", "number", "string", "integer"]);

const COMPONENT_FIELDS_30 = ["schemas", "responses", "parameters", "examples", "requestBodies", "headers", "securitySchemes", "links", "callbacks"];
const COMPONENT_FIELDS_31 = [...COMPONENT_FIELDS_30, "pathItems"];

// "All the fixed fields declared above are objects that MUST use keys that
// match the regular expression: ^[a-zA-Z0-9\.\-_]+$" (each accepted edition).
const COMPONENT_KEY_RE = /^[a-zA-Z0-9.\-_]+$/;

// RFC 6838 restricted-name grammar, reached through the OAS's "The key is a
// media type or media type range".
const MEDIA_RE = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+\/[!#$%&'*+\-.^_`|~0-9A-Za-z]+(\s*;.*)?$/;

const SUCCESS_CODE_RE = /^2\d\d$/;

// The Schema Object keyword inventory the closure walk follows -- the union
// of both edition lines; a keyword absent from an edition never occurs there.

const FLOOR_METHODS = ["get", "put", "post", "delete", "options", "head", "patch", "trace"];

/** One defective position with its per-(class, edition-line) authority citation. */
export interface FloorDefect {
  class: string;
  position: string;
  authority: string;
}

/** One raw-inventory operation's ladder verdict. */
export interface FloorOp {
  ref: string;
  path: string;
  method: string;
  disposition: "represented" | "invalid" | "excluded-request-media";
  /** For an invalid operation: the climbed defects, sorted by position. */
  defects: FloorDefect[];
  /** Ladder-invalid request media alternatives, altRef -> defects. */
  invalidAlternatives: Map<string, FloorDefect[]>;
  altOrder: string[];
  /** Projection defects per unit (the op's ref or an alternative's ref). */
  projections: Map<string, FloorDefect[]>;
  projOrder: string[];
  requestMediaExcluded: boolean;
}

/** The instrument's result for one entry document. */
export interface AcceptanceFloor {
  edition: string;
  line: "3.0" | "3.1";
  /** Non-empty when §3 part 2's single derived rule fires. */
  refusal: string;
  ops: Map<string, FloorOp>;
  opOrder: string[];
  externalPathItemMembers: number;
}

/** Looks up a target's ladder verdict; null floors answer nothing. */
export function floorOpVerdict(floor: AcceptanceFloor | undefined, ref: string): FloorOp | undefined {
  return floor?.ops.get(ref);
}

// The per-(class, edition-line) authority citation carried byte-identically
// by every engine's emitted entries and pinned by the shared case tables.
function floorAuthority(cls: string, line: "3.0" | "3.1"): string {
  const is30 = line === "3.0";
  switch (cls) {
    case "D1":
      return is30
        ? "OAS 3.0 line, Data Types / Schema Object: `type` names one of boolean, object, array, number, string, integer; Value MUST be a string"
        : "OAS 3.1 line via JSON Schema Validation 2020-12 §6.1.1: `type` names one of null, boolean, object, array, number, string, integer";
    case "D1s":
      return is30
        ? "OAS 3.0 line, Schema Object: `type` — Value MUST be a string"
        : "JSON Schema Validation 2020-12 §6.1.1: the value of `type` MUST be either a string or an array of strings";
    case "D2":
      return "OAS, Paths Object: each patterned field holds a Path Item Object";
    case "D2b":
      return "OAS, Path Item Object: each HTTP-method field holds an Operation Object";
    case "D4":
      return "OAS, Components Object: each fixed field is a map of named objects; a member map whose single key is `$ref` declares no component, so every pointer beneath it identifies no location";
    case "D5":
      return "OAS, OpenAPI Object: the field holds its declared object; no Reference Object alternative is named at this position";
    case "D6":
      return "OAS, Components Object / Response Object: a `components.responses` member is a Response Object, whose `description` is REQUIRED; a reference from a schema position denotes the value at the pointer";
    case "D7":
      return "OAS, Responses Object: each member holds a Response Object or a Reference Object resolving to one";
    case "D8":
      return "OAS, content maps: the key is a media type or media type range (RFC 6838 restricted-name grammar via RFC 7231 Appendix D)";
    case "D9":
      return is30
        ? "OAS 3.0 line, Response Object: `description` is REQUIRED"
        : "OAS 3.1 line, Response Object: `description` is REQUIRED";
    case "D10":
      return "OAS, Parameter Object: `name` and `in` are REQUIRED";
    case "D11":
      return "OAS, Components Object: fixed-field maps MUST use keys matching ^[a-zA-Z0-9\\.\\-_]+$";
    case "D12":
      return "OAS, content maps: each media type key's value is a Media Type Object; no Reference Object alternative is named at this position";
    case "D13":
      return "OAS, Paths Object: each patterned field key begins with a forward slash and is appended to the Server Object's url to construct the target URL";
    case "D15":
      return is30
        ? "OAS 3.0 line, Schema Object: a keyword's value carries the JSON type the governing dialect declares for it -- `items` Value MUST be an object and not an array; `properties` definitions MUST be a Schema Object; `required` and `enum` are taken directly from JSON Schema, where `required` is an array of unique elements and `enum` is an array"
        : "OAS 3.1 line, §4.8.24.1: absent `jsonSchemaDialect` the OAS dialect schema id MUST be used for Schema Objects, and the value fails that dialect (https://spec.openapis.org/oas/3.1/dialect/base)";
    case "D16":
      return is30
        ? "OAS 3.0 line, Response Object: `description` is a string, `content` is a map of Media Type Objects, `headers` a map of Header Objects, `links` a map of Link Objects; a fixed field carrying another JSON kind, or a `headers` member that is not a Header Object, violates the Response Object's fixed-field constraints"
        : "OAS 3.1 line, Response Object: `description` is a string, `content` is a map of Media Type Objects, `headers` a map of Header Objects, `links` a map of Link Objects; a fixed field carrying another JSON kind, or a `headers` member that is not a Header Object, violates the Response Object's fixed-field constraints";
    case "URef":
      return is30
        ? "OAS 3.0 line, Reference Object: $ref follows JSON Reference; its fragment is a JSON Pointer (RFC 6901) and identifies no location in the entry document"
        : "OAS 3.1 line, Reference Object: $ref is a URI-Reference whose JSON Pointer fragment (RFC 6901) identifies no location in the entry document";
  }
  return "";
}

import {
  SCHEMA_SUB_LIST,
  SCHEMA_SUB_MAP,
  SCHEMA_SUB_SINGLE,
  schemaObjectDefects,
} from "./schema-dialect.js";

type Obj = Record<string, unknown>;

const isObj = (v: unknown): v is Obj => v !== null && typeof v === "object" && !Array.isArray(v);
const isRefObj = (v: unknown): boolean => isObj(v) && typeof v["$ref"] === "string";
const refString = (v: unknown): string => (isObj(v) && typeof v["$ref"] === "string" ? (v["$ref"] as string) : "");
// A Response Object is REQUIRED to carry `description` in all eight accepted
// editions; its absence is a decidable proof that a value is not one.
const isResponseObject = (v: unknown): boolean => isObj(v) && typeof v["description"] === "string";

// The Response Object's fixed fields other than `description`.
const RESPONSE_OBJECT_FIXED_FIELDS = ["content", "headers", "links"] as const;

// The D16 defects a Response Object owns: a fixed field carrying a JSON kind
// the OAS does not declare for it, or a `headers` member that is not a Header
// Object.
//
// A DECIDABLE test, deliberately not a validator. Each of `content`, `headers`,
// and `links` is a MAP in every accepted edition, so a present non-map is a
// violation no edition disjunction can rescue; a `headers` member is a Header
// Object or a Reference Object, so a present non-object is the same kind of
// proof. Nothing here inspects a Header Object's own fields: that is a
// declaration question the response rungs already own, and a wrong answer
// would cost a target its representation.
//
// `base` is the pointer of the Response Object itself -- the response member's
// pointer for an inline response, the referenced object's pointer for a
// `$ref`ed one -- so a defect is positioned where it actually is.
function responseFixedFieldDefects(
  value: unknown,
  base: string,
  line: "3.0" | "3.1",
): FloorDefect[] {
  if (!isObj(value)) return [];
  const out: FloorDefect[] = [];
  const authority = floorAuthority("D16", line);
  // `description` is REQUIRED and its value MUST be a string. ABSENCE is D9's
  // finding and keeps D9's declared-content gate -- that is the `{}` carve-out.
  // A PRESENT `description` of another kind is not the carve-out: it is a
  // fixed-field violation like any other and excludes the target.
  if (Object.hasOwn(value, "description") && typeof value["description"] !== "string") {
    out.push({ class: "D16", position: `${base}/description`, authority });
  }
  for (const field of RESPONSE_OBJECT_FIXED_FIELDS) {
    if (!Object.hasOwn(value, field)) continue;
    const raw = value[field];
    if (!isObj(raw)) {
      out.push({ class: "D16", position: `${base}/${field}`, authority });
      continue;
    }
    if (field !== "headers") continue;
    for (const name of sortedKeys(raw)) {
      if (name.startsWith("x-")) continue;
      if (!isObj(raw[name])) {
        out.push({ class: "D16", position: `${base}/headers/${esc(name)}`, authority });
      }
    }
  }
  out.sort((a, b) => (a.position < b.position ? -1 : a.position > b.position ? 1 : 0));
  return out;
}

const esc = (token: string): string => token.replaceAll("~", "~0").replaceAll("/", "~1");

const sortedKeys = (m: Obj | undefined): string[] => (m ? Object.keys(m).sort() : []);

interface Resolution {
  resolved: boolean;
  stoppedOnReference: boolean;
  malformedFragment: boolean;
  value?: unknown;
}

// Resolves a same-document reference over the raw tree. A walk that stops ON
// a node that is itself a Reference Object is reported distinctly and never
// emitted as dangling (the edition-split traversal question).
function resolveInternal(root: Obj, ref: string): Resolution {
  if (!ref.startsWith("#")) return { resolved: false, stoppedOnReference: false, malformedFragment: false };
  const frag = ref.slice(1);
  if (frag === "" || frag === "/") return { resolved: true, stoppedOnReference: false, malformedFragment: false, value: root };
  if (!frag.startsWith("/")) return { resolved: false, stoppedOnReference: false, malformedFragment: true };
  let cur: unknown = root;
  for (const raw of frag.slice(1).split("/")) {
    let decoded = raw;
    if (decoded.includes("%")) {
      try {
        decoded = decodeURIComponent(decoded);
      } catch {
        // keep the literal token
      }
    }
    decoded = decoded.replaceAll("~1", "/").replaceAll("~0", "~");
    const stopped = isRefObj(cur);
    if (Array.isArray(cur)) {
      const index = /^\d+$/.test(decoded) ? Number(decoded) : NaN;
      if (!Number.isInteger(index) || index < 0 || index >= cur.length) {
        return { resolved: false, stoppedOnReference: stopped, malformedFragment: false };
      }
      cur = cur[index];
    } else if (isObj(cur)) {
      if (!(decoded in cur)) return { resolved: false, stoppedOnReference: stopped, malformedFragment: false };
      cur = cur[decoded];
    } else {
      return { resolved: false, stoppedOnReference: stopped, malformedFragment: false };
    }
  }
  return { resolved: true, stoppedOnReference: false, malformedFragment: false, value: cur };
}

const sortDefects = (ds: FloorDefect[]): FloorDefect[] => ds.sort((a, b) => (a.position < b.position ? -1 : a.position > b.position ? 1 : 0));

function part2Refusal(detail: string): string {
  return `whole-source refusal: no addressable target remains: ${detail}`;
}

// The entry messages, byte-identical in every engine.
export function floorInvalidTargetMessage(n: number): string {
  return `invalid target: ${n} defective position(s) under the governing edition invalidate this operation`;
}

export function floorInvalidAlternativeMessage(n: number): string {
  return `invalid request media alternative: ${n} defective position(s) under the governing edition invalidate this alternative`;
}

export function floorProjectionMessage(n: number): string {
  return `${n} invalid position(s) are reached by or recorded against this unit; the unit survives`;
}

/** Renders defects for a coverage entry's details member (key order matches Go's sorted-map marshaling). */
export function floorDefectDetails(ds: FloorDefect[]): Array<{ authority: string; position: string }> {
  return ds.map((d) => ({ authority: d.authority, position: d.position }));
}

/**
 * Computes the acceptance floor over a raw entry-document tree. Returns
 * undefined when the root is not an object or the edition is not accepted:
 * those verdicts belong to §3 part 1's closed load gates, which the load
 * path owns.
 */
export function computeAcceptanceFloor(raw: unknown): AcceptanceFloor | undefined {
  if (!isObj(raw)) return undefined;
  const root = raw;
  const edition = typeof root["openapi"] === "string" ? (root["openapi"] as string) : "";
  let line: "3.0" | "3.1";
  if (EDITIONS_30.has(edition)) line = "3.0";
  else if (EDITIONS_31.has(edition)) line = "3.1";
  // The edition gate (part 1) owns every other value.
  //
  // THE 2.0 AND 3.2 LANES HAVE NO LADDER, and that is debt rather than design.
  // Both reach their declaration verdicts ad hoc -- 3.2 over its raw overlay,
  // 2.0 in its native lane -- so where they happen to agree with this instrument
  // they agree for a different reason, and where an edition genuinely differs
  // (OAS 3.2.0 dropped `REQUIRED` from the Response Object's `description`; see
  // `openAPI32ResponseDescriptionGate` in `openapi32-artifact.ts`) nothing here
  // records that it is an edition difference rather than an unexplained
  // divergence. Round R measured the cost of the gap in both directions and
  // queued Round R2 to close it: an acceptance floor for 2.0 and one for 3.2.
  else return undefined;

  const floor: AcceptanceFloor = { edition, line, refusal: "", ops: new Map(), opOrder: [], externalPathItemMembers: 0 };
  // The D16 defects a Responses Object member owns, keyed by that MEMBER's
  // pointer rather than by the defective position, because the rung that reads
  // them asks a question about the member: does this governing Response Object
  // violate the Response Object's fixed-field constraints? The defects are
  // positioned at the offending field (or, for a `$ref`ed response, inside the
  // referenced Response Object), which is not derivable from the member
  // pointer, so the two cannot be collapsed.
  const responseFixedFields = new Map<string, FloorDefect[]>();
  const addResponseFixedFields = (rptr: string, ds: FloorDefect[]): void => {
    if (ds.length === 0) return;
    responseFixedFields.set(rptr, [...(responseFixedFields.get(rptr) ?? []), ...ds]);
  };

  const componentFields = line === "3.0" ? COMPONENT_FIELDS_30 : COMPONENT_FIELDS_31;
  const types = line === "3.0" ? TYPES_30 : TYPES_31;

  const defect = (cls: string, position: string): FloorDefect => ({ class: cls, position, authority: floorAuthority(cls, line) });

  // ---- detection ----------------------------------------------------------

  const defectPositions = new Map<string, FloorDefect>();
  const projectionPositions = new Map<string, FloorDefect>();
  let destroyedDeclared = false;

  const addDefect = (d: FloorDefect): void => {
    if (!defectPositions.has(d.position)) defectPositions.set(d.position, d);
  };

  const pathsValue = root["paths"];
  const pathsDeclared = "paths" in root;
  const pathsIsRef = isRefObj(pathsValue);
  if (pathsIsRef) destroyedDeclared = true; // D5 at #/paths

  const comps = isObj(root["components"]) ? (root["components"] as Obj) : undefined;
  if (comps) {
    for (const fieldName of componentFields) {
      const memberMap = comps[fieldName];
      if (!isObj(memberMap)) continue;
      const keys = sortedKeys(memberMap);
      if (keys.length === 1 && keys[0] === "$ref") {
        addDefect(defect("D4", `#/components/${fieldName}`));
        continue; // D4 subsumes the key-grammar reading of the same byte
      }
      for (const key of keys) {
        if (!COMPONENT_KEY_RE.test(key)) {
          const ptr = `#/components/${esc(fieldName)}/${esc(key)}`;
          projectionPositions.set(ptr, defect("D11", ptr));
        }
      }
    }
    const responses = comps["responses"];
    if (isObj(responses)) {
      for (const key of sortedKeys(responses)) {
        if (key === "$ref") continue;
        const value = responses[key];
        if (isRefObj(value)) continue;
        if (!isResponseObject(value)) {
          const ptr = `#/components/responses/${esc(key)}`;
          projectionPositions.set(ptr, defect("D6", ptr));
        }
      }
    }
  }

  const paths = isObj(pathsValue) && !pathsIsRef ? (pathsValue as Obj) : undefined;

  interface RawOpRow {
    path: string;
    method: string;
    ref: string;
    value: unknown;
    pathItem: Obj;
    pathKeyBad: boolean;
  }
  const rawOps: RawOpRow[] = [];
  if (paths) {
    for (const pathKey of sortedKeys(paths)) {
      if (pathKey.startsWith("x-")) continue;
      const pathValue = paths[pathKey];
      const pathKeyBad = !pathKey.startsWith("/");
      if (pathKeyBad) addDefect(defect("D13", `#/paths/${esc(pathKey)}`));
      if (!isObj(pathValue)) {
        destroyedDeclared = true; // D2: a declared member that is not a Path Item Object
        continue;
      }
      const pathItem = pathValue;
      if (isRefObj(pathValue) && !refString(pathValue).startsWith("#")) {
        floor.externalPathItemMembers++;
      }
      for (const method of FLOOR_METHODS) {
        if (!(method in pathItem)) continue;
        const opValue = pathItem[method];
        const ref = `#/paths/${esc(pathKey)}/${method}`;
        rawOps.push({ path: pathKey, method, ref, value: opValue, pathItem, pathKeyBad });
        if (!isObj(opValue)) {
          addDefect(defect("D2b", ref));
          continue;
        }
        const opMap = opValue;
        // D10: Parameter Objects omitting name or in.
        for (const holder of [
          { list: pathItem["parameters"], ptr: `#/paths/${esc(pathKey)}/parameters` },
          { list: opMap["parameters"], ptr: `${ref}/parameters` },
        ]) {
          if (!Array.isArray(holder.list)) continue;
          holder.list.forEach((p, i) => {
            if (isRefObj(p) || !isObj(p)) return;
            if (!("name" in p) || !("in" in p)) addDefect(defect("D10", `${holder.ptr}/${i}`));
          });
        }
        // Responses: D7 / D9 / D16 / D8 / D12.
        const responses = opMap["responses"];
        if (isObj(responses)) {
          for (const code of sortedKeys(responses)) {
            if (code.startsWith("x-")) continue;
            const rptr = `${ref}/responses/${esc(code)}`;
            const rv = responses[code];
            if (isRefObj(rv)) {
              const r = resolveInternal(root, refString(rv));
              if (r.resolved && !isResponseObject(r.value)) {
                addDefect(defect("D7", rptr));
                continue;
              }
              // A `$ref`ed Response Object governs its referencing target
              // exactly as an inline one does, so its fixed-field constraints
              // are read at the position they actually occupy -- inside the
              // referenced object -- and owned by every member that denotes it.
              if (r.resolved) {
                const ds = responseFixedFieldDefects(r.value, refString(rv), line);
                for (const d of ds) addDefect(d);
                addResponseFixedFields(rptr, ds);
              }
              continue;
            }
            if (!isObj(rv)) {
              addDefect(defect("D7", rptr));
              continue;
            }
            if (!isResponseObject(rv)) addDefect(defect("D9", rptr));
            {
              const ds = responseFixedFieldDefects(rv, rptr, line);
              for (const d of ds) addDefect(d);
              addResponseFixedFields(rptr, ds);
            }
            const content = rv["content"];
            if (isObj(content)) {
              for (const mediaKey of sortedKeys(content)) {
                const mptr = `${rptr}/content/${esc(mediaKey)}`;
                if (!MEDIA_RE.test(mediaKey)) addDefect(defect("D8", mptr));
                if (isRefObj(content[mediaKey])) addDefect(defect("D12", mptr));
              }
            }
          }
        }
        // Request body content keys: D8 / D12.
        const rb = opMap["requestBody"];
        if (isObj(rb) && !isRefObj(rb)) {
          const content = rb["content"];
          if (isObj(content)) {
            for (const mediaKey of sortedKeys(content)) {
              const mptr = `${ref}/requestBody/content/${esc(mediaKey)}`;
              if (!MEDIA_RE.test(mediaKey)) addDefect(defect("D8", mptr));
              if (isRefObj(content[mediaKey])) addDefect(defect("D12", mptr));
            }
          }
        }
      }
    }
  }

  // D1 / D1s / D15 over every reachable Schema Object position. D1n/D1a (the
  // two 3.1-shaped null spellings on the 3.0 line) are referred, never emitted.
  const visitSchema = (node: Obj, ptr: string): void => {
    // D1 / D1s -- the `type` gate. It runs FIRST so that `type` keeps its own
    // class and its own citation: a position already carrying a defect keeps
    // the class that reached it (addDefect is first-wins), and D15's dialect
    // verdict below reaches `/type` too on the 3.1 line.
    if ("type" in node) {
      const t = node["type"];
      if (typeof t === "string") {
        // D1n (the 3.0 line's `type: "null"`) is referred.
        if (!(line === "3.0" && t === "null") && !types.has(t)) addDefect(defect("D1", `${ptr}/type`));
      } else if (Array.isArray(t)) {
        // D1a (the 3.0 line's array-valued `type`) is referred.
        if (line !== "3.0") {
          for (const member of t) {
            if (typeof member !== "string" || !types.has(member)) {
              addDefect(defect("D1", `${ptr}/type`));
              break;
            }
          }
        }
      } else {
        addDefect(defect("D1s", `${ptr}/type`));
      }
    }

    // D15 -- a Schema Object keyword whose value violates the governing
    // dialect's declared JSON type for it. Same derivation as D1/D1s: the
    // nearest rung containing the Schema Object owns it, and P1/P2 decide
    // whether it climbs. The verdict itself is not decided here: on the 3.1
    // line it is delegated to the OAS dialect's own published artifact and on
    // the 3.0 line it is a labeled, guarded transcription of that edition's own
    // sentences, because that line's dialect published no meta-schema to point
    // at. See schema-dialect.ts.
    for (const position of schemaObjectDefects(node, line)) addDefect(defect("D15", `${ptr}${position}`));
  };
  const schemaRoots: Array<{ value: unknown; ptr: string }> = [];
  if (comps) {
    const schemas = comps["schemas"];
    if (isObj(schemas)) {
      for (const key of sortedKeys(schemas)) schemaRoots.push({ value: schemas[key], ptr: `#/components/schemas/${esc(key)}` });
    }
  }
  for (const row of rawOps) {
    if (!isObj(row.value)) continue;
    const opMap = row.value;
    for (const holder of [
      { list: row.pathItem["parameters"], ptr: `#/paths/${esc(row.path)}/parameters` },
      { list: opMap["parameters"], ptr: `${row.ref}/parameters` },
    ]) {
      if (!Array.isArray(holder.list)) continue;
      holder.list.forEach((p, i) => {
        if (!isObj(p)) return;
        const schema = p["schema"];
        if (isObj(schema)) schemaRoots.push({ value: schema, ptr: `${holder.ptr}/${i}/schema` });
      });
    }
    const rb = opMap["requestBody"];
    if (isObj(rb)) {
      const content = rb["content"];
      if (isObj(content)) {
        for (const mediaKey of sortedKeys(content)) {
          const mo = content[mediaKey];
          if (isObj(mo) && isObj(mo["schema"])) {
            schemaRoots.push({ value: mo["schema"], ptr: `${row.ref}/requestBody/content/${esc(mediaKey)}/schema` });
          }
        }
      }
    }
    const responses = opMap["responses"];
    if (isObj(responses)) {
      for (const code of sortedKeys(responses)) {
        const rv = responses[code];
        if (!isObj(rv)) continue;
        const content = rv["content"];
        if (!isObj(content)) continue;
        for (const mediaKey of sortedKeys(content)) {
          const mo = content[mediaKey];
          if (isObj(mo) && isObj(mo["schema"])) {
            schemaRoots.push({ value: mo["schema"], ptr: `${row.ref}/responses/${esc(code)}/content/${esc(mediaKey)}/schema` });
          }
        }
      }
    }
  }
  const visitedSchemaPtrs = new Set<string>();
  const walkSchemaTree = (node: unknown, ptr: string): void => {
    if (!isObj(node)) return;
    if (visitedSchemaPtrs.has(ptr)) return;
    visitedSchemaPtrs.add(ptr);
    visitSchema(node, ptr);
    for (const key of SCHEMA_SUB_SINGLE) {
      if (key in node) walkSchemaTree(node[key], `${ptr}/${esc(key)}`);
    }
    for (const key of SCHEMA_SUB_LIST) {
      const children = node[key];
      if (Array.isArray(children)) children.forEach((child, i) => walkSchemaTree(child, `${ptr}/${esc(key)}/${i}`));
    }
    for (const key of SCHEMA_SUB_MAP) {
      const children = node[key];
      if (isObj(children)) for (const name of sortedKeys(children)) walkSchemaTree(children[name], `${ptr}/${esc(key)}/${esc(name)}`);
    }
  };
  for (const sr of schemaRoots) walkSchemaTree(sr.value, sr.ptr);

  // ---- the whole-source gate at the paths member ---------------------------

  if (pathsIsRef || (pathsDeclared && pathsValue !== null && pathsValue !== undefined && !isObj(pathsValue))) {
    floor.refusal = part2Refusal("the paths member cannot carry a Paths Object under the governing edition, so no target can be addressed");
    return floor;
  }
  if (!pathsDeclared || pathsValue === null || pathsValue === undefined) {
    if (line === "3.0") {
      floor.refusal = part2Refusal("the 3.0 line makes paths a REQUIRED fixed field of the OpenAPI Object and the document declares none");
      return floor;
    }
    if (!("components" in root) && !("webhooks" in root)) {
      floor.refusal = part2Refusal("the 3.1 line requires at least one paths, components, or webhooks field and the document declares none");
      return floor;
    }
    return floor; // accepted: a components/webhooks-only document, empty interface
  }

  // ---- the ladder projection ----------------------------------------------

  const isDefective = (ptr: string): FloorDefect | undefined => defectPositions.get(ptr);

  // D4 member maps: a schema-closure `$ref` descending into one is a defect
  // at its REFERENCING site, attributed to the class that explains it.
  const d4Ptrs: string[] = [];
  for (const [pos, d] of defectPositions) if (d.class === "D4") d4Ptrs.push(pos);
  const viaD4 = (ref: string): boolean => d4Ptrs.some((p) => ref === p || ref.startsWith(`${p}/`));

  const closureDefects = (rootPtr: string, rootValue: unknown): { defs: FloorDefect[]; projs: FloorDefect[] } => {
    const defs: FloorDefect[] = [];
    const projs: FloorDefect[] = [];
    const stack: Array<{ node: unknown; ptr: string }> = [{ node: rootValue, ptr: rootPtr }];
    const walked = new Set<string>();
    const foundDef = new Set<string>();
    const foundProj = new Set<string>();
    while (stack.length > 0) {
      const frame = stack.pop()!;
      if (walked.has(frame.ptr)) continue;
      walked.add(frame.ptr);
      if (!isObj(frame.node)) continue;
      const nodeMap = frame.node;
      for (const [pos, d] of defectPositions) {
        if ((pos === frame.ptr || pos.startsWith(`${frame.ptr}/`)) && !foundDef.has(pos)) {
          foundDef.add(pos);
          defs.push(d);
        }
      }
      for (const [pos, d] of projectionPositions) {
        if ((pos === frame.ptr || pos.startsWith(`${frame.ptr}/`)) && !foundProj.has(pos)) {
          foundProj.add(pos);
          projs.push(d);
        }
      }
      const ref = refString(nodeMap);
      if (ref !== "" && ref.startsWith("#")) {
        const r = resolveInternal(root, ref);
        if (viaD4(ref) && !r.resolved) {
          if (!foundDef.has(frame.ptr)) {
            foundDef.add(frame.ptr);
            defs.push({ class: "D4", position: frame.ptr, authority: floorAuthority("D4", line) });
          }
        } else if (r.resolved) {
          stack.push({ node: r.value, ptr: ref });
        } else if (!r.stoppedOnReference) {
          if (!foundDef.has(frame.ptr)) {
            foundDef.add(frame.ptr);
            defs.push({ class: "URef", position: frame.ptr, authority: floorAuthority("URef", line) });
          }
        }
        // stopped-on-reference: the edition-split traversal question; never a defect here.
      }
      for (const key of SCHEMA_SUB_SINGLE) {
        if (key in nodeMap) stack.push({ node: nodeMap[key], ptr: `${frame.ptr}/${esc(key)}` });
      }
      for (const key of SCHEMA_SUB_LIST) {
        const children = nodeMap[key];
        if (Array.isArray(children)) children.forEach((child, i) => stack.push({ node: child, ptr: `${frame.ptr}/${esc(key)}/${i}` }));
      }
      for (const key of SCHEMA_SUB_MAP) {
        const children = nodeMap[key];
        if (isObj(children)) for (const name of sortedKeys(children)) stack.push({ node: children[name], ptr: `${frame.ptr}/${esc(key)}/${esc(name)}` });
      }
    }
    return { defs: sortDefects(defs), projs: sortDefects(projs) };
  };

  let anyDestroyedOrInvalid = destroyedDeclared;
  let representedOrAddressed = 0;

  for (const row of rawOps) {
    const op: FloorOp = {
      ref: row.ref,
      path: row.path,
      method: row.method,
      disposition: "represented",
      defects: [],
      invalidAlternatives: new Map(),
      altOrder: [],
      projections: new Map(),
      projOrder: [],
      requestMediaExcluded: false,
    };
    const climbing: FloorDefect[] = [];
    const addClimb = (...ds: FloorDefect[]): void => {
      for (const d of ds) {
        if (!climbing.some((e) => e.position === d.position)) climbing.push(d);
      }
    };
    const addProjection = (unit: string, ...ds: FloorDefect[]): void => {
      for (const d of ds) {
        const existing = op.projections.get(unit) ?? [];
        if (!existing.some((e) => e.position === d.position)) {
          existing.push(d);
          op.projections.set(unit, existing);
        }
      }
    };

    if (row.pathKeyBad) {
      const d = isDefective(`#/paths/${esc(row.path)}`);
      if (d) addClimb(d); // P3: the Paths Object key
    }
    if (!isObj(row.value)) {
      const d = isDefective(op.ref);
      if (d) addClimb(d); // P3: the HTTP-method member
      op.disposition = "invalid";
      op.defects = sortDefects(climbing);
      floor.ops.set(op.ref, op);
      floor.opOrder.push(op.ref);
      anyDestroyedOrInvalid = true;
      continue;
    }
    const opMap = row.value;

    // P1 -- effective parameters (path-level plus operation-level).
    for (const holder of [
      { list: row.pathItem["parameters"], ptr: `#/paths/${esc(row.path)}/parameters` },
      { list: opMap["parameters"], ptr: `${op.ref}/parameters` },
    ]) {
      if (!Array.isArray(holder.list)) continue;
      holder.list.forEach((p, i) => {
        const pptr = `${holder.ptr}/${i}`;
        const d = isDefective(pptr);
        if (d) {
          addClimb(d);
          return;
        }
        if (isRefObj(p)) {
          const ref = refString(p);
          if (ref.startsWith("#")) {
            const r = resolveInternal(root, ref);
            if (!r.resolved && !r.stoppedOnReference) {
              addClimb({ class: "URef", position: pptr, authority: floorAuthority("URef", line) });
            }
          }
          return;
        }
        if (!isObj(p)) return;
        const schema = p["schema"];
        if (isObj(schema)) {
          const { defs, projs } = closureDefects(`${pptr}/schema`, schema);
          addClimb(...defs);
          addProjection(op.ref, ...projs);
        }
      });
    }

    // Request media alternatives -- units, never climbing.
    let declaredAlternatives = 0;
    const rb = opMap["requestBody"];
    let rbContent: Obj | undefined;
    if (isObj(rb) && !isRefObj(rb) && isObj(rb["content"])) rbContent = rb["content"] as Obj;
    if (rbContent) {
      for (const mediaKey of sortedKeys(rbContent)) {
        const aptr = `${op.ref}/requestBody/content/${esc(mediaKey)}`;
        declaredAlternatives++;
        const d = isDefective(aptr);
        if (d) {
          op.invalidAlternatives.set(aptr, [d]);
          op.altOrder.push(aptr);
          continue;
        }
        const mo = rbContent[mediaKey];
        if (isObj(mo) && isObj(mo["schema"])) {
          const { defs, projs } = closureDefects(`${aptr}/schema`, mo["schema"]);
          if (defs.length > 0) {
            op.invalidAlternatives.set(aptr, defs);
            op.altOrder.push(aptr);
          }
          addProjection(aptr, ...projs);
        }
      }
    }

    // P2 -- success response declarations that declared a body.
    const responses = isObj(opMap["responses"]) ? (opMap["responses"] as Obj) : undefined;
    const codes = sortedKeys(responses).filter((c) => !c.startsWith("x-"));
    const has2XX = codes.includes("2XX");
    const isSuccess = (code: string): boolean => SUCCESS_CODE_RE.test(code) || code === "2XX" || (code === "default" && !has2XX);
    for (const code of codes) {
      const rptr = `${op.ref}/responses/${esc(code)}`;
      const rv = responses![code];
      const respDefect = isDefective(rptr);
      const fixedFieldDefects = responseFixedFields.get(rptr) ?? [];
      if (!isSuccess(code)) {
        // Non-success responses never climb; classification is the 2xx rule
        // (OAPI-P-08), so no success contract is lost.
        if (respDefect) addProjection(op.ref, respDefect);
        for (const d of fixedFieldDefects) addProjection(op.ref, d);
        continue;
      }
      if (isRefObj(rv)) {
        const ref = refString(rv);
        if (ref.startsWith("#")) {
          const r = resolveInternal(root, ref);
          if (!r.resolved && !r.stoppedOnReference) {
            // A dangling success-response reference declares no readable
            // body: an invalid declaration that loses no representation.
            addProjection(op.ref, { class: "URef", position: rptr, authority: floorAuthority("URef", line) });
            continue;
          }
        }
      }
      let declared: string[] = [];
      let rvContent: Obj | undefined;
      if (isObj(rv) && !isRefObj(rv) && isObj(rv["content"])) {
        rvContent = rv["content"] as Obj;
        declared = sortedKeys(rvContent);
      }
      // An upstream-invalid GOVERNING Response Object excludes the selected
      // target before any actual response is inspected: response governance is
      // target-level (`openbindings.openapi-3.1@1` §9.5 and its restored
      // siblings). Two of the three response-rung classes are that defect, and
      // they climb with no further question asked:
      //
      //   D7  -- the member is not a Response Object at all, so there is no
      //          governing declaration to read.
      //   D16 -- the member IS a Response Object and violates the fixed-field
      //          constraints the same sentence names.
      //
      // D9 is deliberately NOT here, and its declared-content gate below is
      // unchanged. A Response Object that omits its REQUIRED `description`
      // while declaring no content loses no representation: nothing about the
      // response body is misdeclared, so the target still carries everything it
      // ever carried. That is the `{}` carve-out the documents state in the
      // same sentence, and it is the whole difference between S1 (invalid) and
      // a bare `{}` (represented).
      if (respDefect && respDefect.class === "D7") {
        addClimb(respDefect); // P2
        continue;
      }
      if (fixedFieldDefects.length > 0) {
        if (respDefect) addClimb(respDefect);
        addClimb(...fixedFieldDefects); // P2
        continue;
      }
      if (respDefect && declared.length === 0) {
        addProjection(op.ref, respDefect);
        continue;
      }
      if (respDefect && declared.length > 0) {
        addClimb(respDefect); // P2
        continue;
      }
      if (declared.length === 0) continue;
      let surviving = 0;
      const deadAlternativeDefects: FloorDefect[] = [];
      for (const mediaKey of declared) {
        const aptr = `${rptr}/content/${esc(mediaKey)}`;
        const d = isDefective(aptr);
        if (d) {
          deadAlternativeDefects.push(d);
          continue;
        }
        let dead = false;
        const mo = rvContent![mediaKey];
        if (isObj(mo) && isObj(mo["schema"])) {
          const { defs, projs } = closureDefects(`${aptr}/schema`, mo["schema"]);
          addProjection(op.ref, ...projs);
          if (defs.length > 0) {
            dead = true;
            deadAlternativeDefects.push(...defs);
          }
        }
        if (!dead) surviving++;
      }
      if (surviving === 0) {
        addClimb(...deadAlternativeDefects); // P2: no surviving representable alternative
      } else if (deadAlternativeDefects.length > 0) {
        // Some response media alternatives are invalid but the response
        // survives: an at-position projection on the operation.
        addProjection(op.ref, ...deadAlternativeDefects);
      }
    }

    let requiredBody = false;
    if (rbContent && isObj(rb) && rb["required"] === true) requiredBody = true;
    const requestMediaExcluded = requiredBody && declaredAlternatives > 0 && declaredAlternatives === op.invalidAlternatives.size;

    for (const [unit, ds] of op.projections) op.projections.set(unit, sortDefects(ds));
    op.projOrder = [...op.projections.keys()].sort();

    if (climbing.length > 0) {
      op.disposition = "invalid";
      op.defects = sortDefects(climbing);
      anyDestroyedOrInvalid = true;
    } else if (requestMediaExcluded) {
      op.disposition = "excluded-request-media";
      op.requestMediaExcluded = true;
      representedOrAddressed++;
    } else {
      op.disposition = "represented";
      representedOrAddressed++;
    }
    floor.ops.set(op.ref, op);
    floor.opOrder.push(op.ref);
  }

  // §3 part 2, the single derived rule: refuse only when no addressable
  // target remains -- zero operations survive as represented or
  // excluded-addressed AND at least one declared target was destroyed or
  // invalidated AND no Paths Object member is an external Reference Object
  // (externals defer the question to resolution).
  if (representedOrAddressed === 0 && anyDestroyedOrInvalid && floor.externalPathItemMembers === 0) {
    floor.refusal = part2Refusal("every position that would have carried an operation object is defective under the governing edition");
  }
  return floor;
}
