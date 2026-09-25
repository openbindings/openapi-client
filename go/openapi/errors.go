package openapi

import "errors"

// ErrNoOperation is wrapped by the error for a key that names no single
// operation: none has it, or several share it as their operationId. Both
// Client.Operation and a refused call return it, and so does a call made
// through the zero Client or a Request that Prepare did not make.
var ErrNoOperation = errors.New("openapi: no such operation")

// ErrUnresolved is wrapped by the Err of a part whose defect is a reference
// that cannot be resolved, as distinct from a malformed declaration. The Err
// names the reference, and wraps the retrieval error too when fetching its
// document failed, so a caller can tell a fixable fetch (see Loader.Origins
// and Loader.Fetch) from a broken document.
var ErrUnresolved = errors.New("openapi: unresolved reference")

// A RequestError is a call refused before anything was sent, or Options
// that Load refuses. It lists every problem found, so a form can show them
// all at once and a caller can supply them all before trying again. Its
// text names, for each problem, the field that fixes it, as in: no
// credential for security scheme "api_key" (set
// Options.Credentials["api_key"]). When no alternative is satisfied, it
// names Options.Credentials for the schemes of each offered alternative,
// and Input.Security for picking one.
//
// Why a call was refused is told, in this order, by: errors.Is(err,
// ErrNoOperation), for a key that names no operation; errors.Is(err,
// op.Err), for a defective operation; Choices and Inputs, for what the
// caller must supply or correct; and otherwise Err.
//
// A library that insists on explicit choices can return a RequestError of
// its own, listing the Choices of a Request that have Default set.
type RequestError struct {
	// Choices lists each decision that stops the call: missing, with an
	// empty Value; supplied but not offered, with a Value not among
	// Offered; or, in an error a library builds, made by default, with
	// Default set. When no security alternative can be satisfied and the
	// operation offers several, Choices holds one SecurityChoice rather
	// than guessing which credentials to ask for; once one is selected, it
	// holds a CredentialChoice for each of its schemes without a credential.
	Choices []Choice

	// Inputs holds each input the operation cannot accept, and why: an
	// unknown parameter, a missing required one, a value its style cannot
	// serialize or a header cannot carry, a body the operation does not
	// take, a []byte, reader or Part where the media type cannot carry one,
	// or a Part that sets both Filename and NoFilename. The key is the
	// Input.Params key or, for the body, a JSON Pointer to the part of Body
	// concerned: "" for the body itself, "/photo" for its property photo.
	// The two never collide (see Input.Params).
	Inputs map[string]error

	// Err is the reason for any other refusal, or nil: ErrNoOperation, the
	// operation's own Err, a credential source's error (naming the scheme),
	// an Options setting the document cannot use or a field set to a media
	// range, a bearer or Basic credential that would go over plain http, a
	// selected alternative two of whose schemes set the same field, an out
	// that cannot receive a result, a prepared request whose URL was changed
	// to another origin, or a second send of a body that can be read only
	// once. Several are joined, as by errors.Join.
	Err error
}

// Error describes every problem and the field that fixes each, never a
// credential or an input's value. The text of an error a credential source
// returned is included as it is.
func (e *RequestError) Error() string {
	panic("unimplemented")
}

// Unwrap returns Err and the errors of Inputs, in key order.
func (e *RequestError) Unwrap() []error {
	panic("unimplemented")
}

// A StatusError is a response whose final status is not 2xx, including a
// 3xx that was not followed. It stands whenever such a status arrived, even
// when reading its body failed. Its body has been read, up to
// Options.MaxErrorBytes, and the connection released. The promoted Body
// reads Content again, and when Content is incomplete the promoted
// ContentLength is len(Content), so reading Body, se.Write(w) and
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
	panic("unimplemented")
}

// Decode decodes Content into v by the response's media type, as Call
// decodes a 2xx body, any number of times. When Err is set it returns Err
// instead of decoding an incomplete body, except into a *[]byte, which
// receives the bytes read along with Err.
func (e *StatusError) Decode(v any) error {
	panic("unimplemented")
}

// Unwrap returns e.Err.
func (e *StatusError) Unwrap() error {
	return e.Err
}

// A DecodeError is a 2xx response whose body could not be used: it was
// empty where the operation declares content (Err is io.EOF), it was longer
// than Options.MaxBodyBytes (an *http.MaxBytesError), it did not decode
// into the value given, or reading it failed, as when the context ended
// (Err then matches the context's error). The server has handled the call.
// The promoted Body reads Content again, and ContentLength is
// len(Content), as for a StatusError.
type DecodeError struct {
	*Response

	// Content is the start of the body, at most 4 KiB, for logging and
	// diagnosis.
	Content []byte

	Err error
}

// Error returns the operation, the status, the media type and the reason,
// never the body.
func (e *DecodeError) Error() string {
	panic("unimplemented")
}

// Unwrap returns e.Err.
func (e *DecodeError) Unwrap() error {
	return e.Err
}
