# Developing the Go client

The public API in `openapi/` and `openapi/schema2020/` is designed first
and implemented beneath, in the stages `DESIGN.md` lists. This file says
how each stage is built and what it must pass.

## The API is fixed

The exported declarations and their documentation are the contract.
Implementation never changes them for its own convenience. A change is one
of three kinds:

1. A clarification: wording that makes an existing rule precise without
   changing behavior.
2. A correction: a rule found to contradict the OpenAPI specifications or
   an RFC they rely on, or one no implementation could honor.
3. A change to the exported surface or to a design ruling. This needs the
   project owner's decision.

Release qualification compares the Go documentation with the reviewed
snapshot in `../api/public-api-v1.json`, so any change is deliberate: the
snapshot is refreshed in the same change, and the change says which kind
it is.

## Each stage

1. **Tests first.** Tests are written from the documented contract and the
   specifications before the implementation, each citing the rule it
   checks. The implementation does not decide what is correct; a test that
   seems wrong is corrected on its own merits.
2. **Implementation** makes the tests pass with the least code that meets
   the contract at full performance.
3. **Gates.** `gofmt -l .` prints nothing, `go vet ./...` and
   `go test -race ./...` pass, fuzz targets run for a bounded time, the
   benchmarks stay within budget, and repository qualification passes.
   Run Go with `GOWORK=off`.
4. **Review** by independent readers for Go idiom and code quality,
   performance, security, conformance to the contract and specifications,
   and adversarial inputs, followed by fixes.
5. **Land** on `main` through a pull request.

Examples in `openapi/example_test.go` stay as documentation; each stage adds
tests that run the same flows against real test servers.

## Performance

`openapi/bench_test.go` compares calls with the same requests written by
hand with net/http, and measures loading a large document. Report
latency, allocations and bytes with `-benchmem`. A stage may not regress a
benchmark beyond its budget without a recorded reason. Profile before
optimizing, and profile at the end of every stage.

## Code

The engine uses the standard library and the copied YAML parser under
`internal/yaml`, whose provenance and maintained changes are recorded there.
Additional dependencies require a project-owner decision. No dead code and
nothing the tests do not justify. An internal package must earn its place.
