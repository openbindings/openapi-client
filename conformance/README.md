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

The next migration unit is the existing OpenBindings OpenAPI artifact corpus and invocation suite. Cases should move by ownership: artifact-execution assertions belong here; OBI synthesis and abstraction assertions remain in the binding package.
