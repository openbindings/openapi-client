import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { buildURLEncodedBody } from "./media.js";

// The identical file is executed by openbindings-go/formats/openapi and by
// openapi-client/go; changing it in one engine without the others fails here.
const CASES_DIGEST = "1af21db9a7c158d671bd4e388f7729dc5372d571c4c00fa257bfb707d162d818";

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
//   - The STYLE lane is RFC 6570 form expansion with allowReserved not in
//     effect, so its literal set is RFC 3986's unreserved set and a space is
//     %20. OAS 3.0.4 / 3.1.1 Appendix E.3 (E.4 in 3.1.2) assigns that lane to
//     RFC 6570 and notes it "does not use + for form-urlencoded".
//   - The CONTENT lane is RFC 1866 Section 8.2.1 ("space characters are
//     replaced by `+`"), whose operative clause escapes the rest "as per
//     [URL]" = RFC 1738. RFC 1738 Section 2.2 permits "only alphanumerics, the
//     special characters `$-_.+!*'(),`, and reserved characters used for their
//     reserved purposes" unencoded, names `~` UNSAFE ("must always be
//     encoded"), and permits encoding anything not required to be encoded. The
//     WHATWG form-urlencoded serializer set this engine emits lies inside that
//     permission, and OAS 3.0.4 / 3.1.1 Appendix E.3.2 (E.4.2 in 3.1.2) gives
//     it a SHOULD.
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

  // The table is the only guard on the edition branches and on the presence
  // class, so an edition or declaration shape silently dropping out of it is
  // the failure mode this asserts against.
  it("covers every edition branch and the presence class", () => {
    const seen = new Map<string, Map<string, string>>();
    for (const c of cases) {
      const declarations = seen.get(c.openapi) ?? new Map<string, string>();
      const prior = declarations.get(c.declaration);
      if (prior !== undefined) expect(prior, `${c.openapi} ${c.declaration}`).toBe(c.lane);
      declarations.set(c.declaration, c.lane);
      seen.set(c.openapi, declarations);
    }
    // One edition per branch the engines distinguish: the 3.0 line that applies
    // the style defaults unconditionally, the 3.0 edition that reaches the
    // content lane, and the 3.1 line.
    const want: Record<string, Record<string, string>> = {
      "3.0.3": { content: "style", style: "style", "allow-reserved-false": "style" },
      "3.0.4": { content: "content", style: "style", "allow-reserved-false": "style" },
      "3.1.1": { content: "content", style: "style", "allow-reserved-false": "style" },
    };
    expect([...seen.keys()].sort()).toStrictEqual(Object.keys(want).sort());
    for (const [edition, declarations] of Object.entries(want)) {
      for (const [declaration, lane] of Object.entries(declarations)) {
        expect(seen.get(edition)?.get(declaration), `${edition} ${declaration}`).toBe(lane);
      }
    }
  });
});

// Lane selection is PRESENCE, not truthiness: OAS 3.0.4 / 3.1.1 / 3.1.2 say
// "the presence of at least one of style, explode, or allowReserved WITH AN
// EXPLICIT VALUE is equivalent to using schema with in: 'query' Parameter
// Objects", and OAS 3.1.0 says of each of the three fields "If a value is
// explicitly defined, then the value of contentType (implicit or explicit)
// SHALL be ignored." `allowReserved: false` writes the field's own default, so
// an engine that reads truthiness puts it in the content lane and spells a
// space `+` where the authority spells it `%20`.
describe("urlencoded encoding presence", () => {
  for (const edition of ["3.0.4", "3.1.0", "3.1.1", "3.1.2"]) {
    it(`${edition} selects the style lane on an explicit allowReserved: false`, () => {
      const media = {
        schema: { type: "object", properties: { note: { type: "string" } } },
        encoding: { note: { allowReserved: false } },
      };
      expect(buildURLEncodedBody(media as never, { note: "one two" }, true, edition, false))
        .toBe("note=one%20two");
    });

    it(`${edition} selects the content lane when all three fields are absent`, () => {
      const media = { schema: { type: "object", properties: { note: { type: "string" } } } };
      expect(buildURLEncodedBody(media as never, { note: "one two" }, true, edition, false))
        .toBe("note=one+two");
    });
  }
});
