import { spawn } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const corpusRoot = resolve(
  process.env.OPENAPI_UPSTREAM_CORPUS_ROOT
    ?? resolve(root, "conformance/upstream/openbindings-0.2/processor"),
);
const environment = {
  ...process.env,
  GOWORK: "off",
  OPENAPI_UPSTREAM_CORPUS: "1",
  OPENAPI_UPSTREAM_CORPUS_ROOT: corpusRoot,
};

await run("pnpm", ["--dir", "typescript", "exec", "vitest", "run", "src/upstream-processor-corpus.test.ts"]);
await run("go", ["-C", "go", "test", "./internal/runtime", "-run", "TestUpstreamProcessorCorpus", "-count=1"]);

console.log(`public TypeScript and Go clients passed the processor corpus at ${corpusRoot}`);

function run(command, args) {
  return new Promise((resolveRun, rejectRun) => {
    const child = spawn(command, args, { cwd: root, env: environment, stdio: "inherit" });
    child.on("error", rejectRun);
    child.on("exit", (code, signal) => {
      if (code === 0) resolveRun();
      else rejectRun(new Error(`${command} ${args.join(" ")} exited with ${code ?? signal}`));
    });
  });
}
