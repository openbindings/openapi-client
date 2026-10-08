package openapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
)

// A Client calls the operations of one loaded document. It is safe for
// concurrent use and never changes once made. The zero Client has no
// operations: it describes nothing and refuses every call with a
// *RequestError wrapping ErrNoOperation.
type Client struct {
	doc *document // shared by derived Clients
	cfg *config
}

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
	// Values are substituted as given, within the part of the URL their
	// variable occupies. Each variable is placed by where its default falls
	// in the URL with every default substituted. A variable whose default
	// spans "://", or that is the whole URL template, supplies a whole URL
	// and is not restricted. An empty default at the boundary between two
	// parts may take a value belonging to either. Otherwise a value may
	// change only its own part: in the scheme, the resulting
	// scheme must be one (RFC 3986 section 3.1); in the authority, a value
	// may not hold "/", "?", "#", "@" or "\\", though it may change the
	// host (a document restricts that with an enum); in the path, a value
	// may not change the scheme or authority, add a query or fragment, or
	// form a whole "." or ".." segment, percent-encoded or not (sections
	// 3.2, 5.2.4 and 6.2.2.2).
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
	// sequential, multipart or application/x-www-form-urlencoded type, whose
	// framing and field encoding stay the client's, as OpenAPI's Encoding
	// Object governs them; their items and parts use the codec for their own
	// type. Sequential response items use application/json for JSON Lines
	// and SSE event objects, or application/json or the corresponding +json
	// type for JSON sequences. Whole sequential responses are assembled as
	// JSON arrays and decoded once by that item-type codec, without decoding
	// and re-encoding each item. An Encode error refuses the call at the body's
	// or parameter's Inputs key, or aborts the body for an iterator's item; a
	// Decode error is a *DecodeError, or an ErrItem for one item. A key Load
	// would refuse, given through Client.With, refuses at Settings
	// "Options.Codecs" each call that encodes a value as a body or
	// content-serialized parameter, or whose out is a pointer to decode into
	// (not a *[]byte or an io.Writer). Unless the response has no body (see
	// Client.Call), it also makes Response.Decode and StatusError.Decode into
	// such a pointer return a *DecodeError naming Options.Codecs, and Items,
	// for a T other than []byte or *multipart.Part, yield such an error, not
	// wrapping ErrItem, as its only result.
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
	//
	// The bound counts bytes before value decoding: a JSON line without its
	// terminating LF or CRLF; a JSON-sequence record without RS, including
	// its JSON whitespace; or an SSE block's nonblank lines and their line
	// endings, including ignored fields and comments, but not its terminating
	// empty line or the stream's optional leading UTF-8 BOM. Each empty SSE
	// line resets the count, even when no event is dispatched. A decoded
	// multipart part counts its body after transfer decoding, excluding its
	// headers and boundaries; any other body counts in full. MaxBodyBytes
	// does not additionally bound Items or Events. Whole-response decoding
	// uses MaxBodyBytes on the response bytes, without an item bound.
	MaxItemBytes int64

	// NameOnlyEmpty sends "" for a Swagger 2.0 query or formData parameter
	// that allows empty values as its name alone (?flag), instead of the
	// default, its name and an equals sign (?flag=).
	NameOnlyEmpty bool

	// DeepObjectArrays says how the deepObject style writes an array as or
	// inside its value, which OpenAPI leaves undefined, wherever the client
	// writes a value in that style. An array it does not write, every array
	// when it is zero, is refused at the value's key in RequestError.Inputs,
	// and RequestError.Settings names "Options.DeepObjectArrays". See
	// [DeepObjectArrays].
	DeepObjectArrays DeepObjectArrays
}

// A Codec encodes values to, and decodes them from, one media type. The
// client may call its methods concurrently. Where one applies is on
// Options.Codecs.
type Codec interface {
	Encode(w io.Writer, v any) error
	Decode(r io.Reader, v any) error
}

// Redirects says which 3xx responses the client follows. Only 301, 302, 303,
// 307 and 308 with a Location that url.Parse accepts can be followed, as
// net/http follows them. A 303 is followed with GET (HEAD stays HEAD) and no
// body. A 301 or 302 changes POST to GET with no body, and keeps any other
// method and its body, as 307 and 308 do. A hop that drops the body drops
// Content-Type and the other content fields, and a hop that must resend a body
// that cannot be sent again (see Input.Body) is not followed. When the server
// answered before reading a body that the hop then drops, the upload is
// incomplete, and Call reports it with the final response (see WaitRequest). A
// 3xx not followed is the outcome, a *StatusError. The client adds no Referer.
// A Host set on Request.HTTP is kept on a hop whose Location has no authority,
// and dropped where the Location names one, a network-path reference
// (//host/path) included, unlike net/http. When the HTTPClient has a Jar, the
// Cookie field a hop carries drops each pair whose name the 3xx sets, leaving
// that cookie to the Jar, as net/http's Client does.
//
// The HTTPClient's CheckRedirect is still consulted on every hop the client
// follows, after the client applies the other rules here and before it places
// credentials on the hop, and can restore a field the caller deliberately wants
// to forward. The earlier requests it receives in via hold no credential the
// client placed either, and the hop's body it sees is a copy the client takes
// with GetBody, which the client closes unless the hop sends it. With a nil
// CheckRedirect, the chain stops after 10 requests, as net/http's default does.
//
// On a hop to another origin (scheme, host and port, a scheme's default port
// being the same as none, and ASCII letters compared without regard to case but
// every other byte exactly), the client removes the credentials it added and
// any header a security scheme placed, the Authorization and Cookie fields
// (cookie parameters included), all header parameters, and every field supplied
// through Options.Header, Input.Header, or an edit to Request.HTTP.Header. A
// User-Agent that one of those removed, by an entry with no values, stays
// removed, so that net/http adds none. Generated fields needed to describe a
// replayed body, such as Content-Type and Content-Length, are rebuilt.
// CheckRedirect may restore a field intentionally, such as Range or Accept; the
// client does not infer whether an arbitrary caller header is a secret.
//
// On a hop within the origin, header, query and cookie credentials are placed
// again, a query credential after the Location's own query. Once a hop has left
// the call's origin, no later hop has credentials, header parameters or caller
// fields placed again, even one back on that origin. On every hop, before
// CheckRedirect sees it, the client removes from the Location's query each pair
// named as a query credential the call places (see Credentials in the package
// documentation), whatever its value. So a credential a server echoes there
// reaches no other origin, and is in the URL of no request that a Response,
// CheckRedirect or an error shows, though the 3xx's Location field still holds
// what the server sent. A credential can fail to be placed on a hop: a source's
// error, empty secret or value its destination cannot carry, or a bearer or
// Basic credential the hop would send over plain http or ws to a host other
// than a loopback one. That ends the call with a *url.Error, returned with the
// 3xx, its body closed; for a source's failure, the error names the scheme. A
// transport that satisfies a FromTransport scheme sees every hop, other origins
// included, and must apply its own origin policy.
type Redirects int

const (
	FollowNone Redirects = iota // follow none (the default)
	FollowAll                   // follow every eligible 3xx
)

// DeepObjectArrays says how the deepObject style writes an array as or inside
// its value, which OpenAPI leaves undefined, so that a caller states the
// convention its server reads rather than the client guessing one. Each item
// takes the array's name with a suffix; a member of an object item adds
// [member] after it. Indexes number the items written, from 0, so an item
// skipped as undefined takes none. Brackets are always written as %5B and %5D,
// and member names are percent-encoded as in any deepObject value (see
// Percent-encoding in the package documentation).
type DeepObjectArrays int

const (
	RefuseArrays  DeepObjectArrays = iota // refuse an array (the default)
	BracketArrays                         // a[]=1&a[]=2; an item that is an object or array is refused
	IndexArrays                           // a[0]=1&a[1]=2, and a[0][b]=x for an object item
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
	if f == nil {
		return c
	}
	cfg := c.cfg
	if cfg == nil {
		cfg = newConfig(Options{}, nil)
	}
	o := cfg.options()
	f(&o)
	return &Client{doc: c.doc, cfg: newConfig(o, cfg)}
}

// An Input holds the values and settings for one call. The client never
// modifies an Input. Call has stopped reading its body when it returns,
// provided a reader body returns from Read when the call's context ends
// or its connection closes; the client cannot interrupt a Read that blocks
// forever.
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

	// ParamWriters overrides the serialization of individual parameters, keyed
	// by Param.Key. A writer counts as supplying a required parameter; an
	// unknown key, a nil writer, and the same key in Params and ParamWriters,
	// are refused at that key. After the client serializes the other parameters
	// and the body, Prepare calls writers in the operation's parameter order
	// with its unsigned *http.Request. The writer may edit URL.Path,
	// URL.RawPath, URL.RawQuery, headers or cookies to implement a spelling the
	// built-in serializer cannot represent.
	// A path parameter's {name} remains in both URL.Path and URL.RawPath
	// until its writer replaces it in both, keeping RawPath an encoding of
	// Path; an unresolved path token after all writers refuses preparation.
	// Locate the token in RawPath: there other values are percent-encoded,
	// so their text cannot match it, as it can in Path.
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
	//   - For form and multipart media, Body is an object whose properties are
	//     the fields: a value whose JSON data is an object, such as a struct, a
	//     non-nil map or a non-nil pointer to either, but not a Part, a *Part,
	//     or a pointer to either or to a reader, which is refused at
	//     Inputs["Input.Body"]. A property may be a []byte, an io.Reader or a
	//     [Part] (or a non-nil *Part); a property whose value is an array sends
	//     one field or part per item under the property's name, unless its
	//     collectionFormat or Encoding style says otherwise, each item taking
	//     the property's content type (an array schema's items type by
	//     default); any other value is one field or part. A field an Encoding
	//     style or a Swagger 2.0 collectionFormat serializes, multi included,
	//     takes JSON data, under multipart/form-data too, so a []byte there is
	//     a base64 string and a Part or reader is refused. A typed nil is a
	//     value, never a reader, so a property or item holding one is omitted
	//     as null. A multipart object with no fields sends the close delimiter
	//     alone ("--" boundary "--" CRLF), as browsers do.
	//   - For OpenAPI 3.2 multipart/form-data, Body may instead be a list, a
	//     slice or array other than a byte slice (whose JSON data is a string),
	//     one part per element, in order: each a one-property object, whose
	//     property names the part and whose value is its content, encoded as
	//     that position's Encoding says, or a Part whose Header gives
	//     Content-Disposition, which takes a type its Encoding lists (see
	//     Part.MediaType) even where that Encoding sets a style.
	//   - For any other OpenAPI 3.2 multipart media type, Body may instead be
	//     a list, or an iterator as for a sequential type, one part per
	//     element, in order, whether or not the type declares prefixEncoding
	//     or itemEncoding: each a []byte, an io.Reader, a Part, or a value
	//     encoded by that part's media type as that value on its own would
	//     be, so a list and an iterator yielding the same values send the
	//     same parts. Such a part has no Content-Disposition or filename
	//     unless its Part sets them. Under these types and OpenAPI 3.2
	//     multipart/form-data, a non-nil pointer to a list sends what the list
	//     it points to sends, and a Body whose JSON data is null, as a nil
	//     slice's is, has no parts: it is the close delimiter alone.
	//   - A part whose media type is multipart is encoded, one level deep,
	//     from an object or list by its Encoding's own encoding,
	//     prefixEncoding or itemEncoding; a []byte or io.Reader supplies it
	//     pre-encoded, with its boundary in Part.MediaType. A field of an
	//     application/x-www-form-urlencoded body whose media type is multipart,
	//     and a field or part whose media type is sequential, are never encoded
	//     from a value: they take only a value whose type is exactly string or
	//     []byte, or an io.Reader.
	//   - For a sequential media type (JSON Lines, JSON text sequences,
	//     server-sent events), in any edition, Body is a list, an iter.Seq, or
	//     an iter.Seq2 whose second value is an error, of any element type;
	//     each element is one item, encoded as that value on its own would be,
	//     so a list and an iterator yielding the same values send the same
	//     bytes; a non-nil pointer to a list sends what the list it points to
	//     sends, and a Body whose JSON data is null, as a nil slice's is, has
	//     no items. A nil iterator is refused at Inputs["Input.Body"]. Under
	//     text/event-stream an item is an object with no members but data,
	//     event and id, as strings, and retry, as a non-negative integer, or an
	//     [Event] (or a non-nil *Event), of which only the fields it sets are
	//     used. It is written as those fields, each as a "field: value" line
	//     ending in LF, data as one data line per line (split at CRLF, LF or
	//     CR), then a blank line; any other member or type, a line break in
	//     event or id, a NUL in id, or a retry that is not whole milliseconds,
	//     is an item that cannot be encoded; invalid UTF-8 is written as
	//     U+FFFD, as an event stream is UTF-8. Under JSON Lines and JSON text
	//     sequences, a []byte or io.Reader item is the item's JSON text,
	//     written as given and framed (a JSON Lines item is followed by LF; a
	//     sequence item has RS before it and LF after); one holding the
	//     framing's separator (LF or CR, or RS) cannot be encoded, and a
	//     codec's output is framed after its trailing JSON whitespace is
	//     trimmed. An iterator is written one item at a time as it yields, so a
	//     large body is never held, and each item reaches the connection on its
	//     own; for many small items a slice, or a reader the caller frames, is
	//     faster. It runs on a goroutine of the transport, from the transport's
	//     first Read of the body, so a body closed unread never runs it; its
	//     yield returns false once the body is no longer wanted. Call waits for
	//     the iterator to return; Send and Stream may return at response
	//     headers while it is still running. An error from an iter.Seq2, an
	//     item that cannot be encoded, or the context ending before the
	//     iterator returns aborts the body and is reported by Call or
	//     Response.WaitRequest. An iter.Seq[any], an iter.Seq2[any, error], or
	//     an io.Reader the caller frames avoids per-item reflection.
	//   - Any other value is written by the media type's codec (see Values in
	//     the package documentation). Under a JSON type,
	//     json.RawMessage("null") sends null, and bytes go as a base64
	//     string: give the string, or the bytes as a named byte-slice type,
	//     which encoding/json writes in base64. In Swagger 2.0, the body
	//     parameter is Body itself.
	//
	// A body can be sent again, by a redirect, a retry or a second send of a
	// Request, when every source in it can: a []byte, a *bytes.Buffer, a
	// *bytes.Reader and a *strings.Reader, from the bytes they hold when the
	// call is prepared, without being drained (the client may keep those bytes
	// rather than a copy, so the caller must not change them while the Request
	// may still send them); an *os.File that Stat reports to be a regular file,
	// from its offset when the call is prepared; and every value the client
	// encodes. Any other reader, such as a pipe or an os.Stdin that is not a
	// regular file, is read once, and so is an iterator. A reader is read as
	// the body is sent, and again on each replay that resends it, except that
	// in an application/x-www-form-urlencoded body a field's reader that can be
	// sent again is read when the call is prepared (see Form bodies in the
	// package documentation). Content that cannot be encoded, such as a given
	// boundary's delimiter in a part or a separator in a sequence's item,
	// refuses the call when it is prepared, unless a reader or an iterator
	// supplies it; then it is found only as it is sent, and aborts the body, as
	// an iterator's error does. A regular file that yields fewer bytes than
	// Stat reported fails the call with an error wrapping io.ErrUnexpectedEOF
	// rather than send a short body.
	//
	// A Part or io.Reader inside a JSON value is refused with an Inputs entry
	// at its place in Body, unless its own MarshalJSON or MarshalText encodes
	// it. A []byte nested in a JSON value is encoded by encoding/json as a
	// base64 string; in a form or multipart value it remains raw part bytes. A
	// nil Body where the request body is required is refused at
	// Inputs["Input.Body"], and a missing required Swagger 2.0 formData field
	// in a structured form body at "Input.Body" followed by its pointer, as the
	// parameter it is. A pre-encoded body bypasses those field checks. The
	// client never closes a reader it is given. A body for an operation that
	// takes none is refused at Inputs["Input.Body"]; to send one anyway,
	// Prepare the call and set the body on Request.HTTP.
	Body any

	// MediaType is the body's media type: a concrete type matching one the
	// operation declares, by the rules on Response.Media; any concrete type
	// where Swagger 2.0 declares none; or, for Swagger 2.0 formData with no
	// form type declared, either form type. When an OpenAPI 3.x requestBody has
	// an empty content map, a pre-encoded []byte or io.Reader body may use any
	// concrete type given here; there is no governing Media descriptor or
	// structured encoder. A range is refused. MediaType may carry parameters,
	// which are sent as given: a pre-encoded multipart body requires its
	// boundary here, and a boundary given for a multipart body the client
	// encodes is used, a part whose content holds its delimiter, or "--" and
	// the boundary after a CR or LF, being an input that cannot be encoded (RFC
	// 2046 section 5.1.1; in a reader, it is found only as the body is sent, as
	// Body says). A boundary in a request body's declared content key is used
	// and checked the same way; an invalid one is the Media's Err. Two boundary
	// parameters in a multipart type are refused. Empty means
	// Options.MediaType, else the declared type where one selects itself (see
	// the package documentation); otherwise a body requires MediaType before
	// dispatch.
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

// A Part is one field of a form or multipart body, or one part of a positional
// multipart body, given where its media type, filename or part header fields
// matter. An empty field means its default. A named part's Content-Disposition
// is form-data, under any multipart type, with its name and filename written as
// given, each as a quoted-string with \ and " escaped, never as filename*; a
// control character other than a tab in either is refused, as a quoted-string
// cannot carry it. In an application/x-www-form-urlencoded body only Content
// and MediaType apply, and Filename, NoFilename or Header is refused.
type Part struct {
	// Content is the part's value: a []byte or an io.Reader for raw content, a
	// string, or any other value, encoded by MediaType. A nil Content, or one
	// whose JSON data is null, omits the part, as a null property is omitted,
	// whatever the part's media type. In an OpenAPI 3.2 multipart/form-data
	// list, which takes a Part only when its Header gives Content-Disposition
	// (see Input.Body), a Part without one is refused at its key whatever its
	// Content.
	Content any

	// MediaType is the part's Content-Type: a concrete type matching, by the
	// rules on Response.Media, one its Encoding lists, or, where the Encoding
	// lists no type (as always in Swagger 2.0), any concrete type. A range is
	// refused. Empty uses the part's type where one selects itself (see the
	// package documentation); otherwise the call requires MediaType. A
	// multipart type's boundary is used and checked as Input.MediaType says.
	MediaType string

	// Filename is sent in the part's Content-Disposition. Empty means the
	// default: the part's name for a []byte or io.Reader Content whose media
	// type is not multipart, and none otherwise. A part without a name, as in
	// positional multipart, takes its Content-Disposition only from Header, and
	// Filename is refused on it.
	Filename string

	// NoFilename sends no filename, whatever the Content. Setting it with
	// Filename is refused.
	NoFilename bool

	// Header holds other header fields of the part, such as those its Encoding
	// declares, under the rules for Options.Header's field names and values. A
	// Content-Disposition field replaces the one the client writes;
	// Content-Type, in any spelling, is refused (set MediaType). The part's
	// header is written in this order: the client's Content-Disposition, unless
	// replaced; Content-Type; then these fields, a replacing
	// Content-Disposition included, under their canonical names, sorted by
	// name, each value on its own line.
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
// out must be nil, a non-nil *[]byte, an io.Writer, or a non-nil pointer;
// anything else, a nil pointer of any type included, is refused before
// sending. For a 2xx, the body is read to the
// end and closed before Call returns, and out receives it:
//
//   - nil discards it, reading at most MaxBodyBytes before closing the
//     connection.
//   - A *[]byte receives the raw bytes, appended to (*p)[:0] so its
//     capacity is reused, bounded by MaxBodyBytes. It stays nil when the
//     body is empty and the slice was nil, which tells an empty body from
//     JSON null.
//   - An io.Writer receives the raw bytes as they arrive, unbounded, without
//     holding them in memory.
//   - Any other pointer receives the body, read in full within
//     MaxBodyBytes and decoded once, with no intermediate value, by the
//     caller's codec for its media type (see Options.Codecs), or else by
//     its codec class (see Values in the
//     package documentation): JSON types (and a sequential type, as a
//     JSON array of its items) with encoding/json, anything but whitespace
//     after the value being a failure, as for json.Unmarshal; XML types
//     with encoding/xml, which ignores json tags, and which reads UTF-8,
//     US-ASCII and ISO-8859-1 documents, taking the encoding from a byte
//     order mark, else the Content-Type's charset, else the document's own
//     declaration. A *string and a *any take any text/* type as text,
//     whatever its codec class (text/xml and text/event-stream included):
//     a *string its bytes as sent, the charset left in the Content-Type,
//     and a *any a string; a *any takes any other non-JSON type, multipart
//     included, as a []byte. A missing, repeated or unparsable
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
// A 1xx, 204, 205 or 304 response, a response to HEAD, and a 2xx response to
// CONNECT have no body: Call, Stream, StatusError and Response.Decode do not
// read one, and Send keeps a 101 or a tunnel open. An empty body decoded into a
// pointer is a *DecodeError wrapping io.EOF under a JSON or XML type when the
// response can have a body and its governing Message has Media (in Swagger 2.0,
// a schema). Otherwise it leaves out as it was under a JSON or XML type, or a
// type with a caller's codec, which is not called; under any other type, it
// decodes as "" into a *string or *any for a text/* type, as an empty array for
// a sequential type (a *DecodeError for an out that cannot hold one), and as an
// empty []byte into a *any for any other type, leaving any other out as it was.
// A body that fails to read, to decode, or to be written to out is a
// *DecodeError, and out may be partly filled. Any other final status is a
// *StatusError, and out is untouched. A call refused before sending returns a
// nil Response and a *RequestError.
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
	o, err := c.operation(key)
	if err != nil {
		return nil, err
	}
	x := &exchange{Context: ctx, cfg: c.cfg, op: o}
	re := RequestError{Err: o.Err}
	c.cfg.checkOut(out, &re)
	req, p, _, sec := c.newRequest(x, o, in, &re)
	c.cfg.checkAccept(o, req.Header, out, &re)
	if err := re.refused(); err != nil {
		return nil, err
	}
	x.selection = sec
	x.attach(req, p)
	return x.call(req, out)
}

// Prepare builds the request for the operation named key with in, applying
// OpenAPI's rules and the caller's settings, and sends nothing. It never calls
// a credential source, and reads no reader or iterator given in in, except, in
// an application/x-www-form-urlencoded body, a field's reader that can be sent
// again, which it reads without draining (see Input.Body). When the call cannot
// be sent, Prepare returns a *RequestError listing every problem at once. It
// does not require schema validation or a structured body encoder when in.Body
// is pre-encoded bytes or a reader. A known parameter's ParamWriters entry
// bypasses its built-in serializer. These escapes run before a Request exists;
// editing Request.HTTP is a later option, only after preparation succeeds. A
// missing operation, or an operation with Err set, still prevents preparation.
func (c *Client) Prepare(key string, in *Input) (*Request, error) {
	o, err := c.operation(key)
	if err != nil {
		return nil, err
	}
	pr := &prepared{Context: context.Background(), cfg: c.cfg, op: o}
	re := RequestError{Err: o.Err}
	req, p, media, sec := c.newRequest(pr, o, in, &re)
	if err := re.refused(); err != nil {
		return nil, err
	}
	pr.selection, pr.payload = sec, p
	setBody(req, &pr.payload)
	if sec.places {
		pr.origin = &url.URL{Scheme: req.URL.Scheme, Host: req.URL.Host}
	}
	return &Request{HTTP: req, Media: media, Security: sec.key()}, nil
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
// closing Response.Body or canceling the call context stops it, except for
// the unchanged upgrade and tunnel bodies described below.
// A custom HTTPClient.Transport can also handle a Swagger 2.0 ws or wss
// URL. Send exposes the resulting status and body without imposing
// WebSocket framing; a 101 upgrade is returned as-is.
// Transport errors retain their usual uncertainty about
// whether the server received the request; a pre-dispatch refusal is a
// *RequestError. This is the lower-level path for callers whose response
// policy differs from Call and Stream. For a CONNECT operation, a custom
// HTTPClient.Transport can return a Body implementing io.ReadWriteCloser
// for tunnel use; the client leaves that body unchanged.
// Closing an unchanged upgrade or tunnel body has the transport's behavior;
// cancel the original call context to signal an outstanding upload to stop.
func (r *Request) Send(ctx context.Context) (*Response, error) {
	x, req, err := r.newExchange(ctx)
	if err != nil {
		return nil, err
	}
	return x.streamResponse(req, false)
}

// Call sends r with ctx, adding credentials, and returns as [Client.Call]
// does. When r's body can be sent again (HTTP.GetBody is set, or there is
// no body), r may be sent any number of times, concurrently too. Otherwise
// it may be sent once, and sending it again is refused with a
// *RequestError, nothing sent; a send refused with a *RequestError does not
// count.
func (r *Request) Call(ctx context.Context, out any) (*Response, error) {
	var re RequestError
	if r.HTTP != nil {
		if pr, ok := r.HTTP.Context().Value(preparedKey{}).(*prepared); ok {
			pr.cfg.checkOut(out, &re)
			pr.cfg.checkAccept(pr.op, r.HTTP.Header, out, &re)
		}
	}
	if err := re.refused(); err != nil {
		return nil, err
	}
	x, req, err := r.newExchange(ctx)
	if err != nil {
		return nil, err
	}
	return x.call(req, out)
}

// A Response is a response the server sent to a call: the *http.Response,
// with what the operation declares about it.
//
// For a Response from Call, Body has been read and closed, except with a
// StatusError or a DecodeError, when it reads that error's Content, as it does
// for a StatusError from Stream. Otherwise, for one from Stream or Send, Body
// is open and must be closed, unless an error ended a redirect chain, which
// closed it (see Outcomes in the package documentation). Its Request is the
// last request sent, after any redirects, without the credentials the client
// added to its URL and header fields or the cookies the HTTPClient's Jar
// supplied, and so is every earlier request reachable from it. The responses in
// that chain hold what the server sent.
type Response struct {
	*http.Response

	// Declaration is the operation's response Message that governs
	// StatusCode: the one whose Key is the exact code ("201"), else its
	// range ("2XX"), else "default". It is nil when the operation declares
	// nothing for the status, or the status is outside 100 to 599, which is
	// not in itself an error. A lowercase
	// range such as "2xx" is not a range key: it is reported on its
	// Message, and never governs. It is the same immutable descriptor
	// Operation.Responses exposes.
	Declaration *Message

	// Media is the Declaration's Media that the Content-Type matches, or nil
	// when none does. A Media matches when its Type's type and subtype equal
	// the Content-Type's, compared without regard to case, or cover them as a
	// range does, and every parameter it names is present with an equal value:
	// parameter names are compared without regard to case, and values after
	// removing quoted-string quoting, a charset without regard to case and
	// others exactly. The most specific match wins: a concrete type over
	// type/*, type/* over */*, then more parameters over fewer; a tie matches
	// none. An absent Content-Type is treated as application/octet-stream for
	// matching; a repeated one, or one outside RFC 9110's media-type grammar,
	// matches none. That grammar governs every media type the client reads (a
	// content key, an Encoding contentType, Input.MediaType, Part.MediaType),
	// and an Encoding contentType that lists several is split at commas outside
	// quoted strings. A Swagger 2.0 response with a schema that declares no
	// produces has one Media with an empty Type, which matches any
	// Content-Type, as */* would. It is the same immutable descriptor
	// Declaration.Media exposes.
	Media *Media

	// Security is the Key of the security alternative applied, "{}" for the
	// anonymous one, or empty when the operation takes no credentials.
	Security string
}

// Decode reads r's open Body into out, using Call's target, codec, empty-body
// and MaxBodyBytes rules for any HTTP status. After a successful read to EOF,
// it waits for any outstanding request body to finish before closing the
// response body; reading the response first permits a finite duplex peer to
// make progress. A response that has no body (see Call) counts as read to EOF:
// Decode reads nothing, leaves out as it was, and closes Body after that wait,
// an upgrade or tunnel body included. A caller that needs the response while an
// upload remains open uses Body directly or Stream instead. Decode applies no
// success-status policy and never returns a StatusError. A failure to read or
// decode is a *DecodeError holding a copy of r, even for a non-2xx status. An
// invalid out is a *DecodeError without consuming or closing Body, so the
// caller may retry with a valid target or read the raw bytes. Call it before
// reading Body directly or through Items or Events; the body can be consumed
// only once. An outstanding upload error is joined to a decode error, or
// returned alone, and is also available from WaitRequest. A failed or bounded
// response read may close Body early and abort an upload. Stream followed by
// Decode, its target chosen for the response, is Call.
func (r *Response) Decode(out any) error {
	if err := checkOut(out); err != nil {
		return invalidDecodeError(r, err)
	}
	return exchangeOf(r.Response).decode(r, mediaOf(r.Header), out)
}

// WaitRequest waits until the HTTP transport has consumed the complete request
// body or stopped consuming it, for every request of the call that carried one:
// the first, each copy the transport takes with GetBody to send it again, and
// each redirect hop that sent it again. It reports on the last of them: nil
// when its body was consumed completely (read to EOF, or, for a body of known
// length, read to that length, reading past a declared ContentLength being an
// error), or the encoding, iterator, read, premature-close or cancellation
// error that stopped it. It returns nil when no request carried a body. A write
// error reported by RoundTrip is returned by Send; a general RoundTripper does
// not expose when bytes are written to the network. A nil result here therefore
// proves body consumption, not delivery or server acceptance. The wait is safe
// to repeat and to call concurrently. It relies on the transport closing the
// request body, as http.RoundTripper requires, and every copy it takes with
// GetBody; with a transport that neither reads nor closes it, the wait, and
// Call's, ends only with the context. When ctx ends first, WaitRequest returns
// an error matching ctx.Err() and context.Cause(ctx), and that ends only this
// wait; cancel the call's original context or close Body to stop an outstanding
// upload. For Send's unchanged upgrade and tunnel bodies, use the original
// context; their Close behavior belongs to the transport. A caller of Send or
// Stream should read or close Body concurrently when the peer needs that
// progress before it can read the rest of the request.
func (r *Response) WaitRequest(ctx context.Context) error {
	if x := exchangeOf(r.Response); x != nil {
		return x.waitUpload(ctx)
	}
	return nil
}

// OperationFromContext returns the operation being sent when ctx is the
// context of a request the client sends, and nil otherwise, including for
// requests a credential source makes. It lets an http.RoundTripper label
// traces and metrics by Operation.Key, or decide whether a retry is safe.
// It does no work beyond the lookup.
func OperationFromContext(ctx context.Context) *Operation {
	if o, ok := ctx.Value(operationKey{}).(*operation); ok {
		return o.Operation
	}
	return nil
}
