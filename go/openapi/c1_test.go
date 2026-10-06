package openapi_test

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Error text never quotes a response body. errors.go, DecodeError.Error
// "returns the operation, the status and the reason, never the body ... A
// decoder's own error, whose message can quote the body, is not in the text;
// errors.As finds it through Unwrap." client.go,
// Call: JSON types are decoded "with encoding/json", XML types "with
// encoding/xml", a caller's type "exactly as json.Unmarshal would" (doc.go,
// Values). Each body below is built from letter runs that no error text
// would hold for any other reason, and, but for malformed JSON, whose
// SyntaxError quotes one character, each decoder's own message quotes part
// of it.

// c1Target is decoded from JSON; its only field has a name no error text
// holds unless it quotes the body.
type c1Target struct {
	Qzv string `json:"qzv"`
}

// c1XML is decoded from XML.
type c1XML struct {
	XMLName xml.Name `xml:"qzxj"`
	Vwkq    string   `xml:"vwkq"`
}

// quotedBody is a caller codec's error that quotes the whole body.
type quotedBody struct{ body string }

func (e *quotedBody) Error() string { return fmt.Sprintf("cannot read %q", e.body) }

// quotingCodec's Decode fails with a *quotedBody.
type quotingCodec struct{}

func (quotingCodec) Encode(w io.Writer, v any) error { return errors.New("not used") }
func (quotingCodec) Decode(r io.Reader, v any) error {
	b, _ := io.ReadAll(r)
	return &quotedBody{string(b)}
}

// wantNoBodyInText fails when text holds any run of 3 or more bytes of body.
func wantNoBodyInText(t *testing.T, text, body string) {
	t.Helper()
	for i := 0; i+3 <= len(body); i++ {
		if strings.Contains(text, body[i:i+3]) {
			t.Errorf("DecodeError text %q holds %q from the body %q", text, body[i:i+3], body)
			return
		}
	}
}

func TestC1DecodeErrorOmitsBody(t *testing.T) {
	type target = func() any
	tests := []struct {
		name     string
		key, ct  string
		body     string
		out      target
		codecs   map[string]openapi.Codec
		decoderE func(error) bool // errors.As finds the decoder's own error
		quiet    bool             // the decoder's message quotes less than 3 bytes
	}{
		{
			name: "malformed JSON", key: "getPet", ct: "application/json", body: `{"qzv":QZXJWVKQ}`,
			out:      func() any { return new(c1Target) },
			decoderE: func(err error) bool { var se *json.SyntaxError; return errors.As(err, &se) },
			quiet:    true, // encoding/json quotes one character
		},
		{
			name: "JSON type mismatch", key: "getPet", ct: "application/json", body: `{"qzv":987654321987}`,
			out: func() any { return new(c1Target) },
			decoderE: func(err error) bool {
				var ue *json.UnmarshalTypeError
				return errors.As(err, &ue)
			},
		},
		{
			name: "XML element names", key: "xml", ct: "application/xml", body: `<qzxj><vwkq>1</vwkqz></qzxj>`,
			out:      func() any { return new(c1XML) },
			decoderE: func(err error) bool { var se *xml.SyntaxError; return errors.As(err, &se) },
		},
		{
			name: "caller codec", key: "cborAndJSON", ct: "application/cbor", body: `QZXJ-VWKQ-ZQVX`,
			out:      func() any { return new(any) },
			codecs:   map[string]openapi.Codec{"application/cbor": quotingCodec{}},
			decoderE: func(err error) bool { var qb *quotedBody; return errors.As(err, &qb) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The decoder's own message does quote the body (the premise).
			var own error
			switch tt.ct {
			case "application/json":
				own = json.Unmarshal([]byte(tt.body), tt.out())
			case "application/xml":
				own = xml.Unmarshal([]byte(tt.body), tt.out())
			default:
				own = quotingCodec{}.Decode(strings.NewReader(tt.body), tt.out())
			}
			if own == nil {
				t.Fatalf("premise: the body decodes")
			}
			quotes := false
			for i := 0; i+3 <= len(tt.body); i++ {
				if strings.Contains(own.Error(), tt.body[i:i+3]) {
					quotes = true
				}
			}
			if !quotes && !tt.quiet {
				t.Fatalf("premise: the decoder's message %q quotes nothing of the body", own)
			}

			_, c := respClient(t, typedAnswer(200, tt.ct, tt.body), &openapi.Options{Codecs: tt.codecs})
			resp, err := c.Call(t.Context(), tt.key, nil, tt.out())
			var de *openapi.DecodeError
			if !errors.As(err, &de) {
				t.Fatalf("Call = %v, want a *DecodeError", err)
			}
			if resp == nil {
				t.Errorf("no Response with the DecodeError")
			}
			wantNoBodyInText(t, err.Error(), tt.body)
			wantNoBodyInText(t, de.Error(), tt.body)
			if !tt.decoderE(err) {
				t.Errorf("errors.As finds no decoder error in %v", err)
			}
			if string(de.Content) != tt.body {
				t.Errorf("Content = %q, want the body", de.Content)
			}

			// Response.Decode's DecodeError too, after Request.Send.
			resp, err = mustPrepare(t, c, tt.key, nil).Send(t.Context())
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			defer resp.Body.Close()
			err = resp.Decode(tt.out())
			if !errors.As(err, &de) {
				t.Fatalf("Response.Decode = %v, want a *DecodeError", err)
			}
			wantNoBodyInText(t, err.Error(), tt.body)
			if !tt.decoderE(err) {
				t.Errorf("Response.Decode: errors.As finds no decoder error in %v", err)
			}
		})
	}
}

// The same for a StatusError's Decode: its DecodeError text holds nothing
// of the kept body (errors.go, StatusError.Decode: "decodes Content ... as
// Response.Decode does").
func TestC1StatusErrorDecodeOmitsBody(t *testing.T) {
	body := `{"qzv":987654321987}`
	_, c := respClient(t, jsonAnswer(http.StatusNotFound, body), nil)
	_, err := c.Call(t.Context(), "getPet", nil, nil)
	var se *openapi.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("Call = %v, want a *StatusError", err)
	}
	err = se.Decode(new(c1Target))
	var de *openapi.DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("StatusError.Decode = %v, want a *DecodeError", err)
	}
	wantNoBodyInText(t, err.Error(), body)
	var ue *json.UnmarshalTypeError
	if !errors.As(err, &ue) {
		t.Errorf("errors.As finds no *json.UnmarshalTypeError in %v", err)
	}
}
