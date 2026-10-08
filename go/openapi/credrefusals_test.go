package openapi_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Credential refusals: calls refused for a
// missing, empty or zero credential, an alternative two of whose schemes
// set one field, the plain-http rule and the URL scheme rule; Load's
// refusals of Credentials; Basic's own checks; and cookie credentials that
// cannot be written as given.

// doc.go, Credentials: "A call is refused, never sent without the
// authorization the caller selected, when a scheme in its selected
// alternative has no credential, or has an empty or zero one". errors.go,
// RequestError.Settings: keyed by the setting that fixes it,
// "Options.Credentials[\"api_key\"]"; RequestError: "It reports every
// independently detectable problem." client.go, With: a derived Client
// skips Load's name checks, "and any other Options the document cannot use
// refuse each call they affect".
func TestMissingEmptyOrZeroCredential(t *testing.T) {
	for name, edit := range map[string]func(*openapi.Options){
		"missing": func(o *openapi.Options) { delete(o.Credentials, "key_h"); delete(o.Credentials, "key_q") },
		"empty": func(o *openapi.Options) {
			o.Credentials["key_h"] = openapi.Secret("")
			o.Credentials["key_q"] = openapi.Secret("")
		},
		"zero": func(o *openapi.Options) {
			o.Credentials["key_h"] = openapi.Credential{}
			o.Credentials["key_q"] = openapi.Credential{}
		},
		"empty Basic": func(o *openapi.Options) {
			delete(o.Credentials, "key_q")
			o.Credentials["key_h"] = openapi.Basic("", "")
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWire(t, nil)
			d := credClient(t, w, nil).With(edit)
			// Both schemes of the alternative are reported.
			resp, err := d.Call(t.Context(), "two", nil, nil)
			re := refusedBeforeSending(t, w, resp, err)
			wantKeys(t, "Settings", re.Settings, false, credKey("key_h"), credKey("key_q"))
			if req, err := d.Prepare("two", nil); req != nil || err == nil {
				t.Errorf("Prepare = %v, %v; want the same refusal", req, err)
			}
			resp, err = d.Call(t.Context(), "keyHeader", nil, nil)
			re = refusedBeforeSending(t, w, resp, err)
			wantKeys(t, "Settings", re.Settings, true, credKey("key_h"))
			noSecrets(t, err, credSecrets...)
			// A call whose alternative does not use the scheme is unaffected.
			mustCall(t, d, "bearer", nil, nil)
		})
	}
}

// load.go, Load: "Load also fails, with a *RequestError, on Options the
// document cannot use: a Credentials name the document never uses, an empty
// static credential, or a credential its schemes cannot use". doc.go,
// Credentials: "Load refuses a Credentials name the document never uses, as
// a likely misspelling, an empty static credential (Secret(""), Basic("",
// "") or the zero Credential), a Basic credential for a name none of
// whose schemes is http basic, and any credential but FromTransport for a
// name all of whose schemes are mutualTLS"; "A credential value its
// destination cannot carry is refused at Options.Credentials["name"]: by
// Load for a static credential ... Such a value is a header field value with
// an ASCII control character other than a tab, or with leading or trailing
// whitespace". errors.go, RequestError.Settings: keyed
// Options.Credentials[<name>], the name quoted as strconv.Quote does.
func TestLoadRefusesCredentials(t *testing.T) {
	base := "https://api.example.test"
	tests := []struct {
		name  string
		creds map[string]openapi.Credential
		key   string
	}{
		{"unused name", map[string]openapi.Credential{"apiKey": openapi.Secret("unused-4Rf")}, credKey("apiKey")},
		{"unused name needing quotes", map[string]openapi.Credential{"we\"ird\n": openapi.Secret("unused-4Rf")},
			"Options.Credentials[" + strconv.Quote("we\"ird\n") + "]"},
		{"unused FromTransport", map[string]openapi.Credential{"nope": openapi.FromTransport()}, credKey("nope")},
		{"empty Secret", map[string]openapi.Credential{"bearer": openapi.Secret("")}, credKey("bearer")},
		{"empty Basic", map[string]openapi.Credential{"basic": openapi.Basic("", "")}, credKey("basic")},
		{"zero Credential", map[string]openapi.Credential{"key_h": {}}, credKey("key_h")},
		{"Basic for a bearer scheme", map[string]openapi.Credential{"bearer": openapi.Basic("u", "basic-pw-5Tg")}, credKey("bearer")},
		{"Basic for an apiKey", map[string]openapi.Credential{"key_h": openapi.Basic("u", "basic-pw-5Tg")}, credKey("key_h")},
		{"Basic for oauth2", map[string]openapi.Credential{"oauth": openapi.Basic("u", "basic-pw-5Tg")}, credKey("oauth")},
		{"Basic for mutualTLS", map[string]openapi.Credential{"mtls": openapi.Basic("u", "basic-pw-5Tg")}, credKey("mtls")},
		{"Secret for mutualTLS", map[string]openapi.Credential{"mtls": openapi.Secret("unused-4Rf")}, credKey("mtls")},
		{"SecretFunc for mutualTLS", map[string]openapi.Credential{"mtls": fixedSource("unused-4Rf").credential()}, credKey("mtls")},
		{"CR in a header key", map[string]openapi.Credential{"key_h": openapi.Secret("unused-4Rf\rX")}, credKey("key_h")},
		{"LF in a header key", map[string]openapi.Credential{"key_h": openapi.Secret("unused-4Rf\nX-Evil: 1")}, credKey("key_h")},
		{"NUL in a header key", map[string]openapi.Credential{"key_h": openapi.Secret("unused\x00-4Rf")}, credKey("key_h")},
		{"leading space in a header key", map[string]openapi.Credential{"key_h": openapi.Secret(" unused-4Rf")}, credKey("key_h")},
		{"trailing tab in a header key", map[string]openapi.Credential{"key_h": openapi.Secret("unused-4Rf\t")}, credKey("key_h")},
		{"CR LF in a bearer token", map[string]openapi.Credential{"bearer": openapi.Secret("unused-4Rf\r\nX-Evil: 1")}, credKey("bearer")},
		{"NUL in an oauth2 token", map[string]openapi.Credential{"oauth": openapi.Secret("unused\x00-4Rf")}, credKey("oauth")},
		{"trailing space in a bearer token", map[string]openapi.Credential{"bearer": openapi.Secret("unused-4Rf ")}, credKey("bearer")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseErr(t, credDoc, base, &openapi.Options{Credentials: tt.creds})
			re := asRequestError(t, err)
			wantKeys(t, "Settings", re.Settings, true, tt.key)
			noSecrets(t, err, "unused-4Rf", "basic-pw-5Tg")
		})
	}

	// Accepted: a Basic for a scheme spelled "Basic" (compared without
	// regard to case), FromTransport for a name a requirement uses but the
	// document never declares, and a credential source, which Load does not
	// call.
	doc := bare31(`"/g":{"get":{"security":[{"ghost":[]},{"basic_mc":[]},{"bearer":[]}]}}`, credSchemes)
	for name, creds := range map[string]map[string]openapi.Credential{
		"Basic for basic_mc":    {"basic_mc": openapi.Basic("u", "p")},
		"FromTransport, ghost":  {"ghost": openapi.FromTransport()},
		"SecretFunc for bearer": {"bearer": fixedSource("").credential()},
	} {
		if _, err := openapi.Parse(t.Context(), []byte(expand(doc, base)), testDocURI, &openapi.Options{Credentials: creds}); err != nil {
			t.Errorf("%s: Parse: %v", name, err)
		}
	}
}

// credential.go, Basic: "A username containing a colon, or either value
// containing an ASCII control character, is a value RFC 7617 forbids, refused
// as the package documentation says under Credentials" (RFC 7617 section 2:
// "a user-id containing a colon character is invalid", and user-id and
// password "MUST NOT contain any control characters", CTL in RFC 5234:
// %x00-1F / %x7F); doc.go, Credentials: such a value is refused "at
// Options.Credentials["name"]: by Load for a static credential (by each
// call, for a Client from Client.With)", so every static credential problem,
// Basic's included, is refused by Load (by each call for a Client.With's
// Options). The key is the setting that fixes it (errors.go,
// RequestError.Settings).
func TestBasicRefusals(t *testing.T) {
	w := newWire(t, nil)
	base := parseFor(t, w, credDoc, nil)
	for name, cred := range map[string]openapi.Credential{
		"colon in username":      openapi.Basic("al:ice", "basic-pw-5Tg"),
		"NUL in username":        openapi.Basic("al\x00ice", "basic-pw-5Tg"),
		"US in username":         openapi.Basic("al\x1fice", "basic-pw-5Tg"),
		"LF in password":         openapi.Basic("alice", "basic-pw-5Tg\n"),
		"CR in password":         openapi.Basic("alice", "basic\r-pw-5Tg"),
		"tab in password":        openapi.Basic("alice", "basic\t-pw-5Tg"),
		"DEL in password":        openapi.Basic("alice", "basic-pw-5Tg\x7f"),
		"control in both values": openapi.Basic("a\x01", "basic-pw-5Tg\x02"),
	} {
		t.Run(name, func(t *testing.T) {
			err := parseErr(t, credDoc, w.URL, &openapi.Options{Credentials: map[string]openapi.Credential{"basic": cred}})
			wantKeys(t, "Settings", asRequestError(t, err).Settings, true, credKey("basic"))
			noSecrets(t, err, "basic-pw-5Tg")

			d := base.With(func(o *openapi.Options) { o.Credentials["basic"] = cred })
			resp, err := d.Call(t.Context(), "basic", nil, nil)
			re := refusedBeforeSending(t, w, resp, err)
			wantKeys(t, "Settings", re.Settings, true, credKey("basic"))
			noSecrets(t, err, "basic-pw-5Tg")
		})
	}
	// A colon in the password is allowed (RFC 7617 section 2: the user-id
	// ends at the first colon).
	c := parseFor(t, w, credDoc, &openapi.Options{Credentials: map[string]openapi.Credential{"basic": openapi.Basic("alice", "a:b:c")}})
	mustCall(t, c, "basic", nil, nil)
}

// doc.go, Percent-encoding: "an apiKey sent in a cookie, [is] written as
// given too; a cookie value written as given that holds a ";" or a control
// character is refused."
func TestCookieCredentialRefused(t *testing.T) {
	w := newWire(t, nil)
	c := credClient(t, w, nil)
	for _, v := range []string{"ck-5Yh;admin=1", "ck-5Yh\x00", "ck-5Yh\r\nX-Evil: 1", "ck\x7f-5Yh"} {
		d := c.With(func(o *openapi.Options) { o.Credentials["key_c"] = openapi.Secret(v) })
		resp, err := d.Call(t.Context(), "keyCookie", nil, nil)
		refusedBeforeSending(t, w, resp, err)
		noSecrets(t, err, "ck-5Yh")
		src := fixedSource(v)
		d = c.With(func(o *openapi.Options) { o.Credentials["key_c"] = src.credential() })
		resp, err = d.Call(t.Context(), "keyCookie", nil, nil)
		refusedBeforeSending(t, w, resp, err)
		noSecrets(t, err, "ck-5Yh")
	}
}

// doc.go, Credentials: "An alternative two of whose schemes set the same
// header field, query name or cookie name cannot be used, and is refused
// naming both". errors.go, RequestError.Err: "a selected alternative two of
// whose schemes set the same field". Field names compare without regard to
// case (RFC 9110 section 5.1). "A FromTransport scheme places nothing, so it
// takes part in no destination rule", and "Unselected alternatives have no
// effect on the call."
func TestAlternativeSettingOneFieldTwice(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/c1":{"get":{"operationId":"bearerOAuth","security":[{"bearer":[],"oauth":[]}]}},
		"/c2":{"get":{"operationId":"basicBearer","security":[{"basic":[],"bearer":[]}]}},
		"/c3":{"get":{"operationId":"authzKeyBearer","security":[{"key_authz":[],"bearer":[]}]}},
		"/c4":{"get":{"operationId":"twoHeaderKeys","security":[{"key_h":[],"hk_other":[]}]}},
		"/c5":{"get":{"operationId":"unselected","security":[{"bearer":[],"oauth":[]},{"key_h":[]}]}},
		"/c6":{"get":{"operationId":"queryAndCookie","security":[{"key_q":[],"key_qc":[]}]}},
		"/c7":{"get":{"operationId":"twoQueryKeys","security":[{"key_q":[],"qk_other":[]}]}},
		"/c8":{"get":{"operationId":"twoCookieKeys","security":[{"key_qc":[],"ck_other":[]}]}}`,
		`"components":{"securitySchemes":{
			"bearer":{"type":"http","scheme":"bearer"},
			"oauth":{"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://auth.example.test/token","scopes":{}}}},
			"basic":{"type":"http","scheme":"basic"},
			"key_authz":{"type":"apiKey","in":"header","name":"authorization"},
			"key_h":{"type":"apiKey","in":"header","name":"X-API-Key"},
			"hk_other":{"type":"apiKey","in":"header","name":"x-api-key"},
			"key_q":{"type":"apiKey","in":"query","name":"k"},
			"key_qc":{"type":"apiKey","in":"cookie","name":"k"},
			"qk_other":{"type":"apiKey","in":"query","name":"k"},
			"ck_other":{"type":"apiKey","in":"cookie","name":"k"}
		}}`)
	creds := map[string]openapi.Credential{
		"bearer": openapi.Secret(bToken), "oauth": openapi.Secret(oToken), "basic": openapi.Basic(basicUser, basicPass),
		"key_authz": openapi.Secret("authz-6Uj"), "key_h": openapi.Secret(hSecret), "hk_other": openapi.Secret("h2-7Ik"),
		"key_q": openapi.Secret(qSecret), "key_qc": openapi.Secret(cSecret),
		"qk_other": openapi.Secret("q2-8Ol"), "ck_other": openapi.Secret("qc2-9Pz"),
	}
	c := parseFor(t, w, doc, &openapi.Options{Credentials: creds})
	for key, names := range map[string][2]string{
		"bearerOAuth":    {"bearer", "oauth"},
		"basicBearer":    {"basic", "bearer"},
		"authzKeyBearer": {"key_authz", "bearer"},
		"twoHeaderKeys":  {"key_h", "hk_other"},
		"twoQueryKeys":   {"key_q", "qk_other"},
		"twoCookieKeys":  {"key_qc", "ck_other"},
	} {
		t.Run(key, func(t *testing.T) {
			resp, err := c.Call(t.Context(), key, nil, nil)
			re := refusedBeforeSending(t, w, resp, err)
			if re.Err == nil {
				t.Errorf("RequestError.Err is nil")
			}
			if !errorContains(err, names[0], names[1]) {
				t.Errorf("error %q does not name both %s and %s", err, names[0], names[1])
			}
			noSecrets(t, err, bToken, oToken, basicPass, basicField, "authz-6Uj", hSecret, "h2-7Ik", qSecret, cSecret, "q2-8Ol", "qc2-9Pz")
		})
	}

	mustCall(t, c, "unselected", &openapi.Input{Security: `{"key_h":[]}`}, nil)
	wantField(t, w.last(t).Header, "X-API-Key", hSecret)

	// A query name and a cookie name are different destinations.
	mustCall(t, c, "queryAndCookie", nil, nil)
	r := w.last(t)
	wantURI(t, r, "/c6?k="+qSecret)
	wantField(t, r.Header, "Cookie", "k="+cSecret)

	// With one of the two from the transport, the other is placed.
	d := c.With(func(o *openapi.Options) { o.Credentials["oauth"] = openapi.FromTransport() })
	mustCall(t, d, "bearerOAuth", nil, nil)
	wantField(t, w.last(t).Header, "Authorization", "Bearer "+bToken)
}

// doc.go, Credentials: "Bearer tokens (http bearer, oauth2, openIdConnect)
// and Basic credentials are sent only over https or wss, as RFC 6750
// requires and RFC 7617 advises, or to a loopback host, where they do not
// leave the machine: a loopback IP address as net/netip parses one (so 127.1
// is a name), an IPv4-mapped one included, or the name localhost written
// exactly so, the one name http.ProxyFromEnvironment never sends through a
// proxy. Other names, such as those under .localhost, can be proxied or
// resolved elsewhere, so they are not loopback here. A call that would send
// one over plain http or ws to any other host is refused. ... A URL scheme
// other than http, https, ws or wss requires FromTransport for these
// credentials. Neither rule restricts API keys, which no RFC governs, or http
// schemes other than bearer and basic." errors.go, RequestError.Err: "a
// bearer or Basic credential that would go over plain http or ws". Loopback
// addresses are 127.0.0.0/8 and ::1 (RFC 6890, RFC 4291 section 2.5.3);
// "127.1" is not a loopback literal (RFC 3986 section 3.2.2's IPv4address
// has four parts). Loopback is a loopback IP literal (IPv4-mapped included)
// or the name "localhost" exactly, as net/http's proxy bypass reads it, so
// *.localhost, a trailing dot and case variants are refused over plain http.
// The transport here dials nothing, so a name the client resolved would show
// up only as a refusal.
func TestPlainHTTPRule(t *testing.T) {
	doc := bare31(credPaths, credSchemes)
	restricted := []string{"bearer", "bearerUpper", "basic", "basicMixedCase", "oauth", "oidc"}
	allowed := []string{
		"https://api.example.test",
		"https://api.example.test:8443",
		"wss://api.example.test",
		"http://localhost",
		"http://localhost:8080",
		"http://127.0.0.1",
		"http://127.0.0.1:8080",
		"http://127.8.9.10",
		"http://127.255.255.254",
		"http://[::1]",
		"http://[::1]:8080",
		"http://[::ffff:127.0.0.1]",
		"http://[::ffff:127.8.9.10]:8080",
		"http://[::ffff:7f00:1]",
		"ws://localhost",
		"ws://127.0.0.1:8080",
	}
	refused := []string{
		"http://api.example.test",
		"http://api.example.test:443",
		"http://localhost.example.test",
		"http://localhostx",
		"http://notlocalhost",
		"http://localhost-api.example.test",
		"http://127.0.0.1.example.test",
		"http://128.0.0.1",
		"http://126.255.255.255",
		"http://10.0.0.1",
		"http://192.168.1.1",
		"http://[::2]",
		"http://[fe80::1]",
		"http://[::ffff:10.0.0.1]",
		"http://127.1",
		"http://127.1:8080",
		"http://127.0.1",
		"http://localhost..",
		"http://LOCALHOST",
		"http://LOCALHOST:8080",
		"http://Localhost",
		"http://api.localhost",
		"http://a.b.localhost:9000",
		"http://localhost.",
		"http://localhost.:8080",
		"http://api.localhost.",
		"ws://LOCALHOST",
		"ws://localhost.",
		"ws://api.example.test",
		"ws://10.0.0.1",
		"ftp://api.example.test",
		"ftp://localhost",
		"unix://localhost",
	}
	run := func(t *testing.T, base, key string, creds map[string]openapi.Credential) (*memRT, *openapi.Response, error) {
		t.Helper()
		hc, rt := memClient()
		c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{HTTPClient: hc, BaseURL: base, Credentials: creds})
		if err != nil {
			t.Fatalf("Parse with BaseURL %s: %v", base, err)
		}
		resp, err := c.Call(t.Context(), key, nil, nil)
		return rt, resp, err
	}
	for _, base := range allowed {
		for _, key := range restricted {
			t.Run("sent "+key+" to "+base, func(t *testing.T) {
				rt, _, err := run(t, base, key, credSet())
				if err != nil {
					t.Fatalf("Call: %v", err)
				}
				if reqs := rt.requests(); len(reqs) != 1 || reqs[0].Header.Get("Authorization") == "" {
					t.Errorf("transport carried %d requests, want one with its Authorization", len(reqs))
				}
			})
		}
	}
	for _, base := range refused {
		for _, key := range restricted {
			t.Run("refused "+key+" to "+base, func(t *testing.T) {
				rt, resp, err := run(t, base, key, credSet())
				re := refusedBeforeSending(t, nil, resp, err)
				if rt.count() != 0 {
					t.Errorf("transport carried %d requests, want none", rt.count())
				}
				if strings.HasPrefix(base, "http:") || strings.HasPrefix(base, "ws:") {
					if re.Err == nil {
						t.Errorf("RequestError.Err is nil")
					}
				}
				noSecrets(t, err, credSecrets...)
			})
		}
	}

	// API keys are not restricted; a FromTransport scheme places nothing,
	// so the rule does not apply to it ("A caller whose network secures
	// plain http or ws another way places the credential through its own
	// transport, with FromTransport").
	for _, key := range []string{"keyHeader", "keyQuery", "keyCookie"} {
		t.Run("sent "+key+" over plain http", func(t *testing.T) {
			rt, _, err := run(t, "http://api.example.test", key, credSet())
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if rt.count() != 1 {
				t.Errorf("transport carried %d requests, want 1", rt.count())
			}
		})
	}
	for _, base := range []string{"http://api.example.test", "ws://api.example.test", "ftp://api.example.test"} {
		t.Run("FromTransport to "+base, func(t *testing.T) {
			creds := map[string]openapi.Credential{"bearer": openapi.FromTransport(), "basic": openapi.FromTransport()}
			for _, key := range []string{"bearer", "basic"} {
				rt, _, err := run(t, base, key, creds)
				if err != nil {
					t.Fatalf("%s: Call: %v", key, err)
				}
				if reqs := rt.requests(); len(reqs) != 1 || reqs[0].Header.Get("Authorization") != "" {
					t.Errorf("%s: transport carried %d requests, want one without Authorization", key, len(reqs))
				}
			}
		})
	}

	// The rule applies to the server a document names as it does to BaseURL.
	hc, rt := memClient()
	c := parseAt(t, credDoc, "http://api.example.test", "http://api.example.test/openapi.json",
		&openapi.Options{HTTPClient: hc, Credentials: credSet()})
	resp, err := c.Call(t.Context(), "bearer", nil, nil)
	refusedBeforeSending(t, nil, resp, err)
	if rt.count() != 0 {
		t.Errorf("transport carried %d requests to a document's plain http server", rt.count())
	}
}

// The plain-http rule names bearer tokens and Basic credentials; an http
// scheme other than those (here DPoP), like an API key, is not listed among
// the refusals (doc.go, Credentials: "A call is refused ... when ..."), so
// it is sent over plain http. Only Bearer and Basic are restricted over
// plain http (RFC 6750, RFC 7617); other http schemes, such as Digest and
// DPoP, are governed by their own RFCs, which the client does not model.
func TestPlainHTTPOtherHTTPScheme(t *testing.T) {
	hc, rt := memClient()
	c, err := openapi.Parse(t.Context(), []byte(bare31(credPaths, credSchemes)), testDocURI,
		&openapi.Options{HTTPClient: hc, BaseURL: "http://api.example.test", Credentials: credSet()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(t.Context(), "dpop", nil, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if reqs := rt.requests(); len(reqs) != 1 {
		t.Fatalf("transport carried %d requests, want 1", len(reqs))
	} else {
		wantAuthorization(t, reqs[0].Header, "DPoP", dProof)
	}
}

// doc.go, Credentials: "A credential value its destination cannot carry is
// refused at Options.Credentials["name"]: by Load for a static credential
// (by each call, for a Client from Client.With), by the call for a source's.
// Such a value is a header field value with a control character other than a
// tab, or with leading or trailing whitespace". The call is refused before
// anything is sent.
func TestHeaderUnsafeCredentialRefused(t *testing.T) {
	values := map[string]string{
		"CR":                  "hu-3Kl\rX",
		"LF":                  "hu-3Kl\nX-Evil: 1",
		"CR LF":               "hu-3Kl\r\nX-Evil: 1",
		"NUL":                 "hu\x00-3Kl",
		"leading space":       " hu-3Kl",
		"leading tab":         "\thu-3Kl",
		"trailing space":      "hu-3Kl ",
		"trailing tab":        "hu-3Kl\t",
		"trailing line break": "hu-3Kl\n",
	}
	for name, v := range values {
		for _, tt := range []struct{ key, scheme string }{{"keyHeader", "key_h"}, {"bearer", "bearer"}, {"oidc", "oidc"}} {
			if tt.scheme != "key_h" && strings.HasPrefix(name, "leading") {
				continue // a bearer token's leading whitespace is not at the start of the field
			}
			t.Run(name+" "+tt.scheme, func(t *testing.T) {
				w := newWire(t, nil)
				src := fixedSource(v)
				c := credClient(t, w, func(o *openapi.Options) { o.Credentials[tt.scheme] = src.credential() })
				resp, err := c.Call(t.Context(), tt.key, nil, nil)
				re := refusedBeforeSending(t, w, resp, err)
				wantKeys(t, "Settings", re.Settings, true, credKey(tt.scheme))
				noSecrets(t, err, "hu-3Kl")

				d := credClient(t, w, nil).With(func(o *openapi.Options) { o.Credentials[tt.scheme] = openapi.Secret(v) })
				resp, err = d.Call(t.Context(), tt.key, nil, nil)
				re = refusedBeforeSending(t, w, resp, err)
				wantKeys(t, "Settings", re.Settings, true, credKey(tt.scheme))
				noSecrets(t, err, "hu-3Kl")
			})
		}
	}
	// Whitespace inside a header key is carried as given.
	w := newWire(t, nil)
	c := credClient(t, w, func(o *openapi.Options) { o.Credentials["key_h"] = openapi.Secret("a b\tc") })
	mustCall(t, c, "keyHeader", nil, nil)
	wantField(t, w.last(t).Header, "X-API-Key", "a b\tc")
}
