package openapi_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"iter"
	"log"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

const docURL = "https://api.example.com/openapi.json"

// client is a Client loaded as in Example_firstCall.
var client *openapi.Client

type Pet struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	Tag  string `json:"tag,omitempty"`
}

type Problem struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// ask and choose stand in for asking a user to type a value or pick one of
// several options; record stands in for a metrics sink.
func ask(prompt string, options []string) string    { return "" }
func choose(prompt string, options []string) int    { return 0 }
func record(label, outcome string, d time.Duration) {}

// Scenario 1: load a document, call an operation with a path parameter and
// an integer query parameter, and decode the JSON result.
func Example_firstCall() {
	ctx := context.Background()
	c, err := openapi.Load(ctx, docURL, nil)
	if err != nil {
		log.Fatal(err)
	}

	var pet Pet
	_, err = c.Call(ctx, "getPet", &openapi.Input{
		Params: map[string]any{"petId": "p-7", "revision": 3},
	}, &pet)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(pet.Name)
}

// Scenario 2: send a JSON body built from a struct, and read Location from
// the 201.
func Example_create() {
	ctx := context.Background()

	var created Pet
	resp, err := client.Call(ctx, "createPet", &openapi.Input{
		Body: Pet{Name: "Rex", Tag: "dog"},
	}, &created)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(resp.StatusCode, resp.Header.Get("Location"), created.ID)
}

// Scenario 3: a declared 404 or 422 error body, told apart from a status the
// document does not declare, a reply that did not decode, a call that was
// never sent, a cancelled context and a transport failure. The package's
// own types are tested first, since they may wrap the others.
func Example_failure() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var pet Pet
	_, err := client.Call(ctx, "updatePet", &openapi.Input{
		Params: map[string]any{"petId": "p-404"},
		Body:   Pet{Name: ""},
	}, &pet)

	var (
		se *openapi.StatusError
		de *openapi.DecodeError
		re *openapi.RequestError
		ue *url.Error
	)
	switch {
	case err == nil:
		fmt.Println("updated", pet.Name)
	case errors.As(err, &se) && se.Declared != "":
		// 404 or 422, with the body the document declares for it.
		var p Problem
		if err := se.Decode(&p); err != nil {
			log.Printf("%d with an unreadable body: %v", se.StatusCode, err)
			return
		}
		fmt.Println(se.StatusCode, p.Title, p.Detail)
	case errors.As(err, &se):
		// The document promises nothing about this status or its body.
		fmt.Printf("undeclared %d %s: %.200s\n", se.StatusCode, se.Header.Get("Content-Type"), se.Content)
	case errors.As(err, &de):
		// A 2xx: the update happened, but the reply did not fit Pet.
		fmt.Println("updated; reply unreadable:", de.Err)
	case errors.As(err, &re):
		fmt.Println("not sent:", re)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		fmt.Println("gave up:", err)
	case errors.As(err, &ue):
		fmt.Println("transport failure:", ue.Err)
	}
}

// Scenario 4a: an API key, for the document's security scheme api_key.
func Example_credentialsAPIKey() {
	ctx := context.Background()
	c, err := openapi.Load(ctx, docURL, &openapi.Options{
		Credentials: map[string]openapi.Credential{
			"api_key": openapi.Secret(os.Getenv("PETS_API_KEY")),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := c.Call(ctx, "listPets", nil, nil); err != nil {
		log.Fatal(err)
	}
}

// Scenario 4a, two mistakes. A credential under a name the document does
// not use is refused by Load, as a misspelling. An environment variable
// that was never set gives an empty secret, refused at the call, even on an
// operation that also allows anonymous access. Neither sends anything.
func Example_credentialsTypo() {
	ctx := context.Background()
	_, err := openapi.Load(ctx, docURL, &openapi.Options{
		Credentials: map[string]openapi.Credential{
			"apiKey": openapi.Secret(os.Getenv("PETS_API_KEY")), // the document says api_key
		},
	})
	var re *openapi.RequestError
	if errors.As(err, &re) {
		// Options.Credentials names "apiKey", which the document never
		// uses (it uses api_key, oauth).
		log.Print(re)
	}

	c, err := openapi.Load(ctx, docURL, &openapi.Options{
		Credentials: map[string]openapi.Credential{
			"api_key": openapi.Secret(os.Getenv("PETS_API_KEY_TYPO")), // never set
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	_, err = c.Call(ctx, "searchPets", nil, nil)
	if errors.As(err, &re) {
		// no credential for security scheme "api_key" (set
		// Options.Credentials["api_key"])
		log.Fatal(re)
	}
}

// Scenario 4b: a static bearer token, for an http bearer scheme.
func Example_credentialsBearer() {
	ctx := context.Background()
	c, err := openapi.Load(ctx, docURL, &openapi.Options{
		Credentials: map[string]openapi.Credential{
			"bearerAuth": openapi.Secret(os.Getenv("PETS_TOKEN")),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := c.Call(ctx, "listPets", nil, nil); err != nil {
		log.Fatal(err)
	}
}

// tokenSource stands in for a golang.org/x/oauth2 TokenSource made with
// oauth2.ReuseTokenSource, which is safe for concurrent use, caches its
// token, and refreshes it once when it expires.
var tokenSource interface {
	Token() (*oauthToken, error)
}

type oauthToken struct{ AccessToken string }

// errIdentity marks a failure of the identity provider, not of the API.
var errIdentity = errors.New("identity provider unavailable")

// Scenario 4c: a token that refreshes. The source is asked for the token
// each time a request is sent, from every sending goroutine, never when a
// call is prepared. Its own error type says who failed.
func Example_credentialsRefreshing() {
	ctx := context.Background()
	c, err := openapi.Load(ctx, docURL, &openapi.Options{
		Credentials: map[string]openapi.Credential{
			"oauth": openapi.SecretFunc(func(context.Context) (string, error) {
				t, err := tokenSource.Token()
				if err != nil {
					return "", fmt.Errorf("%w: %w", errIdentity, err)
				}
				return t.AccessToken, nil
			}),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	_, err = c.Call(ctx, "listPets", nil, nil)
	if errors.Is(err, errIdentity) {
		// Nothing was sent: page the identity team, not the partner's.
		log.Fatal(err)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// Scenario 4d: HTTP Basic.
func Example_credentialsBasic() {
	ctx := context.Background()
	c, err := openapi.Load(ctx, docURL, &openapi.Options{
		Credentials: map[string]openapi.Credential{
			"basicAuth": openapi.Basic("alice", os.Getenv("PETS_PASSWORD")),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := c.Call(ctx, "listPets", nil, nil); err != nil {
		log.Fatal(err)
	}
}

// Scenario 4e: mutual TLS through the caller's own http.Client. A mutualTLS
// scheme needs no entry in Credentials.
func Example_credentialsMutualTLS() {
	ctx := context.Background()
	cert, err := tls.LoadX509KeyPair("client.crt", "client.key")
	if err != nil {
		log.Fatal(err)
	}
	hc := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
	}}
	c, err := openapi.Load(ctx, docURL, &openapi.Options{HTTPClient: hc})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := c.Call(ctx, "listPets", nil, nil); err != nil {
		log.Fatal(err)
	}
}

// signer stands in for a transport that authorizes requests itself, such as
// an oauth2.Transport or a request-signing RoundTripper. It sees every hop of
// a redirect, so it signs only requests to the API's own host.
type signer struct {
	host string
	next http.RoundTripper
}

func (s signer) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != s.host {
		return s.next.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Signature", "...")
	return s.next.RoundTrip(req)
}

// Scenario 4f: a scheme the caller's own transport satisfies, marked
// explicitly, so a scheme merely left out is still refused locally.
func Example_credentialsOwnTransport() {
	ctx := context.Background()
	c, err := openapi.Load(ctx, docURL, &openapi.Options{
		HTTPClient: &http.Client{Transport: signer{host: "api.example.com", next: http.DefaultTransport}},
		Credentials: map[string]openapi.Credential{
			"signature": openapi.FromTransport(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := c.Call(ctx, "listPets", nil, nil); err != nil {
		log.Fatal(err)
	}
}

// Scenario 4g: the operation offers several security alternatives and the
// caller holds credentials for more than one. By default the first
// alternative whose schemes all have credentials is used; Input.Security
// picks another by its key, and Response.Security says which was used.
func Example_credentialsAlternatives() {
	ctx := context.Background()
	c, err := openapi.Load(ctx, docURL, &openapi.Options{
		Credentials: map[string]openapi.Credential{
			"api_key": openapi.Secret(os.Getenv("PETS_API_KEY")),
			"oauth":   openapi.Secret(os.Getenv("PETS_TOKEN")),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	op, err := c.Operation("listPets")
	if err != nil {
		log.Fatal(err)
	}
	// Prefer the OAuth alternative that asks for the pets:read scope.
	in := &openapi.Input{}
	for _, alt := range op.Security {
		if slices.ContainsFunc(alt.Schemes, func(s openapi.SecurityScheme) bool {
			return s.Type == "oauth2" && slices.Contains(s.Scopes, "pets:read")
		}) {
			in.Security = alt.Key // {"oauth":["pets:read"]}
			break
		}
	}

	resp, err := c.Call(ctx, op.Key, in, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("signed in with", resp.Security)
}

// Scenario 4h: an operation that allows anonymous access and offers more
// with a key, listing {} first, as OpenAPI's own example does:
//
//	security: [{}, {api_key: []}]
//
// The credential the caller supplied is sent: anonymous access is the
// default only when no supplied credential satisfies an alternative. A call
// may still ask for anonymous access, and a client may state which schemes
// it prefers wherever an operation offers an alternative of exactly those.
func Example_credentialsOptionalAuth() {
	ctx := context.Background()
	c, err := openapi.Load(ctx, docURL, &openapi.Options{
		Credentials: map[string]openapi.Credential{
			// Refused on every call to searchPets if PETS_API_KEY is unset,
			// rather than falling back to anonymous access.
			"api_key": openapi.Secret(os.Getenv("PETS_API_KEY")),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	resp, err := c.Call(ctx, "searchPets", nil, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(resp.Security) // {"api_key":[]}

	resp, err = c.Call(ctx, "searchPets", &openapi.Input{Security: "{}"}, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(resp.Security) // {}

	// With an OAuth token too, prefer it wherever an operation offers it.
	both := c.With(func(o *openapi.Options) {
		o.Credentials["oauth"] = openapi.Secret(os.Getenv("PETS_TOKEN"))
		o.Security = []string{"oauth"} // whatever scopes each operation asks for
	})
	if _, err := both.Call(ctx, "searchPets", nil, nil); err != nil {
		log.Fatal(err)
	}
}

// Scenario 5a: a server by URL, one the document need not list.
func Example_environmentsBaseURL() {
	ctx := context.Background()
	staging := client.With(func(o *openapi.Options) {
		o.BaseURL = "https://staging.api.example.com/v1"
	})
	if _, err := staging.Call(ctx, "listPets", nil, nil); err != nil {
		log.Fatal(err)
	}
}

// Scenario 5b: a server the document lists, chosen by its OpenAPI 3.2 name
// or its URL as written, at load or per derived Client.
func Example_environmentsListed() {
	ctx := context.Background()
	op, err := client.Operation("listPets")
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range op.Servers {
		fmt.Println(s.Name, s.URL, s.Description)
	}

	production, err := openapi.Load(ctx, docURL, &openapi.Options{Server: "production"})
	if err != nil {
		log.Fatal(err)
	}
	sandbox := production.With(func(o *openapi.Options) { o.Server = "sandbox" })
	for _, c := range []*openapi.Client{production, sandbox} {
		if _, err := c.Call(ctx, op.Key, nil, nil); err != nil {
			log.Print(err)
		}
	}
}

// Scenario 5c: server variables. A variable left out keeps its declared
// default.
func Example_environmentsVariables() {
	ctx := context.Background()
	op, err := client.Operation("listPets")
	if err != nil {
		log.Fatal(err)
	}
	for _, v := range op.Servers[0].Variables {
		fmt.Println(variablePrompt(v))
	}

	eu := client.With(func(o *openapi.Options) {
		o.Server = "https://{region}.api.example.com/{version}"
		o.Variables["region"] = "eu"
	})
	if _, err := eu.Call(ctx, op.Key, nil, nil); err != nil {
		log.Fatal(err)
	}
}

// variablePrompt describes a server variable for a picker.
func variablePrompt(v openapi.Variable) string {
	switch {
	case !v.Declared:
		return fmt.Sprintf("%s (not declared; any value)", v.Name)
	case !v.DefaultSet:
		return fmt.Sprintf("%s (required): %s", v.Name, v.Description)
	}
	return fmt.Sprintf("%s: one of %q, default %q: %s", v.Name, v.Enum, v.Default, v.Description)
}

// Scenario 6a: upload a file as a multipart part alongside other fields. The
// file is streamed from disk when the request is sent, and read afresh if a
// redirect or a retry sends it again.
func Example_filesUpload() {
	ctx := context.Background()
	f, err := os.Open("q3.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close() // the client never closes it
	thumbnail, err := os.ReadFile("q3.png")
	if err != nil {
		log.Fatal(err)
	}

	_, err = client.Call(ctx, "uploadDocument", &openapi.Input{
		Params: map[string]any{"folderId": "f-1"},
		Body: map[string]any{
			"title": "Q3 report",
			"tags":  []string{"finance", "q3"},
			// A file part with the caller's filename and type.
			"file": openapi.Part{Content: f, MediaType: "application/pdf", Filename: "q3.pdf"},
			// Bytes: a file part named after its property ("thumbnail"),
			// its type from the document's Encoding.
			"thumbnail": thumbnail,
		},
	}, nil)
	if err != nil {
		log.Fatal(err)
	}
}

// Scenario 6b: several files under one field, one part each.
func Example_filesUploadRepeated() {
	ctx := context.Background()
	var attachments []openapi.Part
	for _, name := range []string{"invoice.pdf", "receipt.pdf"} {
		f, err := os.Open(name)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		attachments = append(attachments, openapi.Part{Content: f, MediaType: "application/pdf", Filename: name})
	}

	_, err := client.Call(ctx, "POST /expenses", &openapi.Input{
		Body: map[string]any{
			"note":        "Team dinner",
			"attachments": attachments, // two parts named attachments
		},
	}, nil)
	var re *openapi.RequestError
	if errors.As(err, &re) {
		log.Fatal(re) // names "/attachments/1" if, say, its type is not offered
	}
	if err != nil {
		log.Fatal(err)
	}
}

// Scenario 6c: a multipart body whose parts carry no filename and carry the
// header fields their Encoding declares.
func Example_filesUploadParts() {
	ctx := context.Background()
	manifest := map[string]any{"records": 2, "source": "nightly"}
	payload := []byte("...")

	_, err := client.Call(ctx, "POST /batches", &openapi.Input{
		Body: map[string]any{
			// JSON data in a part of its own; a value has no filename.
			"manifest": openapi.Part{Content: manifest, MediaType: "application/json"},
			// Raw bytes with no filename, and a part header the document
			// declares.
			"payload": openapi.Part{
				Content:    payload,
				NoFilename: true,
				Header:     http.Header{"X-Checksum-Sha256": {"n4bQgYhMfWWaL-qgxVrQFaO_TxsrC4Is0V1sFbDwCgg"}},
			},
		},
	}, nil)
	if err != nil {
		log.Fatal(err)
	}
}

// Scenario 6d: an OpenAPI 3.2 positional multipart body (multipart/mixed
// with prefixEncoding and itemEncoding), sent from a slice, one part per
// element, each typed as its Encoding says unless a Part says otherwise.
func Example_filesPositionalSend() {
	ctx := context.Background()
	f, err := os.Open("q3.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	thumbnail, err := os.ReadFile("q3.png")
	if err != nil {
		log.Fatal(err)
	}

	_, err = client.Call(ctx, "POST /bundles", &openapi.Input{
		Body: []any{
			map[string]any{"title": "Q3 report"},                   // part 0: JSON metadata
			openapi.Part{Content: f, MediaType: "application/pdf"}, // part 1: the document
			thumbnail, // part 2 and on: as itemEncoding says
		},
	}, nil)
	if err != nil {
		log.Fatal(err)
	}
}

// Scenario 6e: an OpenAPI 3.2 positional multipart response, read part by
// part as it arrives, each part streamed to disk.
func Example_filesPositionalRead() {
	ctx := context.Background()
	resp, err := client.Stream(ctx, "GET /bundles/{id}", &openapi.Input{
		Params: map[string]any{"id": "b-1"},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	for part, err := range openapi.Items[*multipart.Part](resp) {
		if err != nil {
			log.Fatal(err)
		}
		if part.FileName() == "" {
			fmt.Println("metadata part:", part.Header.Get("Content-Type"))
			continue
		}
		out, err := os.Create(filepath.Base(part.FileName()))
		if err != nil {
			log.Fatal(err)
		}
		if _, err := io.Copy(out, part); err != nil {
			log.Fatal(err)
		}
		out.Close()
	}
}

// Scenario 6f: download a binary response straight to a file, without
// holding it in memory.
func Example_filesDownload() {
	ctx := context.Background()
	out, err := os.Create("q3.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()

	resp, err := client.Call(ctx, "downloadDocument", &openapi.Input{
		Params: map[string]any{"docId": "d-9"},
	}, out)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(resp.Header.Get("Content-Type"), resp.ContentLength)
}

// Scenario 6g: a body the client can send again, and one it cannot. A
// regular file is read afresh, from where it stood when the call was
// prepared, by a redirect or by retry middleware. A pipe such as standard
// input is read once: its request has no GetBody, so middleware cannot
// retry it, and sending it again is refused.
func Example_filesReplay() {
	ctx := context.Background()
	f, err := os.Open("backup.tar")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	in := &openapi.Input{Params: map[string]any{"id": "b-1"}, Body: f}
	if _, err := client.Call(ctx, "PUT /backups/{id}", in, nil); err != nil {
		log.Fatal(err)
	}

	req, err := client.Prepare("PUT /backups/{id}", &openapi.Input{
		Params: map[string]any{"id": "b-2"},
		Body:   os.Stdin,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(req.HTTP.GetBody == nil) // true: read once
	if _, err := req.Call(ctx, nil); err != nil {
		log.Fatal(err)
	}
	_, err = req.Call(ctx, nil)
	var re *openapi.RequestError
	fmt.Println(errors.As(err, &re)) // true: refused, nothing sent again
}

// Scenario 7: pages the caller drives: the cursor in each result goes back
// as a parameter.
func Example_pagination() {
	ctx := context.Background()
	params := map[string]any{"limit": 100}
	for {
		var page struct {
			Pets       []Pet  `json:"pets"`
			NextCursor string `json:"next_cursor"`
		}
		if _, err := client.Call(ctx, "listPets", &openapi.Input{Params: params}, &page); err != nil {
			log.Fatal(err)
		}
		for _, p := range page.Pets {
			fmt.Println(p.Name)
		}
		if page.NextCursor == "" {
			break
		}
		params["cursor"] = page.NextCursor
	}
}

// labelled is per-API middleware: it labels each request's latency with
// its API, named by the caller, and its operation.
type labelled struct {
	api  string
	next http.RoundTripper
}

func (t labelled) RoundTrip(req *http.Request) (*http.Response, error) {
	label := t.api + " other"
	if op := openapi.OperationFromContext(req.Context()); op != nil {
		label = t.api + " " + op.Key
	}
	start := time.Now()
	resp, err := t.next.RoundTrip(req)
	outcome := "error"
	if err == nil {
		outcome = resp.Status
	}
	record(label, outcome, time.Since(start))
	return resp, err
}

// retry is shared middleware: it retries idempotent requests on a 5xx or a
// transport error, replaying the body only when the client made it
// replayable.
type retry struct{ next http.RoundTripper }

func (t retry) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
	default:
		return t.next.RoundTrip(req)
	}
	replayable := req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
	for attempt := 1; ; attempt++ {
		try := req
		if attempt > 1 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			try = req.Clone(req.Context())
			try.Body = body
		}
		resp, err := t.next.RoundTrip(try)
		if !replayable || attempt == 3 || req.Context().Err() != nil || (err == nil && resp.StatusCode < 500) {
			return resp, err
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
}

// shared is one Transport, and so one connection pool, with retries, for
// every API a service calls.
var shared http.RoundTripper = retry{next: http.DefaultTransport}

// Scenario 8: many Clients share one Transport, each through a small
// http.Client whose RoundTripper labels metrics by API and operation, since
// operation keys repeat across APIs.
func Example_sharedTransport() {
	ctx := context.Background()
	pets, err := openapi.Load(ctx, "https://pets.example.com/openapi.yaml", &openapi.Options{
		HTTPClient: &http.Client{Transport: labelled{api: "pets", next: shared}},
	})
	if err != nil {
		log.Fatal(err)
	}
	stores, err := openapi.Load(ctx, "https://stores.example.com/openapi.yaml", &openapi.Options{
		HTTPClient: &http.Client{Transport: labelled{api: "stores", next: shared}},
	})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := pets.Call(ctx, "getHealth", nil, nil); err != nil {
		log.Print(err)
	}
	if _, err := stores.Call(ctx, "getHealth", nil, nil); err != nil {
		log.Print(err)
	}
}

var errBudget = errors.New("request budget spent")

// Scenario 9a: a per-call deadline. The error matches the context's error
// and carries its cause.
func Example_timeouts() {
	ctx, cancel := context.WithTimeoutCause(context.Background(), 2*time.Second, errBudget)
	defer cancel()

	var pet Pet
	_, err := client.Call(ctx, "getPet", &openapi.Input{
		Params: map[string]any{"petId": "p-7"},
	}, &pet)
	if errors.Is(err, context.DeadlineExceeded) {
		fmt.Println("timed out:", errors.Is(err, errBudget)) // true
	}
}

var (
	errHeaderTimeout = errors.New("no response headers within 5s")
	errShutdown      = errors.New("shutting down")
	shutdown         = make(chan struct{})
)

// Scenario 9b: cancel mid-call and mid-stream, and tell which happened and
// why. The timer bounds only the wait for the response headers; after that
// the stream runs until cancelled.
func Example_cancelStream() {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	timer := time.AfterFunc(5*time.Second, func() { cancel(errHeaderTimeout) })

	resp, err := client.Stream(ctx, "watchPets", nil)
	if !timer.Stop() && err == nil {
		// The timer fired as the headers arrived: the stream is cancelled.
		resp.Body.Close()
		err = context.Cause(ctx)
	}
	if err != nil {
		// Mid-call: no usable response arrived.
		fmt.Println("call:", err, context.Cause(ctx))
		return
	}
	defer resp.Body.Close()
	go func() { <-shutdown; cancel(errShutdown) }()

	for ev, err := range openapi.Events(resp) {
		if errors.Is(err, context.Canceled) {
			// Mid-stream: the events already received stand.
			fmt.Println("stream:", context.Cause(ctx))
			return
		}
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(ev.Event, string(ev.Data))
	}
}

// Scenario 9c: a stream prepared once and resumed after a drop, from the
// last event ID and after the delay the server asked for.
func Example_streamingResume() {
	ctx := context.Background()
	req, err := client.Prepare("watchPets", nil)
	if err != nil {
		log.Fatal(err)
	}

	lastID, wait := "", 3*time.Second
	for attempt := 0; attempt < 5; attempt++ {
		if lastID != "" {
			req.HTTP.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := req.Stream(ctx)
		if err != nil {
			log.Print(err)
			time.Sleep(wait)
			continue
		}
		for ev, err := range openapi.Events(resp) {
			if err != nil {
				break // dropped: resume below
			}
			if ev.IDSet {
				lastID = ev.ID // an empty ID resets it
			}
			if ev.RetrySet {
				wait = ev.Retry
			}
			fmt.Println(ev.Event, string(ev.Data))
		}
		resp.Body.Close()
		time.Sleep(wait)
	}
}

type tenant struct {
	ID      string
	Partner bool // may use the platform's shared partner key
	Token   func(context.Context) (string, error)
	PetIDs  []string
}

var tenants []tenant

// Scenario 10: one loaded Client used from many goroutines, with a Client
// derived per tenant. Deriving copies the maps, so a tenant can add its own
// credential and drop an inherited one without touching the base Client.
func Example_tenants() {
	ctx := context.Background()
	base, err := openapi.Load(ctx, docURL, &openapi.Options{
		HTTPClient: &http.Client{Transport: labelled{api: "pets", next: shared}},
		Credentials: map[string]openapi.Credential{
			"partnerKey": openapi.Secret(os.Getenv("PARTNER_KEY")),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	var wg sync.WaitGroup
	for _, t := range tenants {
		c := base.With(func(o *openapi.Options) {
			o.Variables["tenant"] = t.ID
			o.Credentials["oauth"] = openapi.SecretFunc(t.Token)
			if !t.Partner {
				delete(o.Credentials, "partnerKey")
			}
		})
		for _, id := range t.PetIDs {
			wg.Go(func() {
				var pet Pet
				_, err := c.Call(ctx, "getPet", &openapi.Input{Params: map[string]any{"petId": id}}, &pet)
				if err != nil {
					log.Print(t.ID, ": ", err)
					return
				}
				fmt.Println(t.ID, pet.Name)
			})
		}
	}
	wg.Wait()
}

// outcome classifies how a call ended, for a metric or an alert. The
// package's types come first, since a refusal may wrap a *url.Error from a
// token endpoint and a status or decode error may wrap the context's error.
func outcome(err error) string {
	var (
		re     *openapi.RequestError
		se     *openapi.StatusError
		de     *openapi.DecodeError
		tooBig *http.MaxBytesError
	)
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, openapi.ErrNoOperation):
		return "unknown_operation" // the document changed under us
	case errors.Is(err, errIdentity):
		return "identity_down" // nothing sent
	case errors.As(err, &re):
		return "not_sent"
	case errors.As(err, &se) && se.StatusCode >= 500:
		return "server_error"
	case errors.As(err, &se):
		return "rejected"
	case errors.As(err, &de) && errors.As(err, &tooBig):
		return "reply_too_large" // the server acted
	case errors.As(err, &de):
		return "reply_unreadable" // the server acted
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "transport" // the request may have reached the server
	}
}

// Scenario 10, continued: classify every outcome of production calls.
func Example_classifyFailures() {
	ctx := context.Background()
	start := time.Now()
	_, err := client.Call(ctx, "listPets", nil, nil)
	record("listPets", outcome(err), time.Since(start))
}

// Scenario 11: redirects, in their three modes. By default the client
// follows a 3xx the operation does not declare and returns a declared one;
// FollowAll follows declared ones too, and FollowNone follows none. On a hop
// to another origin, every credential the client added and every
// Options.Header field is removed, while Input.Header fields, here Range, go
// on.
func Example_redirects() {
	ctx := context.Background()
	c := client.With(func(o *openapi.Options) {
		o.Credentials["api_key"] = openapi.Secret(os.Getenv("PETS_API_KEY"))
	})
	in := &openapi.Input{
		Params: map[string]any{"petId": "p-7"},
		Header: http.Header{"Range": {"bytes=0-1023"}},
	}

	// getPetPhoto declares its 303, so by default the 303 is the outcome.
	_, err := c.Call(ctx, "getPetPhoto", in, nil)
	var se *openapi.StatusError
	if errors.As(err, &se) && se.StatusCode == http.StatusSeeOther {
		loc, err := se.Location()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("the photo is at", loc)
	}

	// An app that only wants the bytes follows it.
	var photo []byte
	follow := c.With(func(o *openapi.Options) { o.Redirects = openapi.FollowAll })
	if _, err := follow.Call(ctx, "getPetPhoto", in, &photo); err != nil {
		log.Fatal(err)
	}

	// A proxy that hands every 3xx to its own caller follows none.
	manual := c.With(func(o *openapi.Options) { o.Redirects = openapi.FollowNone })
	_, err = manual.Call(ctx, "getPet", &openapi.Input{Params: map[string]any{"petId": "p-7"}}, nil)
	if errors.As(err, &se) && se.StatusCode/100 == 3 {
		fmt.Println(se.Status, se.Header.Get("Location"))
	}
}

// Scenario 12: bound document size, decoded replies, kept error bodies and
// stream items, and recognize each bound when it is hit.
func Example_bounds() {
	ctx := context.Background()
	var tooBig *http.MaxBytesError

	loader := openapi.Loader{MaxBytes: 8 << 20} // all documents together
	c, err := loader.Load(ctx, docURL, &openapi.Options{
		MaxBodyBytes:  4 << 20,
		MaxErrorBytes: 64 << 10,
		MaxItemBytes:  1 << 20,
	})
	if errors.As(err, &tooBig) {
		log.Fatalf("the documents are over %d bytes", tooBig.Limit)
	}
	if err != nil {
		log.Fatal(err)
	}

	var pets []Pet
	_, err = c.Call(ctx, "listPets", nil, &pets)
	var se *openapi.StatusError
	switch {
	case errors.As(err, &se):
		// Still the status error, whatever happened to its body.
		if errors.As(se.Err, &tooBig) {
			fmt.Printf("%s, body cut to %d bytes\n", se.Status, len(se.Content))
		}
	case errors.As(err, &tooBig):
		fmt.Printf("reply over %d bytes; read it with Stream instead\n", tooBig.Limit)
	case err != nil:
		log.Fatal(err)
	}
}

// Scenario 13a: list operations, and describe one well enough to render a
// form: its parameters and how each is serialized, its body's media types
// and parts, its responses and their headers, its servers and its security.
func Example_describe() {
	for _, op := range client.Operations() {
		fmt.Printf("%-28s %v %s\n", op.Key, op.Tags, op.Summary)
	}

	op, err := client.Operation("uploadDocument")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(op.Method, op.Path, op.Deprecated, op.Source)
	for _, p := range op.Params {
		fmt.Printf("field %s: %s %q, required %t, %s\n", p.Key, p.In, p.Name, p.Required, serialization(p))
	}
	if op.Body != nil {
		for _, m := range op.Body.Media {
			fmt.Println("body as", m.Type, "required", op.Body.Required, "declared at", m.Source)
			for _, e := range m.Encoding {
				fmt.Printf("  part %s as %q with %d declared headers\n", e.Name, e.ContentType, len(e.Headers))
			}
		}
	}
	for _, r := range op.Responses {
		fmt.Println("responds", r.Key, r.Description)
		for _, h := range r.Headers {
			fmt.Printf("  header %s, required %t\n", h.Name, h.Required)
		}
		for _, m := range r.Media {
			fmt.Println("  as", m.Type, "streamed:", m.ItemSchema != nil)
		}
	}
	for _, alt := range op.Security {
		fmt.Println("sign in with", alt.Key)
		for _, s := range alt.Schemes {
			fmt.Printf("  %s (%s %s%s %s) scopes %v, deprecated %t\n", s.Name, s.Type, s.Scheme, s.In, s.ParamName, s.Scopes, s.Deprecated)
			for _, f := range s.Flows {
				fmt.Println("  sign in:", f.Type, f.AuthorizationURL, f.TokenURL, slices.Sorted(maps.Keys(f.Scopes)))
			}
		}
	}
}

// serialization says how a form should expect a parameter to be sent.
func serialization(p *openapi.Param) string {
	switch {
	case p.ContentType != "":
		return "serialized as " + p.ContentType
	case p.CollectionFormat != "":
		return "Swagger 2.0 array, " + p.CollectionFormat
	case p.Style == "deepObject" && !p.ExplodeSet:
		return "deepObject with explode left unwritten"
	}
	return fmt.Sprintf("style %s, explode %t, reserved %t, may be empty %t",
		p.Style, p.Explode, p.AllowReserved, p.AllowEmptyValue)
}

// bodyMedia picks the body media a tool can collect, and the media type to
// send it as. For a range such as */* or application/*, and a Swagger 2.0
// body with no consumes, sendAs is "", so the client's default decides (a
// value goes as application/json). binary reports a type whose content is
// raw bytes, which a tool collects as a base64 string.
func bodyMedia(body *openapi.Message) (m *openapi.Media, sendAs string, binary bool) {
	for _, m := range body.Media {
		if m.Err != nil {
			continue
		}
		if m.Type == "" {
			return m, "", false
		}
		t, _, err := mime.ParseMediaType(m.Type)
		if err != nil {
			continue
		}
		switch {
		case t == "*/*", t == "application/*":
			return m, "", false
		case t == "application/json", strings.HasSuffix(t, "+json"),
			t == "application/x-www-form-urlencoded", t == "multipart/form-data",
			strings.HasPrefix(t, "text/") && t != "text/*":
			return m, m.Type, false
		case !strings.HasSuffix(t, "/*"):
			return m, m.Type, true // application/octet-stream, image/png, ...
		}
	}
	return nil, "", false
}

// toolInput builds the JSON Schema 2020-12 of an AI tool's input for op: its
// parameters under "params", by the exact keys Input.Params takes, each
// with its description, and its body under "body", with the media type to
// send it as. Every schema's
// references point into one shared $defs, so they compose without
// rewriting, and a schema that cannot be rendered costs only its own place:
// it becomes true, accepting any value.
func toolInput(op *openapi.Operation) (schema map[string]any, mediaType string, binary bool) {
	defs := map[string]any{}
	render := func(s *openapi.Schema) any {
		b, err := json.Marshal(s)
		if err != nil {
			log.Printf("%s: %v", op.Key, err)
			return true
		}
		return json.RawMessage(b)
	}
	withDescription := func(s *openapi.Schema, description string) any {
		var out any = true // no schema declared: any value
		if s != nil {
			out = render(s)
			for key, d := range s.Defs() {
				if _, done := defs[key]; !done {
					defs[key] = render(d)
				}
			}
		}
		if description == "" {
			return out
		}
		return map[string]any{"description": description, "allOf": []any{out}}
	}

	params := map[string]any{}
	required := []string{}
	for _, p := range op.Params {
		if p.Err != nil {
			continue // cannot be supplied; the rest of the operation works
		}
		params[p.Key] = withDescription(p.Schema, p.Description)
		if p.Required {
			required = append(required, p.Key)
		}
	}
	props := map[string]any{
		"params": map[string]any{"type": "object", "properties": params, "required": required, "additionalProperties": false},
	}
	top := []string{"params"}
	if op.Body != nil {
		if m, sendAs, bin := bodyMedia(op.Body); m != nil {
			if bin {
				props["body"] = map[string]any{"type": "string", "contentEncoding": "base64", "description": op.Body.Description}
			} else {
				props["body"] = withDescription(m.Schema, op.Body.Description)
			}
			if op.Body.Required {
				top = append(top, "body")
			}
			mediaType, binary = sendAs, bin
		}
	}
	return map[string]any{
		"$schema":    "https://json-schema.org/draft/2020-12/schema",
		"type":       "object",
		"properties": props,
		"required":   top,
		"$defs":      defs,
	}, mediaType, binary
}

// Scenario 13b: a JSON Schema 2020-12 tool definition for every callable
// operation, whatever the document's edition.
func Example_toolSchema() {
	for _, op := range client.Operations() {
		if op.Err != nil || op.Deprecated {
			continue
		}
		input, _, _ := toolInput(op)
		schema, err := json.Marshal(input)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("tool %q: %s\n%s\n", op.Key, op.Summary, schema)
	}
}

// Scenario 14: a call from a model's JSON arguments, for the tool built in
// Example_toolSchema, with a dynamic JSON result. Numbers stay exact both
// ways, and input mistakes go back to the model by parameter.
func Example_dynamicCall() {
	ctx := context.Background()
	args := `{"params": {"petId": "p-7", "revision": 9007199254740993},
	          "body": {"name": "Rex", "weight": 12.50}}`

	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber() // 9007199254740993 stays exact, and 12.50 is sent as written
	dec.DisallowUnknownFields()
	var in struct {
		Params map[string]any `json:"params"`
		Body   any            `json:"body"`
	}
	if err := dec.Decode(&in); err != nil {
		log.Fatal(err)
	}

	op, err := client.Operation("updatePet")
	if err != nil {
		log.Fatal(err)
	}
	_, mediaType, binary := toolInput(op) // how the tool's body was described
	if in.Body == nil {
		mediaType = ""
	}
	if s, ok := in.Body.(string); ok && binary {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			log.Fatal(err)
		}
		in.Body = b // sent as the raw bytes the operation takes
	}

	var out any // map[string]any, []any, string, bool, nil and json.Number
	_, err = client.Call(ctx, op.Key, &openapi.Input{Params: in.Params, Body: in.Body, MediaType: mediaType}, &out)
	var (
		re *openapi.RequestError
		se *openapi.StatusError
	)
	switch {
	case errors.As(err, &re) && len(re.Inputs) > 0:
		for name, err := range re.Inputs {
			fmt.Printf("fix %q: %v\n", name, err) // "" is the body
		}
		return
	case errors.As(err, &se):
		if err := se.Decode(&out); err != nil { // the error body, as dynamic JSON
			log.Fatal(err)
		}
	case err != nil:
		log.Fatal(err)
	}
	result, err := json.Marshal(out) // json.Number values are written as received
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(result))
}

// schemeNamed finds the declaration of scheme name among op's alternatives.
func schemeNamed(op *openapi.Operation, name string) (openapi.SecurityScheme, bool) {
	for _, alt := range op.Security {
		for _, s := range alt.Schemes {
			if s.Name == name {
				return s, true
			}
		}
	}
	return openapi.SecurityScheme{}, false
}

// signInPrompt tells a user what secret a scheme wants and where to get it.
func signInPrompt(s openapi.SecurityScheme) string {
	switch s.Type {
	case "apiKey":
		return fmt.Sprintf("%s: an API key, sent in %s %s", s.Name, s.In, s.ParamName)
	case "http":
		return fmt.Sprintf("%s: HTTP %s credentials %s", s.Name, s.Scheme, s.BearerFormat)
	case "openIdConnect":
		return fmt.Sprintf("%s: a token from %s", s.Name, s.OpenIDConnectURL)
	case "oauth2":
		var where []string
		for _, f := range s.Flows {
			for _, u := range []string{f.AuthorizationURL, f.DeviceAuthorizationURL, f.TokenURL, f.RefreshURL} {
				if u != "" {
					where = append(where, f.Type+" "+u)
				}
			}
		}
		if s.OAuth2MetadataURL != "" {
			where = append(where, "metadata "+s.OAuth2MetadataURL)
		}
		return fmt.Sprintf("%s: an OAuth token with scopes %v, from %s", s.Name, s.Scopes, strings.Join(where, ", "))
	}
	return s.Name + ": " + s.Description
}

// setPartType sets the media type of the part a Choice's JSON Pointer names
// in body: a property ("/file") or an item of one ("/files/1").
func setPartType(body map[string]any, pointer, mediaType string) {
	var tokens []string
	for _, t := range strings.Split(pointer, "/")[1:] {
		tokens = append(tokens, strings.NewReplacer("~1", "/", "~0", "~").Replace(t))
	}
	part := func(v any) openapi.Part {
		p, ok := v.(openapi.Part)
		if !ok {
			p = openapi.Part{Content: v}
		}
		p.MediaType = mediaType
		return p
	}
	switch len(tokens) {
	case 1:
		body[tokens[0]] = part(body[tokens[0]])
	case 2:
		items, _ := body[tokens[0]].([]any)
		if i, err := strconv.Atoi(tokens[1]); err == nil && i < len(items) {
			items[i] = part(items[i])
		}
	}
}

// Scenario 15: before calling, find out what the caller still has to choose
// or supply, present it, supply it, then show the call and send it. The
// recipe covers a map body whose files are values or []any items; a
// positional body, or a typed slice of Parts, needs setPartType extended.
func Example_whatIsMissing() {
	ctx := context.Background()
	c, err := openapi.Parse(ctx, reportsYAML, "", nil) // no URI: its relative server needs a URL
	if err != nil {
		log.Fatal(err)
	}
	op, err := c.Operation("uploadReport")
	if err != nil {
		log.Fatal(err)
	}
	// The file makes multipart/form-data the body's type; the file's own
	// type is one of two the document offers, so the call asks.
	body := map[string]any{"title": "Q3", "file": []byte("...")}
	in := &openapi.Input{Body: body}

	for range 10 { // each round answers every open question; a few suffice
		req, err := c.Prepare(op.Key, in)
		var re *openapi.RequestError
		if !errors.As(err, &re) || len(re.Choices) == 0 || len(re.Inputs) > 0 || re.Err != nil {
			if err != nil {
				log.Fatal(err) // not something a choice fixes
			}
			// Show the user what will be sent, and what was chosen for them.
			fmt.Println(req.HTTP.Method, req.HTTP.URL)
			for _, ch := range req.Choices {
				fmt.Printf("%s %s = %q of %q (by default: %t)\n", ch.Kind, ch.Name, ch.Value, ch.Offered, ch.Default)
			}
			if _, err := req.Call(ctx, nil); err != nil {
				log.Fatal(err)
			}
			return
		}

		for _, ch := range re.Choices {
			switch ch.Kind {
			case openapi.ServerChoice:
				server := ask("Server URL", ch.Offered)
				c = c.With(func(o *openapi.Options) {
					if slices.Contains(ch.Offered, server) {
						o.Server = server
					} else {
						o.BaseURL = server // nil Offered: only an absolute URL will do
					}
				})
			case openapi.VariableChoice:
				value := ask("Value for "+ch.Name, ch.Offered)
				c = c.With(func(o *openapi.Options) { o.Variables[ch.Name] = value })
			case openapi.SecurityChoice:
				// Each offer is an alternative's key, such as {"api_key":[]}.
				in.Security = ch.Offered[choose("Sign in with", ch.Offered)]
			case openapi.CredentialChoice:
				s, _ := schemeNamed(op, ch.Name)
				secret := ask(signInPrompt(s), nil)
				c = c.With(func(o *openapi.Options) { o.Credentials[ch.Name] = openapi.Secret(secret) })
			case openapi.MediaTypeChoice:
				mediaType := ask("Send "+ch.Name+" as", ch.Offered)
				if ch.Name == "" {
					in.MediaType = mediaType
				} else {
					setPartType(body, ch.Name, mediaType)
				}
			}
		}
	}
	log.Fatal("still refused after 10 rounds of answers")
}

// sign stands in for a partner's request-signing scheme, which the document
// cannot express.
func sign(method, path string, body []byte) string { return "" }

// Scenario 15, continued: see a call exactly as it will be sent, change what
// the document cannot express, and send exactly that. Preparing called no
// credential source; credentials are added when the request is sent.
func Example_prepare() {
	ctx := context.Background()
	// A field edited into the request follows a redirect to another origin,
	// so a signed call follows no redirect at all.
	signed := client.With(func(o *openapi.Options) { o.Redirects = openapi.FollowNone })
	req, err := signed.Prepare("createOrder", &openapi.Input{
		Body: map[string]any{"sku": "A-1", "quantity": 2},
	})
	if err != nil {
		log.Fatal(err)
	}

	body, err := req.HTTP.GetBody() // a fresh copy of the encoded body
	if err != nil {
		log.Fatal(err)
	}
	encoded, err := io.ReadAll(body)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s %s (declared as %s)\n%s\n", req.HTTP.Method, req.HTTP.URL, req.MediaType, encoded)
	req.HTTP.Header.Set("X-Signature", sign(req.HTTP.Method, req.HTTP.URL.Path, encoded))

	var o order
	if _, err := req.Call(ctx, &o); err != nil {
		log.Fatal(err)
	}
	fmt.Println(o.ID)
}

var reportsYAML = []byte(`openapi: 3.2.0
info: {title: Reports, version: "1"}
servers: [{url: /v1}]
paths:
  /reports:
    post:
      operationId: uploadReport
      security: [{oauth: [reports:write]}, {api_key: []}]
      requestBody:
        content:
          multipart/form-data:
            encoding: {file: {contentType: "application/pdf, text/csv"}}
          application/json: {}
      responses:
        "201": {description: Created}
components:
  securitySchemes:
    oauth: {type: oauth2, flows: {clientCredentials: {tokenUrl: /token, scopes: {reports:write: ""}}}}
    api_key: {type: apiKey, in: header, name: X-API-Key}
`)

// Scenario 16a: server-sent events as they arrive, each event's data decoded
// as JSON, stopping early.
func Example_streamingEvents() {
	ctx := context.Background()
	resp, err := client.Stream(ctx, "complete", &openapi.Input{
		Body: map[string]any{"prompt": "Name a dog", "stream": true},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	for ev, err := range openapi.Events(resp) {
		if err != nil {
			log.Fatal(err)
		}
		if string(ev.Data) == "[DONE]" {
			break // closes the body, without waiting for the server
		}
		var chunk struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(ev.Data, &chunk); err != nil {
			log.Fatal(err)
		}
		fmt.Print(chunk.Text)
	}
}

// Scenario 16b: JSON Lines, each item decoded into the caller's type as it
// arrives. A bad line is reported and skipped; any other error means the
// export was cut off, not finished.
func Example_streamingJSONLines() {
	ctx := context.Background()
	resp, err := client.Stream(ctx, "exportPets", nil)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	n := 0
	for pet, err := range openapi.Items[Pet](resp) {
		if errors.Is(err, openapi.ErrItem) {
			log.Printf("skipping a bad line after %d pets: %v", n, err)
			continue
		}
		if err != nil {
			log.Fatalf("export cut off after %d pets: %v", n, err)
		}
		fmt.Println(pet.Name)
		n++
	}
}

// Scenario 16c: any sequential response as dynamic JSON items, for a tool
// that forwards them: JSON Lines values, or server-sent events as the
// objects OpenAPI 3.2 defines, holding only the fields each event set.
func Example_streamingDynamic() {
	ctx := context.Background()
	resp, err := client.Stream(ctx, "watchPets", nil)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	enc := json.NewEncoder(os.Stdout)
	for item, err := range openapi.Items[any](resp) {
		if errors.Is(err, openapi.ErrItem) {
			continue
		}
		if err != nil {
			log.Fatal(err)
		}
		if err := enc.Encode(item); err != nil {
			log.Fatal(err)
		}
	}
}

// Scenario 16d: the raw stream, framed by the caller.
func Example_streamingRaw() {
	ctx := context.Background()
	resp, err := client.Stream(ctx, "tailLog", nil)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	fmt.Println(resp.StatusCode, resp.Header.Get("Content-Type"))
	sc := bufio.NewScanner(resp.Body)
	sc.Split(bufio.ScanWords)
	for sc.Scan() {
		fmt.Println(sc.Text())
	}
	if err := sc.Err(); err != nil {
		log.Fatal(err)
	}
}

// db stands in for the service's database.
var db *sql.DB

// petRows yields the pets a query returns, and reports a failed cursor as
// an error rather than ending early as though the export were complete.
func petRows(rows *sql.Rows) iter.Seq2[Pet, error] {
	return func(yield func(Pet, error) bool) {
		defer rows.Close()
		for rows.Next() {
			var p Pet
			if err := rows.Scan(&p.ID, &p.Name, &p.Tag); err != nil {
				yield(Pet{}, err)
				return
			}
			if !yield(p, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Pet{}, err)
		}
	}
}

// Scenario 16e: a large JSON Lines upload from a database cursor. Each pet
// is written as it is yielded, so the upload is never held in memory; if
// the cursor fails, the upload is aborted, so the server never receives a
// complete body, and the call returns the cursor's error.
func Example_streamingUpload() {
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, "SELECT id, name, tag FROM pets")
	if err != nil {
		log.Fatal(err)
	}
	_, err = client.Call(ctx, "importPets", &openapi.Input{Body: petRows(rows)}, nil)
	if err != nil {
		log.Fatal(err) // errors.Is matches the cursor's error, if that was the cause
	}
}

// Scenario 17: a document with defects. It loads, the rest works, and each
// defect is reported on the smallest part it disables. This legacy API also
// wants an empty flag sent as ?flag rather than ?flag=.
func Example_sloppyDocument() {
	ctx := context.Background()
	c, err := openapi.Load(ctx, "https://legacy.example.com/swagger.yaml", nil)
	if err != nil {
		log.Fatal(err) // only a document unusable as a whole fails here
	}
	for _, op := range c.Operations() {
		if op.Key == "" {
			// Not an operation: a Paths entry that could not be read, or an
			// additional operation spelled like a fixed method.
			fmt.Printf("%s: %v\n", op.Path, op.Err)
			continue
		}
		if op.Err != nil {
			fmt.Printf("%s cannot be called: %v\n", op.Key, op.Err)
			continue
		}
		for _, p := range op.Params {
			if p.Err != nil {
				fmt.Printf("%s: parameter %s cannot be used: %v\n", op.Key, p.Key, p.Err)
			}
		}
		for _, r := range op.Responses {
			for _, m := range r.Media {
				if m.Err != nil {
					fmt.Printf("%s: %s %s is unreadable: %v\n", op.Key, r.Key, m.Type, m.Err)
				}
			}
		}
		for _, s := range op.Servers {
			if s.Err != nil {
				fmt.Printf("%s: server %s cannot be used: %v\n", op.Key, s.URL, s.Err)
			}
		}
	}

	const key = "GET /reports/{id}"
	op, err := c.Operation(key)
	if err != nil {
		log.Fatal(err)
	}
	// Call with the key used to find it: a defect entry's own Key is empty.
	_, err = c.Call(ctx, key, &openapi.Input{Params: map[string]any{"id": "r-1"}}, nil)
	fmt.Println(errors.Is(err, op.Err)) // true when op.Err is set: refused, nothing sent

	legacy := c.With(func(o *openapi.Options) { o.NameOnlyEmpty = true })
	var pets []Pet
	if _, err := legacy.Call(ctx, "listPets", &openapi.Input{
		Params: map[string]any{"includeDeleted": ""},
	}, &pets); err != nil {
		log.Fatal(err)
	}
}

// Outcome is how a wrapper library reports a response it classified itself.
type Outcome struct {
	Status    int
	Declared  string // the responses key that governs Status, or ""
	MediaType string // the declared media type the body matched, or ""
	Body      []byte
	BodyErr   error // why Body is incomplete, or nil
}

// invoke is a library's one entry point over any document. It sends a call
// exactly as its own caller specified: it refuses any decision the client
// would make by default, and classifies and decodes responses itself.
func invoke(ctx context.Context, c *openapi.Client, key string, in *openapi.Input) (*Outcome, error) {
	req, err := c.Prepare(key, in)
	if err != nil {
		return nil, err // a *openapi.RequestError, presentable as it is
	}
	var defaulted []openapi.Choice
	for _, ch := range req.Choices {
		if ch.Default {
			defaulted = append(defaulted, ch)
		}
	}
	if len(defaulted) > 0 {
		// Its text names each decision and the field that makes it.
		return nil, &openapi.RequestError{Choices: defaulted}
	}

	var raw []byte
	resp, err := req.Call(ctx, &raw)
	var se *openapi.StatusError
	switch {
	case errors.As(err, &se):
		return &Outcome{se.StatusCode, se.Declared, se.MediaType, se.Content, se.Err}, nil
	case err != nil:
		return nil, err
	}
	return &Outcome{resp.StatusCode, resp.Declared, resp.MediaType, raw, nil}, nil
}

type report struct {
	XMLName xml.Name `xml:"report"`
	Title   string   `xml:"title"`
}

// reportsWrite is the key of the security alternative the library's caller
// pinned, copied from Operation.Security.
const reportsWrite = `{"oauth":["reports:write"]}`

// Scenario 18a: a library pins every open decision, gets a refusal it can
// present when one is missing or left to a default, and classifies and
// decodes custom media itself.
func Example_exactBehavior() {
	ctx := context.Background()
	pinned := client.With(func(o *openapi.Options) {
		o.Redirects = openapi.FollowUndeclared // every mode pinned by name
		o.BaseURL = "https://api.example.com/v2"
		o.Credentials = map[string]openapi.Credential{"oauth": openapi.Secret(os.Getenv("TOKEN"))}
	})
	out, err := invoke(ctx, pinned, "PUT /reports/{id}", &openapi.Input{
		Params:    map[string]any{"id": "r-1", "revision": "7"},
		Body:      []byte("<report><title>Q3</title></report>"),
		MediaType: "application/xml",
		Security:  reportsWrite,
		Header:    http.Header{"Accept": {"application/xml, application/problem+json"}},
	})
	if err != nil {
		// For example: security alternative left to the default
		// {"oauth":["reports:write"]} (set Input.Security).
		log.Fatal(err)
	}

	switch {
	case out.Declared == "":
		fmt.Printf("status %d is not declared\n", out.Status)
	case out.Status/100 != 2:
		fmt.Printf("declared failure %s (%d): %s (complete: %t)\n", out.Declared, out.Status, out.Body, out.BodyErr == nil)
	case out.MediaType == "application/xml":
		var r report
		if err := xml.Unmarshal(out.Body, &r); err != nil {
			log.Fatal(err)
		}
		fmt.Println(r.Title)
	}
}

// validator stands in for a JSON Schema validator a library brings, which
// reads documents by URI and compiles a schema by its URI.
type validator interface {
	AddDocument(uri string, content []byte)
	Compile(uri, dialect string) error
}

var schemas validator

// Scenario 18b: the schemas as the document writes them, in its own
// dialect: a keyword the library checks itself, and the library's own
// validator compiling each response schema where it is written. Each
// document is copied out once.
func Example_schemasAsWritten() {
	op, err := client.Operation("getPetPhoto")
	if err != nil {
		log.Fatal(err)
	}
	added := map[string]bool{}
	for _, r := range op.Responses {
		for _, m := range r.Media {
			if m.Schema == nil {
				continue
			}
			// OpenAPI 3.0 marks raw bytes with format: binary; a $ref in Raw
			// resolves against Base.
			var kw struct {
				Format string `json:"format"`
				Ref    string `json:"$ref"`
			}
			if err := json.Unmarshal(m.Schema.Raw(), &kw); err != nil {
				log.Fatal(err)
			}
			raw := strings.HasPrefix(client.Version(), "3.0") && kw.Format == "binary"
			fmt.Println(r.Key, m.Type, "raw bytes:", raw, "ref:", kw.Ref, "against", m.Schema.Base())

			src := m.Schema.Source() // "https://api.example.com/openapi.json#/components/schemas/Photo"
			doc, _, _ := strings.Cut(src, "#")
			if !added[doc] {
				schemas.AddDocument(doc, client.Document(doc))
				added[doc] = true
			}
			if err := schemas.Compile(src, m.Schema.Dialect()); err != nil {
				log.Fatal(err)
			}
		}
	}
}

// specs stands in for documents a library ships with, by URI, such as a
// split description embedded with go:embed.
var specs map[string][]byte

// splitSchemes stands in for the library's knowledge of a partner whose
// split documents each declare the security schemes they use.
var splitSchemes, lenientPartner bool

// Scenario 18c: a library loads documents exactly as it chooses: only from
// its own copies, each at the URI it was published at; references allowed
// to reach one more origin, where the partner keeps shared schemas; and
// security scheme names looked up in exactly one document, with no
// fallback: the entry document, as OpenAPI recommends, or, for a partner
// that writes them beside each requirement, that document.
func Example_loadExactly() {
	ctx := context.Background()
	lookup := openapi.SchemesInEntry
	switch {
	case splitSchemes:
		lookup = openapi.SchemesInReferrer
	case lenientPartner:
		lookup = openapi.SchemesInEntryFirst // the default, pinned by name
	}
	loader := openapi.Loader{
		Fetch: func(ctx context.Context, uri string) (io.ReadCloser, string, error) {
			content, ok := specs[uri]
			if !ok {
				return nil, "", fmt.Errorf("%s is not shipped with this library", uri)
			}
			return io.NopCloser(bytes.NewReader(content)), uri, nil
		},
		Origins:      []string{"https://schemas.partner.example.com"},
		SchemeLookup: lookup,
	}
	c, err := loader.Load(ctx, "https://partner.example.com/openapi/root.yaml", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(c.Version(), len(c.Operations()))
}

type order struct {
	ID string `json:"id"`
}

// A 2xx that fails to decode: the server acted, so the call must not be
// retried blindly, and the first few KiB of the body are kept for the log.
func Example_decodeFailure() {
	ctx := context.Background()
	var o order
	resp, err := client.Call(ctx, "createOrder", &openapi.Input{
		Body: map[string]any{"sku": "A-1", "quantity": 2},
	}, &o)
	var de *openapi.DecodeError
	if errors.As(err, &de) {
		fmt.Println("created:", de.StatusCode, de.Header.Get("Location"), de.MediaType)
		switch {
		case errors.Is(err, io.EOF):
			fmt.Println("but the reply had no content")
		default:
			fmt.Printf("but the reply did not decode (%v); it began %.200q\n", de.Err, de.Content)
		}
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(resp.StatusCode, o.ID)
}

// Scenario 19: the package's conventions. Zero values are safe, zero Options
// are the defaults, With composes and can reset, errors work with errors.Is
// and errors.As, a mistaken out is caught before sending, and credentials
// never print.
func Example_package() {
	ctx := context.Background()

	var zero openapi.Client
	fmt.Println(len(zero.Operations())) // 0
	_, err := zero.Call(ctx, "getPet", nil, nil)
	fmt.Println(errors.Is(err, openapi.ErrNoOperation)) // true: refused, nothing sent

	c, err := openapi.Load(ctx, docURL, &openapi.Options{})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := c.Operation("getPets"); errors.Is(err, openapi.ErrNoOperation) {
		fmt.Println(err) // names the keys it might have meant
	}

	var pet Pet
	_, err = c.Call(ctx, "getPet", &openapi.Input{Params: map[string]any{"petId": "p-7"}}, pet)
	var re *openapi.RequestError
	fmt.Println(errors.As(err, &re)) // true: pet is not a pointer, so nothing was sent

	tenant := c.With(func(o *openapi.Options) {
		o.Credentials["api_key"] = openapi.Secret("k-123")
		o.Server = "sandbox"
	})
	anonymous := tenant.With(func(o *openapi.Options) {
		clear(o.Credentials) // drop every inherited credential
		o.Server = ""        // back to the document's first server
	})

	req, err := anonymous.Prepare("getPet", &openapi.Input{Params: map[string]any{"petID": "p-7"}})
	if errors.As(err, &re) {
		for name := range re.Inputs {
			fmt.Println("no parameter", name) // the document spells it petId
		}
	} else if err == nil {
		var buf bytes.Buffer
		if err := req.HTTP.Write(&buf); err != nil { // holds no credentials
			log.Fatal(err)
		}
		fmt.Print(buf.String())
	}

	fmt.Println(openapi.Secret("hunter2")) // prints no secret
}
