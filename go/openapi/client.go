package openapi

import (
	"context"
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

// Options says where calls go, as whom, and how. The zero value means every
// default. The Options given to [Load] apply to every call of the Client,
// and Load refuses those the document cannot use; [Client.With] derives a
// Client with different ones, for a tenant or for one call.
type Options struct {
	// HTTPClient sends every request, and fetches documents while loading.
	// Nil means http.DefaultClient. It is used as given and never modified.
	// Its Transport is the place for tracing, metrics, retries, proxies and
	// client certificates. Prefer a context deadline to its Timeout, which
	// also ends a Stream mid-body.
	HTTPClient *http.Client

	// Server selects one of the operation's servers, by its URL as written
	// (with its {variables}) or by its OpenAPI 3.2 name, as
	// Operation.Servers lists them. Empty means the default. A value that
	// matches two servers (one's name, another's URL) is refused as
	// ambiguous; one that matches no server of the document is refused by
	// Load; and a call to an operation that does not list it is refused.
	//
	// Credentials go wherever the call's server is. When the document is not
	// trusted to choose, set Server or BaseURL.
	Server string

	// BaseURL, if set, replaces every server of every operation: an absolute
	// http or https URL, joined to the operation's path as the package
	// documentation says. Server must then be empty, and Variables are not
	// used.
	BaseURL string

	// Variables gives values for server variables, by name. A variable
	// without a value takes its declared default. A value outside the
	// variable's enum refuses the call. A name that appears in no server URL
	// of the document is refused by Load, as a likely misspelling. Values
	// are substituted as given.
	Variables map[string]string

	// Credentials holds a Credential for each security scheme, by the name
	// a security requirement uses for it. The rules for their use and
	// refusal are in the package documentation, under Credentials. A
	// mutualTLS scheme needs no entry.
	Credentials map[string]Credential

	// Security prefers, for every operation that offers it, the security
	// alternative whose schemes are exactly these, whatever scopes each
	// operation asks for; the first such alternative wins, and a missing
	// credential for it refuses the call rather than falling back. An
	// operation that offers no such alternative takes the default. A set of
	// names no alternative of the document uses is refused by Load.
	// Input.Security overrides it for one call.
	Security []string

	// Redirects says which redirects the client follows; see [Redirects].
	Redirects Redirects

	// Header holds header fields for every request, applied by the rules in
	// the package documentation, under Fixed rules. How its fields fare on a
	// redirect is on [Redirects].
	Header http.Header

	// MaxBodyBytes bounds a 2xx body decoded into a value, read into a
	// *[]byte, or discarded for a nil out. Zero means 64 MiB, and a negative
	// value means no limit. A longer body is a *DecodeError wrapping
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

// Redirects says which 3xx responses the client follows. A 3xx is declared
// when the operation's responses have its exact code, or the 3XX range, as
// a key; a default response never declares one. Only 301, 302, 303, 307
// and 308 with a Location can be followed, as net/http follows them, and a
// 307 or 308 whose request body cannot be sent again (see Input.Body) never
// is. A 3xx not followed is the outcome, a *StatusError. The HTTPClient's
// CheckRedirect is still consulted on every hop the client follows.
//
// On a hop to another origin (scheme, host and port), the client removes
// the credentials it added and any header a security scheme placed, the
// Authorization and Cookie fields (cookie parameters included), and every
// field from Options.Header, since client-level headers are where keys for
// a gateway the document does not declare live; a generated field one
// replaced comes back. Fields from Input.Header, and fields edited into
// Request.HTTP, go on as net/http carries them, such as Range,
// Last-Event-ID or a per-call Accept, except that a Referer holding a query
// credential the client added, which net/http sets from the previous URL,
// is removed. On a hop within the origin, header and cookie credentials are
// placed again; a query credential goes only on the request the client
// builds, never onto a Location. A transport that satisfies a FromTransport
// scheme sees every hop, other origins included.
type Redirects int

const (
	FollowUndeclared Redirects = iota // follow every 3xx the operation does not declare (the default)
	FollowAll                         // follow every 3xx, declared or not
	FollowNone                        // follow none: every 3xx is the outcome
)

// With returns a Client that shares c's document, with c's Options changed
// by f. f receives a copy: its maps are always non-nil and belong to the new
// Client, so f may add, replace or delete entries, and may reset any field
// to its zero value to restore the default. Nothing f does affects c. With
// is cheap enough to call for each tenant or each call, and the new Client
// keeps a copy of the Options f leaves, maps and slices included. With(nil)
// returns c. Since With returns no error, Options the document cannot use
// are refused when the derived Client calls, rather than here.
func (c *Client) With(f func(*Options)) *Client {
	panic("unimplemented")
}

// An Input holds the values and the choices for one call. The client never
// modifies or retains an Input, and has stopped reading its body when Call
// returns, or, for Stream, when the response Body is closed; the Input may
// then be reused, for example across the pages of a list. A body that can
// be read only once is consumed by its first call. A nil *Input is an empty
// one.
type Input struct {
	// Params holds parameter values by Param.Key. A parameter's key is its
	// name, unless another parameter of the operation has the same name, or
	// the name is empty, begins with "/", or begins with a location and a
	// dot (as "query.id" does); then its key is its location, a dot and its
	// name: "query.id", "header.query.id", "query./photo". So no two
	// parameters share a key, and none looks like a body pointer in
	// RequestError.Inputs. A key the operation does not declare, or a
	// missing required parameter, refuses the call; a header parameter
	// supplied as an Options.Header or Input.Header field counts as given. In
	// Swagger 2.0, formData parameters are properties of Body.
	Params map[string]any

	// Body is the request body, or nil for none:
	//
	//   - A []byte, or an io.Reader, is sent as its bytes, under whatever
	//     media type is chosen, JSON types included.
	//   - For form and multipart media, Body is an object (a map or a
	//     struct) whose properties are the fields. A property may be a
	//     []byte, an io.Reader or a [Part]; an array property sends one field
	//     or part per item under the property's name.
	//   - For an OpenAPI 3.2 multipart media type declared with
	//     prefixEncoding or itemEncoding, Body is a slice: one part per
	//     element, in order, each a []byte, an io.Reader, a Part, or a value
	//     encoded by that part's media type.
	//   - For an OpenAPI 3.2 sequential media type (JSON Lines, JSON text
	//     sequences, server-sent events), Body is a slice, an iter.Seq, or an
	//     iter.Seq2 whose second value is an error, of any element type; each
	//     element is one item. An iterator is written one item at a time as it
	//     yields, so a large body is never held. It runs on a goroutine of the
	//     transport; its yield returns false once the body is no longer
	//     wanted, and the call returns only after it has returned. An error
	//     from an iter.Seq2, an item that cannot be encoded, or the context
	//     ending before the iterator returns, aborts the body so the server
	//     never receives a complete one, and the call returns a *url.Error
	//     wrapping that error. An iter.Seq[any], an iter.Seq2[any, error], or
	//     an io.Reader the caller frames avoids per-item reflection.
	//   - Any other value is encoded by the media type: JSON (use
	//     json.RawMessage("null") to send null), or text from a string. XML
	//     is sent only from a string or []byte. In Swagger 2.0, the body
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
	// A Part, []byte or io.Reader inside a value (a property, or an element)
	// is never JSON-encoded: where the media type is not multipart or form,
	// it is refused with an Inputs entry at its place in Body. A nil Body
	// where the request body is required is refused at Inputs[""], and a
	// missing required Swagger 2.0 formData field at its pointer, as the
	// parameter it is. The client never closes a reader it is given.
	Body any

	// MediaType is the body's media type: a concrete type matching one the
	// operation declares, by the rules on Response.MediaType; any concrete
	// type where Swagger 2.0 declares none; or, for Swagger 2.0 formData
	// with no form type declared, either form type. A range is refused. It
	// may carry parameters, such as the boundary of a multipart body given
	// as bytes, which are sent as given. Empty means the default (see
	// Choices in the package documentation).
	MediaType string

	// Security selects the security alternative by its
	// SecurityRequirement.Key, exactly as Operation.Security spells it,
	// "{}" for the anonymous alternative, overriding Options.Security. Empty
	// means Options.Security, or else the default. A key the operation does
	// not list refuses the call.
	Security string

	// Header holds header fields for this call, applied after
	// Options.Header by the same rules. How its fields fare on a redirect is
	// on [Redirects].
	Header http.Header
}

// A Part is one field of a form or multipart body, or one part of a
// positional multipart body, given where its media type, filename or part
// header fields matter. An empty field means its default.
type Part struct {
	// Content is the part's value: a []byte or an io.Reader for raw
	// content, a string, or any other value, encoded by MediaType.
	Content any

	// MediaType is the part's Content-Type: a concrete type matching one its
	// Encoding lists, or, where the Encoding lists no type (as always in
	// Swagger 2.0), any concrete type. A range is refused. Empty
	// means the default (see Choices in the package documentation).
	MediaType string

	// Filename is sent in the part's Content-Disposition. Empty means the
	// default: the property name for a []byte or io.Reader Content, and
	// none for anything else.
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
//     connection by its media type: JSON types (and a sequential type, as a
//     JSON array of its items) with encoding/json, anything but whitespace
//     after the value being a failure, as for json.Unmarshal; XML types
//     with encoding/xml, which ignores json tags, and which reads UTF-8,
//     US-ASCII and ISO-8859-1 documents; text into a *string, its bytes as
//     sent, the charset left in the Content-Type; and into a *any, text as a
//     string and any other non-JSON type as a []byte. A missing Content-Type
//     is taken to be the governing response's only declared type, if it has
//     one; otherwise it is application/octet-stream, which a *any receives
//     as a []byte and a typed target cannot. A type these rules cannot
//     decode into out is a *DecodeError, whose Content keeps the start of
//     the bytes; a *[]byte takes any body as it is.
//
// An empty body is a success for every out, except that a pointer to decode
// into is a *DecodeError wrapping io.EOF when the operation declares content
// for the status (not a 204, a 304, or a HEAD request). A body that fails to
// read, to decode, or to be written to out is a *DecodeError, and out may be
// partly filled. Any other final status is a *StatusError, and out is
// untouched. A call refused before sending returns a nil Response and a
// *RequestError.
//
// Call is Prepare followed by [Request.Call], without building the
// records.
func (c *Client) Call(ctx context.Context, key string, in *Input, out any) (*Response, error) {
	panic("unimplemented")
}

// Prepare builds the request for the operation named key with in, making
// every decision Call would make, and sends nothing. It never calls a
// credential source, and never reads a reader or iterator given in in. When
// the call cannot be sent, Prepare returns a *RequestError listing every
// problem at once.
func (c *Client) Prepare(key string, in *Input) (*Request, error) {
	panic("unimplemented")
}

// A Request is a call prepared by [Client.Prepare] and not yet sent. A
// Request that Prepare did not make, the zero Request included, is refused
// with a *RequestError wrapping ErrNoOperation.
type Request struct {
	// HTTP is the request that will be sent: method, URL, parameters,
	// Accept, Content-Type and body, but no credentials, which are added
	// when it is sent. Its context carries the operation for
	// OperationFromContext and is replaced by the one given to Call or
	// Stream.
	//
	// HTTP may be changed before sending, to set a header the document
	// cannot express, say. A send takes HTTP.Body the first time and
	// GetBody for every replay, as net/http does, so a caller who replaces
	// the body sets GetBody and ContentLength with it, or clears GetBody.
	// GetBody is set when the body can be sent again (see Input.Body). If
	// the URL is changed to another origin and the call needs credentials,
	// sending it is refused; set Options.BaseURL instead.
	HTTP *http.Request

	// MediaType is the Type of the operation's request body Media that
	// governs the body, as Response.MediaType is for a response: the
	// declared type or range the Content-Type matched, whose schema and
	// Encoding the body follows. It is empty when there is no body.
	MediaType string

	// Choices records each decision the document left open for this call,
	// in the order made: the server when several are listed, a variable
	// without a default, the security alternative when several are offered,
	// and the media type of the body and of each part where the document
	// leaves it open. A call whose Choices have no Default set is sent as the
	// caller specified.
	Choices []Choice
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
// from Stream, Body is open and must be closed. Its Request is the last
// request sent, after any redirects, with the credentials the client added
// removed, from its URL, its header fields and its Referer.
type Response struct {
	*http.Response

	// Declared is the Message.Key of the operation's response that
	// governs StatusCode: the exact code ("201"), else its range ("2XX"),
	// else "default". It is empty when the operation declares nothing for
	// the status, which is not in itself an error. A lowercase range such as
	// "2xx" is not a range key: it is reported on its Message, and never
	// governs.
	Declared string

	// MediaType is the Type of the governing response's Media that the
	// Content-Type matches. A Type matches when its type and subtype equal
	// the Content-Type's, compared without regard to case, or cover them as
	// a range does, and every parameter it names is present with an equal
	// value: a charset compared without regard to case, other parameters
	// exactly. The most specific match wins (a type over a range, more
	// parameters over fewer), and a tie matches none. A missing
	// Content-Type matches the governing response's only Type, if it
	// declares one; a repeated Content-Type matches none.
	//
	// A Swagger 2.0 response that declares no produces has one Media with
	// an empty Type, which matches any Content-Type, as */* would. So
	// MediaType is empty both when nothing matched and when that Media did;
	// the governing Message tells which.
	MediaType string

	// Security is the Key of the security alternative applied, "{}" for the
	// anonymous one, or empty when the operation takes no credentials.
	Security string
}

// A Choice records one decision the document left open: which server,
// variable value, security alternative or media type to use where the
// document offers several, or none. Request.Choices lists the decisions a
// prepared call made; RequestError.Choices lists those that stop a call,
// including a missing credential, recorded as a CredentialChoice. The
// fields mean the same wherever a Choice appears.
type Choice struct {
	// Kind says what is decided, and so which field decides it.
	Kind ChoiceKind

	// Name says which one: the variable of a VariableChoice, or the scheme
	// of a CredentialChoice. A MediaTypeChoice names the part of Body it
	// concerns by a JSON Pointer (RFC 6901): "" for the body itself,
	// "/photo" for its property photo, "/files/0" for an item of it, "/0"
	// for the first part of a positional body. Name is empty for a
	// ServerChoice and a SecurityChoice.
	Name string

	// Offered lists what the document offers that would resolve the
	// decision, in document order: server URLs as written, a variable's
	// enum, the Keys of the security alternatives, or media types, where a
	// range stands for any concrete type within it. It leaves out anything
	// that cannot be used (a server with an Err, an alternative whose
	// schemes cannot be applied, a relative server without a base URI), and
	// is nil when the document offers no usable list, as for a credential,
	// or a server that only Options.BaseURL can supply.
	Offered []string

	// Value is the decision: the server URL as written, or the BaseURL; the
	// variable's value; the alternative's Key; the media type. It is empty
	// when the decision is not made, as for a missing choice, and it never
	// holds a credential. A Value not among Offered is one the caller
	// supplied and the document does not offer.
	Value string

	// Default reports that the client made the decision by its documented
	// default, because the caller did not.
	Default bool
}

// A ChoiceKind says what a Choice decides.
type ChoiceKind string

// The kinds of Choice, with the field that decides each.
const (
	ServerChoice     ChoiceKind = "server"     // Options.Server or Options.BaseURL
	VariableChoice   ChoiceKind = "variable"   // Options.Variables
	SecurityChoice   ChoiceKind = "security"   // Options.Credentials, then Input.Security (a Key) or Options.Security (its scheme names)
	CredentialChoice ChoiceKind = "credential" // Options.Credentials
	MediaTypeChoice  ChoiceKind = "mediaType"  // Input.MediaType, or Part.MediaType for a part
)

// OperationFromContext returns the operation being sent when ctx is the
// context of a request the client sends, and nil otherwise, including for
// requests a credential source makes. It lets an http.RoundTripper label
// traces and metrics by Operation.Key, or decide whether a retry is safe.
// It does no work beyond the lookup.
func OperationFromContext(ctx context.Context) *Operation {
	panic("unimplemented")
}
