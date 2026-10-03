package openapi_test

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// RFC 2045 §6.8: inserted MIME whitespace does not change base64 payload
// bytes. Standard encoding supplies the oracle; no client parser is reused.
func FuzzStream7MIMEBase64Whitespace(f *testing.F) {
	f.Add([]byte("abc"), uint8(0))
	f.Add([]byte("a"), uint8(2))
	f.Add([]byte{0, 255, 128, 10, 13}, uint8(7))
	f.Add([]byte{}, uint8(1))
	f.Fuzz(func(t *testing.T, payload []byte, chunk uint8) {
		if len(payload) > 4096 {
			t.Skip()
		}
		encoded := base64.StdEncoding.EncodeToString(payload)
		var wire strings.Builder
		wire.WriteString("--B\r\nContent-Transfer-Encoding: base64\r\n\r\n")
		for i := range len(encoded) {
			wire.WriteByte(encoded[i])
			wire.WriteString([]string{" ", "\t", "\r\n"}[(i+int(chunk))%3])
		}
		wire.WriteString("\r\n--B--\r\n")
		r, _ := stream7Response(t, "multipart/mixed; boundary=B", wire.String(), func(o *openapi.Options) { o.MaxItemBytes = int64(max(1, len(payload))) }, 1+int(chunk)%31)
		got, errs := stream7Collect[[]byte](r)
		stream7NoErrors(t, errs)
		if len(got) != 1 || !bytes.Equal(got[0], payload) {
			t.Fatalf("decoded %x, want %x", got, payload)
		}
	})
}
