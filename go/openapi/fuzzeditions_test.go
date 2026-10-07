package openapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// doc.go Swagger arrays: each item is percent-encoded, then joined using the
// location's delimiter. This independent oracle reuses the RFC byte encoder
// and WHATWG form encoder already tested against their standards.
func FuzzEditionCollectionFormat(f *testing.F) {
	for _, seed := range [][2]string{{"a", "b"}, {"", ""}, {"a b", "c/d"}, {"a,b", "c|d"}, {"%41", "%zz"}, {"日本", "é"}, {"\x00", "\r\n"}, {"\xff", "\xfe"}} {
		f.Add(seed[0], seed[1])
	}
	var clients []*openapi.Client
	for _, cf := range []string{"csv", "ssv", "tsv", "pipes", "multi"} {
		doc := editionDoc("2.0", fmt.Sprintf(`"/x":{"get":{"parameters":[{"name":"p","in":"query","type":"array","collectionFormat":%q,"items":{"type":"string"}}]}}`, cf))
		c, err := openapi.Parse(context.Background(), []byte(doc), testDocURI, nil)
		if err != nil {
			f.Fatal(err)
		}
		clients = append(clients, c)
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		a, b = asJSON(a), asJSON(b)
		encoded, err := json.Marshal([]string{a, b})
		if err != nil {
			t.Fatal(err)
		}
		values := []any{[]string{a, b}, namedStrings{a, b}, callerReturnedJSON{data: append(append([]byte(" \n"), encoded...), '\t')}}
		for i, delimiter := range []string{",", "%20", "%09", "%7C", ""} {
			want := "p=" + pctName(a) + delimiter + pctName(b)
			if i == 4 {
				want = "p=" + pctName(a) + "&p=" + pctName(b)
			}
			for _, value := range values {
				req, err := clients[i].Prepare("GET /x", &openapi.Input{Params: map[string]any{"p": value}})
				if err != nil {
					t.Fatalf("format %d, %T: %v", i, value, err)
				}
				if got := req.HTTP.URL.RawQuery; got != want {
					t.Fatalf("format %d, %T: %q want %q", i, value, got, want)
				}
			}
		}
	})
}

// OAS 3.2.1 querystring form serialization uses the existing WHATWG oracle;
// a string codec value for the whole query is escaped exactly once.
func FuzzEditionQuerystring(f *testing.F) {
	for _, seed := range [][2]string{{"a", "b"}, {"", ""}, {"a b", "c/d&x=y"}, {"é", "日本"}, {"%20", "\r\n"}, {"\xff", "\xfe"}} {
		f.Add(seed[0], seed[1])
	}
	c, err := openapi.Parse(context.Background(), []byte(editionDoc("3.2.1", `"/x":{"get":{"parameters":[{"name":"whole","in":"querystring","content":{"application/x-www-form-urlencoded":{}}}]}}`)), testDocURI, nil)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, key, value string) {
		key, value = asJSON(key), asJSON(value)
		req, err := c.Prepare("GET /x", &openapi.Input{Params: map[string]any{"whole": map[string]string{key: value}}})
		if err != nil {
			t.Fatal(err)
		}
		want := formPairs(key, value)
		if got := req.HTTP.URL.RawQuery; got != want {
			t.Fatalf("query %q want %q", got, want)
		}
		if req.HTTP.URL.Fragment != "" {
			t.Fatal("query leaked into fragment")
		}
	})
}
