import { CORE_SCHEMA, Type, loadAll } from "js-yaml";
import { cloneJSON, parseJSON } from "@openbindings/json";

// Parser-local scalar, not a public number carrier. js-yaml stringifies mapping
// keys during construction; the authored scalar spelling must survive that step.
class SourceNumber {
  readonly [Symbol.toStringTag] = "OpenAPISourceNumber";
  constructor(readonly token: string, readonly spelling: string) {}
  toString(): string { return this.spelling; }
}

const integer = /^(?:[-+]?[0-9]+|0o[0-7]+|0x[0-9a-fA-F]+)$/;
const decimal = /^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?$/;
const nonfinite = /^[-+]?\.(?:inf|Inf|INF|nan|NaN|NAN)$/;

function decimalToken(spelling: string): string {
  const match = /^([-+]?)([^eE]+)([eE][-+]?[0-9]+)?$/.exec(spelling)!;
  const [whole = "", fraction] = match[2]!.split(".");
  return (match[1] === "-" ? "-" : "") + (whole.replace(/^0+/, "") || "0")
    + (fraction === undefined ? "" : `.${fraction || "0"}`) + (match[3] ?? "");
}

const exactCore = CORE_SCHEMA.extend({
  implicit: [
    new Type("tag:yaml.org,2002:int", {
      kind: "scalar", resolve: (s) => s !== null && integer.test(s),
      construct: (s: string) => new SourceNumber(BigInt(s).toString(), s),
    }),
    new Type("tag:yaml.org,2002:float", {
      kind: "scalar", resolve: (s) => s !== null && (decimal.test(s) || nonfinite.test(s)),
      construct: (s: string) => {
        if (nonfinite.test(s)) throw new TypeError("nonfinite YAML scalar has no JSON representation");
        return new SourceNumber(decimalToken(s), s);
      },
    }),
  ],
});

/** Existing js-yaml grammar/resolver, with source-aware Core numeric constructors. */
export function parseExactSourceDocuments(text: string): unknown[] {
  const documents: unknown[] = [];
  loadAll(text, (doc) => documents.push(doc), { schema: exactCore, json: false });
  const active = new Set<object>();
  const memo = new Map<object, unknown>();
  const convert = (value: unknown): unknown => {
    if (value instanceof SourceNumber) return parseJSON(value.token);
    if (value === null || typeof value !== "object") return value;
    if (active.has(value)) throw new TypeError("cyclic YAML alias graph has no JSON image");
    if (memo.has(value)) return memo.get(value);
    active.add(value);
    const out: unknown[] | Record<string, unknown> = Array.isArray(value) ? [] : Object.create(null);
    memo.set(value, out);
    for (const [key, child] of Object.entries(value)) {
      Object.defineProperty(out, key, { value: convert(child), writable: true, enumerable: true, configurable: true });
    }
    active.delete(value);
    return out;
  };
  return documents.map((doc) => cloneJSON(convert(doc)));
}
