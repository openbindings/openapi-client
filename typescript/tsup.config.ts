import { defineConfig } from "tsup";

export default defineConfig({
  entry: {
    index: "src/index.ts",
    provider: "src/provider.ts",
  },
  format: ["esm", "cjs"],
  dts: true,
  sourcemap: true,
  clean: true,
});
