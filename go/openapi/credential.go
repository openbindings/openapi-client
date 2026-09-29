package openapi

import "context"

// A Credential satisfies one security scheme. How and where the client
// uses one, and when it refuses a call instead, is in the package
// documentation, under Credentials.
//
// A Credential keeps its secret out of reach: printing one shows no
// secret, and no error the client creates contains one. The zero
// Credential, like an empty secret, is no credential.
type Credential struct {
	source func(context.Context) (string, error)
	kind   int
}

// Secret returns a Credential holding one secret, whose meaning follows the
// scheme's type:
//
//   - apiKey: the key, sent in the declared header, query parameter or
//     cookie.
//   - http bearer, oauth2 and openIdConnect: the token, sent as
//     "Authorization: Bearer <token>".
//   - http basic (and Swagger 2.0 basic): the user-id and password joined
//     by a colon, as RFC 7617 writes them, sent base64-encoded. [Basic]
//     builds and checks it.
//   - any other http scheme: everything after the scheme name in the
//     Authorization field.
//
// An empty secret, as from an environment variable that was never set, is
// no credential.
func Secret(secret string) Credential {
	panic("unimplemented")
}

// SecretFunc returns a Credential whose secret, as for [Secret], f returns
// when a request is about to be sent, so it can return a token that
// refreshes or a secret looked up per tenant. f receives the call's
// context, with its deadline, cancellation and values, but not the
// operation: a request f makes is not labelled or retried as the call's
// operation by middleware that asks OperationFromContext.
//
// f is called once for each request the client builds: the call's first
// request, and each redirect hop within the origin that the client
// follows, where credentials are placed again. A retry made by the caller's
// own transport resends the request as built, credential included, without
// calling f. f is called concurrently from every goroutine that sends, so
// it must be safe for concurrent use, and should cache what it returns and
// refresh it once rather than on every call at expiry, as
// oauth2.ReuseTokenSource does. An error from f, or an empty secret, on the
// first request refuses the call with a *RequestError: nothing is sent. On a
// redirect hop the first request has already been sent, so the call ends
// with the *url.Error the http.Client returns, wrapping f's error. An error
// from f is passed on as it is.
//
// For a golang.org/x/oauth2 TokenSource ts:
//
//	openapi.SecretFunc(func(context.Context) (string, error) {
//		t, err := ts.Token()
//		if err != nil {
//			return "", err
//		}
//		return t.AccessToken, nil
//	})
func SecretFunc(f func(ctx context.Context) (string, error)) Credential {
	panic("unimplemented")
}

// Basic returns a Credential for http basic authentication (RFC 7617),
// sent in UTF-8. A username containing a colon, or either value containing
// a control character, refuses the call.
func Basic(username, password string) Credential {
	panic("unimplemented")
}

// FromTransport returns a Credential that marks a scheme as satisfied by
// the HTTPClient itself, such as an oauth2.Transport or a request-signing
// RoundTripper. The client adds nothing for the scheme, but counts it as
// satisfied after the caller selects a security alternative.
//
// Such a transport sees every hop of a redirect, other origins included,
// and plain http too: it must decide for itself where its credential goes,
// for example by comparing each request's URL with the first one's. A
// credential the client places itself is kept to the call's origin.
func FromTransport() Credential {
	panic("unimplemented")
}
