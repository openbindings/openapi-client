import yaml from "js-yaml";

/**
 * YAML 1.2.2 §10.3.2 core tag resolution, stated as its own schema.
 *
 * `openbindings.openapi@1` §3 pins the string-content grammar to YAML 1.2.2
 * and resolves untagged scalars by "[YAML 1.2.2 §10.3.2]'s core tag
 * resolution", constrained by every accepted OAS edition's §4.2 sentence
 * "Tags MUST be limited to those allowed by [YAML's] JSON schema ruleset".
 * That restriction is the artifact authority's, not a local preference, and
 * the patterns below are transcribed from the pinned bytes of the authority
 * (corpus-lab/authorities/texts/openapi/yaml/yaml-1.2.2.html, SHA-256
 * 2fe1bf40792df22ef8b8a5972a34c48e639969222e0bbae605aa9bb2ca85e9ab, the
 * `<h3 id="1032-tag-resolution">` anchor at byte 370,122).
 *
 * js-yaml's own CORE_SCHEMA is not that resolution. Its header says so —
 * "JS-YAML does not support schema-specific tag resolution restrictions …
 * It allows numbers in binary notaion" — and the divergence is real: it
 * resolves `0b101` to the integer 5 where §10.3.2's table has no binary
 * pattern at all, so the scalar matches nothing and is a string. It also
 * misses `-.5`, because its float pattern permits a sign only on the
 * `[0-9]+` alternative where §10.3.2 places `[-+]?` outside the whole
 * group. Both are corrected here rather than at the call sites, because the
 * resolution is a property of the grammar the specification pinned.
 *
 * The permitted tag set is §10.2.1's null, boolean, integer and float
 * alongside the failsafe map, sequence and string — which is exactly
 * `FAILSAFE_SCHEMA` extended with these four implicit types. A scalar
 * carrying an explicit tag outside that set (`!!timestamp`, `!!binary`,
 * `!!merge`, `!!set`, `!ruby/object`) resolves against no type here, so
 * js-yaml refuses the document, which is §3's "an explicit tag naming an
 * unpermitted type is refused rather than resolved".
 *
 * Merge keys fall out of the same table: `<<` matches no pattern, so it is
 * the plain string key `<<` and no merging happens. §3 names merge among
 * the YAML 1.1 types outside the permitted set.
 */

/** `null | Null | NULL | ~`, and the empty scalar's own row. */
function resolveNull(data: string | null): boolean {
  if (data === null) return true;
  return data === "" || data === "~" || data === "null" || data === "Null" || data === "NULL";
}

/** `true | True | TRUE | false | False | FALSE`. */
const BOOLEAN = /^(?:true|True|TRUE|false|False|FALSE)$/;

/** `[-+]? [0-9]+` — base 10. */
const INTEGER_BASE_10 = /^[-+]?[0-9]+$/;
/** `0o [0-7]+` — base 8. The authority's row carries no sign. */
const INTEGER_BASE_8 = /^0o[0-7]+$/;
/** `0x [0-9a-fA-F]+` — base 16. The authority's row carries no sign. */
const INTEGER_BASE_16 = /^0x[0-9a-fA-F]+$/;

/** `[-+]? ( \. [0-9]+ | [0-9]+ ( \. [0-9]* )? ) ( [eE] [-+]? [0-9]+ )?`. */
const FLOAT_NUMBER = /^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?$/;
/** `[-+]? ( \.inf | \.Inf | \.INF )`. */
const FLOAT_INFINITY = /^[-+]?\.(?:inf|Inf|INF)$/;
/** `\.nan | \.NaN | \.NAN`. */
const FLOAT_NAN = /^\.(?:nan|NaN|NAN)$/;

const nullType = new yaml.Type("tag:yaml.org,2002:null", {
  kind: "scalar",
  resolve: resolveNull,
  construct: () => null,
  predicate: (object: unknown) => object === null,
  represent: { canonical: () => "~", lowercase: () => "null" },
  defaultStyle: "lowercase",
});

const boolType = new yaml.Type("tag:yaml.org,2002:bool", {
  kind: "scalar",
  resolve: (data: string | null) => data !== null && BOOLEAN.test(data),
  construct: (data: string) => data === "true" || data === "True" || data === "TRUE",
  predicate: (object: unknown) => typeof object === "boolean",
  represent: { lowercase: (object: unknown) => (object ? "true" : "false") },
  defaultStyle: "lowercase",
});

const intType = new yaml.Type("tag:yaml.org,2002:int", {
  kind: "scalar",
  resolve: (data: string | null) =>
    data !== null
    && (INTEGER_BASE_10.test(data) || INTEGER_BASE_8.test(data) || INTEGER_BASE_16.test(data)),
  construct: (data: string) => {
    if (INTEGER_BASE_8.test(data)) return Number.parseInt(data.slice(2), 8);
    if (INTEGER_BASE_16.test(data)) return Number.parseInt(data.slice(2), 16);
    // Base 10 keeps every leading zero as a decimal digit: `017` is 17, not
    // 15. Only the `0o` row is octal in this table.
    return Number(data);
  },
  predicate: (object: unknown) => typeof object === "number" && Number.isInteger(object),
  represent: (object: unknown) => String(object),
  defaultStyle: "decimal",
});

/**
 * A magnitude outside the double domain (`1e400`, and the corpus's real
 * `600e27371700`) keeps its source text, DELIBERATELY, and this is the one
 * place this schema does not follow §10.3.2's table.
 *
 * §10.3.2 resolves such a spelling to a float. What a processor then does
 * with it is not §10.3.2's to say: §10.2.1.4 states that "The supported
 * range and accuracy depends on the implementation", which puts refusing
 * the artifact, carrying ±Infinity, and clamping to the nearest
 * representable double all inside what the authority permits — a choice, not
 * a deduction. §3's no-JSON-image refusal does not reach it either: unlike
 * `.inf` and `.nan`, which JSON cannot spell at all, `6e27371702` is a
 * perfectly ordinary JSON number that this number type cannot hold.
 *
 * Both engines spell such a scalar as its own text today, so leaving it
 * alone is the outcome that moves nothing while the question is open. It is
 * filed as F-O1-15 in corpus-lab/OPENAPI-RUNTIME.md and pinned by name in
 * the shared case table so it cannot drift silently. Underflow is NOT in
 * this class: `1e-400` becomes 0, which is §10.2.1.4's "a float value may
 * change by 'a small amount' when round-tripped".
 */
function representableFloat(data: string): boolean {
  return Number.isFinite(Number(data));
}

const floatType = new yaml.Type("tag:yaml.org,2002:float", {
  kind: "scalar",
  resolve: (data: string | null) => {
    if (data === null) return false;
    if (FLOAT_INFINITY.test(data) || FLOAT_NAN.test(data)) return true;
    return FLOAT_NUMBER.test(data) && representableFloat(data);
  },
  construct: (data: string) => {
    if (FLOAT_NAN.test(data)) return Number.NaN;
    if (FLOAT_INFINITY.test(data)) {
      return data[0] === "-" ? Number.NEGATIVE_INFINITY : Number.POSITIVE_INFINITY;
    }
    // `.inf` and `.nan` are the only non-finite values reachable here, and
    // the document's JSON-domain guard refuses them rather than letting a
    // writer emit `null` in place of a declared value.
    return Number(data);
  },
  predicate: (object: unknown) => typeof object === "number",
  represent: (object: unknown) => String(object),
  defaultStyle: "lowercase",
});

/**
 * The failsafe schema (`!!map`, `!!seq`, `!!str`) extended with §10.2.1's
 * null, boolean, integer and float — the complete permitted tag set, with
 * §10.3.2's resolution.
 */
export const OPENAPI_YAML_SCHEMA = yaml.FAILSAFE_SCHEMA.extend({
  implicit: [nullType, boolType, intType, floatType],
});
