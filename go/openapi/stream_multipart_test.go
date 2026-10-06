package openapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

func stream7Multipart(parts ...string) string {
	return "preamble ignored\r\n--B\r\n" + strings.Join(parts, "\r\n--B\r\n") + "\r\n--B--\r\nepilogue ignored"
}

// stream.go Items; RFC 2046 §5.1: ordered parts, their own media type,
// text/plain default, preamble/epilogue ignored. RFC 2045 §§6.7/6.8:
// transfer decoding precedes value decoding. No response schema inference.
func TestStream7MultipartDecoded(t *testing.T) {
	wire := stream7Multipart("Content-Type: application/json\r\n\r\n{\"n\":9007199254740993}", "\r\nplain", "Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\n"+base64.StdEncoding.EncodeToString([]byte("café")), "Content-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\na=3Db=0Asecond")
	for _, ct := range []string{"multipart/mixed; boundary=B", "multipart/form-data; boundary=\"B\"", "multipart/x-unrecognized; boundary=B"} {
		t.Run(ct, func(t *testing.T) {
			r, b := stream7Response(t, ct, wire, nil, 1, 2, 3)
			got, errs := stream7Collect[any](r)
			stream7NoErrors(t, errs)
			want := []any{map[string]any{"n": json.Number("9007199254740993")}, "plain", "café", "a=b\nsecond"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v want %#v", got, want)
			}
			if b.closed.Load() == 0 {
				t.Fatal("not closed")
			}
		})
	}
	t.Run("digest default", func(t *testing.T) {
		r, _ := stream7Response(t, "multipart/digest; boundary=B", stream7Multipart("\r\nFrom: example@example.test\r\n\r\nhello"), nil)
		got, errs := stream7Collect[any](r)
		stream7NoErrors(t, errs)
		if len(got) != 1 || !bytes.Equal(got[0].([]byte), []byte("From: example@example.test\r\n\r\nhello")) {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("default media custom codec", func(t *testing.T) {
		codec := stream7Codec{decode: func(r io.Reader, v any) error {
			p, err := io.ReadAll(r)
			if err == nil {
				*v.(*string) = "message:" + string(p)
			}
			return err
		}}
		r, _ := stream7Response(t, "multipart/digest; boundary=B", stream7Multipart("\r\nhello"), func(o *openapi.Options) { o.Codecs = map[string]openapi.Codec{"message/rfc822": codec} })
		got, errs := stream7Collect[string](r)
		stream7NoErrors(t, errs)
		if !reflect.DeepEqual(got, []string{"message:hello"}) {
			t.Fatal(got)
		}
	})
	t.Run("part codecs", func(t *testing.T) {
		r, _ := stream7Response(t, "multipart/mixed; boundary=B", stream7Multipart("Content-Type: application/json\r\nContent-Transfer-Encoding: base64\r\n\r\nMTIz"), func(o *openapi.Options) {
			o.Codecs = map[string]openapi.Codec{"application/json": stream7Codec{decode: func(r io.Reader, v any) error { return json.NewDecoder(r).Decode(v) }}}
		})
		got, errs := stream7Collect[any](r)
		stream7NoErrors(t, errs)
		if len(got) != 1 || got[0] != float64(123) {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("nested remains one part", func(t *testing.T) {
		inner := "--C\r\nContent-Type: text/plain\r\n\r\ninside\r\n--C--\r\n"
		r, _ := stream7Response(t, "multipart/mixed; boundary=B", stream7Multipart("Content-Type: multipart/mixed; boundary=C\r\n\r\n"+inner), nil, 1)
		got, errs := stream7Collect[any](r)
		stream7NoErrors(t, errs)
		if len(got) != 1 || !bytes.Equal(got[0].([]byte), []byte(inner)) {
			t.Fatalf("got %#v", got)
		}
	})
}

// Value/transfer errors concern the part alone. A later part still arrives.
// Multipart syntax errors are terminal. Raw Parts are precisely NextPart,
// whose quoted-printable special handling deliberately differs from base64.
func TestStream7MultipartErrorsAndRawOracle(t *testing.T) {
	t.Run("bad JSON and type recover", func(t *testing.T) {
		r, _ := stream7Response(t, "multipart/mixed; boundary=B", stream7Multipart("Content-Type: application/json\r\n\r\n{bad}", "Content-Type: application/json\r\n\r\n\"wrong type\"", "Content-Type: application/json\r\n\r\n3"), nil, 1)
		got, errs := stream7Collect[int](r)
		if len(got) != 3 || !errors.Is(errs[0], openapi.ErrItem) || !errors.Is(errs[1], openapi.ErrItem) || errs[2] != nil || got[2] != 3 {
			t.Fatalf("got %v errors %v", got, errs)
		}
	})
	t.Run("bad base64 recovers", func(t *testing.T) {
		r, _ := stream7Response(t, "multipart/mixed; boundary=B", stream7Multipart("Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\n%%%", "Content-Type: text/plain\r\n\r\ngood"), nil)
		got, errs := stream7Collect[string](r)
		if len(got) != 2 || !errors.Is(errs[0], openapi.ErrItem) || errs[1] != nil || got[1] != "good" {
			t.Fatalf("%v %v", got, errs)
		}
	})
	for _, wire := range []string{"not a multipart body", "--B\r\nmalformed header\r\n\r\nx\r\n--B--\r\n"} {
		t.Run("syntax "+wire, func(t *testing.T) {
			r, _ := stream7Response(t, "multipart/mixed; boundary=B", wire, nil)
			_, errs := stream7Collect[any](r)
			if len(errs) != 1 || errs[0] == nil || errors.Is(errs[0], openapi.ErrItem) {
				t.Fatalf("syntax errors %v", errs)
			}
		})
	}
	t.Run("raw NextPart parity", func(t *testing.T) {
		wire := stream7Multipart("X-Name: first\r\nContent-Transfer-Encoding: base64\r\n\r\naGVsbG8=", "Content-Transfer-Encoding: quoted-printable\r\n\r\na=3Db", "\r\nlast")
		oracle := multipart.NewReader(strings.NewReader(wire), "B")
		r, b := stream7Response(t, "multipart/mixed; boundary=B", wire, func(o *openapi.Options) { o.MaxItemBytes = 1 }, 1)
		count := 0
		for got, err := range openapi.Items[*multipart.Part](r) {
			if err != nil {
				t.Fatal(err)
			}
			want, e := oracle.NextPart()
			if e != nil {
				t.Fatal(e)
			}
			gb, ge := io.ReadAll(got)
			wb, we := io.ReadAll(want)
			if ge != nil || we != nil || !bytes.Equal(gb, wb) || !reflect.DeepEqual(got.Header, want.Header) {
				t.Fatalf("part got %q %v %v want %q %v %v", gb, got.Header, ge, wb, want.Header, we)
			}
			count++
		}
		if count != 3 || b.closed.Load() == 0 {
			t.Fatalf("count %d closed %d", count, b.closed.Load())
		}
	})
	t.Run("unread raw part skipped at next", func(t *testing.T) {
		r, _ := stream7Response(t, "multipart/mixed; boundary=B", stream7Multipart("\r\nfirst unread", "\r\nsecond"), nil, 1)
		i := 0
		for part, err := range openapi.Items[*multipart.Part](r) {
			if err != nil {
				t.Fatal(err)
			}
			if i == 1 {
				p, e := io.ReadAll(part)
				if e != nil || string(p) != "second" {
					t.Fatalf("second %q %v", p, e)
				}
			}
			i++
		}
		if i != 2 {
			t.Fatal(i)
		}
	})
}

// MaxItemBytes applies after transfer decoding, excluding headers and MIME
// delimiters (Options.MaxItemBytes); raw Parts remain unbounded.
func TestStream7MultipartLimitAccounting(t *testing.T) {
	for _, tc := range []struct {
		name, part string
		limit      int64
		bad        bool
	}{
		{"base64 exact", "Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\nYWJj", 3, false},
		{"base64 too long", "Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\nYWJjZA==", 3, true},
		{"QP exact", "Content-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\na=3Db", 3, false},
		{"headers excluded", "X-Long: " + strings.Repeat("h", 2048) + "\r\n\r\nabc", 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := stream7Response(t, "multipart/mixed; boundary=B", stream7Multipart(tc.part), func(o *openapi.Options) { o.MaxItemBytes = tc.limit }, 1)
			got, errs := stream7Collect[[]byte](r)
			if len(errs) != 1 {
				t.Fatalf("%q %v", got, errs)
			}
			if tc.bad {
				var max *http.MaxBytesError
				if !errors.As(errs[0], &max) || errors.Is(errs[0], openapi.ErrItem) {
					t.Fatal(errs[0])
				}
			} else if errs[0] != nil || len(got[0]) != int(tc.limit) {
				t.Fatalf("%q %v", got, errs)
			}
		})
	}
}

// Raw NextPart yields when the part headers have arrived, before its body
// has completed. The consumer acknowledges the open part to release the
// remaining bytes; no wall-clock sleep releases this producer.
func TestStream7RawMultipartIncremental(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	defer open()
	prefix := "--B\r\nContent-Type: text/plain\r\n\r\n"
	suffix := "later\r\n--B--\r\n"
	body := &stream7Body{reader: stream7ReadFunc(func(p []byte) (int, error) {
		if prefix != "" {
			n := copy(p, prefix)
			prefix = prefix[n:]
			return n, nil
		}
		<-release
		if suffix != "" {
			n := copy(p, suffix)
			suffix = suffix[n:]
			return n, nil
		}
		return 0, io.EOF
	})}
	c := stream7Client(t, "3.1.2", stream7RT(func(r *http.Request) (*http.Response, error) {
		return stream7HTTP(r, 200, "multipart/mixed; boundary=B", body), nil
	}), func(o *openapi.Options) { o.MaxItemBytes = 1 })
	r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		count := 0
		for part, e := range openapi.Items[*multipart.Part](r) {
			if e != nil {
				done <- e
				return
			}
			first <- struct{}{}
			p, e := io.ReadAll(part)
			if e != nil || string(p) != "later" {
				done <- fmt.Errorf("part %q: %v", p, e)
				return
			}
			count++
		}
		if count != 1 {
			done <- fmt.Errorf("part count %d", count)
			return
		}
		done <- nil
	}()
	stream7Await(t, first)
	open()
	if err := stream7Await(t, done); err != nil {
		t.Fatal(err)
	}
}
