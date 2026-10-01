package openapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// follow sends signed, which is req as it is sent, and each redirect hop
// the client follows (see Redirects), returning the last response. An
// error that ends the chain after a response arrived comes with that
// response, its body closed. Each response's Request is the request without
// the credentials the client placed, and so is every earlier one reachable
// from it.
func (x *exchange) follow(req, signed *http.Request) (*http.Response, error) {
	cfg, hc, first := x.cfg, x.cfg.client, req
	var (
		via      []*http.Request
		resp     *http.Response // the last response, which a hop answers
		left     bool           // a hop has left the call's origin
		deadline time.Time      // when HTTPClient.Timeout ends the chain
	)
	if hc.Timeout > 0 && cfg.Redirects == FollowAll {
		deadline = time.Now().Add(hc.Timeout)
	}
	fail := func(hop *http.Request, err error) error {
		return &url.Error{Op: urlErrorOp(first.Method), URL: hop.URL.Redacted(), Err: err}
	}
	for {
		r, err := hc.Do(signed)
		if err != nil {
			return resp, redact(err, req.URL)
		}
		resp = r
		resp.Request = req
		if redirection(resp.StatusCode) {
			move(resp.Header, heldLocation, "Location")
		}
		if cfg.jar != nil {
			if cookies := resp.Cookies(); len(cookies) > 0 {
				cfg.jar.SetCookies(req.URL, cookies)
			}
		}
		if cfg.Redirects != FollowAll {
			return resp, nil
		}
		method, keep, ok := redirect(req.Method, resp.StatusCode)
		loc := resp.Header.Get("Location")
		if !ok || loc == "" {
			return resp, nil
		}
		ref, err := url.Parse(loc)
		body := keep && req.Body != nil && req.Body != http.NoBody
		if err != nil || body && first.GetBody == nil {
			return resp, nil // no Location, or a body that cannot be sent again
		}
		u := req.URL.ResolveReference(ref)
		left = left || !sameOrigin(u, first.URL)
		next := x.hop(req, resp, method, u, keep, left)
		if ref.Host == "" && req.Host != "" && req.Host != req.URL.Host {
			next.Host = req.Host // with no authority in the Location, as net/http keeps it (Go issue 22233)
		}
		via = append(via, req)

		// CheckRedirect sees the body the hop sends, as net/http shows it; a
		// copy it takes with GetBody is its own.
		var hb *sentBody
		if body {
			if hb, err = x.newBody(); err != nil {
				discard(resp)
				return resp, fail(next, err)
			}
			next.Body, next.GetBody, next.ContentLength = hb, first.GetBody, first.ContentLength
		}
		x.checking.Store(true)
		err = cfg.checkRedirect(next, via)
		x.checking.Store(false)
		if err != http.ErrUseLastResponse {
			discard(resp)
		}
		if err == nil { // CheckRedirect may have moved the hop
			left = left || next.URL == nil || !sameOrigin(next.URL, first.URL)
			src, cancel := x.Context, func() {}
			if !deadline.IsZero() {
				src, cancel = context.WithDeadline(x.Context, deadline)
			}
			var re RequestError
			if signed = x.sign(next, src, !left, &re); re.refused() != nil {
				err = re.sent()
			}
			cancel()
		}
		if err == nil && !deadline.IsZero() { // what remains, charged for everything since the chain began
			hc = new(http.Client)
			*hc = *cfg.client
			if hc.Timeout = time.Until(deadline); hc.Timeout <= 0 {
				err = errChainTimeout{}
			}
		}
		sent, _ := next.Body.(*sentBody) // the hop's body, when the client made it
		if hb != nil && (err != nil || sent != hb) {
			hb.Close() // not sent, or CheckRedirect gave the hop another body
		}
		switch {
		case err == http.ErrUseLastResponse:
			return resp, nil
		case err != nil:
			return resp, fail(next, err)
		case sent != nil && sent.x == x:
			x.hand(sent)
		}
		req = next
	}
}

// redirect returns how the client follows a response of status to a
// request with method: the hop's method, and whether the hop keeps the
// request's content, which a change to GET drops (RFC 9110 section 15.4).
// ok is false for a status the client does not follow.
func redirect(method string, status int) (hop string, keep, ok bool) {
	switch {
	case status == 303 && method != "HEAD", (status == 301 || status == 302) && method == "POST":
		return "GET", false, true
	case status == 303:
		return method, false, true
	case status == 301, status == 302, status == 307, status == 308:
		return method, true, true
	}
	return "", false, false
}

// hop returns the request that follows req to u, answering resp, with
// method. Its URL is u less the call's query credentials, which the server
// may have repeated. It carries req's header fields, less the content
// fields unless keep is set; once the chain has left the call's origin,
// only the Content-Type the client wrote and a User-Agent with no value,
// which suppresses net/http's. With a jar, its Cookie field leaves out the
// cookies resp sets, which the jar supplies.
func (x *exchange) hop(req *http.Request, resp *http.Response, method string, u *url.URL, keep, left bool) *http.Request {
	if names := x.queryNames(); names != nil && u.RawQuery != "" {
		u.RawQuery = withQuery(u.RawQuery, isOneOf(names), nil)
	}
	h := make(http.Header, len(req.Header))
	for k, vs := range req.Header {
		ck := textproto.CanonicalMIMEHeaderKey(k)
		switch {
		case !keep && (strings.HasPrefix(ck, "Content-") || ck == "Digest" || ck == "Last-Modified"):
		case !left, ck == "Content-Type" && len(vs) == 1 && vs[0] == x.payload.ctype, ck == "User-Agent" && len(vs) == 0:
			h[k] = vs
		}
	}
	// As net/http has it (Go issue 17494).
	if x.cfg.jar != nil && len(h["Cookie"]) > 0 {
		if set := resp.Cookies(); len(set) > 0 {
			names := make([]string, len(set))
			for i, c := range set {
				names[i] = c.Name
			}
			if list := withCookies(strings.Join(h["Cookie"], "; "), isOneOf(names), nil); list != "" {
				h["Cookie"] = []string{list}
			} else {
				delete(h, "Cookie")
			}
		}
	}
	return (&http.Request{Method: method, URL: u, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: h, Response: resp}).WithContext(x)
}

// discard reads and closes the body of a response the client follows,
// reading at most 2 KiB, so that a small one leaves the connection
// reusable, as net/http does.
func discard(resp *http.Response) {
	const most = 2 << 10
	if resp.ContentLength == -1 || resp.ContentLength <= most {
		io.CopyN(io.Discard, resp.Body, most)
	}
	resp.Body.Close()
}

// noFollow is the Transport of the http.Client the client sends with: the
// caller's, or http.DefaultTransport at each send for none, moving the
// Location of a 301, 302, 303, 307 or 308 response to heldLocation, so that
// the http.Client finds none and returns the response: it never parses the
// Location, calls GetBody or consults CheckRedirect. The client moves the
// Location back when Do returns, and follows redirects itself.
type noFollow struct{ rt http.RoundTripper }

// heldLocation holds a Location from the transport; with a colon, it is no
// field name HTTP/1 can carry or HTTP/2 accepts.
const heldLocation = "openapi:Location"

func (t noFollow) RoundTrip(r *http.Request) (*http.Response, error) {
	rt := t.transport()
	resp, err := rt.RoundTrip(r)
	switch {
	case err != nil:
	case resp == nil: // what net/http would say of the caller's transport, which it does not see
		return nil, fmt.Errorf("http: RoundTripper implementation (%T) returned a nil *Response with a nil error", rt)
	case resp.Body == nil && resp.ContentLength > 0 && r.Method != "HEAD":
		return nil, fmt.Errorf("http: RoundTripper implementation (%T) returned a *Response with content length %d but a nil Body", rt, resp.ContentLength)
	case redirection(resp.StatusCode):
		move(resp.Header, "Location", heldLocation)
	}
	return resp, err
}

// redirection reports whether net/http follows a response of status.
func redirection(status int) bool {
	return status == 301 || status == 302 || status == 303 || status == 307 || status == 308
}

// move renames the field from of h to.
func move(h http.Header, from, to string) {
	if vs, ok := h[from]; ok {
		delete(h, from)
		h[to] = vs
	}
}

// CancelRequest passes on the cancellation the http.Client makes, for its
// Timeout, to a transport that has only this way to stop.
func (t noFollow) CancelRequest(r *http.Request) {
	if c, ok := t.transport().(interface{ CancelRequest(*http.Request) }); ok {
		c.CancelRequest(r)
	}
}

func (t noFollow) transport() http.RoundTripper {
	if t.rt == nil {
		return http.DefaultTransport
	}
	return t.rt
}

// tenRedirects is net/http's default CheckRedirect.
func tenRedirects(_ *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// errChainTimeout ends a chain whose HTTPClient.Timeout ran out before a
// hop, reported as net/http reports that Timeout: a timeout that matches
// context.DeadlineExceeded.
type errChainTimeout struct{}

func (errChainTimeout) Error() string     { return "Client.Timeout exceeded before a redirect hop" }
func (errChainTimeout) Timeout() bool     { return true }
func (errChainTimeout) Is(err error) bool { return err == context.DeadlineExceeded }

// urlErrorOp is the Op of a *url.Error for a request with method, as
// net/http writes it.
func urlErrorOp(method string) string {
	if method == "" {
		return "Get"
	}
	return method[:1] + strings.ToLower(method[1:])
}
