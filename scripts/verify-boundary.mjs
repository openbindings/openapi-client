import { readFile, readdir } from "node:fs/promises";

const packageJSON = JSON.parse(await readFile(new URL("../typescript/package.json", import.meta.url), "utf8"));
const dependencies = Object.keys(packageJSON.dependencies ?? {});
const forbiddenDependencies = dependencies.filter((name) => name.startsWith("@openbindings/"));
if (forbiddenDependencies.length > 0) {
  throw new Error(`standalone package has OpenBindings runtime dependencies: ${forbiddenDependencies.join(", ")}`);
}

const exportedPaths = Object.keys(packageJSON.exports ?? {});
if (exportedPaths.join(",") !== ".,./engine,./analysis") {
  throw new Error(`standalone package must expose native client, engine, and analysis entry points, got ${exportedPaths.join(", ")}`);
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

const engineDeclarations = await readFile(new URL("../typescript/dist/engine.d.ts", import.meta.url), "utf8");
for (const forbidden of [
  "BindingInvocationArgs",
  "ContextRequiredDetails",
  "InvocationError",
  "bindingSpec",
  "@openbindings/",
]) {
  if (engineDeclarations.includes(forbidden)) {
    throw new Error(`engine declarations leak internal/OpenBindings concept ${forbidden}`);
  }
}

const analysisDeclarations = await readFile(new URL("../typescript/dist/analysis.d.ts", import.meta.url), "utf8");
for (const forbidden of ["BindingInvocationArgs", "InvocationError", "bindingSpec", "@openbindings/"]) {
  if (analysisDeclarations.includes(forbidden)) {
    throw new Error(`analysis declarations leak internal/OpenBindings concept ${forbidden}`);
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

console.log("standalone package boundary verified");
