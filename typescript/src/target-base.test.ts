import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { denotesTargetBase } from "./target-base.js";
import {
  absolutizeServerURL,
  effectiveServers,
  eligibleServers,
  substituteServerVariables,
} from "./servers.js";
import type { OpenAPIDocument, OpenAPIOperation, OpenAPIPathItem } from "./types.js";

// serverTargetBaseCasesDigest pins the frozen twin case table. The identical
// file is executed by openbindings-go/formats/openapi and openapi-client/go;
// changing it in one engine without the others fails here.
const serverTargetBaseCasesDigest = "5c87131c4db1b3e90381ae29d51948fdebb772a928648c4217fcfcb32386c68f";

interface PredicateCase {
  name: string;
  url: string;
  usable: boolean;
  why: string;
  basis: string;
}

interface ResolutionCase {
  name: string;
  openapi: string;
  rootServers: Record<string, unknown>[] | null;
  operationServers: Record<string, unknown>[] | null;
  expect: { resolvable: boolean; targetBase: string | null };
  requirements: string[];
  basis: string;
}

function loadTable(): { predicateCases: PredicateCase[]; resolutionCases: ResolutionCase[] } {
  const path = fileURLToPath(new URL("./testdata/server-target-base-cases.json", import.meta.url));
  const raw = readFileSync(path);
  const digest = createHash("sha256").update(raw).digest("hex");
  if (digest !== serverTargetBaseCasesDigest) {
    throw new Error(
      `case table digest = ${digest}, want ${serverTargetBaseCasesDigest} (the table is shared byte-for-byte with the two Go twins)`,
    );
  }
  return JSON.parse(raw.toString("utf8"));
}

const table = loadTable();

/**
 * Renders one resolution case as a WHOLE OpenAPI document, exactly as the Go
 * twins render it, so the three engines are answering the same question about
 * the same artifact rather than about three hand-built models.
 */
function caseDocument(c: ResolutionCase): OpenAPIDocument {
  const operation: Record<string, unknown> = {
    operationId: "listThings",
    responses: { "200": { description: "ok" } },
  };
  if (c.operationServers !== null) operation["servers"] = c.operationServers;
  const document: Record<string, unknown> = {
    openapi: c.openapi,
    info: { title: "server target base case table", version: "1.0.0" },
    paths: { "/things": { get: operation } },
  };
  if (c.rootServers !== null) document["servers"] = c.rootServers;
  return document as unknown as OpenAPIDocument;
}

describe("server target base case table", () => {
  it("carries both halves of the shared table", () => {
    expect(table.predicateCases.length).toBeGreaterThan(0);
    expect(table.resolutionCases.length).toBeGreaterThan(0);
  });

  // The predicate half: does this string denote a target address under RFC
  // 3986's URI production plus RFC 9110's non-empty-host rule for http/https?
  for (const c of table.predicateCases) {
    it(`predicate: ${c.name}`, () => {
      expect(denotesTargetBase(c.url), `${c.name}\nbasis: ${c.basis}`).toBe(c.usable);
    });
  }

  // The resolution half: the effective server with NO consumer configuration
  // and no source location, which is the state an unconfigured invocation --
  // and synthesis coverage -- asks about.
  for (const c of table.resolutionCases) {
    it(`resolution: ${c.name}`, () => {
      const doc = caseDocument(c);
      const pathItem = (doc as unknown as { paths: Record<string, OpenAPIPathItem> }).paths["/things"];
      const op = (pathItem as unknown as { get: OpenAPIOperation }).get;
      let base: string | null = null;
      let resolvable = true;
      try {
        const servers = eligibleServers(
          effectiveServers(doc, pathItem ?? null, op ?? null),
          c.openapi,
          undefined,
        );
        if (servers.length !== 1) throw new Error("selection required");
        base = absolutizeServerURL(
          substituteServerVariables(servers[0], undefined, c.openapi),
          undefined,
        );
      } catch {
        resolvable = false;
      }
      expect(resolvable, `${c.name}\nbasis: ${c.basis}`).toBe(c.expect.resolvable);
      if (resolvable) {
        expect(base, `${c.name}\nbasis: ${c.basis}`).toBe(c.expect.targetBase);
      }
    });
  }
});
