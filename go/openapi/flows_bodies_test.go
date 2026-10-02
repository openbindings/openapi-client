package openapi_test

import (
	"bytes"
	"errors"
	"io"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Runnable versions of the stage 4 example flows in example_test.go (dev
// loop: "Examples in example_test.go become runnable tests against httptest
// servers as their stage lands"): scenarios 6a (Example_filesUpload), 6b
// (Example_filesUploadRepeated), 6c (Example_filesUploadParts) and 16e
// (Example_streamingUpload). The OpenAPI 3.2 positional flows (6d, 6e) come
// with their edition.

const filesPaths = `
	"/folders/{folderId}/documents":{"post":{"operationId":"uploadDocument",
		"parameters":[{"name":"folderId","in":"path","required":true,"schema":{"type":"string"}}],
		"requestBody":{"required":true,"content":{"multipart/form-data":{
			"schema":{"type":"object","properties":{"title":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"file":{},"thumbnail":{}}},
			"encoding":{"file":{"contentType":"application/pdf"},"thumbnail":{"contentType":"image/png"}}}}}}},
	"/expenses":{"post":{"requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"note":{"type":"string"},"attachments":{"type":"array","items":{}}}},
		"encoding":{"attachments":{"contentType":"application/pdf"}}}}}}},
	"/batches":{"post":{"requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"manifest":{"type":"object"},"payload":{}}},
		"encoding":{"payload":{"contentType":"application/octet-stream","headers":{"X-Checksum-Sha256":{"required":true,"schema":{"type":"string"}}}}}}}}}},
	"/pets/import":{"post":{"operationId":"importPets","requestBody":{"required":true,"content":{"application/jsonl":{}}}}}`

// tempFile writes content to a file named name in a temporary directory
// and opens it.
func tempFile(t *testing.T, name, content string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// Scenario 6a: a file part with the caller's filename and type, a byte
// slice named after its property and typed by the Encoding, other fields as
// text; the file "streamed from disk when the request is sent, and read
// afresh if a redirect or a retry sends it again" (here a 307).
func TestFlowFilesUpload(t *testing.T) {
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/folders/") {
			rw.Header().Set("Location", "/stored")
			rw.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		rw.WriteHeader(http.StatusCreated)
	})
	c := parseFor(t, w, doc31(filesPaths), &openapi.Options{Redirects: openapi.FollowAll})
	f := tempFile(t, "q3.pdf", "%PDF-1.7 report")
	thumbnail := []byte("\x89PNG thumb")
	mustCall(t, c, "uploadDocument", &openapi.Input{
		Params: map[string]any{"folderId": "f-1"},
		Body: map[string]any{
			"title":     "Q3 report",
			"tags":      []string{"finance", "q3"},
			"file":      openapi.Part{Content: f, MediaType: "application/pdf", Filename: "q3.pdf"},
			"thumbnail": thumbnail,
		},
	}, nil)
	reqs := w.requests()
	if len(reqs) != 2 || reqs[0].Path != "/folders/f-1/documents" || reqs[1].Path != "/stored" {
		t.Fatalf("%d requests, want the upload and its 307 hop", len(reqs))
	}
	if !bytes.Equal(reqs[0].Body, reqs[1].Body) {
		t.Errorf("the hop sent another body")
	}
	_, _, parts := readMultipart(t, reqs[1].Header.Get("Content-Type"), reqs[1].Body)
	checkParts(t, parts, []wantPart{
		{disposition: formData("file", "q3.pdf"), ctype: "application/pdf", content: "%PDF-1.7 report"},
		{disposition: formData("tags"), ctype: "text/plain", content: "finance"},
		{disposition: formData("tags"), ctype: "text/plain", content: "q3"},
		{disposition: formData("thumbnail", "thumbnail"), ctype: "image/png", content: string(thumbnail)},
		{disposition: formData("title"), ctype: "text/plain", content: "Q3 report"},
	})
}

// Scenario 6b: several files under one field, one part each; the refusal
// names "Input.Body/attachments/1" when that part's type is not offered.
func TestFlowFilesUploadRepeated(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(filesPaths), nil)
	var attachments []openapi.Part
	for _, name := range []string{"invoice.pdf", "receipt.pdf"} {
		attachments = append(attachments, openapi.Part{Content: tempFile(t, name, "%PDF "+name), MediaType: "application/pdf", Filename: name})
	}
	mustCall(t, c, "POST /expenses", &openapi.Input{Body: map[string]any{"note": "Team dinner", "attachments": attachments}}, nil)
	got := w.last(t)
	_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
	checkParts(t, parts, []wantPart{
		{disposition: formData("attachments", "invoice.pdf"), ctype: "application/pdf", content: "%PDF invoice.pdf"},
		{disposition: formData("attachments", "receipt.pdf"), ctype: "application/pdf", content: "%PDF receipt.pdf"},
		{disposition: formData("note"), ctype: "text/plain", content: "Team dinner"},
	})

	attachments[1].MediaType = "image/png"
	before := w.count()
	resp, err := c.Call(t.Context(), "POST /expenses", &openapi.Input{Body: map[string]any{"note": "x", "attachments": attachments}}, nil)
	re := refusedSince(t, w, before, resp, err)
	wantKeys(t, "Settings", re.Settings, true, "Input.Body/attachments/1")
}

// Scenario 6c: JSON data in a part of its own, with no filename; raw bytes
// with no filename and a part header the document declares.
func TestFlowFilesUploadParts(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(filesPaths), nil)
	const sum = "n4bQgYhMfWWaL-qgxVrQFaO_TxsrC4Is0V1sFbDwCgg"
	mustCall(t, c, "POST /batches", &openapi.Input{Body: map[string]any{
		"manifest": openapi.Part{Content: map[string]any{"records": 2, "source": "nightly"}, MediaType: "application/json"},
		"payload":  openapi.Part{Content: []byte("..."), NoFilename: true, Header: http.Header{"X-Checksum-Sha256": {sum}}},
	}}, nil)
	got := w.last(t)
	_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
	checkParts(t, parts, []wantPart{
		{disposition: formData("manifest"), ctype: "application/json", content: `{"records":2,"source":"nightly"}`},
		{disposition: formData("payload"), ctype: "application/octet-stream", content: "...", extra: http.Header{"X-Checksum-Sha256": {sum}}},
	})
	op := mustOp(t, c, "POST /batches")
	if e := encodingByName(t, reqMedia(t, op, 0))["payload"]; e == nil || len(e.Headers) != 1 || e.Headers[0].Name != "X-Checksum-Sha256" {
		t.Errorf("payload Encoding = %+v, want its declared header", e)
	}
}

// cursor yields pets as a database cursor would, then fails with err, if
// not nil, once failAfter is closed (Example_streamingUpload's petRows).
func cursor(ps []Pet, err error, failAfter <-chan struct{}) iter.Seq2[Pet, error] {
	return func(yield func(Pet, error) bool) {
		for _, p := range ps {
			if !yield(p, nil) {
				return
			}
		}
		if err != nil {
			select {
			case <-failAfter:
			case <-time.After(10 * time.Second): // a client holding items back never lets the server see them
			}
			yield(Pet{}, err)
		}
	}
}

// Scenario 16e: a JSON Lines upload from a cursor, each pet written as it
// is yielded; a failed cursor aborts the upload, so the server never
// receives a complete body, and the call returns the cursor's error.
func TestFlowStreamingUpload(t *testing.T) {
	srv := newBodyServer(t, false, "")
	c := parseAt(t, doc31(filesPaths), srv.URL, srv.URL+"/openapi.json", nil)
	ps := []Pet{{ID: "p-1", Name: "Rex"}, {ID: "p-2", Name: "Tom", Tag: "cat"}}
	if _, err := c.Call(t.Context(), "importPets", &openapi.Input{Body: cursor(ps, nil, nil)}, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	body, err := srv.finished(t)
	if err != nil {
		t.Fatalf("the server's read ended with %v", err)
	}
	wantLines(t, body, `{"id":"p-1","name":"Rex"}`, `{"id":"p-2","name":"Tom","tag":"cat"}`)

	srv = newBodyServer(t, false, "")
	c = parseAt(t, doc31(filesPaths), srv.URL, srv.URL+"/openapi.json", nil)
	_, err = c.Call(t.Context(), "importPets", &openapi.Input{Body: cursor(ps, errCursor, srv.seen(`"p-2"`))}, nil)
	if !errors.Is(err, errCursor) || isRequestError(err) {
		t.Fatalf("Call = %v, want the cursor's error", err)
	}
	if _, err := srv.finished(t); err == nil {
		t.Errorf("the server received a complete body from a failed cursor")
	} else if errors.Is(err, io.EOF) {
		t.Errorf("the server's read ended at io.EOF")
	}
}
