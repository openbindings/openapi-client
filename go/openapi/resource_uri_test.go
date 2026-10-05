package openapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"
)

func TestResourceURIStoredPaths(t *testing.T) {
	for _, path := range []string{
		strings.Repeat("directory/", 64) + "schema.json",
		strings.Repeat("directory%2fsegment/", 32) + "schema.json?revision=1",
	} {
		t.Run(path, func(t *testing.T) {
			raw := fmt.Sprintf(`{"openapi":"3.1.2","paths":{},"components":{"schemas":{"S":{"$id":%q,"type":"string"}}}}`, path)
			c, err := Parse(t.Context(), []byte(raw), "https://example.test/api.json", nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.doc.ids) != 1 {
				t.Fatal("expected one resource identity")
			}
			for key, claim := range c.doc.ids {
				u := claim.v.t.resourceBases[claim.v.i]
				if u == nil || u.String() != key {
					t.Fatal("resource identity changed")
				}
				s := u.Path
				if u.RawPath != "" {
					s = u.RawPath
				}
				p := uintptr(unsafe.Pointer(unsafe.StringData(s)))
				start := uintptr(unsafe.Pointer(unsafe.StringData(key)))
				if p < start || p+uintptr(len(s)) > start+uintptr(len(key)) {
					t.Fatal("loaded resource retained a separate path allocation")
				}
			}
		})
	}
}

func TestResourceURIStorage(t *testing.T) {
	for _, raw := range []string{
		"https://example.test/" + strings.Repeat("directory/", 64) + "schema.json",
		"https://example.test/a%2fb/schema.json?version=1",
		"https://example.test/%E2%98%83/schema.json",
		"urn:example:" + strings.Repeat("component:", 64) + "schema",
		"file:///directory/schema.json",
		"https://example.test/?query=/directory/schema.json",
		"https://user:password@example.test/path?query=1",
	} {
		t.Run(raw, func(t *testing.T) {
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			before := *u
			uri := u.String()
			shareResourceURI(u, uri)
			if !reflect.DeepEqual(*u, before) || u.String() != uri {
				t.Fatal("URL value changed")
			}
			for _, s := range []string{u.Path, u.RawPath} {
				if s == "" || !strings.Contains(uri, s) {
					continue
				}
				p := uintptr(unsafe.Pointer(unsafe.StringData(s)))
				start := uintptr(unsafe.Pointer(unsafe.StringData(uri)))
				if p < start || p+uintptr(len(s)) > start+uintptr(len(uri)) {
					t.Fatal("resource text retained separately from identifier")
				}
			}
		})
	}
}

// Sharing a retained identifier's bytes must be invisible to net/url, including
// optional RawPath encodings, opaque bases, queries, userinfo and empty paths.
func FuzzResourceURIStorage(f *testing.F) {
	for _, pair := range [][2]string{
		{"https://example.test/a/b.json", "../c.json"},
		{"https://example.test/a%2fb/base.json", "%2e%2e/next%2fitem.json"},
		{"https://example.test/a/base.json?version=1", "?version=2#anchor"},
		{"urn:example:base?version=1", "#anchor"},
		{"file:///a/base.json", "../b.json"},
		{"https://example.test/", "https://user:password@other.test/%E2%98%83"},
		{"https://example.test/root.json", strings.Repeat("directory/", 64) + "schema.json"},
		{"https://example.test/root.json", strings.Repeat("directory%2fsegment/", 32) + "schema.json"},
	} {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, base, ref string) {
		if len(base)+len(ref) > 8192 || !utf8.ValidString(base) || !utf8.ValidString(ref) {
			t.Skip()
		}
		b, err := url.Parse(base)
		if err != nil {
			return
		}
		u, err := b.Parse(ref)
		if err != nil {
			return
		}
		invalidID := u.Fragment != "" // JSON Schema 2020-12 section 8.2.1
		u.Fragment, u.RawFragment = "", ""
		before := *u
		uri := u.String()
		shareResourceURI(u, uri)
		if !reflect.DeepEqual(*u, before) || u.String() != uri {
			t.Fatal("URI storage changed URL semantics")
		}
		if !b.IsAbs() || b.User != nil || strings.Contains(base, "#") || uri == base {
			return
		}
		// Exercise discovery, storage compaction and subsequent graph access,
		// with net/url supplying the independently resolved resource base.
		schema, err := json.Marshal(map[string]string{"$id": ref, "type": "string"})
		if err != nil {
			t.Fatal(err)
		}
		raw := append([]byte(`{"openapi":"3.1.2","paths":{},"components":{"schemas":{"S":`), schema...)
		raw = append(raw, []byte(`}}}`)...)
		c, err := Parse(t.Context(), raw, base, nil)
		if err != nil { // the Loader can refuse a URI net/url accepts
			return
		}
		s, err := c.Schema(base + "#/components/schemas/S")
		if err != nil {
			t.Fatal(err)
		}
		wantBase := uri
		if invalidID {
			wantBase = b.String()
		}
		if s.Base() != wantBase || !bytes.Equal(s.Raw(), schema) {
			t.Fatalf("loaded resource differs: base %q, want %q; raw %s", s.Base(), wantBase, s.Raw())
		}
	})
}
