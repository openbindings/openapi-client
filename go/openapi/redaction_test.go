package openapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Stage 3, redaction (brief, Scope: "Redaction"; Tests: "a query
// credential in a failed request's *url.Error, a Basic credential, a
// SecretFunc error"). doc.go, Outcomes: "No credential appears in the text
// of an error the client creates, nor in any *url.Error in the chain of one
// it returns: credentials the client added to a URL are redacted there.
// Errors made by the caller's own code, such as its transport or a
// credential source, are passed on as they are." errors.go, RequestError:
// "never a credential".

// hijackClose ends a request by closing its connection without a response.
func hijackClose(w http.ResponseWriter, r *http.Request) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		conn.Close()
	}
}

// A transport failure after the query credential was placed: net/http's
// *url.Error names the request URL, which the client redacts.
func TestRedactTransportFailure(t *testing.T) {
	for name, secret := range map[string]string{"plain": qSecret, "needing encoding": "q s/cr+t&x=é-8Er"} {
		t.Run(name, func(t *testing.T) {
			b := newRawServer(t, "") // closes every connection unanswered
			c := parseAt(t, credDoc, b.URL, b.URL+"/openapi.json", &openapi.Options{Credentials: map[string]openapi.Credential{
				"key_q": openapi.Secret(secret), "key_h": openapi.Secret(hSecret), "basic": openapi.Basic(basicUser, basicPass),
			}})
			for _, key := range []string{"keyQuery", "two", "basic"} {
				_, err := c.Call(t.Context(), key, &openapi.Input{}, nil)
				var ue *url.Error
				if !errors.As(err, &ue) {
					t.Fatalf("%s: error %v (%T), want the http.Client's *url.Error", key, err, err)
				}
				noSecrets(t, err, secret, hSecret, basicPass, basicField)

				req := mustPrepare(t, c, key, nil)
				_, err = req.Send(t.Context())
				if !errors.As(err, &ue) {
					t.Fatalf("%s: Send error %v (%T), want a *url.Error", key, err, err)
				}
				noSecrets(t, err, secret, hSecret, basicPass, basicField)
			}
			if len(b.requests()) == 0 {
				t.Errorf("nothing reached the server; the test proves nothing")
			}
		})
	}
}

// A caller's transport error is passed on as it is, inside the *url.Error
// the http.Client makes, whose URL is redacted.
func TestRedactCallerTransportError(t *testing.T) {
	rt := &memRT{answer: func(*http.Request) (*http.Response, error) { return nil, errTransport }}
	c := parseAt(t, credDoc, "https://api.example.test", "https://api.example.test/openapi.json",
		&openapi.Options{HTTPClient: &http.Client{Transport: rt}, Credentials: credSet()})
	for _, key := range []string{"keyQuery", "keyHeader", "keyCookie", "bearer", "basic", "two"} {
		_, err := c.Call(t.Context(), key, nil, nil)
		var ue *url.Error
		if !errors.Is(err, errTransport) || !errors.As(err, &ue) {
			t.Errorf("%s: error %v, want a *url.Error wrapping the transport's own error", key, err)
		}
		noSecrets(t, err, credSecrets...)
	}
	if rt.count() == 0 {
		t.Errorf("the transport carried nothing; the test proves nothing")
	}
}

// A call whose context ends while the query credential is on the wire.
func TestRedactDeadline(t *testing.T) {
	release := make(chan struct{})
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	c := credClient(t, w, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := c.Call(ctx, "two", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not match context.DeadlineExceeded", err)
	}
	noSecrets(t, err, credSecrets...)
}

// A same-origin redirect hop, on which the query credential is placed
// again, fails in the transport: the *url.Error names the hop.
func TestRedactHopFailure(t *testing.T) {
	a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, "/next"), "/next": hijackClose}))
	c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
	_, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Fatalf("error %v (%T), want a *url.Error", err, err)
	}
	noSecrets(t, err, redirSecrets...)
	if n := a.count(); n < 2 {
		t.Errorf("server received %d requests; the hop was not sent", n)
	}
}

// Refusals of Basic and bearer credentials never quote them: the plain-http
// rule, a missing partner scheme, and an alternative two of whose schemes
// set Authorization.
func TestRedactRefusals(t *testing.T) {
	hc, rt := memClient()
	c := parseAt(t, credDoc, "http://api.example.test", "http://api.example.test/openapi.json",
		&openapi.Options{HTTPClient: hc, Credentials: credSet()})
	for _, key := range []string{"basic", "basicMixedCase", "bearer", "oauth", "oidc"} {
		_, err := c.Call(t.Context(), key, nil, nil)
		asRequestError(t, err)
		noSecrets(t, err, credSecrets...)
	}
	d := c.With(func(o *openapi.Options) { delete(o.Credentials, "key_q") })
	_, err := d.Call(t.Context(), "two", nil, nil)
	asRequestError(t, err)
	noSecrets(t, err, credSecrets...)
	if rt.count() != 0 {
		t.Errorf("a refused call reached the transport")
	}
}

// StatusError and DecodeError texts never hold the URL (errors.go,
// StatusError.Error: "never the body or the URL"), so never a query
// credential.
func TestRedactStatusAndDecodeErrors(t *testing.T) {
	w := newWire(t, jsonAnswer(500, `{"detail":"`+qSecret+`"}`))
	c := credClient(t, w, nil)
	_, err := c.Call(t.Context(), "two", nil, nil)
	var se *openapi.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error %v, want a *StatusError", err)
	}
	noSecrets(t, err, credSecrets...)

	w.setAnswer(jsonAnswer(200, `not json`))
	var out struct{}
	_, err = c.Call(t.Context(), "two", nil, &out)
	var de *openapi.DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("error %v, want a *DecodeError", err)
	}
	noSecrets(t, err, credSecrets...)
}
