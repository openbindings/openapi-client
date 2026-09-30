package openapi_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Scaling (dev loop, "Lessons folded in from stage 1": "Every
// document-driven path a stage adds must be covered by the scaling-test
// harness (4x the input costs at most about 10x, best of 3)"; stage 2 brief:
// "compiling many parameters is linear, and serializing a value costs O(size
// of its encoded form)"), with wantLinear (regress2_scale_test.go).

// styledParams is a document with one operation of n parameters that cycle
// through every serialization stage 2 adds: each style and location, explode
// both ways, allowReserved, and content parameters of several media types.
func styledParams(n int) []byte {
	var path strings.Builder
	var params []string
	path.WriteString("/x")
	for i := range n {
		var p string
		switch i % 12 {
		case 0:
			p = `{"name":"p%d","in":"path","required":true,"style":"matrix","explode":true,"schema":{}}`
			fmt.Fprintf(&path, "/{p%d}", i)
		case 1:
			p = `{"name":"p%d","in":"path","required":true,"style":"label","schema":{}}`
			fmt.Fprintf(&path, "/{p%d}", i)
		case 2:
			p = `{"name":"p%d","in":"query","explode":false,"allowReserved":true,"schema":{}}`
		case 3:
			p = `{"name":"p%d","in":"query","style":"spaceDelimited","explode":false,"schema":{}}`
		case 4:
			p = `{"name":"p%d","in":"query","style":"pipeDelimited","explode":true,"schema":{}}`
		case 5:
			p = `{"name":"p%d","in":"query","style":"deepObject","schema":{}}`
		case 6:
			p = `{"name":"p%d","in":"header","explode":true,"schema":{}}`
		case 7:
			p = `{"name":"p%d","in":"cookie","explode":false,"schema":{}}`
		case 8:
			p = `{"name":"p%d","in":"query","content":{"application/json":{"schema":{}}}}`
		case 9:
			p = `{"name":"p%d","in":"header","content":{"text/plain":{}}}`
		case 10:
			p = `{"name":"p%d","in":"cookie","content":{"application/vnd.x+json":{}}}`
		default:
			p = `{"name":"p%d","in":"query","content":{"application/x-custom":{}}}`
		}
		params = append(params, fmt.Sprintf(p, i))
	}
	return []byte(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{"` +
		path.String() + `":{"get":{"operationId":"op","parameters":[` + strings.Join(params, ",") + `]}}}}`)
}

// Compiling an operation's parameters is linear in their number: at first
// use (Operations), and at a first Prepare that gives every path parameter.
// The sizes are where a quadratic dominates: stage 1's path template
// compile, quadratic in the number of path parameters, took 16x as long
// for 960 to 3,840 parameters (160 to 640 of them in the path).
func TestStyledParamsCompileScale(t *testing.T) {
	wantLinear(t, "Operations()", 960, func(n int) func() { return timedOperations(t, styledParams(n)) })
	wantLinear(t, "first Prepare", 960, func(n int) func() {
		doc := styledParams(n)
		in := &openapi.Input{Params: map[string]any{}}
		for i := 0; i < n; i += 12 {
			in.Params["p"+strconv.Itoa(i)] = "a"
			in.Params["p"+strconv.Itoa(i+1)] = "b"
		}
		// One more client than the harness's runs checks, untimed, that the
		// call prepares.
		clients := freshClients(t, doc, nil, scaleRuns+1)
		if _, err := clients[scaleRuns].Prepare("op", in); err != nil {
			t.Errorf("%.200v", err)
			return func() {}
		}
		i := 0
		return func() {
			c := clients[i%scaleRuns]
			i++
			c.Prepare("op", in)
		}
	})
}

// largeValueDoc has one operation per serialization, each with the one
// parameter v.
const largeValueDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{
	"/fx":{"get":{"operationId":"formExplode","parameters":[{"name":"v","in":"query","schema":{}}]}},
	"/fn":{"get":{"operationId":"form","parameters":[{"name":"v","in":"query","explode":false,"schema":{}}]}},
	"/fr":{"get":{"operationId":"reserved","parameters":[{"name":"v","in":"query","explode":false,"allowReserved":true,"schema":{}}]}},
	"/sd":{"get":{"operationId":"space","parameters":[{"name":"v","in":"query","style":"spaceDelimited","explode":false,"schema":{}}]}},
	"/pd":{"get":{"operationId":"pipe","parameters":[{"name":"v","in":"query","style":"pipeDelimited","explode":false,"schema":{}}]}},
	"/do":{"get":{"operationId":"deep","parameters":[{"name":"v","in":"query","style":"deepObject","schema":{}}]}},
	"/m/x{v}":{"get":{"operationId":"matrix","parameters":[{"name":"v","in":"path","required":true,"style":"matrix","explode":true,"schema":{}}]}},
	"/l/x{v}":{"get":{"operationId":"label","parameters":[{"name":"v","in":"path","required":true,"style":"label","schema":{}}]}},
	"/h":{"get":{"operationId":"header","parameters":[{"name":"X-V","in":"header","explode":true,"schema":{}}]}},
	"/c":{"get":{"operationId":"cookie","parameters":[{"name":"v","in":"cookie","schema":{}}]}},
	"/j":{"get":{"operationId":"json","parameters":[{"name":"v","in":"query","content":{"application/json":{}}}]}}
}}`

// Serializing a value costs time linear in its encoded size: arrays and
// objects of up to 100,000 items, and a deepObject nested 250 to 1,000
// levels (one leaf, so its encoded form is linear in its depth), within the
// 1,000-level bound on values (stage 2 ledger, Q9). At 25,000 items a
// quadratic serializer, one that copies what it has written for each item,
// moves gigabytes against a linear cost of about a millisecond, so it takes
// about 16x. At 1,000 levels such copying is megabytes, which time alone
// may not separate from the linear cost, so the depth case is checked on the
// bytes allocated too.
func TestLargeParamValuesScale(t *testing.T) {
	c, err := openapi.Parse(context.Background(), []byte(largeValueDoc), testDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := func(n int) []string {
		s := make([]string, n)
		for i := range s {
			s[i] = "it/" + strconv.Itoa(i)
		}
		return s
	}
	members := func(n int) map[string]string {
		m := make(map[string]string, n)
		for i := range n {
			m["k"+strconv.Itoa(i)] = "v " + strconv.Itoa(i)
		}
		return m
	}
	// prepare returns work that prepares key with the value, after checking
	// once that it prepares; a refusal is reported, not timed.
	prepare := func(key, param string, v any) func() {
		in := &openapi.Input{Params: map[string]any{param: v}}
		if _, err := c.Prepare(key, in); err != nil {
			t.Errorf("%s: %.200v", key, err)
			return func() {}
		}
		return func() { c.Prepare(key, in) }
	}
	for _, tt := range []struct {
		key, param string
		value      func(n int) any
	}{
		{"formExplode", "v", func(n int) any { return items(n) }},
		{"form", "v", func(n int) any { return items(n) }},
		{"reserved", "v", func(n int) any { return items(n) }},
		{"space", "v", func(n int) any { return items(n) }},
		{"pipe", "v", func(n int) any { return items(n) }},
		{"matrix", "v", func(n int) any { return items(n) }},
		{"label", "v", func(n int) any { return items(n) }},
		{"header", "X-V", func(n int) any { return members(n) }},
		{"cookie", "v", func(n int) any { return items(n) }},
		{"formExplode", "v", func(n int) any { return members(n) }},
		{"deep", "v", func(n int) any { return members(n) }},
		{"json", "v", func(n int) any { return items(n) }},
	} {
		wantLinear(t, fmt.Sprintf("%s %T", tt.key, tt.value(0)), 25000, func(n int) func() {
			return prepare(tt.key, tt.param, tt.value(n))
		})
	}
	// levels returns a deepObject value n levels deep, counting the leaf:
	// n-1 objects around a string, so it is within the bound whether or not
	// the leaf counts as a level.
	levels := func(n int) any {
		var v any = "leaf"
		for range n - 1 {
			v = map[string]any{"a": v}
		}
		return v
	}
	wantLinear(t, "deepObject depth", 250, func(n int) func() { return prepare("deep", "v", levels(n)) })
	wantLinearBytes(t, "deepObject depth, bytes", 250, func(n int) func() { return prepare("deep", "v", levels(n)) })
}
