#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const sourcePath = join(root, "authority", "openbindings-0.2.source.json");
const source = JSON.parse(readFileSync(sourcePath, "utf8"));
const specRepo = resolve(process.argv[2] ?? join(root, "..", "..", "openbindings", "spec"));
const outputRoot = join(root, "conformance", "upstream", "openbindings-0.2");

function gitShow(path) {
  return execFileSync("git", ["show", `${source.commit}:${path}`], {
    cwd: specRepo,
    encoding: "utf8",
    maxBuffer: 32 * 1024 * 1024,
  });
}

function digest(value) {
  return createHash("sha256").update(value).digest("hex");
}

function write(path, value) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, value);
}

const copied = [];
const ledgers = [];

for (const family of source.families) {
  const specPath = `binding-specs/${family.family}/openbindings.${family.family}.md`;
  const processorPath = `conformance/binding-specs/processor/${family.family}.json`;
  const synthesisPath = `conformance/binding-specs/synthesis/${family.family}.json`;
  const spec = gitShow(specPath);
  const processorText = gitShow(processorPath);
  const synthesisText = gitShow(synthesisPath);
  const processor = JSON.parse(processorText);
  const synthesis = JSON.parse(synthesisText);

  for (const [kind, upstreamPath, text] of [
    ["processor", processorPath, processorText],
    ["synthesis", synthesisPath, synthesisText],
  ]) {
    const localPath = join(outputRoot, kind, `${family.family}.json`);
    write(localPath, text);
    copied.push({
      upstreamPath,
      localPath: localPath.slice(root.length + 1),
      sha256: digest(text),
      bytes: Buffer.byteLength(text),
    });
  }

  const descriptions = new Map();
  const rulePattern = new RegExp(`\\*\\*\\[convention\\]\\*\\* A (?:processor|synthesizer) conforms to \\*\\*(${family.rulePrefix}-(?:P|S)-\\d+)\\*\\* when ([^\\n]+)`, "g");
  for (const match of spec.matchAll(rulePattern)) descriptions.set(match[1], match[2].trim());

  const scenarioRules = new Map();
  for (const scenario of processor.scenarios) {
    for (const rule of scenario.rules ?? []) {
      const ids = scenarioRules.get(rule) ?? [];
      ids.push(scenario.id);
      scenarioRules.set(rule, ids);
    }
  }
  for (const scenario of synthesis.scenarios) {
    for (const rule of scenario.rules ?? []) {
      const ids = scenarioRules.get(rule) ?? [];
      ids.push(scenario.id);
      scenarioRules.set(rule, ids);
    }
  }

  const rules = [...descriptions.entries()]
    .map(([id, description]) => ({
      id,
      kind: id.includes("-P-") ? "processor" : "synthesis",
      owner: id.includes("-P-") ? "engine-and-native-client" : "analysis-and-openbindings-adapter",
      description,
      upstreamScenarios: scenarioRules.get(id) ?? [],
      implementationEvidence: { typescript: [], go: [], adapter: [] },
      status: "upstream-scenario-pinned",
    }))
    .sort((left, right) => left.id.localeCompare(right.id, undefined, { numeric: true }));

  ledgers.push({
    family: family.family,
    bindingSpec: family.bindingSpec,
    specPath,
    specSha256: digest(spec),
    processorScenarioCount: processor.scenarios.length,
    synthesisScenarioCount: synthesis.scenarios.length,
    rules,
  });
}

for (const name of ["processor-scenario.schema.json", "synthesis-scenario.schema.json"]) {
  const upstreamPath = `conformance/binding-specs/${name}`;
  const text = gitShow(upstreamPath);
  const localPath = join(outputRoot, "schemas", name);
  write(localPath, text);
  copied.push({
    upstreamPath,
    localPath: localPath.slice(root.length + 1),
    sha256: digest(text),
    bytes: Buffer.byteLength(text),
  });
}

{
  const upstreamPath = "conformance/binding-specs/README.md";
  const text = gitShow(upstreamPath);
  const localPath = join(outputRoot, "README.md");
  write(localPath, text);
  copied.push({
    upstreamPath,
    localPath: localPath.slice(root.length + 1),
    sha256: digest(text),
    bytes: Buffer.byteLength(text),
  });
}

const ledger = {
  format: "openbindings.openapi-client-authority-ledger@1",
  source: {
    repository: source.repository,
    releaseBranch: source.releaseBranch,
    commit: source.commit,
    corpusRevision: source.corpusRevision,
  },
  generated: ledgers,
};
write(join(root, "conformance", "openapi-authority-ledger.json"), `${JSON.stringify(ledger, null, 2)}\n`);

const lock = {
  format: "openbindings.openapi-client-authority-lock@1",
  source: ledger.source,
  files: copied.sort((left, right) => left.localPath.localeCompare(right.localPath)),
  totals: {
    processorRules: ledgers.reduce((sum, family) => sum + family.rules.filter((rule) => rule.kind === "processor").length, 0),
    synthesisRules: ledgers.reduce((sum, family) => sum + family.rules.filter((rule) => rule.kind === "synthesis").length, 0),
    processorScenarios: ledgers.reduce((sum, family) => sum + family.processorScenarioCount, 0),
    synthesisScenarios: ledgers.reduce((sum, family) => sum + family.synthesisScenarioCount, 0),
  },
};
write(join(root, "authority", "openbindings-0.2.lock.json"), `${JSON.stringify(lock, null, 2)}\n`);

console.log(`Pinned ${lock.totals.processorRules} processor rules in ${lock.totals.processorScenarios} scenarios and ${lock.totals.synthesisRules} synthesis rules in ${lock.totals.synthesisScenarios} scenarios from ${source.commit}.`);
