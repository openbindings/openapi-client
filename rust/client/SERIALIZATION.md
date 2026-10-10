# Logical parameter and form serialization

Parameters and applicable form encodings share one private traversal of scalar,
array and flat-object values. Keep using `.parameter(...)` and `.form(...)` with
ordinary/exact inputs. The library owns framing; no template or wire-string
constructor is needed. Names, data and structural separators are distinct atoms.

The current defined styles are `simple` for path/header, `form` for query/cookie
and form encodings, and `deepObject` for query and applicable form encodings.
Flat `deepObject` requires scalar members. It requires `explode: true` under the
3.0/3.1 clarification profiles; 3.2 ignores that flag. Nested objects, arrays as
a deepObject parameter, null conventions and advanced styles remain explicit
unsupported capabilities. The pinned Stripe metadata objects are defined; its
array-valued `expand` convention still needs a custom serialization policy.

```rust
use dynamic_openapi_client::*;
# fn request(operation: &Operation) -> Result<PreparedRequest, RequestError> {
let metadata = [("order", OrdinaryValue::String("o 7")),
                ("source", OrdinaryValue::String("web"))];
operation.request()
    .parameter(ParameterLocation::Query, "metadata", OrdinaryValue::Object(&metadata))
    .prepare()
# }
```

For a `deepObject` declaration this emits
`metadata%5Border%5D=o%207&metadata%5Bsource%5D=web`. The
[stripe_metadata example](examples/stripe_metadata.rs) reads an unchanged provider
file, supplies dummy credentials and prepares two actual operations without
network dispatch.

Style-based URI spaces use `%20`; content-based URL forms use `+`. This is a
canonical spelling policy: older `+` style output decoded to the same spaces.
A collection comma stays `,`; a comma inside its data is `%2C`. Content-based
forms retain the WHATWG-compatible tuple encoding profile, including `%7E` for
`~` and literal `*`; default style expansion uses literal `~` and `%2A`.

`allowReserved` applies to query in 3.0/3.1 and additionally path and form-style
cookies in 3.2. Our component-safe expansion preserves applicable reserved data
and valid `%HH` triples, including hex case. Invalid percent signs become `%25`.
It protects raw query/form `#[]&=+`, path `/?#[]`, cookie separators, and an active
collection comma. Names always remain logical names: `%74oken` is encoded as
`%2574oken`, never silently renamed `token`. A generated name such as `meta[x]`
participates in the same credential collision check as an authored name.
Percent triples are not decoded while constructing a request.

Brackets inside a deepObject base/member name require an additional convention
and refuse with `AmbiguousStyleDelimiter`. This is an interoperability policy,
not schema validation. Refusals retain caller context and declaration/value source
owners, and contain no values or secret material in their reason variants.

Multipart data remains literal, including percent triples, spaces and Unicode.
OAS 3.0 ignores multipart style fields; 3.1/3.2 apply them without URI escaping.
OAS 3.2 expands outer named arrays before applying the encoding to each item.
Exact JSON part tokens and exact numbers retain their existing representation.
No defaults, schema branches, content codings or implicit fetches are introduced.

Parameter output is counted before allocation, bounded by target/header/input
limits and checked for cancellation during traversal and encoding. Forms retain
independent metadata, work, parts and complete-body limits. `form_work` does not
limit parameters. Security, origin scoping and the no-replay transport contract
remain in the shared preparation path. See [FORMS.md](FORMS.md) and
[GRAPH.md](GRAPH.md) for their separate limitations.

Sources: [3.0.4](https://spec.openapis.org/oas/v3.0.4.html#parameter-object),
[3.1.2](https://spec.openapis.org/oas/v3.1.2.html#parameter-object),
[3.2.1](https://spec.openapis.org/oas/v3.2.1.html#parameter-object),
[OAS encoding guidance](https://spec.openapis.org/oas/v3.2.1.html#appendix-e-percent-encoding-and-form-media-types),
[RFC6570](https://www.rfc-editor.org/rfc/rfc6570.html).
