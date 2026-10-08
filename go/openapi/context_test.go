package openapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Context cancellation and deadlines. doc.go, Outcomes: "When the call's
// context is done before the call completes, and the transport honors the
// request's context, as net/http's does, the error matches ctx.Err()
// with errors.Is, and also context.Cause(ctx), even where net/http would
// report only the cause ... a *StatusError or *DecodeError may wrap the
// context's error when a deadline cut the body short."

var errCause = errors.New("the caller gave up")

// stall answers by writing, when status is not zero, the status, a JSON
// Content-Type and partial, flushing them; it then signals on the returned
// channel and blocks until the request's context ends or the test ends.
func stall(t *testing.T, status int, ct, partial string) (http.HandlerFunc, <-chan struct{}) {
	t.Helper()
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	return func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(status)
			io.WriteString(w, partial)
			w.(http.Flusher).Flush()
		}
		select {
		case arrived <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}, arrived
}

// newStalled returns a Client for respDoc whose server stalls as stall says.
func newStalled(t *testing.T, status int, ct, partial string) (*openapi.Client, <-chan struct{}) {
	t.Helper()
	w := newWire(t, nil) // closed after the stalled handler is released
	h, arrived := stall(t, status, ct, partial)
	w.setAnswer(h)
	return parseFor(t, w, respDoc, nil), arrived
}

func wantCtxErr(t *testing.T, err, ctxErr, cause error) {
	t.Helper()
	if !errors.Is(err, ctxErr) {
		t.Errorf("error %v does not match %v", err, ctxErr)
	}
	if !errors.Is(err, cause) {
		t.Errorf("error %v does not match the cause %v", err, cause)
	}
}

// A context already cancelled.
func TestContextCancelledBeforeCall(t *testing.T) {
	_, c := respClient(t, nil, nil)
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errCause)
	_, err := c.Call(ctx, "getPet", nil, new(Pet))
	wantCtxErr(t, err, context.Canceled, errCause)
}

// Cancelled while waiting for the response headers: no response arrived.
func TestContextCancelledBeforeHeaders(t *testing.T) {
	c, arrived := newStalled(t, 0, "", "")
	ctx, cancel := context.WithCancelCause(t.Context())
	go func() { <-arrived; cancel(errCause) }()
	_, err := c.Call(ctx, "getPet", nil, new(Pet))
	wantCtxErr(t, err, context.Canceled, errCause)
	var se *openapi.StatusError
	var de *openapi.DecodeError
	if errors.As(err, &se) || errors.As(err, &de) {
		t.Errorf("no response arrived, but the error is %T", err)
	}

	// client.go, Request.Send: the same for Send.
	c, arrived = newStalled(t, 0, "", "")
	ctx, cancel = context.WithCancelCause(t.Context())
	go func() { <-arrived; cancel(errCause) }()
	_, err = mustPrepare(t, c, "getPet", nil).Send(ctx)
	wantCtxErr(t, err, context.Canceled, errCause)
}

// afterHeaders is an http.RoundTripper that calls then once the response
// headers have arrived, before the client receives them, so that what then
// does (cancel the call, or wait for its deadline) happens mid-body and
// never races the headers.
type afterHeaders struct {
	next http.RoundTripper
	then func(*http.Request)
}

func (a afterHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := a.next.RoundTrip(r)
	if err == nil {
		a.then(r)
	}
	return resp, err
}

// newCutMidBody returns a Client for respDoc whose server writes status, a
// Content-Type and the partial body, then blocks until the request's
// context ends; then runs once the client's transport has the headers.
func newCutMidBody(t *testing.T, status int, ct, partial string, then func(*http.Request)) *openapi.Client {
	t.Helper()
	w := newWire(t, nil) // closed after the stalled handler is released
	h, _ := stall(t, status, ct, partial)
	w.setAnswer(h)
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	return parseFor(t, w, respDoc, &openapi.Options{HTTPClient: &http.Client{Transport: afterHeaders{tr, then}}})
}

// Cancelled after a 2xx's headers, mid-body: a *DecodeError that matches the
// context's error (errors.go, DecodeError: "reading it failed, as when the
// context ended (Err then matches the context's error)").
func TestContextCancelledMidBody(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	c := newCutMidBody(t, 200, "application/json", `{"name":"Re`, func(*http.Request) { cancel(errCause) })
	resp, err := c.Call(ctx, "getPet", nil, new(Pet))
	var de *openapi.DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("error %v, want a *DecodeError", err)
	}
	if resp == nil || resp.StatusCode != 200 {
		t.Errorf("Response = %v, want the 200", resp)
	}
	wantCtxErr(t, err, context.Canceled, errCause)
	if !errors.Is(de.Err, context.Canceled) {
		t.Errorf("DecodeError.Err = %v, want the context's error", de.Err)
	}
}

// Cancelled after a non-2xx's headers, mid-body: the *StatusError stands
// (errors.go, StatusError: "It stands whenever such a status arrived, even
// when reading its body failed"; Err "matches the context's error when a
// deadline cut it").
func TestContextCancelledMidErrorBody(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	c := newCutMidBody(t, 404, "application/problem+json", `{"title":"No`, func(*http.Request) { cancel(errCause) })
	resp, err := c.Call(ctx, "getPet", nil, new(Pet))
	var se *openapi.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error %v, want a *StatusError", err)
	}
	if resp == nil || se.StatusCode != 404 {
		t.Errorf("Response %v, StatusError status %d", resp, se.StatusCode)
	}
	wantCtxErr(t, err, context.Canceled, errCause)
	if !errors.Is(se.Err, context.Canceled) {
		t.Errorf("StatusError.Err = %v, want the context's error", se.Err)
	}
}

// A deadline with a cause, before and after the headers. After them, the
// transport holds the headers until the deadline has passed, so the
// deadline always cuts the body.
func TestContextDeadline(t *testing.T) {
	c, _ := newStalled(t, 0, "", "")
	ctx, cancel := context.WithTimeoutCause(t.Context(), 100*time.Millisecond, errCause)
	defer cancel()
	_, err := c.Call(ctx, "getPet", nil, new(Pet))
	wantCtxErr(t, err, context.DeadlineExceeded, errCause)

	c = newCutMidBody(t, 200, "application/json", `[1,`, func(r *http.Request) { <-r.Context().Done() })
	ctx, cancel = context.WithTimeoutCause(t.Context(), 100*time.Millisecond, errCause)
	defer cancel()
	resp, err := c.Call(ctx, "getPet", nil, new(any))
	var de *openapi.DecodeError
	if !errors.As(err, &de) {
		t.Errorf("error %v, want a *DecodeError", err)
	}
	if resp == nil || resp.StatusCode != 200 {
		t.Errorf("Response = %v, want the 200", resp)
	}
	wantCtxErr(t, err, context.DeadlineExceeded, errCause)
}
