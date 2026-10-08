package openapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// OAS 3.2.1 Components, Parameter, Request Body and Response Objects admit
// Reference Objects in content entries. describe.go Media retains the target
// Source and resolved schema/Encoding; unrelated Reference Object siblings do
// not replace them. Media has no Description field, and the Media Type Object
// defines none: a description sibling stays authored data, never a parent
// Message or Param description (OAS 3.2.1 Reference Object).
func TestEditionsMediaReferences(t *testing.T) {
	const mediaURI = "https://api.example.test/media.json"
	const schemaURI = "https://api.example.test/schema.json"
	mediaDoc := `{"openapi":"3.2.1","components":{"mediaTypes":{"Form":{"schema":{"$ref":"schema.json#/Form"},"encoding":{"p":{"style":"form","explode":false}}},"JSON":{"schema":{"type":"object"}}}}}`
	var mu sync.Mutex
	hits := map[string]int{}
	l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
		mu.Lock()
		hits[u]++
		mu.Unlock()
		switch u {
		case mediaURI:
			return io.NopCloser(strings.NewReader(mediaDoc)), "", nil
		case schemaURI:
			return io.NopCloser(strings.NewReader(`{"Form":{"type":"object","properties":{"p":{"type":"array","items":{"type":"string"}}}}}`)), "", nil
		}
		return nil, "", fmt.Errorf("unexpected fetch %s", u)
	}}
	doc := editionDoc("3.2.1", `"/x":{"post":{"parameters":[{"name":"q","in":"query","description":"parameter","content":{"application/json":{"$ref":"media.json#/components/mediaTypes/JSON","description":"not parent"}}}],"requestBody":{"description":"body","content":{"application/x-www-form-urlencoded":{"$ref":"#/components/mediaTypes/Alias","description":"use","schema":{"type":"string"},"encoding":{"p":{"style":"invalid"}}}}},"responses":{"200":{"description":"response","headers":{"X-Value":{"description":"header","content":{"application/json":{"$ref":"media.json#/components/mediaTypes/JSON","description":"not header"}}}},"content":{"application/json":{"$ref":"media.json#/components/mediaTypes/JSON","description":"not response"}}}}}}`, `"components":{"mediaTypes":{"Alias":{"$ref":"media.json#/components/mediaTypes/Form","description":"intermediate"}}}`)
	c, err := l.Parse(t.Context(), []byte(doc), testDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	op := mustOp(t, c, "POST /x")
	m := reqMedia(t, op, 0)
	if m.Err != nil || m.Source != mediaURI+"#/components/mediaTypes/Form" || m.Schema == nil || len(m.Encoding) != 1 || m.Encoding[0].Name != "p" || m.Encoding[0].Err != nil {
		t.Fatalf("request Media %+v", m)
	}
	if m.Schema.Source() != mediaURI+"#/components/mediaTypes/Form/schema" {
		t.Errorf("schema Source %s", m.Schema.Source())
	}
	if got := responseMedia(t, op, 0, 0); got.Err != nil || got.Source != mediaURI+"#/components/mediaTypes/JSON" || got.Schema == nil {
		t.Errorf("response Media %+v", got)
	}
	if op.Body.Description != "body" || op.Responses[0].Description != "response" || op.Params[0].Description != "parameter" || op.Responses[0].Headers[0].Description != "header" {
		t.Error("media reference description escaped into its parent")
	}
	for _, p := range []*openapi.Param{op.Params[0], op.Responses[0].Headers[0]} {
		if p.Schema == nil || p.Schema.Source() != mediaURI+"#/components/mediaTypes/JSON/schema" || p.Err != nil {
			t.Errorf("content Param %+v", p)
		}
	}
	req := mustPrepare(t, c, op.Key, &openapi.Input{Params: map[string]any{"q": map[string]int{"n": 1}}, Body: map[string]any{"p": []string{"a b", "c"}}})
	if got := string(editionBody(t, req)); got != "p=a%20b,c" {
		t.Errorf("body %q", got)
	}
	if got := req.HTTP.URL.RawQuery; got != "q=%7B%22n%22%3A1%7D" {
		t.Errorf("query %q", got)
	}
	var authored map[string]json.RawMessage
	if err := json.Unmarshal(c.Document(testDocURI+"#/paths/~1x/post/requestBody/content/application~1x-www-form-urlencoded"), &authored); err != nil {
		t.Fatal(err)
	}
	if string(authored["description"]) != `"use"` || string(authored["$ref"]) != `"#/components/mediaTypes/Alias"` || authored["schema"] == nil {
		t.Errorf("authored reference %v", authored)
	}
	if !slices.Equal(c.DocumentURIs(), []string{testDocURI, mediaURI, schemaURI}) {
		t.Errorf("documents %q", c.DocumentURIs())
	}
	if len(hits) != 2 || hits[mediaURI] != 1 || hits[schemaURI] != 1 {
		t.Errorf("fetches %v", hits)
	}
}

// Before 3.2, a content map's values are Media Type Objects, never Reference
// Objects, so one with a $ref member marks a document meant to be bundled
// (describe.go, Operation: "It makes the nearest part holding that value
// unusable: the Err of that ... Media ... says the document must be bundled
// first, and does not wrap ErrUnresolved"). Such a $ref is never retrieved,
// and neither is one in a components.mediaTypes entry, a field OpenAPI 3.0
// and 3.1 do not define (load.go, Loader: the references followed are those
// in Reference Objects, Path Items and Schema Objects).
//
// The Media still names its media type, so the type can govern a call: a
// structured body is checked against Media.Err and refused at the body
// (client.go, Input.Body: a pre-encoded body is not checked "against ... a
// Media.Err other than an invalid key's", so a structured one is; errors.go,
// RequestError.Inputs holds "a body refused by the request body's Message.Err
// or by its governing Media's Err for any reason but an invalid key", keyed
// "for the body, "Input.Body""), while a pre-encoded body is sent under it,
// with Request.Media that Media (client.go, Request.Media).
func TestEditionsMediaReferencesEarlierEditions(t *testing.T) {
	for _, version := range []string{"3.0.4", "3.1.2"} {
		t.Run(version, func(t *testing.T) {
			var mu sync.Mutex
			hits := 0
			l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
				mu.Lock()
				hits++
				mu.Unlock()
				return nil, "", fmt.Errorf("unexpected fetch %s", u)
			}}
			doc := editionDoc(version, `"/x":{"post":{"requestBody":{"content":{"multipart/form-data":{"$ref":"not-a-reference.json#/M","schema":{"type":"object","properties":{"p":{"type":"string"}}}}}}}}`, `"components":{"mediaTypes":{"Unused":{"$ref":"also-not-a-reference.json#/M"}}}`)
			rt := &memRT{}
			c, err := l.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: rt}, BaseURL: "https://api.example.test"})
			if err != nil {
				t.Fatal(err)
			}
			op := mustOp(t, c, "POST /x")
			if op.Err != nil || op.Body.Err != nil {
				t.Errorf("Operation.Err %v, Body.Err %v; want the defect on the Media alone", op.Err, op.Body.Err)
			}
			m := reqMedia(t, op, 0)
			if !mentionsBundling(m.Err) || errors.Is(m.Err, openapi.ErrUnresolved) {
				t.Errorf("Media.Err = %v; want it to say the document must be bundled first, without ErrUnresolved", m.Err)
			}
			if m.Type != "multipart/form-data" || m.Source != testDocURI+"#/paths/~1x/post/requestBody/content/multipart~1form-data" {
				t.Errorf("Media Type %q Source %q", m.Type, m.Source)
			}

			_, err = c.Call(t.Context(), op.Key, &openapi.Input{Body: map[string]string{"p": "value"}}, nil)
			re := asRequestError(t, err)
			wantKeys(t, "Inputs", re.Inputs, true, "Input.Body")
			if !errors.Is(re.Inputs["Input.Body"], m.Err) {
				t.Errorf("Inputs[\"Input.Body\"] = %v; want it to wrap Media.Err", re.Inputs["Input.Body"])
			}
			if len(re.Settings) != 0 {
				t.Errorf("Settings %q; the declared media type is usable", sortedKeys(re.Settings))
			}
			if n := rt.count(); n != 0 {
				t.Fatalf("a refused call sent %d requests", n)
			}

			raw := "--b\r\nContent-Disposition: form-data; name=\"p\"\r\n\r\nvalue\r\n--b--\r\n"
			req := mustPrepare(t, c, op.Key, &openapi.Input{Body: []byte(raw), MediaType: "multipart/form-data; boundary=b"})
			if req.Media != m {
				t.Errorf("Request.Media %p; want the operation's Media %p", req.Media, m)
			}
			sendAndClose(t, req)
			if reqs := rt.requests(); len(reqs) != 1 || string(reqs[0].Body) != raw {
				t.Errorf("pre-encoded body: requests %+v", reqs)
			}
			if hits != 0 {
				t.Errorf("earlier edition fetched %d references", hits)
			}
		})
	}
}

// describe.go Media.Err localizes unreadable media references, preserving
// ErrUnresolved and retrieval causes. Input.Body expressly permits a raw body
// to bypass Media.Err when its declared media key is already known.
func TestEditionsUnreadableMediaReferenceRawBypass(t *testing.T) {
	boom := errors.New("media document unavailable")
	l := openapi.Loader{Fetch: func(context.Context, string) (io.ReadCloser, string, error) { return nil, "", boom }}
	rt := &memRT{}
	doc := editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"required":true,"content":{"application/json":{"$ref":"missing.json#/M"}}}}}`)
	c, err := l.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatal(err)
	}
	op := mustOp(t, c, "POST /x")
	m := reqMedia(t, op, 0)
	if op.Err != nil || op.Body.Err != nil || !errors.Is(m.Err, openapi.ErrUnresolved) || !errors.Is(m.Err, boom) {
		t.Fatalf("operation %v body %v media %v", op.Err, op.Body.Err, m.Err)
	}
	if m.Source != testDocURI+"#/paths/~1x/post/requestBody/content/application~1json" {
		t.Errorf("unresolved Source %s", m.Source)
	}
	_, err = c.Prepare(op.Key, &openapi.Input{Body: map[string]int{"n": 1}})
	re := asRequestError(t, err)
	wantKeys(t, "Inputs", re.Inputs, false, "Input.Body")
	for _, body := range []any{[]byte(`{"raw":true}`), strings.NewReader(`{"raw":true}`)} {
		req := mustPrepare(t, c, op.Key, &openapi.Input{Body: body})
		if req.Media != m {
			t.Error("raw request lost governing Media")
		}
		sendAndClose(t, req)
	}
	for _, r := range rt.requests() {
		if string(r.Body) != `{"raw":true}` || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("raw request %+v", r)
		}
	}
}

// OAS 3.2.1 Encoding Object adds recursively named and positional encodings.
// External Header and Schema references below each nested field are real
// document edges. Example data below the Header remains opaque. Input.Body
// requires one nested multipart level, exercised here through all three forms.
func TestEditionsNestedEncodingReferenceDiscovery(t *testing.T) {
	for _, kind := range []string{"encoding", "prefixEncoding", "itemEncoding"} {
		t.Run(kind, func(t *testing.T) {
			child := `{"contentType":"text/plain","headers":{"X-Part":{"$ref":"headers.json#/H"}}}`
			nested := `"encoding":{"p":` + child + `}`
			inner := any(map[string]any{"p": openapi.Part{Content: "nested", Header: http.Header{"X-Part": {"present"}}}})
			if kind == "prefixEncoding" {
				nested = `"prefixEncoding":[` + child + `]`
			}
			if kind == "itemEncoding" {
				nested = `"itemEncoding":` + child
			}
			if kind != "encoding" {
				inner = []any{openapi.Part{Content: "nested", Header: http.Header{"X-Part": {"present"}}}}
			}
			doc := editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"bundle":{"type":"object"}}},"encoding":{"bundle":{"contentType":"multipart/mixed",`+nested+`}}}}}}}`)
			var mu sync.Mutex
			hits := map[string]int{}
			l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
				mu.Lock()
				hits[u]++
				mu.Unlock()
				switch u {
				case "https://api.example.test/headers.json":
					return io.NopCloser(strings.NewReader(`{"H":{"description":"part header","schema":{"$ref":"schemas.json#/S"},"example":{"$ref":"never.json"}}}`)), "", nil
				case "https://api.example.test/schemas.json":
					return io.NopCloser(strings.NewReader(`{"S":{"type":"string"}}`)), "", nil
				}
				return nil, "", fmt.Errorf("unexpected fetch %s", u)
			}}
			c, err := l.Parse(t.Context(), []byte(doc), testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(c.DocumentURIs(), []string{testDocURI, "https://api.example.test/headers.json", "https://api.example.test/schemas.json"}) {
				t.Errorf("documents %q", c.DocumentURIs())
			}
			if len(hits) != 2 || hits["https://api.example.test/headers.json"] != 1 || hits["https://api.example.test/schemas.json"] != 1 {
				t.Errorf("fetches %v", hits)
			}
			req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]any{"bundle": openapi.Part{Content: inner}}})
			_, _, outer := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
			if len(outer) != 1 {
				t.Fatalf("outer parts %d", len(outer))
			}
			_, _, parts := readMultipart(t, outer[0].header.Get("Content-Type"), outer[0].body)
			if len(parts) != 1 || string(parts[0].body) != "nested" || parts[0].header.Get("X-Part") != "present" {
				t.Errorf("inner parts %#v", parts)
			}
		})
	}
}
