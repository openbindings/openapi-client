package openapi_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Swagger 2.0 Parameter and Items Objects, doc.go Swagger arrays and empty
// values. Nested items serialize first; location decides delimiter escaping.
func TestEditionsCollectionFormat(t *testing.T) {
	for _, loc := range []string{"path", "query", "header", "formData"} {
		for _, cf := range []string{"", "csv", "ssv", "tsv", "pipes", "multi"} {
			t.Run(loc+"/"+cf, func(t *testing.T) {
				if cf == "multi" && (loc == "path" || loc == "header") {
					return
				} // separately refused below
				path := "/x"
				if loc == "path" {
					path += "/{p}"
				}
				decl := fmt.Sprintf(`{"name":"p","in":%q,"required":true,"type":"array","items":{"type":"string"}`, loc)
				if cf != "" {
					decl += fmt.Sprintf(`,"collectionFormat":%q`, cf)
				}
				decl += "}"
				c := editionClient(t, editionDoc("2.0", fmt.Sprintf(`%q:{"post":{"consumes":["application/x-www-form-urlencoded"],"parameters":[%s]}}`, path, decl)), nil)
				in := &openapi.Input{Params: map[string]any{"p": []string{"a b", "c/d"}}}
				if loc == "formData" {
					in = &openapi.Input{Body: map[string]any{"p": []string{"a b", "c/d"}}}
				}
				req := mustPrepare(t, c, "POST "+path, in)
				delim := map[string]string{"": ",", "csv": ",", "ssv": " ", "tsv": "\t", "pipes": "|"}[cf]
				var got, want string
				switch loc {
				case "path":
					got = req.HTTP.URL.EscapedPath()
					want = "/x/a%20b" + strings.NewReplacer(" ", "%20", "\t", "%09", "|", "%7C").Replace(delim) + "c%2Fd"
				case "query":
					got = req.HTTP.URL.RawQuery
					want = "p=a%20b" + strings.NewReplacer(" ", "%20", "\t", "%09", "|", "%7C").Replace(delim) + "c%2Fd"
					if cf == "multi" {
						want = "p=a%20b&p=c%2Fd"
					}
				case "header":
					got = req.HTTP.Header.Get("p")
					want = "a b" + delim + "c/d"
				case "formData":
					got = string(editionBody(t, req))
					want = formPairs("p", "a b"+delim+"c/d")
					if cf == "multi" {
						want = formPairs("p", "a b", "p", "c/d")
					}
				}
				if got != want {
					t.Errorf("serialized %q want %q", got, want)
				}
				if loc != "formData" {
					p := param(t, mustOp(t, c, "POST "+path), 0)
					effective := cf
					if effective == "" {
						effective = "csv"
					}
					if p.Style != "" || p.CollectionFormat != effective || p.Schema == nil {
						t.Errorf("descriptor %+v", p)
					}
				}
			})
		}
	}
}

func TestEditionsNestedCollectionAndUndefined(t *testing.T) {
	c := editionClient(t, editionDoc("2.0", `"/x":{"get":{"parameters":[{"name":"p","in":"query","type":"array","collectionFormat":"pipes","items":{"type":"array","collectionFormat":"ssv","items":{"type":"string"}}}]}}`), nil)
	for _, tc := range []struct {
		v    any
		want string
	}{
		{[][]string{{"a b", "c"}, {"d", "e/f"}}, "p=a%20b%20c%7Cd%20e%2Ff"},
		{[]any{nil, []string{"a", "b"}, []any{}, []any{nil}}, "p=a%20b"},
		{[]any{}, ""},
	} {
		req := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"p": tc.v}})
		if got := req.HTTP.URL.RawQuery; got != tc.want {
			t.Errorf("%#v: %q want %q", tc.v, got, tc.want)
		}
	}
}

// doc.go NameOnlyEmpty is limited to allowEmptyValue query/formData in 2.0.
func TestEditionsNameOnlyEmpty(t *testing.T) {
	for _, loc := range []string{"query", "formData"} {
		for _, allow := range []bool{false, true} {
			for _, setting := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%v/%v", loc, allow, setting), func(t *testing.T) {
					c := editionClient(t, editionDoc("2.0", fmt.Sprintf(`"/x":{"post":{"consumes":["application/x-www-form-urlencoded"],"parameters":[{"name":"p","in":%q,"type":"string","allowEmptyValue":%v}]}}`, loc, allow)), &openapi.Options{NameOnlyEmpty: setting})
					in := &openapi.Input{Params: map[string]any{"p": ""}}
					if loc == "formData" {
						in = &openapi.Input{Body: map[string]any{"p": ""}}
					}
					req := mustPrepare(t, c, "POST /x", in)
					got := req.HTTP.URL.RawQuery
					if loc == "formData" {
						got = string(editionBody(t, req))
					}
					want := "p="
					if allow && setting {
						want = "p"
					}
					if got != want {
						t.Errorf("%q want %q", got, want)
					}
				})
			}
		}
	}
}

// describe.go Params plus doc.go Header fields: Swagger Accept survives;
// Content-Type is satisfied by a body, and is ordinary without one.
func TestEditionsHeaderParameters(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			decl := `{"name":"Accept","in":"header","required":true,"type":"string","schema":{"type":"string"}},{"name":"Content-Type","in":"header","required":true,"type":"string","schema":{"type":"string"}}`
			body := `"requestBody":{"content":{"text/plain":{}}}`
			if version == "2.0" {
				decl += `,{"name":"payload","in":"body","schema":{"type":"string"}}`
				body = `"consumes":["text/plain"]`
			}
			c := editionClient(t, editionDoc(version, `"/x":{"post":{"parameters":[`+decl+`],`+body+`},"get":{"parameters":[`+strings.Split(decl, `,{"name":"payload"`)[0]+`]}}`), nil)
			want := 0
			if version == "2.0" {
				want = 2
			}
			if got := len(mustOp(t, c, "POST /x").Params); got != want {
				t.Errorf("params %d want %d", got, want)
			}
			in := &openapi.Input{Body: "hello"}
			if version == "2.0" {
				in.Params = map[string]any{"Accept": "text/plain"}
			}
			req := mustPrepare(t, c, "POST /x", in)
			if got := req.HTTP.Header.Get("Content-Type"); got != "text/plain" {
				t.Errorf("Content-Type %q", got)
			}
			if version == "2.0" {
				if req.HTTP.Header.Get("Accept") != "text/plain" {
					t.Error("Accept absent")
				}
				r := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"Accept": "text/plain", "Content-Type": "custom/type"}})
				if r.HTTP.Header.Get("Content-Type") != "custom/type" {
					t.Error("ordinary Content-Type absent")
				}
			}
		})
	}
}

// OAS 3.2.1 4.12.1-4.12.2 and doc.go Querystring: the name is absent,
// form bytes are not double-encoded and credentials follow after an ampersand.
func TestEditionsQuerystring(t *testing.T) {
	for _, tc := range []struct {
		name, media string
		v           any
		want        string
	}{
		{"form", "application/x-www-form-urlencoded", map[string]any{"a b": "c/d", "n": []string{"x", "y"}}, "a+b=c%2Fd&n=x&n=y"},
		{"json", "application/json", map[string]string{"x": "a b"}, "%7B%22x%22%3A%22a%20b%22%7D"},
		{"text", "text/plain", "a=b&c", "a%3Db%26c"},
		{"empty", "application/x-www-form-urlencoded", map[string]any{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &memRT{}
			c := editionClient(t, editionDoc("3.2.1", fmt.Sprintf(`"/x":{"query":{"parameters":[{"name":"whole","in":"querystring","content":{%q:{}}}],"security":[{"key":[]}]}}`, tc.media), `"components":{"securitySchemes":{"key":{"type":"apiKey","in":"query","name":"token"}}}`), &openapi.Options{Credentials: map[string]openapi.Credential{"key": openapi.Secret("secret")}, HTTPClient: &http.Client{Transport: rt}})
			req := mustPrepare(t, c, "QUERY /x", &openapi.Input{Params: map[string]any{"whole": tc.v}})
			if got := req.HTTP.URL.RawQuery; got != tc.want {
				t.Errorf("query %q want %q", got, tc.want)
			}
			r := sendAndClose(t, req)
			_ = r
			want := tc.want
			if want != "" {
				want += "&"
			}
			want += "token=secret"
			if got := rt.requests()[0].URL.RawQuery; got != want {
				t.Errorf("sent query %q want %q", got, want)
			}
			// Credential placement occurs at Send; the custom transport observes it.
			if got := req.HTTP.URL.RawQuery; got != tc.want {
				t.Errorf("Send mutated prepared URL: %q", got)
			}
		})
	}
}

func TestEditionsQuerystringDefects(t *testing.T) {
	for _, decl := range []string{
		`{"name":"q","in":"querystring","schema":{"type":"string"},"required":true}`,
		`{"name":"q","in":"querystring","style":"form","content":{"text/plain":{}}}`,
		`{"name":"q","in":"querystring","content":{"text/plain":{}}},{"name":"r","in":"query"}`,
		`{"name":"q","in":"querystring","content":{"text/plain":{}}},{"name":"r","in":"querystring","content":{"text/plain":{}}}`,
	} {
		t.Run(decl, func(t *testing.T) {
			c := editionClient(t, editionDoc("3.2.1", `"/x":{"get":{"parameters":[`+decl+`]}}`), nil)
			values := map[string]any{"q": "a"}
			if strings.Contains(decl, `"name":"r"`) {
				values["r"] = "b"
			}
			req, err := c.Prepare("GET /x", &openapi.Input{Params: values})
			if req != nil || err == nil {
				t.Fatal("invalid querystring combination prepared")
			}
			re := asRequestError(t, err)
			if len(values) == 1 {
				if param(t, mustOp(t, c, "GET /x"), 0).Err == nil {
					t.Error("malformed querystring lacks Param.Err")
				}
				wantKeys(t, "Inputs", re.Inputs, false, "q")
			}
		})
	}
	for _, version := range []string{"3.0.4", "3.1.2"} {
		t.Run(version, func(t *testing.T) {
			c := editionClient(t, editionDoc(version, `"/x":{"get":{"parameters":[{"name":"q","in":"querystring","required":true,"content":{"text/plain":{}}}]}}`), nil)
			p := param(t, mustOp(t, c, "GET /x"), 0)
			if p.Err == nil {
				t.Error("earlier edition enabled querystring")
			}
			if _, err := c.Prepare("GET /x", &openapi.Input{Params: map[string]any{"q": "x"}}); err == nil {
				t.Error("earlier edition prepared querystring")
			}
		})
	}
}

// doc.go Percent-encoding/Styles; OAS 3.2.1 Parameter Object and Appendix D.
func TestEditionsCookieAndReserved(t *testing.T) {
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		t.Run(version, func(t *testing.T) {
			c := editionClient(t, editionDoc(version, `"/x/{p}":{"get":{"parameters":[{"name":"p","in":"path","required":true,"allowReserved":true,"schema":{"type":"string"}},{"name":"c","in":"cookie","allowReserved":true,"schema":{"type":"string"}}]}}`), nil)
			req := mustPrepare(t, c, "GET /x/{p}", &openapi.Input{Params: map[string]any{"p": "a:b", "c": "a:b"}})
			want := "a%3Ab"
			if version == "3.2.1" {
				want = "a:b"
			}
			if req.HTTP.URL.EscapedPath() != "/x/"+want || req.HTTP.Header.Get("Cookie") != "c="+want {
				t.Errorf("path %q cookie %q", req.HTTP.URL.EscapedPath(), req.HTTP.Header.Get("Cookie"))
			}
		})
	}
	for _, tc := range []struct {
		v    any
		want string
	}{{"a/b", "c=a/b"}, {[]string{"a", "b"}, "c=a; c=b"}, {map[string]string{"a": "b", "c": "d"}, "a=b; c=d"}} {
		c := editionClient(t, editionDoc("3.2.1", `"/x":{"get":{"parameters":[{"name":"c","in":"cookie","style":"cookie","schema":{}}]}}`), nil)
		if got := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"c": tc.v}}).HTTP.Header.Get("Cookie"); got != tc.want {
			t.Errorf("cookie %q want %q", got, tc.want)
		}
		for _, bad := range []any{"a;b", "a\nb"} {
			_, err := c.Prepare("GET /x", &openapi.Input{Params: map[string]any{"c": bad}})
			wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, "c")
		}
	}
	for _, style := range []string{"cookie", "form"} {
		c := editionClient(t, editionDoc("3.2.1", `"/x":{"get":{"parameters":[{"name":"c","in":"cookie","style":"`+style+`","explode":false,"schema":{}}]}}`), nil)
		for _, v := range []any{[]string{"a", "b"}, map[string]string{"a": "b"}} {
			_, err := c.Prepare("GET /x", &openapi.Input{Params: map[string]any{"c": v}})
			wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, "c")
		}
	}
}
