package openapi_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// client.go, Input.Body: "A field an Encoding style or a Swagger 2.0
// collectionFormat serializes, multi included, takes JSON data, under
// multipart/form-data too, so a []byte there is a base64 string and a Part or
// reader is refused"; doc.go, Values: in "a form or multipart field
// serialized by ... a Swagger 2.0 collectionFormat, null, an empty array and
// an object whose members are all undefined are undefined ... An undefined
// member or array item is skipped", and "a number or boolean is written in its
// JSON spelling"; doc.go, Fixed rules, Swagger 2.0 arrays: "multi repeats the
// name and value". So a multi array's items are written the same way under
// multipart/form-data, one text/plain part per item, as under
// application/x-www-form-urlencoded, one field per item: undefined items
// skipped, a number in its JSON spelling, a string from the JSON data, in
// which encoding/json writes an invalid byte as U+FFFD, and a []byte as
// base64.
func TestSwaggerMultiItemsSameUnderEachFormType(t *testing.T) {
	items := []any{"a", nil, []any{}, map[string]any{}, 5, "b\xffc", []byte("raw")}
	want := []string{"a", "5", "b\uFFFDc", "cmF3"}
	for _, media := range []string{"application/x-www-form-urlencoded", "multipart/form-data"} {
		t.Run(media, func(t *testing.T) {
			c := editionClient(t, editionDoc("2.0", `"/x":{"post":{"consumes":["`+media+`"],"parameters":[
				{"name":"tags","in":"formData","type":"array","collectionFormat":"multi","items":{"type":"string"}}]}}`), nil)
			req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]any{"tags": items}})
			body := editionBody(t, req)
			if media == "application/x-www-form-urlencoded" {
				if got := string(body); got != "tags=a&tags=5&tags=b%EF%BF%BDc&tags=cmF3" {
					t.Errorf("body %q, want tags=a&tags=5&tags=b%%EF%%BF%%BDc&tags=cmF3", got)
				}
				return
			}
			_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), body)
			var wantParts []wantPart
			for _, v := range want {
				wantParts = append(wantParts, wantPart{disposition: formData("tags"), ctype: "text/plain", content: v})
			}
			checkParts(t, parts, wantParts)
		})
	}
}

// client.go, Input.Body, as for TestSwaggerMultiItemsSameUnderEachFormType:
// in a multi array, which takes JSON data, "a Part or reader is refused",
// under multipart/form-data as under application/x-www-form-urlencoded, at the
// field's key or the item's.
func TestSwaggerMultiRefusesPartsAndReaders(t *testing.T) {
	for _, media := range []string{"application/x-www-form-urlencoded", "multipart/form-data"} {
		c := editionClient(t, editionDoc("2.0", `"/x":{"post":{"consumes":["`+media+`"],"parameters":[
			{"name":"tags","in":"formData","type":"array","collectionFormat":"multi","items":{"type":"string"}}]}}`), nil)
		for _, item := range []any{openapi.Part{Content: "p"}, strings.NewReader("r")} {
			t.Run(fmt.Sprintf("%s/%T", media, item), func(t *testing.T) {
				_, err := c.Prepare("POST /x", &openapi.Input{Body: map[string]any{"tags": []any{item}}})
				wantAnyKey(t, "Inputs", asRequestError(t, err).Inputs, "Input.Body/tags", "Input.Body/tags/0")
			})
		}
	}
}
