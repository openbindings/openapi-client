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

/**
 * A magnitude outside the double domain keeps its source text,
 * DELIBERATELY, and this is the one place this schema does not follow
 * §10.3.2's table. It guards EVERY numeric row — base 10, base 8, base 16
 * and the float row alike — because the class is a magnitude, not a
 * spelling: `1e400` and `600e27371700` are its float members,
 * `1` followed by 309 zeros its base-10 member, `0x` followed by 256 `F`
 * its base-16 member, and `0o` followed by 342 sevens its base-8 member.
 *
 * §10.3.2 resolves each of those spellings to a number. What a processor
 * then does with one it cannot hold is not §10.3.2's to say, and the
 * authority says so twice, once per tag. §10.2.1.4 (float) states that "The
 * supported range and accuracy depends on the implementation, though 32 bit
 * IEEE floats should be safe"; §10.2.1.3 (integer) states that "an integer
 * may overflow the native type's storage capability. A YAML processor may
 * reject such a value as an error, truncate it with a warning or find some
 * other manner to round-trip it." Refusing the artifact, carrying
 * ±Infinity, and clamping to the nearest representable double are therefore
 * all inside what the authority permits — a choice, not a deduction. §3's
 * no-JSON-image refusal does not reach the class either: unlike `.inf` and
 * `.nan`, which JSON cannot spell at all, `6e27371702` is a perfectly
 * ordinary JSON number that this number type cannot hold.
 *
 * So the choice is not made here. Every engine spells such a scalar as its
 * own source text — which is also what js-yaml's incumbent int and float
 * types already did, so this schema changes nothing about the class — and
 * the question is filed as F-O1-15 in corpus-lab/OPENAPI-RUNTIME.md. Every
 * row it spans is pinned by name in the shared case table, in both
 * directions of each boundary, so no engine can decide it by side effect.
 *
 * Underflow is NOT in this class: `1e-400` becomes 0, which is §10.2.1.4's
 * "a float value may change by 'a small amount' when round-tripped".
 * `Number` reads `0o…` and `0x…` exactly as this table spells them, so one
 * predicate covers all four rows.
 */
function representableMagnitude(data: string): boolean {
  return Number.isFinite(Number(data));
}

const intType = new yaml.Type("tag:yaml.org,2002:int", {
  kind: "scalar",
  resolve: (data: string | null) =>
    data !== null
    && (INTEGER_BASE_10.test(data) || INTEGER_BASE_8.test(data) || INTEGER_BASE_16.test(data))
    && representableMagnitude(data),
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

const floatType = new yaml.Type("tag:yaml.org,2002:float", {
  kind: "scalar",
  resolve: (data: string | null) => {
    if (data === null) return false;
    if (FLOAT_INFINITY.test(data) || FLOAT_NAN.test(data)) return true;
    return FLOAT_NUMBER.test(data) && representableMagnitude(data);
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
