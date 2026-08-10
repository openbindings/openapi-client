# Conformance

Conformance is behavioral, not snapshot-based. A case describes an OpenAPI artifact, a native call, and observable facts at four boundaries:

1. selection and pre-dispatch refusal;
2. exact HTTP request;
3. native result or stream;
4. lifecycle and cancellation.

The suite must include OpenAPI 3.0 and 3.1, JSON and YAML, local and external references, documents from unrelated producers, adversarial minimized fixtures, and differential checks against independent OpenAPI/HTTP implementations where a credible oracle exists.

Corpus discoveries are promoted into small specification-rule fixtures. Product code must never special-case a filename, repository, generator signature, operation id, vendor extension, or other corpus accident.

## Initial native contract

The TypeScript tests currently cover:

- operation-id, path/method, and canonical-reference selection;
- independent same-named path, query, and body values;
- scheme-name-keyed API-key and bearer placement;
- declared non-2xx bodies plus status/header/declaration evidence;
- falsy whole bodies;
- explicit empty optional object body versus omitted body;
- protocol-aware middleware;
- SSE ordering, completion, and unary misuse refusal;
- typed local selection/configuration failures.

The existing OpenBindings OpenAPI artifact corpus and invocation suite remain an expansion source. Cases move by ownership: artifact-execution assertions belong here; OBI synthesis and abstraction assertions remain in the binding package.

## Cross-language gate

`cases/native-wire.json` is executed by both TypeScript and Go. It covers
OpenAPI 3.0 and 3.1, collision-preserving grouped inputs, parameter
serialization, scheme-named security placement, falsy whole bodies, and rich
declared HTTP failures. New cross-language behavior belongs in this format
when it can be stated without language- or implementation-specific machinery.

The shared set is a floor, not a representative corpus by itself. Each
language also runs its deeper artifact, streaming, external-reference,
adversarial, and adapter suites; corpus discoveries are promoted here only
when a stable language-neutral oracle can be stated.
