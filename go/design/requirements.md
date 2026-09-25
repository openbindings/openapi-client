# OpenAPI client requirements from OAS and its RFCs

Derived only from the authorities themselves: OAS 2.0, 3.0.0 to 3.0.4, 3.1.0 to 3.1.2, 3.2.0, and the RFCs, YAML, WHATWG and JSON Schema texts they rely on.

Section 2's defaults were the starting point for the API design, and review refined several of them (the security alternative, the request media type, redirects, and others). Where this report and the package documentation differ, the package documentation is the decision.

## 0. How to read this

**Edition labels.** `2.0` is Swagger 2.0. `3.0` means 3.0.0 to 3.0.4, read per 3.0.4 unless a row says otherwise. `3.1` means 3.1.0 to 3.1.2, read per 3.1.2. `3.2` is 3.2.0. `3.x` is all three 3.x lines.

**Patches.** OAS says patch releases "address errors in, or provide clarifications to, this document, not the feature set", and that "the patch version SHOULD NOT be considered by tooling" (3.0 §4.1, 3.1 §4.1, 3.2 §2.1). This report therefore reads each line by its latest patch. Appendix A lists every place where an earlier patch says something materially different.

**Citations.** Section numbers are those of the cached renderings: 2.0 uses `§6.4.x` for objects, 3.0 uses 3.0.4's `§4.7.x`, 3.1 uses 3.1.2's `§4.8.x`, 3.2 uses `§4.x`. "App" is an appendix. RFC citations name the RFC section.

**Level column.** The keyword exactly as the source uses it (MUST, MUST NOT, SHALL, SHALL NOT, SHOULD, SHOULD NOT, RECOMMENDED, MAY). `def` means a definitional statement with no keyword (a field's meaning, a default value). `undef` and `impl-def` mean the source labels the behaviour undefined or implementation-defined.

**Client decision.** Anything labelled "Client decision" or appearing as a recommended default in section 2 is my recommendation, not a spec statement. Evidence about what other clients do comes from `prior-art.md` in this directory (its section numbers are given) and from Go's `net/http` documented behaviour.

---

## 1. What OAS and the RFCs require of a client

### 1.1 Documents and parsing

| # | Requirement | Editions | Level | Source |
|---|---|---|---|---|
| D1 | A description is a JSON object, written in JSON or YAML. API bodies need not be JSON or YAML. | all | def | 2.0 §6.1; 3.0/3.1 §4.2; 3.2 §3 |
| D2 | Field names are case-sensitive unless marked otherwise. Names and values that map to HTTP concepts follow HTTP's case rules. | all (HTTP rule 3.x) | def | 2.0 §6.1; 3.0/3.1 §3.8; 3.2 §3, §3.2 |
| D3 | Patterned fields MUST have unique names within their object. | all | MUST | 3.0/3.1 §4.2; 3.2 §3; 2.0 §6.1 ("as long as each has a unique name") |
| D4 | YAML: 2.0 says YAML "can be used as well". 3.0/3.1: YAML 1.2 RECOMMENDED; tags MUST be limited to YAML's JSON schema ruleset; map keys MUST be Failsafe scalar strings. 3.2: YAML 1.2 RECOMMENDED with RFC 9512 §3.4 constraints; authors SHOULD NOT rely on JSON-incompatible YAML values. | per edition | RECOMMENDED, MUST, SHOULD NOT | 2.0 §6.1; 3.0/3.1 §4.2; 3.2 §3.1 |
| D5 | Response status-code keys MUST be quoted (for example `"200"`) for JSON/YAML compatibility. | 3.x | MUST | 3.0 §4.7.16; 3.1 §4.8.16; 3.2 §4.16.2 |
| D6 | Version: `swagger` MUST be `"2.0"`. `openapi` is REQUIRED, MUST be the OAS version number, SHOULD be used by tooling to interpret the document. Tooling for 3.N SHOULD be compatible with all 3.N.* and SHOULD NOT consider the patch. | all | MUST, SHOULD, SHOULD NOT | 2.0 §6.4.1; 3.0 §4.1, §4.7.1; 3.1 §4.1, §4.8.1; 3.2 §2.1, §4.1.1 |
| D7 | Required roots: 2.0 `swagger`, `info`, `paths`. 3.0 `openapi`, `info`, `paths`. 3.1/3.2 `openapi`, `info`, and at least one of `paths`, `components`, `webhooks`. Paths and Path Items MAY be empty (ACL filtering). | all | REQUIRED, MUST, MAY | 2.0 §6.4.1, §6.4.5, §6.4.6; 3.0 §4.7.1, §4.9; 3.1 §3.1, §4.8.1, §4.10; 3.2 §4.1.1, §6.4 |
| D8 | The JSON Schema published for each edition is informational; if it differs from the text, the text MUST be considered authoritative. | 3.x | MUST | 3.0 §4.7; 3.1 §4.8; 3.2 §4 |
| D9 | Each document MUST be fully parsed to find reference targets (`$self`, `$id`, `$anchor`, `$dynamicAnchor`). A reference MUST NOT be treated as unresolvable before all documents supplied to the implementation are completely parsed (3.2); 3.1 states the same for Schema Object references. Parsing referenced fragments in isolation is undefined. | 3.1, 3.2 | MUST, MUST NOT, undef | 3.1 §4.3.1; 3.2 §4.1.2.1, App G.1 |
| D10 | Every document in a 3.2 description MUST have an OpenAPI Object or a Schema Object at its root. Other roots MAY be supported, with implementation-defined behaviour. | 3.2 | MUST, MAY | 3.2 §4.1.2, App G |
| D11 | If one JSON/YAML object must be read as two different Object types (via different references), behaviour is implementation-defined and MAY be an error. | 3.0.4, 3.1.1+, 3.2 | MAY, impl-def | 3.0 §4.3.1; 3.1 §4.3.2; 3.2 App G.2 |
| D12 | JSON exchanged between systems MUST be UTF-8; senders MUST NOT add a BOM; parsers MAY ignore a BOM. Object names SHOULD be unique, and behaviour with duplicates is unpredictable. Objects are unordered. Parsers may limit text size, nesting depth, number range and precision, and string length. | all | MUST, MUST NOT, MAY, SHOULD | RFC 8259 §4, §6, §8.1, §9 |
| D13 | YAML: a mapping's keys are unique, and a non-unique key is a loading failure. Key order is a serialization detail that should not be used when composing data. Processors must accept UTF-8 and UTF-16, and UTF-32 for JSON compatibility. A 1.2 processor processes `%YAML 1.1` documents as 1.2 with a warning. The Core schema is the recommended default; under the JSON schema an unmatched plain scalar is an error. | all | must, should | YAML 1.2.2 §3.2.1.1, §3.2.2.1, §3.3, §5.2, §6.8.1, §10.2.2, §10.3 |
| D14 | YAML/JSON interop hazards: multi-document streams, non-UTF-8, non-string keys, cyclic anchors, `.inf`/`.nan`, non-JSON tags. Alias expansion can exhaust resources (billion laughs) and must be bounded. In YAML 1.2 only `true|True|TRUE|false|False|FALSE` are booleans. | all | advice | RFC 9512 §3.4, §4.2, §4.4 |
| D15 | Security considerations: descriptions carry JSON, YAML and JSON Schema risks; referenced external resources "may be hosted on different domains that may be untrusted"; reference cycles: "tooling must detect and handle cycles to prevent resource exhaustion". | 3.0.4, 3.1.1+, 3.2 | must (lowercase) | 3.0 §5.1, §5.4, §5.5; 3.1 §5.1, §5.4, §5.5; 3.2 §6.1, §6.5, §6.6 |
| D16 | Specification extensions (`x-` fields) MAY appear on most objects; Schema Objects in 3.1/3.2 MAY carry arbitrary unprefixed keywords. | all | MAY | 2.0 patterned `^x-`; 3.2 §5, §4.24.2 |

### 1.2 References and base URIs

| # | Requirement | Editions | Level | Source |
|---|---|---|---|---|
| R1 | 2.0 references are JSON References whose value is a URI; the fragment SHOULD be a JSON Pointer; relative URIs resolve per RFC 3986 §5.2 "relative to the referring document"; members other than `$ref` SHALL be ignored; only canonical dereferencing is supported (schema `id` does not change the base). | 2.0 | SHOULD, SHALL, MUST | 2.0 §6.2, §6.4.17; JSON Reference draft-03 §3, §4; JSON Schema draft-04 core §7.2.3 |
| R2 | 3.0 Reference Objects follow JSON Reference, "not ... the JSON Schema specification". `$ref` resolves against the URL of the current document. Other properties SHALL be ignored (so Schema `$ref` siblings have no effect). | 3.0 | SHALL | 3.0 §4.6, §4.7.23 |
| R3 | 3.1: relative references inside Schema Objects use the nearest parent `$id` (JSON Schema 2020-12). Other relative URIs MUST resolve against the referring document's base (RFC 3986 §5.1.2 to §5.1.4), usually the retrieval URI, which MAY be a user-supplied expected location. Reference Object `summary`/`description` SHOULD override the target's; other properties SHALL be ignored. Schema `$ref` siblings are evaluated (2020-12). | 3.1 | MUST, MAY, SHOULD, SHALL | 3.1 §4.6, §4.8.23, §4.8.24 |
| R4 | 3.2: `$self` is the document's own URI and its base URI (RFC 3986 §5.1.1); a relative `$self` resolves against the next base source. References MUST use the target's `$self` when present (support for the retrieval URI is MAY and not interoperable). Schemas under an `$id` MUST be referenced via the nearest `$id`. Retrieval is MAY; implementations SHOULD let users supply documents with their intended retrieval URIs. An in-memory document may use an application default base such as a `urn:uuid:`. | 3.2 | MUST, MAY, SHOULD | 3.2 §4.1.1, §4.1.2.2.1, App F.1 to F.5 |
| R5 | A fragment in a JSON/YAML reference SHOULD be interpreted as a JSON Pointer. 3.1/3.2 Schema references may also use `$anchor` names (2020-12). | all | SHOULD | 3.1 §4.6; 3.2 §4.1.2.2.2; JSON Schema 2020-12 core §8.2 |
| R6 | JSON Pointer: percent-decode the URI fragment first, then unescape `~1` to `/` and then `~0` to `~`. | all | def | RFC 6901 §4, §6 |
| R7 | Path Item `$ref`: MUST be a URI to a Path Item; a field present both locally and in the target has undefined behaviour. 3.1/3.2 note this is likely to align with Reference Object behaviour later. | all | MUST, undef | 2.0 §6.4.6; 3.0 §4.7.9; 3.1 §4.8.9; 3.2 §4.9.1 |
| R8 | Implicit (name-based) connections in multi-document descriptions (Security Requirement names, discriminator names, tags, Link `operationId`) are implementation-defined. RECOMMENDED: resolve component and tag names from the entry document; consider Operation Objects from all parsed documents for `operationId`. | 3.0.4, 3.1.1+, 3.2 | RECOMMENDED, impl-def | 3.0 §4.3.2, App F; 3.1 §4.3.3, App F; 3.2 §4.1.2.3, App G.3 |
| R9 | 3.2 Security Requirement keys may be component names or URIs of Security Scheme Objects; a key identical to a component name MUST be treated as a component name (a known hijack vector). | 3.2 | MUST | 3.2 §4.30, §6.3 |
| R10 | Dialects: `$schema` in a schema resource root MUST be used if present; otherwise the document's `jsonSchemaDialect`, otherwise the OAS dialect, which tooling MUST support. | 3.1, 3.2 | MUST | 3.1 §4.8.24.5; 3.2 §4.24.7 |
| R11 | JSON Schema implementations SHOULD understand ahead of time which schemas they will use; identifying a schema by URI "does not necessarily mean anything is downloaded". | 3.1, 3.2 | SHOULD | JSON Schema 2020-12 core §9.1.2 |

### 1.3 Operations and how they are addressed

| # | Requirement | Editions | Level | Source |
|---|---|---|---|---|
| O1 | Operation slots on a Path Item: 2.0 `get put post delete options head patch`; 3.0/3.1 add `trace`; 3.2 adds `query` and `additionalOperations`, whose key "is the HTTP method with the same capitalization that is to be sent" and MUST NOT duplicate a fixed-field method. | all | def, MUST NOT | 2.0 §6.4.6; 3.0 §4.7.9; 3.1 §4.8.9; 3.2 §4.9.1 |
| O2 | Paths keys MUST begin with `/`. The path is appended to the server URL (3.x) or to `basePath` (2.0) with no relative resolution. Templated paths that differ only in variable names MUST NOT exist. 3.2: a template expression MUST NOT appear more than once in a path. | all | MUST, MUST NOT | 2.0 §6.4.5; 3.0 §4.7.8; 3.1 §4.8.8; 3.2 §4.8.1, §4.8.2 |
| O3 | Each template expression MUST correspond to a path parameter (exception for empty Path Items in 3.1+). A path parameter's `name` MUST correspond to a template expression. | all | MUST | 3.0 §3.5, §4.7.12; 3.1 §3.5, §4.8.12; 3.2 §4.8.2, §4.12.2.1; 2.0 §6.4.9 |
| O4 | `operationId` is optional; if present it MUST be unique among all operations; tools MAY use it to identify operations. 3.2 states it is case-sensitive. | all | MUST, MAY | 2.0 §6.4.7; 3.0 §4.7.10; 3.1 §4.8.10; 3.2 §4.10.1 |
| O5 | `responses` is REQUIRED on 2.0 and 3.0 operations, optional in 3.1/3.2. A Responses Object MUST contain at least one response code. | all | REQUIRED, MUST | 2.0 §6.4.7, §6.4.11; 3.0 §4.7.10, §4.7.16; 3.1 §4.8.16; 3.2 §4.16 |
| O6 | `deprecated: true` means consumers SHOULD refrain from use; the operation still exists. | all | SHOULD | 2.0 §6.4.7; 3.x Operation Object |
| O7 | HTTP method tokens are case-sensitive; standard methods are uppercase by convention. | all | def | RFC 9110 §9.1 |
| O8 | Callbacks and webhooks describe requests initiated by the API provider, not by the client. | 3.x | def | 3.0 §4.7.18; 3.1 §4.8.1 (`webhooks`), §4.8.18; 3.2 §4.1.1, §4.18 |
| O9 | Links are design-time relationships; "the presence of a link does not guarantee the caller's ability to successfully invoke it". | 3.x | def | 3.0 §4.7.20; 3.1 §4.8.20; 3.2 §4.20 |

### 1.4 Servers and the base URL

| # | Requirement | Editions | Level | Source |
|---|---|---|---|---|
| S1 | `servers` may appear on the root, a Path Item, or an Operation; a lower level overrides the higher. An absent or empty root `servers` means a single Server with `url: /`. | 3.x | def | 3.0 §4.7.1, §4.7.9, §4.7.10; 3.1 §4.8.1, §4.8.9, §4.8.10; 3.2 §4.1.1, §4.9.1, §4.10.1 |
| S2 | Server `url` is REQUIRED and MAY be relative "to the location where the document containing the Server Object is being served". Query and fragment MUST NOT be part of it (3.1.2, 3.2). In 3.2 `$self` is ignored for API URLs and the retrieval URI is used. 3.2 adds an optional unique `name`. | 3.x | REQUIRED, MAY, MUST NOT | 3.0 §4.7.5; 3.1 §4.8.5, §4.7; 3.2 §4.5.1, §4.5.2 |
| S3 | Server variables: `{name}` in the URL is substituted. `default` is REQUIRED and SHALL be sent if no alternate value is supplied. `enum` MUST NOT be empty and `default` MUST be in it (3.1, 3.2); 3.0 says SHOULD for both. 3.2: a variable MUST NOT appear more than once, and the template follows an ABNF (literals exclude `{`, `}`, and so on). | 3.x | REQUIRED, SHALL, MUST, SHOULD | 3.0 §4.7.6; 3.1 §4.8.6; 3.2 §4.6 |
| S4 | The full request URL is the server URL, resolved and with variables substituted, with the path appended (no relative resolution). | 3.x | def | 3.1 §4.8.8; 3.2 §4.8.1 |
| S5 | 2.0: `host` MUST be host only, MAY include a port; if absent, "the host serving the documentation is to be used (including the port)". `basePath` MUST start with `/`; if absent the API is served directly under the host. `schemes` values MUST be `http`, `https`, `ws`, `wss`; if absent, the scheme used to access the definition. Operation `schemes` override the root. | 2.0 | MUST, MAY, def | 2.0 §6.4.1, §6.4.7 |
| S6 | All API URLs MUST parse and percent-decode per RFC 3986. | 3.1.2, 3.2 | MUST | 3.1 §4.8.12.4; 3.2 §4.12.4 |
| S7 | A sender MUST NOT generate userinfo (`user@`) in an http(s) target URI. | all | MUST NOT | RFC 9110 §4.2.4 |
| S8 | Relative reference resolution and base URI precedence (embedded, encapsulating entity, retrieval URI, default). | all | def | RFC 3986 §5.1, §5.2 |

### 1.5 Parameters

| # | Requirement | Editions | Level | Source |
|---|---|---|---|---|
| P1 | A parameter is identified by `name` plus `in`. Operation parameters override same-identity Path Item parameters and cannot remove them. Lists MUST NOT contain duplicates. | all | MUST NOT | 2.0 §6.4.6, §6.4.7, §6.4.9; 3.0 §4.7.12; 3.1 §4.8.12; 3.2 §4.12 |
| P2 | Locations: 2.0 `path query header body formData`; 3.0/3.1 `path query header cookie`; 3.2 adds `querystring`. | all | def | 2.0 §6.4.9; 3.0 §4.7.12.1; 3.1 §4.8.12.1; 3.2 §4.12.1 |
| P3 | Path parameters: `required` MUST be `true`. 3.1/3.2: path parameter values MUST NOT contain unescaped `/`, `?`, `#`. | all; 3.1+ | MUST, MUST NOT | 2.0 §6.4.9; 3.1 §3.5; 3.2 §4.8.2 |
| P4 | Header parameters named `Accept`, `Content-Type` or `Authorization` SHALL be ignored. A header parameter named `Cookie` has undefined effect. 2.0 has no such rule. | 3.x | SHALL, undef | 3.0 §4.7.12.2.1; 3.1 §4.8.12.2.1; 3.2 §4.12.2.1 |
| P5 | A 3.x parameter MUST have `schema` or `content`, not both; `content` MUST have exactly one entry. | 3.x | MUST | 3.0 §4.7.12.2; 3.1 §4.8.12.2; 3.2 §4.12.2 |
| P6 | Defaults: `style` is `form` for query and cookie, `simple` for path and header. `explode` defaults to true for `form` (and 3.2 `cookie`), false otherwise. `allowReserved` defaults to false; it applies only to `query` in 3.0/3.1, and in 3.2 to any `in`/`style` that percent-encodes. | 3.x | def | 3.0 §4.7.12.2.2; 3.1 §4.8.12.2.2; 3.2 §4.12.2.2 |
| P7 | Style/location/type table: `matrix`, `label` (path); `simple` (path, header); `form` (query, cookie); `spaceDelimited`, `pipeDelimited` (query; array, object); `deepObject` (query; object); 3.2 `cookie` (cookie). 3.2: "Combinations not represented in this table are not permitted". Cells marked n/a are undefined. | 3.x | def, undef | 3.0 §4.7.12.3, §4.7.12.4; 3.1 §4.8.12.3, §4.8.12.6; 3.2 §4.12.3, §4.12.6 |
| P8 | Style serialization is defined by equivalence to RFC 6570: `simple` none, `matrix` `;`, `label` `.`, `form` `?` (the `?` is stripped for bodies and cookies), `allowReserved` `+`, `explode` `*`. Several form parameters equal one variable list (`{?a,b}`). Configurations without an RFC 6570 equivalent SHOULD be handled according to RFC 6570. Names that are not legal RFC 6570 variable names MUST be percent-encoded. Compound values beyond one level are implementation-defined. | 3.0.4, 3.1.1+, 3.2 | SHOULD, MUST, impl-def | 3.0/3.1/3.2 App C, C.1, C.3 |
| P9 | RFC 6570 "undefined" values (null, empty list, empty map, map with all values undefined) are skipped in expansion, and an expression whose variables are all undefined expands to the empty string. The empty string is defined, not undefined. OAS's style table has an `undefined` column (3.0.4, 3.1.1+, 3.2). | 3.x | def | RFC 6570 §2.3, §3.2.1; 3.2 §4.12.6 |
| P10 | Percent-encoding: all API URLs MUST parse per RFC 3986; form-urlencoded content, including `in: query` query strings, MUST also parse per RFC 1866 (3.1.2) or WHATWG URL (3.2), where unencoded `+` is a space. Reserved characters MUST NOT be encoded when used for their reserved purpose. The `[ ] | ` and space delimiters of `deepObject`, `pipeDelimited`, `spaceDelimited` MUST be percent-encoded. "The safest approach" is to encode everything outside RFC 3986 unreserved (and `~` for form-urlencoded). | 3.0.4, 3.1.1+, 3.2 | MUST, advice | 3.1 §4.8.12.4; 3.2 §4.12.4; 3.0 App E.5; 3.1/3.2 App E.6 |
| P11 | `allowReserved: true` uses RFC 6570 reserved expansion, but callers stay responsible for encoding reserved characters not allowed in the target (3.0/3.1 name `[ ] #` and the form-urlencoded specials). | 3.x | def | 3.0 §4.7.12.2.2; 3.1 §4.8.12.2.2; 3.2 §4.12.2.2 |
| P12 | Header values from `schema`: URI percent-encoding MUST NOT be applied; implementations MUST pass values through unchanged and MUST NOT auto-quote. 3.2 applies the same to `style: cookie`. (3.0 and 3.1.0/3.1.1 define headers by RFC 6570 `simple`, which percent-encodes; see X5.) | 3.1.2, 3.2 | MUST, MUST NOT | 3.1 §4.8.12.2.2, §4.8.21.1.2, App D; 3.2 §4.12.2.2, §4.21.1.2, App D |
| P13 | Cookies: 3.2 `style: cookie` joins pairs with `; ` and applies no percent-encoding; data needing escaping MUST be provided escaped. `style: form` for cookies is RFC 6570 form minus the `?`, and is "always incorrect" for multiple values (wrong delimiter). 3.0.4: whether the `?` is included is implementation-defined. | 3.x | MUST, impl-def | 3.0 App D; 3.1 App D.1; 3.2 §4.12.3, App D.1 |
| P14 | Cookie wire syntax: `cookie-string = cookie-pair *( ";" SP cookie-pair )`; cookie-value octets exclude space, `"`, `,`, `;`, `\` and controls; a user agent MUST NOT attach more than one `Cookie` header. | all | MUST NOT | RFC 6265 §4.1.1, §4.2.1, §5.4 |
| P15 | Header fields: names case-insensitive; values carry no leading/trailing whitespace; CR, LF, NUL are invalid and dangerous; senders MUST NOT generate repeated field lines unless the field is list-based. | all | MUST NOT | RFC 9110 §5.1, §5.3, §5.5 |
| P16 | `allowEmptyValue` (3.x query only; 2.0 query and formData): when true, clients MAY send a zero-length value; SHALL be ignored where the style cell is n/a; interaction with the schema is implementation-defined; NOT RECOMMENDED and deprecated (3.0.3+, 3.2). | all | MAY, SHALL, impl-def | 2.0 §6.4.9; 3.0 §4.7.12.2.1; 3.1 §4.8.12.2.1; 3.2 §4.12.2.1 |
| P17 | `content` parameters: serialize with the media type, then percent-encode for URL locations when the media type does not already incorporate URI encoding. 3.2 examples use compact JSON (`coordinates=%7B%22lat%22%3A10...`). | 3.x | def | 3.2 §4.12.4, §4.12.8; 3.0/3.1/3.2 App E |
| P18 | `in: querystring`: the whole query string; MUST use `content`; `name` is not used; MUST NOT appear more than once; MUST NOT coexist with `in: query`. `application/x-www-form-urlencoded` is serialized with Encoding Objects as for bodies and needs no further escaping; other media are percent-encoded. | 3.2 | MUST, MUST NOT | 3.2 §4.12.1, §4.12.2.3, §4.12.8 |
| P19 | 2.0 non-body parameters: `type` is one of `string number integer boolean array file`; arrays need `items`; `collectionFormat` is `csv` (default), `ssv`, `tsv`, `pipes`, or `multi` (repeated name, query and formData only); Items may nest arrays with their own format. `file` only in `formData`, with `consumes` including multipart or urlencoded. | 2.0 | MUST, def | 2.0 §6.4.9, §6.4.10 |
| P20 | `default` values describe the receiver's behaviour, not data to insert: 2.0 parameter `default` is "the value ... the server will use if none is provided"; 3.x Server Variable text contrasts its own `default` with the Schema Object's, "which documents the receiver's behavior rather than inserting the value into the data". | all | def | 2.0 §6.4.9; 3.0 §4.7.6; 3.1 §4.8.6; 3.2 §4.6.1 |
| P21 | There is "no general-purpose specification for converting" non-string values to strings; conversions are implementation-defined; 3.2's boolean example serializes `flag=true`. | 3.0.4, 3.1.1+, 3.2 | impl-def | 3.x App B; 3.2 §4.19.3.3 |

### 1.6 Request bodies and media types

| # | Requirement | Editions | Level | Source |
|---|---|---|---|---|
| B1 | 2.0: at most one `in: body` parameter; `body` and `formData` cannot coexist; the body parameter's `name` has no effect. Media types come from effective `consumes` (operation replaces root; an empty list clears it). | 2.0 | def, MAY | 2.0 §6.4.1, §6.4.7, §6.4.9 |
| B2 | 3.x `requestBody.content` is REQUIRED; keys are media types or media ranges; "only the most specific key is applicable" (`text/plain` overrides `text/*`); `required` defaults to false; the map SHOULD have at least one entry (3.1.2, 3.2). | 3.x | REQUIRED, SHOULD | 3.0 §4.7.13; 3.1 §4.8.13; 3.2 §4.13.1 |
| B3 | Body by method. 3.0: `requestBody` "is only supported" where RFC 7231 defines body semantics; elsewhere (3.0.4 names GET, HEAD, DELETE) it SHALL be ignored by consumers. 3.1: permitted but without well-defined semantics, SHOULD be avoided. 3.2: same, citing RFC 9110 §9.3. 2.0: no rule. | per edition | SHALL, SHOULD | 3.0 §4.7.10; 3.1 §4.8.10; 3.2 §4.10.1 |
| B4 | HTTP: a client SHOULD NOT generate content in GET, HEAD or DELETE unless the origin server has indicated, in or out of band, that it is supported; an OPTIONS request with content MUST carry `Content-Type`; a client MUST NOT send content in TRACE. A sender of content SHOULD send `Content-Type`. | all | SHOULD NOT, MUST | RFC 9110 §8.3, §9.3.1, §9.3.2, §9.3.5, §9.3.7, §9.3.8 |
| B5 | Media types: type and subtype are case-insensitive; parameter names case-insensitive; parameter values may or may not be; `charset` values are case-insensitive. `+json` marks JSON structure. | all | def | RFC 9110 §8.3.1, §8.3.2; RFC 2046 §4.1.2; RFC 6838 §4.2; RFC 6839 §3.1 |
| B6 | Encoding Objects apply only to `multipart/*` and `application/x-www-form-urlencoded` (3.0/3.1: and only to request bodies; 3.2: any Media Type Object). 3.0/3.1: encoding keys MUST be schema properties; 3.2: keys without a property SHALL be ignored. | 3.x | SHALL, MUST | 3.0 §4.7.14; 3.1 §4.8.14; 3.2 §4.14.1, §4.14.5.1 |
| B7 | Default part/field `contentType`. 3.0.4: string with `format: binary` or `byte` is `application/octet-stream`; other strings, numbers, integers, booleans `text/plain`; object `application/json`; array from its `items`. 3.1.2: no `type` is octet-stream; string with `contentEncoding` octet-stream; string `text/plain`; number/integer/boolean `text/plain`; object JSON; array from `items`. 3.2: as 3.1.2, but array properties are encoded one item at a time and the `array` row (JSON) applies only to arrays nested inside an array. | 3.x | def | 3.0 §4.7.15.1.1; 3.1 §4.8.15.1.1; 3.2 §4.15.1.1 |
| B8 | `contentType` may be a comma list and may contain wildcards. For serialization, implementations MUST give applications a way to say which type is intended; sniffing MAY be offered but MUST NOT be the default. | 3.x; rule 3.2 | MUST, MAY, MUST NOT | 3.2 §4.15.4.1 |
| B9 | If `style`, `explode` or `allowReserved` is present, the property uses RFC 6570 serialization and `contentType` SHALL be ignored; if all three are absent, the property is content-encoded by `contentType`. 3.0 applies these only to urlencoded (3.0.4 RECOMMENDS this reading); 3.1/3.2 also allow `multipart/form-data`, where percent-encoding MUST NOT be applied. | 3.x | SHALL, RECOMMENDED, MUST NOT | 3.0 §4.7.15.1.2; 3.1 §4.8.15.1.2; 3.2 §4.15.1.2 |
| B10 | Array properties produce one field/part per item with the same name; name-value order is implementation-defined. Multiple files for one field MUST be sent as separate parts with the same name. | 3.x; RFC | MUST, impl-def | 3.2 §4.14.5.1; 3.0/3.1 Encoding; RFC 7578 §4.3 |
| B11 | Form-urlencoded: 2.0 cites HTML 4.01 §17.13.4 (space as `+`, non-alphanumerics `%HH`, line breaks `%0D%0A`). 3.0/3.1: body MUST be encoded per RFC 1866. 3.2: MUST be percent-encoded per WHATWG URL (space as `+`; `*-._` and alphanumerics literal; `~` encoded). | per edition | MUST | 2.0 §6.4.9; HTML 4.01 §17.13.4; 3.0 §4.7.15.2; 3.1 §4.8.15.2; 3.2 §4.15.3; WHATWG URL §5 |
| B12 | Multipart: a schema is REQUIRED for multipart (3.0/3.1). `multipart/form-data` parts MUST carry `Content-Disposition: form-data; name=...`; a `filename` SHOULD be supplied for file content; `filename*` MUST NOT be used; part `Content-Type` defaults to `text/plain`, and file data SHOULD be labelled with its type or `application/octet-stream`; senders SHOULD NOT generate `Content-Transfer-Encoding`; other part headers MUST NOT be included. The boundary MUST NOT appear in any part (1 to 70 characters); line breaks are CRLF. Non-ASCII field names SHOULD be avoided; if unavoidable, UTF-8. | 3.x; RFC | REQUIRED, MUST, SHOULD, MUST NOT | 3.0 §4.7.15.3; 3.1 §4.8.15.3; RFC 7578 §4.1 to §4.8, §5.1.1; RFC 2046 §5.1.1; RFC 9110 §8.3.3 |
| B13 | Encoding `headers` apply only to multipart; a `Content-Type` entry there SHALL be ignored. | 3.x | SHALL | 3.0 §4.7.15.1.1; 3.1 §4.8.15.1.1; 3.2 §4.15.1.1 |
| B14 | `format: byte` (3.0) or `contentEncoding` (3.1, 3.2) on a multipart field is equivalent to requiring a matching `Content-Transfer-Encoding` part header; a conflict is undefined. | 3.x | undef | 3.0 §4.7.15.3; 3.1 §4.8.15.3; 3.2 §4.15.4.2 |
| B15 | 3.2 positional multipart: `prefixEncoding`/`itemEncoding` need `itemSchema` or an array `schema`; positional `multipart/form-data` MUST supply `Content-Disposition` via Encoding `headers`; nested Encoding: one level MUST be supported, more MAY. | 3.2 | MUST, MAY | 3.2 §4.14.5.2, §4.14.5.3, §4.15.2 |
| B16 | Binary data. 2.0: `type: file` (formData, responses), `format: binary` raw, `format: byte` base64. 3.0: `type: string` with `format: binary` (raw) or `byte` (base64). 3.1/3.2: raw binary has no `type` (a schema MAY be omitted entirely for binary content); encoded binary is `type: string` with `contentEncoding`; `contentMediaType` SHALL be ignored when it contradicts the Media Type key or Encoding `contentType`. `contentEncoding`/`byte` are unrelated to HTTP `Content-Encoding`. | per edition | def, MAY, SHALL | 2.0 §6.3, §6.4.9; 3.0 §4.4.1, §4.4.2; 3.1 §4.4.2, §4.8.14; 3.2 §4.14.7, §4.24.4.3 |
| B17 | Schema inspection for non-JSON serialization: MUST examine the starting schema and schemas reachable via `$ref` and `allOf`; a schema without `type` MUST be treated as allowing all types; ambiguous `type` lists are implementation-defined; implementations MAY inspect `oneOf` and similar but MUST NOT resolve ambiguities; "serializers that have access to validated data MUST inspect the data if possible". | 3.2 (applies naturally to all) | MUST, MAY, MUST NOT | 3.2 §4.24.4.2 |
| B18 | Sequential request bodies (3.2): data modelled as an array; `itemSchema` applies per item. | 3.2 | MUST | 3.2 §4.14.3.1, §4.14.3.1.1 |
| B19 | `readOnly`: 2.0 "MUST NOT be sent as part of the request"; 3.0 SHOULD NOT be sent, and `required` then applies to responses only; 3.1/3.2 annotations that the owning authority MAY ignore or reject, with the note that stripping read-only fields "is burdensome for clients". | per edition | MUST NOT, SHOULD NOT, MAY | 2.0 §6.4.18; 3.0 §4.7.24; 3.2 §4.24.5.2 |
| B20 | Examples never define wire values; tools MAY validate them. | 3.x | MAY | 3.2 §4.19 |

### 1.7 Security schemes and credentials on the wire

| # | Requirement | Editions | Level | Source |
|---|---|---|---|---|
| C1 | `security` is a list of alternatives: only one Security Requirement Object needs to be satisfied. Within one object, all listed schemes MUST be satisfied. Operation `security` overrides the root; `[]` removes it. `{}` indicates anonymous access (3.0.3+). 3.1.1+/3.2: "The list can be incomplete, up to being empty or absent." | all | MUST, def | 2.0 §6.4.1, §6.4.7, §6.4.26; 3.0 §4.7.1, §4.7.30; 3.1 §4.8.1, §4.8.30; 3.2 §4.1.1, §4.30 |
| C2 | Scheme types: 2.0 `basic`, `apiKey` (query, header), `oauth2` (single `flow`: implicit, password, application, accessCode). 3.0 `apiKey` (query, header, cookie), `http` (`scheme` per RFC 7235/9110, case-insensitive, SHOULD be IANA-registered; `bearerFormat` is a hint), `oauth2` (`flows`), `openIdConnect`. 3.1 adds `mutualTLS`. 3.2 adds the `deviceAuthorization` flow, `oauth2MetadataUrl`, and `deprecated`. | per edition | REQUIRED, SHOULD | 2.0 §6.4.24; 3.0 §4.7.27; 3.1 §4.8.27; 3.2 §4.27, §4.28, §4.29 |
| C3 | Requirement values: scopes for `oauth2`/`openIdConnect` (may be empty); for other types the array MUST be empty (2.0, 3.0) or MAY list role names not exchanged in-band (3.1, 3.2). | per edition | MUST, MAY | 2.0 §6.4.26; 3.0 §4.7.30; 3.1 §4.8.30; 3.2 §4.30.1 |
| C4 | Basic: `base64(user-id ":" password)`; a user-id containing `:` is invalid; no control characters; the default charset is left undefined (only `UTF-8` may be advertised by servers). | all | MUST NOT | RFC 7617 §2, §2.1 |
| C5 | Bearer: `Authorization: Bearer <b64token>`; clients SHOULD use the header method and MUST NOT use more than one method per request. | all | SHOULD, MUST NOT | RFC 6750 §2, §2.1 |
| C6 | OAuth 2: a client MUST NOT use an access token whose type it does not understand. OAS says nothing about how an `oauth2` or `openIdConnect` token goes on the wire. | 2.0, 3.x | MUST NOT | RFC 6749 §7.1 |
| C7 | Authentication scheme tokens are case-insensitive. | all | def | RFC 9110 §11.1 |
| C8 | `apiKey`: `name` is the header, query, or cookie parameter name; OAS gives no encoding rule beyond the location's own. | all | def | 2.0 §6.4.24; 3.x Security Scheme |
| C9 | `mutualTLS`: a client certificate at the TLS layer; nothing in the HTTP message. | 3.1, 3.2 | def | 3.1 §4.8.27; 3.2 §4.27 |
| C10 | Security Requirement name resolution across documents is ambiguous and exploitable; the entry document is RECOMMENDED. | 3.0.4, 3.1.1+, 3.2 | RECOMMENDED | 3.0 App F; 3.1 App F; 3.2 §6.3, App G.3 |

### 1.8 Responses

| # | Requirement | Editions | Level | Source |
|---|---|---|---|---|
| Q1 | Documentation need not cover every status; it is expected to cover success and known errors. `default` MAY describe all codes not covered individually. | all | MAY | 2.0 §6.4.11; 3.0 §4.7.16; 3.1 §4.8.16; 3.2 §4.16 |
| Q2 | Ranges `1XX` to `5XX` with uppercase `X` MAY be used; an explicit code takes precedence over its range. 2.0 has no ranges. | 3.x | MAY | 3.0 §4.7.16.2; 3.1 §4.8.16.2; 3.2 §4.16.2 |
| Q3 | 2.0 Response `schema` absent means "no content is returned"; root `type: file` is allowed and SHOULD come with a matching `produces`. | 2.0 | def, SHOULD | 2.0 §6.4.12 |
| Q4 | 3.x Response `content` keys are media types or ranges; for a response matching several keys "only the most specific key is applicable". | 3.x | def | 3.0 §4.7.17; 3.1 §4.8.17; 3.2 §4.17.1 |
| Q5 | A response header declared as `Content-Type` SHALL be ignored. Header names are case-insensitive. Header Objects follow Parameter rules with `style: simple` only; `required` defaults to false; no percent-encoding (3.1.2, 3.2). `Set-Cookie` is the multi-line exception. | 3.x | SHALL | 3.x Response and Header Objects; 3.2 §4.21.3; RFC 9110 §5.3 |
| Q6 | Status code classes: 1xx informational, 2xx successful, 3xx redirection, 4xx client error, 5xx server error. | all | def | RFC 9110 §15 |
| Q7 | A missing `Content-Type`: the recipient MAY assume `application/octet-stream` or examine the data; repeated `Content-Type` fields are an error seen in practice with divergent handling. | all | MAY | RFC 9110 §8.3 |
| Q8 | Responses to HEAD and 1xx, 204, 304 responses have no content. An incomplete response (truncated length or chunking) is recorded as incomplete. | all | def | RFC 9112 §6.3, §8 |
| Q9 | `schema` applies to the complete content. | 3.2 | MUST | 3.2 §4.14.3 |

### 1.9 Sequential media and server-sent events (3.2)

| # | Requirement | Editions | Level | Source |
|---|---|---|---|---|
| Z1 | A sequential media type repeats a structure with no envelope: `application/jsonl`, `application/x-ndjson`, `application/json-seq`, `+json-seq`, `text/event-stream`, `multipart/mixed`. Implementations MUST support mapping them to an array in order. `itemSchema` MUST be applied to each item independently. | 3.2 | MUST | 3.2 §4.14.3.1, §4.14.3.1.1 |
| Z2 | JSON text sequences: `RS` before and `LF` after each text; MUST be UTF-8; a parser SHOULD continue after a malformed element; top-level numbers, `true`, `false`, `null` not followed by whitespace MUST be dropped as possibly truncated; there is no end-of-sequence marker. | 3.2 | MUST, SHOULD | RFC 7464 §2.1 to §2.4 |
| Z3 | SSE: implementations MUST work with event data after parsing per the `text/event-stream` spec (ignored fields, comments, multi-line `data` combined); field types MUST follow that spec (`retry` an integer); other fields are strings. | 3.2 | MUST | 3.2 §4.14.4 |
| Z4 | Event stream parsing: UTF-8 decode, strip one BOM, lines end in CRLF, LF or CR; `event`, `data` (appended with LF), `id` (ignored if it contains NUL), `retry` (ASCII digits only); unknown fields and `:` comments ignored; a blank line dispatches; an unterminated final event is discarded. Browsers skip dispatch when the data buffer is empty and carry `lastEventId` forward; for other user agents dispatch steps "are implementation dependent". | 3.2 | must | WHATWG HTML "Parsing an event stream", "Interpreting an event stream" |
| Z5 | `maxLength` MAY bound a streaming payload's length (octets for raw binary). | 3.x | MAY | 3.0 §4.4.2; 3.1 §4.4.2; 3.2 §4.14.3.2 |
| Z6 | Streaming positional multipart uses `itemSchema` with `itemEncoding` (for example `multipart/mixed`, `multipart/byteranges`). | 3.2 | def | 3.2 §4.14.3.1.1, §4.15.4.8, §4.15.4.9 |

### 1.10 HTTP mechanics the client inherits

| # | Requirement | Level | Source |
|---|---|---|---|
| H1 | A user agent MAY follow a redirect automatically, with care for unsafe methods. When following, it SHOULD: resolve `Location` against the original target; drop headers it generated, including `Authorization` and `Cookie`; consider dropping caller-added `Authorization` and `Cookie` where there are security implications; change the method per status; drop content headers if the method became GET or HEAD. 301 and 302 MAY turn POST into GET; 303 is fetched with GET or HEAD; 307 and 308 MUST NOT change the method. A client SHOULD detect redirect loops. | MAY, SHOULD, MUST NOT | RFC 9110 §15.4, §15.4.2 to §15.4.9 |
| H2 | `Content-Encoding` lists codings in the order applied; codings are case-insensitive; `identity` SHOULD NOT be listed. With no `Accept-Encoding`, any coding is acceptable to the user agent; an empty `Accept-Encoding` asks for none. | SHOULD NOT | RFC 9110 §8.4, §8.4.1, §12.5.3 |
| H3 | `Accept` lists media ranges with optional `q`; more specific ranges take precedence. | def | RFC 9110 §12.5.1 |
| H4 | Uppercase hex SHOULD be used in percent-encoding; implementations must not encode or decode the same string twice. | should, must not | RFC 3986 §2.1, §2.4 |
| H5 | QUERY is safe and idempotent and carries content; a 303 answer is fetched with GET, 307/308 repeat the QUERY. | def | draft-ietf-httpbis-safe-method-w-body-11 |

---
## 2. Gaps: where OAS is silent or ambiguous and a client must still decide

Every entry below is a **client decision**. Each gives: what is unspecified; the realistic options; the recommended default and why; whether a caller plausibly needs to change it; and what happens when the caller configures nothing.

Cross-cutting recommendation: every default here should be documented, visible before sending (a build-without-send or "describe this call" facility, as swagger-client's `buildRequest` does, prior-art §4.3), and overridable per client and per call. A caller who wants no defaults can then pin every choice explicitly without the library needing a second mode.

A second cross-cutting rule removes a whole class of gaps: **convert any Go input value into the JSON data model first** (via `encoding/json` semantics: struct tags, `MarshalJSON`, `TextMarshaler` map keys, numbers kept exact), then apply OAS serialization to that data. OAS defines every serialization over the JSON Schema data model (3.2 §4.24.3, §4.24.4.1), so this gives one predictable mapping from Go to the wire. Scalars take a fast path so simple calls do not pay for reflection.

### 2.1 Documents

**G1. Detecting JSON versus YAML, and text encodings.**
- Unspecified: how to tell the formats apart, and which encodings to accept. YAML requires UTF-16/32 support (YAML 1.2.2 §5.2); JSON requires UTF-8 (RFC 8259 §8.1).
- Options: trust a file extension or `Content-Type`; sniff the first non-whitespace byte; always parse as YAML (JSON is nearly a YAML 1.2 subset).
- Default: strip a UTF-8 BOM (RFC 8259 §8.1 MAY); detect UTF-16/32 by BOM and transcode; if the first significant byte is `{`, parse as strict JSON, otherwise YAML 1.2. Rationale: JSON parsing is much faster, and a JSON parse failure then reports JSON-shaped errors.
- Caller change: no.
- Nothing configured: as above.

**G2. YAML 1.1 versus 1.2 quirks.**
- Unspecified: OAS recommends YAML 1.2 (D4) but real documents are written against 1.1-era parsers. Differences that change meaning: `yes/no/on/off/y/n` booleans (1.1) versus strings (1.2, RFC 9512 §4.4); `0777` octal (1.1) versus decimal 777 (1.2 Core); `1:20` sexagesimal; unquoted dates as timestamps; `<<` merge keys (1.1 only); `.inf`/`.nan`; unquoted numeric keys such as `200:`; `swagger: 2.0` written unquoted (a float).
- Options: strict 1.2 JSON schema (rejects ordinary unquoted strings, see X16); 1.2 Core schema; 1.1 emulation.
- Default: YAML 1.2 Core schema resolution; treat a `%YAML 1.1` directive as 1.2 (YAML 1.2.2 §6.8.1 says to warn, not fail). Stringify scalar mapping keys (so `200:` is `"200"`, matching D4's Failsafe-string rule and D5's intent); reject non-scalar keys. Timestamps stay strings. Reject `.inf`/`.nan` (not representable in the JSON data model; RFC 9512 §3.4; 3.2 §3.1 SHOULD NOT). Honour `<<` merge keys, because an author who wrote `<<:` never meant a literal key and RFC 9512 §3.5 notes deployments rely on them. Accept an unquoted numeric `swagger: 2.0` as `"2.0"`, since the intent cannot be mistaken.
- Caller change: rarely.
- Nothing configured: as above; a 1.1-only boolean like `yes` stays the string `"yes"`.

**G3. Duplicate keys.**
- Unspecified: OAS says patterned fields MUST be unique (D3); YAML makes duplicates a loading failure (D13); RFC 8259 says SHOULD be unique and leaves behaviour unpredictable.
- Options: error; first wins; last wins.
- Default: in **documents**, reject with the location of the duplicate (a differential-parsing risk; YAML requires it). In **response bodies**, last wins, which is RFC 8259-permitted and what Go's `encoding/json` does.
- Caller change: no.
- Nothing configured: as above.

**G4. Which version strings to accept.**
- Unspecified: behaviour for future patches (3.1.3, 3.2.1), pre-release suffixes (`3.1.0-rc1`), unknown minors (3.3.0), or missing patch (`3.1`).
- Default: accept any `3.0.*`, `3.1.*`, `3.2.*` patch, including future ones, because OAS says tooling for a minor line SHOULD accept all its patches (D6). Reject unknown minors and majors with a clear error; accept a pre-release suffix of a known line. Reject `3.1` without a patch (it is not a version number).
- Caller change: rarely; a "treat as edition X" override covers experiments.
- Nothing configured: unknown minor fails at load.

**G5. When defects fail: load time or call time.**
- Unspecified: OAS never says what a consumer does with a partly broken description.
- Options: fail the whole load on any defect; fail only the operations a defect reaches.
- Default: the load succeeds when the root is usable (version, required roots); each operation carries its own problems, reported when it is described or called. A defect in an unused component affects nothing. Rationale: real descriptions are large and imperfect, and one bad schema should not disable a whole API (restish's "diagnose, never silently drop", prior-art §4.6). Offer a whole-document diagnostics listing.
- Caller change: a caller wanting all-or-nothing can check the diagnostics listing.
- Nothing configured: broken operations return a descriptive error when called; others work.

**G6. External reference retrieval and local files.**
- Unspecified: retrieval is MAY (R4); OAS warns that external resources may be untrusted (D15) but sets no policy.
- Options: never fetch; fetch anything; same-origin only; caller-supplied fetcher.
- Default: fetch only within the entry document's origin. Entry loaded from a file: `file:` references allowed, network references not. Entry loaded over http(s): same scheme, host and port only; no `file:`; cross-origin needs explicit permission. Entry supplied as bytes: nothing fetched unless the caller supplies more documents or a fetcher. Every fetch honours the caller's context, timeout and size limit (G8). Rationale: split specs work in the two common layouts, while a network-loaded document cannot read local files or reach internal hosts (SSRF). restish applies the same rule (prior-art §4.6), and JSON Schema 2020-12 §9.1.2 expects implementations to know their schemas ahead of time.
- Caller change: yes, often: to allow a specific other origin, or to supply documents under their intended URIs (3.2 R4 SHOULD). Provide one hook: a map of URI to document plus an optional fetch function.
- Nothing configured: out-of-policy references fail and are reported against the operations they reach (G5).

**G7. Documents without a URI.**
- Unspecified: a caller may pass bytes. Relative references and relative servers then have no base.
- Default: give the document an internal base (a random `urn:uuid:`, as 3.2 App F.4 suggests) so fragment-only references work. Relative server URLs (including the default `/`) cannot be resolved; see G15.
- Caller change: yes when the document has relative servers: set the document's URI or a base URL.
- Nothing configured: internal references resolve; a call whose server is relative fails with an error that says to supply a base URL.

**G8. Document size and complexity limits.**
- Unspecified entirely by OAS; RFC 8259 §9 permits limits; RFC 9512 §4.2 requires bounding alias expansion.
- Default: no size limit on bytes or files the caller hands over (they are already in memory or trusted). For network-fetched documents, a per-document limit (suggested 64 MiB; the largest public descriptions are tens of MB). Always: an alias-expansion bound, a nesting-depth bound, and cycle detection during reference traversal (D15).
- Caller change: occasionally (raise the fetch limit).
- Nothing configured: an over-limit fetch fails with an error naming the limit.

**G9. Path Item `$ref` with sibling fields.**
- Unspecified: undefined when a field appears on both sides (R7).
- Options: referenced wins; local wins; error.
- Default: merge, local fields winning. 3.1 and 3.2 say this behaviour "is likely to change ... to bring it into closer alignment with the behavior of the Reference Object", whose local `summary`/`description` override the target.
- Caller change: no.
- Nothing configured: as above.

**G10. Security scheme names used in referenced documents.**
- Unspecified: implementation-defined; entry document RECOMMENDED (R8, C10).
- Default: resolve from the entry document; if the name is absent there, fall back to the document containing the requirement. A 3.2 key that is not a component name is resolved as a URI (R9).
- Caller change: no.
- Nothing configured: as above.

### 2.2 Operations

**G11. Addressing operations.**
- Unspecified: what to do without an `operationId`, with duplicates (an invalid document), or with odd casing.
- Default: two addressing forms, both always available: `operationId` (exact, case-sensitive, searched across all parsed documents per R8) and method plus exact Paths key (no URL matching). A duplicated `operationId` is an error when used by id, naming the colliding paths; method plus path still works. Webhooks and callbacks are not in either index.
- Caller change: no.
- Nothing configured: as above.

**G12. Unusual methods.**
- Unspecified: `additionalOperations` keys such as `Post` or `get` (case-distinct from the fixed fields but distinct methods under RFC 9110 §9.1), `CONNECT`, and bodies on TRACE.
- Default: send additional-operation keys exactly as spelled. Refuse `CONNECT` operations (tunnel semantics, RFC 9112 §6.3 item 2, have no API-call meaning). Refuse a body on TRACE (RFC 9110 §9.3.8 MUST NOT).
- Caller change: no.
- Nothing configured: as above.

### 2.3 Servers and the base URL

**G13. Choosing among several servers.**
- Unspecified: OAS lists alternatives but never says which one a client uses.
- Options: first listed; require a choice; choose by `name` (3.2); prefer https; caller base URL.
- Default: the first entry of the effective list (operation, then path item, then root), with variable defaults. `servers` is an array, so order is authored and meaningful. swagger-client and openapi-client-axios default to the first server (prior-art §4.2, §4.3).
- Caller change: yes, commonly (staging versus production). Offer selection by index, by 3.2 `name`, by URL match, and a full base-URL override that replaces the server entirely, per client or per call.
- Nothing configured: first server.

**G14. Server variable values.**
- Unspecified: a supplied value outside `enum`; a supplied variable the template lacks; encoding of substituted values; `{name}` with no declaration.
- Default: a value outside `enum` is an error (enum defines "a limited set", and a typo is far likelier than an undeclared deployment; the base-URL override is the escape hatch). An unknown variable name is an error (restish, prior-art §4.6). Values are substituted verbatim (no percent-encoding), and the finished URL must parse (S6). An undeclared `{name}` needs a caller value or the server is unusable.
- Caller change: yes, for tenant or region variables.
- Nothing configured: declared defaults.

**G15. Relative servers, and 2.0 host, scheme and basePath gaps.**
- Unspecified: relative server URLs with no document URI; 2.0 with no `host` or `schemes` and no document URI; several 2.0 `schemes`; `ws`/`wss`.
- Default: resolve relative 3.x server URLs against the document's retrieval URI (not `$self` in 3.2, S2); with no URI, require a base URL (error naming it). 2.0: `host` absent means the document's host and port; `schemes` absent means the document's scheme; with several schemes prefer `https`, then the document's own scheme, then the first listed; skip `ws`/`wss` for HTTP calls. With no document URI and no `schemes`, use `https`.
- Caller change: yes when the document is supplied as bytes.
- Nothing configured: as above, or an actionable error.

**G16. Joining base and path.**
- Unspecified: OAS says "appended" (S4). With `url: /` (the default!) or `basePath: /`, naive concatenation gives `//pets`. 3.2's own example shows a server `.` resolving to a URL without a trailing slash (3.2 §4.5.2.1).
- Default: concatenate, collapsing exactly one `/` when the base ends with `/` and the path starts with `/`. Keep everything else byte for byte. A chosen server URL containing a query or fragment (forbidden, S2) is an error.
- Caller change: no.
- Nothing configured: as above.

### 2.4 Parameters

**G17. Naming parameters in call input.**
- Unspecified: parameters are unique by name plus location (P1), so `id` in path and `id` in query can coexist. Unknown names supplied by a caller.
- Default: accept inputs grouped by location (openapi-fetch style, prior-art §4.1) or a flat map by name; a flat name that matches parameters in two locations is an error asking for the location. An unknown name is an error (typo protection).
- Caller change: no.
- Nothing configured: as above.

**G18. Missing and nil values.**
- Unspecified: OAS says "required" but not what a client does when one is absent.
- Default: a missing required parameter or required body is an error before sending (a path parameter cannot be omitted anyway). An absent optional parameter is omitted. A Go `nil` for an optional parameter is treated as absent; for a required one it is an error. Schema and parameter `default` values are never sent (P20).
- Caller change: no.
- Nothing configured: as above.

**G19. Non-string values in parameters (numbers, booleans, null, dates, bytes).**
- Unspecified: explicitly implementation-defined (P21).
- Options: JSON spelling; Go `fmt` spelling; locale-specific; refuse.
- Default: JSON spelling. Booleans `true`/`false` (3.2's own example, §4.19.3.3). Numbers in the shortest round-trip form with no exponent for integers (`10`, not `1e1` or `10.0`); `json.Number` passes through verbatim; other floats use the ECMAScript algorithm that `encoding/json` also uses, so query strings match the JSON a JavaScript client would send. `time.Time`: RFC 3339 `date-time`, or `full-date` when the schema says `format: date`. `[]byte`: standard base64 with padding (RFC 4648 §4), matching `format: byte`. A `TextMarshaler` uses `MarshalText`. Null: see G20.
- Caller change: rarely; a caller needing another spelling passes a string (App B's own advice).
- Nothing configured: as above.

**G20. Undefined values, empty strings, empty arrays and objects.**
- Unspecified: the OAS `undefined` column and RFC 6570 disagree (X1); what `nil`, `[]`, `{}` and `""` produce.
- Default: follow RFC 6570, which OAS's App C.4.3 also follows: `nil`, `[]`, `{}` are undefined and the parameter is omitted. `""` is a defined empty value and uses the style's empty spelling (`name=` for form, `;name` matrix, `.` label, empty simple). Those spellings are exactly what OAS's `undefined` column shows, so a caller who needs them passes `""`. An empty path parameter value is an error: it yields an empty segment that silently re-routes the request.
- Caller change: rarely.
- Nothing configured: as above.

**G21. Nesting beyond one level.**
- Unspecified: implementation-defined (P8); `deepObject` "representation of array or object properties is not defined".
- Default: `deepObject` nested objects use nested brackets (`a[b][c]=v`, the widely used convention); arrays inside `deepObject`, and nested values in other styles, are errors naming the parameter, because conventions disagree (`a[]=`, `a[0]=`, repeated keys). The caller can pre-serialize to a string or use a `content` parameter.
- Caller change: sometimes (a string value or an option for an array convention).
- Nothing configured: error for ambiguous nesting.

**G22. Ordering.**
- Unspecified: query parameter order, object member order in serialized values and bodies. OAS calls form pair order implementation-defined (B10).
- Default: query parameters in declared order (path item parameters, then operation parameters, overrides in place), security query keys last; object members in the order the Go value provides when ordered (structs), else sorted by key. Deterministic output helps caching, signing and tests.
- Caller change: no.
- Nothing configured: as above.

**G23. Percent-encoding details.**
- Unspecified: exact character sets per location; what `allowReserved` still encodes; encoding of literal characters in path keys; Go pitfalls.
- Default: build every URL component ourselves and send those bytes (RFC 3986 §2.4: never encode twice). Values: encode everything outside RFC 3986 unreserved, uppercase hex (the "safest approach", P10; H4); spaces as `%20` in style-based query parameters (RFC 6570). With `allowReserved`, keep reserved characters but still encode `#`, `[`, `]` (never valid in a query, RFC 3986 §3.4) and anything outside the URI character set; in 3.2 path parameters, still encode `/ ? #` (P3). Literal characters in Paths keys that are not valid `pchar` are percent-encoded; existing `%HH` triples are kept. Note that Go's `url.PathEscape` and `url.QueryEscape` do not match RFC 6570 (`PathEscape` leaves `& + : = @ $` unescaped), so the client needs its own unreserved-only encoder.
- Caller change: no.
- Nothing configured: as above.

**G24. Header values.**
- Unspecified: characters that cannot appear in a field; quoting; duplicate header parameters differing only in case; empty values.
- Default: no percent-encoding and no quoting in any edition (X5). A value containing CR, LF, NUL or other controls, or leading/trailing whitespace, is an error before sending (RFC 9110 §5.5). Two supplied header parameters whose names differ only in case are an error. An empty string sends an empty field value. Arrays use `simple` style in one field line.
- Caller change: no.
- Nothing configured: as above.

**G25. Cookies.**
- Unspecified: combining cookie parameters, `apiKey` cookies and cookies from the caller's `http.Client` jar; validation; 3.0/3.1 `form` cookies with several values.
- Default: one `Cookie` header, pairs joined by `; ` (RFC 6265 §4.2.1, §5.4), parameters in declared order then credentials. 3.0/3.1 `form` cookies: percent-encode values (RFC 6570 form), no `?`, and for exploded objects emit each pair joined by `; ` rather than `&` (App D says `&` is always wrong for cookies). 3.2 `style: cookie`: pass values through. Reject only bytes that break framing (controls and `;`); do not police the full cookie-octet set, which RFC 6265 §4.1.1 imposes on servers' `Set-Cookie`.
- Caller change: no.
- Nothing configured: as above.

**G26. Headers the document cannot express.**
- Unspecified: 3.x ignores `Accept`, `Content-Type`, `Authorization` header parameters (P4), so documents that modelled a bearer token as a header parameter lose it; 2.0 has no such rule.
- Default: 3.x: ignore them as required, and offer a per-client and per-call extra-headers option plus a request hook as the escape hatch. 2.0: honour them as ordinary parameters: a supplied `Authorization` is sent, a supplied `Accept` replaces the generated one (G45), and a supplied `Content-Type` selects the request media type from `consumes` (G27).
- Caller change: yes, for 3.x documents that relied on such parameters.
- Nothing configured: 3.x ignores them; 2.0 treats them as normal inputs.

### 2.5 Request bodies

**G27. Choosing a request media type.**
- Unspecified: which of several `content` keys (or `consumes` entries) to use; how to send a media range key such as `*/*` or `image/*`. OAS only says the most specific key applies to a given request (B2). Content maps are unordered (RFC 8259 §4; YAML 1.2.2 §3.2.2.1).
- Options: caller always chooses; first listed; prefer JSON; infer from the Go value.
- Default: (1) a caller-chosen media type, which must equal a key or fall within a declared range; (2) otherwise, if one key, use it; (3) otherwise by value kind: `[]byte`, `io.Reader` or a file value prefers `application/octet-stream`, then another concrete non-JSON, non-text, non-form key; everything else prefers `application/json`, then any `+json`, then `application/x-www-form-urlencoded`, then `multipart/form-data`, then `text/plain`; (4) remaining ties go to document order, documented as a heuristic. A range key needs a concrete type: `*/*` and `application/*` become `application/octet-stream` for bytes and `application/json` otherwise; `text/*` with a string becomes `text/plain`; any other range (for example `image/*`) is an error asking for the type, since sniffing is not an acceptable default (B8). swagger-client exposes the same choice as `requestContentType` (prior-art §4.3).
- Caller change: yes, when an API offers several and the heuristic picks the wrong one.
- Nothing configured: as above.

**G28. Encoding the body for the chosen media type.**
- Unspecified: JSON byte details; non-string values under `text/*`; XML from objects; `charset`.
- Default: JSON (`application/json`, `+json`): compact UTF-8, `json.Number` verbatim, no HTML escaping (smaller and byte-faithful). `text/*`: a string sent as UTF-8; a bool or number uses the JSON spelling (as for form text parts, B7); objects are an error. XML (`application/xml`, `+xml`): only a string or bytes; no object-to-XML mapping (the XML Object's rules are large and rarely needed at call time). Binary: bytes or a reader, untouched. `Content-Type` is the declared key verbatim (with the chosen concrete type for ranges); add nothing, not even `charset`.
- Caller change: rarely; a caller can always pass pre-encoded bytes with an explicit media type.
- Nothing configured: as above.

**G29. Form-urlencoded bytes.**
- Unspecified in practice: the editions cite HTML 4.01, RFC 1866 and WHATWG, which differ on `~`, `*`, and space (X8).
- Default: one encoder for all editions: WHATWG's (space as `+`; alphanumerics and `*-._` literal; everything else `%HH`). Every cited decoder accepts it, because decoders are tolerant (3.x App E.4/E.5: "URIs percent-encoded according to any specification will be decoded correctly").
- Caller change: no.
- Nothing configured: as above.

**G30. Multipart parts: content types, filenames, headers.**
- Unspecified: which type to put on a part whose `contentType` is a list or wildcard; whether to send `filename` (OAS never mentions it); what to do with Encoding `headers` whose values the document cannot fix; `Content-Transfer-Encoding`.
- Default:
  - Part type: a caller-supplied type (a file value carrying name and type); else the single concrete type if the list has one; else, for bytes, `application/octet-stream`, which RFC 7578 §4.4 names for unknown file types; for other values, the Encoding default (B7). Never sniff.
  - Filename: send `filename` for binary parts (bytes, readers, file values), taken from the file value's name, else the part name. RFC 7578 §4.2 says a filename SHOULD be supplied for file content, and common servers (including Go's own `multipart.Reader` form parsing) treat a part without `filename` as a text field, not a file. Escape `"` and `\`; never emit `filename*` (RFC 7578 §4.2 MUST NOT).
  - Part headers: send caller-supplied values for declared Encoding headers only. Do not send `Content-Transfer-Encoding` even when `format: byte` or `contentEncoding` implies it (RFC 7578 §4.7 SHOULD NOT; X12).
  - Arrays: one part per item with the same name (B10). Boundary: random, RFC 2046 compliant.
- Caller change: yes, for exact part types and filenames (a small file value type covers both).
- Nothing configured: as above.

**G31. Bodies on GET, HEAD, DELETE, and bodies the operation does not declare.**
- Unspecified: 2.0 says nothing; 3.0 says ignore; 3.1/3.2 permit (B3, X7).
- Default: 3.0 GET/HEAD/DELETE: the `requestBody` declaration is ignored, so the operation has no body input. 2.0, 3.1, 3.2: send a declared body on any method except TRACE; the declaration is the out-of-band indication RFC 9110 §9.3.1 asks for. In every edition, a body supplied to an operation with no effective request body is an error, never silently dropped.
- Caller change: no.
- Nothing configured: as above.

**G32. Streaming request bodies and length.**
- Unspecified: `Content-Length` for readers; replay on 307/308 redirects.
- Default: bytes and strings get a length; readers are streamed (chunked on HTTP/1.1) unless the caller supplies a length; a body that cannot be replayed stops a method-preserving redirect with an error rather than resending without it. 3.2 sequential bodies take an iterator or slice and write one framed item at a time.
- Caller change: sometimes (supply a length for servers that reject chunked uploads).
- Nothing configured: as above.

**G33. `readOnly`, `writeOnly`, schema defaults.**
- Unspecified: whether a client strips or rejects read-only properties (B19 differs by edition).
- Default: never strip, add or reject properties; send what the caller gives. 3.2 §4.24.5.2 calls stripping "burdensome for clients"; the 2.0/3.0 wording constrains what belongs in a request, which is the caller's call to make.
- Caller change: no.
- Nothing configured: as above.

**G34. Validating inputs and outputs against schemas.**
- Unspecified: OAS makes validation a tool option (examples, formats, readOnly all MAY).
- Default: no schema validation of requests or responses; only the shape checks serialization needs (for example an object for `deepObject`). The server is the authority, validation costs time, and restish makes it opt-in (prior-art §4.6). Unknown `format` values are ignored (3.x "MAY default back to the type alone").
- Caller change: sometimes (contract testing); keep validation a separate, optional layer.
- Nothing configured: no validation.

### 2.6 Security

**G35. Several security alternatives.**
- Unspecified: which alternative to apply; what to do when several are satisfiable; what to do when none is.
- Options: apply everything the caller supplied; first alternative; first satisfiable alternative; refuse or send anonymously when none is satisfiable.
- Default: apply exactly one alternative: the first, in document order, whose every scheme is satisfiable. A scheme is satisfiable when the caller supplied its credential; `mutualTLS` counts as satisfiable (the client adds nothing; the caller's TLS config decides); `{}` is satisfiable with no credentials; a scheme the caller marked "handled by my transport" (for example an `oauth2.Transport` round tripper) counts as satisfied. Never mix credentials from different alternatives (RFC 6750 §2 also forbids sending a bearer token two ways). If none is satisfiable and there is no `{}`, fail before sending with an error that lists the alternatives and the missing credentials without printing secrets (restish, prior-art §4.6). `security: []` sends nothing.
- Caller change: yes: forcing a specific alternative, or marking transport-provided schemes.
- Nothing configured: an operation that requires credentials fails locally with an actionable message, not a 401. The "list can be incomplete" sentence (C1) is covered by the transport marker and by the extra-headers escape hatch (G26).

**G36. Putting credentials on the wire.**
- Unspecified: how `oauth2` and `openIdConnect` tokens are sent (C6); Basic's charset; unregistered or challenge-based `http` schemes; `apiKey` encoding.
- Default: `oauth2` and `openIdConnect`: `Authorization: Bearer <token>` (RFC 6750 §2.1, the method every resource server MUST support). `http` `bearer`: same. `http` `basic`: RFC 7617 with UTF-8, rejecting a user-id containing `:` or any control character. Other `http` schemes (Digest, HOBA, and so on): accept a caller-supplied full credential string placed after the scheme name, or a per-request hook for challenge-based schemes. `apiKey`: query keys are percent-encoded like query values (G23); header keys pass through with G24 checks; cookie keys join the single `Cookie` header (G25). Tokens can come from a static value or a refreshing source (for example `oauth2.TokenSource`).
- Caller change: yes: credentials are always caller input.
- Nothing configured: see G35.

**G37. Credentials colliding with parameters.**
- Unspecified: an `apiKey` named like a declared parameter at the same location; two schemes in one alternative both needing `Authorization`.
- Default: an alternative with two `Authorization` producers is unusable (error naming both). A credential and a parameter supplied for the same destination is an error; either one alone is sent.
- Caller change: no.
- Nothing configured: as above.

**G38. Acquiring tokens.**
- Unspecified: OAS describes flows and discovery URLs but not whether a client runs them.
- Default: the client never runs OAuth flows, OIDC discovery, or scope checks; it accepts tokens or token sources. Flows, scopes and URLs are exposed as description data for callers who build login.
- Caller change: no.
- Nothing configured: as above.

### 2.7 Responses

**G39. Success versus failure.**
- Unspecified: OAS does not classify outcomes.
- Default: final status 2xx is success; anything else is returned as a failure value carrying status, headers, the decoded body if possible (G42, G43), and the raw bytes. A transport error, a cancelled context and a failure response are distinguishable. This is what swagger-client, openapi-client-axios and bravado do (prior-art §4.2, §4.3, §4.5). A 3xx reaches the caller only when not followed (G47) or for 304, and is then a failure value the caller can inspect.
- Caller change: no (the failure value carries everything).
- Nothing configured: as above.

**G40. Status codes the document does not declare.**
- Unspecified: OAS only says documentation need not be complete (Q1).
- Options: error; decode generically.
- Default: lookup order exact code, then range (3.x; accept lowercase `2xx` too, which has no conflicting meaning), then `default`. With no match, decode generically by the actual `Content-Type` (JSON for `application/json` and `+json`, a string for `text/*`, bytes otherwise). An undeclared 2xx is still a success.
- Caller change: sometimes (a contract-testing caller wants an error; offer an inspectable "undeclared" flag rather than a mode).
- Nothing configured: generic decoding, no error.

**G41. Matching the response media type.**
- Unspecified: parameter handling (`application/json; charset=utf-8` against a declared `application/json`), undeclared types, missing or repeated `Content-Type`.
- Default: compare type/subtype case-insensitively; a declared key matches when its type/subtype matches (or its range covers it) and all its parameters are present with equal values; among matches the most specific wins (Q4, H3). No match: decode by structured suffix (`+json` as JSON, RFC 6839) or generically (G40). Missing `Content-Type`: if the governing response declares exactly one media type, use it; otherwise bytes (RFC 9110 §8.3 allows assuming `application/octet-stream`; never sniff). Several `Content-Type` values: bytes, with a decode error if a typed target was requested.
- Caller change: rarely (a per-call "decode as" override).
- Nothing configured: as above.

**G42. Decoding.**
- Unspecified: number precision; text charsets; XML; empty bodies.
- Default: into the caller's typed target when given (JSON via `encoding/json`; XML via `encoding/xml` when the media type is XML); otherwise into generic values with numbers kept exact (`json.Number`), because `int64` formats are common and float64 silently corrupts them. Text: UTF-8 when `charset` is absent (RFC 6838 §4.2.1 prefers UTF-8; X21); other charsets decoded when supported, otherwise the raw bytes are returned with an error. Empty body, 204, 304 or HEAD: no value, distinct from `null` or `""`. A declared schema never forces a failure on a success response; a typed-decode error is returned with the raw bytes.
- Caller change: rarely.
- Nothing configured: as above.

**G43. Failure bodies.**
- Unspecified: entirely.
- Default: decode with the same lookup and matching rules as success (the declared `default` or 4XX entry usually describes an error schema); read at most a bounded amount (suggested 1 MiB) so an HTML error page from a proxy cannot exhaust memory; always keep raw bytes up to that bound.
- Caller change: rarely (raise the bound).
- Nothing configured: as above.

**G44. Response headers.**
- Unspecified: whether a client parses declared headers or enforces `required`.
- Default: expose raw headers; offer an opt-in helper that parses a declared header by its Header Object (`simple` style, schema types). Do not fail a response for a missing `required` header (no validation, G34).
- Caller change: rarely.
- Nothing configured: raw headers only.

**G45. The `Accept` header.**
- Unspecified: OAS never says to send `Accept`; 2.0's `produces` and 3.x's ignoring of `Accept` parameters imply the tool generates it.
- Default: send `Accept` listing the union of declared response media keys for the operation (success and failure responses, so error bodies such as `application/problem+json` are negotiated too), in declaration order, without `q`; omit it when none are declared. A caller value replaces it (and in 2.0 a supplied `Accept` parameter does, G26).
- Caller change: sometimes (to ask for one representation).
- Nothing configured: as above.

**G46. Streaming responses.**
- Unspecified: when to stream; SSE dispatch details for non-browsers; JSON Lines blank lines; reconnection.
- Default: stream only when the caller asks for a stream, and then frame by the actual `Content-Type` in any edition (media-type driven, not edition driven). Otherwise buffer, and for 3.2 sequential types decode to an array (Z1 MUST). JSON Lines/NDJSON: split on LF, accept CRLF, skip empty lines. `json-seq`: RFC 7464 including dropping possibly truncated scalars, reporting malformed items without ending the stream. SSE: parse per Z4; yield one item per dispatched event holding the fields that event set (`data` joined with LF, `event`, `id`, `retry` as an integer), matching 3.2's own JSON Lines rendering of an event stream (§4.14.6.3), which carries no field forward; skip events that set no field. No automatic reconnection or `Last-Event-ID` resend: that is `EventSource` behaviour, not HTTP; the caller can resend with the header. Positional multipart: an iterator of parts.
- Caller change: yes, choosing stream versus buffer; reading the raw body for custom framing.
- Nothing configured: buffered.

### 2.8 HTTP behaviour

**G47. Redirects.**
- Unspecified by OAS; RFC 9110 makes following MAY (H1).
- Default: follow, as Go's `http.Client` does (up to 10 hops), preserving method and body on 307/308 and following RFC 9110 on 301/302/303. On any hop to a different origin (scheme, host, port), drop every credential the client added: `Authorization`, header `apiKey`s, and cookie credentials. Go strips only `Authorization`, `WWW-Authenticate` and `Cookie` (and only for non-subdomain hosts), so custom `apiKey` headers need the client's own redirect check. Query credentials are never re-added to a `Location`. Classify by the final response. Detect loops (RFC 9110 SHOULD).
- Caller change: yes (disable following, or supply their own `http.Client` policy).
- Nothing configured: as above.

**G48. Content codings.**
- Unspecified by OAS beyond `contentEncoding` being unrelated (H2).
- Default: responses: rely on Go's transport, which sends `Accept-Encoding: gzip` and transparently decodes it; any other coding present on a response is decoded if supported, otherwise raw bytes plus an error. Requests: no compression unless the caller asks (a per-call option that sets `Content-Encoding` and encodes); a declared `Content-Encoding` header parameter alone never compresses. A caller-supplied `Accept-Encoding` disables Go's transparent decoding, so the client must then decode itself.
- Caller change: rarely.
- Nothing configured: gzip handled transparently.

**G49. Response size limits.**
- Unspecified (RFC 8259 §9 permits limits; 3.x `maxLength` MAY bound streams).
- Default: streamed and raw bodies are unlimited; buffered decoding has a generous limit (suggested 64 MiB); failure bodies 1 MiB (G43); per-item limit for streams (suggested 16 MiB). An over-limit body is an error that names the limit and keeps what was read.
- Caller change: sometimes.
- Nothing configured: as above.

**G50. Transport concerns outside OAS.**
- Timeouts, retries, proxies, HTTP/2, `User-Agent`, tracing: none are OAS's business. Default: use the caller's `http.Client` and `context`; do nothing extra. Expose the operation identity on the request context so middleware can label metrics.

---
## 3. Edition differences

### 3.1 Differences the API can absorb, so a caller never cares

| Topic | 2.0 | 3.0 | 3.1 | 3.2 | How the client absorbs it |
|---|---|---|---|---|---|
| Edition detection | `swagger: "2.0"` | `openapi: 3.0.x` | `3.1.x` | `3.2.x` | Detected at load (G4); never a caller input. |
| Where the target lives | `schemes` + `host` + `basePath`, defaults from the document URL | `servers` + variables | same | same, plus `name` | One concept: pick a server (first by default), optionally set variables, or override the base URL (G13 to G16). |
| Where the body lives | one `in: body` parameter, or `formData` parameters | `requestBody.content` | same | same | One body input. 2.0 `formData` parameters become the properties of a form object. |
| Request media types | operation `consumes` (replaces root) | per-body `content` map | same | same | Same selection algorithm over a list of keys (G27). |
| Response media types | operation `produces`, response `schema` | per-response `content` map | same | same | Same matching (G41); 2.0 pairs each response `schema` with `produces`. |
| Array serialization | `collectionFormat` (`csv ssv tsv pipes multi`) | `style`/`explode` | same | same, plus `cookie` | Map `csv`→simple/form non-exploded, `multi`→form exploded, `ssv`→spaceDelimited, `pipes`→pipeDelimited, `tsv` handled directly. |
| Form-urlencoded bytes | HTML 4.01 | RFC 1866 | RFC 1866 | WHATWG | One tolerant-decoder-safe encoder (G29). |
| Binary and base64 | `type: file`, `format: binary`/`byte` | `format: binary`/`byte` | no `type` / `contentEncoding` | same | Go `[]byte`/`io.Reader` means raw bytes; strings are sent as given (pre-encoded base64 stays text). Runtime value first, schema second (B17). |
| Multipart part defaults | text parts, `file` parts | table keyed on `format` | table keyed on `contentEncoding`/`type` | per item for arrays | Decide from the Go value first; fall back to the edition's table (G30). |
| Nullability | not modelled | `nullable` | `type: [..., "null"]` | same | Matters only for schema inspection; a Go `nil` follows G18/G20 in every edition. |
| Header percent-encoding | unspecified | RFC 6570 implied | 3.1.2 MUST NOT | MUST NOT | Never encode headers (X5). |
| `deepObject` explode | n/a | `false` undefined | same | ignored | Always exploded (X17). |
| Style table spellings | n/a | corrected in 3.0.4 | corrected in 3.1.1 | current | Use the corrected (RFC 6570) spellings for every patch (X2). |
| Encoding defaults for urlencoded objects | n/a | "form" in 3.0.0 to 3.0.3, JSON in 3.0.4 | "form" in 3.1.0, JSON in 3.1.1+ | JSON | JSON for every patch (X4). |
| Response ranges | none | `1XX` to `5XX` | same | same | One lookup: exact, range, `default` (G40). |
| References and bases | JSON Reference, document base | JSON Reference, document base | 2020-12 `$id` for schemas | plus `$self` | Internal to loading; caller only supplies documents or a fetch policy (G6). |
| Security scheme spellings | `basic`, flows `application`/`accessCode` | `http`+`basic`, flows `clientCredentials`/`authorizationCode` | plus `mutualTLS` | plus device flow, metadata URL | Credentials keyed by scheme name; the kind of credential follows the scheme (G36). |
| Security name lookup | entry `securityDefinitions` | entry (RECOMMENDED) | same | same, plus URI keys | Internal (G10). |
| `readOnly` wording | MUST NOT send | SHOULD NOT send | annotation | annotation | Never touched (G33). |

### 3.2 Differences a caller genuinely sees

| Difference | 2.0 | 3.0 | 3.1 | 3.2 | What the caller notices |
|---|---|---|---|---|---|
| Parameter locations | path, query, header, body, formData | + cookie, no body/formData | same | + `querystring` | Which input groups exist for an operation. |
| Methods | 7 | + TRACE | same | + QUERY, any `additionalOperations` token | Which operations exist; method strings may be unusual (G12). |
| Body on GET/HEAD/DELETE | sent if declared | ignored (no body input) | sent | sent | Same document shape, different call surface (G31, X7). |
| `Accept`/`Content-Type`/`Authorization` header parameters | ordinary inputs | ignored | ignored | ignored | 3.x callers use security schemes or extra headers instead (G26). |
| Cookie credentials and parameters | none | yes (form style) | yes | yes, plus raw `style: cookie` | 3.2 `style: cookie` values must be pre-escaped by the caller (P13). |
| Security types | basic, apiKey, oauth2 | + http schemes, OIDC, cookie apiKey | + mutualTLS, roles | + device flow | Which credentials can be supplied. |
| Response ranges | no | yes | yes | yes | Only visible through which declaration governs a status. |
| Response with no declared content | 2.0: "no content is returned" | undeclared | undeclared | undeclared | None if G40 decodes generically; a strict caller sees "undeclared" flags. |
| Sequential and SSE media | not modelled | not modelled (string at most) | same | modelled, `itemSchema` | Streaming is available in every edition by media type (G46); only 3.2 gives per-item schemas and requires the array mapping. |
| Positional multipart (`multipart/mixed`, `byteranges`) | no | no | no | yes | Body input is an array of parts. |
| Server `name` | no | no | no | yes | Select a server by name. |
| `operationId` case | unstated | unstated | unstated | case-sensitive | Exact matching in every edition anyway (G11). |

---

## 4. Where editions contradict each other or the RFCs

Each item names both sides and the client action. "Latest patch wins" applies OAS's own rule that patches are clarifications (D6).

**X1. OAS's `undefined` column versus RFC 6570, and versus OAS itself.** 3.0.4, 3.1.1+ and 3.2 renamed the style table's `empty` column to `undefined` "to better align with RFC6570 Section 2.3 terminology", and list `;color`, `.`, empty, `color=` there. Under RFC 6570 §3.2.1 an undefined variable is skipped entirely and produces nothing; the listed spellings are RFC 6570's results for the empty string. OAS's own App C.4.3 follows RFC 6570 (`formulas: {}` must be left out entirely). **Action:** omit undefined values (`nil`, `[]`, `{}`); give the empty string the listed spellings (G20).

**X2. Style tables before the 2024 patches versus RFC 6570 and RFC 3986.** 3.0.0 to 3.0.3 and 3.1.0 show non-exploded `label` arrays as `.blue.black.brown` and objects as `.R.100.G.200.B.150`; RFC 6570 gives `{.list}` as `.red,green,blue`. They mark `simple` of an empty value n/a (RFC 6570 gives the empty string). They show `pipeDelimited` and `deepObject` with raw `|`, `[`, `]`, which RFC 3986 forbids in a query (App E.5/E.6 now say these MUST be percent-encoded). 3.0.0 to 3.0.2 drop `B|150` from the pipeDelimited object example. **Action:** use the 3.0.4/3.1.2/3.2 table for every patch.

**X3. 3.1.0's Encoding default contradicts itself.** 3.1.0 §4.8.15 says the default `contentType` is JSON for objects, per-item for arrays, and `application/octet-stream` "for all other cases", while its own §4.8.14.5 says primitives default to `text/plain`, and its example labels a `contentEncoding` string `text/plain` although the list says octet-stream. 3.1.1 replaced this with the `type`/`contentEncoding` table. **Action:** 3.1.2 table for all 3.1 documents.

**X4. Object properties in urlencoded bodies within one line.** 3.0.0 to 3.0.3 and 3.1.0 say complex properties are "stringified" and "the default serialization strategy ... is described in the Encoding Object's style property as form" (flattened pairs). 3.0.4 and 3.1.1+ say that with no `style`/`explode`/`allowReserved` the property is content-encoded by `contentType`, whose default for objects is `application/json`, and 3.0.4 RECOMMENDS this to match 3.1. **Action:** JSON-encode object properties unless the Encoding Object sets a style field, for every patch.

**X5. Header percent-encoding.** 3.0 and 3.1.0/3.1.1 define header serialization as RFC 6570 `simple`, which percent-encodes; 3.0.4/3.1.1 App D only advise against `schema` for non-URL-safe header values. 3.1.2 and 3.2 say URI percent-encoding MUST NOT be applied and add that "percent-encoding was never intended to apply to headers". **Action:** never percent-encode header values in any edition; validate them as field values (G24).

**X6. `form` style for cookies versus RFC 6265.** 3.0 and 3.1 default cookies to `form` with `explode: true`, which by RFC 6570 produces `?R=100&G=200`. RFC 6265 §4.2.1 requires `name=value` pairs joined by `; `. OAS's App D concedes `form` is "always incorrect" for multiple values, and 3.0.4 leaves the `?` implementation-defined. **Action:** emit pairs without `?`, joined by `; ` (G25).

**X7. Request bodies on GET, HEAD, DELETE.** 3.0 says the declaration SHALL be ignored where RFC 7231 §4.3.1 gives no body semantics; 3.1 reads the same RFC and permits bodies; 3.2 cites RFC 9110 §9.3, which says a client SHOULD NOT send such content unless the origin server indicated support in or out of band. A declared `requestBody` is exactly such an indication. **Action:** 3.0 ignores the declaration as its text requires; 2.0, 3.1 and 3.2 send declared bodies (G31).

**X8. Form-urlencoded has a different authority in every edition.** HTML 4.01 §17.13.4 (2.0) encodes every non-alphanumeric; RFC 1866 via RFC 1738 (3.0, 3.1) leaves `-._~` style characters; WHATWG (3.2) encodes `~` but not `*`. 3.1.2 additionally says query strings MUST parse per RFC 1866. **Action:** one WHATWG encoder; every one of these decoders accepts it (G29).

**X9. 3.2's cookie example violates RFC 6265.** 3.2 §4.12.8 shows `greeting=Hello%2C world!; code=42`; the space is not a `cookie-octet` (RFC 6265 §4.1.1). **Action:** do not validate beyond framing (G25); the caller owns escaping for `style: cookie`.

**X10. Part headers in `multipart/form-data` versus RFC 7578.** 3.x Encoding examples attach headers such as `X-Rate-Limit-Limit` to `multipart/form-data` parts (3.0.4 §4.7.15.3.2, 3.1.2 §4.8.15.3.2, 3.2 §4.15.4.4). RFC 7578 §4.8 says form-data parts MUST NOT include headers other than `Content-Type`, `Content-Disposition` and (limited) `Content-Transfer-Encoding`. OAS itself notes "significant restrictions". **Action:** send only caller-supplied values for declared part headers, and document the RFC 7578 limit (G30).

**X11. 2.0 files over urlencoded.** 2.0 §6.4.9 lets a `file` parameter's `consumes` be multipart, urlencoded "or both". HTML 4.01 §17.13.4 says multipart "should be used for submitting forms that contain files". **Action:** send files only as multipart; if urlencoded is the only declared form type, a file input is an error.

**X12. `Content-Transfer-Encoding`.** 3.0 says `format: byte` in multipart is equivalent to a `Content-Transfer-Encoding: base64` requirement, and 3.1/3.2 say the same of `contentEncoding`. RFC 7578 §4.7 says senders SHOULD NOT generate `Content-Transfer-Encoding` in HTTP. **Action:** never send it (G30).

**X13. A typo in `allowReserved`.** 3.0.4, 3.1.1 and 3.1.2 list the form-urlencoded special characters as `-`, `&`, `+`; App E and 3.2 §4.12.4 give `=`, `&`, `+`. **Action:** treat `=` as the special character.

**X14. 3.0.4 describes 3.1 keywords.** 3.0.4 §4.7.15.3 discusses `contentMediaType` and `contentEncoding: identity`, which do not exist in 3.0's schema dialect. **Action:** ignore those keywords in 3.0 documents.

**X15. Editorial errors in early 3.0 patches.** 3.0.0 to 3.0.2 call `application/octet-stream` "functionally equivalent to `*/*`" (it is a concrete type, not a range, RFC 9110 §12.5.1); 3.0.0 to 3.0.3 use an undefined `format: base64`. **Action:** octet-stream is concrete; `format: base64` is an unknown format (treated as its `type`, 3.x "MAY default back to the type alone").

**X16. "YAML's JSON schema ruleset" versus YAML 1.2.** 3.0 and 3.1 say tags MUST be limited to that ruleset. Under YAML 1.2.2 §10.2.2, a plain scalar that matches none of the JSON schema's patterns is an error, so taken literally every unquoted English `description` would be invalid. The rule's evident intent is "only JSON-representable types". 3.2 replaced it with RFC 9512 §3.4 and notes the old wording let JSON-incompatible values in. **Action:** parse with the Core schema and reject only non-JSON results (G2).

**X17. `deepObject` and `explode`.** 3.0.4 and 3.1.x say `explode: false` is the default for `deepObject` but the combination is undefined; 3.2 says `explode` has no effect for `deepObject`. **Action:** always exploded in every edition.

**X18. Required-header quoting and `Set-Cookie`.** Not a conflict, but note: RFC 9110 §5.3 forbids repeating field lines except for list-based fields, and `Set-Cookie` is the documented exception that 3.2 §4.21.3 models line by line. **Action:** expose raw multi-value headers without joining.

**X19. HTTP citations.** 2.0 cites RFC 7231; 3.0 and 3.1 cite RFC 7230, 7231 and 7235; 3.2 cites RFC 9110, which obsoletes them. No behavioural conflict was found. 3.2 cites QUERY draft -08; the cached draft is -11 with the same method semantics. **Action:** implement RFC 9110 and the current QUERY draft.

**X20. `readOnly` strength.** 2.0 MUST NOT send, 3.0 SHOULD NOT send, 3.1/3.2 an annotation that servers MAY ignore. **Action:** the client neither strips nor rejects (G33).

**X21. Default charset of text.** RFC 2046 §4.1.2 makes US-ASCII the default for `text/plain`; RFC 6838 §4.2.1 says relying on that default is "no longer permitted" for new registrations and UTF-8 SHOULD be the default; 3.2 §4.19.2.3 presumes UTF-8. RFC 8259 §8.1 requires UTF-8 JSON while RFC 6839 §3.1 (citing RFC 4627) allows UTF-16/32 for `+json`. **Action:** decode charset-less text and all JSON as UTF-8 (UTF-8 is a superset of US-ASCII, so this is safe for conforming senders).

**X22. Relative servers and `$self`.** RFC 3986 §5.1.1 gives a base embedded in content the highest precedence; 3.2 §4.5.2 deliberately ignores `$self` for API URLs ("RFC3986's base URI rules for the OpenAPI document do not apply") and uses the retrieval URI. **Action:** follow 3.2: documents resolve against `$self`, servers against the retrieval URI.

**X23. `default`-only Responses Objects.** 3.x says a Responses Object "MUST contain at least one response code"; `default` is not a code, yet default-only objects are common and 3.2's own `default` text invites them. **Action:** accept a default-only Responses Object.

**X24. SSE `data` is required in OAS's generic schema but optional in the stream format.** 3.2 §4.14.4's schema has `required: [data]`; WHATWG allows events without `data` (browsers then do not dispatch). **Action:** items omit `data` when the event had none; events with no fields are skipped (G46).

**X25. Server variable default wording.** 3.0.0 says the default "MUST be provided by the consumer"; 3.0.3 onward says it "SHALL be sent if an alternate value is not supplied". Same practical meaning. **Action:** use the default when the caller supplies nothing.

---

## Appendix A. Patch-level changes a client must know

Only changes that affect client behaviour are listed. In every row the client follows the later text for all patches of the line.

| Line | Topic | Earlier text | Later text |
|---|---|---|---|
| 3.0 | Style table | 3.0.0 to 3.0.3: `empty` column; label non-exploded `.a.b.c`; delimiters unencoded; form without `?` | 3.0.4: `undefined` column; RFC 6570 spellings; delimiters percent-encoded; `?` shown (stripped for bodies) |
| 3.0 | `simple` style types | 3.0.0: array only | 3.0.4: primitive, array, object |
| 3.0 | `allowReserved` | 3.0.0 to 3.0.3: reserved chars "SHOULD" be allowed unencoded | 3.0.4: RFC 6570 reserved expansion; caller still encodes disallowed chars |
| 3.0 | Encoding for urlencoded objects | 3.0.0 to 3.0.3: "form" style strategy | 3.0.4: RECOMMENDED: `contentType` unless a style field is present |
| 3.0 | GET/HEAD/DELETE bodies | 3.0.0 to 3.0.3: "where the HTTP spec is vague" | 3.0.4: names GET, HEAD, DELETE |
| 3.0 | Optional security | 3.0.0 to 3.0.2: no `{}` statement | 3.0.3+: `{}` makes security optional; 3.0.4: indicates anonymous access |
| 3.0 | Server variables | 3.0.0: no enum rule, default "MUST be provided by the consumer" | 3.0.3+: default SHALL be sent if none supplied and SHOULD be in the enum; enum SHOULD NOT be empty |
| 3.0 | Implicit connections, undefined behaviour, security considerations | absent | 3.0.4: added (R8, D11, D15) |
| 3.0 | `application/octet-stream` as `*/*`; `format: base64` | 3.0.0 to 3.0.2 / 3.0.3 | removed |
| 3.1 | Encoding default `contentType` | 3.1.0: octet-stream for all non-object, non-array | 3.1.1: `type`/`contentEncoding` table (text/plain for primitives) |
| 3.1 | Encoding for urlencoded objects | 3.1.0: "form" style strategy | 3.1.1: `contentType` unless a style field is present; style fields also for `multipart/form-data` |
| 3.1 | Style table | 3.1.0: as 3.0.0 | 3.1.1: corrected with `?`; 3.1.2: without `?` |
| 3.1 | Header serialization | 3.1.0/3.1.1: RFC 6570 `simple`; 3.1.1 App D advice | 3.1.2: MUST NOT percent-encode; MUST pass through unchanged |
| 3.1 | Server URL | 3.1.0/3.1.1: silent on query and fragment | 3.1.2: MUST NOT contain them |
| 3.1 | URL parsing | 3.1.0/3.1.1: silent | 3.1.2: API URLs MUST parse per RFC 3986; form query strings per RFC 1866 |
| 3.1 | Complete-document parsing | 3.1.0: brief | 3.1.1: fragment parsing undefined; parse whole documents |
| 3.1 | Relative references | 3.1.0: base per RFC 3986 §5.1 | 3.1.1: §5.1.2 to §5.1.4, retrieval URI may be user-supplied |
| 3.1 | Request body `content` | silent | 3.1.2: SHOULD have at least one entry |
