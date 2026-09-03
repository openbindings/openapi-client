package openapiclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// An authored `anyOf: [{}, {not: {}}]` at a form property or multipart part
// is a choice with two candidates under §5.2 of the 3.x binding
// specifications: a choice skips only a branch whose resolved declaration
// declares only `null`, `not` never participates in resolution, so
// `{not: {}}` is typeless and is a candidate beside `{}`, and the choice
// supplies a single resolved member declaration only when exactly one
// candidate remains. No single member means no Encoding default row (§9.3)
// and no part carriage, so a value supplied for that member refuses before
// dispatch as the plain species -- exactly as `oneOf: [{type: string},
// {type: integer}]` does. Until 2026-09-02 these engines read the structure
// as a literal `true`, because the loader encodes a literal boolean Schema
// Object in that shape; the loader now marks its own encoding.
//
// Every cell runs through the SHIPPED path: a whole document handed to the
// loader, not a hand-built typed model.

func ambiguousChoiceDocument(edition, media, part, server string) string {
	var schema string
	if media == "text/plain" {
		schema = part
	} else {
		schema = `{"type":"object","properties":{"ok":{"type":"string"},"choice":` + part + `}}`
	}
	return `{"openapi":"` + edition + `","info":{"title":"t","version":"1"},"servers":[{"url":"` + server + `"}],"paths":{"/up":{"post":{"requestBody":{"required":true,"content":{"` + media + `":{"schema":` + schema + `}}},"responses":{"204":{"description":"ok"}}}}}}`
}

// invokeThroughLoader prepares, starts, sends one input and drains the
// execution. It reports whether any request reached the peer, the part or
// field bytes it carried, and the first error the engine raised.
func invokeThroughLoader(t *testing.T, document func(server string) string, input map[string]any) (dispatched bool, body string, contentType string, err error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatched = true
		contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	prepared, err := NewEngine(srv.Client()).Prepare(context.Background(), PrepareOptions{
		Source:  Source{Content: []byte(document(srv.URL))},
		Ref:     "#/paths/~1up/post",
		Profile: FullProfile(),
	})
	if err != nil {
		return dispatched, body, contentType, fmt.Errorf("prepare: %w", err)
	}
	exec, err := prepared.Start(context.Background())
	if err != nil {
		return dispatched, body, contentType, fmt.Errorf("start: %w", err)
	}
	if err := exec.Send(context.Background(), input); err != nil {
		return dispatched, body, contentType, fmt.Errorf("send: %w", err)
	}
	exec.FinishInput()
	for range exec.Events() {
	}
	return dispatched, body, contentType, exec.Wait()
}

func TestAmbiguousChoiceMemberRefusesBeforeDispatch(t *testing.T) {
	const ambiguous = `{"anyOf":[{},{"not":{}}]}`
	for _, edition := range []string{"3.0.4", "3.1.2", "3.2.0"} {
		for _, media := range []string{"multipart/form-data", "application/x-www-form-urlencoded", "text/plain"} {
			t.Run(edition+" "+media, func(t *testing.T) {
				// The standalone engine's caller value is flat: form members
				// ride at the top level and a non-object body under `body`.
				input := map[string]any{"ok": "fine", "choice": "eA=="}
				if media == "text/plain" {
					input = map[string]any{"body": "eA=="}
				}
				dispatched, body, _, err := invokeThroughLoader(t, func(server string) string {
					return ambiguousChoiceDocument(edition, media, ambiguous, server)
				}, input)
				if err == nil {
					t.Fatalf("an ambiguous choice member admitted a supplied value; wire body %q", body)
				}
				if dispatched {
					t.Fatalf("an ambiguous choice member reached the wire: %q (%v)", body, err)
				}
				if media != "text/plain" && !strings.Contains(err.Error(), "choice applicator") {
					// The route is §5.2's: the refusal names the choice that
					// collapses to no single member, not a typeless part.
					t.Fatalf("refusal route = %v, want the §5.2 choice rule", err)
				}
				if media == "text/plain" {
					// A text/plain body is not a part or property: its lane is
					// selected by the declaration under §9.2 (the 2026-08-15
					// string-carriage ruling), and a choice that resolves to no
					// member selects none. Refusal without dispatch is the pin.
					return
				}
				// And it is the route every two-candidate choice takes: the
				// engine says exactly what it says of `oneOf: [{type: string},
				// {type: integer}]`, which the shared union-type table already
				// pins as refused.
				typedDispatched, typedBody, _, typedErr := invokeThroughLoader(t, func(server string) string {
					return ambiguousChoiceDocument(edition, media, `{"oneOf":[{"type":"string"},{"type":"integer"}]}`, server)
				}, input)
				if typedErr == nil || typedDispatched {
					t.Fatalf("typed two-branch choice: dispatched=%v body=%q err=%v", typedDispatched, typedBody, typedErr)
				}
				if got, want := stripServerURL(err.Error()), stripServerURL(typedErr.Error()); got != want {
					t.Fatalf("refusal route differs from the typed two-branch choice:\n  anyOf[{}, {not: {}}]: %s\n  oneOf[string, integer]: %s", got, want)
				}
			})
		}
	}
}

// The literal `true` written in a source is still the always-true schema: the
// loader's marked encoding survives normalization and the 3.1 multipart part
// takes the typeless application/octet-stream default with the canonical
// Base64 boundary (§9.3). This is the cell the §5.2 reading must not move.
func TestLiteralTruePartStillTakesTheTypelessOctetLane(t *testing.T) {
	dispatched, body, contentType, err := invokeThroughLoader(t, func(server string) string {
		return ambiguousChoiceDocument("3.1.2", "multipart/form-data", `true`, server)
	}, map[string]any{"ok": "fine", "choice": "eA=="})
	if err != nil || !dispatched {
		t.Fatalf("literal true part: dispatched=%v err=%v", dispatched, err)
	}
	if !strings.HasPrefix(contentType, "multipart/form-data") || !strings.Contains(body, "Content-Type: application/octet-stream") || !strings.Contains(body, "\r\n\r\nx\r\n") {
		t.Fatalf("literal true part carriage = %q %q", contentType, body)
	}
}

func TestStructuralBooleanSchemaLiteralReadsOnlyTheLoaderEncoding(t *testing.T) {
	structure := func(keyword string) map[string]any {
		return map[string]any{keyword: []any{map[string]any{}, map[string]any{"not": map[string]any{}}}}
	}
	marked := func(keyword string, literal bool) map[string]any {
		m := structure(keyword)
		m[liftedBooleanLiteralMarker] = literal
		return m
	}
	cases := []struct {
		name    string
		schema  map[string]any
		literal bool
		boolean bool
	}{
		{"authored anyOf is a two-candidate choice, not true", structure("anyOf"), false, false},
		{"authored allOf is still read as false (recorded residue)", structure("allOf"), false, true},
		{"lifted true", marked("anyOf", true), true, true},
		{"lifted false", marked("allOf", false), false, true},
		{"marker contradicting the shape is not a literal", marked("anyOf", false), false, false},
		{"marker beside a foreign keyword is not a literal", func() map[string]any {
			m := marked("anyOf", true)
			m["description"] = "x"
			return m
		}(), false, false},
	}
	for _, c := range cases {
		literal, boolean := structuralBooleanSchemaLiteral(c.schema)
		if literal != c.literal || boolean != c.boolean {
			t.Errorf("%s: structuralBooleanSchemaLiteral = (%v, %v), want (%v, %v)", c.name, literal, boolean, c.literal, c.boolean)
		}
	}
}

// stripServerURL removes the per-test peer origin so two refusals can be
// compared for their route rather than their port.
func stripServerURL(message string) string {
	return regexp.MustCompile(`http://127\.0\.0\.1:\d+`).ReplaceAllString(message, "http://peer")
}
