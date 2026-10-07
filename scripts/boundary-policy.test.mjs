import test from "node:test";
import assert from "node:assert/strict";
import {
  goDeclaredNames,
  isForbiddenGoPackage,
  isForbiddenPackage,
  processLabelInGoFileName,
  processLabelInGoName,
} from "./boundary-policy.mjs";

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

test("Go names carry no development-process label", () => {
  for (const [name, label] of [
    ["TestStage6RawCookieNameTokens", "Stage6"],
    ["TestReview9DialectInputParity", "Review9"],
    ["TestReview8MediaTypeOverride", "Review8"],
    ["BenchmarkReviewOctal", "Review"],
    ["TestReviewRepairWindowsPaths", "Review"],
    ["TestOverrideRepair", "Repair"],
    ["TestEmptyValueRulings", "Rulings"],
    ["TestLedger", "Ledger"],
    ["TestStreamExistingBehaviorPins", "Pins"],
    ["TestRegress2Upload", "Regress2"],
    ["FuzzStream7SSEFields", "Stream7"],
    ["TestIP4F8ZeroClient", "IP4F8"],
    ["TestIP4F5ResponseMedia", "IP4F5"],
    ["BenchmarkSchema9LookupFirst", "Schema9"],
    ["BenchmarkPrepared8SendBody", "Prepared8"],
    ["TestC1DecodeErrorOmitsBody", "C1"],
    ["TestC41CyclesRefused", "C41"],
    ["TestC48LongChainTrueDefault", "C48"],
    ["TestF1CyclicBody", "F1"],
    ["TestF12HeaderFieldValidation", "F12"],
    ["TestF41DotSegmentValues", "F41"],
    ["TestG1RejectedDocumentAllocation", "G1"],
    ["TestG18ServerVariableDotSegments", "G18"],
    ["TestH1SharedOperationObject", "H1"],
    ["TestH10UnusableServerFromValues", "H10"],
    ["TestK1ReadersInStyleValues", "K1"],
    ["TestK11UnknownKeyScales", "K11"],
    ["TestT1_15NilPointerOfAnyType", "T1_15"],
    ["TestT2_1DotSegmentFromTwoValues", "T2_1"],
    ["TestVP7NumberWithIntegerIsInteger", "VP7"],
    ["TestVN8EventStreamRefusals", "VN8"],
    ["TestVPP1FormScalarBoundary", "VPP1"],
    ["TestRQ3StringOption", "RQ3"],
    ["TestRQ6EncodingOrder", "RQ6"],
    ["TestIFP9ClosedIteratorBody", "IFP9"],
    ["TestIFP10ObjectsWhereArraysBelong", "IFP10"],
    ["TestA8ContentTypeList", "A8"],
    ["TestA10PartHeaderScales", "A10"],
    ["checkG2", "G2"],
    ["benchPrepared8Doc", "Prepared8"],
    ["stream7Client", "stream7"],
    ["stage6Strings", "stage6"],
    ["review9DeepFixture", "review9"],
    ["schema9Parse", "schema9"],
    ["prepared8Tunnel", "prepared8"],
    ["ip4f8Client", "ip4f8"],
    ["c1Target", "c1"],
    ["g2Doc", "g2"],
    ["vppPaths", "vpp"],
    ["vnHolder", "vn"],
  ]) {
    assert.equal(processLabelInGoName(name), label, name);
  }
  for (const name of [
    "TestYAMLOctalValuesExact", "FuzzYAMLRobustness", "TestRegressionFree", "TestPreviewText",
    "TestReviewerNotes", "TestReviewed", "TestRepairsNothing", "TestPinsetup", "TestUTF8Text", "TestHTTP2Server",
    "TestK8sClient", "TestF42Unlisted", "TestC12", "TestVPNTunnel", "TestStreamItems", "TestSchemaGraphWorkload",
    "BenchmarkPreparedSendBody", "c1", "k2", "c12", "h2c", "vndJSON", "streamClient", "sha256Sum", "base64Text",
    "TestSHA1", "TestOAS31Dialect", "dialectOAS32", "TestRFC6570Styles",
  ]) {
    assert.equal(processLabelInGoName(name), undefined, name);
  }
});

test("Go file names carry no development-process label", () => {
  for (const [name, label] of [
    ["stage6_scale_test.go", "stage6"],
    ["regress_test.go", "regress"],
    ["regress2_scale_test.go", "regress2"],
    ["docreview_cost_test.go", "review"],
    ["schema_review_scale_test.go", "review"],
    ["bench_review_test.go", "review"],
    ["docrulings_test.go", "ruling"],
    ["stream_pins_test.go", "pins"],
    ["c1_test.go", "c1"],
    ["regress_ip4f8_unix_test.go", "regress"],
    ["ip4f8_test.go", "ip4f8"],
    ["stream7_test.go", "stream7"],
    ["review_repair_test.go", "review"],
    ["ledger.go", "ledger"],
    ["f12_test.go", "f12"],
    ["vpp_test.go", "vpp"],
  ]) {
    assert.equal(processLabelInGoFileName(name), label, name);
  }
  for (const name of [
    "scaling_test.go", "schema_dialect_redaction_test.go", "schema_diagnostic_regression_test.go", "preview_test.go",
    "bench_prepared_test.go", "stream_send_raw_targets_test.go", "fuzzstream_mime_test.go", "mapping_test.go",
    "spinner.go", "http2_test.go", "decodeerror_text_test.go", "deepobjectarrays_test.go",
  ]) {
    assert.equal(processLabelInGoFileName(name), undefined, name);
  }
});

test("declared Go names are read from code, not comments or literals", () => {
  const source = [
    "package p",
    "",
    "// func TestStage6InAComment() and type review9Hidden are prose.",
    "const one, two = 1, 2",
    "var (",
    "\tx, y = f(`func TestF1InARawString()`)",
    "\tz    = []int{",
    "\t\t3,",
    "\t}",
    ")",
    "type (",
    "\tpoint[T any] struct{ X, Y T }",
    "\tlabel string",
    ")",
    "type single struct{ v int }",
    "",
    "func (s *single) Method() string { return \"var quoted int\" }",
    "",
    "func generic[T any](v T) T {",
    "\tvar local int",
    "\tconst inner = 'x'",
    "\ttype shadow int",
    "\t_ = func() {}",
    "\treturn v",
    "}",
  ].join("\n");
  assert.deepEqual(goDeclaredNames(source).map(({ name, line }) => `${name}:${line}`).sort(), [
    "Method:17", "generic:19", "inner:21", "label:13", "local:20", "one:4", "point:12", "shadow:22", "single:15",
    "two:4", "x:6", "y:6", "z:7",
  ]);
});
