package openapi_test

import (
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// allowReserved (doc.go, Fixed rules, Percent-encoding: "allowReserved
// applies to query parameters, and in OpenAPI 3.2 to path parameters and
// form-style cookie parameters too; elsewhere it is ignored. Where it
// applies, RFC 6570 reserved expansion is used exactly: reserved characters
// and existing %XX triples pass through, and the caller supplies any
// percent-encoding OpenAPI leaves to the application"). RFC 6570 section
// 3.2.3 (reserved expansion allows "the set (unreserved / reserved /
// pct-encoded)") and section 3.2.1 ("the percent character ("%") is only
// allowed as part of a pct-encoded triplet"); OAS 3.1.2 section 4.8.12.2.2
// ("Applications are still responsible for percent-encoding reserved
// characters that are not allowed in the query string ([, ], #)") and
// Appendix C.3 (non-RFC 6570 styles take "regular or reserved expansion
// (based on allowReserved)"). Reserved expansion covers member names too,
// while parameter names always follow the name rule (doc.go: "RFC 6570
// reserved expansion is used exactly, member names included (parameter
// names always follow the rule above)").
func TestAllowReservedQuery(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/f":{"get":{"operationId":"f","parameters":[{"name":"p","in":"query","allowReserved":true,"schema":{}}]}},
		"/n":{"get":{"operationId":"n","parameters":[{"name":"p","in":"query","explode":false,"allowReserved":true,"schema":{}}]}},
		"/s":{"get":{"operationId":"s","parameters":[{"name":"p","in":"query","style":"spaceDelimited","explode":false,"allowReserved":true,"schema":{}}]}},
		"/pd":{"get":{"operationId":"pd","parameters":[{"name":"p","in":"query","style":"pipeDelimited","explode":false,"allowReserved":true,"schema":{}}]}},
		"/d":{"get":{"operationId":"d","parameters":[{"name":"p","in":"query","style":"deepObject","allowReserved":true,"schema":{}}]}},
		"/name":{"get":{"operationId":"name","parameters":[{"name":"a/b c","in":"query","allowReserved":true,"schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	tests := []struct {
		name, key, param string
		v                any
		want             string
	}{
		{"gen-delims and sub-delims pass", "f", "p", ":/?@!$&'()*+,;=", "/f?p=:/?@!$&'()*+,;="},
		{"brackets and hash pass", "f", "p", "a[0]#x", "/f?p=a[0]#x"},
		{"triples pass", "f", "p", "x%2By%2fz", "/f?p=x%2By%2fz"},
		{"lone percent encoded", "f", "p", "50%", "/f?p=50%25"},
		{"percent not before two hex digits", "f", "p", "%zz%4", "/f?p=%25zz%254"},
		{"unsafe characters encoded", "f", "p", " \"<>\\^`{|}", "/f?p=%20%22%3C%3E%5C%5E%60%7B%7C%7D"},
		{"non-ASCII encoded", "f", "p", "é", "/f?p=%C3%A9"},
		{"unreserved", "f", "p", "AZaz09-._~", "/f?p=AZaz09-._~"},
		{"number", "f", "p", 1e21, "/f?p=1e+21"},
		{"array exploded", "f", "p", []string{"a/b", "c,d"}, "/f?p=a/b&p=c,d"},
		{"object exploded", "f", "p", map[string]string{"k": "a/b", "m": "c=d"}, "/f?k=a/b&m=c=d"},
		{"array", "n", "p", []string{"a/b", "c,d"}, "/n?p=a/b,c,d"},
		{"object", "n", "p", map[string]string{"k": "a/b"}, "/n?p=k,a/b"},
		// OAS 3.1.2 Appendix C.4.2's words, with / passed through.
		{"spaceDelimited", "s", "p", []string{"x/y", "is", "fun"}, "/s?p=x/y%20is%20fun"},
		{"spaceDelimited keeps its delimiter", "s", "p", []string{"a b", "c"}, "/s?p=a%20b%20c"},
		{"pipeDelimited", "pd", "p", []string{"a/b", "c|d"}, "/pd?p=a/b%7Cc%7Cd"},
		{"deepObject", "d", "p", map[string]string{"k": "a/b?c"}, "/d?p%5Bk%5D=a/b?c"},
		// Member names take reserved expansion (doc.go, Fixed rules,
		// Percent-encoding), in every style
		// and whether or not the object is exploded; a character outside
		// the reserved and unreserved sets is still encoded.
		{"member names exploded", "f", "p", map[string]string{"a/b": "c", "d[e]": "f"}, "/f?a/b=c&d[e]=f"},
		{"member names", "n", "p", map[string]string{"k/1": "v"}, "/n?p=k/1,v"},
		{"member name with a space", "f", "p", map[string]string{"k 1:": "v"}, "/f?k%201:=v"},
		{"member name triple", "f", "p", map[string]string{"k%2F": "v"}, "/f?k%2F=v"},
		{"spaceDelimited member names", "s", "p", map[string]string{"k/1": "v?"}, "/s?p=k/1%20v?"},
		{"pipeDelimited member names", "pd", "p", map[string]string{"k=1": "v"}, "/pd?p=k=1%7Cv"},
		{"deepObject member names", "d", "p", map[string]any{"a/b": map[string]string{"c:d": "e"}}, "/d?p%5Ba/b%5D%5Bc:d%5D=e"},
		// A parameter name is literal template text, not a value: the name
		// rule applies (doc.go: "parameter names always follow the rule
		// above"; OAS 3.1.2 Appendix C.3).
		{"name encoded", "name", "a/b c", "x/y", "/name?a%2Fb%20c=x/y"},
		{"name encoded, object", "name", "a/b c", map[string]string{"k": "v"}, "/name?k=v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, re := callOne(t, w, c, tt.key, tt.param, tt.v)
			if re != nil {
				t.Fatalf("refused: %v", re)
			}
			if got.RequestURI != tt.want {
				t.Errorf("request target %q, want %q", got.RequestURI, tt.want)
			}
		})
	}
	// OAS 3.1.2 Appendix C.4.2: formulas exploded with allowReserved true
	// and "values ... pre-percent encoded" for +, and words space-delimited:
	// ?a=x%2By&b=x/y&c=x%5Ey&words=math%20is%20fun.
	doc = doc31(`"/c42":{"get":{"operationId":"c42","parameters":[
		{"name":"formulas","in":"query","explode":true,"allowReserved":true,"schema":{"type":"object","additionalProperties":{"type":"string"}}},
		{"name":"words","in":"query","style":"spaceDelimited","explode":false,"schema":{"type":"array","items":{"type":"string"}}}]}}`)
	c42 := parseFor(t, w, doc, nil)
	mustCall(t, c42, "c42", &openapi.Input{Params: map[string]any{
		"formulas": map[string]string{"a": "x%2By", "b": "x/y", "c": "x^y"},
		"words":    []string{"math", "is", "fun"},
	}}, nil)
	if got := w.last(t).RequestURI; got != "/c42?a=x%2By&b=x/y&c=x%5Ey&words=math%20is%20fun" {
		t.Errorf("request target %q, want OAS 3.1.2 Appendix C.4.2's", got)
	}
}

// OAS 3.1.2 section 4.8.12.2.2, allowReserved: "This field only applies to
// parameters with an in value of query"; doc.go, Fixed rules,
// Percent-encoding: "elsewhere it is ignored" (in 3.1, path and cookie
// parameters encode as without it; header values are never
// percent-encoded); describe.go, Param.AllowReserved: "the effective
// allowReserved: false where the edition ignores it".
func TestAllowReservedIgnoredOutsideQuery(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/p/{p}":{"get":{"operationId":"path","parameters":[{"name":"p","in":"path","required":true,"allowReserved":true,"schema":{}}]}},
		"/m{p}":{"get":{"operationId":"matrix","parameters":[{"name":"p","in":"path","required":true,"style":"matrix","allowReserved":true,"schema":{}}]}},
		"/c":{"get":{"operationId":"cookie","parameters":[{"name":"c","in":"cookie","allowReserved":true,"schema":{}}]}},
		"/h":{"get":{"operationId":"header","parameters":[{"name":"X-H","in":"header","allowReserved":true,"schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	got, re := callOne(t, w, c, "path", "p", "a/b%2F?")
	if re != nil || got.RequestURI != "/p/a%2Fb%252F%3F" {
		t.Errorf("path: %q, %v; want /p/a%%2Fb%%252F%%3F", got.RequestURI, re)
	}
	got, re = callOne(t, w, c, "matrix", "p", []string{"a/b", "c"})
	if re != nil || got.RequestURI != "/m;p=a%2Fb,c" {
		t.Errorf("matrix: %q, %v; want /m;p=a%%2Fb,c", got.RequestURI, re)
	}
	got, re = callOne(t, w, c, "cookie", "c", "a/b")
	if v := got.Header.Values("Cookie"); re != nil || len(v) != 1 || v[0] != "c=a%2Fb" {
		t.Errorf("cookie: %q, %v; want [c=a%%2Fb]", v, re)
	}
	got, re = callOne(t, w, c, "header", "X-H", "a/b%2F")
	if v := got.Header.Values("X-H"); re != nil || len(v) != 1 || v[0] != "a/b%2F" {
		t.Errorf("header: %q, %v; want [a/b%%2F]", v, re)
	}
	for _, key := range []string{"path", "matrix", "cookie", "header"} {
		if p := param(t, mustOp(t, c, key), 0); p.AllowReserved || p.Err != nil {
			t.Errorf("%s: AllowReserved %t, Err %v; want false, nil", key, p.AllowReserved, p.Err)
		}
	}
}
