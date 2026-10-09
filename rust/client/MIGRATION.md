# API quality revision (unpublished)

This pre-stabilization revision preserves the JSON/OpenAPI 3.1 finite supplied-transport profile. It does not add HTTP acquisition, a production transport, schema evaluation or an OpenBindings dependency.

Diagnostic fields are now read-only. Replace `.code`, `.location`, `.related` and `.setting` reads with `code()`, `location()`, `related()` and `setting()`. Location access borrows: clone it explicitly when an independent value is needed. Keep `reason()`, `context()` and `source_context()`. Add application context with a wrapper implementing `std::error::Error` rather than rewriting a diagnostic's source range.

ParseFailure's source and diagnostic are also an immutable pair. Use `diagnostic()` and `source_bytes()`. `std::error::Error::source` still exposes the Diagnostic. Over-byte-limit admission still retains no oversized source. Construction of a category-only Diagnostic remains available through `Diagnostic::new(code)`.

An operation's `parameters()` supplies effective parameter declarations independently of request-body, media and security compilation. Returned values retain authored/reference/target sources after the original Document or Operation is dropped. Use raw `inspect()` or `value()` when structured interpretation refuses. `schema() == None` does not mean a parameter has no content declaration; inspect its resolved raw declaration.
