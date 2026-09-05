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

  const documents = JSON.stringify([
    {
      edition: "2.0",
      document: {
        swagger: "2.0",
        info: { title: "Swagger", version: "1" },
        schemes: ["https"],
        host: "api.example.test",
        paths: { "/ping": { get: { operationId: "ping20", responses: { "204": { description: "done" } } } } },
      },
      operationId: "ping20",
    },
    ...["3.0.4", "3.1.2", "3.2.0"].map((edition) => ({
      edition,
      document: {
        openapi: edition,
        info: { title: `OpenAPI ${edition}`, version: "1" },
        servers: [{ url: "https://api.example.test" }],
        paths: { "/ping": { get: { operationId: `ping${edition.replaceAll(".", "")}`, responses: { "204": { description: "done" } } } } },
      },
      operationId: `ping${edition.replaceAll(".", "")}`,
    })),
  ]);
  await writeFile(join(typeScriptConsumer, "esm.mjs"), `
import assert from "node:assert/strict";
import { OpenAPIClient } from "@openbindings/openapi-client";
const documents = ${documents};
for (const fixture of documents) {
  const client = await OpenAPIClient.load(fixture.document, {
    fetch: async () => new Response(null, { status: 204 }),
  });
  assert.equal(client.edition, fixture.edition);
  assert.deepEqual(client.operations().map(({ operationId }) => operationId), [fixture.operationId]);
  assert.equal((await client.operation(fixture.operationId).call()).ok, true);
}
`);
  await writeFile(join(typeScriptConsumer, "cjs.cjs"), `
const assert = require("node:assert/strict");
const { OpenAPIClient } = require("@openbindings/openapi-client");
(async () => {
  const fixture = ${documents}[2];
  const client = await OpenAPIClient.load(fixture.document);
  assert.equal(client.edition, "3.1.2");
  assert.deepEqual(client.operations().map(({ operationId }) => operationId), [fixture.operationId]);
})().catch((error) => { console.error(error); process.exitCode = 1; });
`);
  await writeFile(join(typeScriptConsumer, "consumer.ts"), `
import {
  OpenAPIClient,
  OpenAPIClientError,
  type OpenAPIResult,
} from "@openbindings/openapi-client";

declare const document: Record<string, unknown>;
async function consume(): Promise<void> {
  const client = await OpenAPIClient.load(document, {
    fetch: async () => new Response(null, { status: 204 }),
  });
  const result: OpenAPIResult<{ name: string }, { message: string }> =
    await client.call("getPet", { parameters: { path: { id: "p-1" } } });
  if (result.ok) result.data?.name.toUpperCase();
  else result.error?.message.toUpperCase();
  try {
    await client.stream("watchPets");
  } catch (error) {
    if (error instanceof OpenAPIClientError) error.kind satisfies string;
  }
}
void consume;
`);
  await writeFile(join(typeScriptConsumer, "consumer.cts"), `
import openapi = require("@openbindings/openapi-client");
const Client: typeof openapi.OpenAPIClient = openapi.OpenAPIClient;
const ErrorType: typeof openapi.OpenAPIClientError = openapi.OpenAPIClientError;
void Client;
void ErrorType;
`);
  await writeFile(join(typeScriptConsumer, "tsconfig.json"), `${JSON.stringify({
    compilerOptions: {
      target: "ES2022",
      module: "NodeNext",
      moduleResolution: "NodeNext",
      lib: ["ES2022", "DOM", "DOM.Iterable"],
      strict: true,
      noEmit: true,
      skipLibCheck: false,
    },
    include: ["consumer.ts", "consumer.cts"],
  }, null, 2)}\n`);
  await run(process.execPath, ["esm.mjs"], typeScriptConsumer);
  await run(process.execPath, ["cjs.cjs"], typeScriptConsumer);
  await run(process.execPath, [
    resolve(root, "typescript", "node_modules", "typescript", "bin", "tsc"),
    "--project",
    "tsconfig.json",
  ], typeScriptConsumer);

  const goModule = resolve(root, "go").replaceAll("\\", "/");
  await writeFile(join(goConsumer, "go.mod"), `module releaseconsumer

go 1.25.12

require github.com/openbindings/openapi-client/go v0.0.0

replace github.com/openbindings/openapi-client/go => ${goModule}
`);
  await writeFile(join(goConsumer, "client_test.go"), `package releaseconsumer

import (
  "context"
  "io"
  "net/http"
  "strings"
  "testing"

  openapiclient "github.com/openbindings/openapi-client/go"
)

type transportFunc func(*http.Request) (*http.Response, error)
func (f transportFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestCleanConsumer(t *testing.T) {
  fixtures := []struct { edition openapiclient.Edition; operationID, document string }{
    {openapiclient.Swagger20, "ping20", ${JSON.stringify(JSON.stringify(JSON.parse(documents)[0].document))}},
    {openapiclient.OpenAPI304, "ping304", ${JSON.stringify(JSON.stringify(JSON.parse(documents)[1].document))}},
    {openapiclient.OpenAPI312, "ping312", ${JSON.stringify(JSON.stringify(JSON.parse(documents)[2].document))}},
    {openapiclient.OpenAPI320, "ping320", ${JSON.stringify(JSON.stringify(JSON.parse(documents)[3].document))}},
  }
  transport := transportFunc(func(request *http.Request) (*http.Response, error) {
    return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
  })
  for _, fixture := range fixtures {
    client, err := openapiclient.Load(context.Background(), openapiclient.FromText(fixture.document), openapiclient.Options{
      HTTPClient: &http.Client{Transport: transport},
    })
    if err != nil { t.Fatal(err) }
    if client.Edition() != fixture.edition { t.Fatalf("edition = %q", client.Edition()) }
    operation, err := client.Operation(openapiclient.OperationID(fixture.operationID))
    if err != nil { t.Fatal(err) }
    result, err := operation.Call(context.Background(), openapiclient.Input{})
    if err != nil || !result.OK { t.Fatalf("result=%#v err=%v", result, err) }
  }
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
  assert.deepEqual(Object.keys(manifest.exports), ["."]);
  console.log("clean TypeScript ESM/CJS runtime and type consumers, plus Go consumer, verified");
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
