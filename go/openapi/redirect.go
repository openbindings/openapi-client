package openapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// follow sends signed, which is req with the call's credentials, and each
// redirect hop the client follows (see Redirects), returning the last
// response. The http.Client follows none itself. Each response's Request is
// the request without the credentials the client placed, and so is every
// earlier one reachable from it.
func (x *exchange) follow(req, signed *http.Request) (*http.Response, error) {
	first := req
	var via []*http.Request
	left := false // a hop has left the call's origin
	for {
		resp, err := x.cfg.client.Do(signed)
		if err != nil {
			return nil, redact(err, req.URL)
		}
		resp.Request = req
		if x.cfg.Redirects != FollowAll {
			return resp, nil
		}
		method, body, ok := redirect(req, resp.StatusCode)
		loc := resp.Header.Get("Location")
		if !ok || loc == "" {
			return resp, nil
		}
		u, err := req.URL.Parse(loc)
		if err != nil {
			resp.Body.Close()
			return nil, &url.Error{Op: urlErrorOp(first.Method), URL: req.URL.Redacted(), Err: fmt.Errorf("failed to parse Location header %q: %v", loc, err)}
		}
		via = append(via, req)
		left = left || !sameOrigin(u, first.URL)
		next := x.hop(req, resp, method, u, body, left)
		err = x.cfg.checkRedirect(next, via)
		if err == http.ErrUseLastResponse {
			return resp, nil
		}
		discard(resp)
		if err != nil {
			return nil, &url.Error{Op: urlErrorOp(first.Method), URL: loc, Err: err}
		}
		// CheckRedirect may have changed the URL.
		left = left || next.URL == nil || !sameOrigin(next.URL, first.URL)
		signed = next
		if !left {
			if signed, err = x.sign(next); err != nil {
				return nil, &url.Error{Op: urlErrorOp(first.Method), URL: next.URL.Redacted(), Err: errors.Join(err.(*RequestError).Unwrap()...)}
			}
		}
		// The body, once CheckRedirect has let the hop go: an upload that
		// begins is waited for.
		if body {
			if next.Body, err = next.GetBody(); err != nil {
				return nil, &url.Error{Op: urlErrorOp(first.Method), URL: next.URL.Redacted(), Err: err}
			}
			signed.Body = next.Body
		} else {
			x.end(x.newGeneration(), nil) // the upload is the hop's, of no body
		}
		req = next
	}
}

// redirect returns how the client follows a response of status to req: the
// hop's method, and whether it sends req's body again. ok is false when the
// client does not follow it: for another status, or a body that cannot be
// sent again.
func redirect(req *http.Request, status int) (method string, body, ok bool) {
	switch {
	case status == 303 && req.Method != "HEAD", (status == 301 || status == 302) && req.Method == "POST":
		return "GET", false, true
	case status == 303:
		return req.Method, false, true
	case status != 301 && status != 302 && status != 307 && status != 308:
		return "", false, false
	}
	body = req.Body != nil && req.Body != http.NoBody
	return req.Method, body, !body || req.GetBody != nil
}

// hop returns the request that follows req to u, answering resp, with
// method, and req's body when body is set. It carries req's header fields,
// or, once the chain has left the call's origin, only the Content-Type of
// the body; a hop without the body carries none of its content fields
// (RFC 9110 section 15.4).
func (x *exchange) hop(req *http.Request, resp *http.Response, method string, u *url.URL, body, left bool) *http.Request {
	next, _ := http.NewRequestWithContext(x, method, "", nil)
	next.URL, next.Response = u, resp
	for k, vs := range req.Header {
		switch {
		case !body && (strings.HasPrefix(k, "Content-") || k == "Digest" || k == "Last-Modified"):
		case left && k != "Content-Type":
		default:
			next.Header[k] = vs
		}
	}
	if body {
		next.GetBody, next.ContentLength = req.GetBody, req.ContentLength
	}
	return next
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

// followNone is the CheckRedirect of the http.Client the client sends with,
// which follows no redirect, as the client follows them itself. For a 307
// or 308, the http.Client has already taken the body again, with GetBody,
// for the hop req, which is never sent.
func followNone(req *http.Request, _ []*http.Request) error {
	if b, ok := req.Body.(*sentBody); ok && b.x != nil {
		b.x.unsent(b.gen)
		if b.rc != nil {
			b.rc.Close()
		}
	}
	return http.ErrUseLastResponse
}

// tenRedirects is net/http's default CheckRedirect.
func tenRedirects(_ *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// urlErrorOp is the Op of a *url.Error for a request with method, as
// net/http writes it.
func urlErrorOp(method string) string {
	if method == "" {
		return "Get"
	}
	return method[:1] + strings.ToLower(method[1:])
}
