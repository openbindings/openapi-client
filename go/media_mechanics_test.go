package openapiclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientPropertyMediaSelectionAndMultipartTransferHeader(t *testing.T) {
	var partType, transferEncoding, partBody string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("Content-Type: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		part, err := multipart.NewReader(request.Body, parameters["boundary"]).NextRawPart()
		if err != nil {
			t.Errorf("multipart part: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Errorf("part body: %v", err)
		}
		partType = part.Header.Get("Content-Type")
		transferEncoding = part.Header.Get("Content-Transfer-Encoding")
		partBody = string(body)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	document := testDocument(server.URL, `{
	  "/upload":{"post":{"requestBody":{"required":true,"content":{"multipart/form-data":{
	    "schema":{"type":"object","properties":{"payload":{}}},
	    "encoding":{"payload":{"contentType":"image/*"}}
	  }}},"responses":{"204":{"description":"ok"}}}},
	  "/encoded":{"post":{"requestBody":{"required":true,"content":{"multipart/form-data":{
	    "schema":{"type":"object","properties":{"payload":{"type":"string","format":"byte"}}},
	    "encoding":{"payload":{"headers":{"Content-Transfer-Encoding":{"schema":{"type":"string","enum":["base64"]}}}}}
	  }}},"responses":{"204":{"description":"ok"}}}}
	}`)
	document = bytes.Replace(document, []byte(`"openapi":"3.1.0"`), []byte(`"openapi":"3.0.4"`), 1)
	client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Call(context.Background(), PathOperation("/upload", POST), Input{
		Body:               map[string]any{"payload": base64.StdEncoding.EncodeToString([]byte("abc"))},
		PropertyMediaTypes: map[string]string{"payload": "image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if partType != "image/png" || transferEncoding != "" || partBody != "abc" {
		t.Fatalf("selected property part = type %q, transfer %q, body %q", partType, transferEncoding, partBody)
	}

	_, err = client.Call(context.Background(), PathOperation("/encoded", POST), Input{
		Body: map[string]any{"payload": "YWJj"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if transferEncoding != "base64" || partBody != "YWJj" {
		t.Fatalf("declared transfer part = transfer %q, body %q", transferEncoding, partBody)
	}
}
