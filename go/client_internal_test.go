package openapi

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	runtime "github.com/openbindings/openapi-client/go/internal/runtime"
)

func TestClientErrorConfinesUnknownRuntimeFailures(t *testing.T) {
	cause := errors.New("unclassified private failure")
	err := clientError(cause)
	client, ok := err.(*ClientError)
	if !ok {
		t.Fatalf("error type = %T, want *ClientError", err)
	}
	if client.Kind != ErrorInternal || client.Code != "INTERNAL_ERROR" {
		t.Fatalf("error = %#v", client)
	}
	if !errors.Is(err, cause) {
		t.Fatal("public error does not preserve the original cause")
	}
}

func TestStreamResultNeverExposesSuccessfulReplayBody(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://api.example.test/items", nil)
	if err != nil {
		t.Fatal(err)
	}
	result := streamResultValue(&runtime.StreamResult{
		OK: true,
		Response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    request,
		},
	})
	if result.Response.Body != nil {
		t.Fatal("successful public stream exposes a competing response body")
	}
	if result.Response.StatusCode != http.StatusOK || result.Response.Header.Get("Content-Type") != "application/json" || result.Response.Request.URL.String() != request.URL.String() {
		t.Fatalf("response metadata = %#v", result.Response)
	}
}
