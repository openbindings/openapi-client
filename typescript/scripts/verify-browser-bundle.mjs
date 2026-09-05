import assert from "node:assert/strict";
import { build } from "esbuild";

const result = await build({
  stdin: {
    contents: `
      import { OpenAPIClient } from "./dist/index.js";
      globalThis.OpenAPIClient = OpenAPIClient;
    `,
    resolveDir: process.cwd(),
    sourcefile: "clean-browser-consumer.mjs",
  },
  bundle: true,
  format: "esm",
  platform: "browser",
  target: "es2022",
  write: false,
  metafile: true,
  logLevel: "warning",
});

assert.equal(result.outputFiles.length, 1, "browser build must produce one JavaScript bundle");
assert.ok(result.outputFiles[0].contents.length > 0, "browser bundle must not be empty");
assert.deepEqual(
  Object.values(result.metafile.outputs).flatMap((output) => output.imports.filter(({ external }) => external)),
  [],
  "browser bundle must not retain external runtime imports",
);

// Execute the bundled bytes through web-standard APIs as well as inspecting
// their dependency graph. This catches tree-shaking or code-splitting mistakes
// that can still produce a superficially valid browser bundle.
const bundleURL = `data:text/javascript;base64,${Buffer.from(result.outputFiles[0].contents).toString("base64")}`;
await import(bundleURL);
const OpenAPIClient = globalThis.OpenAPIClient;
assert.equal(typeof OpenAPIClient?.load, "function", "browser bundle must expose the client");
for (const edition of ["2.0", "3.0.4", "3.1.2", "3.2.0"]) {
  const operationId = `ping${edition.replaceAll(".", "")}`;
  const document = edition === "2.0"
    ? {
      swagger: edition,
      info: { title: "Browser smoke", version: "1" },
      schemes: ["https"],
      host: "api.example.test",
      paths: { "/ping": { get: { operationId, responses: { 204: { description: "done" } } } } },
    }
    : {
      openapi: edition,
      info: { title: "Browser smoke", version: "1" },
      servers: [{ url: "https://api.example.test" }],
      paths: { "/ping": { get: { operationId, responses: { 204: { description: "done" } } } } },
    };
  const client = await OpenAPIClient.load(document, {
    fetch: async () => new Response(null, { status: 204 }),
    transport: null,
  });
  assert.equal(client.edition, edition);
  assert.equal((await client.call(operationId)).ok, true);
}
delete globalThis.OpenAPIClient;

console.log(`browser consumer bundle verified (${result.outputFiles[0].contents.length} bytes)`);
