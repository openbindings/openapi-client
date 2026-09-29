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
	// Several security alternatives with none selected are keyed
	// "Options.Security", the error naming Options.SecurityKey and
	// Input.Security too. An empty secret from a credential source is keyed
	// as a missing credential is. A caller may inspect the operation
	// description for offered values. The values are errors, never
	// credentials or caller-supplied secrets.
	Settings map[string]error

	// Inputs holds each input the operation cannot accept, and why: an
	// unknown parameter, a missing required one, a value its style cannot
	// serialize or a header cannot carry, a body the operation does not
	// take, a reader or Part where the media type cannot carry one, a
	// ParamWriters failure or conflict, or a Part that sets both Filename
	// and NoFilename. The key is the Param.Key or, for the body, "Input.Body"
	// followed by a JSON Pointer to the part of Body concerned:
	// "Input.Body" alone for the body itself, "Input.Body/photo" for its
	// property photo. The two never collide (see Input.Params).
	Inputs map[string]error

	// Err is the reason for any other refusal, or nil: ErrNoOperation, the
	// operation's own Err, a credential source's error (naming the scheme), an
	// Options setting the document cannot use or a field set to a media range,
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
	panic("unimplemented")
}

// Unwrap returns Err and the errors of Settings and Inputs, in key order.
func (e *RequestError) Unwrap() []error {
	panic("unimplemented")
}

// A StatusError is a response whose final status is not 2xx, including a
// 3xx that was not followed. It stands whenever such a status arrived, even
// when reading its body failed. Its body, for a status that has one (see
// Client.Call), has been read, up to Options.MaxErrorBytes, and the
// connection released. The promoted Body
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

// Decode decodes Content into v by the response's media type, as
// Response.Decode does for an open body, any number of times. When Err is set
// it returns Err instead of decoding an incomplete body, except into a *[]byte,
// which receives the bytes read along with Err.
func (e *StatusError) Decode(v any) error {
	panic("unimplemented")
}

// Unwrap returns e.Err.
func (e *StatusError) Unwrap() error {
	return e.Err
}

// A DecodeError is a response whose body could not be used by Call or
// Response.Decode: it was empty where Call's empty-body rule requires
// content (Err is io.EOF), it was longer
// than Options.MaxBodyBytes (an *http.MaxBytesError), it did not decode
// into the value given, or reading it failed, as when the context ended
// (Err then matches the context's error). The server has handled the call.
// The promoted Body reads Content again, and ContentLength is
// len(Content), as for a StatusError; the promoted Decode therefore reads
// only Content.
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
