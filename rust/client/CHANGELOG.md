# Unreleased (0.0.0)

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
