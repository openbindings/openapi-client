package openapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

func editionPost(version, media, schema string) string {
	if version == "2.0" {
		return editionDoc(version, `"/x":{"post":{"consumes":["`+media+`"],"produces":["application/json"],"parameters":[{"name":"payload","in":"body","required":true,"schema":`+schema+`}],"responses":{"200":{"description":"ok","schema":{"type":"object"}}}}}`)
	}
	return editionDoc(version, `"/x":{"post":{"requestBody":{"required":true,"content":{"`+media+`":{"schema":`+schema+`}}},"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}}}`)
}

// describe.go Message/Media and Input.Body: Swagger body normalization and
// equivalent ordinary calls, response decoding, raw sends and replay in all
// editions, 3.1 included.
func TestEditionsJSONFlow(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			rt := &memRT{answer: func(r *http.Request) (*http.Response, error) {
				return memResponse(r, 200, http.Header{"Content-Type": {"application/json"}}, `{"ok":true}`), nil
			}}
			c := editionClient(t, editionPost(version, "application/json", `{"type":"object"}`), &openapi.Options{HTTPClient: &http.Client{Transport: rt}})
			op := mustOp(t, c, "POST /x")
			if op.Body == nil || !op.Body.Required || len(op.Params) != 0 || reqMedia(t, op, 0).Type != "application/json" || responseMedia(t, op, 0, 0).Type != "application/json" {
				t.Fatalf("normalized operation %+v", op)
			}
			in := &openapi.Input{Body: map[string]int{"n": 1}}
			req := mustPrepare(t, c, op.Key, in)
			got := editionBody(t, req)
			if !bytes.Equal(trimNL(got), []byte(`{"n":1}`)) {
				t.Errorf("body %s", got)
			}
			if req.HTTP.GetBody == nil {
				t.Fatal("missing replay")
			}
			r, err := req.HTTP.GetBody()
			if err != nil {
				t.Fatal(err)
			}
			again, err := io.ReadAll(r)
			r.Close()
			if err != nil || !bytes.Equal(got, again) {
				t.Errorf("replay %s %v", again, err)
			}
			var out struct {
				OK bool `json:"ok"`
			}
			mustCall(t, c, op.Key, in, &out)
			if !out.OK {
				t.Error("response not decoded")
			}
			raw := mustPrepare(t, c, op.Key, &openapi.Input{Body: []byte("raw")})
			sendAndClose(t, raw)
			if got := string(rt.requests()[1].Body); got != "raw" {
				t.Errorf("raw %q", got)
			}
		})
	}
}

// Swagger consumes/produces inheritance and overriding lists (2.0 Operation
// Object), plus describe.go Message empty-Type normalization.
func TestEditionsSwaggerMedia(t *testing.T) {
	c := editionClient(t, editionDoc("2.0", `"/x":{"post":{"parameters":[{"name":"body","in":"body","description":"payload","schema":{"type":"object"}}],"responses":{"200":{"description":"ok","schema":{"type":"object"}},"204":{"description":"empty"}}},"put":{"consumes":["text/plain"],"produces":[],"parameters":[{"name":"body","in":"body","schema":{"type":"string"}}],"responses":{"200":{"description":"ok","schema":{"type":"string"}}}}}`, `"consumes":["application/json","application/xml"],"produces":["application/json","text/plain"]`), nil)
	post := mustOp(t, c, "POST /x")
	if len(post.Body.Media) != 2 || post.Body.Description != "payload" || post.Body.Source != testDocURI+"#/paths/~1x/post/parameters/0" {
		t.Errorf("body %+v", post.Body)
	}
	if len(response(t, post, 0).Media) != 2 || len(response(t, post, 1).Media) != 0 {
		t.Error("response media normalization")
	}
	put := mustOp(t, c, "PUT /x")
	if reqMedia(t, put, 0).Type != "text/plain" || responseMedia(t, put, 0, 0).Type != "" {
		t.Error("operation media override")
	}
	if _, err := c.Prepare(post.Key, &openapi.Input{Body: map[string]int{"n": 1}}); err == nil {
		t.Error("ambiguous consumes selected itself")
	}
	c = editionClient(t, editionDoc("2.0", `"/x":{"post":{"parameters":[{"name":"body","in":"body","schema":{"type":"object"}}]}}`), nil)
	if reqMedia(t, mustOp(t, c, "POST /x"), 0).Type != "" {
		t.Error("missing consumes needs empty Type")
	}
	if _, err := c.Prepare("POST /x", &openapi.Input{Body: map[string]int{"n": 1}}); err == nil {
		t.Error("missing concrete media accepted")
	}
	if got := editionBody(t, mustPrepare(t, c, "POST /x", &openapi.Input{MediaType: "application/json", Body: map[string]int{"n": 1}})); !bytes.Equal(trimNL(got), []byte(`{"n":1}`)) {
		t.Errorf("untyped body %s", got)
	}
}

// Swagger formData is a synthetic object schema (describe.go Media.Schema),
// with every field in Encoding, never Params. Required fields are checked
// only for structured bodies (client.go Input.Body).
func TestEditionsSwaggerFormNormalization(t *testing.T) {
	for _, media := range []string{"application/x-www-form-urlencoded", "multipart/form-data", ""} {
		t.Run(media, func(t *testing.T) {
			consumes := ""
			if media != "" {
				consumes = `"consumes":["` + media + `"],`
			}
			c := editionClient(t, editionDoc("2.0", `"/x":{"post":{`+consumes+`"parameters":[{"name":"q","in":"query","type":"string"},{"name":"name","in":"formData","type":"string","required":true,"minLength":1},{"name":"tags","in":"formData","type":"array","collectionFormat":"multi","items":{"type":"string"}}]}}`), nil)
			op := mustOp(t, c, "POST /x")
			m := reqMedia(t, op, 0)
			if len(op.Params) != 1 || len(m.Encoding) != 2 || m.Type != media || m.Schema == nil {
				t.Fatalf("normalization: %+v %+v", op, m)
			}
			var schema struct {
				Type       string
				Properties map[string]json.RawMessage
				Required   []string
			}
			if err := json.Unmarshal(m.Schema.Raw(), &schema); err != nil || schema.Type != "object" || len(schema.Properties) != 2 || len(schema.Required) != 1 || schema.Required[0] != "name" {
				t.Errorf("synthetic schema %s: %v", m.Schema.Raw(), err)
			}
			chosen := media
			if chosen == "" {
				chosen = "application/x-www-form-urlencoded"
			}
			_, err := c.Prepare(op.Key, &openapi.Input{MediaType: chosen, Body: map[string]any{"tags": []string{"a"}}})
			wantKeys(t, "Inputs", asRequestError(t, err).Inputs, false, "Input.Body/name")
			req := mustPrepare(t, c, op.Key, &openapi.Input{MediaType: chosen, Body: map[string]any{"name": "a b", "tags": []string{"x", "y"}}})
			body := editionBody(t, req)
			if chosen == "application/x-www-form-urlencoded" {
				if string(body) != "name=a+b&tags=x&tags=y" {
					t.Errorf("form %q", body)
				}
			} else {
				_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), body)
				if len(parts) != 3 || string(parts[0].body) != "a b" || string(parts[1].body) != "x" || string(parts[2].body) != "y" {
					t.Errorf("parts %#v", parts)
				}
			}
			rawType := chosen
			if chosen == "multipart/form-data" {
				rawType += "; boundary=raw"
			}
			mustPrepare(t, c, op.Key, &openapi.Input{MediaType: rawType, Body: []byte("opaque")})
		})
	}
}

// Swagger file formData defaults to octets, and may only be structurally
// encoded as multipart. Caller-encoded bytes bypass field schema limitations.
func TestEditionsSwaggerFile(t *testing.T) {
	c := editionClient(t, editionDoc("2.0", `"/x":{"post":{"consumes":["multipart/form-data","application/x-www-form-urlencoded"],"parameters":[{"name":"file","in":"formData","type":"file","required":true}]}}`), nil)
	req := mustPrepare(t, c, "POST /x", &openapi.Input{MediaType: "multipart/form-data", Body: map[string]any{"file": []byte("abc")}})
	_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
	if len(parts) != 1 || parts[0].header.Get("Content-Type") != "application/octet-stream" || parts[0].header.Get("Content-Disposition") != formData("file", "file") {
		t.Errorf("file parts %#v", parts)
	}
	if _, err := c.Prepare("POST /x", &openapi.Input{MediaType: "application/x-www-form-urlencoded", Body: map[string]any{"file": []byte("abc")}}); err == nil {
		t.Error("structured file form accepted")
	}
	mustPrepare(t, c, "POST /x", &openapi.Input{MediaType: "application/x-www-form-urlencoded", Body: []byte("file=abc")})
}

// OAS 3.0.4 Encoding default types; later minor lines use the latest patch
// semantics. In particular a 3.0 string binary schema selects octets, while
// a 3.1/3.2 untyped schema uses application/octet-stream (doc.go Values).
func TestEditionsMultipartDefaults(t *testing.T) {
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		t.Run(version, func(t *testing.T) {
			c := editionClient(t, editionPost(version, "multipart/form-data", `{"type":"object","properties":{"plain":{},"file":{"type":"string","format":"binary"},"object":{"type":"object"}}}`), nil)
			m := reqMedia(t, mustOp(t, c, "POST /x"), 0)
			wantPlain, wantFile := "application/octet-stream", "text/plain"
			if version == "3.0.4" {
				wantPlain, wantFile = "text/plain", "application/octet-stream"
			}
			for i, want := range []string{wantPlain, wantFile, "application/json"} {
				if got := m.Encoding[i].ContentType; got != want {
					t.Errorf("field %d type %q want %q", i, got, want)
				}
			}
		})
	}
}

// Input.Body sequential framing is independent of edition ("in any
// edition"): every edition loads a sequential body and frames it the same
// way.
func TestEditionsSequentialBodies(t *testing.T) {
	for _, version := range editionVersions {
		for _, media := range []string{"application/jsonl", "application/json-seq", "text/event-stream"} {
			t.Run(version+"/"+media, func(t *testing.T) {
				c := editionClient(t, editionPost(version, media, `{"type":"array"}`), nil)
				body := any([]int{1, 2})
				want := "1\n2\n"
				if media == "application/json-seq" {
					want = "\x1e1\n\x1e2\n"
				}
				if media == "text/event-stream" {
					body = []openapi.Event{{Data: []byte("one")}, {Data: []byte("two")}}
					want = "data: one\n\ndata: two\n\n"
				}
				if got := string(editionBody(t, mustPrepare(t, c, "POST /x", &openapi.Input{Body: body}))); got != want {
					t.Errorf("framing %q want %q", got, want)
				}
			})
		}
	}
}

func positionalDoc(media, encoding string) string {
	return editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"content":{"`+media+`":{"schema":{"type":"array"},`+encoding+`}}}}}`)
}

// Input.Body and OAS 3.2.1 4.14.5.2: positional multipart has one part per
// slice/iterator element and positional Encoding, without implicit names.
func TestEditionsPositionalMultipart(t *testing.T) {
	for _, media := range []string{"multipart/mixed", "multipart/related"} {
		t.Run(media, func(t *testing.T) {
			c := editionClient(t, positionalDoc(media, `"prefixEncoding":[{"contentType":"text/plain"},{"contentType":"application/json"}],"itemEncoding":{"contentType":"application/octet-stream"}`), nil)
			m := reqMedia(t, mustOp(t, c, "POST /x"), 0)
			if len(m.Encoding) != 3 || m.Encoding[0].Name != "0" || m.Encoding[1].Name != "1" || m.Encoding[2].Name != "*" {
				t.Fatalf("encoding %+v", m.Encoding)
			}
			values := []any{"hello", map[string]int{"n": 1}, []byte("tail")}
			for _, iterator := range []bool{false, true} {
				t.Run(fmt.Sprint(iterator), func(t *testing.T) {
					body := any(values)
					calls := 0
					if iterator {
						body = iter.Seq[any](func(yield func(any) bool) {
							calls++
							for _, v := range values {
								if !yield(v) {
									return
								}
							}
						})
					}
					req := mustPrepare(t, c, "POST /x", &openapi.Input{MediaType: media + "; boundary=editions", Body: body})
					if calls != 0 {
						t.Error("Prepare consumed iterator")
					}
					wire := editionBody(t, req)
					_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), wire)
					if len(parts) != 3 {
						t.Fatalf("parts %d", len(parts))
					}
					for i, want := range []string{"hello", `{"n":1}`, "tail"} {
						if string(trimNL(parts[i].body)) != want || parts[i].header.Get("Content-Disposition") != "" {
							t.Errorf("part %d: %#v", i, parts[i])
						}
					}
					if iterator {
						if calls != 1 || req.HTTP.GetBody != nil {
							t.Error("iterator lifecycle")
						}
					} else {
						if req.HTTP.GetBody == nil {
							t.Fatal("slice not replayable")
						}
						r, e := req.HTTP.GetBody()
						if e != nil {
							t.Fatal(e)
						}
						again, e := io.ReadAll(r)
						r.Close()
						if e != nil || !bytes.Equal(wire, again) {
							t.Error("slice replay differs")
						}
					}
				})
			}
			// An unused prefix is ignored, even if it could not encode a value.
			mustPrepare(t, c, "POST /x", &openapi.Input{Body: []string{"only first"}})
		})
	}
}

// 3.2.1 permits ordered form-data arrays of one-property objects or Parts
// with Content-Disposition. Duplicate names retain order; itemSchema and
// prefixEncoding are exposed, and earlier editions do not enable them.
func TestEditionsArrayFormData(t *testing.T) {
	c := editionClient(t, positionalDoc("multipart/form-data", `"prefixEncoding":[{"contentType":"text/plain"}],"itemEncoding":{"contentType":"text/plain"},"itemSchema":{"type":"object"}`), nil)
	m := reqMedia(t, mustOp(t, c, "POST /x"), 0)
	if m.ItemSchema == nil || !m.Sequential {
		t.Errorf("media %+v", m)
	}
	body := []any{map[string]string{"z": "one"}, map[string]string{"a": "two"}, map[string]string{"z": "three"}, openapi.Part{Content: "four", Header: http.Header{"Content-Disposition": {`form-data; name="p"`}}}}
	req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: body})
	_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
	if len(parts) != 4 {
		t.Fatalf("parts %d", len(parts))
	}
	for i, name := range []string{"z", "a", "z", "p"} {
		if got := parts[i].header.Get("Content-Disposition"); got != formData(name) {
			t.Errorf("part %d disposition %q", i, got)
		}
	}
	for _, bad := range []any{[]any{map[string]string{"a": "x", "b": "y"}}, []any{map[string]string{}}, []any{"unnamed"}} {
		if _, err := c.Prepare("POST /x", &openapi.Input{Body: bad}); err == nil {
			t.Errorf("accepted invalid form array %#v", bad)
		}
	}
	for _, version := range []string{"3.0.4", "3.1.2"} {
		t.Run(version, func(t *testing.T) {
			doc := strings.Replace(positionalDoc("multipart/form-data", `"prefixEncoding":[{"contentType":"text/plain"}],"itemSchema":{"type":"object"}`), `"3.2.1"`, `"`+version+`"`, 1)
			c := editionClient(t, doc, nil)
			if reqMedia(t, mustOp(t, c, "POST /x"), 0).ItemSchema != nil {
				t.Error("earlier edition enabled itemSchema")
			}
			if _, err := c.Prepare("POST /x", &openapi.Input{Body: body}); err == nil {
				t.Error("earlier edition enabled array multipart")
			}
		})
	}
}

// Input.Body's lifecycle and error rules extend to positional writers:
// caller readers remain caller-owned; iterator errors retain identity, and
// no value is read by Prepare. Input.Body paths identify the failing item.
func TestEditionsPositionalIteratorError(t *testing.T) {
	c := editionClient(t, positionalDoc("multipart/mixed", `"itemEncoding":{"contentType":"text/plain"}`), nil)
	boom := errors.New("edition iterator failed")
	called := false
	body := iter.Seq2[string, error](func(yield func(string, error) bool) {
		called = true
		if yield("one", nil) {
			yield("", boom)
		}
	})
	req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: body})
	if called {
		t.Error("Prepare consumed iterator")
	}
	_, err := io.ReadAll(req.HTTP.Body)
	req.HTTP.Body.Close()
	if !errors.Is(err, boom) {
		t.Errorf("read error %v", err)
	}
}

// Omitted null wire parts do not compact source-array positions (OAS 3.2.1
// section 4.14.5.2). Iterators are admitted for multipart types other than
// form-data, per Input.Body.
func TestEditionsPositionalNullKeepsEncodingIndex(t *testing.T) {
	for _, media := range []string{"multipart/mixed", "multipart/form-data"} {
		t.Run(media, func(t *testing.T) {
			c := editionClient(t, positionalDoc(media, `"prefixEncoding":[{"contentType":"text/plain"},{"contentType":"application/json"}],"itemEncoding":{"contentType":"application/octet-stream"}`), nil)
			value := any(map[string]int{"n": 1})
			if media == "multipart/form-data" {
				value = map[string]any{"payload": value}
			}
			values := []any{nil, value}
			bodies := []any{values}
			if media != "multipart/form-data" {
				bodies = append(bodies, iter.Seq[any](func(yield func(any) bool) {
					for _, v := range values {
						if !yield(v) {
							return
						}
					}
				}))
			}
			for _, body := range bodies {
				req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: body})
				_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
				if len(parts) != 1 {
					t.Fatalf("parts %d want 1", len(parts))
				}
				if parts[0].header.Get("Content-Type") != "application/json" || string(trimNL(parts[0].body)) != `{"n":1}` {
					t.Errorf("part %#v", parts[0])
				}
			}
		})
	}
}
