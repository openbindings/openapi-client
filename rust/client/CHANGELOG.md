# Unreleased (0.0.0)

- Extend the shared logical encoder with label/matrix path and space/pipe-delimited
  query and eligible form values. Keep structural delimiters separate from data,
  refuse ambiguous active delimiters at caller values, and preserve edition rules.
- Prepare single-media JSON/text content parameters through existing parameter
  inputs, preserving exact JSON tokens without evaluating payload schemas. Actual
  Media Type references use the existing corrective resource path. Query content
  uses the content form-encoding profile; path/header framing remains protected.
  See SERIALIZATION.md and the structured_parameters example.

- Reduce finite form planning memory for expanded scalar fields by reserving one
  piece initially while retaining bounded geometric growth for joined values.

- Locate unsupported deepObject caller shapes and ambiguous member names at the
  supplied value, preserving exact or generated input ownership. Refuse explicit
  `explode: false` for OpenAPI 3.2 schema-based cookie parameters using `form` or
  `cookie` style before preparation; retain older-edition behavior and valid
  exploded form cookies.

- Share private logical serialization atoms across parameters and forms. Add flat
  `deepObject`, component-safe reserved expansion, final-name credential collision
  checks, bounded parameter output planning and corrective style diagnostics.
- Canonicalize style spaces as `%20` while retaining `+` for content URL forms;
  preserve structural commas separately from data. Keep 3.0 multipart ignoring
  style fields and 3.2 outer-array item expansion. See SERIALIZATION.md.

- Add immutable caller-supplied resource graphs, typed protocol sites, external
  Path Item discovery and uniform corrective resource/layout needs. New snapshots
  share original source arenas and reject stale sites/needs. See GRAPH.md.
- Separate aggregate descriptor work from reference-chain depth. Preserve exact
  source chains, entry-root implicit names, 3.2 security URI precedence and `$self`
  identity independently from server retrieval bases. Acquisition/schema graph
  interpretation remain subsequent work.

- Add one logical `.form(...)` request path for finite named URL-encoded and
  multipart/form-data bodies, sharing existing preparation, credentials,
  cancellation and invocation behavior. Add `FormField`, `FormValue`, name-free
  `Part`, borrowed/shared `FiniteBytes`, and corrective `FormReason` diagnostics.
- Preserve exact JSON/numeric values, declared media choices and edition-specific
  encoding semantics; generate bounded collision-checked multipart framing.
- Move authoritative body field descriptors onto each `MediaDescription`. Add
  partial/opaque source-aware issues and distinguish protocol-required fields
  from schema-required hints. This is an unpublished breaking descriptor change.
- Add independent finite form field/part/metadata/work/boundary limits. Keep raw
  input and the existing JSON API; richer styles, codings, schema-reference facts,
  structured multipart and streams remain explicit open work. See FORMS.md.
