package openapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"
)

func TestFailureValuePresenceAcrossEditions(t *testing.T) {
	for _, edition := range []string{"2.0", "3.0.4", "3.1.2", "3.2.0"} {
		for _, tc := range []struct {
			name, body string
			want       any
			present    bool
		}{
			{"absent", "", nil, false},
			{"null", "null", nil, true},
			{"object", `{"reason":"missing"}`, map[string]any{"reason": "missing"}, true},
			{"empty-string", `""`, "", true},
			{"false", "false", false, true},
			{"zero", "0", float64(0), true},
		} {
			for _, status := range []int{200, 400} {
				t.Run(fmt.Sprintf("%s/%d/%s", edition, status, tc.name), func(t *testing.T) {
					prefix := fmt.Sprintf(`"openapi":%q,"servers":[{"url":"https://api.example.test"}]`, edition)
					response := `{"description":"value","content":{"application/json":{"schema":{}}}}`
					if edition == "2.0" {
						prefix = `"swagger":"2.0","host":"api.example.test","schemes":["https"],"produces":["application/json"]`
						response = `{"description":"value","schema":{}}`
					}
					document := fmt.Sprintf(`{%s,"info":{"title":"Presence","version":"1"},"paths":{"/value":{"get":{"operationId":"value","responses":{%q:%s}}}}}`, prefix, fmt.Sprint(status), response)
					client, err := Load(context.Background(), FromText(document), Options{HTTPClient: &http.Client{Transport: numericResponseTransport{tc.body, status}}})
					if err != nil {
						t.Fatal(err)
					}
					check := func(ok bool, value any, present bool, response *http.Response) {
						t.Helper()
						if ok != (status == 200) || present != (status == 400 && tc.present) {
							t.Fatalf("OK=%v ErrorPresent=%v", ok, present)
						}
						if !ok && !reflect.DeepEqual(value, tc.want) {
							t.Fatalf("Error=%#v; want %#v", value, tc.want)
						}
						if ok && value != nil {
							t.Fatalf("successful result has Error=%#v", value)
						}
						if !ok {
							if response.Body == nil {
								if tc.body != "" {
									t.Fatal("missing nonempty failure replay")
								}
								return
							}
							defer response.Body.Close()
							body, err := io.ReadAll(response.Body)
							if err != nil || string(body) != tc.body {
								t.Fatalf("failure replay=%q, err=%v", body, err)
							}
						}
					}
					result, err := client.Call(context.Background(), OperationID("value"), Input{})
					if err != nil {
						t.Fatal(err)
					}
					check(result.OK, result.Error, result.ErrorPresent, result.Response)
					if result.OK && result.Response.Body != nil {
						result.Response.Body.Close()
					}
					streamed, err := client.Stream(context.Background(), OperationID("value"), Input{})
					if err != nil {
						t.Fatal(err)
					}
					check(streamed.OK, streamed.Error, streamed.ErrorPresent, streamed.Response)
					if streamed.OK {
						for {
							_, open, err := streamed.Stream.Next(context.Background())
							if err != nil {
								t.Fatal(err)
							}
							if !open {
								break
							}
						}
						if err := streamed.Stream.Wait(); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}
