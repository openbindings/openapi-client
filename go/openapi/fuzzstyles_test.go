package openapi_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// FuzzStyleSerialization (stage 2 brief, Tests: "comparing every RFC
// 6570-backed style against the oracle for arbitrary values"): for arbitrary
// strings, a primitive, a two-item array and a two-member object are
// prepared under every (location, style, explode, allowReserved)
// configuration OpenAPI 3.1 defines (styleConfigs), and the prepared request
// is compared with what styleCfg.expect derives: the RFC 6570 oracle for
// matrix, label, simple and form (a header's simple with percent-encoding
// removed, a cookie's form with pairs joined by "; "), and the OAS 3.1.2
// table and text for spaceDelimited, pipeDelimited and deepObject. On top of
// expect: a path value forming a whole "." or ".." segment is refused
// (doc.go, Fixed rules, Percent-encoding); a header value holding a CR, LF or
// NUL, or leading or trailing whitespace, is refused (errors.go,
// RequestError.Inputs), and one holding another control character is not
// asserted (the contract names only those).
func FuzzStyleSerialization(f *testing.F) {
	cfgs := styleConfigs()
	c, err := openapi.Parse(context.Background(), []byte(expand(styleDoc(cfgs), "https://api.example.test")), fuzzURI, nil)
	if err != nil {
		f.Fatalf("Parse: %v", err)
	}
	for _, s := range [][6]string{
		{"blue", "black", "R", "100", "G", "200"},
		{"", "", "", "", "a", ""},
		{".", "..", ".", "..", "..", "."},
		{"a b/c", "d,e;f", "g=h", "i&j", "k l", "m+n"},
		{"%41", "%zz", "%", "%2f", "[x]", "#?"},
		{"é", "日本", "ü", "ß", " ", "\U0001F600"},
		{"\x00", "\r\n", " x", "x ", "\t", "\x7f"},
		{"\xff", "\xfe", "\xff", "a", "\xfe", "b"},
		{"~-._", "!*'()", "$@:", "|^`", "\"<>\\", "{}"},
	} {
		f.Add(s[0], s[1], s[2], s[3], s[4], s[5])
	}
	f.Fuzz(func(t *testing.T, a, b, k1, v1, k2, v2 string) {
		values := []any{a, []string{a, b}}
		if k1 == k2 || asJSON(k1) != asJSON(k2) {
			values = append(values, map[string]string{k1: v1, k2: v2})
		}
		for _, cfg := range cfgs {
			for _, v := range values {
				if cfg.reserved && !reservedNamesSafe(t, v) {
					continue
				}
				want, o := cfg.expect(t, v)
				if o == sent && cfg.in == "path" && cfg.style == "simple" {
					if seg := strings.TrimPrefix(want, "/"+cfg.id+"/"); seg == "." || seg == ".." {
						o = refused
					}
				}
				if o == sent && cfg.in == "header" {
					switch {
					case strings.ContainsAny(want, "\r\n\x00"), want != strings.Trim(want, " \t"):
						o = refused
					case strings.ContainsFunc(want, func(r rune) bool { return r < 0x20 || r == 0x7f }):
						o = unsettled
					}
				}
				req, err := c.Prepare(cfg.id, &openapi.Input{Params: map[string]any{cfg.field(): v}})
				checkPrepared(t, cfg, v, req, err, want, o)
			}
		}
	})
}

// checkPrepared is checkOutcome for a prepared request.
func checkPrepared(t *testing.T, cfg styleCfg, v any, req *openapi.Request, err error, want string, o fate) {
	t.Helper()
	var re *openapi.RequestError
	if err != nil && !errors.As(err, &re) {
		t.Fatalf("%s %#v: Prepare = %v, not a *RequestError", cfg.id, v, err)
	}
	switch o {
	case unsettled:
		return
	case refused:
		if re == nil {
			t.Fatalf("%s %#v: prepared, want a refusal at Inputs[%q]", cfg.id, v, cfg.field())
		}
		if len(re.Inputs) != 1 || re.Inputs[cfg.field()] == nil {
			t.Fatalf("%s %#v: Inputs keys %q, want exactly [%q]", cfg.id, v, sortedKeys(re.Inputs), cfg.field())
		}
		return
	case sentOrRefused:
		if re != nil {
			if len(re.Inputs) != 1 || re.Inputs[cfg.field()] == nil {
				t.Fatalf("%s %#v: Inputs keys %q, want exactly [%q]", cfg.id, v, sortedKeys(re.Inputs), cfg.field())
			}
			return
		}
	}
	if re != nil {
		t.Fatalf("%s %#v: refused: %v; want %q", cfg.id, v, re, want)
	}
	switch cfg.in {
	case "path", "query":
		if got := req.HTTP.URL.RequestURI(); got != want {
			t.Fatalf("%s %#v: request target %q, want %q", cfg.id, v, got, want)
		}
	default:
		name := cfg.field()
		if cfg.in == "cookie" {
			name = "Cookie"
		}
		got := req.HTTP.Header.Values(name)
		if o == omitted {
			if got != nil {
				t.Fatalf("%s %#v: %s = %q, want none", cfg.id, v, name, got)
			}
			return
		}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("%s %#v: %s = %q, want [%q]", cfg.id, v, name, got, want)
		}
	}
}
