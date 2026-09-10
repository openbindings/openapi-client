import test from "node:test";
import assert from "node:assert/strict";
import { isForbiddenPackage, isForbiddenGoPackage } from "./boundary-policy.mjs";

test("only the exact protocol-neutral TypeScript leaves are allowed", () => {
  for (const name of ["@openbindings/json", "@openbindings/json-schema", "js-yaml"]) {
    assert.equal(isForbiddenPackage(name), false, name);
  }
  for (const name of ["core", "sdk", "invoke", "synthesize", "json-extra", "json/invoke"]) {
    assert.equal(isForbiddenPackage(`@openbindings/${name}`), true, name);
  }
});

test("Go module ownership never grants access to Core or other internal packages", () => {
  const prefix = "github.com/openbindings/openbindings-go";
  for (const name of ["jsonvalue", "internal/jstring", "internal/thirdparty/jsoncodec"]) {
    assert.equal(isForbiddenGoPackage(`${prefix}/${name}`), false, name);
  }
  for (const suffix of ["", "/sdk", "/invoke", "/synthesize", "/jsonvalue/extra", "/internal/thirdparty/jsonschema"]) {
    assert.equal(isForbiddenGoPackage(prefix + suffix), true, suffix);
  }
});
