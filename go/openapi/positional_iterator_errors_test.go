package openapi_test

import (
	"encoding/json"
	"errors"
	"iter"
	"math"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// positionalIterDoc is an OpenAPI 3.2 document whose operation "mixed"
// takes a positional multipart/mixed body, each item a JSON part.
func positionalIterDoc() string {
	return `{"openapi":"3.2.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@"}],"paths":{
		"/mixed":{"post":{"operationId":"mixed","requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array"},"itemEncoding":{"contentType":"application/json"}}}},"responses":{"200":{"description":"ok"}}}}}}`
}

// An OpenAPI 3.2 positional multipart iterator whose item cannot be
// encoded, once the request has reached the server, fails the call as a
// JSON Lines iterator's unencodable item does: the body is aborted and the
// error is the encoding failure, never a *RequestError, which says the
// request was not sent. client.go, Input.Body: under such a type Body may be
// "a list, or an iterator as for a sequential type, one part per element",
// and "An error from an iter.Seq2, an item that cannot be encoded, or the
// context ending before the iterator returns aborts the body and is reported
// by Call or Response.WaitRequest"; Options.Codecs: "An Encode error refuses
// the call at the body's or parameter's Inputs key, or aborts the body for an
// iterator's item"; doc.go, Outcomes: "API request not sent: a
// [*RequestError]."
func TestPositionalIteratorUnencodableItemAbortsTheBody(t *testing.T) {
	errEncode := errors.New("cannot encode")
	for _, tt := range []struct {
		name   string
		codecs map[string]openapi.Codec
		bad    any
		mark   string
		check  func(error) bool
	}{
		{"JSON infinity", nil, math.Inf(1), `"first"`, func(err error) bool {
			var uv *json.UnsupportedValueError
			return errors.As(err, &uv)
		}},
		{"codec error", map[string]openapi.Codec{"application/json": pickyCodec{errEncode}}, 13, "J(first)", func(err error) bool {
			return errors.Is(err, errEncode)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := newBodyServer(t, false, "")
			c := parseAt(t, positionalIterDoc(), srv.URL, srv.URL+"/openapi.json", &openapi.Options{Codecs: tt.codecs})
			ctx, _ := gateCtx(t)
			it := func(yield func(any) bool) {
				if !yield("first") {
					return
				}
				select {
				case <-srv.seen(tt.mark):
				case <-ctx.Done():
					return
				}
				yield(tt.bad)
			}
			r := awaitCall(t, callAsync(ctx, c, "mixed", &openapi.Input{Body: iter.Seq[any](it)}), "Call")
			if !tt.check(r.err) || isRequestError(r.err) {
				t.Fatalf("Call = %v (%T), want the encoding failure, not a *RequestError", r.err, r.err)
			}
			if _, err := srv.finished(t); err == nil {
				t.Errorf("the server read a complete body")
			}
		})
	}
}

// An iter.Seq2's error, after the first part has reached the server, is
// reported the same way (client.go, Input.Body, as above).
func TestPositionalIteratorErrorAbortsTheBody(t *testing.T) {
	srv := newBodyServer(t, false, "")
	c := parseAt(t, positionalIterDoc(), srv.URL, srv.URL+"/openapi.json", nil)
	ctx, _ := gateCtx(t)
	it := func(yield func(any, error) bool) {
		if !yield("first", nil) {
			return
		}
		select {
		case <-srv.seen(`"first"`):
		case <-ctx.Done():
			return
		}
		yield(nil, errCursor)
	}
	r := awaitCall(t, callAsync(ctx, c, "mixed", &openapi.Input{Body: iter.Seq2[any, error](it)}), "Call")
	if !errors.Is(r.err, errCursor) || isRequestError(r.err) {
		t.Fatalf("Call = %v (%T), want the iterator's error, not a *RequestError", r.err, r.err)
	}
	if _, err := srv.finished(t); err == nil {
		t.Errorf("the server read a complete body")
	}
}
