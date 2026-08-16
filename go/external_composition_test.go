package openapiclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// externalCompositionCasesDigest pins the frozen twin case table. The identical
// file is executed by openbindings-go/formats/openapi and by
// openbindings-ts/packages/openapi; changing it in one engine without the
// others fails here.
const externalCompositionCasesDigest = "e14547ad29ea33a9e1c1aea190a044016a78f2548a3c2df21c6f402f2dc95c61"

type externalCompositionTable struct {
	Note  string                    `json:"note"`
	Cases []externalCompositionCase `json:"cases"`
}

type externalCompositionCase struct {
	Name      string                                 `json:"name"`
	Why       string                                 `json:"why"`
	Entry     string                                 `json:"entry"`
	Documents map[string]externalCompositionDocument `json:"documents"`
	Expect    externalCompositionExpectation         `json:"expect"`
}

type externalCompositionDocument struct {
	Text string          `json:"text"`
	JSON json.RawMessage `json:"json"`
}

func (d externalCompositionDocument) body() string {
	if d.JSON != nil {
		return string(d.JSON)
	}
	return d.Text
}

type externalCompositionExpectation struct {
	Outcome         string   `json:"outcome"`
	Unretrieved     []string `json:"unretrieved"`
	MessageContains []string `json:"messageContains"`
}

type compositionRoundTripper func(*http.Request) (*http.Response, error)

func (f compositionRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestExternalCompositionIsPointerScoped runs the shared multi-document case
// table through this engine's loader. This package carries no synthesizer, so
// it asserts the composition verdict itself — whether the artifact resolves,
// which references it names when it does not, and which resources it retrieves.
func TestExternalCompositionIsPointerScoped(t *testing.T) {
	data, err := os.ReadFile("testdata/external-composition-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	digest := sha256.Sum256(data)
	if got := hex.EncodeToString(digest[:]); got != externalCompositionCasesDigest {
		t.Fatalf("case table digest = %s, want %s; the table is shared byte-for-byte with the other engines", got, externalCompositionCasesDigest)
	}
	var table externalCompositionTable
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}

	for _, testCase := range table.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			retrieved := map[string]bool{}
			client := &http.Client{Transport: compositionRoundTripper(func(req *http.Request) (*http.Response, error) {
				address := req.URL.String()
				retrieved[address] = true
				document, ok := testCase.Documents[address]
				if !ok {
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Header:     http.Header{},
						Body:       io.NopCloser(strings.NewReader("no such document")),
						Request:    req,
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{},
					Body:       io.NopCloser(strings.NewReader(document.body())),
					Request:    req,
				}, nil
			})}

			_, err := loadDocument(context.Background(), client, Source{Location: testCase.Entry}, true)

			if testCase.Expect.Outcome == "refused" {
				if err == nil {
					t.Fatalf("%s: expected refusal, the document loaded", testCase.Why)
				}
				for _, fragment := range testCase.Expect.MessageContains {
					if !strings.Contains(err.Error(), fragment) {
						t.Fatalf("refusal does not name %q: %v", fragment, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: %v", testCase.Why, err)
			}
			for _, address := range testCase.Expect.Unretrieved {
				if retrieved[address] {
					t.Fatalf("%s was retrieved although it is reachable only from outside the composed closure", address)
				}
			}
		})
	}
}
