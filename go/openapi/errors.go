package openapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrNoOperation is wrapped by the error for a key that names no single
// operation: none has it, or several share it as their operationId. Both
// Client.Operation and a refused call return it, and so does a call made
// through the zero Client or a Request that Prepare did not make.
var ErrNoOperation = errors.New("openapi: no such operation")

// ErrUnresolved is wrapped by the Err of a part, or of a SchemaReference, whose
// defect is a reference that cannot be resolved, as distinct from a malformed
// declaration or a document that must be bundled first (see Operation), and by
// every error Client.Schema returns but one saying the document must be bundled
// first. A reference cycle cannot be resolved: a chain of Reference Objects or
// of Path Item $refs, in any edition, or of Swagger 2.0 or OpenAPI 3.0 schemas
// written as references, that leads back into itself.
// Other cycles, such as a recursive schema or a cycle of OpenAPI 3.1 or 3.2
// schema $refs, resolve (see Schema). The Err names the reference, and wraps
// the retrieval error too when fetching or reading its document failed (one
// that cannot be read is named with where the problem is, as Loader describes
// for an entry document), or an error naming the refused URI when admission
// refused it, so a caller can tell a fixable fetch or admission (see
// Loader.Origins, Loader.AllowReference and Loader.Fetch) from a broken
// document. Where the client names a URI, a reference or a redirect's Location
// included, it omits userinfo and query. A retrieval error that is or wraps a
// *url.Error is shown as that *url.Error, each URL without userinfo or query,
// though it is still wrapped unchanged, and any other, such as a Loader.Fetch
// error that wraps no *url.Error, is shown as it is.
var ErrUnresolved = errors.New("openapi: unresolved reference")

// A RequestError is an API call refused before it was sent, or Options
// that Load refuses. It reports every independently detectable problem.
// Settings names the option that needs to be supplied or corrected; Inputs
// names request values that cannot be serialized. The operation description
// lists the servers, security alternatives and media types the document
// offers. The error does not choose among them.
//
// Why a call was refused is told, in this order, by: errors.Is(err,
// ErrNoOperation), for a key that names no operation; errors.Is(err,
// op.Err), for a defective operation; Settings and Inputs, for what the
// caller must supply or correct; and otherwise Err.
type RequestError struct {
	// Settings holds missing or invalid configuration, keyed by the Go
	// setting that fixes it, in one of these forms:
	//
	//   - Options.<Field>, as "Options.Server";
	//   - Options.<Field>[<name>], for Credentials and Variables, the name
	//     quoted as strconv.Quote does: "Options.Credentials[\"api_key\"]";
	//   - Input.<Field>, as "Input.MediaType";
	//   - for a part's media type, "Input.Body" followed by the part's JSON
	//     Pointer: "Input.Body/file".
	//
	// A setting the document cannot use, or one that conflicts with another, is
	// keyed by its field, at Load or at a call. Several usable servers with
	// none selected are keyed "Options.Server", BaseURL set with Server or
	// ServerID "Options.BaseURL", and Server set with ServerID
	// "Options.ServerID". A server made unusable by a variable that has no
	// value, or by Options.Variables values, is keyed by variable, in the form
	// above, when its Err is nil: by the variable without a value, by one whose
	// value is off its enum or unfit for its place in the URL, or, when the URL
	// the given values form cannot be used, by each variable given a value. It
	// is keyed "Options.BaseURL" too when neither Options.Server nor
	// Options.ServerID is set, unless the operation has one server (see
	// Configuration in the package documentation). A body's media type is keyed
	// "Input.MediaType" when it is undetermined, is not a concrete media type,
	// is one the operation does not declare, or is declared under a key that
	// causes its Media.Err (see Input.MediaType); a body that any other Media.Err
	// refuses is keyed in Inputs. A header field that a supplied header
	// parameter or the credential sets is keyed by the Header that set it.
	// Several security alternatives with none selected, or an Options.Security
	// matching several that differ only in scopes, are keyed
	// "Options.Security", the error naming Options.SecurityKey and
	// Input.Security too; SecurityKey set with Security is keyed
	// "Options.SecurityKey". An empty secret from a credential source is keyed
	// as a missing credential is. A caller may inspect the operation
	// description for offered values. The values are errors, never credentials
	// or caller-supplied secrets.
	Settings map[string]error

	// Inputs holds each input the operation cannot accept, and why: an unknown
	// parameter, a missing required one, a value its style cannot serialize or
	// a header cannot carry (see Header fields in the package documentation), a
	// body the operation does not take, a body refused by the request body's
	// Message.Err or by its governing Media's Err unless its key causes it (see
	// Settings), a reader or Part where the media type cannot carry one, a
	// ParamWriters failure or conflict, or a Part that sets both Filename and
	// NoFilename. The key is the Param.Key or, for the body, "Input.Body"
	// followed by a JSON Pointer to the part of Body concerned, in which a Part
	// adds no token for its Content: "Input.Body" alone for the body itself,
	// "Input.Body/photo" for its property photo, and "Input.Body/photo/r" for
	// the member r of the Content of a Part given as photo. The two never
	// collide (see Input.Params). An error with a cause, such as
	// encoding/json's, a codec's, a reader's or a ParamWriters function's
	// error, or that Message.Err or Media.Err, wraps it, for errors.Is and
	// errors.As.
	Inputs map[string]error

	// Err is the reason for any other refusal, or nil: ErrNoOperation, the
	// operation's own Err, a credential source's error (naming the scheme),
	// a bearer or Basic credential that would go over plain http or ws, a
	// selected alternative two of whose schemes set the same field, an out that
	// cannot receive a result, a prepared request whose URL was changed to
	// another origin, a second send of a body that can be read only once, or
	// the error of HTTP.GetBody when a later send of a prepared request takes
	// its body from it. Several are joined, as by errors.Join.
	Err error
}

// Error describes every problem and the field that fixes each. The text the
// client writes never holds a credential, an input's value, or a value given
// for a server variable or a header field. The text of an error the caller's
// own code returned, such as a credential source, a ParamWriters function or a
// Codec's Encode, is included as it is, whatever it holds, except where the
// package documentation, under Outcomes, says it is left out.
func (e *RequestError) Error() string {
	var parts []string
	if e.Err != nil {
		parts = append(parts, strings.TrimPrefix(e.Err.Error(), "openapi: "))
	}
	for _, k := range slices.Sorted(maps.Keys(e.Settings)) {
		parts = append(parts, settingLabel(k, e.Settings[k])+": "+e.Settings[k].Error())
	}
	for _, k := range slices.Sorted(maps.Keys(e.Inputs)) {
		parts = append(parts, label(k)+": "+e.Inputs[k].Error())
	}
	return "openapi: " + strings.Join(parts, "; ")
}

// Unwrap returns Err and the errors of Settings and Inputs, in key order.
func (e *RequestError) Unwrap() []error {
	errs := make([]error, 0, 1+len(e.Settings)+len(e.Inputs))
	if e.Err != nil {
		errs = append(errs, e.Err)
	}
	for _, k := range slices.Sorted(maps.Keys(e.Settings)) {
		errs = append(errs, e.Settings[k])
	}
	for _, k := range slices.Sorted(maps.Keys(e.Inputs)) {
		errs = append(errs, e.Inputs[k])
	}
	return errs
}

// A StatusError is a response whose final status is not 2xx, including a 3xx
// that was not followed. It stands whenever such a status arrived, even when
// reading its body failed. Its body, for a status that has one (see
// Client.Call), has been read, up to Options.MaxErrorBytes, and the connection
// released. It holds a copy of the call's Response, so its promoted Body and
// the Response's Body each read Content again, and when Content is incomplete
// the promoted ContentLength is len(Content), so reading Body, se.Write(w) and
// httputil.DumpResponse work as in net/http.
type StatusError struct {
	*Response

	// Content is the body as read: all of it when Err is nil.
	Content []byte

	// Err is why Content is incomplete, or nil: an *http.MaxBytesError when
	// the body was longer than MaxErrorBytes, or the error that stopped the
	// read, which matches the context's error when a deadline cut it.
	Err error
}

// Error returns the operation and the status, such as
// "openapi: getPet: 404 Not Found", never the body or the URL.
func (e *StatusError) Error() string {
	return "openapi: " + describeResponse(e.Response)
}

// Decode decodes Content into v by the response's media type, as
// Response.Decode does for an open body, any number of times. When Err is set
// it returns Err instead of decoding an incomplete body, except into a *[]byte,
// which receives the bytes read along with Err.
func (e *StatusError) Decode(v any) error {
	if err := checkOut(v); err != nil {
		return invalidDecodeError(e.Response, err)
	}
	if e.Err != nil {
		if p, ok := v.(*[]byte); ok {
			*p = append((*p)[:0], e.Content...)
		}
		return e.Err
	}
	r := *e.Response
	resp := *r.Response
	resp.Body, resp.ContentLength, r.Response = io.NopCloser(bytes.NewReader(e.Content)), int64(len(e.Content)), &resp
	cfg := &config{}
	if x := exchangeOf(e.Response.Response); x != nil {
		cfg = x.cfg
	}
	if head, _, err := cfg.read(r.Response, mediaOf(r.Header), r.Declaration, v); err != nil {
		return decodeError(&r, head, err)
	}
	return nil
}

// Unwrap returns e.Err.
func (e *StatusError) Unwrap() error {
	return e.Err
}

// A DecodeError is a response whose body could not be used by Call or
// Response.Decode: it was empty where Call's empty-body rule requires content
// (Err is io.EOF), it was longer than Options.MaxBodyBytes (an
// *http.MaxBytesError), it did not decode into the value given, or reading it
// failed, as when the context ended (Err then matches the context's error). The
// server has handled the call. It holds a copy of the Response, whose promoted
// Body reads Content again, and ContentLength is len(Content), as for a
// StatusError; the promoted Decode therefore reads only Content.
type DecodeError struct {
	*Response

	// Content is the start of the body, at most 4 KiB, for logging and
	// diagnosis.
	Content []byte

	Err error
}

// Error returns the operation, the status and the reason, never the body
// or response-controlled media and encoding text, which may reflect credentials.
// A decoder's own error, a Codec's Decode or an UnmarshalJSON method's
// included, whose message can quote the body, is not in the text; errors.As
// finds it through Unwrap. Header remains available for inspection.
func (e *DecodeError) Error() string {
	msg := "openapi: " + describeResponse(e.Response)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns e.Err.
func (e *DecodeError) Unwrap() error {
	return e.Err
}

// describeResponse names the operation r answers, and its status.
func describeResponse(r *Response) string {
	if r == nil || r.Response == nil {
		return "no response"
	}
	s := status(r.StatusCode)
	if x := exchangeOf(r.Response); x != nil {
		s = label(x.op.Key) + ": " + s
	}
	return s
}

// status names a status code, never with a peer's reason phrase.
func status(code int) string {
	return strings.TrimSpace(fmt.Sprintf("%d %s", code, http.StatusText(code)))
}

// label returns s, from a document, a caller or a peer, for the text of an
// error: quoted unless every character is printable, so it cannot forge a
// line of a log or reach a terminal as control codes.
func label(s string) string {
	if utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return !strconv.IsPrint(r) }) {
		return s
	}
	return strconv.Quote(s)
}

// settingLabelError keeps a generated URI label separate from the exact
// programmatic key and preserves the original cause and its presentation.
type settingLabelError struct {
	cause error
	shown string
}

func (e *settingLabelError) Error() string { return e.cause.Error() }
func (e *settingLabelError) Unwrap() error { return e.cause }

func settingLabel(key string, err error) string {
	if e, ok := err.(*settingLabelError); ok {
		key = e.shown
	}
	return label(key)
}
