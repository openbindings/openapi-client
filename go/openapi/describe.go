package openapi

import "encoding/json"

// Operations describes every operation of the document, in document order:
// paths as listed, within a path the methods in the order get, put, post,
// delete, options, head, patch, trace, query, then additional operations
// as listed. Operations that cannot be called are included, with Err set.
// So are two defects that are not operations: a Paths entry whose $ref
// cannot be read, listed once with its Path and Err, and an OpenAPI 3.2
// additional operation spelled like a fixed method, which OpenAPI forbids;
// each has an empty Key. Webhooks and callbacks describe requests the
// server sends and are not operations of the Client.
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
// method-and-path key always reaches the fixed method, never an additional
// operation spelled like it.
func (c *Client) Operation(key string) (*Operation, error) {
	panic("unimplemented")
}

// An Operation describes one operation, in the same terms for every
// edition: Swagger 2.0 body and formData parameters appear as Body, its
// host, basePath and schemes as Servers, and its securityDefinitions in
// Security. Inherited declarations (path-level parameters, root servers
// and security) are already applied.
//
// The descriptions follow references. In OpenAPI 3.1 and 3.2, where a
// parameter, request body, response, media type or security scheme is a
// Reference Object that gives a summary or description, that text replaces
// the target's, while Source still names the target; in Swagger 2.0 and
// OpenAPI 3.0, a reference's siblings are ignored.
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
	// merged, in document order. It holds no Swagger 2.0 body or formData
	// parameter (see Body), and no header parameter that OpenAPI 3.x tells
	// clients to ignore (Accept, Content-Type, Authorization). A Swagger 2.0
	// Content-Type header parameter is listed, but giving it a value is
	// refused, since Input.MediaType chooses the media type; a Swagger 2.0
	// Accept parameter's value is sent as the Accept field.
	Params []*Param

	// Body describes the request body, or is nil when the operation takes
	// none, as for an OpenAPI 3.0 GET, HEAD or DELETE. In Swagger 2.0 it
	// comes from the body parameter, or from the formData parameters.
	Body *Message

	// Responses lists the declared responses, in document order.
	Responses []*Message

	// Servers lists the effective servers, in document order. In Swagger
	// 2.0 there is one for each declared scheme, with the URL
	// scheme://host/basePath.
	Servers []*Server

	// Security lists the effective security alternatives, in document
	// order: one of them must be satisfied. An empty Security means the
	// operation takes no credentials.
	Security []SecurityRequirement

	// Source is where the operation is written: the absolute URI of its
	// document, with a JSON Pointer to its Operation Object as the fragment,
	// as in "https://api.example.com/openapi.yaml#/paths/~1pets/get". See
	// Client.Document.
	Source string

	// Err is why the operation cannot be called, or nil. It is set by a
	// defect without which no request can be built: an unresolvable
	// reference, a malformed required parameter, a path template that does
	// not match the path parameters. Calling the operation then returns a
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
	// writes it, which matters for deepObject: OpenAPI defaults explode to
	// false for it, a combination it does not define.
	Explode    bool
	ExplodeSet bool

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
	// its type, format, items, enum and bounds. It is nil when none is
	// declared, and then any value is accepted.
	Schema *Schema

	// Headers lists the header fields a multipart field's Encoding declares
	// for its part.
	Headers []*Param

	// Source is where the value is declared: the absolute URI of its
	// document, with a JSON Pointer to its Parameter, Header or Encoding
	// Object as the fragment. See Client.Document.
	Source string

	// Err is why the value cannot be used, or nil. An optional parameter
	// with Err set cannot be supplied; a required one disables the
	// operation.
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

	// Headers lists a response's declared header fields.
	Headers []*Param

	// Media lists the declared media types, in document order. It is empty
	// for a response that declares no content. In Swagger 2.0 it pairs the
	// schema with each consumes or produces entry, formData with the form
	// types among them; where the operation declares none, it holds one
	// Media with an empty Type. Swagger 2.0 formData whose consumes names no
	// form type is described by one Media of the type the call would send
	// (multipart/form-data when a field is of type file, otherwise
	// application/x-www-form-urlencoded), which Input.MediaType may pin.
	Media []*Media

	// Source is where the request body or response is declared: the
	// absolute URI of its document, with a JSON Pointer as the fragment.
	// See Client.Document.
	Source string

	// Err is why the request body or response cannot be used, or nil, as
	// for a response whose key is not a status code, a range such as "4XX",
	// or "default".
	Err error
}

// A Media describes one media type of a request body or response.
type Media struct {
	// Type is the media type or range as declared, such as
	// "application/json" or "image/*". It is empty where a Swagger 2.0
	// operation declares no consumes or produces; such a Media matches any
	// type, as */* would, and a body sent under it takes the defaults the
	// package documentation gives for that case, under Choices.
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

	// Err is why this media type cannot be used, or nil.
	Err error
}

// A Server describes one server an operation may be sent to.
type Server struct {
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
	// document, with a JSON Pointer as the fragment, and the base a
	// relative URL resolves against. Empty for a Swagger 2.0 server, which
	// the client assembles from host, basePath and schemes.
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

	// Enum lists the permitted values, or is nil when any value is
	// permitted.
	Enum []string
}

// A SecurityRequirement describes one security alternative of an
// operation: a Security Requirement Object, every scheme of which must be
// satisfied.
type SecurityRequirement struct {
	// Key names the alternative in Input.Security, Response.Security and a
	// SecurityChoice's Offered and Value. It is the Security Requirement
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

	// Deprecated reports an OpenAPI 3.2 scheme marked deprecated, which the
	// default security choice ranks low.
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
// never runs flows; a caller that signs users in uses these URLs.
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

// A Schema is a Schema Object of the document. It is a lazy handle: nothing
// about it is computed until it is used, so describing operations costs no
// schema work. It offers the schema two ways: as written, through Raw,
// Source, Base and Dialect, and as JSON Schema 2020-12, through MarshalJSON
// and Defs.
//
// A handle is the schema where it is used. In OpenAPI 3.1 and 3.2, a
// schema that is a $ref with siblings, such as a description, keeps them:
// the handle is that site, and the schema it references is among its Defs.
// In Swagger 2.0 and OpenAPI 3.0, whose references ignore their siblings, a
// reference is followed, and the handle is its target.
type Schema struct {
	loc string
}

// MarshalJSON renders the schema as JSON Schema 2020-12, translated from
// the document's dialect, with no $schema, $defs, $id, $anchor or
// $dynamicAnchor at any depth, so it can be placed anywhere inside another
// schema without changing what its references mean. readOnly, writeOnly and
// required are kept as written; in OpenAPI 3.0 a required readOnly
// property is required only in responses, so a caller building request
// input leaves readOnly properties out. OpenAPI 3.0 nullable
// becomes a type list, and a boolean exclusiveMinimum a number. Binary
// content, however each edition writes it (Swagger 2.0 type file, OpenAPI
// 3.0 format binary, a 3.1 schema with contentMediaType or with no type
// for raw content), becomes {"contentMediaType": T}, where T is the type
// the document gives, else application/octet-stream; base64 content
// (format byte) becomes {"type": "string", "contentEncoding": "base64"}.
// So one check, for contentMediaType, finds file inputs in every edition.
//
// Each reference to another schema becomes "#/$defs/KEY". KEY is made from
// the component's name, or, where two schemas would share that, from its
// document and JSON Pointer; every byte other than a letter, a digit, "."
// or "-" is written as "_" and two uppercase hex digits. So KEY needs no
// escaping in a JSON Pointer or a URI fragment, different names give
// different KEYs, and a KEY is the same in every Schema of one Client. Defs
// holds the schemas behind those references. Marshaling fails, naming the
// reference, when a reference cannot be resolved, or uses $dynamicRef.
func (s *Schema) MarshalJSON() ([]byte, error) {
	panic("unimplemented")
}

// Defs returns every schema that s references, directly or through other
// schemas, by the KEY its references use, or nil for a nil Schema. The map
// is new on each call. Because keys are stable across one Client, the Defs
// of several schemas merge into one $defs without conflict.
func (s *Schema) Defs() map[string]*Schema {
	panic("unimplemented")
}

// Raw returns a copy of the Schema Object exactly as written, as JSON (from
// YAML if need be), its references unchanged: they resolve against Base.
// For a Swagger 2.0 parameter, it holds the parameter's schema keywords
// (type, format, items, enum and bounds).
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
// it.
func (s *Schema) Base() string {
	panic("unimplemented")
}

// Dialect is the JSON Schema dialect the schema is written in, in OpenAPI
// 3.1 and 3.2: the URI its $schema, or its document's jsonSchemaDialect,
// names, or else OpenAPI's own base dialect. It is empty in Swagger 2.0 and
// OpenAPI 3.0, whose schemas are those editions' own subset of JSON Schema
// (see Client.Version).
func (s *Schema) Dialect() string {
	panic("unimplemented")
}
