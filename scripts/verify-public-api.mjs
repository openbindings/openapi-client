import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const expected = JSON.parse(await readFile(resolve(root, "api/public-api-v1.json"), "utf8"));
const typeScript = await readFile(resolve(root, "typescript/dist/index.d.ts"));
const goDocumentation = execFileSync("go", ["doc", "-all", "."], {
  cwd: resolve(root, "go"),
  env: { ...process.env, GOWORK: "off" },
});

const actual = {
  typescriptDeclarationSha256: digest(typeScript),
  goDocumentationSha256: digest(goDocumentation),
};

for (const [name, value] of Object.entries(actual)) {
  if (expected[name] !== value) {
    throw new Error(`${name} changed: expected ${expected[name]}, got ${value}; review the public API and intentionally refresh api/public-api-v1.json`);
  }
}

console.log("TypeScript and Go public API snapshots verified");

function digest(value) {
  return createHash("sha256").update(value).digest("hex");
}
