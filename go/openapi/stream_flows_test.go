package openapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Runnable versions of example_test.go scenarios 16a–16d. Server flushing
// plus a client-acknowledged first item proves incremental delivery, not
// merely eventual decoding of a buffered response. Each real HTTP response
// is closed on break, as callers in the examples require.
func TestStreamIncrementalExampleFlows(t *testing.T) {
	for _, family := range []string{"events", "jsonl", "dynamic", "raw"} {
		t.Run(family, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			first := make(chan struct{})
			var once sync.Once
			ack := func() { once.Do(func() { close(first) }) }
			defer ack()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch family {
				case "events", "dynamic":
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"text\":\"Rex\"}\nid: 1\n\n")
				case "jsonl":
					w.Header().Set("Content-Type", "application/jsonl")
					io.WriteString(w, "{\"name\":\"Rex\"}\n")
				case "raw":
					w.Header().Set("Content-Type", "text/plain")
					io.WriteString(w, "Rex\n")
				}
				w.(http.Flusher).Flush()
				select {
				case <-first:
				case <-r.Context().Done():
					return
				}
				switch family {
				case "events", "dynamic":
					io.WriteString(w, "data: [DONE]\n\n")
				case "jsonl":
					io.WriteString(w, "{bad}\n{\"name\":\"Fido\"}\n")
				case "raw":
					io.WriteString(w, "Fido\n")
				}
			}))
			defer func() { ack(); cancel(); srv.Close() }()
			c := parseAt(t, streamDoc("3.2.1"), srv.URL, srv.URL+"/openapi.json", &openapi.Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
			r, err := c.Stream(ctx, "get", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			switch family {
			case "events":
				var text string
				for ev, e := range openapi.Events(r) {
					if e != nil {
						t.Fatal(e)
					}
					if string(ev.Data) == "[DONE]" {
						break
					}
					var chunk struct {
						Text string `json:"text"`
					}
					if err := json.Unmarshal(ev.Data, &chunk); err != nil {
						t.Fatal(err)
					}
					text += chunk.Text
					ack()
				}
				if text != "Rex" {
					t.Fatal(text)
				}
			case "jsonl":
				var names []string
				bad := 0
				for pet, e := range openapi.Items[Pet](r) {
					if errors.Is(e, openapi.ErrItem) {
						bad++
						continue
					}
					if e != nil {
						t.Fatal(e)
					}
					names = append(names, pet.Name)
					ack()
				}
				if !reflect.DeepEqual(names, []string{"Rex", "Fido"}) || bad != 1 {
					t.Fatalf("%v bad %d", names, bad)
				}
			case "dynamic":
				var got []any
				for v, e := range openapi.Items[any](r) {
					if e != nil {
						t.Fatal(e)
					}
					got = append(got, v)
					ack()
				}
				want := []any{map[string]any{"data": `{"text":"Rex"}`, "id": "1"}, map[string]any{"data": "[DONE]"}}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%#v", got)
				}
			case "raw":
				sc := bufio.NewScanner(r.Body)
				sc.Split(bufio.ScanWords)
				var words []string
				for sc.Scan() {
					words = append(words, sc.Text())
					ack()
				}
				if sc.Err() != nil || strings.Join(words, " ") != "Rex Fido" {
					t.Fatalf("%v %v", words, sc.Err())
				}
			}
		})
	}
}
