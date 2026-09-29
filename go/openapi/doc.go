// Package openapi calls the operations of an OpenAPI document with ordinary
// Go values. It reads Swagger 2.0 and OpenAPI 3.0, 3.1 and 3.2 documents,
// written in JSON or YAML, and needs no generated code. Its only authorities
// are the OpenAPI specifications and the RFCs they rely on.
// Client operations are outbound requests under paths. Callbacks and
// webhooks describe provider-initiated requests toward the API consumer;
// their authored declarations are available through Client.Document but
// are not outbound operations of this Client.
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
// [Client.Stream] is Call with a successful response body left open, for
// server-sent events, JSON Lines, multipart parts, and anything read as it
// arrives. An outstanding request body has a separate completion result,
// [Response.WaitRequest].
// [Client.Prepare] builds a request without sending. The resulting [Request]
// exposes its [*http.Request] before credentials are placed and can send it
// through [Request.Send] without classifying the HTTP status. The request
// is editable, although the transport may still add wire fields. A call
// whose server, security alternative or request media type is ambiguous
// refuses before dispatch and names the setting needed. [Client.Operations]
// describes the document for callers that need its alternatives.
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
// field serialized by a style, a nil value, an empty slice and an empty map
// are undefined and omitted, as RFC 6570 says, and "" is a value. A
// parameter serialized by content applies its media codec instead: under
// application/json, [] and {} are present values. A JSON body or JSON part
// is exactly what encoding/json writes, null members, [] and {} included.
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
// Call and Stream classify the response as follows:
//
//   - API request not sent: a [*RequestError]. A credential source may have
//     made its own request before returning an error.
//   - Transport failure: the *url.Error from the http.Client. The request
//     may have reached the server.
//   - A final status other than 2xx: a [*StatusError], holding the response
//     and a bounded copy of its body.
//   - A 2xx whose body could not be read or decoded: a [*DecodeError]. The
//     server has handled the call; do not assume it can be repeated.
//   - A 2xx, decoded: a nil error.
//
// An upload error may be joined with a response error; errors.As can find
// both. Send leaves every status open and unclassified. Response.Decode
// applies the same codecs and bounds to any status, while WaitRequest
// reports whether the transport consumed the complete request body. Whenever a response
// arrived, the [*Response] is returned, even with an error. When the call's
// context is done before the call completes, the
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
// # Configuration when the document is incomplete
//
// The client applies defaults that OpenAPI or HTTP defines. It does not
// elect among multiple authored alternatives merely because one is first,
// a credential happens to be present, or a Go value resembles one media
// type. When a value is needed to form a request, a *RequestError names the
// setting; [Client.Operation] and [Client.Operations] describe the offered
// alternatives:
//
//   - One usable server selects itself. Several require Options.Server,
//     Options.ServerID or Options.BaseURL. ServerID identifies one even when
//     URL or name collides. A relative server without a base URI requires BaseURL.
//     Server variables use their OpenAPI defaults; a variable without a
//     usable default requires Options.Variables.
//   - One security alternative selects itself. Several require
//     Options.Security, Options.SecurityKey or Input.Security, including an
//     anonymous alternative. SecurityKey names an exact alternative.
//     The selected alternative requires its own Credentials; credentials
//     never select an alternative implicitly.
//   - A body with one concrete declared request media type uses it. Several,
//     a media range without a concrete type, or no declared type require
//     Input.MediaType. A multipart part takes an OpenAPI-defined default
//     or sole concrete Encoding type; otherwise it requires Part.MediaType.
//
// These are setting requirements, not a decision history. A caller that
// needs another spelling for a scalar parameter can pass a string, or use
// Input.ParamWriters for a parameter the built-in serializer cannot encode.
// A body codec the client does not implement can supply pre-encoded []byte
// or an io.Reader; a caller with its own response policy can use Request.Send.
//
// # Raw invocation boundary
//
// A resolvable operation does not depend on the client's ability to
// interpret its schemas. For a declared request media type whose key can
// govern the call, Input.Body as []byte or io.Reader supplies the complete
// encoded body. The client does not inspect its schema, multipart Encoding
// or Swagger 2.0 formData fields; the caller owns their wire validity.
// Input.ParamWriters supplies a known parameter whose built-in serializer
// cannot produce the needed spelling, before Prepare could refuse it.
// Options.BaseURL supplies a usable server when an authored one cannot be
// used, and FromTransport delegates a security scheme the client cannot
// place. Request.Send leaves every final status and response body to the
// caller. These paths can be combined in one call.
//
// The client still needs the operation's method and path, the identity of
// every required parameter, a concrete request media type when a body is
// sent, and a selected security alternative. A broken or inaccessible
// reference that hides one of those facts can prevent preparation. A
// caller may configure Loader.Fetch and AllowReference to supply trusted
// referenced documents. For a complete, valid OpenAPI description, an
// unsupported schema or structured serializer alone must not prevent a
// caller-encoded request from reaching Request.Send.
//
// # Fixed rules
//
// Where OpenAPI is silent and offers no alternatives, the client follows
// these documented rules. Their request-side results are visible in the
// prepared request and may be changed there when preparation succeeds.
// Redirects follow [Redirects]; filenames follow [Part].
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
//     TRACE. An OpenAPI 3.2 CONNECT additional operation can be prepared
//     and sent; a 2xx tunnel needs a caller-provided transport that exposes
//     its duplex connection. Request.Send leaves the response open.
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
//   - Accept: none is synthesized. Set Options.Header or Input.Header to
//     request a particular representation.
//   - Header fields: the client generates Content-Type,
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
//   - Content codings are Go's: the transport may ask for gzip and remove it,
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
// alternative selected by the caller or the sole one in the document, calls
// the credential sources it needs when
// the request is sent, never when it is prepared, and adds credentials only
// while the request's URL has the origin of the server the call resolved
// to.
//
// Bearer tokens (http bearer, oauth2, openIdConnect) and Basic credentials
// are sent only over https, wss, or to a loopback host (a loopback address, or
// localhost or a name under .localhost, as RFC 6761 reserves them, matched
// without resolving), as RFC 6750 requires and RFC 7617 advises; a call
// that would send one over plain http or ws elsewhere is refused. The
// caller's transport is responsible for actually securing wss. Other
// custom schemes require FromTransport for these credentials. API keys,
// which no RFC governs, are not restricted by this rule. A caller whose
// network secures plain http or ws another way places the credential
// through its own transport, with
// [FromTransport].
//
// A credential and a parameter never share a destination: a call that
// supplies a parameter for the header field, query name or cookie name its
// applied credential sets is refused at the parameter's key, and an
// alternative two of whose schemes set the same field cannot be used, and
// is refused naming both.
//
// A call is refused, never sent without the authorization the caller
// selected, when a scheme in its selected alternative has no credential,
// or has an empty or zero one. Unselected alternatives have no effect on
// the call. Load refuses a Credentials name the document never
// uses, as a likely misspelling, and a [Basic] credential for a scheme that
// is not http basic. FromTransport also satisfies a scheme a requirement
// names but the document never declares, or declares defectively.
package openapi
