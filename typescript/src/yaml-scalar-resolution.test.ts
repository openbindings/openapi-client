import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { loadOpenAPIDocument } from "./util.js";

/**
 * The shared twin case table for `openbindings.openapi@1` §3's
 * scalar-resolution paragraph. The file is byte-identical to the copies in
 * openbindings-go/formats/openapi/testdata, openapi-client/go/testdata and
 * openbindings-ts/packages/openapi/src/testdata; changing it in one engine
 * without the others fails the digest assertion here. Regenerate with
 * corpus-lab/scripts/build-yaml-scalar-resolution-table.mjs, which re-proves
 * every authority quote against the pinned YAML 1.2.2 bytes first.
 */
export const YAML_SCALAR_RESOLUTION_CASES_DIGEST =
  "f34df2ad41c9fb3f99f49406c4e3479e862d5f66cd398a508fedbece97be43b0";

export interface YAMLScalarResolutionCase {
  name: string;
  position: "value" | "key" | "merge";
  spelling: string;
  outcome: "resolved" | "source-refused";
  image: string | null;
  basis: string;
}

export function loadYAMLScalarResolutionCases(raw: Buffer): YAMLScalarResolutionCase[] {
  const digest = createHash("sha256").update(raw).digest("hex");
  if (digest !== YAML_SCALAR_RESOLUTION_CASES_DIGEST) {
    throw new Error(
      `case table digest = ${digest}, want ${YAML_SCALAR_RESOLUTION_CASES_DIGEST} (the table is shared byte-for-byte with the twin engines)`,
    );
  }
  const table = JSON.parse(raw.toString("utf8")) as { cases: YAMLScalarResolutionCase[] };
  if (table.cases.length === 0) throw new Error("case table is empty");
  return table.cases;
}

/**
 * Render one case as a WHOLE OpenAPI document, in YAML, because YAML is the
 * grammar under test: a harness that hands the engine a pre-parsed tree
 * measures nothing at all here. The scalar sits at `info/x-case`, a
 * specification extension, so it survives into the loaded document untouched
 * by any schema or parameter machinery.
 */
export function yamlScalarResolutionDocument(c: YAMLScalarResolutionCase): string {
  let body: string;
  switch (c.position) {
    case "key":
      body = `  x-case:\n    ${c.spelling}: marker\n`;
      break;
    case "merge":
      body = `  x-anchor: &anchor\n    x: 1\n  x-case:\n    ${c.spelling}: *anchor\n    y: 2\n`;
      break;
    default:
      body = `  x-case: ${c.spelling}\n`;
  }
  return (
    "openapi: 3.1.0\n"
    + "info:\n"
    + "  title: yaml scalar resolution case table\n"
    + "  version: 1.0.0\n"
    + body
    + "paths:\n"
    + "  /p:\n"
    + "    get:\n"
    + "      operationId: getP\n"
    + "      responses:\n"
    + '        "200":\n'
    + "          description: ok\n"
  );
}

/** Canonical JSON with object keys sorted — the Go twin's `json.Marshal` order. */
export function canonicalImage(value: unknown): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value) ?? String(value);
  if (Array.isArray(value)) return `[${value.map(canonicalImage).join(",")}]`;
  const entries = Object.keys(value as Record<string, unknown>)
    .sort()
    .map(key => `${JSON.stringify(key)}:${canonicalImage((value as Record<string, unknown>)[key])}`);
  return `{${entries.join(",")}}`;
}

export async function yamlScalarResolutionImage(
  c: YAMLScalarResolutionCase,
  load: typeof loadOpenAPIDocument,
): Promise<string> {
  let document: Awaited<ReturnType<typeof loadOpenAPIDocument>>;
  try {
    document = await load(undefined, yamlScalarResolutionDocument(c));
  } catch {
    return "source-refused";
  }
  const info = (document as { info?: Record<string, unknown> }).info;
  if (!info || !("x-case" in info)) {
    throw new Error(`${c.name}: loaded document does not carry info/x-case; the harness, not the engine, is wrong`);
  }
  return canonicalImage(info["x-case"]);
}

/** The classes F-O1-7 was opened on; the table may grow but not shrink past them. */
export const YAML_SCALAR_RESOLUTION_REQUIRED_CASES = [
  "value|017",
  "value|0o17",
  "value|17",
  "value|1_000",
  "value|0b101",
  "value|0x1F",
  "merge|<<",
  "value|true",
  "value|True",
  "value|TRUE",
  "value|null",
  "value|Null",
  "value|~",
  "value|.inf",
  "value|.nan",
  "value|2020-01-01T12:00:00Z",
  "value|12:30:45",
  "tag|!!timestamp",
  "tag|!!binary",
  "tag|!!merge",
  "tag|!!set",
  "tag|!ruby/object",
];

const cases = loadYAMLScalarResolutionCases(
  readFileSync(new URL("./testdata/yaml-scalar-resolution-cases.json", import.meta.url)),
);

describe("YAML 1.2.2 §10.3.2 scalar resolution (shared twin case table)", () => {
  for (const c of cases) {
    it(`${c.name} — ${c.outcome}`, async () => {
      const want = c.outcome === "source-refused" ? "source-refused" : c.image;
      await expect(yamlScalarResolutionImage(c, loadOpenAPIDocument)).resolves.toBe(want);
    });
  }

  it("keeps every case F-O1-7 was opened on", () => {
    const present = new Set(cases.map(c => c.name));
    for (const name of YAML_SCALAR_RESOLUTION_REQUIRED_CASES) expect(present).toContain(name);
  });

  it("refuses loudly rather than resolving a value the OAS forbids", async () => {
    for (const c of cases.filter(entry => entry.outcome === "source-refused")) {
      await expect(loadOpenAPIDocument(undefined, yamlScalarResolutionDocument(c))).rejects.toThrow();
    }
  });
});

/**
 * R3's guard: F-O1-15 — what a numeric scalar outside the double domain
 * becomes — is OPEN, and it spans EVERY §10.3.2 numeric row: base 10, base 8,
 * base 16 and the float row. A class filed as open needs a pin on every row it
 * spans in BOTH directions of its boundary, or "the behavior is pinned by name
 * so it cannot drift silently" is not true. Pinning the class on the float row
 * alone is exactly what let this engine decide it by side effect, toward
 * refusal, with the corpus byte-identical.
 *
 * The spellings are constructed here rather than copied out of the table, so
 * this is an independent expectation and not a restatement of the file.
 */
describe("F-O1-15 is pinned on every §10.3.2 numeric row it spans", () => {
  const byName = new Map(cases.map(c => [c.name, c]));
  const rows: Array<{ row: string; inDomain: string; outside: string }> = [
    { row: "base 10", inDomain: `1${"0".repeat(308)}`, outside: `1${"0".repeat(309)}` },
    { row: "base 16", inDomain: `0x${"F".repeat(255)}`, outside: `0x${"F".repeat(256)}` },
    { row: "base 8", inDomain: `0o${"7".repeat(341)}`, outside: `0o${"7".repeat(342)}` },
    { row: "float", inDomain: "1e308", outside: "1e309" },
  ];
  for (const { row, inDomain, outside } of rows) {
    it(`${row} — the representable side resolves to a number`, () => {
      const entry = byName.get(`value|${inDomain}`);
      expect(entry, `${row} row has no representable-side pin`).toBeDefined();
      expect(entry?.outcome).toBe("resolved");
      expect(entry?.image?.startsWith('"')).toBe(false);
    });
    it(`${row} — the out-of-domain side keeps its source text`, () => {
      const entry = byName.get(`value|${outside}`);
      expect(
        entry,
        `${row} row has no out-of-domain pin, so F-O1-15 can be decided here by side effect`,
      ).toBeDefined();
      expect(entry?.outcome).toBe("resolved");
      // Changing this expectation decides F-O1-15, which is a ruling.
      expect(entry?.image).toBe(JSON.stringify(outside));
    });
  }
});
