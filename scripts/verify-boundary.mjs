import { readFile, readdir } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import { isForbiddenPackage, isForbiddenGoPackage } from "./boundary-policy.mjs";

// Repository ownership is not an architectural dependency. These qualified
// leaves are protocol-neutral; Core, invoker and synthesis remain forbidden.

const packageJSON = JSON.parse(await readFile(new URL("../typescript/package.json", import.meta.url), "utf8"));
const dependencies = Object.keys(packageJSON.dependencies ?? {});
const forbiddenDependencies = dependencies.filter(isForbiddenPackage);
if (forbiddenDependencies.length > 0) {
  throw new Error(`standalone package has OpenBindings runtime dependencies: ${forbiddenDependencies.join(", ")}`);
}

const exportedPaths = Object.keys(packageJSON.exports ?? {});
const expectedExportedPaths = [".", "./provider"];
if (exportedPaths.join(",") !== expectedExportedPaths.join(",")) {
  throw new Error(`standalone package must expose exactly its native client and provider entry points, got ${exportedPaths.join(", ")}`);
}

const declarations = [
  await readFile(new URL("../typescript/dist/index.d.ts", import.meta.url), "utf8"),
  await readFile(new URL("../typescript/dist/provider.d.ts", import.meta.url), "utf8"),
].join("\n");
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
const tsFiles = (await readdir(new URL("../typescript/src/", import.meta.url), { recursive: true }))
  .filter((name) => name.endsWith(".ts") && !name.endsWith(".test.ts") && !name.endsWith(".d.ts"));
for (const name of tsFiles) {
  const source = await readFile(new URL(`../typescript/src/${name}`, import.meta.url), "utf8");
  for (const imported of source.matchAll(/(?:from\s+|import\s*\()?["'](@openbindings\/[^"']+)["']/g)) {
    if (isForbiddenPackage(imported[1])) {
      throw new Error(`standalone TypeScript source ${name} depends on ${imported[1]}`);
    }
  }
  // The existing vocabulary check targets the public-facing source layer;
  // internal implementation comments are not exported API declarations.
  for (const forbidden of ["openbindings.openapi@", '"$openbindings"']) {
    if (!name.includes("/") && source.includes(forbidden)) {
      throw new Error(`standalone TypeScript source ${name} leaks internal/OpenBindings concept ${forbidden}`);
    }
  }
}

// jsonvalue is hosted in the SDK module but imports no OpenBindings runtime.
// Inspect the complete compiled dependency closure, including internal files,
// rather than granting that entire module a namespace exception.
const goDependencies = execFileSync("go", ["list", "-deps", "./..."], {
  cwd: new URL("../go/", import.meta.url), encoding: "utf8",
}).trim().split("\n");
for (const dependency of goDependencies) {
  if (isForbiddenGoPackage(dependency)) {
    throw new Error(`standalone Go dependency closure contains ${dependency}`);
  }
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
const goProvider = await readFile(new URL("../go/provider/provider.go", import.meta.url), "utf8");
for (const forbidden of ["github.com/openbindings/openbindings-go", "openbindings.openapi@", '"$openbindings"']) {
  if (goProvider.includes(forbidden)) {
    throw new Error(`standalone Go provider source leaks internal/OpenBindings concept ${forbidden}`);
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
