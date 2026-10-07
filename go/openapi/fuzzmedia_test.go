package openapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openbindings/openapi-client/go/openapi"
)

// The client's media-list splitter and media-type parser mirror RFC 9110's
// media-type grammar: a quoted backslash at the end leaves the last element
// for the parser to refuse, and the client accepts a media type, a
// media-type list or a Content-Type only where mime.ParseMediaType accepts
// each element, with the same type, subtype and parameters.
//
// The client reads media types at four places: an Encoding Object's
// contentType, a list (OAS 3.1.2 section 4.8.15.1: "a comma-separated list
// of specific media types, wildcard media types, or both"); Input.MediaType;
// Part.MediaType; and a response's Content-Type, matched to a declared
// response key (client.go, Response.Media). Wherever it accepts a string,
// each element of it, split at commas outside quoted strings (RFC 9110
// section 5.6.4), must be one mime.ParseMediaType accepts, and the client
// must read it as mime does: a response key written as mime reads the
// element matches a Content-Type of the element as written, and the reverse.
//
// RFC 9110 governs the client's media-type grammar, and mime.ParseMediaType
// is the oracle except on four constructs, where the client follows RFC
// 9110; inputs that use one (mediaQuirks) are left out: an empty parameter
// (RFC 9110 section 5.6.6: "parameters = *( OWS ";" OWS [ parameter ] )"), a
// quoted-pair of any octet (section 5.6.4: "Recipients that process the
// value of a quoted-string MUST handle a quoted-pair as if it were replaced
// by the octet following the backslash"), a "*" in a parameter name (no RFC
// 2231 in media type parameters: section 5.6.6's parameter-name is a token),
// and a parameter given twice (accepted, the boundary excepted: client.go,
// Input.MediaType, "Two boundary parameters are refused").

// mediaElements splits s at commas outside quoted strings, a backslash in a
// quoted string escaping the byte after it (RFC 9110 section 5.6.4), each
// element trimmed of spaces and tabs.
func mediaElements(s string) []string {
	var list []string
	quoted, start := false, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && quoted:
			i++
		case s[i] == '"':
			quoted = !quoted
		case s[i] == ',' && !quoted:
			list, start = append(list, strings.Trim(s[start:i], " \t")), i+1
		}
	}
	return append(list, strings.Trim(s[start:], " \t"))
}

// mediaQuirks names the construct of the media type e, as the client's RFC
// 9110 reading takes its parameters (a type, then OWS ";" OWS [ parameter ]
// repeated), on which RFC 9110 and mime.ParseMediaType disagree and the
// client follows RFC 9110, or returns "": an empty
// parameter before another (RFC 9110 section 5.6.6 allows it; mime refuses
// all but a last one); a quoted-pair of a byte that is not one of mime's
// tspecials (RFC 9110 section 5.6.4: "as if it were replaced by the octet
// following the backslash"; mime keeps the backslash); a "*" in a parameter
// name (RFC 2231, which mime applies and media type parameters do not
// have); and a parameter named twice with different values (RFC 9110
// accepts it, and so does the client but for the boundary; mime refuses it).
func mediaQuirks(e string) string {
	_, s, _ := strings.Cut(e, ";")
	values := map[string]string{}
	for {
		s = strings.TrimLeft(s, " \t")
		switch {
		case s == "":
			return ""
		case s[0] == ';':
			return "an empty parameter before another"
		}
		i := strings.IndexByte(s, '=')
		if i <= 0 {
			return "" // not a parameter: the client refuses it
		}
		name := s[:i]
		if strings.Contains(name, "*") {
			return "a * in a parameter name"
		}
		s = s[i+1:]
		var value strings.Builder
		if s != "" && s[0] == '"' {
			j := 1
			for ; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '\\' && j+1 < len(s) {
					if j++; strings.IndexByte(`()<>@,;:\"/[]?=`, s[j]) < 0 {
						return "a quoted-pair of a byte mime does not unescape"
					}
				}
				value.WriteByte(s[j])
			}
			if j == len(s) {
				return "" // unterminated: the client refuses it
			}
			s = s[j+1:]
		} else {
			j := strings.IndexAny(s, "; \t")
			if j < 0 {
				j = len(s)
			}
			value.WriteString(s[:j])
			s = s[j:]
		}
		key := strings.ToLower(name)
		if v, dup := values[key]; dup && v != value.String() {
			return "a parameter named twice with different values"
		}
		values[key] = value.String()
		if s = strings.TrimLeft(s, " \t"); s != "" {
			if s[0] != ';' {
				return ""
			}
			s = s[1:]
		}
	}
}

// mimeMedia writes the media type mt with params as RFC 9110 writes it,
// every value a quoted-string with \ and " escaped, names sorted.
func mimeMedia(mt string, params map[string]string) string {
	var b strings.Builder
	b.WriteString(mt)
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range params {
			if !yield(k) {
				return
			}
		}
	}) {
		b.WriteString("; " + name + "=" + quoted(params[name]))
	}
	return b.String()
}

// mediaProbe reads strings through the client's four media type inputs.
type mediaProbe struct {
	t     *testing.T
	input *openapi.Client // a request body of */*, whose pre-encoded body takes any concrete Input.MediaType
	part  *openapi.Client // a multipart body whose field f no schema or Encoding describes, so any concrete Part.MediaType
	same  map[string]bool // the elements checked against mime's reading
}

func newMediaProbe(t *testing.T) *mediaProbe {
	t.Helper()
	p := &mediaProbe{t: t, same: map[string]bool{}}
	p.input = parseAt(t, doc31(`"/i":{"post":{"operationId":"in","requestBody":{"content":{"*/*":{}}}}}`), "https://api.example.test", testDocURI, nil)
	p.part = parseAt(t, doc31(`"/p":{"post":{"operationId":"part","requestBody":{"content":{"multipart/form-data":{}}}}}`), "https://api.example.test", testDocURI, nil)
	return p
}

// encodingAccepts reports whether an Encoding Object's contentType s is
// accepted: its Param has no Err.
func (p *mediaProbe) encodingAccepts(s string) bool {
	q, _ := json.Marshal(s)
	c := parseAt(p.t, doc31(`"/e":{"post":{"operationId":"enc","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"f":{}}},"encoding":{"f":{"contentType":`+string(q)+`}}}}}}}`),
		"https://api.example.test", testDocURI, nil)
	e := encodingByName(p.t, reqMedia(p.t, mustOp(p.t, c, "enc"), 0))["f"]
	return e != nil && e.Err == nil
}

// inputAccepts reports whether Input.MediaType s is accepted for a
// pre-encoded body.
func (p *mediaProbe) inputAccepts(s string) bool {
	_, err := p.input.Prepare("in", &openapi.Input{Body: []byte("x"), MediaType: s})
	return err == nil
}

// partAccepts reports whether Part.MediaType s is accepted for raw content.
func (p *mediaProbe) partAccepts(s string) bool {
	_, err := p.part.Prepare("part", &openapi.Input{MediaType: "multipart/form-data; boundary=B",
		Body: map[string]any{"f": openapi.Part{Content: []byte("x"), MediaType: s}}})
	return err == nil
}

// responseMatches reports whether a response whose Content-Type is header
// matches the response key key (client.go, Response.Media).
func (p *mediaProbe) responseMatches(key, header string) bool {
	q, _ := json.Marshal(key)
	hc, rt := memClient()
	rt.answer = func(r *http.Request) (*http.Response, error) {
		resp := memResponse(r, 200, nil, "")
		resp.Header["Content-Type"] = []string{header}
		return resp, nil
	}
	c := parseAt(p.t, doc31(`"/r":{"get":{"operationId":"get","responses":{"200":{"description":"ok","content":{`+string(q)+`:{}}}}}}`),
		"https://api.example.test", testDocURI, &openapi.Options{HTTPClient: hc})
	resp, err := c.Call(context.Background(), "get", nil, nil)
	if err != nil {
		p.t.Fatalf("Call with Content-Type %q: %v", header, err)
	}
	return resp.Media != nil
}

// responseAccepts reports whether a response Content-Type s is accepted:
// it matches */*, which every media type does.
func (p *mediaProbe) responseAccepts(s string) bool { return p.responseMatches("*/*", s) }

// agree checks that mime.ParseMediaType accepts the element e of s, which
// the client accepted where, and that the client reads e as mime does.
func (p *mediaProbe) agree(where, s, e string) {
	t := p.t
	t.Helper()
	mt, params, err := mime.ParseMediaType(e)
	if err != nil {
		t.Fatalf("%s accepts %q, whose element %q mime.ParseMediaType refuses: %v", where, s, e, err)
	}
	if _, ok := params["boundary"]; ok || p.same[e] {
		return // a declared key's boundary has rules of its own (Input.MediaType)
	}
	p.same[e] = true
	k := mimeMedia(mt, params)
	if !p.responseMatches(k, e) {
		t.Errorf("%s accepts %q, but a response key %q, as mime.ParseMediaType reads it, does not match it as a Content-Type", where, e, k)
	}
	if !p.responseMatches(e, k) {
		t.Errorf("%s accepts %q, but as a response key it does not match a Content-Type %q, as mime.ParseMediaType reads it", where, e, k)
	}
}

// check reads s through every input, checking each acceptance against mime.
func (p *mediaProbe) check(s string) {
	elems := mediaElements(s)
	if len(elems) > 8 {
		return
	}
	for _, e := range elems {
		if mediaQuirks(e) != "" {
			return
		}
	}
	if p.encodingAccepts(s) {
		for _, e := range elems {
			p.agree("an Encoding contentType", s, e)
		}
	}
	for _, in := range []struct {
		where   string
		accepts func(string) bool
	}{{"Input.MediaType", p.inputAccepts}, {"Part.MediaType", p.partAccepts}, {"a response Content-Type", p.responseAccepts}} {
		// An empty Input.MediaType or Part.MediaType is no media type but
		// the default (client.go: "Empty means ...", "An empty field means
		// its default").
		if s != "" && in.accepts(s) {
			p.agree(in.where, s, s)
		}
	}
}

// FuzzMediaTypes checks the client's media-type reading against
// mime.ParseMediaType. The seeds are media types the suite uses, edge cases
// of quoting, parameters and lists, and the constructs mediaQuirks leaves
// out; every seed runs as a test.
func FuzzMediaTypes(f *testing.F) {
	for _, s := range []string{
		"text/plain", "application/json; charset=utf-8", "TEXT/Plain; Charset=UTF-8", "image/*", "*/*",
		`text/plain; profile="a,b", application/json`, "image/png, image/jpeg", "text/plain ;a=b", " text/plain ",
		"text/plain;a=b;", `text/plain; a=""`, `text/plain; a="\""`, `text/plain; a="\\"`, `application/vnd.x+json;v="1"`,
		"multipart/form-data; boundary=xyz", "text/plain,", ",", "", "text/plain; a=b c", "text/plain; charset", "text/plain; =x",
		`text/plain; a="x\`, `text/plain, application/json; a="\`, `text/plain; a="x`, `text/plain; a="\\\`,
		"text/plain;;a=b", `text/plain; a="x\y"`, "text/plain; a*=utf-8''%41", "text/plain; a=1; A=2",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 256 || !utf8.ValidString(s) {
			return
		}
		newMediaProbe(t).check(s)
	})
}

// A quoted backslash at the end leaves the last element for the parser to
// refuse: an Encoding contentType whose last element ends in a quoted
// string's backslash is refused as a document defect (Param.Err), a call
// with the field is refused at its key, and Input.MediaType, Part.MediaType
// and a response Content-Type ending so are refused too. A list splitter
// that dropped the last element would accept the contentType with its other
// types and no Err.
func TestMediaTypeTrailingQuotedBackslash(t *testing.T) {
	p := newMediaProbe(t)
	for _, s := range []string{`text/plain; a="x\`, `text/plain, application/json; a="\`, `text/plain; a="\\\`, `image/png; q="\`} {
		t.Run(s, func(t *testing.T) {
			p.t = t
			if p.encodingAccepts(s) {
				t.Errorf("an Encoding contentType %q is accepted", s)
			}
			q, _ := json.Marshal(s)
			w := newWire(t, nil)
			c := parseFor(t, w, doc31(`"/e":{"post":{"operationId":"enc","requestBody":{"content":{"multipart/form-data":{
				"schema":{"type":"object","properties":{"f":{}}},"encoding":{"f":{"contentType":`+string(q)+`}}}}}}}`), nil)
			resp, err := c.Call(t.Context(), "enc", &openapi.Input{Body: map[string]any{"f": "v"}}, nil)
			re := refusedBeforeSending(t, w, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, "Input.Body/f")
			for name, accepts := range map[string]func(string) bool{
				"Input.MediaType": p.inputAccepts, "Part.MediaType": p.partAccepts, "a response Content-Type": p.responseAccepts,
			} {
				if accepts(s) {
					t.Errorf("%s %q is accepted", name, s)
				}
			}
		})
	}
}

// The reference splitter and quirk finder, on the cases they decide; and
// each quirk is one where mime.ParseMediaType refuses the type or reads its
// parameters otherwise than RFC 9110 does.
func TestMediaFuzzReferences(t *testing.T) {
	for s, want := range map[string][]string{
		`a/b; p="x,y", c/d`:   {`a/b; p="x,y"`, "c/d"},
		`a/b; p="x\",y", c/d`: {`a/b; p="x\",y"`, "c/d"},
		`a/b, c/d; q="\`:      {"a/b", `c/d; q="\`},
		` a/b ,`:              {"a/b", ""},
	} {
		if got := mediaElements(s); !slices.Equal(got, want) {
			t.Errorf("mediaElements(%q) = %q, want %q", s, got, want)
		}
	}
	for _, s := range []string{"text/plain", "text/plain;", "text/plain;a=b;", "text/plain; a=b ;", `text/plain; a="\""`, "text/plain; a=1; a=1"} {
		if q := mediaQuirks(s); q != "" {
			t.Errorf("mediaQuirks(%q) = %q, want none", s, q)
		}
	}
	for _, tt := range []struct {
		s, rfc string // the type, and its parameters as RFC 9110 reads them
	}{
		{"text/plain;;a=b", "map[a:b]"},
		{"text/plain; ;", "map[]"},
		{`text/plain; a="\y"`, "map[a:y]"},
		{"text/plain; a*=x", "map[a*:x]"},
		{"text/plain; a=1; A=2", "map[a:1 A:2]"},
	} {
		if mediaQuirks(tt.s) == "" {
			t.Errorf("mediaQuirks(%q) finds none", tt.s)
		}
		if _, params, err := mime.ParseMediaType(tt.s); err == nil && fmt.Sprint(params) == tt.rfc {
			t.Errorf("mime.ParseMediaType(%q) = %v, as RFC 9110 reads it", tt.s, params)
		}
	}
}
