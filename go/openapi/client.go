package openapi

import (
	"context"
	"io"
	"net/http"
)

// A Client calls the operations of one loaded document. It is safe for
// concurrent use and never changes once made. The zero Client has no
// operations: it describes nothing and refuses every call with a
// *RequestError wrapping ErrNoOperation.
type Client struct {
	doc  *document // shared by derived Clients
	opts Options
}

type document struct{}

// Options says where calls go, as whom, and how. Its zero value supplies only
// documented transport defaults. When the document leaves a consequential
// alternative open, a call refuses before dispatch rather than electing one.
// The Options given to [Load] apply to every call of the Client, and Load
// refuses those the document cannot use; [Client.With] derives a Client with
// different ones, for a tenant or for one call.
type Options struct {
	// HTTPClient sends every request, and fetches documents while loading.
	// Nil means http.DefaultClient. It is used as given and never modified.
	// Its Transport is the place for tracing, metrics, retries, proxies and
	// client certificates. Prefer a context deadline to its Timeout, which
	// also ends a Stream mid-body.
	HTTPClient *http.Client

	// Server selects one of the operation's servers, by its URL as written
	// (with its {variables}) or by its OpenAPI 3.2 name, as Operation.Servers
	// lists them. Empty selects the sole usable server; if several are usable,
	// the call requires Server, ServerID or BaseURL. A value that matches two
	// servers (one's name, another's URL) is refused as ambiguous; use ServerID
	// to name one exactly. A value that matches no server of the document is
	// refused by Load, and a call to an operation that does not list it is
	// refused. Server and ServerID cannot both be set.
	//
	// Credentials go wherever the call's server is. When the document is not
	// trusted to choose, set Server or BaseURL.
	Server string

	// ServerID selects an exact Server.ID from Operation.Servers. IDs tell
	// apart authored servers with the same URL or name. An ID not used
	// anywhere in the document is refused by Load; an operation that does
	// not offer it refuses the call. With preserves IDs from the shared
	// document. ServerID cannot be set with Server or BaseURL.
	ServerID string

	// BaseURL, if set, replaces every server of every operation: an absolute
	// URL with a scheme and host, joined to the operation's path as the
	// package documentation says. HTTPClient.Transport must support its
	// scheme. This includes Swagger 2.0's ws and wss schemes when the
	// caller supplies a suitable transport. Server and ServerID must then
	// be empty, and Variables are not used.
	BaseURL string

	// Variables gives values for server variables, by name. A variable
	// without a value takes its declared default, which is always usable.
	// The enum limits other values: one outside it refuses the call, and an
	// empty enum permits only the default. A name that appears in no server
	// URL of the document is refused by Load, as a likely misspelling.
	// Values are substituted as given.
	Variables map[string]string

	// Credentials holds a Credential for each security scheme, by the name
	// a security requirement uses for it. The rules for their use and
	// refusal are in the package documentation, under Credentials. A
	// mutualTLS scheme needs no entry.
	Credentials map[string]Credential

	// Security selects, for every operation that offers it, the security
	// alternative whose schemes are exactly these, whatever scopes each
	// operation asks for; it is a preference, as the package documentation
	// says. If several alternatives have the same scheme names, the call
	// requires Input.Security to distinguish their scope requirements. A
	// missing credential for the selected alternative refuses the call. An
	// operation with several alternatives requires Security, SecurityKey
	// or Input.Security, even when credentials satisfy only one or one
	// alternative is anonymous. A set of names no alternative of the
	// document uses is refused by Load. Input.Security overrides it for one
	// call.
	Security []string

	// SecurityKey selects one exact SecurityRequirement.Key for every
	// operation that offers it, including "{}" for an anonymous alternative;
	// it is a preference, as Security is. It takes precedence over an absent
	// Input.Security. Security and SecurityKey cannot both be set. Unlike
	// Security's scheme-name match, this distinguishes alternatives with the
	// same schemes but different scopes. A key that names no alternative or
	// credential-free operation is refused by Load.
	SecurityKey string

	// MediaType selects a concrete request media type, as Input.MediaType
	// does, for every operation whose request body declares it, matched as
	// Request.Media is; it is a preference, as Security is.
	// Input.MediaType overrides it for one call. A type no operation
	// declares is refused by Load.
	MediaType string

	// Redirects says whether the client follows redirects; zero means none.
	// See [Redirects].
	Redirects Redirects

	// Header holds header fields for every request, applied by the rules in
	// the package documentation, under Fixed rules. How its fields fare on a
	// redirect is on [Redirects].
	Header http.Header

	// Codecs adds codecs, or replaces the client's own, by media type. A key
	// is a media type without parameters, such as "application/cbor", or a
	// structured syntax suffix, such as "+cbor" (RFC 6838 section 4.2.8),
	// compared without regard to case; a type key wins over a suffix key,
	// which wins over a built-in codec. A codec applies wherever the client
	// encodes or decodes a value of its type: request bodies, parts and
	// content-serialized parameters, and responses that Call,
	// Response.Decode, StatusError.Decode and Items decode, their items and
	// parts included. So entries for "application/json" and "+json" replace
	// encoding/json everywhere, *any included, and decide how numbers are
	// represented. A *[]byte or io.Writer target, and a []byte or io.Reader
	// body, bypass codecs. Load refuses any other key, and one that names a
	// sequential or multipart type, whose framing stays the client's; their
	// items and parts use the codec for their own type. An Encode error
	// refuses the call at the body's or parameter's Inputs key, or aborts
	// the body for an iterator's item; a Decode error is a *DecodeError, or
	// an ErrItem for one item.
	Codecs map[string]Codec

	// MaxBodyBytes bounds a body decoded by Call or Response.Decode into a
	// value, read into a *[]byte, or discarded for a nil out. Zero means
	// 64 MiB, and a negative value means no limit. A longer body is a
	// *DecodeError wrapping
	// *http.MaxBytesError, except that a discarded one is simply cut off. A
	// body copied to an io.Writer is bounded only by the writer.
	MaxBodyBytes int64

	// MaxErrorBytes bounds the body a StatusError keeps. Zero means 1 MiB,
	// and a negative value means no limit. A longer body is cut, and the
	// StatusError says so; it is never replaced by a size error.
	MaxErrorBytes int64

	// MaxItemBytes bounds one item read with Items or Events, a multipart
	// part Items decodes included. Zero means 16 MiB, and a negative value
	// means no limit. A longer item ends the iteration with an
	// *http.MaxBytesError. Bytes read from a Stream's Body directly, or from
	// a *multipart.Part by the caller, are not bounded.
	MaxItemBytes int64

	// NameOnlyEmpty sends "" for a Swagger 2.0 query or formData parameter
	// that allows empty values as its name alone (?flag), instead of the
	// default, its name and an equals sign (?flag=).
	NameOnlyEmpty bool
}

// A Codec encodes values to, and decodes them from, one media type. The
// client may call its methods concurrently. Where one applies is on
// Options.Codecs.
type Codec interface {
	Encode(w io.Writer, v any) error
	Decode(r io.Reader, v any) error
}

// Redirects says which 3xx responses the client follows. Only 301, 302, 303,
// 307 and 308 with a Location can be followed. A 303 is followed with GET
// (HEAD stays HEAD) and no body. A 301 or 302 changes POST to GET with no
// body, and keeps any other method and its body, as 307 and 308 do. A hop
// that drops the body drops Content-Type and the other content fields, and
// a hop that must resend a body that cannot be sent again (see Input.Body)
// is not followed. A 3xx not followed is the outcome, a *StatusError. The
// HTTPClient's CheckRedirect is still consulted on every hop the client
// follows, after the client applies the rules below, and can restore a
// field the caller deliberately wants to forward; with a nil
// CheckRedirect, net/http's limit of 10 hops applies.
//
// On a hop to another origin (scheme, host and port), the client removes the
// credentials it added and any header a security scheme placed, the
// Authorization and Cookie fields (cookie parameters included), all header
// parameters, and every field supplied through Options.Header, Input.Header, or
// an edit to Request.HTTP.Header. Generated fields needed to describe a
// replayed body, such as Content-Type and Content-Length, are rebuilt. Referer
// is removed if it holds a query credential the client added. CheckRedirect may
// restore a field intentionally, such as Range or Accept; the client does not
// infer whether an arbitrary caller header is a secret. On a hop within the
// origin, header and cookie credentials are placed again; a query credential
// goes only on the request the client builds, never onto a Location. Once a hop
// has left the call's origin, no later hop has credentials, header parameters
// or caller fields placed again, even one back on that origin. A transport that
// satisfies a FromTransport scheme sees every hop, other origins included and
// must apply its own origin policy.
type Redirects int

const (
	FollowNone Redirects = iota // follow none (the default)
	FollowAll                   // follow every eligible 3xx
)

// With returns a Client that shares c's document, with c's Options changed
// by f. f receives a copy: its maps are always non-nil and belong to the new
// Client, so f may add, replace or delete entries, and may reset any field
// to its zero value to restore the default. Nothing f does affects c. With
// is cheap enough to call for each tenant or each call, and the new Client
// keeps a copy of the Options f leaves, maps and slices included. With(nil)
// returns c. With returns no error: a derived Client skips Load's checks for
// names the document never uses (in Credentials, Variables and MediaType),
// and any other Options the document cannot use refuse each call they
// affect.
func (c *Client) With(f func(*Options)) *Client {
	panic("unimplemented")
}

// An Input holds the values and settings for one call. The client never
// modifies an Input. Call has stopped reading its body when it returns.
// For Send or Stream, wait for Response.WaitRequest before reusing a body
// reader or iterator; closing Response.Body stops an outstanding upload.
// A body that can be read only once is consumed by its first call. A nil
// *Input is an empty one.
type Input struct {
	// Params holds parameter values by Param.Key. A parameter's key is its
	// name, unless another parameter Operation.Params lists has the same
	// name, or the name is empty, begins with "/" or "Input.Body", or begins
	// with a location and a dot (as "query.id" does); then its key is its
	// location, a dot and its name: "query.id", "header.query.id",
	// "query./photo". So no two parameters share a key, and none looks like
	// a body key in RequestError.Inputs. A key the operation does not
	// declare, or a
	// missing required parameter, refuses the call; a header parameter
	// supplied as an Options.Header or Input.Header field counts as given,
	// and so does one the applied credential supplies (see Credentials in
	// the package documentation). In Swagger 2.0, formData parameters are
	// properties of Body.
	Params map[string]any

	// ParamWriters overrides the serialization of individual parameters,
	// keyed by Param.Key. A writer counts as supplying a required parameter;
	// an unknown key, and the same key in Params and ParamWriters, are
	// refused. After the client
	// serializes the other parameters and the body, Prepare calls writers in
	// the operation's parameter order with its unsigned *http.Request. The
	// writer may edit URL.Path, URL.RawPath, URL.RawQuery, headers or cookies
	// to implement a spelling the built-in serializer cannot represent.
	// A path parameter's {name} remains in both URL.Path and URL.RawPath
	// until its writer replaces it in both, keeping RawPath an encoding of
	// Path; an unresolved path token after all writers refuses preparation.
	// A writer also bypasses that parameter's serialization Err, including
	// for a required parameter, when its Key is known. It cannot bypass a
	// defect in the operation or an unresolved parameter reference whose
	// Key cannot be determined.
	// It must keep the URL valid and return an error on failure; that error
	// is reported at RequestError.Inputs[key]. Credential-destination
	// collisions are still refused. A writer is caller code and may be
	// called concurrently if the Input is reused concurrently.
	//
	// This is a narrow pre-serialization escape hatch. Ordinary Params are
	// safer and follow the edition's OpenAPI rules.
	ParamWriters map[string]func(*http.Request) error

	// Body is the request body, or a nil interface for none (a typed nil is
	// a value; see Values in the package documentation):
	//
	//   - A value whose type is exactly []byte, or an io.Reader, is sent as
	//     its bytes, under whatever media type is chosen, JSON types
	//     included. This is a complete pre-encoded body: the client does not
	//     inspect its schema, encode form fields or parts, or check it
	//     against a Media.Err, a field's Param.Err or required formData
	//     fields. The caller owns its framing and content. It can therefore
	//     send a declared media type whose structured encoder cannot
	//     represent the intended value, including a Swagger 2.0 file
	//     formData value under application/x-www-form-urlencoded. Any other
	//     type, a named byte-slice type included, is a value for the codec.
	//   - For form and multipart media, Body is an object (a map or a
	//     struct) whose properties are the fields. A property may be a
	//     []byte, an io.Reader or a [Part]; an array property sends one field
	//     or part per item under the property's name, unless its
	//     collectionFormat or Encoding style says otherwise.
	//   - For OpenAPI 3.2 multipart/form-data, Body may instead be a slice,
	//     one part per element, in order: each a one-property object, whose
	//     property names the part and whose value is its content, encoded as
	//     that position's Encoding says, or a Part whose Header gives
	//     Content-Disposition.
	//   - For any other OpenAPI 3.2 multipart media type, Body may instead be
	//     a slice, or an iterator as for a sequential type, one part per
	//     element, in order, whether or not the type declares prefixEncoding
	//     or itemEncoding: each a []byte, an io.Reader, a Part, or a value
	//     encoded by that part's media type. Such a part has no
	//     Content-Disposition or filename unless its Part sets them.
	//   - A part whose media type is multipart is encoded, one level deep,
	//     from an object or slice by its Encoding's own encoding,
	//     prefixEncoding or itemEncoding; a []byte or io.Reader supplies it
	//     pre-encoded, with its boundary in Part.MediaType.
	//   - For a sequential media type (JSON Lines, JSON text sequences,
	//     server-sent events), in any edition, Body is a slice, an iter.Seq,
	//     or an iter.Seq2 whose second value is an error, of any element
	//     type; each element is one item. Under text/event-stream an item is
	//     an object with no members but data, event and id, as strings, and
	//     retry, as a non-negative integer, or an [Event], of which only the
	//     fields it sets are used. It is written as those fields, data as one
	//     data line per line, then a blank line; any other member or type, or
	//     a line break in event or id, is an item that cannot be encoded.
	//     An iterator is written one item at a time as it
	//     yields, so a large body is never held. It runs on a goroutine of the
	//     transport; its yield returns false once the body is no longer
	//     wanted. Call waits for the iterator to return; Send and Stream may
	//     return at response headers while it is still running. An error
	//     from an iter.Seq2, an item that cannot be encoded, or the context
	//     ending before the iterator returns aborts the body and is reported
	//     by Call or Response.WaitRequest. An iter.Seq[any], an
	//     iter.Seq2[any, error], or
	//     an io.Reader the caller frames avoids per-item reflection.
	//   - Any other value is written by the media type's codec (see Values in
	//     the package documentation). Under a JSON type,
	//     json.RawMessage("null") sends null, and bytes go as a base64
	//     string: give the string, or the bytes as a named byte-slice type,
	//     which encoding/json writes in base64. In Swagger 2.0, the body
	//     parameter is Body itself.
	//
	// A body can be sent again, by a redirect, a retry or a second send of
	// a Request, when every source in it can: a []byte, a *bytes.Buffer, a
	// *bytes.Reader and a *strings.Reader, from the bytes they hold when the
	// call is prepared, without being drained; an *os.File that Stat
	// reports to be a regular file, from its offset when the call is
	// prepared; and every value the client encodes. Any other reader, such
	// as a pipe or os.Stdin, and an iterator, is read once.
	//
	// A Part or io.Reader inside a JSON value is refused with an Inputs
	// entry at its place in Body. A []byte nested in a JSON value is encoded
	// by encoding/json as a base64 string; in a form or multipart value it
	// remains raw part bytes. A nil Body where the request body is required
	// is refused at Inputs["Input.Body"], and a missing required Swagger 2.0
	// formData field in a structured form body at "Input.Body" followed by
	// its pointer, as the parameter it is. A pre-encoded body bypasses those
	// field checks. The client never closes a reader it is given.
	Body any

	// MediaType is the body's media type: a concrete type matching one the
	// operation declares, by the rules on Response.Media; any concrete
	// type where Swagger 2.0 declares none; or, for Swagger 2.0 formData
	// with no form type declared, either form type. When an OpenAPI 3.x
	// requestBody has an empty content map, a pre-encoded []byte or
	// io.Reader body may use any concrete type given here; there is no
	// governing Media descriptor or structured encoder. A range is refused.
	// MediaType may carry parameters, which are sent as given: a pre-encoded
	// multipart body requires its boundary here, and a boundary given for a
	// multipart body the client encodes is used. Empty means
	// Options.MediaType, else the declared type where one selects itself
	// (see the package documentation); otherwise a body requires MediaType
	// before dispatch.
	MediaType string

	// Security selects the security alternative by its
	// SecurityRequirement.Key, exactly as Operation.Security spells it,
	// "{}" for the anonymous alternative, overriding Options.SecurityKey and
	// Options.Security. Empty means SecurityKey, then Security, or the sole
	// alternative when there is one.
	// A key the operation does not list refuses the call, except that "{}"
	// also permits an operation whose Security is empty.
	Security string

	// Header holds header fields for this call, applied after
	// Options.Header by the same rules. How its fields fare on a redirect is
	// on [Redirects].
	Header http.Header
}

// A Part is one field of a form or multipart body, or one part of a
// positional multipart body, given where its media type, filename or part
// header fields matter. An empty field means its default. A part's name and
// filename are written in its Content-Disposition as given, each as a
// quoted-string with \ and " escaped, never as filename*; a CR or LF in
// either is refused.
type Part struct {
	// Content is the part's value: a []byte or an io.Reader for raw
	// content, a string, or any other value, encoded by MediaType.
	Content any

	// MediaType is the part's Content-Type: a concrete type matching, by the
	// rules on Response.Media, one its Encoding lists, or, where the
	// Encoding lists no type (as always in Swagger 2.0), any concrete type.
	// A range is refused. Empty uses the part's type where one selects
	// itself (see the package documentation); otherwise the call requires
	// MediaType.
	MediaType string

	// Filename is sent in the part's Content-Disposition. Empty means the
	// default: the part's name for a []byte or io.Reader Content, and none
	// otherwise. A part without a name, as in positional multipart, takes
	// its Content-Disposition only from Header, and Filename is refused on
	// it.
	Filename string

	// NoFilename sends no filename, whatever the Content. Setting it with
	// Filename is refused.
	NoFilename bool

	// Header holds other header fields of the part, such as those its
	// Encoding declares. A Content-Disposition field replaces the one the
	// client writes; Content-Type is refused (set MediaType).
	Header http.Header
}

// Call sends the operation named key with in, and decodes a 2xx body into
// out.
//
// The key is an operationId, or a method and a Paths key separated by one
// space, as in "GET /pets/{petId}": a fixed method in uppercase, as it is
// sent, or an OpenAPI 3.2 additional operation's method exactly as written.
// A key made of a method, one space and a string beginning with "/" always
// means a method and a Paths key, never an operationId.
//
// out must be nil, a *[]byte, an io.Writer, or a non-nil pointer; anything
// else is refused before sending. For a 2xx, the body is read to the end and
// closed before Call returns, and out receives it:
//
//   - nil discards it, reading at most MaxBodyBytes before closing the
//     connection.
//   - A *[]byte receives the raw bytes, appended to (*p)[:0] so its
//     capacity is reused, bounded by MaxBodyBytes. It stays nil when the
//     body is empty and the slice was nil, which tells an empty body from
//     JSON null.
//   - An io.Writer receives the raw bytes as they arrive, unbounded, without
//     holding them in memory.
//   - Any other pointer receives the body decoded straight from the
//     connection by the caller's codec for its media type (see
//     Options.Codecs), or else by its codec class (see Values in the
//     package documentation): JSON types (and a sequential type, as a
//     JSON array of its items) with encoding/json, anything but whitespace
//     after the value being a failure, as for json.Unmarshal; XML types
//     with encoding/xml, which ignores json tags, and which reads UTF-8,
//     US-ASCII and ISO-8859-1 documents, taking the encoding from a byte
//     order mark, else the Content-Type's charset, else the document's own
//     declaration; any text/* type (text/event-stream included) into a
//     *string, its bytes as sent, the charset left in the Content-Type;
//     and into a *any, text as a string and any other non-JSON type,
//     multipart included, as a []byte. A missing, repeated or unparsable
//     Content-Type is treated as application/octet-stream, which a *any
//     receives as a []byte and a typed target cannot; no type is inferred
//     solely from the document. A type these rules cannot decode into out
//     is a *DecodeError, whose Content keeps the start of the bytes; a
//     *[]byte takes any body as it is.
//
// When out is a pointer to decode into (not a *[]byte, an io.Writer or a
// *any) and the operation's 2xx responses declare concrete media types of
// more than one codec class (a type with a caller's codec is a class of its
// own), a call whose request carries no Accept field is refused before
// sending, at Settings key "Options.Header", naming the offered types.
//
// A 1xx, 204, 205 or 304 response, a response to HEAD, and a 2xx response
// to CONNECT have no body: Call, Stream and StatusError do not read one,
// and Send keeps a 101 or a tunnel open. An empty body is a success for
// every out, except that a JSON or XML type decoded into a pointer is a
// *DecodeError wrapping io.EOF when the response can have a body and its
// governing Message has Media (in Swagger 2.0, a schema). An empty text
// body decodes as "", an empty sequential body as an empty array, and any
// other empty body into a *any as an empty []byte. A body that fails to
// read, to decode, or to be written to out is a *DecodeError, and out may be
// partly filled. Any other final status is a *StatusError, and out is
// untouched. A call refused before sending returns a nil Response and a
// *RequestError.
//
// Call drains a successful response and waits for complete consumption of
// its request body before closing the response body. This lets a peer make
// progress on both sides of a finite duplex exchange. If request-body
// consumption fails, the error wraps its cause and the Response is still
// returned. Where the response also has a StatusError or DecodeError, the
// errors are joined, so errors.As can find both. A successful response does
// not hide an incomplete upload.
//
// Call is Prepare followed by [Request.Call]. Call applies the package's
// 2xx success policy; [Request.Send] leaves status interpretation to the
// caller.
func (c *Client) Call(ctx context.Context, key string, in *Input, out any) (*Response, error) {
	panic("unimplemented")
}

// Prepare builds the request for the operation named key with in, applying
// OpenAPI's rules and the caller's settings, and sends nothing. It never calls
// a credential source, and never reads a reader or iterator given in in. When
// the call cannot be sent, Prepare returns a *RequestError listing every
// problem at once. It does not require schema validation or a structured body
// encoder when in.Body is pre-encoded bytes or a reader. A known parameter's
// ParamWriters entry bypasses its built-in serializer. These escapes run before
// a Request exists; editing Request.HTTP is a later option, only after
// preparation succeeds. A missing operation, or an operation with Err set,
// still prevents preparation.
func (c *Client) Prepare(key string, in *Input) (*Request, error) {
	panic("unimplemented")
}

// A Request is a call prepared by [Client.Prepare] and not yet sent. A
// Request that Prepare did not make, the zero Request included, is refused
// with a *RequestError wrapping ErrNoOperation.
type Request struct {
	// HTTP is the request that will be sent: method, URL, parameters,
	// caller-supplied Accept, Content-Type and body, but no credentials, which
	// are added when it is sent. Its context carries the operation for
	// OperationFromContext and is replaced by the one given to Call or Stream.
	//
	// HTTP may be changed before sending, to set a header the document
	// cannot express, say, or to add a raw body the operation does not
	// declare. Such an edit is caller-owned HTTP behavior outside the
	// OpenAPI description. A send takes HTTP.Body the first time and
	// GetBody for every replay, as net/http does, so a caller who replaces
	// the body sets GetBody and ContentLength with it, or clears GetBody.
	// GetBody is set when the body can be sent again (see Input.Body). If
	// the URL is changed to another origin and the call needs credentials,
	// sending it is refused; set Options.BaseURL instead.
	HTTP *http.Request

	// Media is the operation's request body Media that governs the body,
	// the one the Content-Type sent matches by the rules on Response.Media,
	// whose schema and Encoding a structured body follows. It is nil when
	// there is no body or none is declared, as for an empty
	// requestBody.content with a raw body; HTTP.Header.Get("Content-Type")
	// still reports the concrete type sent. It is the same immutable
	// descriptor Operation.Body.Media exposes.
	Media *Media

	// Security is the Key of the security alternative the request will
	// carry, as Response.Security will report it: "{}" for the anonymous
	// one, or empty when the operation takes no credentials. It is known
	// when the call is prepared; the credentials are still placed when it
	// is sent.
	Security string
}

// Send sends r with ctx, adding credentials, and returns at the response
// headers for every final HTTP status. It neither classifies status as
// success or failure nor decodes the body. The caller owns and must close
// Response.Body. A request body may still be sending after response headers
// arrive. WaitRequest reports its separate completion or failure;
// closing Response.Body or canceling the call context stops it.
// A custom HTTPClient.Transport can also handle a Swagger 2.0 ws or wss
// URL. Send exposes the resulting status and body without imposing
// WebSocket framing; a 101 upgrade is returned as-is.
// Transport errors retain their usual uncertainty about
// whether the server received the request; a pre-dispatch refusal is a
// *RequestError. This is the lower-level path for callers whose response
// policy differs from Call and Stream. For a CONNECT operation, a custom
// HTTPClient.Transport can return a Body implementing io.ReadWriteCloser
// for tunnel use; the client leaves that body unchanged.
func (r *Request) Send(ctx context.Context) (*Response, error) {
	panic("unimplemented")
}

// Call sends r with ctx, adding credentials, and returns as [Client.Call]
// does. When r's body can be sent again (HTTP.GetBody is set, or there is
// no body), r may be sent any number of times, concurrently too. Otherwise
// it may be sent once, and sending it again is refused with a
// *RequestError, nothing sent.
func (r *Request) Call(ctx context.Context, out any) (*Response, error) {
	panic("unimplemented")
}

// A Response is a response the server sent to a call: the *http.Response,
// with what the operation declares about it.
//
// For a Response from Call, Body has been read and closed, except with a
// StatusError or a DecodeError, when it reads that error's Content. For one
// from Stream or Send, Body is open and must be closed. Its Request is the last
// request sent, after any redirects, with the credentials the client added
// removed, from its URL, its header fields and its Referer, and so is every
// earlier request reachable from it.
type Response struct {
	*http.Response

	// Declaration is the operation's response Message that governs
	// StatusCode: the one whose Key is the exact code ("201"), else its
	// range ("2XX"), else "default". It is nil when the operation declares
	// nothing for the status, which is not in itself an error. A lowercase
	// range such as "2xx" is not a range key: it is reported on its
	// Message, and never governs. It is the same immutable descriptor
	// Operation.Responses exposes.
	Declaration *Message

	// Media is the Declaration's Media that the Content-Type matches, or
	// nil when none does. A Media matches when its Type's type and subtype
	// equal the Content-Type's, compared without regard to case, or cover
	// them as a range does, and every parameter it names is present with an
	// equal value: parameter names are compared without regard to case, and
	// values after removing quoted-string quoting, a charset without regard
	// to case and others exactly. The most specific match wins: a concrete
	// type over type/*, type/* over */*, then more parameters over fewer; a
	// tie matches none. An absent Content-Type is treated as
	// application/octet-stream for matching; a repeated Content-Type
	// matches none. A Swagger 2.0 response with a schema that declares no
	// produces has one Media with an empty Type, which matches any
	// Content-Type, as */* would. It is the same immutable descriptor
	// Declaration.Media exposes.
	Media *Media

	// Security is the Key of the security alternative applied, "{}" for the
	// anonymous one, or empty when the operation takes no credentials.
	Security string
}

// Decode reads r's open Body into out, using Call's target, codec,
// empty-body and MaxBodyBytes rules for any HTTP status. After a successful
// read to EOF, it waits for any outstanding request body to finish before
// closing the response body; reading the response first permits a finite
// duplex peer to make progress. A caller that needs the response while an
// upload remains open uses Body directly or Stream instead. Decode applies
// no success-status policy and never returns a StatusError. A failure to
// read or decode is a *DecodeError holding r, even for a non-2xx status.
// An invalid out is a *DecodeError without consuming or closing Body, so
// the caller may retry with a valid target or read the raw bytes.
// Call it before reading Body directly or through Items or Events; the
// body can be consumed only once. An outstanding upload error is joined to
// a decode error, or returned alone, and is also available from WaitRequest.
// A failed or bounded response read may close Body early and abort an upload.
// Stream followed by Decode, its target chosen for the response, is Call.
func (r *Response) Decode(out any) error {
	panic("unimplemented")
}

// WaitRequest waits until the HTTP transport has consumed the complete
// request body or stopped consuming it. It returns nil for a bodyless
// request or when the body reached EOF, or the encoding, iterator, read,
// premature-close or cancellation error that stopped it. A write error
// reported by RoundTrip is returned by Send; a general RoundTripper does
// not expose when bytes are written to the network. A nil result here
// therefore proves body consumption, not delivery or server acceptance.
// The wait is safe to repeat and to call concurrently.
// A cancellation of ctx ends only this wait; cancel the call's original
// context or close Body to stop an outstanding upload. A caller of Send or
// Stream should read or close Body concurrently when the peer needs that
// progress before it can read the rest of the request.
func (r *Response) WaitRequest(ctx context.Context) error {
	panic("unimplemented")
}

// OperationFromContext returns the operation being sent when ctx is the
// context of a request the client sends, and nil otherwise, including for
// requests a credential source makes. It lets an http.RoundTripper label
// traces and metrics by Operation.Key, or decide whether a retry is safe.
// It does no work beyond the lookup.
func OperationFromContext(ctx context.Context) *Operation {
	panic("unimplemented")
}
