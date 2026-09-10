package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

type numericResponseTransport struct {
	body   string
	status int
}

func (t numericResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: t.status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(t.body)), Request: r}, nil
}

func TestResponseNumbersAreExactAcrossEditions(t *testing.T) {
	raw, err := os.ReadFile("../conformance/json-response-numbers.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Name, Body, Expected string }
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, edition := range []string{"2.0", "3.0.4", "3.1.2", "3.2.0"} {
		for _, status := range []int{200, 400} {
			for _, tc := range cases {
				t.Run(fmt.Sprintf("%s/%d/%s", edition, status, tc.Name), func(t *testing.T) {
					prefix := fmt.Sprintf(`"openapi":%q,"servers":[{"url":"https://api.example.test"}]`, edition)
					response := `{"description":"value","content":{"application/json":{"schema":{}}}}`
					if edition == "2.0" {
						prefix = `"swagger":"2.0","host":"api.example.test","schemes":["https"],"produces":["application/json"]`
						response = `{"description":"value","schema":{}}`
					}
					doc := fmt.Sprintf(`{%s,"info":{"title":"Numbers","version":"1"},"paths":{"/value":{"get":{"operationId":"value","responses":{%q:%s}}}}}`, prefix, fmt.Sprint(status), response)
					client, err := Load(context.Background(), FromText(doc), Options{HTTPClient: &http.Client{Transport: numericResponseTransport{tc.Body, status}}})
					if err != nil {
						t.Fatal(err)
					}
					var want any
					decoder := json.NewDecoder(strings.NewReader(tc.Body))
					decoder.UseNumber()
					if err = decoder.Decode(&want); err != nil {
						t.Fatal(err)
					}
					check := func(got any) {
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("got %#v, want %#v", got, want)
						}
						if _, err := json.Marshal(got); err != nil {
							t.Fatalf("not portable JSON: %v", err)
						}
					}
					result, err := client.Call(context.Background(), OperationID("value"), Input{})
					if err != nil {
						t.Fatal(err)
					}
					if result.OK != (status == 200) {
						t.Fatalf("wrong HTTP outcome: %+v", result)
					}
					if result.OK {
						check(result.Data)
					} else {
						check(result.Error)
					}
					streamed, err := client.Stream(context.Background(), OperationID("value"), Input{})
					if err != nil {
						t.Fatal(err)
					}
					if !streamed.OK {
						check(streamed.Error)
					} else {
						event, ok, err := streamed.Stream.Next(context.Background())
						if err != nil || !ok {
							t.Fatalf("missing value: %v", err)
						}
						check(event.Data)
						_, ok, err = streamed.Stream.Next(context.Background())
						if err != nil || ok {
							t.Fatalf("missing completion: %v", err)
						}
					}
				})
			}
		}
	}
}
