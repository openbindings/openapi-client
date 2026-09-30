package openapi_test

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Stage 3, credentials and placement (brief, Scope: "Credentials and
// placement"): what each Credential constructor holds, where the client
// places it for every scheme type, when it is placed, and how a credential
// source is called. Refusals are in credrefusals_test.go, destinations in
// destinations_test.go, redirects in redirects_test.go.

// credential.go, Secret: "apiKey: the key, sent in the declared header,
// query parameter or cookie"; "http bearer, oauth2 and openIdConnect: the
// token, sent as "Authorization: Bearer <token>""; "http basic ...: the
// user-id and password joined by a colon, as RFC 7617 writes them, sent
// base64-encoded"; "any other http scheme: everything after the scheme name
// in the Authorization field". doc.go, Credentials: "An http scheme, such as
// bearer or basic, is compared without regard to case". doc.go, Order:
// "query credentials last"; Cookies: "one Cookie field, pairs joined by
// "; ", parameters in declared order, then credentials". client.go,
// Request.Security and Response.Security: the Key of the alternative
// applied. OAS 3.1.2 section 4.8.27: a mutualTLS scheme uses a client
// certificate, so the client adds nothing (client.go, Options.Credentials:
// "A mutualTLS scheme needs no entry").
func TestCredentialPlacement(t *testing.T) {
	w := newWire(t, nil)
	c := credClient(t, w, nil)
	tests := []struct {
		key      string
		in       *openapi.Input
		security string
		check    func(t *testing.T, r rec)
	}{
		{"keyHeader", nil, `{"key_h":[]}`, func(t *testing.T, r rec) {
			wantURI(t, r, "/h")
			wantField(t, r.Header, "X-API-Key", hSecret)
			wantNoFields(t, r.Header, "Authorization", "Cookie")
		}},
		{"keyQuery", nil, `{"key_q":[]}`, func(t *testing.T, r rec) {
			wantURI(t, r, "/q?api_key="+qSecret)
			wantNoFields(t, r.Header, "Authorization", "Cookie")
		}},
		{"keyQuery", &openapi.Input{Params: map[string]any{"p2": "b", "p1": "a"}}, `{"key_q":[]}`, func(t *testing.T, r rec) {
			wantURI(t, r, "/q?p1=a&p2=b&api_key="+qSecret)
		}},
		{"keyCookie", nil, `{"key_c":[]}`, func(t *testing.T, r rec) {
			wantURI(t, r, "/c")
			wantField(t, r.Header, "Cookie", "sid="+cSecret)
			wantNoFields(t, r.Header, "Authorization", "Sid")
		}},
		{"keyCookie", &openapi.Input{Params: map[string]any{"c1": "v"}}, `{"key_c":[]}`, func(t *testing.T, r rec) {
			wantField(t, r.Header, "Cookie", "c1=v; sid="+cSecret)
		}},
		{"basic", nil, `{"basic":[]}`, func(t *testing.T, r rec) {
			wantAuthorization(t, r.Header, "Basic", basicField)
		}},
		{"basicMixedCase", nil, `{"basic_mc":[]}`, func(t *testing.T, r rec) {
			wantAuthorization(t, r.Header, "Basic", base64.StdEncoding.EncodeToString([]byte("bob:pw-2Kd")))
		}},
		{"bearer", nil, `{"bearer":[]}`, func(t *testing.T, r rec) {
			wantField(t, r.Header, "Authorization", "Bearer "+bToken)
		}},
		{"bearerUpper", nil, `{"bearer_uc":[]}`, func(t *testing.T, r rec) {
			wantField(t, r.Header, "Authorization", "Bearer "+uToken)
		}},
		{"dpop", nil, `{"dpop":[]}`, func(t *testing.T, r rec) {
			wantAuthorization(t, r.Header, "DPoP", dProof)
		}},
		{"oauth", nil, `{"oauth":["read"]}`, func(t *testing.T, r rec) {
			wantField(t, r.Header, "Authorization", "Bearer "+oToken)
		}},
		{"oidc", nil, `{"oidc":["openid"]}`, func(t *testing.T, r rec) {
			wantField(t, r.Header, "Authorization", "Bearer "+iToken)
		}},
		{"mtls", nil, `{"mtls":[]}`, func(t *testing.T, r rec) {
			wantURI(t, r, "/mtls")
			wantNoFields(t, r.Header, "Authorization", "Cookie", "X-API-Key")
		}},
		// Both schemes of one alternative are applied.
		{"two", nil, `{"key_h":[],"key_q":[]}`, func(t *testing.T, r rec) {
			wantURI(t, r, "/two?api_key="+qSecret)
			wantField(t, r.Header, "X-API-Key", hSecret)
		}},
		{"open", nil, "", func(t *testing.T, r rec) {
			wantURI(t, r, "/open")
			wantNoFields(t, r.Header, "Authorization", "Cookie", "X-API-Key")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if req, err := c.Prepare(tt.key, tt.in); err != nil {
				t.Fatalf("Prepare: %v", err)
			} else if req.Security != tt.security {
				t.Errorf("Request.Security = %q, want %q", req.Security, tt.security)
			}
			before := w.count()
			resp := mustCall(t, c, tt.key, tt.in, nil)
			if resp.Security != tt.security {
				t.Errorf("Response.Security = %q, want %q", resp.Security, tt.security)
			}
			if n := w.count() - before; n != 1 {
				t.Fatalf("server received %d requests, want 1", n)
			}
			tt.check(t, w.last(t))
		})
	}
}

// credential.go, Secret for http basic: "the user-id and password joined by
// a colon, as RFC 7617 writes them, sent base64-encoded"; Basic: "a
// Credential for http basic authentication (RFC 7617), sent in UTF-8" (RFC
// 7617 section 2.1: the charset is UTF-8, applied before base64).
func TestBasicCredentialEncoding(t *testing.T) {
	w := newWire(t, nil)
	tests := []struct {
		name string
		cred openapi.Credential
		want string // user-id ":" password, before base64
	}{
		{"Basic", openapi.Basic("alice", "p@ss word"), "alice:p@ss word"},
		{"Basic in UTF-8", openapi.Basic("jösé", "pässwörd€"), "jösé:pässwörd€"},
		{"Basic with an empty password", openapi.Basic("alice", ""), "alice:"},
		{"Secret for a basic scheme", openapi.Secret("carol:s3:cr3t"), "carol:s3:cr3t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := parseFor(t, w, credDoc, &openapi.Options{Credentials: map[string]openapi.Credential{"basic": tt.cred}})
			mustCall(t, c, "basic", nil, nil)
			wantAuthorization(t, w.last(t).Header, "Basic", base64.StdEncoding.EncodeToString([]byte(tt.want)))
		})
	}
}

// doc.go, Percent-encoding: "path and query values ... and parameter and
// member names encode every byte outside RFC 3986's unreserved set as %XX
// in uppercase hex"; a query credential is a query value. Header values are
// "written as given", and "an apiKey sent in a cookie, [is] written as given
// too" (the refusal of ";" and controls is in credrefusals_test.go).
func TestAPIKeyValuesAsWritten(t *testing.T) {
	w := newWire(t, nil)
	c := credClient(t, w, func(o *openapi.Options) {
		o.Credentials["key_q"] = openapi.Secret("a b/c+d&e=f%20?é")
		o.Credentials["key_h"] = openapi.Secret("a%20b c/+=")
		o.Credentials["key_c"] = openapi.Secret("a%20b=c/+")
	})
	mustCall(t, c, "keyQuery", nil, nil)
	wantURI(t, w.last(t), "/q?api_key=a%20b%2Fc%2Bd%26e%3Df%2520%3F%C3%A9")
	mustCall(t, c, "keyHeader", nil, nil)
	wantField(t, w.last(t).Header, "X-API-Key", "a%20b c/+=")
	mustCall(t, c, "keyCookie", nil, nil)
	wantField(t, w.last(t).Header, "Cookie", "sid=a%20b=c/+")
}

// doc.go, Credentials: "calls the credential sources it needs when the
// request is sent, never when it is prepared". client.go, Prepare: "It never
// calls a credential source"; Request.HTTP: "no credentials, which are added
// when it is sent"; Request.Call: a request whose body can be sent again
// "may be sent any number of times, concurrently too", so sending does not
// change HTTP. credential.go, SecretFunc: "f is called once for each request
// the client builds that carries its credential".
func TestCredentialsPlacedAtSend(t *testing.T) {
	w := newWire(t, nil)
	src := fixedSource(bToken)
	c := credClient(t, w, func(o *openapi.Options) { o.Credentials["bearer"] = src.credential() })
	for _, key := range []string{"keyHeader", "keyQuery", "keyCookie", "bearer", "basic", "two"} {
		t.Run(key, func(t *testing.T) {
			req := mustPrepare(t, c, key, nil)
			unplaced := func(when string) {
				t.Helper()
				h := req.HTTP.Header
				if v := h.Values("Authorization"); v != nil {
					t.Errorf("%s: Request.HTTP has Authorization %q", when, v)
				}
				if v := h.Values("X-API-Key"); v != nil {
					t.Errorf("%s: Request.HTTP has X-API-Key %q", when, v)
				}
				if v := h.Values("Cookie"); v != nil {
					t.Errorf("%s: Request.HTTP has Cookie %q", when, v)
				}
				if strings.Contains(req.HTTP.URL.String(), "api_key") {
					t.Errorf("%s: Request.HTTP.URL is %q, holding the query credential", when, req.HTTP.URL)
				}
			}
			unplaced("after Prepare")
			sendAndClose(t, req)
			unplaced("after Send")
			r := w.last(t)
			switch key {
			case "keyHeader":
				wantField(t, r.Header, "X-API-Key", hSecret)
			case "keyQuery":
				wantURI(t, r, "/q?api_key="+qSecret)
			case "keyCookie":
				wantField(t, r.Header, "Cookie", "sid="+cSecret)
			case "bearer":
				wantField(t, r.Header, "Authorization", "Bearer "+bToken)
			case "basic":
				wantAuthorization(t, r.Header, "Basic", basicField)
			case "two":
				wantURI(t, r, "/two?api_key="+qSecret)
				wantField(t, r.Header, "X-API-Key", hSecret)
			}
		})
	}
	if n := src.calls(); n != 1 {
		t.Errorf("the bearer source was called %d times for one request that carries it, want 1", n)
	}
}

// credential.go, SecretFunc: "f is called once for each request the client
// builds that carries its credential"; doc.go, Credentials: never when a
// call is prepared, and "Unselected alternatives have no effect on the
// call". A call refused before sending builds no request to send.
func TestSecretFuncCalledPerRequest(t *testing.T) {
	w := newWire(t, nil)
	src := fixedSource(bToken)
	c := parseFor(t, w, credDoc, &openapi.Options{Credentials: map[string]openapi.Credential{
		"bearer": src.credential(),
		"key_h":  openapi.Secret(hSecret),
	}})
	want := func(n int64, after string) {
		t.Helper()
		if got := src.calls(); got != n {
			t.Errorf("after %s, the source was called %d times, want %d", after, got, n)
		}
	}
	req := mustPrepare(t, c, "bearer", nil)
	want(0, "Prepare")
	sendAndClose(t, req)
	want(1, "one Send")
	sendAndClose(t, req)
	want(2, "a second Send of the same Request")
	if _, err := req.Call(t.Context(), nil); err != nil {
		t.Fatalf("Request.Call: %v", err)
	}
	want(3, "Request.Call")
	mustCall(t, c, "bearer", nil, nil)
	want(4, "Client.Call")
	for _, r := range w.requests() {
		wantField(t, r.Header, "Authorization", "Bearer "+bToken)
	}

	before := w.count()
	resp, err := c.Call(t.Context(), "bearer", &openapi.Input{Params: map[string]any{"undeclared": 1}}, nil)
	refusedSince(t, w, before, resp, err)
	want(4, "a call refused before sending")

	mustCall(t, c, "open", nil, nil)
	mustCall(t, c, "either", &openapi.Input{Security: `{"key_h":[]}`}, nil)
	mustCall(t, c, "either", &openapi.Input{Security: "{}"}, nil)
	want(4, "calls whose alternative does not use it")
}

// credential.go, SecretFunc: "f receives the call's context, with its
// deadline, cancellation and values, but not the operation: a request f
// makes is not labelled ... as the call's operation by middleware that asks
// OperationFromContext." client.go, OperationFromContext: nil "for requests
// a credential source makes".
func TestSecretFuncContext(t *testing.T) {
	w := newWire(t, nil)
	type ctxKey struct{}
	var (
		mu       sync.Mutex
		value    any
		deadline time.Time
		hasDL    bool
		op       *openapi.Operation
	)
	src := &source{fn: func(ctx context.Context, _ int64) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		value = ctx.Value(ctxKey{})
		deadline, hasDL = ctx.Deadline()
		op = openapi.OperationFromContext(ctx)
		return bToken, nil
	}}
	c := parseFor(t, w, credDoc, &openapi.Options{Credentials: map[string]openapi.Credential{"bearer": src.credential()}})
	dl := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.WithValue(t.Context(), ctxKey{}, "tenant-7"), dl)
	defer cancel()
	if _, err := c.Call(ctx, "bearer", nil, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if value != "tenant-7" {
		t.Errorf("the source's context value = %v, want the call's", value)
	}
	if !hasDL || !deadline.Equal(dl) {
		t.Errorf("the source's deadline = %v, %v, want the call's %v", deadline, hasDL, dl)
	}
	if op != nil {
		t.Errorf("OperationFromContext in the source = %q, want nil", op.Key)
	}

	// Cancellation reaches the source: it ends when the call's context
	// does, and the call is refused with nothing sent (credential.go,
	// SecretFunc: "An error from f ... on the first request refuses the call
	// with a *RequestError: nothing is sent"; doc.go, Outcomes: the error
	// "matches ctx.Err() with errors.Is").
	var once sync.Once
	entered := make(chan struct{})
	blocked := &source{fn: func(ctx context.Context, _ int64) (string, error) {
		once.Do(func() { close(entered) })
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Second):
			return "", errors.New("the source's context was not cancelled")
		}
	}}
	c2 := parseFor(t, w, credDoc, &openapi.Options{Credentials: map[string]openapi.Credential{"bearer": blocked.credential()}})
	cctx, ccancel := context.WithCancel(t.Context())
	go func() {
		<-entered
		ccancel()
	}()
	before := w.count()
	resp, err := c2.Call(cctx, "bearer", nil, nil)
	refusedSince(t, w, before, resp, err)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v does not match context.Canceled", err)
	}
}

// idpError is an error a credential source returns.
type idpError struct{ status int }

func (e *idpError) Error() string { return fmt.Sprintf("identity provider answered %d", e.status) }

// credential.go, SecretFunc: "An error from f, or an empty secret, on the
// first request refuses the call with a *RequestError: nothing is sent ...
// An error from f is passed on as it is." errors.go, RequestError.Err: "a
// credential source's error (naming the scheme)"; RequestError.Error: "The
// text of an error a credential source returned is included as it is";
// RequestError.Settings: "An empty secret from a credential source is keyed
// as a missing credential is."
func TestSecretFuncRefusals(t *testing.T) {
	w := newWire(t, nil)
	t.Run("error", func(t *testing.T) {
		fail := &idpError{503}
		src := &source{fn: func(context.Context, int64) (string, error) { return "", fail }}
		c := parseFor(t, w, credDoc, &openapi.Options{Credentials: map[string]openapi.Credential{
			"bearer": src.credential(), "key_h": openapi.Secret(hSecret),
		}})
		resp, err := c.Call(t.Context(), "bearer", nil, nil)
		re := refusedBeforeSending(t, w, resp, err)
		var got *idpError
		if !errors.As(err, &got) || got != fail {
			t.Errorf("errors.As found %v, want the source's own error", got)
		}
		if re.Err == nil || !errors.Is(re.Err, fail) {
			t.Errorf("RequestError.Err = %v, want the source's error", re.Err)
		}
		if !errorContains(err, fail.Error(), "bearer") {
			t.Errorf("error %q does not include the source's text and name the scheme bearer", err)
		}
		noSecrets(t, err, hSecret, bToken)
	})
	t.Run("empty secret", func(t *testing.T) {
		src := fixedSource("")
		c := parseFor(t, w, credDoc, &openapi.Options{Credentials: map[string]openapi.Credential{"bearer": src.credential()}})
		resp, err := c.Call(t.Context(), "bearer", nil, nil)
		re := refusedBeforeSending(t, w, resp, err)
		wantKeys(t, "Settings", re.Settings, false, credKey("bearer"))
		if src.calls() != 1 {
			t.Errorf("the source was called %d times, want 1", src.calls())
		}
		// The same key as no credential at all.
		c2 := parseFor(t, w, credDoc, nil)
		resp, err = c2.Call(t.Context(), "bearer", nil, nil)
		re = refusedBeforeSending(t, w, resp, err)
		wantKeys(t, "Settings", re.Settings, false, credKey("bearer"))
	})
}

// credential.go, SecretFunc: "f is called concurrently from every goroutine
// that sends, so it must be safe for concurrent use": the client does not
// serialize calls to f. Every call of n concurrent calls must be inside f at
// once before any returns. Run with -race.
func TestSecretFuncConcurrent(t *testing.T) {
	w := newWire(t, nil)
	const n = 8
	var arrived atomic.Int64
	all := make(chan struct{})
	src := &source{fn: func(ctx context.Context, _ int64) (string, error) {
		if arrived.Add(1) == n {
			close(all)
		}
		select {
		case <-all:
			return bToken, nil
		case <-time.After(10 * time.Second):
			return "", errors.New("the source was not called concurrently")
		}
	}}
	c := parseFor(t, w, credDoc, &openapi.Options{Credentials: map[string]openapi.Credential{"bearer": src.credential()}})
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			_, err := c.Call(context.Background(), "bearer", nil, nil)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if got := src.calls(); got != n {
		t.Errorf("the source was called %d times for %d calls", got, n)
	}
	if got := w.count(); got != n {
		t.Errorf("server received %d requests, want %d", got, n)
	}
}

// retryRT sends every request twice, as a retrying transport does, and
// returns the second response.
type retryRT struct{ next http.RoundTripper }

func (r retryRT) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return r.next.RoundTrip(req)
}

// credential.go, SecretFunc: "A retry made by the caller's own transport
// resends the request as built, credential included, without calling f."
func TestSecretFuncTransportRetry(t *testing.T) {
	w := newWire(t, nil)
	src := fixedSource(bToken)
	c := parseFor(t, w, credDoc, &openapi.Options{
		HTTPClient:  &http.Client{Transport: retryRT{http.DefaultTransport}},
		Credentials: map[string]openapi.Credential{"bearer": src.credential()},
	})
	mustCall(t, c, "bearer", nil, nil)
	reqs := w.requests()
	if len(reqs) != 2 {
		t.Fatalf("server received %d requests, want the request and its retry", len(reqs))
	}
	for _, r := range reqs {
		wantField(t, r.Header, "Authorization", "Bearer "+bToken)
	}
	if n := src.calls(); n != 1 {
		t.Errorf("the source was called %d times, want 1", n)
	}
}

// doc.go, Credentials: "A header credential replaces a field of the same
// name, and a cookie or query credential a pair of the same name, including
// one edited into Request.HTTP." Field names compare without regard to case
// (RFC 9110 section 5.1). Where the replaced pair sat among the others is
// not asserted: only that one pair of the name remains, holding the
// credential, and the other pairs keep their order.
func TestCredentialReplacesEditedFields(t *testing.T) {
	w := newWire(t, nil)
	c := credClient(t, w, nil)

	t.Run("header", func(t *testing.T) {
		req := mustPrepare(t, c, "keyHeader", nil)
		req.HTTP.Header.Set("X-API-Key", "caller-value")
		sendAndClose(t, req)
		wantField(t, w.last(t).Header, "X-API-Key", hSecret)
	})
	t.Run("header in another spelling", func(t *testing.T) {
		req := mustPrepare(t, c, "keyHeader", nil)
		req.HTTP.Header["x-api-key"] = []string{"caller-value"}
		sendAndClose(t, req)
		wantField(t, w.last(t).Header, "X-API-Key", hSecret)
	})
	t.Run("Authorization", func(t *testing.T) {
		req := mustPrepare(t, c, "bearer", nil)
		req.HTTP.Header.Set("Authorization", "Bearer caller-token")
		sendAndClose(t, req)
		wantField(t, w.last(t).Header, "Authorization", "Bearer "+bToken)
	})
	t.Run("query", func(t *testing.T) {
		req := mustPrepare(t, c, "keyQuery", &openapi.Input{Params: map[string]any{"p1": "a", "p2": "b"}})
		req.HTTP.URL.RawQuery = "p1=a&api_key=caller&p2=b"
		sendAndClose(t, req)
		r := w.last(t)
		var others, keys []string
		for _, pair := range strings.Split(r.RawQuery, "&") {
			if name, value, _ := strings.Cut(pair, "="); name == "api_key" {
				keys = append(keys, value)
			} else {
				others = append(others, pair)
			}
		}
		if strings.Join(keys, ",") != qSecret || strings.Join(others, "&") != "p1=a&p2=b" {
			t.Errorf("query %q, want p1=a and p2=b in order and one api_key pair holding the credential", r.RawQuery)
		}
	})
	t.Run("cookie", func(t *testing.T) {
		req := mustPrepare(t, c, "keyCookie", nil)
		req.HTTP.Header.Set("Cookie", "a=1; sid=caller; b=2")
		sendAndClose(t, req)
		got := w.last(t).Header.Values("Cookie")
		if len(got) != 1 {
			t.Fatalf("Cookie = %q, want one field", got)
		}
		var others, sids []string
		for _, pair := range strings.Split(got[0], "; ") {
			if name, value, _ := strings.Cut(pair, "="); name == "sid" {
				sids = append(sids, value)
			} else {
				others = append(others, pair)
			}
		}
		if strings.Join(sids, ",") != cSecret || strings.Join(others, "; ") != "a=1; b=2" {
			t.Errorf("Cookie %q, want a=1 and b=2 in order and one sid pair holding the credential", got[0])
		}
	})
}

// client.go, Request.HTTP: "If the URL is changed to another origin and the
// call needs credentials, sending it is refused; set Options.BaseURL
// instead." errors.go, RequestError.Err: "a prepared request whose URL was
// changed to another origin". A call that needs none may be sent anywhere,
// and a change within the origin keeps the credentials.
func TestPreparedURLChangedOrigin(t *testing.T) {
	w := newWire(t, nil)
	other := newWire(t, nil)
	c := credClient(t, w, nil)

	for _, key := range []string{"keyHeader", "keyQuery", "bearer"} {
		req := mustPrepare(t, c, key, nil)
		req.HTTP.URL.Host = other.hostport()
		resp, err := req.Send(t.Context())
		re := refusedBeforeSending(t, other, resp, err)
		if re.Err == nil {
			t.Errorf("%s: RequestError.Err is nil for a request moved to another origin", key)
		}
		w.nothingSent(t)
		noSecrets(t, err, credSecrets...)
	}

	req := mustPrepare(t, c, "open", nil)
	req.HTTP.URL.Host = other.hostport()
	sendAndClose(t, req)
	if other.count() != 1 {
		t.Errorf("a request that needs no credentials was not sent to the changed origin")
	}

	req = mustPrepare(t, c, "keyHeader", nil)
	req.HTTP.URL.Path = "/moved"
	sendAndClose(t, req)
	r := w.last(t)
	wantURI(t, r, "/moved")
	wantField(t, r.Header, "X-API-Key", hSecret)
}

// credential.go, FromTransport: "The client adds nothing for the scheme, but
// counts it as satisfied after the caller selects a security alternative."
// doc.go, Credentials: "FromTransport also satisfies a scheme a requirement
// names but the document never declares, or declares defectively";
// describe.go, SecurityScheme.Err: "Alternatives that use it can be applied
// only when FromTransport satisfies it." A mutualTLS scheme accepts
// FromTransport too.
func TestFromTransport(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/bearer":{"get":{"operationId":"bearer","security":[{"bearer":[]}]}},
		"/key":{"get":{"operationId":"keyHeader","security":[{"key_h":[]}]}},
		"/mtls":{"get":{"operationId":"mtls","security":[{"mtls":[]}]}},
		"/ghost":{"get":{"operationId":"ghost","security":[{"ghost":[]}]}},
		"/broken":{"get":{"operationId":"broken","security":[{"broken":[]}]}}`,
		`"components":{"securitySchemes":{
			"bearer":{"type":"http","scheme":"bearer"},
			"key_h":{"type":"apiKey","in":"header","name":"X-API-Key"},
			"mtls":{"type":"mutualTLS"},
			"broken":{"type":"apiKey","name":"k"}
		}}`)
	all := map[string]openapi.Credential{}
	for _, name := range []string{"bearer", "key_h", "mtls", "ghost", "broken"} {
		all[name] = openapi.FromTransport()
	}
	c := parseFor(t, w, doc, &openapi.Options{Credentials: all})
	for _, key := range []string{"bearer", "keyHeader", "mtls", "ghost", "broken"} {
		t.Run(key, func(t *testing.T) {
			resp := mustCall(t, c, key, nil, nil)
			if resp.Security == "" || resp.Security == "{}" {
				t.Errorf("Response.Security = %q, want the alternative's key", resp.Security)
			}
			r := w.last(t)
			wantNoFields(t, r.Header, "Authorization", "X-API-Key", "Cookie", "K")
			if r.RawQuery != "" {
				t.Errorf("query %q, want none", r.RawQuery)
			}
		})
	}

	// Anything but FromTransport cannot satisfy the undeclared or defective
	// scheme, and neither can nothing.
	for _, key := range []string{"ghost", "broken"} {
		for name, cred := range map[string]*openapi.Credential{"a Secret": ptr(openapi.Secret("x-9Zz")), "no credential": nil} {
			t.Run(key+" with "+name, func(t *testing.T) {
				d := c.With(func(o *openapi.Options) {
					if cred == nil {
						delete(o.Credentials, key)
					} else {
						o.Credentials[key] = *cred
					}
				})
				before := w.count()
				resp, err := d.Call(t.Context(), key, nil, nil)
				refusedSince(t, w, before, resp, err)
				noSecrets(t, err, "x-9Zz")
			})
		}
	}
}

func ptr[T any](v T) *T { return &v }

// client.go, Options.Credentials: "A mutualTLS scheme needs no entry."
// doc.go, Credentials: a call is refused when "for a mutualTLS scheme, which
// needs none, [it] has any credential but FromTransport"; the key is the
// setting that fixes it (errors.go, RequestError.Settings). A mutualTLS
// scheme alongside another in one alternative needs only the other's
// credential.
func TestMutualTLSCredentials(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/mtls":{"get":{"operationId":"mtls","security":[{"mtls":[]}]}},
		"/both":{"get":{"operationId":"both","security":[{"mtls":[],"key_h":[]}]}}`,
		`"components":{"securitySchemes":{
			"mtls":{"type":"mutualTLS"},
			"key_h":{"type":"apiKey","in":"header","name":"X-API-Key"}
		}}`)
	c := parseFor(t, w, doc, &openapi.Options{Credentials: map[string]openapi.Credential{"key_h": openapi.Secret(hSecret)}})
	if resp := mustCall(t, c, "mtls", nil, nil); resp.Security != `{"mtls":[]}` {
		t.Errorf("Response.Security = %q", resp.Security)
	}
	wantNoFields(t, w.last(t).Header, "Authorization", "Cookie", "X-API-Key")
	mustCall(t, c, "both", nil, nil)
	wantField(t, w.last(t).Header, "X-API-Key", hSecret)

	src := fixedSource("m-secret-0Xc")
	for name, cred := range map[string]openapi.Credential{
		"Secret":     openapi.Secret("m-secret-0Xc"),
		"SecretFunc": src.credential(),
		"Basic":      openapi.Basic("m-user", "m-secret-0Xc"),
	} {
		t.Run(name, func(t *testing.T) {
			d := c.With(func(o *openapi.Options) { o.Credentials["mtls"] = cred })
			for _, key := range []string{"mtls", "both"} {
				before := w.count()
				resp, err := d.Call(t.Context(), key, nil, nil)
				re := refusedSince(t, w, before, resp, err)
				wantKeys(t, "Settings", re.Settings, false, credKey("mtls"))
				noSecrets(t, err, "m-secret-0Xc")
			}
		})
	}
}

// credential.go, Credential: "A Credential keeps its secret out of reach:
// printing one shows no secret ... The zero Credential, like an empty
// secret, is no credential."
func TestCredentialPrinting(t *testing.T) {
	creds := map[string]openapi.Credential{
		"secret":     openapi.Secret("print-secret-1Qa"),
		"basic":      openapi.Basic("print-user", "print-pass-2Ws"),
		"secretfunc": openapi.SecretFunc(func(context.Context) (string, error) { return "print-token-3Ed", nil }),
	}
	secrets := []string{"print-secret-1Qa", "print-pass-2Ws", base64.StdEncoding.EncodeToString([]byte("print-user:print-pass-2Ws"))}
	var texts []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d"} {
		for _, cr := range creds {
			texts = append(texts, fmt.Sprintf(verb, cr), fmt.Sprintf(verb, &cr))
		}
		texts = append(texts, fmt.Sprintf(verb, creds))
	}
	texts = append(texts, fmt.Sprint(creds), fmt.Sprintln(creds))
	for _, text := range texts {
		for _, s := range secrets {
			if strings.Contains(text, s) || strings.Contains(strings.ToLower(text), hex.EncodeToString([]byte(s))) {
				t.Errorf("a printed Credential shows the secret %q: %s", s, text)
			}
		}
	}
}
