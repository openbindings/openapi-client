# JSON and YAML document loading

`Document::parse(bytes, original_url, limits)` detects the serialization internally. The same document, operation and request types apply to every supported OpenAPI edition. The optional URL is the original retrieval identity/base, not a request-target override. Loading supplied bytes performs no I/O. The exact JSON parser runs first; JSON syntax failures may select YAML, including YAML flow collections beginning with `{` or `[`. Duplicate names, Unicode restrictions and JSON resource refusals are never bypassed by YAML fallback. A preliminary depth refusal is disambiguated because JSON-shaped punctuation can occur in YAML strings. When both grammars fail on brace/bracket-leading input, the JSON diagnostic is retained. `exact::Value::parse` and response JSON decoding remain JSON-only.

`Document::source()` retains the complete original text, including comments, scalar notation and whitespace. `Document::format()` reports the detected format for inspection, without being required to invoke operations.

## Values and provenance

JSON values retain the existing exact token and source-range contract. YAML values use an exact JSON semantic representation, with `SourceOrigin::Normalized`, and a separate original YAML source map. This changes serialization, not the OpenAPI edition or schema dialect. No floats, integer narrowing, schema evaluation, default insertion or version conversion are involved.

`Value::raw()`, `Value::location()` and `Value::source_context()` always refer to the indexed JSON representation. For an authored YAML number such as `+001.2500`, `raw()` is `1.2500`; its mathematical value remains exact. Use `Value::source_trace()` to inspect the original notation and coordinates:

```rust
use dynamic_openapi_client::{Document, Limits};
use dynamic_openapi_client::source::{SourceOrigin};
let document = Document::parse(
    b"openapi: 3.1.0\ninfo: {title: example, version: v1}\nx-number: +001.2500\npaths: {}\n",
    Some("https://api.example/openapi.yaml"),
    Limits::default(),
)?;
let number = document.root().get("x-number").unwrap();
assert_eq!(number.raw(), "1.2500");
assert_eq!(number.source_context().origin, SourceOrigin::Normalized);
let trace = number.source_trace();
let authored = trace.authored.unwrap();
assert_eq!(authored.raw(), "+001.2500");
assert_eq!(authored.source(), document.source());
# Ok::<(), Box<dyn std::error::Error>>(())
```

`SourceTrace::representation` identifies the JSON buffer; `authored` identifies the original YAML definition. JSON traces share their existing source owner. Caller-constructed values have no invented authored source. Retained positions remain usable after the document/value is dropped. Source text and pointers are omitted from the positions' default Debug view.

For alias-expanded values, `alias_uses` records the outermost-to-innermost alias use sites. The authored position points to the definition. Pointers identify the semantic use site; the expanded descendant may not occur literally at that pointer in YAML. Value diagnostics point to the original definition and include alias use sites as related ranges in that same source owner. Syntax/profile/expansion failures carry original YAML coordinates and retained original bytes, never generated offsets against a YAML buffer.

## Admitted YAML profile

The dependency owns YAML syntax, quoting, Unicode escapes, multiline scalars, comments and event spans. The adapter applies these documented rules:

- UTF-8, one document, YAML 1.2. A BOM and a `%YAML 1.2` directive are accepted. Other explicit versions and multiple documents refuse.
- Core-schema resolution for plain scalars; quoted/block scalars are strings. Empty values and null spellings are null, distinct from absent members. YAML 1.1 `yes`, `no`, `on`, `off` and date-like scalars remain strings.
- Decimal integers/fractions/exponents retain arbitrary precision. Leading plus/zeros, leading/trailing decimal points, and hexadecimal/octal integers normalize exactly to JSON number syntax. Number conversion never passes through binary floating point. Original spelling remains in the source trace.
- Only standard JSON-compatible tags (`str`, `null`, `bool`, `int`, `float`, `seq`, `map`) are admitted, with matching scalar/container content. NaN, infinity, custom tags, YAML 1.1 merge tags and other non-JSON forms return a typed `YamlReason`. The ordinary Core-schema string key `<<` remains literal data; merge behavior is not applied.
- Mapping keys must resolve to strings. Numeric, boolean, null and composite keys refuse rather than changing identity. Quote status keys such as `'200'`. Duplicate decoded string keys refuse with both original key ranges. Aliases to string keys are accepted under the same identity rule.
- Acyclic aliases expand under the limits below. Cycles refuse explicitly; aliases are never dereferenced through a file or network loader.

The mapping/tag constraints follow [OpenAPI 3.1.2 §4.2](https://spec.openapis.org/oas/v3.1.2.html#format). Scalar resolution follows [YAML 1.2.2 §10.3.2](https://yaml.org/spec/1.2.2/#1032-tag-resolution); the Core and JSON schemas use the same standard tag set. The finite, string-keyed JSON representation is an intentional OpenAPI loading profile, not a claim to represent all YAML graphs or application-specific tags. Any provider incompatibility must be recorded as such, not silently repaired.

## Bounds and cancellation

Original bytes and normalized JSON output are independently capped at `limits.document_bytes`. An original byte refusal retains no oversized input. A normalized-byte refusal retains admitted original YAML and reports `LimitKind::NormalizedBytes`; increase `document_bytes` explicitly when both source and expanded representation need more room. String escaping writes through a bounded writer, so an escaped representation cannot be allocated in full before this check.

`limits.nodes` independently caps admitted YAML nodes (including mapping keys and aliases) and emitted JSON values. `limits.depth` caps parser/scanner collections and expanded JSON nesting. `limits.reference_steps` caps alias chains (`LimitKind::AliasSteps`). Nondecimal numeric conversion is additionally capped by `number_conversion_chars`; decimal/exponent retention is linear in its already admitted source length. Cancellation is checked before admission, at YAML event and expansion steps, and during exact JSON indexing. Dependency scanning of one token and exceptional depth-refusal disambiguation are synchronous work within the admitted source-byte bound.

The default remains 8 MiB per source/representation, 500,000 nodes and depth 128. Configure a larger document profile explicitly, for example `Limits { document_bytes: 32 * 1024 * 1024, ..Limits::default() }`. JSON/YAML owners can coexist during normalization. Retained-byte estimates include original YAML, generated JSON and source mappings; peak parser/normalizer memory and allocator overhead are separate from this estimate.

## Remaining graph work

Caller-supplied resources, retrieval aliases, external protocol references and OpenAPI 3.2 `$self` are supported by the [immutable graph layer](GRAPH.md), preserving original JSON/YAML owners. File/HTTP/custom acquisition drivers and dialect-aware schema `$id`/anchor graphs remain subsequent work. The core never authorizes retrieval or reuses invocation credentials to acquire descriptions. No extension or instance `$ref` field gains behavior from YAML loading.
