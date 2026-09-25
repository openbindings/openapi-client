// Package openapi calls the operations of an OpenAPI document with ordinary
// Go values. It reads Swagger 2.0 and OpenAPI 3.0, 3.1 and 3.2 documents,
// written in JSON or YAML, and needs no generated code. Its only authorities
// are the OpenAPI specifications and the RFCs they rely on.
//
// # The model
//
// [Load] reads a document once and returns a [Client], which is safe for
// concurrent use. [Client.Call] names an operation, gives its inputs, sends
// the request, and decodes a 2xx body into the value you pass:
//
//	c, err := openapi.Load(ctx, "https://api.example.com/openapi.json", nil)
//	...
//	var pet Pet
//	_, err = c.Call(ctx, "getPet", &openapi.Input{
//		Params: map[string]any{"petId": "p-7", "revision": 3},
//	}, &pet)
//
// An operation is named by its operationId, or by its method and Paths key:
// "GET /pets/{petId}". Parameters are given by name whatever their
// location, and the body separately.
//
// [Client.Stream] is Call with the body left open, for server-sent events,
// JSON Lines, multipart parts, and anything read as it arrives.
// [Client.Prepare] is Call without sending: it returns the exact
// [*http.Request] that will be sent, credentials aside, which may be
// inspected and changed, and a [Choice] for each decision the document left
// open. [Client.Operations] describes the document for tools.
//
// [Options] says where calls go, as whom, and how: the http.Client, the
// server, credentials, redirects, extra header fields and memory bounds.
// Load takes a Client's Options, and [Client.With] derives a Client that
// shares the loaded document and changes some of them, for a tenant or for
// one call.
//
// Everything below the request is net/http. Middleware is an
// [http.RoundTripper], which [OperationFromContext] tells which operation it
// is sending; deadlines and cancellation are the context; mutual TLS is the
// transport's TLS configuration.
//
// # Values
//
// Params values, Body, and Part contents take any Go value. The client first
// converts a value to JSON data as encoding/json would (struct tags,
// MarshalJSON, TextMarshaler map keys), then serializes that data as the
// document says. Where a parameter or a form or multipart field needs text,
// a number or boolean is written in its JSON spelling (10, 2.5, true) and a
// string or json.Number as it is. In a parameter or a form or multipart
// field, a nil value, an empty slice and an empty map are undefined and
// omitted, as RFC 6570 says, and "" is a value. A JSON body or JSON part is
// exactly what encoding/json writes, null members, [] and {} included.
// Schema defaults are never sent, and values are never validated against
// schemas. How bytes, readers and iterators are sent is on Input.Body.
//
// Where the client creates the values, as when out is a *any, a
// *map[string]any or a *[]any, and for Items[any] and StatusError.Decode
// into those, JSON numbers are kept exact as json.Number. A caller's own
// type decodes exactly as json.Unmarshal would, its any-typed fields
// included.
//
// # Outcomes
//
// A call ends in exactly one of these ways:
//
//   - Not sent: a [*RequestError]. Nothing reached the network.
//   - Transport failure: the *url.Error from the http.Client. The request
//     may have reached the server.
//   - A final status other than 2xx: a [*StatusError], holding the response
//     and a bounded copy of its body.
//   - A 2xx whose body could not be read or decoded: a [*DecodeError]. The
//     server has handled the call; do not assume it can be repeated.
//   - A 2xx, decoded: a nil error.
//
// Whenever a response arrived, the [*Response] is returned, even with an
// error. When the call's context is done before the call completes, the
// error matches ctx.Err() with errors.Is, and also context.Cause(ctx), even
// where net/http would report only the cause; so does a read of a Stream's
// Body. Test for the package's three types first: a *RequestError may wrap
// a *url.Error from a token endpoint a credential source called, and a
// *StatusError or *DecodeError may wrap the context's error when a deadline
// cut the body short. A memory bound that is hit is an
// [*http.MaxBytesError].
//
// No credential appears in the text of an error the client creates, nor in
// any *url.Error in the chain of one it returns: credentials the client
// added to a URL are redacted there. Errors made by the caller's own code,
// such as its transport or a credential source, are passed on as they are.
//
// # Choices
//
// Where the document offers several alternatives, or none, and leaves the
// pick to the client, the client picks by the default below, records the
// decision as a Choice in Request.Choices, marked Choice.Default, and
// lets the caller make it instead. A caller who wants no defaults refuses
// any Choice with Default set. There is no strict mode.
//
//   - Server, when the operation lists several: the first (in Swagger 2.0,
//     https when declared). Set Options.Server or Options.BaseURL. Kind
//     [ServerChoice].
//   - Server variable: its declared default, which OpenAPI requires and so
//     is not a choice; one without a default needs a value in
//     Options.Variables. Kind [VariableChoice].
//   - Security alternative, when several are offered: of those the caller
//     has satisfied, the first in document order from the first of these
//     groups that has one: alternatives with a scheme satisfied by a
//     credential or [FromTransport], their mutualTLS schemes counting as
//     satisfied; alternatives whose schemes are all mutualTLS; alternatives
//     that use a deprecated scheme; the anonymous {}. So a credential the
//     caller supplied is sent. Set Options.Security for the client, or
//     Input.Security for one call. Kind [SecurityChoice]; Response.Security
//     reports it.
//   - Request media type, when several are declared, or a range, or none:
//     a body holding a [Part], []byte or io.Reader field goes as the
//     declared multipart or form type, preferring multipart/form-data, and
//     Swagger 2.0 formData with no form type declared goes as
//     multipart/form-data when a formData parameter is of type file, else
//     as application/x-www-form-urlencoded; a []byte or io.Reader body goes
//     as application/octet-stream when that is declared or within a
//     declared range; an iterator body as the only declared sequential
//     type; any other value as application/json, else the only +json type.
//     A single range */* or application/*, and a Swagger 2.0 body parameter
//     with no consumes, send application/json for a value and
//     application/octet-stream for bytes. Otherwise the call asks. Set
//     Input.MediaType. Kind [MediaTypeChoice].
//   - Part media type: when the property's Encoding lists several types or
//     a range, the call asks. When an OpenAPI 3.0 property has no type,
//     which 3.0 leaves open, it defaults to application/octet-stream, as
//     3.1 says. Otherwise the part takes the Encoding's one type, or
//     OpenAPI's default for the property's schema (a binary string
//     application/octet-stream, other scalars text/plain, objects
//     application/json); an array property sends one part per item, typed
//     from its items the same way. Set Part.MediaType. Kind
//     MediaTypeChoice, with Name.
//
// A refusal also records a missing credential, as a [CredentialChoice].
//
// # Fixed rules
//
// Where OpenAPI is silent and offers no alternatives, the client follows
// these rules. Their results are visible in the prepared request, and can
// be changed there. Redirects follow the rules on [Redirects]; filenames
// those on [Part].
//
//   - URL: the server URL and the path are joined as written, except that
//     one "/" is dropped where the server URL ends with one and the path
//     begins with one. A relative server URL resolves against the URI of
//     the document that contains the Server Object, the one it was retrieved
//     from, never its OpenAPI 3.2 $self. In Swagger 2.0, as 2.0 says, a
//     missing host is the host and port the document was retrieved from, a
//     missing schemes list its scheme, and a missing basePath adds nothing.
//   - Bodies by method: in OpenAPI 3.0, a request body declared on GET,
//     HEAD or DELETE is ignored, as 3.0 says, so the operation takes none;
//     in the other editions a declared body is sent with any method but
//     TRACE. CONNECT operations are refused.
//   - Parameter order: the path item's parameters, then the operation's, in
//     declared order; query credentials last.
//   - Percent-encoding: path, query and cookie values encode every byte
//     outside RFC 3986's unreserved set as %XX in uppercase hex. With
//     allowReserved, RFC 6570 reserved expansion applies: reserved
//     characters and existing %XX triples pass through. OpenAPI 3.2 cookie
//     style values, header values, and an apiKey sent in a cookie are
//     written as given; a cookie value holding a ";" or a control character
//     is refused. deepObject nests objects as a[b][c]=v; nesting in any
//     other style is refused.
//   - Form bodies use the WHATWG application/x-www-form-urlencoded encoder
//     in every edition (a space as +, letters, digits and *-._ literal,
//     every other byte as %XX), except that a property whose Encoding sets
//     style, explode or allowReserved is written by RFC 6570, as OpenAPI
//     says, and a Swagger 2.0 formData array by its collectionFormat (csv
//     unless declared; multi repeats the field). Multipart/form-data fields
//     are never URI percent-encoded. Fields of a form or multipart body
//     follow the order encoding/json writes members in (a struct's fields in
//     declaration order, a map's keys sorted), and items keep their order.
//     A body is encoded once, so HTTP.Body and every GetBody give the same
//     bytes.
//   - Cookies: one Cookie field, pairs joined by "; ", parameters in
//     declared order, then credentials.
//   - Swagger 2.0 empty values: an allowEmptyValue parameter given "" is
//     sent as name=; set Options.NameOnlyEmpty to send the name alone.
//   - Accept: the JSON types the operation's responses declare, then the
//     other declared types in document order, without q-values; none when
//     none are declared. An Accept header field replaces it.
//   - Header fields: the client generates Accept, Content-Type,
//     Content-Length when known, header parameters and credentials, and no
//     User-Agent beyond net/http's. Options.Header and then Input.Header
//     are applied over the generated fields: a field replaces the same field
//     set before, and one with no values removes it. At either level,
//     Content-Type is refused (Input.MediaType chooses the media type), and
//     so is a field that a header parameter the call supplies, or the call's
//     credential, sets; a declared header parameter the call leaves unset
//     may come from a header field. A declared Swagger 2.0 Content-Type
//     header parameter, required or not, is satisfied by the media type the
//     call sends and is never set by value. A credential is added at send
//     time and replaces a field of the same name edited into Request.HTTP.
//   - Content codings are Go's: the transport asks for gzip and removes it,
//     and every bound counts decoded bytes. A header field that sets
//     Accept-Encoding turns that off, and the coded bytes then pass
//     through unchanged, for a *[]byte or io.Writer to receive.
//
// # Credentials
//
// Options.Credentials holds a [Credential] for each security scheme, by
// the scheme's name as a requirement writes it: a component name, or, in
// OpenAPI 3.2, a URI naming a Security Scheme Object, which the client
// resolves as a reference. For each call the client applies one security
// alternative (see Choices), calls the credential sources it needs when
// the request is sent, never when it is prepared, and adds credentials only
// while the request's URL has the origin of the server the call resolved
// to.
//
// Bearer tokens (http bearer, oauth2, openIdConnect) and Basic credentials
// are sent only over https or to a loopback host (a loopback address, or
// localhost or a name under .localhost, as RFC 6761 reserves them, matched
// without resolving), as RFC 6750 requires and RFC 7617 advises; a call
// that would send one over plain http elsewhere is refused. API keys, which
// no RFC governs, are not. A caller whose network secures plain http
// another way places the credential through its own transport, with
// [FromTransport].
//
// A credential and a parameter never share a destination: a call that
// supplies a parameter for the header field, query name or cookie name its
// applied credential sets is refused at the parameter's key, and an
// alternative two of whose schemes set the same field cannot be used, and
// is refused naming both.
//
// A call is refused, never sent without the authorization the caller
// configured, when a scheme it needs has no credential, or has an empty or
// zero one: a scheme present in Options.Credentials with such a value
// refuses every operation that names it, even one that also allows
// anonymous access. Load refuses a Credentials name the document never
// uses, as a likely misspelling, and a [Basic] credential for a scheme that
// is not http basic. FromTransport also satisfies a scheme a requirement
// names but the document never declares, or declares defectively.
package openapi
