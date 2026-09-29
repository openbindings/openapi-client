package openapi

import "encoding/json"

// Operations describes every operation of the document, in document order:
// paths as listed, within a path the methods in the order get, put, post,
// delete, options, head, patch, trace, query, then additional operations
// as listed. Operations that cannot be called are included, with Err set.
// So are two defects that are not operations: a Paths entry whose $ref
// cannot be read, listed once with its Path and Err, and an OpenAPI 3.2
// additional operation whose method is exactly GET, PUT, POST, DELETE,
// OPTIONS, HEAD, PATCH, TRACE or QUERY, which OpenAPI forbids; each has an
// empty Key. Methods are compared exactly, so an additional operation
// "Post" is an ordinary one, "Post /path". Webhooks and callbacks describe
// requests the server sends and are not operations of the Client.
//
// The slice is new on each call; the Operations it points to are shared by
// every caller and must not be modified. Listing them does no schema work.
func (c *Client) Operations() []*Operation {
	panic("unimplemented")
}

// Operation describes the operation named key, by the rules Call uses. When
// no operation has that key, or several share it as their operationId, it
// returns an error wrapping ErrNoOperation that names the candidates; each
// is still reached by its Operation.Key. An operation that exists but
// cannot be called is returned with Err set, and so is the entry for a
// Paths entry that cannot be read, for a key with any method and that path;
// calling with the same key is refused with an error wrapping that Err. A
// method-and-path key always reaches the fixed method, never the forbidden
// additional operation of the same method (see Client.Operations).
func (c *Client) Operation(key string) (*Operation, error) {
	panic("unimplemented")
}

// An Operation describes one operation, in the same terms for every
// edition: Swagger 2.0 body and formData parameters appear as Body, its
// host, basePath and schemes as Servers, and its securityDefinitions in
// Security. Inherited declarations (path-level parameters, the entry
// document's root servers, and root security) are already applied.
//
// The descriptions follow references. A Path Item's $ref and the Path
// Item's own fields are read together: a field on one side only is used,
// and a field on both sides, which OpenAPI leaves undefined, sets Err on
// each operation whose request it affects (that method's operation, or
// every operation that uses the parameters or servers), except that the
// Path Item's own summary and description win. In OpenAPI 3.1 and 3.2,
// where a parameter, request body, response, media type, header or
// security scheme is a Reference Object that gives a description, that
// description replaces the target's, the Reference Object nearest the use
// site winning, while Source still names the target; in Swagger 2.0 and
// OpenAPI 3.0, a Reference Object's siblings are ignored.
type Operation struct {
	// Key addresses this operation in Call, Stream, Prepare and
	// Client.Operation: its operationId when that names this operation
	// alone and is not itself of the method-and-path form, else its Method
	// and Path, as in "GET /pets/{petId}". It is empty for an entry listed
	// only to report a defect (see Client.Operations).
	Key string

	ID string // the operationId, or empty

	// Method is the method as sent: a fixed method in uppercase ("GET"),
	// or an OpenAPI 3.2 additional operation's method exactly as written.
	Method string

	Path        string // the Paths key, as written
	Summary     string
	Description string
	Tags        []string
	Deprecated  bool

	// Params lists the parameters, the path item's and the operation's
	// merged, in the order the package documentation gives. It holds no
	// Swagger 2.0 body or formData parameter (see Body), and no header
	// parameter that OpenAPI 3.x tells clients to ignore (Accept,
	// Content-Type, Authorization, matched without regard to case). A
	// Swagger 2.0 Content-Type header parameter is listed; the package
	// documentation, under Header fields, says how it is satisfied. A
	// Swagger 2.0 Accept parameter's value is sent as the Accept field.
	Params []*Param

	// Body describes the request body, or is nil when the operation takes
	// none, as for an OpenAPI 3.0 GET (see Bodies by method in the package
	// documentation). In Swagger 2.0 it comes from the body parameter, or
	// from the formData parameters.
	Body *Message

	// Responses lists the declared responses, in document order.
	Responses []*Message

	// Servers lists the effective servers, in document order. In Swagger
	// 2.0 there is one for each declared scheme, with the URL
	// scheme://host/basePath.
	Servers []*Server

	// Security lists the effective security alternatives, in document
	// order: one of them must be satisfied. An empty Security means the
	// operation declares no requirement; the client adds no credentials.
	Security []SecurityRequirement

	// Source is where the operation is written: the absolute URI of its
	// document, with a JSON Pointer to its Operation Object as the fragment,
	// as in "https://api.example.com/openapi.yaml#/paths/~1pets/get". See
	// Client.Document.
	Source string

	// Err is why the operation cannot be called, or nil. It is set by a
	// defect without which no request can be built: an unresolvable
	// operation or path reference; an unresolvable parameter or request
	// body reference, whose identity and requiredness cannot be known, and
	// whose part's Source is the reference's own location; a Path Item
	// field that both the Path Item and its $ref target define (see
	// Operation); or a path template that does not match any knowable
	// parameter key. A required parameter with a known Key but
	// unsupported serialization has its own Param.Err, which Input.ParamWriters
	// may bypass. Calling an operation with Err set returns a
	// *RequestError wrapping Err. A defect in an optional part is reported
	// on that part instead, and fails a call only when the call uses it.
	// Wherever an Err's cause is a reference that could not be resolved, it
	// wraps [ErrUnresolved], and the retrieval error when retrieving a
	// document failed.
	Err error
}

// A Param describes a value serialized into a request or response: a
// parameter of an operation; a header of a response (In is "header", Key
// is empty); or a field of a form or multipart body as its Encoding
// declares it, or a Swagger 2.0 formData parameter (In and Key are empty).
type Param struct {
	// Key is the parameter's key in Input.Params: its Name, or its In and
	// Name joined by a dot where the name alone would be ambiguous (see
	// Input.Params). No two parameters of an operation share a Key.
	Key string

	Name string
	In   string // "path", "query", "header", "cookie", or "querystring" (3.2)

	// Description describes the value. It is not part of Schema: a tool
	// that builds a schema for the value adds it there.
	Description string

	Required   bool
	Deprecated bool

	// Style is the RFC 6570 style the value is serialized with, as declared
	// or as OpenAPI defaults it for the location ("form" for query and
	// cookie, "simple" for path and header), or empty when the value is
	// serialized by ContentType instead, and in Swagger 2.0.
	Style string

	// Explode is the effective explode, and ExplodeSet whether the document
	// writes it.
	Explode    bool
	ExplodeSet bool

	// AllowReserved is the effective allowReserved: false where the edition
	// ignores it (see Percent-encoding in the package documentation).
	AllowReserved   bool
	AllowEmptyValue bool

	// ContentType is the media type the value is serialized with when it is
	// described by content rather than by schema and style, or, for a form
	// or multipart field, its Encoding contentType, which may be a
	// comma-separated list or a range.
	ContentType string

	// CollectionFormat is, in Swagger 2.0, the collectionFormat of an array
	// parameter or formData field: "csv" (its default), "ssv", "tsv",
	// "pipes" or "multi".
	CollectionFormat string

	// Schema is the value's schema: the declared schema, the schema of the
	// single content entry, or, for a Swagger 2.0 parameter, one made from
	// every schema field it declares (type, format, items, default, enum,
	// bounds, multipleOf, pattern and uniqueItems). It is nil when none is
	// declared, and then any value is accepted.
	Schema *Schema

	// Headers lists the header fields a multipart field's Encoding declares
	// for its part.
	Headers []*Param

	// Source is where the value is declared: the absolute URI of its
	// document, with a JSON Pointer to its Parameter, Header or Encoding
	// Object as the fragment. See Client.Document.
	Source string

	// Err is why built-in serialization cannot use the value, or nil. A
	// parameter with Err set can be supplied by Input.ParamWriters when
	// its Key is known; otherwise a required one refuses the call. A
	// pre-encoded body is never checked against a field's Err (see
	// Input.Body).
	Err error
}

// A Message describes the request body of an operation, or one of its
// declared responses.
type Message struct {
	// Key is a response's key in the operation's responses, which
	// Response.Declared reports: a code such as "404", a range such as
	// "4XX", or "default". It is empty for a request body.
	Key string

	Description string

	// Required reports that a call must send the request body.
	Required bool

	// Headers lists a response's declared header fields, without the
	// Content-Type that OpenAPI 3.x ignores.
	Headers []*Param

	// Media lists the declared media types, in document order. It is empty
	// for a response that declares no content, or a 3.x requestBody with an
	// empty content map. In the latter case a raw body with explicit
	// Input.MediaType can still be sent, but no structured encoder governs it.
	// In Swagger 2.0, the schema of a body parameter, or of a response that
	// has one, is paired with each consumes or produces entry, and formData
	// with the form types among them; where the operation declares none,
	// one Media with an empty Type holds it. Swagger 2.0 formData whose
	// consumes names no form type also has an empty Type; Input.MediaType
	// selects a concrete form type for the call.
	Media []*Media

	// Source is where the request body or response is declared: the
	// absolute URI of its document, with a JSON Pointer as the fragment.
	// See Client.Document.
	Source string

	// Err is why the request body or response cannot be used, or nil, as
	// for a response whose key is not a status code, a range such as "4XX",
	// or "default". An empty requestBody.content is not an Err: a caller
	// may send a raw body with an explicit Input.MediaType.
	Err error
}

// A Media describes one media type of a request body or response.
type Media struct {
	// Type is the media type or range as declared, such as
	// "application/json" or "image/*". It is empty where a Swagger 2.0
	// operation declares no consumes or produces; such a Media matches any
	// type, as */* would. A body sent under it requires a concrete
	// Input.MediaType.
	Type string

	// Schema is the content's schema, or nil when none is declared. For
	// Swagger 2.0 formData, it is an object whose properties are the
	// fields and whose required list names the required ones.
	Schema *Schema

	// ItemSchema is, in OpenAPI 3.2, the schema of each item of sequential
	// content, or nil.
	ItemSchema *Schema

	// Encoding describes the fields of form or multipart content, in
	// document order, as Params whose Name is the field: each property
	// that declares an Encoding Object, and in Swagger 2.0 every formData
	// parameter. For a positional multipart type in OpenAPI 3.2, it
	// describes the parts: those of prefixEncoding in order, named "0", "1"
	// and so on, then that of itemEncoding, named "*".
	Encoding []*Param

	// Source is where the media type is declared: the absolute URI of its
	// document, with a JSON Pointer to its Media Type Object as the
	// fragment. See Client.Document.
	Source string

	// Err is why the media type declaration itself cannot govern a
	// structured body, such as an invalid media key or an unreadable Media
	// Type reference. A schema or Encoding defect that affects structured
	// value encoding does not set Media.Err: it is reported by
	// Schema.References or the relevant Encoding Param.Err. A pre-encoded
	// []byte or io.Reader body is checked against neither (see Input.Body).
	Err error
}

// A Server describes one server an operation may be sent to.
type Server struct {
	// ID is an opaque identifier unique among distinct server declarations
	// in one Client, including entries with the same URL and name. An
	// inherited declaration keeps its ID across operations. It is stable
	// for the same loaded document and retained by derived Clients. Use it
	// with Options.ServerID; do not compose it yourself.
	ID string

	// URL is the server URL as written, with its {variables}. It may be
	// relative. Options.Server selects the server by it.
	URL string

	// Name is the OpenAPI 3.2 server name, or empty. Options.Server selects
	// the server by it too.
	Name        string
	Description string

	// Variables lists the variables in URL, in the order they appear.
	Variables []Variable

	// Source is where the Server Object is written: the absolute URI of its
	// document, with a JSON Pointer as the fragment. Empty for a Swagger 2.0
	// server, which the client assembles from host, basePath and schemes.
	// The package documentation, under URL, says what a relative URL
	// resolves against.
	Source string

	// Err is why the server cannot be used, or nil.
	Err error
}

// A Variable describes one variable of a server URL.
type Variable struct {
	Name        string
	Description string

	// Declared reports that the server declares the variable; a {name} in
	// the URL that it does not declare only needs a value in
	// Options.Variables.
	Declared bool

	// Default is the declared default, and DefaultSet whether one is
	// declared. A declared variable without one makes the server unusable
	// until Options.Variables gives it a value.
	Default    string
	DefaultSet bool

	// Enum lists the declared enum, or is nil when none is declared. What
	// it permits is on Options.Variables.
	Enum []string
}

// A SecurityRequirement describes one security alternative of an
// operation: a Security Requirement Object, every scheme of which must be
// satisfied.
type SecurityRequirement struct {
	// Key names the alternative in Input.Security and Response.Security.
	// It is the Security Requirement
	// Object written as canonical JSON, with no whitespace:
	//
	//   - An object with one member for each scheme, in increasing order of
	//     scheme name, comparing the bytes of their UTF-8 encoding (the
	//     order sort.Strings gives).
	//   - Each member's value is an array of the scopes, or roles, that the
	//     alternative lists for the scheme, sorted the same way, with
	//     duplicates removed; [] when it lists none.
	//   - Each name and scope is a JSON string escaped as RFC 8785 escapes
	//     strings: " and \ as \" and \\; U+0008, U+0009, U+000A, U+000C and
	//     U+000D as \b, \t, \n, \f and \r; any other character below U+0020
	//     as \u00xx, with lowercase hex digits; every other character as
	//     itself.
	//   - The anonymous alternative is {}.
	//
	// For example:
	//
	//	{"api_key":[]}
	//	{"oauth":["pets:read","pets:write"]}
	//	{"api_key":[],"oauth":["pets:read"]}
	//	{}
	//
	// The key does not depend on the order in which the document writes
	// schemes or scopes, so it survives a reordering, and it tells apart
	// alternatives that name the same schemes with different scopes.
	// Alternatives with the same key are the same requirement, and the key
	// selects the first. Take keys from here rather than composing them.
	Key string

	// Schemes lists the alternative's schemes, in document order, each with
	// the scopes or roles the alternative requires of it. It is empty for
	// the anonymous alternative {}.
	Schemes []SecurityScheme
}

// A SecurityScheme describes a security scheme as one alternative of an
// operation uses it: the scheme's declaration, and the scopes or roles the
// alternative requires.
type SecurityScheme struct {
	// Name is the scheme's name as the requirement writes it, the key for
	// Options.Credentials: a component name or, in OpenAPI 3.2, a URI.
	Name string

	// Scopes lists the OAuth 2.0 or OpenID Connect scopes, or the roles,
	// that this alternative requires. The client never checks them.
	Scopes []string

	// Type is "apiKey", "http", "mutualTLS", "oauth2" or "openIdConnect". A
	// Swagger 2.0 basic scheme is "http" with Scheme "basic".
	Type        string
	Description string

	// In and ParamName say where an apiKey is sent: "header", "query" or
	// "cookie", under that name.
	In        string
	ParamName string

	// Scheme is, for Type "http", the authentication scheme as written, such
	// as "basic" or "bearer", and BearerFormat its bearerFormat hint.
	Scheme       string
	BearerFormat string

	// Flows lists, for Type "oauth2", the declared flows.
	Flows []Flow

	// OpenIDConnectURL is, for Type "openIdConnect", the discovery URL.
	OpenIDConnectURL string

	// OAuth2MetadataURL is, for Type "oauth2" in OpenAPI 3.2, the
	// authorization server metadata URL (RFC 8414).
	OAuth2MetadataURL string

	// Deprecated reports an OpenAPI 3.2 scheme marked deprecated.
	Deprecated bool

	// Source is where the scheme is declared: the absolute URI of its
	// document, with a JSON Pointer to its Security Scheme Object as the
	// fragment. It tells which document a scheme name was found in. It is
	// empty for a scheme the document never declares.
	Source string

	// Err is why the scheme cannot be used, or nil: a defective or missing
	// declaration. Alternatives that use it can be applied only when
	// FromTransport satisfies it.
	Err error
}

// A Flow describes one OAuth 2.0 flow of a security scheme. The client
// never runs flows; a caller that signs users in uses these URLs. They, and
// a SecurityScheme's OpenIDConnectURL and OAuth2MetadataURL, are as
// written; a relative one resolves against the URL of the server the call
// uses.
type Flow struct {
	// Type is "implicit", "password", "clientCredentials",
	// "authorizationCode" or, in OpenAPI 3.2, "deviceAuthorization". Swagger
	// 2.0's application and accessCode are clientCredentials and
	// authorizationCode.
	Type string

	AuthorizationURL       string
	TokenURL               string
	RefreshURL             string
	DeviceAuthorizationURL string

	// Scopes maps each scope the flow offers to its description.
	Scopes map[string]string
}

// A Schema is a lazy handle to one Schema Object. Describing operations does
// no schema work. Raw, Source, Base, Dialect and References expose its authored
// meaning and resolved references. A caller may build its own schema view
// from this authored graph without changing how an operation is called.
//
// A handle is the schema where it is used. In OpenAPI 3.1 and 3.2 it stays
// at that site, whether or not a $ref there has siblings. In Swagger 2.0
// and OpenAPI 3.0, whose references ignore their siblings, it follows $ref
// to a schema that is not only a $ref; when it cannot, it stays at the
// site, and References reports the Err. Source identifies the resulting
// handle in either case.
//
// Identifiers and references are read as JSON Schema 2020-12 and OpenAPI's
// dialects define them. In a schema resource that declares another
// dialect, identifiers are not interpreted, Base is the base outside the
// resource, and References returns an error.
type Schema struct {
	loc string
}

// Schema returns the Schema Object identified by an absolute URI in the
// loaded graph, including a JSON Pointer, $anchor or $id resource URI. It
// never fetches a document. A URI outside the loaded graph, one claimed by
// multiple distinct schemas, or one whose target is not a Schema Object,
// returns an error rather than selecting one. This lets generators
// follow authored references without reimplementing document retrieval and
// URI resolution. It is safe to call concurrently.
func (c *Client) Schema(uri string) (*Schema, error) {
	panic("unimplemented")
}

// A SchemaReference is one standard $ref or $dynamicRef in a Schema's Raw
// tree. A custom dialect can have additional reference keywords; the client
// does not interpret those. Raw preserves them for the dialect-aware caller.
type SchemaReference struct {
	// At is a JSON Pointer from the root of Schema.Raw to the reference
	// keyword. It distinguishes multiple references in the same schema.
	At string

	// Keyword is "$ref" or "$dynamicRef"; Value is its authored string.
	Keyword string
	Value   string

	// URI is Value resolved against the base effective at At, including
	// nested $id resources. It can be passed to Client.Schema.
	URI string

	// Target is the resolved static target. For $dynamicRef it is only the
	// lexical fallback; the final target depends on the dynamic scope of a
	// validator and cannot be elected from one document node alone. It is
	// nil when Err is set.
	Target *Schema
	Err    error
}

// References lists standard schema references in Raw, in document order.
// It follows schema-bearing keywords of the declared dialect, accounting
// for nested $id bases, but does not traverse a reference's target; each
// target is its own Schema. The slice is new. A schema in another dialect
// (see Schema) returns an error rather than silently omitting references in
// its unknown vocabulary. The caller can always inspect Raw and the loaded
// documents.
func (s *Schema) References() ([]SchemaReference, error) {
	panic("unimplemented")
}

// Raw returns a copy of the Schema Object exactly as written, as JSON (from
// YAML if need be, as the Loader converts it), its references unchanged.
// References at its root resolve
// against Base; a nested $id may change the base within the tree. References
// reports each standard reference's resolved URI.
// For a Swagger 2.0 parameter, it holds the parameter's schema fields (see
// Param.Schema).
func (s *Schema) Raw() json.RawMessage {
	panic("unimplemented")
}

// Source is where the schema is written: the absolute URI of its document,
// with a JSON Pointer from that document's root as the fragment, usable
// with Client.Document, under a nearer $id too. For a schema made from a
// Swagger 2.0 parameter, it points to the parameter.
func (s *Schema) Source() string {
	panic("unimplemented")
}

// Base is the base URI that the references in Raw resolve against: that of
// Source's document, or, in OpenAPI 3.1 and 3.2, the nearest $id at or
// above the schema, its own $id included, resolved against the base outside
// it (see Schema for other dialects).
func (s *Schema) Base() string {
	panic("unimplemented")
}

// Dialect is the JSON Schema dialect the schema is written in, in OpenAPI
// 3.1 and 3.2: the URI named by the $schema of the nearest schema resource
// root at or above it, or else by its document's jsonSchemaDialect, or else
// OpenAPI's default dialect, https://spec.openapis.org/oas/3.1/dialect/base
// in 3.1 and https://spec.openapis.org/oas/3.2/dialect/2025-09-17 in 3.2. It
// is empty in Swagger 2.0 and OpenAPI 3.0, whose schemas are those
// editions' own subset of JSON Schema (see Client.Version).
func (s *Schema) Dialect() string {
	panic("unimplemented")
}
