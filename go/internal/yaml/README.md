# Internal YAML parser

This directory contains an internal copy of `go.yaml.in/yaml/v3 v3.0.5`.
It preserves the upstream package and all 13 non-test Go source files,
LICENSE, NOTICE, README (as README.upstream.md), and module metadata
(as UPSTREAM.go.mod). Upstream test files are omitted; the client tests
exercise the independently specified Loader contract.

The approved source is the 107,640-byte module archive at
https://proxy.golang.org/go.yaml.in/yaml/v3/@v/v3.0.5.zip.

- Archive SHA-256: `f2d70caa35f66283aec58b889af8c5f07c374c5934be27a738c1215263e19205`
- Module checksum: `h1:N6y/pJk8buWs9NY5ERU2HSMfm+IuD/OtfdAnq6kESPw=`
- Upstream go.mod checksum: `h1:HVTZu1O7/Vkt2N+BFy8Zza+lnLsABggaTM2ZpNIGuKg=`

`upstream.sha256` records the unmodified SHA-256 of every copied file,
using its name in this directory. `openapi.patch` records the complete
local source difference, including the added position accessor. Reversing
that patch reconstructs the source baseline verified by the manifest.
The original 13 Go files total 11,346 physical lines and 331,678 bytes;
this third-party footprint is recorded separately from client-owned code.

## Local changes

1. YAML 1.2.2 section 5.4 makes NEL (U+0085), LS (U+2028), and PS
   (U+2029) scalar content, not line breaks. Remove their branches from
   is_break, is_breakz, is_spacez, is_blankz, and read_line. CR, LF, and
   CRLF retain their upstream behavior. Both the scalar bytes and source
   marks now follow YAML 1.2 without rewriting the source text.
2. Preserve an explicitly written non-specific `!` tag in Node.Tag and
   Node.Style, using the same representation as other explicit tags.
   This allows the client's Core resolver to recognize scalar strings
   directly, including anchored scalars, without rescanning source text.
3. Add Decoder.ErrorPosition in position.go. It reads the existing
   reader byte offset or scanner/parser/alias mark without changing
   parser state. The client uses these typed positions to report the
   original encoding's byte column instead of reflecting private fields.

The internal import is intentional: downstream users receive these same
sources with the OpenAPI module. A dependency's conventional vendor
folder would be ignored by downstream modules, so vendor-only patching
would not deliver the Loader's behavior to users. No external YAML module
requirement or replace directive is needed.

The client uses only the Node decoding API; Core scalar resolution and
bounded alias expansion remain client code. Updates must retain the
provenance and patch, review any upstream changes, and run the independent
YAML scalar, directive, alias, encoding, cancellation, and exact-position
regressions, followed by the normal stage gates. Do not substitute a new
upstream version implicitly.
