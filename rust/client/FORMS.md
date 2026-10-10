# Finite named form profile

`RequestBuilder::form` accepts unique logical `FormField`s. Values use the existing
ordinary/exact `Argument` model, a name-free `Part`, or a borrowed slice of Parts.
No network or filesystem work occurs in these constructors. Prepared wire bytes
enter the same media/security/header checks and invocation lifecycle as JSON/raw
bodies. Missing credentials or unsupported security still refuse before dispatch;
no retry or replay is introduced. `Body::Raw` remains the explicit finite escape
hatch with existing media election and final header checks.

## Edition interpretation

The client applies the latest published clarification in each supported minor
family: Swagger 2.0, OAS 3.0.4, 3.1.2 and 3.2.1. Authored patch spelling is retained;
there is no document retagging. OAS [versioning](https://spec.openapis.org/oas/v3.1.2.html#versions)
says tooling should not distinguish patch versions. This policy resolves the
internally inconsistent default-content-type prose in 3.1.0 using 3.1.2.

- Swagger 2.0 uses `formData`, `consumes`, primitive/file types and array
  `collectionFormat` (`csv`, `ssv`, `tsv`, `pipes`, `multi`). Protocol-required
  fields are enforced. File resources require multipart for this lossless profile;
  arrays of `type: file` are not fabricated as valid Swagger Items Objects.
- OAS 3.x keeps each media schema/encoding independently. With no applicable
  explicit style fields, content-based encoding uses declared/elected media or
  the edition's schema-driven defaults. `form` style supports scalar values,
  flat objects and scalar arrays. OAS 3.0 ignores multipart style/explode/
  allowReserved. OAS 3.1 applies multipart style without URI escaping.
- OAS 3.2 expands named array properties before applying encoding to each item,
  for both named form media. This differs from 3.1 `explode: false` on an array.
  Arrays of Parts with joining styles in pre-3.2 editions currently refuse;
  ordinary/exact scalar arrays support the declared joining styles.
- Text is UTF-8. URL forms use `+` for spaces and percent encoding for literal
  plus, percent and separator characters. Multipart retains Unicode text,
  quotes/backslashes filenames safely, and rejects control characters in names
  and filenames. Filenames are never paths to open.
- Primitive default parts are text; object parts are JSON; binary/default octet
  parts accept finite bytes. OAS 3.0 binary/byte format and OAS 3.1+ absent type
  affect default media; OAS 3.1+ format strings do not transform payloads. JSON
  parts preserve the exact supplied token spelling, including numeric exponents.

Sources: [Swagger parameters](https://spec.openapis.org/oas/v2.0.html#parameter-object),
[3.0 encoding](https://spec.openapis.org/oas/v3.0.4.html#encoding-object),
[3.1 encoding](https://spec.openapis.org/oas/v3.1.2.html#encoding-object),
[3.2 encoding by name](https://spec.openapis.org/oas/v3.2.1.html#encoding-by-name),
[form percent encoding](https://url.spec.whatwg.org/#concept-urlencoded-serializer).

Schema evaluation is separate. The client reads only facts needed for an
unambiguous wire representation and never applies defaults, selects schema
branches, coerces values or projects objects. The original schema/encoding Values
remain available. An inline field with explicit JSON media can retain an opaque
field schema while copying its exact JSON value. This does not resolve schema
references: a media-level schema reference still prevents field discovery, and
field references cannot supply default media or hidden coding facts. Missing
required facts refuse rather than being inferred from supplied data.
OAS schema `required` remains a hint; Swagger
`required` is a protocol requirement.

## Header and boundary policy

Multipart/form-data follows [RFC7578 §4.8](https://www.rfc-editor.org/rfc/rfc7578#section-4.8).
The library generates Content-Disposition and Content-Type. Omitted optional
custom declarations remain inspectable and do not block a request. Required or
supplied unsupported headers refuse precisely. Every nonempty `Part::headers`
assignment currently refuses for the supported named-form profile. Supplied framing headers refuse;
there is no silent discard. References whose unresolved target controls required
header behavior remain an explicit reference dependency. Content-transfer coding
support and an explicit interoperability policy remain open capabilities.

The library rejects caller-fixed boundaries, tries at most `boundary_attempts`
candidates of at most 70 bytes, and scans encoded headers and payloads for each
candidate before final allocation. This includes decoded text and exact JSON
material. Boundary spelling is private and may change. Count, collision scan and
write use the same bounded byte traversal. The body limit includes all delimiters,
headers, CRLF and percent expansion, not merely payload bytes.

## Resource ownership and refusal order

Defaults are 128 logical fields, 1024 emitted values/parts, 65536 metadata bytes,
500000 work units and 16 boundary attempts. Existing `headers` independently caps
aggregate part-header lines; `body_bytes` caps the complete encoded output.
Configure these through document `Limits`; there is no order-dependent per-call
limits setter in this tranche.

Metadata has two independent phase ceilings. Admission charges field names once,
then filename/elected media/supplied header names for every supplied Part. Header
Argument data uses ordinary/exact data admission rather than eager stringification.
Planning charges each emitted name, filename and effective media, including
repetition; generated fixed header syntax and delimiters use final body bytes.
These ceilings do not claim to be a total-process memory cap.

Work is independently capped during admission and preparation. Admission charges
fields, supplied Parts/headers and a conservative upper bound of prior-name
comparisons. Planning charges declaration comparisons, property/item/header/media
visits, stored joined pieces, boundary attempts and at-most-4096-byte chunks of
count/scan/write passes. Temporary media-list parsing is metadata-size bounded
before parser allocation. Joined key copies have aggregate body-byte admission.
Caller iterator, Into and arbitrary Serialize execution are outside these caps.

Field-count/name admission precede duplicate matching; value/Part admission follows.
The first refusal stops iteration, drops drafts and ignores later setter work.
Preparation observes cancellation before ordinary serialization and at encoding
checkpoints. Field errors carry bounded index/name/item context; source ranges
belong to their exact original owner (including normalized YAML traces).

Borrowed/shared file owners are retained until preparation. Ordinary values use
the existing bounded generated exact owner; exact inputs retain existing owners.
The plan retains Value handles and metadata, not fully encoded per-part buffers.
Final output is counted before allocation. Conversion from Vec to immutable Arc
can temporarily retain two wire-sized allocations; source owners, generated input,
plan structures, allocator overhead and RSS are separate measured resource domains.

## Remaining capabilities

Schema/external-reference resolution needed for default codecs, advanced styles
(including allowReserved URL behavior), content encodings such as base64,
non-UTF-8 charsets, nested/positional multipart and streaming are explicit open
capabilities. A declaration/refusal is not positive support for them. General
multipart types need their own header/framing rules. The name-free Part/value
model leaves room to compose those representations without requiring callers to
build wire bodies or boundaries. Finite raw input remains available now.
