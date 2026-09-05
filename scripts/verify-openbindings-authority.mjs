#!/usr/bin/env node

import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const source = JSON.parse(readFileSync(join(root, "authority", "openbindings-0.2.source.json"), "utf8"));
const lock = JSON.parse(readFileSync(join(root, "authority", "openbindings-0.2.lock.json"), "utf8"));
const ledger = JSON.parse(readFileSync(join(root, "conformance", "openapi-authority-ledger.json"), "utf8"));
const failures = [];

function digest(value) {
  return createHash("sha256").update(value).digest("hex");
}

if (lock.source.commit !== source.commit || ledger.source.commit !== source.commit) {
  failures.push("source, lock, and ledger commits differ");
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
