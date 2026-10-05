# Sharing retained resource URI storage

The loader keeps canonical resource identifiers in `document.ids`, and parsed
resource bases in `tree.resourceBases`. A long hierarchical identifier can keep
its path twice: in the identifier key and in `url.URL.Path` (or `RawPath`).

Once discovery, retrieval workers and scope settlement have finished, the loader
borrows matching path text from its retained identifier keys. It changes a string's
backing storage only when its bytes match exactly. All URL field values, pointer
identities, resolution rules and diagnostics stay the same. The client is published
after this step; subsequent readers continue to see immutable URLs.

Using the final identifier keys matters: temporary or rejected identity strings
must not acquire a new owner. This pass creates no index or string cache. It also
handles the second claim of an ambiguous identifier, preserving the ambiguity.
Cancellation is checked while walking the identifier map.

Keys shorter than 256 bytes skip resource lookup and compaction. The cutoff
avoids compaction work when duplicated strings are small. It governs storage
only and changes no interpretation of an identifier.

This reduces post-GC retention. It does not remove transient resolution
allocations, canonical identifier keys, or public descriptor `Source` strings.
Deep identifiers can still have quadratic total text length.

`BenchmarkResourceURIParse` measures load-time cost alongside the deep-resource
first-lookup gate. Storage tests verify sharing and exact URL-field equality
for plain and escaped paths. Production code uses ordinary string slicing and
the existing `net/url` implementation.
