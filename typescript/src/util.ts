import type { OpenAPIDocument, OpenAPIParameter } from "./types.js";
import { VALID_METHODS } from "./constants.js";
import { dereference } from "./internal/index.js";
import yaml from "js-yaml";
import { OpenAPIRefSiblingNormalizer } from "./ref-siblings.js";

// The u flag makes the class match whole code points, so an astral-plane
// character replaces as one underscore, not one per surrogate half
// (Go parity: SanitizeKey's regexp operates on runes).
const NON_KEY_CHARS = /[^a-zA-Z0-9._-]/gu;

/**
 * Replaces non-alphanumeric characters in a name with underscores to produce
 * a valid key. The result always matches the OBI-D-03 identifier pattern
 * ^[A-Za-z_][A-Za-z0-9_.-]*$: keys that would start with a digit, '.', or
 * '-' are prefixed with an underscore (Go parity: SanitizeKey).
 */
export function sanitizeKey(name: string): string {
  const key = name.replace(NON_KEY_CHARS, "_").replace(/^_+|_+$/g, "");
  if (!key) return "unnamed";
  return /^[A-Za-z_]/.test(key) ? key : `_${key}`;
}

/** Returns the key as-is if unused, otherwise appends a numeric suffix to make it unique. */
export function uniqueKey(key: string, used: Set<string>): string {
  if (!used.has(key)) return key;
  for (let i = 2; ; i++) {
    const candidate = `${key}_${i}`;
    if (!used.has(candidate)) return candidate;
  }
}

/**
 * Compares strings by Unicode code point: the canonical ordering for
 * synthesis and inspection (Go parity: Go compares strings byte-wise, and
 * UTF-8 byte order is code point order). Neither `localeCompare` (collates
 * under the host locale, so output varies machine to machine) nor default
 * sort / UTF-16 code-unit `<` (ranks astral-plane code points below
 * U+E000..U+FFFF) matches the reference implementation. The order is
 * load-bearing beyond emission: it decides which of two colliding names
 * wins the bare key in {@link uniqueKey}.
 */
export function codePointCompare(a: string, b: string): number {
  let i = 0;
  while (i < a.length && i < b.length) {
    const ca = a.codePointAt(i) as number; // i < a.length, so defined
    const cb = b.codePointAt(i) as number;
    if (ca !== cb) return ca < cb ? -1 : 1;
    i += ca > 0xffff ? 2 : 1;
  }
  return a.length - b.length;
}

/**
 * Parses a binding ref per OAPI-D-03: a JSON Pointer of the exact form
 * `#/paths/<escaped-path>/<method>` addressing an operation object. The
 * path segment carries RFC 6901 escaping ("/" → "~1", "~" → "~0"), and the
 * method is lowercase exactly as the artifact spells it — an uppercase
 * method is non-conformant and refused, never case-folded.
 */
export function parseRef(ref: string): { path: string; method: string } {
  const prefix = "#/paths/";
  if (!ref.startsWith(prefix)) {
    throw new Error(
      `ref "${ref}" must be a JSON Pointer of the form #/paths/<escaped-path>/<method> (OAPI-D-03)`,
    );
  }
  const parts = ref.slice(prefix.length).split("/");
  if (parts.length !== 2) {
    throw new Error(
      `ref "${ref}" must be a JSON Pointer of the form #/paths/<escaped-path>/<method>: the path segment carries RFC 6901 escaping ("/" → "~1") (OAPI-D-03)`,
    );
  }
  // parts.length === 2 was just checked, so both segments exist; the ""
  // fallbacks are unreachable (and "" would refuse at the method check).
  const escapedPath = parts[0] ?? "";
  const method = parts[1] ?? "";
  if (!VALID_METHODS.has(method)) {
    if (VALID_METHODS.has(method.toLowerCase())) {
      throw new Error(
        `ref "${ref}": method "${method}" must be lowercase exactly as the artifact spells it (OAPI-D-03)`,
      );
    }
    throw new Error(`invalid HTTP method "${method}" in ref`);
  }

  // RFC 6901 unescaping, in order: ~1 first, then ~0.
  const path = escapedPath.replaceAll("~1", "/").replaceAll("~0", "~");
  return { path, method };
}

/** Builds a JSON Pointer ref string from a path and HTTP method, escaping special characters. */
export function buildJsonPointerRef(path: string, method: string): string {
  const escaped = path.replaceAll("~", "~0").replaceAll("/", "~1");
  return `#/paths/${escaped}/${method.toLowerCase()}`;
}

/**
 * Loads and discriminates an OpenAPI source per openbindings.openapi@1
 * §3–§6: `content`, when present, is the artifact (content primacy), with a
 * co-present `location` serving as the embedded artifact's BASE URI —
 * relative $refs resolve against it exactly as they would had the document
 * been retrieved from that address (OAPI-D-01/D-02, §6). Embedded content
 * with no location has no base and must be self-contained: a relative
 * external $ref then fails with a readable error (absolute http(s) $refs
 * still resolve — they need no base). The artifact's own `openapi` field
 * discriminates the accepted editions (OAPI-P-01).
 *
 * String content parses as YAML 1.2 (JSON being a valid subset); duplicate
 * mapping keys are refused loudly by the YAML layer itself, satisfying the
 * §3 duplicate-key pin.
 *
 * The document is fully dereferenced before it reaches invocation or
 * synthesis logic (Go parity: the kin-openapi loader resolves every `$ref`
 * once, at load time — path items included — so downstream code always
 * sees direct values, never a `{"$ref": ...}` indirection). External refs
 * are followed via `fetchFn` (or the global `fetch`) unless
 * `options.allowExternalRefs` is explicitly `false`, which keeps the parse
 * side-effect-free (Go parity: `prepareBinding`'s content path disables
 * external I/O — "never fetches").
 */
export async function loadOpenAPIDocument(
  location?: string,
  content?: unknown,
  options?: {
    signal?: AbortSignal;
    allowExternalRefs?: boolean;
    /** Refuse schemas whose dialect cannot be projected faithfully into OBI. */
    requirePortableSchemaDialect?: boolean;
    /**
     * Receives every document this load composed — the artifact first, then
     * each document reached through an external `$ref` — with its base
     * address. Synthesis uses it to recover an externally-declared
     * component's own name (componentSchemaNames).
     */
    onResource?: (root: Record<string, unknown>, baseURI?: string) => void;
    /**
     * Receives every node this load reached through a `$ref`, with the root of
     * the document that declares it and its RFC 6901 pointer there.
     * Dereferencing erases which nodes the artifact ADDRESSED; synthesis reads
     * them back to decide what may become a cut point (componentSchemaNames).
     */
    onRefTarget?: (
      target: object,
      declaringRoot: Record<string, unknown>,
      pointer: string,
    ) => void;
  },
  fetchFn?: typeof globalThis.fetch,
): Promise<OpenAPIDocument> {
  // `location`, when present, must be an absolute URI (OAPI-D-02) —
  // whether it is the fetch target or only the embedded content's base.
  // A bare filesystem path is refused loudly before any fetch (the Go
  // loader's posture; the former "local tooling" lenience is gone).
  if (location) validateDocumentAddress(location);
  let retrievalLocation = location;

  let raw: unknown;
  if (content !== undefined) {
    if (typeof content === "string") raw = parseJSONOrYAML(content);
    else if (typeof content === "object") raw = content;
    else raw = structuredClone(content);
  } else {
    if (!location) {
      throw new Error("source must have location or content");
    }

    const resp = await (fetchFn ?? fetch)(location, { signal: options?.signal });
    if (!resp.ok) {
      throw new Error(
        `failed to fetch ${location}: ${resp.status} ${resp.statusText}`,
      );
    }
    retrievalLocation = resp.url || location;

    let text: string;
    try {
      text = await resp.text();
    } catch (e: unknown) {
      throw new Error(
        `failed to read response body from ${location}: ${errorMessage(e)}`,
        { cause: e },
      );
    }

    try {
      raw = parseJSONOrYAML(text);
    } catch {
      const preview = text.length > 120 ? text.slice(0, 120) + "..." : text;
      throw new Error(`failed to parse response from ${location}: ${preview}`);
    }
  }

  checkAcceptedOpenAPIVersion(raw);
  const openapiVersion = (raw as Record<string, unknown>).openapi as string;
  const normalizer = new OpenAPIRefSiblingNormalizer(
    openapiVersion,
    options?.requirePortableSchemaDialect === true,
  );
  const normalized = normalizer.normalize(
    raw,
    retrievalLocation,
    location && location !== retrievalLocation ? [location] : [],
  );

  const allowExternalRefs = options?.allowExternalRefs ?? true;
  let refFetch: typeof globalThis.fetch;
  if (!allowExternalRefs) {
    refFetch = blockExternalRefFetch;
  } else if (!location && content !== undefined) {
    refFetch = selfContainedRefFetch(fetchFn ?? fetch);
  } else {
    refFetch = fetchFn ?? fetch;
  }
  refFetch = normalizeExternalRefFetch(refFetch, normalizer);
  const dereferenced = await dereference<OpenAPIDocument>(normalized as Record<string, unknown>, {
    baseUrl: retrievalLocation,
    parse: parseJSONOrYAML,
    signal: options?.signal,
    fetch: refFetch,
    mergeRefSiblings: (target, reference) => normalizer.mergeReferenceObject(target, reference),
    prepareRefTarget: (root, target) => normalizer.prepareTarget(root, target.resourceURI),
    onResource: options?.onResource,
    onRefTarget: options?.onRefTarget,
  });
  normalizer.restore(dereferenced);
  return dereferenced;
}

/**
 * Normalizes each fetched reference resource while both its requested and
 * final retrieval URI are available. The requested URI locates target-kind
 * hints recorded by the referring document; the final URI is the base for
 * nested relative references after redirects.
 */
function normalizeExternalRefFetch(
  real: typeof globalThis.fetch,
  normalizer: OpenAPIRefSiblingNormalizer,
): typeof globalThis.fetch {
  return async (input: RequestInfo | URL, init?: RequestInit) => {
    const requested = input instanceof Request ? input.url : String(input);
    const response = await real(input, init);
    if (!response.ok) return response;
    const retrieval = response.url || requested;
    const parsed = parseJSONOrYAML(await response.text());
    const normalized = normalizer.normalize(
      parsed,
      retrieval,
      requested !== retrieval ? [requested] : [],
    );
    const wrapped = new Response(JSON.stringify(normalized), {
      status: response.status,
      statusText: response.statusText,
      headers: response.headers,
    });
    Object.defineProperty(wrapped, "url", { value: retrieval });
    return wrapped;
  };
}

/**
 * Checks OAPI-D-02's location grammar offline, without dereferencing:
 * `location`, when present, is an absolute URI addressing the OpenAPI
 * document itself. A bare filesystem path is a relative reference in form
 * (core OBI-D-05) and is refused — a local artifact is addressed as
 * file:// or embedded as the source's content.
 */
export function validateDocumentAddress(location: string): void {
  try {
    new URL(location);
  } catch {
    throw new Error(
      `openapi location ${JSON.stringify(location)} is not an absolute URI addressing the document (OAPI-D-02): a local artifact is addressed as file:// or embedded as the source's content`,
    );
  }
}

/**
 * Discriminates the exact accepted editions per OAPI-P-01. Patch-looking
 * values outside the frozen set are not inferred compatible.
 */
function checkAcceptedOpenAPIVersion(raw: unknown): void {
  const doc = raw as Record<string, unknown> | null;
  const v = doc && typeof doc === "object" ? doc["openapi"] : undefined;
  if (typeof v !== "string" || v === "") {
    throw new Error(
      "document declares no `openapi` field: openbindings.openapi@1 requires one of its exact accepted OpenAPI editions (OAPI-P-01; Swagger 2.0 is not accepted)",
    );
  }
  if (!ACCEPTED_OPENAPI_VERSIONS.has(v)) {
    throw new Error(
      `unsupported OpenAPI version "${v}": openbindings.openapi@1 accepts exactly 3.0.0–3.0.4 and 3.1.0–3.1.2 (OAPI-P-01)`,
    );
  }
}

const ACCEPTED_OPENAPI_VERSIONS = new Set([
  "3.0.0",
  "3.0.1",
  "3.0.2",
  "3.0.3",
  "3.0.4",
  "3.1.0",
  "3.1.1",
  "3.1.2",
]);

/**
 * Rejects any external `$ref` fetch. Used to keep a content-only document
 * parse side-effect-free (Go parity: `prepareBinding`'s content path never
 * touches the network — internal `#/...` refs still resolve locally).
 */
const blockExternalRefFetch: typeof globalThis.fetch = () => {
  throw new Error("external $ref resolution is disabled for this load");
};

/**
 * Allows absolute http(s) reference targets (they resolve without a base)
 * and refuses everything else: with no co-present location the embedded
 * artifact has no base URI, so a relative reference is unresolvable by
 * definition (§6 — bundle before embedding).
 */
function selfContainedRefFetch(
  real: typeof globalThis.fetch,
): typeof globalThis.fetch {
  return (input: RequestInfo | URL, init?: RequestInit) => {
    const url = input instanceof Request ? input.url : String(input);
    if (url.startsWith("http://") || url.startsWith("https://")) {
      return real(input, init);
    }
    throw new Error(
      `reference "${url}" cannot resolve: embedded content with no co-present location has no base URI and must be self-contained (bundle the document before embedding, or set the source's location)`,
    );
  };
}

/** Merges path-level and operation-level parameters, with operation parameters taking precedence. */
export function mergeParameters(
  pathParams?: OpenAPIParameter[],
  opParams?: OpenAPIParameter[],
): OpenAPIParameter[] {
  if (!pathParams?.length) return opParams ?? [];
  if (!opParams?.length) return pathParams ?? [];
  const overridden = new Set<string>();
  for (const p of opParams) {
    if (p?.in && p?.name) overridden.add(`${p.in}:${p.name}`);
  }
  const merged = pathParams.filter(
    (p) => p?.in && p?.name && !overridden.has(`${p.in}:${p.name}`),
  );
  return [...merged, ...opParams];
}

/** Extracts a human-readable error message from an unknown thrown value. */
export function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message;
  return String(e);
}

/**
 * Parses string content as YAML 1.2.2, of which JSON is a valid subset, so
 * one grammar covers both spellings deterministically (§3's string-grammar
 * pin).
 *
 * Plain scalars resolve under the CORE schema's tag resolution (YAML 1.2.2
 * §10.3.2): the null, bool, int and float patterns, and anything matching
 * none of them — a date- or time-shaped scalar among them — is a string.
 * That restriction is the artifact authority's, not a local preference:
 * every accepted OAS edition requires that "Tags MUST be limited to those
 * allowed by [YAML's] JSON schema ruleset" (§4.2), and YAML 1.1's
 * timestamp, merge, binary, omap, pairs and set tags are outside that set.
 * js-yaml's DEFAULT_SCHEMA carries all six, so it resolves tags the OAS
 * forbids; js-yaml 4 exposes the restricted resolution as CORE_SCHEMA (an
 * alias of its JSON_SCHEMA — the same object, and the one the AsyncAPI
 * client already parses under). Duplicate mapping keys stay loud under it
 * — in the JSON spelling too, which JSON.parse would silently last-wins.
 */
function parseJSONOrYAML(text: string): unknown {
  return assertJSONDomain(yaml.load(text.trim(), { schema: yaml.CORE_SCHEMA }));
}

/**
 * Refuses a parsed artifact carrying a value with no JSON image, before a
 * downstream writer can silently substitute one.
 *
 * The operation value domain is JSON (core §5), and the OAS admits YAML
 * precisely to "preserve the ability to round-trip between YAML and JSON
 * formats" (§4.2); a scalar with no JSON image is outside both. YAML 1.2.2
 * §10.3.2 resolves `.inf`, `-.inf` and `.nan` to floats that JSON cannot
 * spell, and a canonical-JSON writer emits `null` for them — the artifact's
 * declared value, destroyed without a diagnostic. Refusing the artifact is
 * the loud, pre-dispatch outcome, and it is what the Go twin already does
 * (its YAML-to-JSON conversion fails on the same values).
 *
 * This is also the standing guard on the parser itself: a resolution change
 * that produced Date, RegExp or undefined nodes again is refused here
 * instead of reaching the boundary as `{}`.
 */
function assertJSONDomain(root: unknown): unknown {
  // An empty document parses to undefined; the accepted-edition check owns
  // that diagnostic, so this guard stays out of its way.
  if (root === undefined) return root;
  const seen = new WeakSet<object>();
  const walk = (value: unknown, path: string): void => {
    if (value === null) return;
    switch (typeof value) {
      case "string":
      case "boolean":
        return;
      case "number":
        if (Number.isFinite(value)) return;
        throw new Error(
          `OpenAPI document value at ${path || "the document root"} is `
          + `${String(value)}, which has no JSON representation; YAML 1.2.2 `
          + "resolves it as a float, and the OpenAPI document model is JSON",
        );
      case "object": {
        const node = value as object;
        if (seen.has(node)) return;
        seen.add(node);
        if (Array.isArray(node)) {
          node.forEach((item, index) => walk(item, `${path}/${index}`));
          return;
        }
        const tag = Object.prototype.toString.call(node);
        if (tag !== "[object Object]") {
          throw new Error(
            `OpenAPI document value at ${path || "the document root"} parsed `
            + `as ${tag}, which is not a JSON value; string content parses `
            + "under YAML 1.2.2 core tag resolution, whose scalars are only "
            + "null, booleans, numbers and strings",
          );
        }
        for (const [key, member] of Object.entries(node)) {
          walk(member, `${path}/${key.replace(/~/gu, "~0").replace(/\//gu, "~1")}`);
        }
        return;
      }
      default:
        throw new Error(
          `OpenAPI document value at ${path || "the document root"} parsed as `
          + `${typeof value}, which is not a JSON value`,
        );
    }
  };
  walk(root, "");
  return root;
}

/**
 * The direct-node half of §9.1's declaration-only object determination.
 * Callers recursively union `allOf` members around this predicate; a schema
 * declaring neither properties nor object type is typeless/non-object at
 * this node.
 */
export function bodySchemaFlattens(schema: Record<string, unknown>): boolean {
  const props = schema["properties"];
  const hasProperties = props != null && typeof props === "object";
  return hasProperties || isObjectTypedSchema(schema);
}

/**
 * Reports whether a body schema is explicitly object-typed (3.0 string
 * form or a single-element 3.1 type array): the object half of
 * bodySchemaFlattens' declaration facts. Mirrors the Go SDK's
 * isObjectTypedSchema (formats/openapi/synthesize.go).
 */
export function isObjectTypedSchema(schema: Record<string, unknown>): boolean {
  const ty = schema["type"];
  if (typeof ty === "string") return ty === "object";
  if (Array.isArray(ty)) return ty.length === 1 && ty[0] === "object";
  return false;
}

// ---------------------------------------------------------------------------
// Cyclic-schema handling (rev 2a)
// ---------------------------------------------------------------------------

/**
 * A component schema as the document that declares it names it.
 *
 * `document` is that document's address (absent for the artifact's own
 * components, which need no qualification) and `pointer` is the RFC 6901
 * pointer it is declared under. Together they are the cut point's identity;
 * see `assignCutPointNames` for what the two engines do with it.
 */
export interface DeclaredComponent {
  readonly name: string;
  readonly document?: string;
  readonly pointer: string;
}

/**
 * A document root and its address, as `dereference`'s `onResource` reported
 * it. The entry document is the first element.
 */
export interface LoadedResource {
  readonly root: Record<string, unknown>;
  readonly baseURI?: string;
}

/**
 * A node the artifact addressed with a `$ref`, as `dereference`'s
 * `onRefTarget` reported it.
 */
export interface AddressedNode {
  readonly node: object;
  readonly declaringRoot: Record<string, unknown>;
  readonly pointer: string;
}

/**
 * Maps the identity of each node the artifact ADDRESSES to how its own document
 * names it. After full dereference, `$ref`s alias the very objects a document
 * declares, so object identity recovers the name a cycle participant was
 * declared under — for the artifact itself and, when the resources it composed
 * are supplied, for every document it reached.
 *
 * Two kinds of node are in this map, and both are here for one reason: they are
 * the nodes that may become cut points.
 *
 *  - Every `components.schemas` entry, named by its own key.
 *  - Every other node reached through a `$ref` (`addressed`), named by the
 *    final RFC 6901 token of the pointer that reaches it. A cycle can close
 *    through `#/components/schemas/Wrapper/properties/inner`, which is a schema
 *    position the artifact wrote and addressed even though it is not a declared
 *    component; the Go twin registers exactly the same node set.
 *
 * Components are collected first, so a component reached ALSO by a longer
 * pointer keeps the name its own declaration gives it. The addressed nodes are
 * then taken in canonical identity order, never in reference-resolution order,
 * so a node addressed by two pointers takes the same one on every run.
 *
 * Without the external documents an externally-declared component is anonymous
 * here, which is how it used to be named by an ordinal instead of by the name
 * its own document gives it.
 */
export function componentSchemaNames(
  doc: Record<string, unknown>,
  resources?: readonly LoadedResource[],
  addressed?: readonly AddressedNode[],
): Map<object, DeclaredComponent> {
  const names = new Map<object, DeclaredComponent>();
  const seen = new Set<object>();
  const addressOf = new Map<object, string | undefined>();
  const collect = (root: Record<string, unknown>, document?: string): void => {
    addressOf.set(root, document);
    if (seen.has(root)) return;
    seen.add(root);
    const components = root["components"];
    const schemas = components && typeof components === "object"
      ? (components as Record<string, unknown>)["schemas"]
      : undefined;
    if (!schemas || typeof schemas !== "object" || Array.isArray(schemas)) return;
    for (const [name, node] of Object.entries(schemas as Record<string, unknown>)) {
      if (node === null || typeof node !== "object") continue;
      // The first declaration wins, and the artifact is always visited first:
      // a component the artifact itself declares is never renamed because some
      // composed document declares an alias of it.
      if (names.has(node)) continue;
      names.set(node, {
        name,
        document,
        pointer: `/components/schemas/${escapeJSONPointerSegment(name)}`,
      });
    }
  };
  collect(doc);
  for (const resource of resources ?? []) collect(resource.root, resource.baseURI);

  if (addressed !== undefined && addressed.length > 0) {
    const remaining = addressed.filter((entry) => !names.has(entry.node));
    remaining.sort((a, b) => codePointCompare(
      `${addressOf.get(a.declaringRoot) ?? ""}\u0000${a.pointer}`,
      `${addressOf.get(b.declaringRoot) ?? ""}\u0000${b.pointer}`,
    ));
    for (const entry of remaining) {
      if (names.has(entry.node)) continue;
      const slash = entry.pointer.lastIndexOf("/");
      const token = slash < 0 ? entry.pointer : entry.pointer.slice(slash + 1);
      if (token === "") continue;
      names.set(entry.node, {
        name: unescapeJSONPointerSegment(token),
        document: addressOf.get(entry.declaringRoot),
        pointer: entry.pointer,
      });
    }
  }
  return names;
}

function unescapeJSONPointerSegment(segment: string): string {
  return segment.replace(/~1/gu, "/").replace(/~0/gu, "~");
}

function escapeJSONPointerSegment(segment: string): string {
  return segment.replace(/~/gu, "~0").replace(/\//gu, "~1");
}

// ---------------------------------------------------------------------------
// Cut-point naming
//
// A cyclic schema is emitted using the dialect's own recursion mechanism: the
// cycle participant is hoisted into the operation schema's `$defs` and every
// occurrence becomes a same-document `$ref` to it. The hoisted member needs a
// key, and that key is SYNTHESIS surface: `openbindings.openapi@1` §10 places
// deterministic generation of OBI documents from OpenAPI artifacts outside the
// specification, and the binding-specification authoring doctrine states that a
// family specification does not define a synthesis naming convention. Nothing
// in Core, in the OpenAPI specification, or in anything either incorporates
// decides it. It is therefore this implementation's convention, and its only
// hard obligation is that the TypeScript and Go engines mint identical keys.
//
// The convention, twinned in openbindings-go/formats/openapi/cutpoint_names.go:
//
//  1. A cut point is named by the component's OWN name in the document that
//     declares it. (This is the convention the AsyncAPI family settled as F7:
//     cut at the artifact's own component name.)
//  2. Names are assigned over the SET of cut points minted for one operation
//     schema, never in traversal order. A name claimed by exactly one cut point
//     is used as written.
//  3. When two or more cut points claim one name, AT MOST ONE claimant keeps it
//     unqualified — the artifact's own component, or, where no artifact
//     component is in the contest, the first claimant in canonical-identity
//     order among those with no declaring document to qualify by. Every other
//     claimant is qualified by the document that declares it: that document's
//     address relative to the artifact's own, extension stripped, every
//     character outside [A-Za-z0-9_.-] replaced by "_", joined to the component
//     name with "_". A claimant whose declaring document is unknown — absent, or
//     recorded as an empty address — cannot be qualified by it and is treated
//     exactly as the artifact's own component here: qualifying it by nothing
//     would mint a name derived from nothing.
//
//     relativeDocumentName decides "relative to the artifact's own" in three
//     cases, because an address is not always a hierarchical path:
//
//       - An address with an OPAQUE path — a scheme whose remainder does not
//         begin with "/", as in `urn:example:one` — has no hierarchical path at
//         all. There is nothing to make relative, no extension to strip, and no
//         part of it that can be dropped without losing what distinguishes it
//         (`urn:example:one` and `tag:example:one` are different documents). It
//         is used verbatim.
//       - A RELATIVE address — no scheme, no authority, no leading "/" — is
//         already expressed relative to the artifact, so the artifact's own
//         address plays no part in reading it. Leading "./" segments carry no
//         information (RFC 3986 spells "no prefix" both ways) and are removed,
//         so one document referenced both ways qualifies identically. "../" is
//         NOT removed: it denotes a different place.
//       - Every other address is hierarchical: it is expressed relative to the
//         artifact's directory when it shares the artifact's origin, prefixed
//         with its authority when it does not, and then has its leading "/" and
//         file extension removed.
//
//  4. If qualification still leaves a name taken, the claimant is suffixed
//     "_2", "_3", … . Claimants are processed in canonical-identity order
//     (document address, NUL, pointer) in code-point order, so the suffix is a
//     function of the identity set rather than of the walk. Rules 2-4 together
//     are TOTAL and INJECTIVE: every cut point receives exactly one key and no
//     two cut points in one operation schema receive the same key. That is not
//     an aesthetic property — `$defs` is a map, so a repeated key drops one
//     definition and silently resolves the other cut point's `$ref` to the
//     survivor.
// ---------------------------------------------------------------------------

function canonicalComponentIdentity(component: DeclaredComponent): string {
  return `${component.document ?? ""}\u0000${component.pointer}`;
}

function sanitizeNameSegment(segment: string): string {
  return segment.replace(/[^A-Za-z0-9_.-]/gu, "_");
}

/**
 * An address as rule 3 reads it. `opaque` marks an address with no hierarchical
 * path at all; `relative` marks one that is already relative to the artifact.
 * The fields mirror Go's `url.URL` (Scheme/Host/Path) because the two engines
 * must read one address the same way.
 */
interface DocumentAddress {
  readonly opaque: boolean;
  readonly relative: boolean;
  readonly scheme: string;
  readonly host: string;
  readonly path: string;
}

/** RFC 3986 §3.1. A relative reference has no scheme. */
const ADDRESS_SCHEME = /^[A-Za-z][A-Za-z0-9+.-]*:/u;

function decodePath(path: string): string {
  // The DECODED path, matching Go's url.URL.Path: a name qualified by a
  // document whose path carries a space must spell that space the same way in
  // both engines.
  try { return decodeURIComponent(path); } catch { return path; }
}

function documentAddress(uri: string): DocumentAddress {
  const opaqueAddress: DocumentAddress =
    { opaque: true, relative: false, scheme: "", host: "", path: "" };
  const scheme = ADDRESS_SCHEME.exec(uri);
  if (scheme !== null) {
    // Go's url.Parse: a scheme whose remainder does not begin with "/" has an
    // opaque, non-hierarchical path (`urn:example:one`). `new URL` would expose
    // that opaque part as a `pathname` and silently drop the scheme with it.
    if (!uri.slice(scheme[0].length).startsWith("/")) return opaqueAddress;
    try {
      const url = new URL(uri);
      return {
        opaque: false,
        relative: false,
        scheme: url.protocol,
        host: url.host,
        path: decodePath(url.pathname),
      };
    } catch {
      return opaqueAddress;
    }
  }
  // A relative reference. Its query and fragment are not part of its path,
  // which is what Go's url.Parse leaves in Path.
  const path = decodePath(uri.replace(/[#?][\s\S]*$/u, ""));
  return { opaque: false, relative: !path.startsWith("/"), scheme: "", host: "", path };
}

/**
 * Expresses a declaring document's address relative to the artifact's own,
 * with any file extension removed. Keeping it relative is what makes a
 * qualified name independent of how the artifact was reached: the same two
 * documents laid out the same way qualify identically whether they were loaded
 * from a checkout or from a server.
 *
 * The three address cases are rule 3's; the Go twin
 * (openbindings-go/formats/openapi/cutpoint_names.go) reads an address the same
 * way, and cutpoint-names.test.ts pins the whole base x document matrix in both
 * engines.
 */
export function relativeDocumentName(base: string | undefined, document: string): string {
  const target = documentAddress(document);
  // An opaque path is not a path: nothing to make relative, nothing to strip.
  if (target.opaque) return document;
  let path = target.path;
  if (target.relative) {
    // Already relative to the artifact; reading it does not involve the
    // artifact's own address at all.
    while (path.startsWith("./")) path = path.slice("./".length);
  } else {
    const origin = base === undefined ? undefined : documentAddress(base);
    if (
      origin !== undefined && !origin.opaque && origin.host === target.host
      && (origin.scheme === target.scheme || origin.scheme === "" || target.scheme === "")
    ) {
      const slash = origin.path.lastIndexOf("/");
      const dir = slash < 0 ? "./" : slash === 0 ? "/" : `${origin.path.slice(0, slash)}/`;
      if (path.startsWith(dir)) path = path.slice(dir.length);
    } else if (target.host !== "") {
      path = target.host + path;
    }
  }
  path = path.replace(/^\//u, "");
  const dot = path.lastIndexOf(".");
  const lastSlash = path.lastIndexOf("/");
  if (dot > lastSlash) path = path.slice(0, dot);
  return path === "" ? document : path;
}

/**
 * A short, stable digest of a possibly-cyclic schema node's shape. It exists
 * only to name an anonymous cut point from what is emitted rather than from a
 * traversal counter; it is not a security or collision-proof hash, and the
 * caller breaks the residual collision deterministically.
 *
 * The walk is cycle-safe: a node already on the path serializes as a
 * back-reference to its depth, so the same shape always serializes the same
 * way and a cycle terminates. Object keys serialize in code-point order.
 */
export function shapeDigest(node: unknown): string {
  const path = new Map<object, number>();
  const write = (value: unknown, depth: number): string => {
    if (value === null || typeof value !== "object") return JSON.stringify(value) ?? "null";
    const back = path.get(value);
    if (back !== undefined) return `^${depth - back}`;
    path.set(value, depth);
    let out: string;
    if (Array.isArray(value)) {
      out = `[${value.map((item) => write(item, depth + 1)).join(",")}]`;
    } else {
      const entries = Object.keys(value as Record<string, unknown>).sort(codePointCompare);
      out = `{${entries
        .map((key) => `${JSON.stringify(key)}:${write((value as Record<string, unknown>)[key], depth + 1)}`)
        .join(",")}}`;
    }
    path.delete(value);
    return out;
  };
  const serialized = write(node, 0);
  // FNV-1a, 32 bit. No crypto dependency: this must run unchanged in a browser.
  let hash = 0x811c9dc5;
  for (let i = 0; i < serialized.length; i += 1) {
    hash ^= serialized.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash.toString(16).padStart(8, "0");
}

/**
 * Reports whether a claimant has a declaring document address for rule 3 to
 * qualify it BY. The artifact's own components have none, and neither does an
 * external reference whose resolver recorded no address.
 */
function isQualifiable(component: DeclaredComponent): boolean {
  return component.document !== undefined && component.document !== "";
}

/**
 * Assigns a `$defs` key to every cut point minted for one operation schema.
 * The result is a function of the cut-point SET, so no traversal order can
 * reach the output, and it is injective: `$defs` is a map, so two cut points
 * sharing a key would drop one definition and resolve the other's `$ref` to
 * the survivor.
 */
export function assignCutPointNames(
  components: readonly DeclaredComponent[],
  base: string | undefined,
): string[] {
  const claimed = new Map<string, number>();
  for (const component of components) {
    claimed.set(component.name, (claimed.get(component.name) ?? 0) + 1);
  }
  // The artifact's own components first, then canonical-identity order: which
  // claimant keeps a contested name unqualified must not depend on the walk
  // either. Array.sort is stable, so two claimants that agree on document AND
  // pointer — the same component by every fact recorded here — keep their
  // given order.
  const rank = (component: DeclaredComponent): number => (component.document === undefined ? 0 : 1);
  const order = components
    .map((_, index) => index)
    .sort((a, b) => (
      rank(components[a]!) - rank(components[b]!)
      || codePointCompare(
        canonicalComponentIdentity(components[a]!),
        canonicalComponentIdentity(components[b]!),
      )
    ));

  const names = new Array<string>(components.length);
  const taken = new Set<string>();
  const kept = new Set<string>();
  const remaining: number[] = [];
  // Every uncontested name is the declaring document's own. Of the contested
  // ones, the claimants that cannot be qualified take the bare name — but only
  // the first of them, or the rule would not be injective.
  for (const index of order) {
    const component = components[index]!;
    if (
      claimed.get(component.name) === 1
      || (!isQualifiable(component) && !kept.has(component.name))
    ) {
      names[index] = component.name;
      taken.add(component.name);
      kept.add(component.name);
      continue;
    }
    remaining.push(index);
  }
  // Contested claimants qualify by their declaring document where they have
  // one, and fall through to rule 4's suffix where they do not or where
  // qualification still lands on a taken name.
  for (const index of remaining) {
    const component = components[index]!;
    const stem = isQualifiable(component)
      ? `${sanitizeNameSegment(relativeDocumentName(base, component.document as string))}_${component.name}`
      : component.name;
    let name = stem;
    for (let attempt = 2; taken.has(name); attempt += 1) name = `${stem}_${attempt}`;
    names[index] = name;
    taken.add(name);
  }
  return names;
}

/**
 * Converts a possibly-cyclic dereferenced schema graph into an equivalent
 * acyclic JSON Schema tree using the dialect's own recursion mechanism:
 * every occurrence of a cycle-participating named component becomes
 * `{"$ref": "<refBase>/$defs/<componentName>"}` (a same-document pointer from the OBI root, per OBI-D-16) and the component's definition is
 * hoisted into `$defs` (itself rewritten under the same rule). An anonymous
 * cycle (one passing through no named component — only possible via a
 * non-component `$ref` target upstream) is hoisted under a deterministic
 * `cycleN` name at its back-edge target.
 *
 * This is incorporation, not invention: `$defs`/`$ref` recursion has
 * identical validation semantics to the infinite unfolding the artifact
 * declares (JSON Schema 2020-12), and the emitted OBI schema graph is
 * self-contained and fully evaluable (core §5.2 / OBI-T-16). Mirrors the Go
 * SDK's inlineRefs cyclic handling (formats/openapi/synthesize.go).
 */
/**
 * Computes, once per document, the set of named component schema nodes that
 * participate in a reference cycle (can reach themselves). decycleSchema
 * consumes this set per embedded schema.
 */
export function cyclicComponents(names: ReadonlyMap<object, unknown>): ReadonlySet<object> {
  const cyclic = new Set<object>();
  for (const node of names.keys()) {
    const visited = new Set<object>();
    const work: unknown[] = [node];
    let reachesSelf = false;
    while (work.length > 0 && !reachesSelf) {
      const current = work.pop();
      if (current === null || typeof current !== "object") continue;
      for (const child of Object.values(current as Record<string, unknown>)) {
        if (child === null || typeof child !== "object") continue;
        if (child === node) { reachesSelf = true; break; }
        if (!visited.has(child)) { visited.add(child); work.push(child); }
      }
    }
    if (reachesSelf) cyclic.add(node);
  }
  return cyclic;
}

/**
 * Computes the set of nodes in `root`'s object graph that can reach
 * themselves (members of a strongly connected component with a cycle).
 * Iterative Tarjan — dereferenced documents can be deep.
 */
function selfReachingSCCs(root: object): ReadonlyArray<ReadonlySet<object>> {
  const index = new Map<object, number>();
  const low = new Map<object, number>();
  const onStack = new Set<object>();
  const sccStack: object[] = [];
  const groups: Array<Set<object>> = [];
  let counter = 0;

  type Frame = { node: object; children: object[]; next: number };
  const childrenOf = (node: object): object[] => {
    const out: object[] = [];
    for (const value of Object.values(node)) {
      if (value !== null && typeof value === "object") out.push(value as object);
    }
    return out;
  };

  const frames: Frame[] = [];
  const push = (node: object): void => {
    index.set(node, counter);
    low.set(node, counter);
    counter += 1;
    sccStack.push(node);
    onStack.add(node);
    frames.push({ node, children: childrenOf(node), next: 0 });
  };
  push(root);

  while (frames.length > 0) {
    const frame = frames[frames.length - 1]!;
    if (frame.next < frame.children.length) {
      const child = frame.children[frame.next]!;
      frame.next += 1;
      if (!index.has(child)) {
        push(child);
      } else if (onStack.has(child)) {
        low.set(frame.node, Math.min(low.get(frame.node) as number, index.get(child) as number));
        if (child === frame.node) groups.push(new Set([frame.node])); // self-loop
      }
    } else {
      frames.pop();
      if (low.get(frame.node) === index.get(frame.node)) {
        const members: object[] = [];
        for (;;) {
          const member = sccStack.pop() as object;
          onStack.delete(member);
          members.push(member);
          if (member === frame.node) break;
        }
        if (members.length > 1) groups.push(new Set(members));
      }
      const parent = frames[frames.length - 1];
      if (parent) {
        low.set(parent.node, Math.min(low.get(parent.node) as number, low.get(frame.node) as number));
      }
    }
  }
  return groups;
}

type SchemaPos = "schema" | "schemaMap" | "schemaArray" | "data";

const DECYCLE_SINGLE_KEYS = new Set([
  "items", "additionalProperties", "not", "if", "then", "else",
  "propertyNames", "contains", "unevaluatedItems", "unevaluatedProperties",
]);
const DECYCLE_MAP_KEYS = new Set([
  "properties", "patternProperties", "$defs", "definitions", "dependentSchemas",
]);
const DECYCLE_ARRAY_KEYS = new Set(["oneOf", "anyOf", "allOf", "prefixItems"]);

export function decycleSchema(
  schema: unknown,
  names: ReadonlyMap<object, DeclaredComponent>,
  refBase: string,
  base?: string,
): unknown {
  if (schema === null || typeof schema !== "object") return schema;

  // Which reachable nodes participate in a cycle? Identity-level, not
  // name-level: sibling-merged $refs produce anonymous copies whose cycles
  // never pass through the named component object, so detection is an SCC
  // computation over the object graph (iterative Tarjan). Within an SCC
  // that contains addressed nodes, only those are hoisted — anonymous
  // intermediates (property maps, array wrappers) inline and the cycle cuts
  // at the addressed node. An SCC with no addressed member hoists all
  // its members, cutting at the first schema-position encounter; the
  // on-stack backstop below covers any unnamed sub-loop.
  const cyclic = new Set<object>();
  for (const group of selfReachingSCCs(schema)) {
    const named = [...group].filter((member) => names.has(member));
    for (const member of named.length > 0 ? named : [...group]) cyclic.add(member);
  }

  // Every cut point is named before the copy walk begins, over the SET of
  // nodes that will be hoisted — never in the order the walk meets them. The
  // named ones carry the name their own document gives them; see
  // assignCutPointNames for the complete convention and its Go twin.
  const defName = new Map<object, string>();
  const declared: DeclaredComponent[] = [];
  const declaredNodes: object[] = [];
  const anonymous: object[] = [];
  for (const node of cyclic) {
    const component = names.get(node);
    if (component === undefined) anonymous.push(node);
    else { declared.push(component); declaredNodes.push(node); }
  }
  assignCutPointNames(declared, base).forEach((name, index) => {
    defName.set(declaredNodes[index] as object, name);
  });
  // An anonymous cut point — a cycle passing through no node the artifact
  // addressed, which a sibling-merged copy can still produce — has no
  // artifact-supplied name to carry. It is named from the shape it hoists
  // rather than from a counter, so the key is still a function of what is
  // emitted and not of the walk.
  const taken = new Set(defName.values());
  const assignName = (node: object): string => {
    const existing = defName.get(node);
    if (existing !== undefined) return existing;
    const stem = `cycle_${shapeDigest(node)}`;
    let name = stem;
    for (let attempt = 2; taken.has(name); attempt += 1) name = `${stem}_${attempt}`;
    taken.add(name);
    defName.set(node, name);
    return name;
  };
  for (const node of anonymous) assignName(node);

  const defs: Record<string, unknown> = {};
  const pendingDefs: object[] = [];
  const stack = new Set<object>();
  // Fully-dereferenced documents are DAGs: shared components appear at many
  // sites. Memoize the copy of every acyclic completed subtree (per position
  // kind) so the output shares structure instead of re-expanding it.
  const memo = new Map<SchemaPos, Map<object, unknown>>();

  const hoistRef = (node: object): Record<string, unknown> => {
    const name = assignName(node);
    if (!Object.hasOwn(defs, name)) {
      defs[name] = null; // reserve; materialized below
      pendingDefs.push(node);
    }
    return { $ref: `${refBase}/$defs/${name}` };
  };

  const childPos = (parentPos: SchemaPos, key: string): SchemaPos => {
    switch (parentPos) {
      case "schema":
        if (DECYCLE_SINGLE_KEYS.has(key)) return "schema";
        if (DECYCLE_MAP_KEYS.has(key)) return "schemaMap";
        if (DECYCLE_ARRAY_KEYS.has(key)) return "schemaArray";
        return "data"; // annotations, enum/const/default values, unknown keywords
      case "schemaMap": return "schema";
      case "schemaArray": return "schema";
      case "data": return "data";
    }
  };

  const copyChildren = (node: object, pos: SchemaPos): unknown => {
    stack.add(node);
    let out: unknown;
    if (Array.isArray(node)) {
      out = node.map((item) => copy(item, pos === "schemaArray" ? "schema" : pos === "data" ? "data" : pos));
    } else {
      const target: Record<string, unknown> = {};
      for (const key of Object.keys(node).sort(codePointCompare)) {
        target[key] = copy((node as Record<string, unknown>)[key], childPos(pos, key));
      }
      out = target;
    }
    stack.delete(node);
    return out;
  };

  const copy = (node: unknown, pos: SchemaPos): unknown => {
    if (node === null || typeof node !== "object") return node;
    const obj = node;
    // Hoisting emits a {$ref} object, which only means "reference" at a
    // schema position. Cycle participants at other positions (a shared
    // properties map behind a sibling-merged reference; annotation data)
    // are walked through — every reference cycle passes through a schema
    // position within one lap, where it is cut.
    if (pos === "schema" && !Array.isArray(obj)) {
      if (cyclic.has(obj)) return hoistRef(obj);
      if (stack.has(obj)) return hoistRef(obj); // anonymous cycle backstop
    } else if (stack.has(obj)) {
      // On-stack non-schema position: continue without re-adding; the walk
      // terminates when the cycle re-crosses a schema position.
      return copyChildrenOnStack(obj, pos);
    }
    let posMemo = memo.get(pos);
    const cached = posMemo?.get(obj);
    if (cached !== undefined) return cached;
    const out = copyChildren(obj, pos);
    if (!posMemo) { posMemo = new Map(); memo.set(pos, posMemo); }
    posMemo.set(obj, out);
    return out;
  };

  const onStackLaps = new Map<object, number>();
  const copyChildrenOnStack = (node: object, pos: SchemaPos): unknown => {
    // A cycle that never crosses a schema position (cyclic data reached via
    // a dereferenced $ref inside an annotation value) has no faithful
    // tree representation; refuse loudly rather than loop.
    const laps = (onStackLaps.get(node) ?? 0) + 1;
    if (laps > 8) {
      throw new Error("cyclic value at a non-schema position cannot be represented as a JSON tree");
    }
    onStackLaps.set(node, laps);
    try {
    if (Array.isArray(node)) {
      return node.map((item) => copy(item, pos === "schemaArray" ? "schema" : pos === "data" ? "data" : pos));
    }
    const target: Record<string, unknown> = {};
    for (const key of Object.keys(node).sort(codePointCompare)) {
      target[key] = copy((node as Record<string, unknown>)[key], childPos(pos, key));
    }
    return target;
    } finally {
      onStackLaps.set(node, (onStackLaps.get(node) ?? 1) - 1);
    }
  };

  const root = copyChildren(schema, "schema");
  while (pendingDefs.length > 0) {
    const node = pendingDefs.shift() as object;
    const name = defName.get(node) as string;
    defs[name] = copyChildren(node, "schema");
  }

  if (Object.keys(defs).length === 0) return root;
  const sortedDefs: Record<string, unknown> = {};
  for (const key of Object.keys(defs).sort(codePointCompare)) sortedDefs[key] = defs[key];
  return { ...(root as Record<string, unknown>), $defs: sortedDefs };
}


/**
 * Escapes a string for use as a JSON Pointer segment (RFC 6901).
 */
export function escapePointerSegment(segment: string): string {
  return segment.replaceAll("~", "~0").replaceAll("/", "~1");
}

/**
 * A deduplication KEY for possibly-cyclic values: JSON text with any
 * back-edge replaced by a cycle marker. Discriminating, deterministic, and
 * total on cyclic graphs — never used as an emitted value.
 */
export function cycleSafeKey(value: unknown): string {
  const stack = new Set<object>();
  const walk = (node: unknown): unknown => {
    if (node === null || typeof node !== "object") return node;
    if (stack.has(node)) return { $cycle: true };
    stack.add(node);
    let out: unknown;
    if (Array.isArray(node)) out = node.map(walk);
    else {
      const target: Record<string, unknown> = {};
      for (const key of Object.keys(node).sort(codePointCompare)) {
        target[key] = walk((node as Record<string, unknown>)[key]);
      }
      out = target;
    }
    stack.delete(node);
    return out;
  };
  return JSON.stringify(walk(value));
}
