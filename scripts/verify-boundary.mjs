import { readFile, readdir } from "node:fs/promises";

const packageJSON = JSON.parse(await readFile(new URL("../typescript/package.json", import.meta.url), "utf8"));
const dependencies = Object.keys(packageJSON.dependencies ?? {});
const forbiddenDependencies = dependencies.filter((name) => name.startsWith("@openbindings/"));
if (forbiddenDependencies.length > 0) {
  throw new Error(`standalone package has OpenBindings runtime dependencies: ${forbiddenDependencies.join(", ")}`);
}

const exportedPaths = Object.keys(packageJSON.exports ?? {});
if (exportedPaths.join(",") !== ".") {
  throw new Error(`standalone package must expose only its intentional native client-engine entry point, got ${exportedPaths.join(", ")}`);
}

const declarations = await readFile(new URL("../typescript/dist/index.d.ts", import.meta.url), "utf8");
for (const forbidden of [
  "BindingInvocationArgs",
  "ContextRequiredDetails",
  "InvocationError",
  "OpenAPIRuntime",
  "bindingSpec",
]) {
  if (declarations.includes(forbidden)) {
    throw new Error(`public declarations leak internal/OpenBindings concept ${forbidden}`);
  }
}

// The Go block below has a TypeScript twin. Without it the same retired
// identifier sat in four user-facing TypeScript strings while only the Go copy
// ever turned this job red.
const tsFiles = (await readdir(new URL("../typescript/src/", import.meta.url)))
  .filter((name) => name.endsWith(".ts") && !name.endsWith(".test.ts") && !name.endsWith(".d.ts"));
for (const name of tsFiles) {
  const source = await readFile(new URL(`../typescript/src/${name}`, import.meta.url), "utf8");
  for (const forbidden of ["openbindings.openapi@", "@openbindings/", '"$openbindings"']) {
    if (source.includes(forbidden)) {
      throw new Error(`standalone TypeScript source ${name} leaks internal/OpenBindings concept ${forbidden}`);
    }
  }
}

const goMod = await readFile(new URL("../go/go.mod", import.meta.url), "utf8");
if (goMod.includes("github.com/openbindings/openbindings-go")) {
  throw new Error("standalone Go module depends on the OpenBindings Go SDK");
}
const goFiles = (await readdir(new URL("../go/", import.meta.url)))
  .filter((name) => name.endsWith(".go") && !name.endsWith("_test.go"));
for (const name of goFiles) {
  const source = await readFile(new URL(`../go/${name}`, import.meta.url), "utf8");
  for (const forbidden of [
    "github.com/openbindings/openbindings-go",
    "openbindings.openapi@",
    '"$openbindings"',
    "type BindingInvocationArgs",
    "type InvocationError",
  ]) {
    if (source.includes(forbidden)) {
      throw new Error(`standalone Go source ${name} leaks internal/OpenBindings concept ${forbidden}`);
    }
  }
}

const goCorpusAdapter = await readFile(
  new URL("../go/internal/runtime/upstream_processor_corpus_test.go", import.meta.url),
  "utf8",
);
if (!goCorpusAdapter.startsWith("package openapiclient_test\n") ||
    !goCorpusAdapter.includes('openapi "github.com/openbindings/openapi-client/go"')) {
  throw new Error("Go upstream corpus must exercise the public root package from an external test package");
}

const tsCorpusAdapter = await readFile(
  new URL("../typescript/src/upstream-processor-corpus.test.ts", import.meta.url),
  "utf8",
);
if (!tsCorpusAdapter.includes('from "./index.js";')) {
  throw new Error("TypeScript upstream corpus must exercise the public package entry module");
}

console.log("standalone package boundary verified");
