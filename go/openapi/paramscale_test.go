package openapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Scaling: compiling many parameters is linear, and serializing a value
// costs O(size of its encoded form), checked with wantLinear
// (scaling_test.go).

// styledParams is a document with one operation of n parameters that cycle
// through the parameter serializations: each style and location, explode
// both ways, allowReserved, and content parameters of several media types.
// Each is declared under components/parameters and referenced by the
// operation, so that its Param.Source names its component, a fixed length,
// while the path, with n/6 path parameters, grows with n (an inline
// parameter's Source embeds the path, so inline parameters would make the
// Sources quadratic).
func styledParams(n int) []byte {
	var path strings.Builder
	var params, refs []string
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
		params = append(params, fmt.Sprintf(`"p%d":`+p, i, i))
		refs = append(refs, fmt.Sprintf(`{"$ref":"#/components/parameters/p%d"}`, i))
	}
	return []byte(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{"` +
		path.String() + `":{"get":{"operationId":"op","parameters":[` + strings.Join(refs, ",") + `]}}},` +
		`"components":{"parameters":{` + strings.Join(params, ",") + `}}}`)
}

// Regression check, not contract: compiling an operation's parameters, path
// template included, is linear in their number, in time and in bytes
// allocated: at first use (Operations), and at a first Prepare that gives
// every path parameter. The sizes are where a quadratic dominates: a path
// template compile quadratic in the number of path parameters takes 16x as
// long for 960 to 3,840 parameters (160 to 640 of them in the path).
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

// Regression check, not contract: serializing a value costs time and allocated
// bytes linear in its encoded size: arrays and objects of 5,000 to 20,000
// items, whose output stays under the 1 MiB bound on the request target and
// header fields (doc.go, Values; at 20,000 items the largest, the deepObject
// query, is about 500 KiB), and a deepObject nested 250 to 1,000 levels (one
// leaf, so its encoded form is linear in its depth), within the 1,000-level
// bound on values (doc.go, Values). A quadratic serializer, one that copies
// what it has written for each item, would copy about 190 MB at 5,000 items and
// 3 GB at 20,000 against a linear cost well under a millisecond, so it takes
// about 16x in time and in bytes allocated. At 1,000 levels such copying is
// megabytes, which time alone may not separate from the linear cost, which the
// bytes check does.
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
		name := fmt.Sprintf("%s %T", tt.key, tt.value(0))
		wantLinear(t, name, 5000, func(n int) func() { return prepare(tt.key, tt.param, tt.value(n)) })
		wantLinearBytes(t, name+", bytes", 5000, func(n int) func() { return prepare(tt.key, tt.param, tt.value(n)) })
	}
	// levels returns a deepObject value n levels deep: n-1 objects around a
	// string leaf, which counts as a level.
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

// sharedLongName is a document whose one component Parameter Object, named
// by n KiB, sits in location in and is referenced by 16*n operations.
func sharedLongName(n int, in string) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{`)
	for i := range 16 * n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"/p%d":{"get":{"parameters":[{"$ref":"#/components/parameters/P"}]}}`, i)
	}
	fmt.Fprintf(&b, `},"components":{"parameters":{"P":{"name":"%s","in":"%s","schema":{}}}}}`, strings.Repeat("a", n<<10), in)
	return []byte(b.String())
}

// Regression check, not contract: every compiled or decoded form is computed
// at most once per document node, so one Parameter Object with a long name,
// referenced by many operations, costs Operations() time, allocated bytes and
// retained memory linear in the document, the name and the references growing
// together, in every location a shared name can take (a path parameter's name
// is written in each Paths key, so its document is already the product). A
// cost per reference would retain, for 64 KiB by 1,000 references, about 67 MB
// (query, cookie) and 133 MB (header), 16x its quarter.
func TestSharedLongParamNameScalesLinearly(t *testing.T) {
	for _, in := range []string{"query", "header", "cookie"} {
		wantLinear(t, in+": Operations()", 16, func(n int) func() { return timedOperations(t, sharedLongName(n, in)) })
		wantLinearBytes(t, in+": Operations() bytes", 16, func(n int) func() { return timedOperations(t, sharedLongName(n, in)) })
		t.Run(in+": retained after Operations()", func(t *testing.T) {
			keep := func(n int) int64 {
				c, err := openapi.Parse(context.Background(), sharedLongName(n, in), testDocURI, nil)
				if err != nil {
					t.Fatal(err)
				}
				return retainedBy(func() any { return c.Operations() })
			}
			small, large := keep(16), keep(64)
			ratio := float64(large) / float64(max(small, 64<<10))
			t.Logf("16 KiB x 256: %d bytes, 64 KiB x 1,024: %d bytes (%.1fx)", small, large, ratio)
			if ratio > 8 {
				t.Errorf("four times the input retained %.1f times the memory; want linear", ratio)
			}
		})
	}
}

// manyParams is a document whose one operation declares n query
// parameters.
func manyParams(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{"/x":{"get":{"operationId":"op","parameters":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"name":"q%d","in":"query","schema":{}}`, i)
	}
	b.WriteString(`]}}}}`)
	return []byte(b.String())
}

// Regression check, not contract: with n declared parameters, all n keys
// given and one unknown key, the refusal costs time linear in n, in Params and
// in ParamWriters; a scan of the declarations for each key would make n(n+1)/2
// comparisons. The refusal itself is contract (client.go, Input.Params: "A key
// the operation does not declare, or a missing required parameter, refuses the
// call"; Input.ParamWriters: "an unknown key, a nil writer, and the same key in
// Params and ParamWriters, are refused at that key").
func TestUnknownParamKeyRefusalScalesLinearly(t *testing.T) {
	refused := func(c *openapi.Client, in *openapi.Input) func() {
		return func() {
			_, err := c.Prepare("op", in)
			var re *openapi.RequestError
			if !errors.As(err, &re) || re.Inputs["unknown"] == nil {
				t.Errorf("Prepare = %.200v, want the unknown key refused", err)
			}
		}
	}
	client := func(n int) *openapi.Client {
		c, err := openapi.Parse(context.Background(), manyParams(n), testDocURI, nil)
		if err != nil {
			t.Fatal(err)
		}
		c.Operation("op") // compile, untimed
		return c
	}
	wantLinear(t, "Params", 4000, func(n int) func() {
		in := &openapi.Input{Params: map[string]any{"unknown": "x"}}
		for i := range n {
			in.Params["q"+strconv.Itoa(i)] = "v"
		}
		return refused(client(n), in)
	})
	wantLinear(t, "ParamWriters", 4000, func(n int) func() {
		in := &openapi.Input{ParamWriters: map[string]func(*http.Request) error{"unknown": func(*http.Request) error { return nil }}}
		for i := range n {
			in.ParamWriters["q"+strconv.Itoa(i)] = func(*http.Request) error { return nil }
		}
		return refused(client(n), in)
	})
}
