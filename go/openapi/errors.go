package openapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// ErrNoOperation is wrapped by the error for a key that names no single
// operation: none has it, or several share it as their operationId. Both
// Client.Operation and a refused call return it, and so does a call made
// through the zero Client or a Request that Prepare did not make.
var ErrNoOperation = errors.New("openapi: no such operation")

// ErrUnresolved is wrapped by the Err of a part whose defect is a reference
// that cannot be resolved, a reference cycle included, as distinct from a
// malformed declaration. The Err
// names the reference, and wraps the retrieval error too when fetching its
// document failed, or an error naming the refused URI when admission
// refused it, so a caller can tell a fixable fetch or admission (see
// Loader.Origins, Loader.AllowReference and Loader.Fetch) from a broken
// document.
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
	// A setting the document cannot use, or one that conflicts with
	// another, is keyed by its field, at Load or at a call. Several usable
	// servers with none selected are keyed "Options.Server", BaseURL set
	// with Server or ServerID "Options.BaseURL", Server set with ServerID
	// "Options.ServerID", an undetermined or unusable request media type
	// "Input.MediaType", and a header field that a supplied header
	// parameter or the credential sets by the Header that set it.
	// Several security alternatives with none selected are keyed
	// "Options.Security", the error naming Options.SecurityKey and
	// Input.Security too. An empty secret from a credential source is keyed
	// as a missing credential is. A caller may inspect the operation
	// description for offered values. The values are errors, never
	// credentials or caller-supplied secrets.
	Settings map[string]error

	// Inputs holds each input the operation cannot accept, and why: an unknown
	// parameter, a missing required one, a value its style cannot serialize or
	// a header cannot carry (a CR, LF or NUL, or leading or trailing
	// whitespace, which HTTP would strip), a body the operation does not take,
	// a reader or Part where the media type cannot carry one, a ParamWriters
	// failure or conflict, or a Part that sets both Filename and NoFilename.
	// The key is the Param.Key or, for the body, "Input.Body" followed by a
	// JSON Pointer to the part of Body concerned: "Input.Body" alone for the
	// body itself, "Input.Body/photo" for its property photo. The two never
	// collide (see Input.Params).
	Inputs map[string]error

	// Err is the reason for any other refusal, or nil: ErrNoOperation, the
	// operation's own Err, a credential source's error (naming the scheme),
	// a bearer or Basic credential that would go over plain http or ws, a
	// selected alternative two of whose schemes set the same field, an out that
	// cannot receive a result, a prepared request whose URL was changed to
	// another origin, or a second send of a body that can be read only once.
	// Several are joined, as by errors.Join.
	Err error
}

// Error describes every problem and the field that fixes each, never a
// credential or an input's value. The text of an error a credential source
// returned is included as it is.
func (e *RequestError) Error() string {
	var parts []string
	if e.Err != nil {
		parts = append(parts, strings.TrimPrefix(e.Err.Error(), "openapi: "))
	}
	for _, k := range slices.Sorted(maps.Keys(e.Settings)) {
		parts = append(parts, k+": "+e.Settings[k].Error())
	}
	for _, k := range slices.Sorted(maps.Keys(e.Inputs)) {
		parts = append(parts, k+": "+e.Inputs[k].Error())
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
		return &DecodeError{Response: e.Response, Err: err}
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
	if head, _, err := cfg.read(r.Response, r.Declaration, v); err != nil {
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

// Error returns the operation, the status, the media type and the reason,
// never the body, though a decoder's message in the reason may quote a
// short token of it.
func (e *DecodeError) Error() string {
	msg := "openapi: " + describeResponse(e.Response)
	if e.Response != nil && e.Response.Response != nil {
		if ct := e.Header.Get("Content-Type"); ct != "" {
			msg += ": " + ct
		}
	}
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
	s := strings.TrimSpace(fmt.Sprintf("%d %s", r.StatusCode, http.StatusText(r.StatusCode)))
	if x := exchangeOf(r.Response); x != nil {
		s = x.op.Key + ": " + s
	}
	return s
}
