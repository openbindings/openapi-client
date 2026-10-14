# Logical parameter and form serialization

Parameters and applicable form encodings share one private traversal of scalar,
array and flat-object values. Use `.parameter(...)` and `.form(...)` for checked
Serde input, `.parameter_exact(...)` for an exact parameter, and `.form_fields(...)`
for explicit exact fields or files. The library owns framing; no template or wire-string
constructor is needed. Names, data and structural separators are distinct atoms.

The implemented style profile is:

| Location | Styles and logical values |
| --- | --- |
| Path | `simple`, `label`, `matrix`: scalars, scalar arrays and flat objects |
| Header | `simple`: scalars, scalar arrays and flat objects |
| Query | `form`, flat `deepObject`, and `spaceDelimited`/`pipeDelimited` arrays or flat objects |
| Cookie | `form` with the edition's applicable flag rules |
| Named forms | `form`, flat `deepObject`, and `spaceDelimited`/`pipeDelimited`, subject to edition applicability |

Flat `deepObject` requires scalar members. It requires `explode: true` under the
3.0/3.1 clarification profiles; 3.2 ignores that flag. Space/pipe styles require
`explode: false` (the default); scalar inputs and nested members refuse. Empty
parameter collections still require a policy and refuse; empty form collections
emit nothing. Empty scalar label and matrix fragments are `.` and `;name`.
Completed path segments equal to `.` or `..` still refuse before dispatch.

Nested style values, null conventions, 3.2 `cookie`/`querystring` styles and custom
serialization conventions remain explicit unsupported capabilities. The pinned
Stripe metadata objects are defined; its array-valued `expand` convention still
needs a custom serialization policy.

```rust
use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, exact::*, form::*, graph::*, inspect::*, schema::*, source::*, transport::*};
# fn request(operation: &Operation) -> Result<PreparedRequest, Diagnostic> {
let metadata = std::collections::BTreeMap::from([("order", "o 7"), ("source", "web")]);
operation.request()
    .parameter(ParameterLocation::Query, "metadata", &metadata)
    .prepare()
# }
```

For a `deepObject` declaration this emits
`metadata%5Border%5D=o%207&metadata%5Bsource%5D=web`. The
[stripe_metadata example](examples/stripe_metadata.rs) reads an unchanged provider
file, supplies dummy credentials and prepares two actual operations without
network dispatch.

Style-based URI spaces use `%20`; content-based query parameters and URL forms use `+`. This is a
canonical spelling policy: older `+` style output decoded to the same spaces.
A collection comma stays `,`; a comma inside its data is `%2C`. Content-based
forms retain the WHATWG-compatible tuple encoding profile, including `%7E` for
`~` and literal `*`; default style expansion uses literal `~` and `%2A`.

`allowReserved` applies to query in 3.0/3.1 and additionally path and form-style
cookies in 3.2. Our component-safe expansion preserves applicable reserved data
and valid `%HH` triples, including hex case. Invalid percent signs become `%25`.
It protects raw query/form `#[]&=+`, path `/?#[]`, cookie separators, and an active
collection comma or matrix framing. Names always remain logical names: `%74oken` is encoded as
`%2574oken`, never silently renamed `token`. A generated name such as `meta[x]`
participates in the same credential collision check as an authored name.
Percent triples are not decoded while constructing a request.

Brackets inside a deepObject base/member name require an additional convention
and refuse with `AmbiguousStyleDelimiter`. This is an interoperability policy,
not schema validation. Refusals retain caller context and declaration/value source
owners, and contain no values or secret material in their reason variants.

Exploded label collections reject raw `.` in members/keys: the dot is both data
and structural framing in that context. Non-exploded labels and scalar dots are
supported. Space/pipe styles reject their raw delimiter in members/keys. URI
values with reserved expansion also reject active `%20` or `%7C`/`%7c` delimiters;
without that expansion the percent sign is encoded as data. Names never receive
reserved-triplet privileges. This policy does not recursively decode percent
triples or infer a service's custom delimiter convention.

A content parameter must declare exactly one concrete media entry. JSON media
(`application/json` or an application subtype ending in `+json`) accepts any exact JSON value, preserving
numeric tokens and whitespace inside the value. `text/plain` accepts scalar
string/number/boolean data. Only UTF-8 is supported. Query content uses tuple
encoding; path content uses percent-encoded data without style framing; header
content remains literal UTF-8 with header-injection checks. JSON null is supported;
text null and containers are not coerced. Schema-only style flags do not govern
content parameters. Cookie content and other codecs remain unsupported.

Content codecs do not evaluate the payload schema. An opaque schema reference
does not block these codecs. An actual OAS 3.2 Media Type reference uses the same
snapshot-bound resource-need recovery path as other protocol references. The
[structured_parameters example](examples/structured_parameters.rs) prepares
matrix, pipe and exact JSON content parameters without network dispatch.

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
