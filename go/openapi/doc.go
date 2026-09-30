// Package openapi calls the operations of an OpenAPI document with ordinary
// Go values. It reads Swagger 2.0 and OpenAPI 3.0, 3.1 and 3.2 documents,
// written in JSON or YAML, and needs no generated code. Its only authorities
// are the OpenAPI specifications and the RFCs they rely on. A document is
// read by the latest patch of its minor line: 3.0.4, 3.1.2 or 3.2.1.
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
// server, credentials, redirects, extra header fields, codecs and memory
// bounds.
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
// Params values, Body, and Part contents take any Go value. Only a nil
// interface is absent; a typed nil, such as a nil pointer or map, is a
// value, which encoding/json writes as null. The client first converts a
// value to JSON data as encoding/json would (struct tags, MarshalJSON,
// TextMarshaler map keys), then serializes that data as the document says;
// a caller's codec (see [Options.Codecs]) receives the value as given.
// Where a parameter or a form or multipart field needs text, a number or
// boolean is written in its JSON spelling (10, 2.5, true) and a string or
// json.Number as it is.
//
// The client's codecs sort media types into four classes, for requests
// and responses alike, taking the first that applies:
//
//   - sequential: the types [Items] frames item by item (JSON Lines, JSON
//     text sequences, server-sent events), multipart excepted;
//   - JSON: application/json and any +json type;
//   - XML: application/xml, text/xml and any +xml type;
//   - text: any other text/* type.
//
// A body under a form, multipart or sequential type takes the shapes
// Input.Body lists. Otherwise a type with a caller's codec takes a value of
// any Go type, which that codec encodes; a JSON type is written as
// encoding/json writes the value; and any other type takes only a string,
// as its UTF-8 bytes, and a text type also a number or boolean, in its JSON
// spelling.
//
// In a parameter not serialized by content, and in a form or multipart field
// serialized by a style or a Swagger 2.0 collectionFormat, null, an empty array
// and an object whose members are all undefined are undefined, as RFC 6570
// says, and "" is a value. An undefined member or array item is skipped, as RFC
// 6570 section 3.2.1 expands only defined ones, an undefined optional parameter
// is omitted, and an undefined required one is missing. A form or multipart
// property or array item whose JSON data is null is omitted, whatever its
// serialization. A parameter serialized by content is encoded as a body of its
// media type is, so under application/json null, [] and {} are present values,
// and a []byte is the encoded content; a reader, and a multipart or sequential
// media type, cannot serialize a parameter and are refused at its key. A reader
// or Part anywhere inside a parameter value the client encodes with
// encoding/json is refused at the parameter's key, as for a body, unless its
// own MarshalJSON or MarshalText encodes it. A JSON body
// or JSON part is exactly what its codec writes, null members, [] and {}
// included. A value the client encodes that is nested deeper than 1,000 levels,
// counted in the JSON encoding/json writes (a MarshalJSON's output included),
// is refused at its key; a caller's codec receives the value as given. A
// parameter that would take the request target or a header field past 1 MiB,
// far beyond the 8,000 octets RFC 9110 section 4.1 asks servers to accept,
// is refused at its key, and its serialization stops there. Schema
// defaults are never sent, and values are never validated against schemas. How
// bytes, readers and iterators are sent is on Input.Body.
//
// Where the client's own JSON codec creates the values, as when out is a
// *any, a *map[string]any or a *[]any, and for Items[any] and
// StatusError.Decode into those, JSON numbers are kept exact as
// json.Number. A caller's own type decodes exactly as json.Unmarshal
// would, its any-typed fields included.
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
// reports whether the transport consumed the complete request body.
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
//     Options.ServerID or Options.BaseURL, and none requires BaseURL.
//     ServerID identifies one even when URL or name collides. Server
//     variables use their declared defaults; a server with a variable that
//     has none is not usable until Options.Variables gives it a value.
//   - One security alternative selects itself. Several require
//     Options.Security, Options.SecurityKey or Input.Security, including an
//     anonymous alternative. SecurityKey names an exact alternative.
//     The selected alternative requires its own Credentials; credentials
//     never select an alternative implicitly.
//   - A body uses the declared request media type when exactly one is
//     declared and it is concrete; otherwise, a range counting as an
//     alternative, it requires Options.MediaType or Input.MediaType. A
//     form field or multipart
//     part uses its Encoding contentType on the same terms, or its default
//     type when the Encoding gives none; a list or a range requires
//     Part.MediaType. The default is read from the field's schema (the
//     property's, or, for a positional part, the prefixItems, items or
//     itemSchema entry for its position), never from the Go value; in
//     Swagger 2.0, and where an OpenAPI 3.0 schema has no type, it is
//     text/plain, or application/octet-stream for a file parameter.
//
// Options.Security, Options.SecurityKey and Options.MediaType are
// preferences: each applies to the operations that offer its selection,
// and any other operation is called as if it were unset. The Input fields
// select exactly, for one call, and refuse an operation that does not
// offer their selection. Options.Server and Options.ServerID refuse such
// an operation too, since falling back would change where credentials go.
//
// These are setting requirements, not a decision history. A caller that
// needs another spelling for a scalar parameter can pass a string, or use
// Input.ParamWriters for a parameter the built-in serializer cannot encode.
// A media type the client has no codec for can be given one in
// Options.Codecs, or be sent as a pre-encoded []byte or io.Reader body; a
// caller with its own response policy can use Request.Send.
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
// Redirects follow [Redirects]; part names and filenames follow [Part].
//
//   - URL: server variables are substituted first, as given. A relative
//     result then resolves, by RFC 3986 section 5.2, against the URI of the
//     document that contains the Server Object, the one it was retrieved
//     from, never its OpenAPI 3.2 $self; only an http or https URI is such
//     a base, and otherwise the server cannot be used. The path is then
//     appended as written, except that one "/" is dropped where the URL
//     ends with one and the path begins with one. A server URL with
//     userinfo, a query or a fragment after substitution cannot be used
//     either (Server.Err, where the document alone decides it). An empty
//     servers array on a path item or operation, and an empty Swagger 2.0
//     schemes list, are read as absent, as OpenAPI says of the root's
//     servers. In Swagger 2.0, as 2.0 says, a missing host is the host and
//     port as written in the http or https URI the document was retrieved
//     from, a missing schemes list that URI's scheme, and a missing basePath
//     adds nothing; without such a URI, a server missing host or schemes
//     cannot be used.
//   - Bodies by method: a request body declared on TRACE or CONNECT, and in
//     OpenAPI 3.0 on GET, HEAD, DELETE or OPTIONS, as 3.0 says, is ignored,
//     so the operation takes none; otherwise a declared body is sent with
//     any method. An OpenAPI 3.2 CONNECT additional operation can be
//     prepared and sent; its prepared URL is the OpenAPI URL, so for RFC
//     9110's authority-form target the caller clears URL.Path and gives the
//     port, or its transport does. A 2xx tunnel needs a caller-provided
//     transport that exposes its duplex connection. Request.Send leaves the
//     response open.
//   - Order: the path item's parameters, then the operation's, in declared
//     order, an overriding parameter taking the place of the one it
//     overrides; query credentials last. An object value's members, and a
//     form or multipart body's fields, follow the order encoding/json
//     writes members in (a struct's fields in declaration order, a map's
//     keys sorted), and items keep their order.
//   - Percent-encoding: path and query values (content-serialized ones
//     included, application/x-www-form-urlencoded too), form-style cookie
//     values, and parameter and member names encode every byte outside RFC
//     3986's unreserved set
//     as %XX in uppercase hex. A path parameter value that would form a
//     whole "." or ".." segment is refused, since RFC 3986 section 5.2.4
//     removes such segments before the value could reach the server. So deepObject nests objects
//     as a%5Bb%5D%5Bc%5D=v, and the spaceDelimited and pipeDelimited
//     delimiters are %20 and %7C. allowReserved applies to query
//     parameters, and in OpenAPI 3.2 to path parameters and form-style
//     cookie parameters too; elsewhere it is ignored. Where it applies, RFC
//     6570 reserved expansion is used exactly, member names included
//     (parameter names always follow the rule above): reserved characters
//     and existing %XX triples pass through, and the caller supplies any
//     percent-encoding OpenAPI leaves to the application. Header values
//     are written as given in every edition, never percent-encoded, as
//     OpenAPI 3.1.2 corrects. OpenAPI 3.2 cookie style names and values, a
//     content-serialized cookie value (OpenAPI 3.1.2 recommends text/plain
//     content so the application assembles the cookie), and an apiKey sent
//     in a cookie, are written as given too; a cookie
//     value written as given that holds a ";" or a control character is
//     refused.
//   - Styles: RFC 6570's normative text governs where its informative
//     Appendix A differs, so an exploded object member whose value is ""
//     is written as its name alone except in form style. Whether a value
//     is undefined (see Values) is settled first; the refusals here apply
//     to defined values. deepObject ignores explode. Nesting in any style but
//     deepObject is refused, and so are an array in a deepObject value, a
//     primitive for spaceDelimited, pipeDelimited or deepObject, explode
//     true with spaceDelimited or pipeDelimited, and, in OpenAPI 3.2, an
//     array or object for a cookie parameter with explode false. Each is
//     refused at the parameter's key, with Param.Err set where the document
//     alone decides it.
//   - Querystring: an OpenAPI 3.2 querystring parameter is the whole query,
//     and its name is not written. Under application/x-www-form-urlencoded
//     its value is an object written by the form-body rules, Encoding
//     included, and is not encoded again; under any other media type the
//     encoded value is percent-encoded as a query value is. An undefined
//     value or an empty result sends no query. A query credential follows,
//     after "&".
//   - Form bodies use the WHATWG application/x-www-form-urlencoded encoder
//     in every edition (a space as +, letters, digits and *-._ literal,
//     every other byte as %XX), except that a property whose Encoding sets
//     style, explode or allowReserved is written by RFC 6570, as OpenAPI
//     says, and a Swagger 2.0 formData array by its collectionFormat.
//     Multipart/form-data fields are never URI percent-encoded. A body is
//     encoded once, so HTTP.Body and every GetBody give the same bytes.
//   - Swagger 2.0 arrays and empty values: an array's items are encoded
//     first, then joined by its collectionFormat's delimiter (csv unless
//     declared), a nested items array by its own first; multi repeats the
//     name and value. In a path or query the delimiter is percent-encoded
//     (a space as %20, a tab as %09, | as %7C) and a comma stays literal;
//     in a header it is written as given; in a formData field the joined
//     value is then encoded as any form field is. A query or formData parameter
//     given "" is sent as name=; set Options.NameOnlyEmpty to send an
//     allowEmptyValue parameter's name alone.
//   - Cookies: one Cookie field, pairs joined by "; ", parameters in
//     declared order, then credentials. A Cookie field in Options.Header or
//     Input.Header is refused when the call sends cookie parameters or a
//     cookie credential, and a value for a header parameter named Cookie,
//     whose effect OpenAPI leaves undefined, is refused at its key. A
//     required cookie parameter is given in Params or by a writer; a
//     Cookie field never supplies it.
//   - Accept: none is synthesized. Set Options.Header or Input.Header to
//     request a particular representation; Call requires one where a
//     typed out could receive several (see Client.Call).
//   - Header fields: the client generates Content-Type, Content-Length for
//     a body that can be sent again (see Input.Body), header parameters and
//     credentials, and no User-Agent beyond net/http's. Options.Header and
//     then Input.Header are applied over the generated fields: a field
//     replaces the same field set before, and one with no values removes
//     it (a User-Agent included, so net/http adds none). A Header holding
//     two spellings of one field is refused, and so is a field or a header
//     parameter named Host, Content-Length, Transfer-Encoding, Trailer,
//     Connection, Keep-Alive, Proxy-Connection or Upgrade, since net/http
//     derives or HTTP forbids them (RFC 9110 sections 6.6.2 and 8.6, RFC
//     9113 section 8.2.2). A Header entry with no values is a conflict like
//     any other when a header parameter the call supplies, or the Cookie
//     field, sets that field. At either level, Content-Type, Content-Length and
//     Transfer-Encoding are refused (the MediaType settings choose the
//     media type), and so is a field that a header parameter the call supplies,
//     or the call's credential, sets; a declared header parameter the call
//     leaves unset may come from a header field. A declared Swagger 2.0
//     Content-Type header parameter, required or not, is satisfied by the
//     media type the call sends and is never set by value, except on a
//     call that sends no body, where it is an ordinary header parameter.
//   - Content codings are Go's: the transport may ask for gzip and remove it,
//     and every bound counts decoded bytes. A header field that sets
//     Accept-Encoding turns that off. A body whose Content-Encoding, other
//     than identity, remains passes through unchanged to a *[]byte or
//     io.Writer; any other target, Items and Events report an error naming
//     the coding.
//
// # Credentials
//
// Options.Credentials holds a [Credential] for each security scheme, by
// the scheme's name as a requirement writes it: a component name, or, in
// OpenAPI 3.2, a URI naming a Security Scheme Object (see [SchemeLookup]).
// Where one name resolves to different schemes in different operations, its
// Credential is checked and used as the scheme each call resolves; use
// [Client.With] for different credentials per operation. An http scheme,
// such as bearer or basic, is compared without regard to case, as RFC 9110
// says.
//
// For each call the client applies one security alternative selected by the
// caller or the sole one in the document, calls the credential sources it
// needs when the request is sent, never when it is prepared, and adds
// credentials only to requests with the origin of the server the call
// resolved to, as [Redirects] says. A header credential replaces a field of
// the same name, and a cookie or query credential a pair of the same name,
// including one edited into Request.HTTP.
//
// Bearer tokens (http bearer, oauth2, openIdConnect) and Basic credentials
// are sent only over https, wss, or to a loopback host (a loopback address, or
// localhost or a name under .localhost, as RFC 6761 reserves them, matched
// without resolving), as RFC 6750 requires and RFC 7617 advises; a call
// that would send one over plain http or ws elsewhere is refused. The
// caller's transport is responsible for actually securing wss. A URL scheme
// other than http, https, ws or wss requires FromTransport for these
// credentials. API keys, which no RFC governs, are not restricted by this
// rule. A caller whose network secures plain http or ws another way places
// the credential through its own transport, with [FromTransport].
//
// A credential and a parameter never share a destination. A declared
// parameter at the header field, query name or cookie name the applied
// credential sets is supplied by the credential: a required one counts as
// given, and a call that also supplies it is refused at the parameter's
// key. An alternative two of whose schemes set the same field cannot be
// used, and is refused naming both. A FromTransport scheme places nothing,
// so it takes part in no destination rule.
//
// A call is refused, never sent without the authorization the caller
// selected, when a scheme in its selected alternative has no credential,
// or has an empty or zero one, or, for a mutualTLS scheme, which needs
// none, has any credential but FromTransport. Unselected alternatives have
// no effect on the call. Load refuses a Credentials name the document never
// uses, as a likely misspelling, an empty static credential (Secret(""),
// Basic("", "") or the zero Credential), and a [Basic] credential for a
// name none of whose schemes is http basic. FromTransport also satisfies a
// scheme a
// requirement names but the document never declares, or declares
// defectively.
package openapi
