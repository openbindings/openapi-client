package openapi_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Input.Body allows raw []byte, reader and Part field values in every edition.
// Swagger collectionFormat governs arrays, not these scalar field sources.
// Required fields still apply to structured bodies, while a pre-encoded whole
// body bypasses them. Stage 4 replay/ownership rules remain in force.
func TestEditionsSwaggerScalarRawFields(t *testing.T) {
	for _, kind := range []string{"string", "file"} {
		for _, media := range []string{"application/x-www-form-urlencoded", "multipart/form-data"} {
			if kind == "file" && media == "application/x-www-form-urlencoded" {
				continue
			} // file refusal has its own existing test
			t.Run(kind+"/"+media, func(t *testing.T) {
				c := editionClient(t, editionDoc("2.0", `"/x":{"post":{"consumes":["`+media+`"],"parameters":[{"name":"p","in":"formData","type":"`+kind+`","required":true}]}}`), nil)
				for _, tc := range []struct {
					name   string
					make   func() any
					raw    []byte
					replay bool
				}{
					{"bytes", func() any { return []byte{'a', 0, ' ', '&', 0xff} }, []byte{'a', 0, ' ', '&', 0xff}, true},
					{"replayable reader", func() any { return strings.NewReader("r d&") }, []byte("r d&"), true},
					{"one-shot reader", func() any { return &editionCountReader{Reader: strings.NewReader("once &")} }, []byte("once &"), false},
					{"Part bytes", func() any { return openapi.Part{Content: []byte("part &")} }, []byte("part &"), true},
					{"Part reader", func() any { return openapi.Part{Content: &editionCountReader{Reader: strings.NewReader("part once")}} }, []byte("part once"), false},
				} {
					t.Run(tc.name, func(t *testing.T) {
						value := tc.make()
						req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]any{"p": value}})
						var counter *editionCountReader
						switch v := value.(type) {
						case *editionCountReader:
							counter = v
						case openapi.Part:
							counter, _ = v.Content.(*editionCountReader)
						}
						if counter != nil && counter.reads != 0 {
							t.Error("Prepare consumed one-shot field reader")
						}
						if (req.HTTP.GetBody != nil) != tc.replay {
							t.Errorf("replayable=%v want %v", req.HTTP.GetBody != nil, tc.replay)
						}
						body := editionBody(t, req)
						if media == "application/x-www-form-urlencoded" {
							if string(body) != formPairs("p", string(tc.raw)) {
								t.Errorf("form %q want %q", body, formPairs("p", string(tc.raw)))
							}
						} else {
							_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), body)
							if len(parts) != 1 || !bytes.Equal(parts[0].body, tc.raw) {
								t.Errorf("parts %#v", parts)
							}
						}
						if tc.replay {
							r, err := req.HTTP.GetBody()
							if err != nil {
								t.Fatal(err)
							}
							again, err := io.ReadAll(r)
							r.Close()
							if err != nil || !bytes.Equal(again, body) {
								t.Errorf("replay %q error %v", again, err)
							}
						}
						if counter != nil && counter.closed {
							t.Error("client closed caller field reader")
						}
					})
				}
				_, err := c.Prepare("POST /x", &openapi.Input{Body: map[string]any{}})
				wantKeys(t, "Inputs", asRequestError(t, err).Inputs, false, "Input.Body/p")
				chosen := media
				if media == "multipart/form-data" {
					chosen += "; boundary=caller"
				}
				mustPrepare(t, c, "POST /x", &openapi.Input{MediaType: chosen, Body: []byte("caller framed")})
			})
		}
	}
}

type editionCountReader struct {
	io.Reader
	reads  int
	closed bool
}

func (r *editionCountReader) Read(p []byte) (int, error) { r.reads++; return r.Reader.Read(p) }
func (r *editionCountReader) Close() error               { r.closed = true; return nil }

// doc.go Swagger empty values and Options.NameOnlyEmpty speak of a parameter
// given an empty string. Empty raw bytes/readers and one-element arrays remain
// distinct input forms, not scalar empty strings.
func TestEditionsSwaggerNameOnlyScalarEmpty(t *testing.T) {
	for _, loc := range []string{"query", "formData"} {
		t.Run(loc, func(t *testing.T) {
			for _, array := range []bool{false, true} {
				decl := `"type":"string"`
				if array {
					decl = `"type":"array","collectionFormat":"multi","items":{"type":"string"}`
				}
				c := editionClient(t, editionDoc("2.0", `"/x":{"post":{"consumes":["application/x-www-form-urlencoded"],"parameters":[{"name":"p","in":"`+loc+`","allowEmptyValue":true,`+decl+`}]}}`), &openapi.Options{NameOnlyEmpty: true})
				values := []any{""}
				wants := []string{"p"}
				if array {
					values = []any{[]string{""}}
					wants = []string{"p="}
				} else if loc == "formData" {
					values = append(values, []byte{}, strings.NewReader(""))
					wants = append(wants, "p=", "p=")
				}
				for i, v := range values {
					in := &openapi.Input{Params: map[string]any{"p": v}}
					if loc == "formData" {
						in = &openapi.Input{Body: map[string]any{"p": v}}
					}
					req := mustPrepare(t, c, "POST /x", in)
					got := req.HTTP.URL.RawQuery
					if loc == "formData" {
						got = string(editionBody(t, req))
					}
					if got != wants[i] {
						t.Errorf("array=%v value %T: %q want %q", array, v, got, wants[i])
					}
				}
			}
		})
	}
}
