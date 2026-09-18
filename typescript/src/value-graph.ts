import { isDecimal, isEncoded } from "@openbindings/json";

/**
 * A mutable structural copy of a JSON value.
 *
 * The shared value package offers no deep clone on purpose: the graphs it
 * hands back are frozen, so nothing needs defending against mutation. Copies
 * made here are edited afterwards, so containers are rebuilt while exact
 * numbers and byte-backed strings carry by reference, being immutable leaves.
 */
export function cloneValueGraph<T>(value: T): T {
  if (Array.isArray(value)) return value.map((item) => cloneValueGraph(item)) as unknown as T;
  if (typeof value !== "object" || value === null || isDecimal(value) || isEncoded(value)) return value;
  const out: Record<string, unknown> = {};
  for (const [key, child] of Object.entries(value as Record<string, unknown>)) out[key] = cloneValueGraph(child);
  return out as T;
}
