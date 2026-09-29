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

test("the Go module depends on no OpenBindings package but its own", () => {
  for (const name of ["openapi-client/go", "openapi-client/go/openapi", "openapi-client/go/openapi/schema2020"]) {
    assert.equal(isForbiddenGoPackage(`github.com/openbindings/${name}`), false, name);
  }
  for (const name of ["openbindings-go", "openbindings-go/jsonvalue", "openbindings-go/internal/jstring", "openapi-client/gox", "asyncapi-client/go"]) {
    assert.equal(isForbiddenGoPackage(`github.com/openbindings/${name}`), true, name);
  }
});
