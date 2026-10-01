package openapi

import (
	"context"
	"errors"
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
		if !ref.IsAbs() && req.Host != "" && req.Host != req.URL.Host {
			next.Host = req.Host // as net/http keeps it (Go issue 22233)
		}
		via = append(via, req)

		// CheckRedirect sees the body the hop sends, as net/http shows it.
		var hb *sentBody
		if body {
			if hb, err = x.newBody(); err != nil {
				discard(resp)
				return resp, fail(next, err)
			}
			next.Body, next.GetBody, next.ContentLength = hb, first.GetBody, first.ContentLength
		}
		err = cfg.checkRedirect(next, via)
		if err == nil && !deadline.IsZero() {
			hc = new(http.Client)
			*hc = *cfg.client
			if hc.Timeout = time.Until(deadline); hc.Timeout <= 0 {
				err = errChainTimeout{}
			}
		}
		var re RequestError
		if err == nil {
			// CheckRedirect may have moved the hop.
			left = left || next.URL == nil || !sameOrigin(next.URL, first.URL)
			if signed = x.sign(next, !left, false, &re); re.refused() != nil {
				err = re.sent()
			}
		}
		switch {
		case hb == nil:
		case err == nil && next.Body == hb:
			x.hand(hb)
		default: // not sent, or CheckRedirect gave the hop another body
			hb.Close()
		}
		if err == http.ErrUseLastResponse {
			return resp, nil
		}
		discard(resp)
		if err != nil {
			return resp, fail(next, err)
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
// which suppresses net/http's.
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
// caller's, or http.DefaultTransport for none, moving the Location of a
// 301, 302, 303, 307 or 308 response to heldLocation, so that the
// http.Client finds none and returns the response: it never parses the
// Location, calls GetBody or consults CheckRedirect. The client moves the
// Location back when Do returns, and follows redirects itself.
type noFollow struct{ rt http.RoundTripper }

// heldLocation holds a Location from the transport; with a space, it is no
// field name a response can have.
const heldLocation = "Location held by openapi"

func (t noFollow) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.rt.RoundTrip(r)
	if err == nil && resp != nil && redirection(resp.StatusCode) {
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
	if c, ok := t.rt.(interface{ CancelRequest(*http.Request) }); ok {
		c.CancelRequest(r)
	}
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
