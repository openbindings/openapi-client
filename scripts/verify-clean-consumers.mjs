import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const temporaryRoot = await mkdtemp(join(tmpdir(), "openbindings-openapi-client-release-"));

try {
  const packageDirectory = join(temporaryRoot, "package");
  const typeScriptConsumer = join(temporaryRoot, "typescript-consumer");
  const goConsumer = join(temporaryRoot, "go-consumer");
  await Promise.all([
    mkdir(packageDirectory),
    mkdir(typeScriptConsumer),
    mkdir(goConsumer),
  ]);
  const npmEnvironment = { npm_config_cache: join(temporaryRoot, "npm-cache") };

  await run(
    "npm",
    ["pack", "--json", "--pack-destination", packageDirectory],
    join(root, "typescript"),
    npmEnvironment,
  );
  const archives = (await readdir(packageDirectory)).filter((name) => name.endsWith(".tgz"));
  assert.equal(archives.length, 1, `expected one npm archive, got ${archives.join(", ")}`);
  const archive = join(packageDirectory, archives[0]);

  await writeFile(join(typeScriptConsumer, "package.json"), `${JSON.stringify({
    name: "openapi-client-release-consumer",
    private: true,
    type: "module",
  }, null, 2)}\n`);
  await run("npm", [
    "install",
    "--ignore-scripts",
    "--no-audit",
    "--no-fund",
    "--no-package-lock",
    archive,
  ], typeScriptConsumer, npmEnvironment);

  const document = JSON.stringify({
    openapi: "3.1.2",
    info: { title: "clean consumer", version: "1" },
    servers: [{ url: "https://api.example.test" }],
    paths: {
      "/ping": {
        get: {
          operationId: "ping",
          responses: { "204": { description: "done" } },
        },
      },
    },
  });
  await writeFile(join(typeScriptConsumer, "esm.mjs"), `
import assert from "node:assert/strict";
import { OpenAPIClient } from "@openbindings/openapi-client";
const client = await OpenAPIClient.load(${document});
assert.deepEqual(client.operations().map(({ operationId }) => operationId), ["ping"]);
`);
  await writeFile(join(typeScriptConsumer, "cjs.cjs"), `
const assert = require("node:assert/strict");
const { OpenAPIClient } = require("@openbindings/openapi-client");
(async () => {
  const client = await OpenAPIClient.load(${document});
  assert.deepEqual(client.operations().map(({ operationId }) => operationId), ["ping"]);
})().catch((error) => { console.error(error); process.exitCode = 1; });
`);
  await run(process.execPath, ["esm.mjs"], typeScriptConsumer);
  await run(process.execPath, ["cjs.cjs"], typeScriptConsumer);

  const goModule = resolve(root, "go").replaceAll("\\", "/");
  await writeFile(join(goConsumer, "go.mod"), `module releaseconsumer

go 1.25.12

require github.com/openbindings/openapi-client/go v0.0.0

replace github.com/openbindings/openapi-client/go => ${goModule}
`);
  await writeFile(join(goConsumer, "client_test.go"), `package releaseconsumer

import (
  "context"
  "testing"

  openapiclient "github.com/openbindings/openapi-client/go"
)

func TestCleanConsumer(t *testing.T) {
  document := []byte(${JSON.stringify(document)})
  client, err := openapiclient.Load(context.Background(), openapiclient.Source{Content: document}, openapiclient.ClientOptions{})
  if err != nil { t.Fatal(err) }
  operations := client.Operations()
  if len(operations) != 1 || operations[0].OperationID != "ping" { t.Fatalf("operations = %#v", operations) }
}
`);
  await run("go", ["mod", "tidy"], goConsumer, { GOWORK: "off" });
  await run("go", ["test", "./..."], goConsumer, { GOWORK: "off" });

  const manifest = JSON.parse(await readFile(join(
    typeScriptConsumer,
    "node_modules",
    "@openbindings",
    "openapi-client",
    "package.json",
  ), "utf8"));
  assert.equal(manifest.name, "@openbindings/openapi-client");
  assert.deepEqual(Object.keys(manifest.exports), [".", "./engine", "./analysis"]);
  console.log("clean TypeScript ESM/CJS and Go consumers verified");
} finally {
  const expectedPrefix = join(tmpdir(), "openbindings-openapi-client-release-");
  if (!temporaryRoot.startsWith(expectedPrefix)) {
    throw new Error(`refusing to clean unexpected path ${temporaryRoot}`);
  }
  await rm(temporaryRoot, { recursive: true });
}

function run(command, args, cwd, environment = {}) {
  return new Promise((resolveRun, rejectRun) => {
    const child = spawn(command, args, {
      cwd,
      env: { ...process.env, ...environment },
      stdio: "inherit",
    });
    child.on("error", rejectRun);
    child.on("exit", (code, signal) => {
      if (code === 0) resolveRun();
      else rejectRun(new Error(`${command} ${args.join(" ")} exited with ${code ?? signal}`));
    });
  });
}
