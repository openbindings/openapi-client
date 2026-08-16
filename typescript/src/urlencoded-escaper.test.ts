import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { buildURLEncodedBody } from "./media.js";

// The identical file is executed by openbindings-go/formats/openapi and by
// openapi-client/go; changing it in one engine without the others fails here.
const CASES_DIGEST = "7fa47d6a207b33e8530e94b49442e3fbc39610dc16e6fb0ea573d3ac2e94821f";

interface EscaperCase {
  name: string;
  openapi: string;
  declaration: string;
  encoding: Record<string, unknown> | null;
  position: string;
  cell: string;
  lane: string;
  propertyName: string;
  value: string;
  expect: string;
}

function loadCases(): EscaperCase[] {
  const raw = readFileSync(new URL("./testdata/urlencoded-escaper-cases.json", import.meta.url));
  const digest = createHash("sha256").update(raw).digest("hex");
  if (digest !== CASES_DIGEST) {
    throw new Error(
      `case table digest = ${digest}, want ${CASES_DIGEST} (the table is shared byte-for-byte with the two Go engines)`,
    );
  }
  const table = JSON.parse(raw.toString("utf8")) as { cases: EscaperCase[] };
  if (table.cases.length === 0) throw new Error("case table is empty");
  return table.cases;
}

function mediaFor(c: EscaperCase): never {
  const media: Record<string, unknown> = {
    schema: { type: "object", properties: { [c.propertyName]: { type: "string" } } },
  };
  if (c.encoding !== null) media.encoding = { [c.propertyName]: c.encoding };
  return media as never;
}

// The table's expectations come from upstream authority text, never from an
// engine:
//
//   - The STYLE lane is RFC 6570 form expansion with allowReserved absent, so
//     its literal set is RFC 3986's unreserved set and a space is %20. OAS
//     3.0.4 / 3.1.1 Appendix E.3 (E.4 in 3.1.2) assigns that lane to RFC 6570
//     and notes it "does not use + for form-urlencoded".
//   - The CONTENT lane is RFC 1866 Section 8.2.1 ("space characters are
//     replaced by `+`"), whose literal set is fixed to the WHATWG
//     form-urlencoded serializer set on the authority of the same appendix's
//     SHOULD for browser compatibility.
//
// The two lanes are therefore SUPPOSED to disagree about the space character;
// a change that made them agree would break conformance in the style lane.
describe("urlencoded escaper case table", () => {
  const cases = loadCases();

  for (const c of cases) {
    it(`${c.name} (${c.lane} lane)`, () => {
      expect(buildURLEncodedBody(mediaFor(c), { [c.propertyName]: c.value }, true, c.openapi, false))
        .toBe(c.expect);
    });
  }

  // The audit's finding, as an executable claim: across the table's cells the
  // only character the two lanes render differently is the space (plus the two
  // cells where the two authorities' literal sets themselves differ).
  it("the lanes diverge only where the two authorities' rules differ", () => {
    const byCell = new Map<string, Record<string, string>>();
    for (const c of cases) {
      if (c.position !== "value" || c.openapi !== "3.1.1") continue;
      const lanes = byCell.get(c.cell) ?? {};
      lanes[c.lane] = c.expect;
      byCell.set(c.cell, lanes);
    }
    for (const [cell, lanes] of byCell) {
      expect(lanes.style, cell).toBeDefined();
      expect(lanes.content, cell).toBeDefined();
      if (cell === "space") {
        expect(lanes.style).toBe("p=%20");
        expect(lanes.content).toBe("p=+");
      } else if (cell === "asterisk" || cell === "tilde") {
        expect(lanes.style, cell).not.toBe(lanes.content);
      } else {
        expect(lanes.style, cell).toBe(lanes.content);
      }
    }
  });
});
