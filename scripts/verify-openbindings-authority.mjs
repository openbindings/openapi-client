#!/usr/bin/env node

import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const source = JSON.parse(readFileSync(join(root, "authority", "openbindings-0.2.source.json"), "utf8"));
const lock = JSON.parse(readFileSync(join(root, "authority", "openbindings-0.2.lock.json"), "utf8"));
const ledger = JSON.parse(readFileSync(join(root, "conformance", "openapi-authority-ledger.json"), "utf8"));
const failures = [];
// Optional qualification against an explicitly selected source checkout. The
// ordinary package verifier remains self-contained; this is not Core conformance.
const specRepo = process.argv[2] ? resolve(process.argv[2]) : undefined;
if (specRepo) {
  const selected = execFileSync("git", ["rev-parse", "HEAD"], { cwd: specRepo, encoding: "utf8" }).trim();
  if (selected !== source.commit) failures.push(`selected spec ${selected} differs from pinned ${source.commit}`);
  for (const family of ledger.generated) {
    if (digest(readFileSync(join(specRepo, family.specPath))) !== family.specSha256) {
      failures.push(`${family.specPath}: selected spec prose differs from the authority ledger`);
    }
  }
  if ((source.candidateErrata ?? []).length === 0) {
    for (const entry of lock.files) {
      if (digest(readFileSync(join(specRepo, entry.upstreamPath))) !== entry.sha256) {
        failures.push(`${entry.upstreamPath}: selected corpus differs from the authority lock`);
      }
    }
  }
}

function digest(value) {
  return createHash("sha256").update(value).digest("hex");
}

const sourceIdentity = {
  repository: source.repository,
  releaseBranch: source.releaseBranch,
  commit: source.commit,
  corpusRevision: source.corpusRevision,
  candidateErrata: source.candidateErrata ?? [],
};
if (JSON.stringify(lock.source) !== JSON.stringify(sourceIdentity) || JSON.stringify(ledger.source) !== JSON.stringify(sourceIdentity)) {
  failures.push("source, lock, and ledger authority identities differ");
}

for (const entry of lock.files) {
  const value = readFileSync(join(root, entry.localPath));
  if (value.byteLength !== entry.bytes) failures.push(`${entry.localPath}: byte count changed`);
  if (digest(value) !== entry.sha256) failures.push(`${entry.localPath}: SHA-256 changed`);
}

const processorRules = new Set();
const synthesisRules = new Set();
let processorScenarios = 0;
let synthesisScenarios = 0;

for (const family of ledger.generated) {
  const expected = source.families.find((candidate) => candidate.family === family.family);
  if (!expected || expected.bindingSpec !== family.bindingSpec) failures.push(`${family.family}: binding identifier drifted`);
  const ids = new Set();
  for (const rule of family.rules) {
    if (ids.has(rule.id)) failures.push(`${family.family}: duplicate rule ${rule.id}`);
    ids.add(rule.id);
    if (rule.upstreamScenarios.length === 0) failures.push(`${rule.id}: no upstream scenario cites the rule`);
    (rule.kind === "processor" ? processorRules : synthesisRules).add(rule.id);
  }
  processorScenarios += family.processorScenarioCount;
  synthesisScenarios += family.synthesisScenarioCount;
}

const actual = {
  processorRules: processorRules.size,
  synthesisRules: synthesisRules.size,
  processorScenarios,
  synthesisScenarios,
};
for (const [name, value] of Object.entries(lock.totals)) {
  if (actual[name] !== value) failures.push(`${name}: ${actual[name]} != locked ${value}`);
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}

console.log(`OpenAPI authority lock verified at ${source.commit}: ${actual.processorRules} P-rules/${actual.processorScenarios} processor scenarios; ${actual.synthesisRules} S-rules/${actual.synthesisScenarios} synthesis scenarios.`);
