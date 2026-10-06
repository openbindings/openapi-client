package openapi_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Scaling tests for security and redirects: an operation with many
// alternatives and schemes, and a document with many operations sharing
// security. Many alternatives and schemes compile linearly; a long redirect
// chain costs linearly and is bounded by the hop limit. The harness is in
// regress2_scale_test.go.

const scaleBase = "https://api.example.test"

// manyAlternatives returns a document whose one operation, "a", offers n
// alternatives, alternative i being {"s<i>":[],"t<i>":["r<i>"]}, with s<i>
// an apiKey header X-S<i> and t<i> an oauth2 scheme.
func manyAlternatives(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"` + scaleBase + `"}],`)
	b.WriteString(`"paths":{"/a":{"get":{"operationId":"a","security":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"s%d":[],"t%d":["r%d"]}`, i, i, i)
	}
	b.WriteString(`]}}},"components":{"securitySchemes":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"s%d":{"type":"apiKey","in":"header","name":"X-S%d"},"t%d":{"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://auth.example.test/token","scopes":{}}}}`, i, i, i)
	}
	b.WriteString(`}}}`)
	return []byte(b.String())
}

// manySchemes returns a document whose one operation, "a", has one
// alternative of n apiKey headers X-H<i> and n apiKey query parameters
// q<i>, and one oauth2 scheme with n scopes, listed in decreasing order.
func manySchemes(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"` + scaleBase + `"}],`)
	b.WriteString(`"paths":{"/a":{"get":{"operationId":"a","security":[{`)
	for i := range n {
		fmt.Fprintf(&b, `"h%d":[],"q%d":[],`, i, i)
	}
	b.WriteString(`"o":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"scope%06d"`, n-i)
	}
	b.WriteString(`]}]}}},"components":{"securitySchemes":{"o":{"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://auth.example.test/token","scopes":{}}}}`)
	for i := range n {
		fmt.Fprintf(&b, `,"h%d":{"type":"apiKey","in":"header","name":"X-H%d"},"q%d":{"type":"apiKey","in":"query","name":"q%d"}`, i, i, i, i)
	}
	b.WriteString(`}}}`)
	return []byte(b.String())
}

// sharedSecurity returns a document of n operations that inherit root
// security with three alternatives, and n more that each list their own
// copy of them.
func sharedSecurity(n int) []byte {
	const alts = `[{"k":[]},{"o":["read"]},{"k":[],"o":["read"]}]`
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"` + scaleBase + `"}],"security":` + alts + `,"paths":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"/i%d":{"get":{"operationId":"i%d"}},"/o%d":{"get":{"operationId":"o%d","security":%s}}`, i, i, i, i, alts)
	}
	b.WriteString(`},"components":{"securitySchemes":{"k":{"type":"apiKey","in":"header","name":"K"},"o":{"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://auth.example.test/token","scopes":{}}}}}}}`)
	return []byte(b.String())
}

// memOptions is Options over a fresh in-memory transport.
func memOptions(creds map[string]openapi.Credential) *openapi.Options {
	hc, _ := memClient()
	return &openapi.Options{HTTPClient: hc, Credentials: creds}
}

// warmClient parses doc with opts and compiles every operation.
func warmClient(t *testing.T, doc []byte, opts *openapi.Options) *openapi.Client {
	t.Helper()
	c, err := openapi.Parse(context.Background(), doc, testDocURI, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range c.Operations() {
		if op.Err != nil {
			t.Fatalf("%s: %v", op.Key, op.Err)
		}
	}
	return c
}

// An operation with many alternatives: compiling it, Load's checks of
// Credentials and Options.Security, and selecting the last alternative by
// key cost time linear in the alternatives.
func TestManyAlternativesScale(t *testing.T) {
	wantLinear(t, "Operations(), first use", 500, func(n int) func() {
		return timedOperations(t, manyAlternatives(n))
	})
	wantLinear(t, "Load with Credentials and Options.Security", 500, func(n int) func() {
		creds := map[string]openapi.Credential{}
		for i := range n {
			creds["s"+strconv.Itoa(i)] = openapi.Secret("k")
			creds["t"+strconv.Itoa(i)] = openapi.Secret("k")
		}
		last := strconv.Itoa(n - 1)
		return parseDoc(t, manyAlternatives(n), &openapi.Options{Credentials: creds, Security: []string{"s" + last, "t" + last}})
	})
	wantLinear(t, "Load with Options.SecurityKey", 500, func(n int) func() {
		last := strconv.Itoa(n - 1)
		return parseDoc(t, manyAlternatives(n), &openapi.Options{SecurityKey: `{"s` + last + `":[],"t` + last + `":["r` + last + `"]}`})
	})
	wantLinear(t, "calls selecting the last alternative", 500, func(n int) func() {
		last := strconv.Itoa(n - 1)
		c := warmClient(t, manyAlternatives(n), memOptions(map[string]openapi.Credential{
			"s" + last: openapi.Secret("k"), "t" + last: openapi.Secret("tok"),
		}))
		in := &openapi.Input{Security: `{"s` + last + `":[],"t` + last + `":["r` + last + `"]}`}
		return func() {
			for range 20 {
				if _, err := c.Call(context.Background(), "a", in, nil); err != nil {
					t.Fatal(err)
				}
			}
		}
	})
}

// One alternative with many schemes, and a scheme with many scopes:
// compiling the key, checking that no two schemes set one field, and
// placing every credential are linear (a pairwise destination check would
// be quadratic in time, and building the query by repeated concatenation
// quadratic in bytes).
func TestManySchemesScale(t *testing.T) {
	creds := func(n int) map[string]openapi.Credential {
		m := map[string]openapi.Credential{"o": openapi.Secret("tok")}
		for i := range n {
			m["h"+strconv.Itoa(i)] = openapi.Secret("hv")
			m["q"+strconv.Itoa(i)] = openapi.Secret("qv")
		}
		return m
	}
	wantLinear(t, "Operations(), first use", 1000, func(n int) func() {
		return timedOperations(t, manySchemes(n))
	})
	call := func(n int) func() {
		c := warmClient(t, manySchemes(n), memOptions(creds(n)))
		return func() {
			if _, err := c.Call(context.Background(), "a", nil, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	wantLinear(t, "a call placing every credential, time", 1000, call)
	wantLinearBytes(t, "a call placing every credential, bytes", 1000, call)

	// The call places all of them.
	hc, rt := memClient()
	c := warmClient(t, manySchemes(3), &openapi.Options{HTTPClient: hc, Credentials: creds(3)})
	if _, err := c.Call(t.Context(), "a", nil, nil); err != nil {
		t.Fatal(err)
	}
	// Query credentials in the order the alternative lists their schemes
	// (doc.go, Credentials).
	if reqs := rt.requests(); len(reqs) != 1 || reqs[0].URL.RawQuery != "q0=qv&q1=qv&q2=qv" ||
		reqs[0].Header.Get("X-H2") != "hv" || reqs[0].Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("the call did not place every credential: %+v", reqs)
	}
}

// Many operations sharing root security, and as many listing their own:
// Load's checks, first use of every operation, and a call to each are
// linear in the operations.
func TestSharedSecurityScale(t *testing.T) {
	opts := func() *openapi.Options {
		o := memOptions(map[string]openapi.Credential{"k": openapi.Secret("kv"), "o": openapi.Secret("tok")})
		o.Security = []string{"o"}
		return o
	}
	wantLinear(t, "Load with Credentials and Options.Security", 1000, func(n int) func() {
		return parseDoc(t, sharedSecurity(n), opts())
	})
	wantLinear(t, "Load with Options.SecurityKey", 1000, func(n int) func() {
		return parseDoc(t, sharedSecurity(n), &openapi.Options{SecurityKey: `{"k":[],"o":["read"]}`})
	})
	wantLinear(t, "a first call to every operation", 250, func(n int) func() {
		doc := sharedSecurity(n)
		cs := freshClients(t, doc, opts, scaleRuns)
		i := 0
		return func() {
			c := cs[i%len(cs)]
			i++
			for j := range n {
				for _, key := range []string{"i" + strconv.Itoa(j), "o" + strconv.Itoa(j)} {
					if _, err := c.Call(context.Background(), key, nil, nil); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	})
}

// chainRT answers /h<i> with a 307 to /h<i+1> until hops redirects have
// been made, then 200, in memory.
func chainRT(hops int) *memRT {
	return &memRT{answer: func(r *http.Request) (*http.Response, error) {
		i := 0
		if s, ok := strings.CutPrefix(r.URL.Path, "/h"); ok {
			i, _ = strconv.Atoi(s)
		}
		if i < hops {
			return memResponse(r, 307, http.Header{"Location": {"/h" + strconv.Itoa(i+1)}}, ""), nil
		}
		return memResponse(r, 200, nil, "{}"), nil
	}}
}

// A long same-origin redirect chain carrying credentials, with a caller's
// CheckRedirect that allows it: following it, placing credentials on every
// hop, and removing them from every request reachable from Response.Request
// cost linearly in the hops.
func TestRedirectChainScale(t *testing.T) {
	doc := []byte(expand(doc31(`"/h0":{"post":{"operationId":"p","requestBody":{"content":{"application/json":{}}}}}`,
		`"security":[{"key_h":[],"key_q":[],"bearer":[]}]`,
		`"components":{"securitySchemes":{
			"key_h":{"type":"apiKey","in":"header","name":"X-API-Key"},
			"key_q":{"type":"apiKey","in":"query","name":"api_key"},
			"bearer":{"type":"http","scheme":"bearer"}}}`), scaleBase))
	run := func(n int) func() {
		c, err := openapi.Parse(context.Background(), doc, testDocURI, &openapi.Options{
			Redirects: openapi.FollowAll,
			HTTPClient: &http.Client{Transport: chainRT(n), CheckRedirect: func(*http.Request, []*http.Request) error {
				return nil
			}},
			Credentials: map[string]openapi.Credential{
				"key_h": openapi.Secret(hSecret), "key_q": openapi.Secret(qSecret), "bearer": fixedSource(bToken).credential(),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return func() {
			resp, err := c.Call(context.Background(), "p", &openapi.Input{Body: map[string]any{"n": 1}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(requestChain(resp.Request)); got > n+1 {
				t.Fatalf("Response.Request chain of %d for %d hops", got, n)
			}
		}
	}
	wantLinearAllocs(t, "allocations", 50, run)
	wantLinear(t, "time", 100, run)
}
