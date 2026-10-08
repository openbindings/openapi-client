package openapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Styles: every style, explode and location OpenAPI 3.1 defines,
// for primitive, array and object values, checked on the wire with exact
// bytes: against the OAS 3.1.2 style table, and against the RFC 6570 oracle
// (oracle_test.go) for matrix, label, simple and form, and the table and
// text of OAS 3.1.2 for spaceDelimited, pipeDelimited and deepObject.

// fate is what a call with one parameter value must do.
type fate int

const (
	sent      fate = iota // sent, as want says
	omitted               // an undefined optional value: nothing written
	refused               // refused at the parameter's key
	unsettled             // the contract does not settle it: not asserted
)

// styleCfg is one parameter declaration: a location, style, explode and
// allowReserved, in an operation of its own.
type styleCfg struct {
	id       string // the operationId, and the subtest name
	in       string
	style    string
	explode  bool
	reserved bool
}

// path is the operation's Paths key, and field its parameter name (the
// header or cookie name, or "p").
func (c styleCfg) path() string {
	switch {
	case c.in == "path" && c.style == "simple":
		return "/" + c.id + "/{p}"
	case c.in == "path":
		return "/" + c.id + "/x{p}"
	}
	return "/" + c.id
}

func (c styleCfg) field() string {
	switch c.in {
	case "header":
		return "X-P"
	case "cookie":
		return "c"
	}
	return "p"
}

func (c styleCfg) decl() string {
	return fmt.Sprintf(`{"name":%q,"in":%q,"required":%t,"style":%q,"explode":%t,"allowReserved":%t,"schema":{}}`,
		c.field(), c.in, c.in == "path", c.style, c.explode, c.reserved)
}

// styleDoc is a document with one GET operation per configuration.
func styleDoc(cfgs []styleCfg) string {
	var ops []string
	for _, c := range cfgs {
		ops = append(ops, fmt.Sprintf(`%q:{"get":{"operationId":%q,"parameters":[%s]}}`, c.path(), c.id, c.decl()))
	}
	return doc31(strings.Join(ops, ","))
}

// expect derives what a call with value v for c must send: the request
// target for a path or query parameter, and the field's single value for a
// header or cookie parameter.
func (c styleCfg) expect(t testing.TB, v any) (string, fate) {
	t.Helper()
	required := c.in == "path"
	absent := func() (string, fate) {
		if required {
			return "", refused // client.go, Input.Params: "a missing required parameter ... refuses the call"
		}
		if c.in == "path" || c.in == "query" {
			return strings.TrimSuffix(c.path(), "/{p}"), omitted
		}
		return "", omitted
	}
	// doc.go, Values: "Only a nil interface is absent".
	if v == nil {
		return absent()
	}
	n := jsonData(t, v)
	allow := allowU
	if c.reserved && c.in == "query" {
		allow = allowURQuery
	}
	// Undefinedness is settled first; style refusals apply to defined values
	// (doc.go, Fixed rules, Styles).
	if jUndefined(n) {
		return absent()
	}
	switch c.style {
	case "deepObject":
		want, o := deepExpect(c.field(), n, allow)
		if o == omitted {
			return absent()
		}
		return "/" + c.id + "?" + want, o
	case "spaceDelimited", "pipeDelimited":
		if n.kind == 's' || n.kind == 'p' {
			return "", refused // doc.go, Fixed rules, Styles: "a primitive for spaceDelimited, pipeDelimited ... is refused"
		}
		uv, err := uvalOf(n)
		switch {
		case err != nil:
			return "", refused
		case !uv.defined():
			return absent()
		}
		delim := "%20"
		if c.style == "pipeDelimited" {
			delim = "%7C"
		}
		return "/" + c.id + "?" + delimited(c.field(), uv, delim, allow), sent
	}
	uv, err := uvalOf(n)
	switch {
	case err != nil:
		return "", refused
	case !uv.defined():
		return absent()
	case uv.allUndef && c.explode && c.in == "header":
		// Exploded, a list of only undefined items writes nothing, and the
		// header field is left out (RFC 6570 leaves the exploded expansion
		// of a list with no defined members unsettled).
		return "", omitted
	}
	spec := uspec{name: pctName(c.field()), explode: c.explode, value: uv}
	switch c.in {
	case "path":
		op := map[string]string{"simple": "", "label": ".", "matrix": ";"}[c.style]
		return strings.TrimSuffix(c.path(), "{p}") + uexpand(op, spec), sent
	case "query":
		o := uops["?"]
		if allow == allowURQuery {
			o = uformReserved
			o.allow = allowURQuery
		}
		return "/" + c.id + uexpandOp(o, spec), sent
	case "header":
		return uexpandOp(uheader, spec), sent
	default:
		// doc.go, Fixed rules, Cookies: "one Cookie field, pairs joined by
		// "; "", the pairs form style writes; OAS 3.1.2 Appendix D.1: the
		// form "?" prefix is stripped, and pairs in cookies "are delimited by
		// a semicolon followed by a space character rather than &".
		// A ";" or control character in a value is percent-encoded, as any
		// other byte outside the unreserved set (doc.go, Fixed rules,
		// Percent-encoding).
		want := strings.ReplaceAll(strings.TrimPrefix(uexpand("?", spec), "?"), "&", "; ")
		if want == "" {
			return "", omitted // no pair: no Cookie field
		}
		return want, sent
	}
}

// deepExpect derives a deepObject query from defined JSON data n: doc.go, Fixed
// rules, Styles: "Nesting in any style but deepObject is refused, and so are an
// array as or in a deepObject value unless Options.DeepObjectArrays says how to
// write it, a primitive for ... deepObject"; Values: "An undefined member or
// array item is skipped". Undefinedness is settled first (Fixed rules, Styles),
// so an undefined member, an empty array included, is skipped at any depth.
func deepExpect(name string, n jnode, allow uallow) (string, fate) {
	if n.kind != 'o' {
		return "", refused
	}
	var check func(n jnode) fate
	check = func(n jnode) fate {
		o := omitted
		for _, it := range n.items {
			switch {
			case jUndefined(it):
			case it.kind == 'a':
				return refused
			case it.kind == 'o':
				switch check(it) {
				case refused:
					return refused
				case sent:
					o = sent
				}
			default:
				o = sent
			}
		}
		return o
	}
	if o := check(n); o != sent {
		return "", o
	}
	return deepObject(name, n, allow), sent
}

// callOne calls key with one parameter value and returns the request the
// server received, or the refusal.
func callOne(t *testing.T, w *wire, c *openapi.Client, key, param string, v any) (rec, *openapi.RequestError) {
	t.Helper()
	before := w.count()
	resp, err := c.Call(t.Context(), key, &openapi.Input{Params: map[string]any{param: v}}, nil)
	var re *openapi.RequestError
	if errors.As(err, &re) {
		if resp != nil || w.count() != before {
			t.Errorf("a refused call was sent")
		}
		return rec{}, re
	}
	if err != nil {
		t.Fatalf("Call(%q): %v", key, err)
	}
	if w.count() != before+1 {
		t.Fatalf("server received %d requests, want 1", w.count()-before)
	}
	return w.last(t), nil
}

// checkOutcome compares a call's result with what c.expect derived.
func checkOutcome(t *testing.T, c styleCfg, got rec, re *openapi.RequestError, want string, o fate) {
	t.Helper()
	switch o {
	case unsettled:
		return
	case refused:
		if re == nil {
			t.Errorf("sent %q, want a refusal at Inputs[%q]", describeSent(c, got), c.field())
			return
		}
		wantKeys(t, "Inputs", re.Inputs, true, c.field())
		return
	}
	if re != nil {
		t.Errorf("refused: %v; want %q", re, want)
		return
	}
	switch c.in {
	case "path", "query":
		if got.RequestURI != want {
			t.Errorf("request target %q, want %q", got.RequestURI, want)
		}
	default:
		name := c.field()
		if c.in == "cookie" {
			name = "Cookie"
		}
		v := got.Header.Values(name)
		if o == omitted {
			if v != nil {
				t.Errorf("%s = %q, want none", name, v)
			}
			return
		}
		if len(v) != 1 || v[0] != want {
			t.Errorf("%s = %q, want [%q]", name, v, want)
		}
	}
}

func describeSent(c styleCfg, got rec) string {
	switch c.in {
	case "header":
		return fmt.Sprint(got.Header.Values(c.field()))
	case "cookie":
		return fmt.Sprint(got.Header.Values("Cookie"))
	}
	return got.RequestURI
}

// styleConfigs is every (location, style, explode) pair OpenAPI 3.1
// defines (OAS 3.1.2 section 4.8.12.3, Style Values), with allowReserved
// for the query styles.
func styleConfigs() []styleCfg {
	var cfgs []styleCfg
	add := func(in, style string, explode, reserved bool) {
		id := fmt.Sprintf("%s_%s_%t", in, style, explode)
		if reserved {
			id += "_reserved"
		}
		cfgs = append(cfgs, styleCfg{id, in, style, explode, reserved})
	}
	for _, explode := range []bool{false, true} {
		for _, style := range []string{"simple", "label", "matrix"} {
			add("path", style, explode, false)
		}
		for _, reserved := range []bool{false, true} {
			add("query", "form", explode, reserved)
			add("query", "deepObject", explode, reserved)
		}
		add("header", "simple", explode, false)
		add("cookie", "form", explode, false)
	}
	for _, reserved := range []bool{false, true} {
		add("query", "spaceDelimited", false, reserved)
		add("query", "pipeDelimited", false, reserved)
	}
	return cfgs
}

// styleCorpus is the values every configuration is called with: primitives,
// arrays and objects (doc.go, Values: numbers and booleans "in its JSON
// spelling ... a string or json.Number as it is"; members in encoding/json's
// order), the undefined values, and values a style refuses.
var styleCorpus = []struct {
	name string
	v    any
}{
	{"string", "blue"},
	{"empty string", ""},
	{"reserved and unsafe", "a b/c,d;e=f&g?h#i[j]k%l~m+n!o*p'q(r)s$t@u:v|w^x`y\"z<>\\{}"},
	{"non-ASCII", "é☃"},
	{"percent triples", "%41%zz%2f"},
	{"integer", 7},
	{"negative float", -1.5},
	{"exponent", 1e21},
	{"boolean", true},
	{"json.Number", json.Number("12.50")},
	{"array", colors},
	{"array of numbers and booleans", []any{1, 2.5, false, "x"}},
	{"array of empty items", []string{"", ""}},
	{"array of one empty item", []string{""}},
	{"array items holding delimiters", []string{"a,b", "c d", "e;f", "g.h", "i=j&k", "l|m", "n/o"}},
	{"map sorted", colorMap},
	{"struct in field order", colorRGB},
	{"member names encoded", map[string]string{"a b": "c/d", "e,f": "g=h", "i[j]": "k"}},
	{"empty member value", map[string]any{"a": "", "b": "x"}},
	{"undefined member skipped", map[string]any{"a": nil, "b": "x", "c": 3}},
	{"struct tags", tagged{A: "1", B: "2"}},
	{"undefined: nil interface", nil},
	{"undefined: typed nil", (*string)(nil)},
	{"undefined: empty array", []int{}},
	{"undefined: empty object", map[string]any{}},
	{"undefined: all members undefined", map[string]any{"a": nil}},
	{"null item skipped", []any{"a", nil}},
	{"nested array", [][]string{{"a"}}},
	{"object item", []any{map[string]int{"a": 1}}},
	{"array member", map[string]any{"a": []int{1}}},
	{"object member", map[string]any{"a": map[string]int{"b": 1}, "c": "d"}},
	{"deep object member", map[string]any{"a": map[string]any{"b": map[string]any{"c": "v", "d": nil}}}},
	// Undefinedness is settled first (doc.go, Fixed rules, Styles):
	// undefined collections nested in a value are skipped, not refused as
	// nesting.
	{"nested undefined collections skipped", map[string]any{"a": []int{}, "b": map[string]any{}, "c": "x", "d": map[string]any{"e": nil}}},
	{"undefined: only nested undefined members", map[string]any{"a": []int{}, "b": map[string]any{"c": []string{}}}},
	// doc.go, Values: undefined array items are skipped; nested defined
	// collections stay refused.
	{"undefined array items skipped", []any{"a", nil, []int{}, map[string]any{}, "b"}},
	{"all-undefined object item skipped", []any{"a", map[string]any{"b": nil}}},
	{"only undefined items", []any{nil, []int{}}},
	{"defined collection item", []any{"a", []any{"b", nil}}},
	{"reserved member names", map[string]string{"a/b": "c", "d[e]": "f?g"}},
}

// Every configuration with every corpus value. Matrix, label, simple and form
// (and a header's simple, and a cookie's form) are checked against the RFC 6570
// oracle (doc.go, Values: "as RFC 6570 says"; OAS 3.1.2 section 4.8.12.3:
// matrix is RFC 6570 section 3.2.7, label 3.2.5, simple 3.2.2, form 3.2.8),
// spaceDelimited, pipeDelimited and deepObject against the OAS 3.1.2 table and
// text. allowReserved applies to the query styles: RFC 6570 reserved expansion,
// member names included (doc.go, Fixed rules, Percent-encoding). An exploded
// member whose value is "" is written as its name alone except in form style
// and deepObject, which write its name and "=" (Fixed rules, Styles), matrix
// without explode writes [""] as ";p" (RFC 6570 section 3.2.7), a cookie
// value's ";" is percent-encoded (Fixed rules, Percent-encoding), and undefined
// values, nested ones included, are settled before any style refusal (Fixed
// rules, Styles).
func TestStylesAgainstOracle(t *testing.T) {
	cfgs := styleConfigs()
	w := newWire(t, nil)
	c := parseFor(t, w, styleDoc(cfgs), nil)
	for _, cfg := range cfgs {
		t.Run(cfg.id, func(t *testing.T) {
			for _, tt := range styleCorpus {
				t.Run(tt.name, func(t *testing.T) {
					want, o := cfg.expect(t, tt.v)
					got, re := callOne(t, w, c, cfg.id, cfg.field(), tt.v)
					checkOutcome(t, cfg, got, re, want, o)
				})
			}
		})
	}
}

// OAS 3.1.2 section 4.8.12.6, Style Examples, reproduced for the parameter
// color with "blue", ["blue","black","brown"] and {"R":100,"G":200,"B":150}
// (a struct, so the members keep the table's order). The table's
// "undefined" column holds what RFC 6570 expands the empty string to
// (";color", ".", empty and "color=": sections 3.2.7, 3.2.5, 3.2.2 and
// 3.2.8), and doc.go, Values, says "" is a value, so that column is checked
// with ""; an undefined value is checked separately (TestUndefinedValues).
// "n/a" is "undefined" behavior: refused at the key (doc.go, Fixed rules,
// Styles), except that "deepObject ignores explode", so deepObject with
// explode false serializes an object as with explode true. The matrix and
// label rows are sent in a path segment after "/items", simple after
// "/items/" and as a header, the query styles in the query.
func TestOAS312StyleExamples(t *testing.T) {
	const na = "n/a"
	const deep = "color%5BR%5D=100&color%5BG%5D=200&color%5BB%5D=150"
	rows := []struct {
		style                string
		explode              bool
		empty, str, arr, obj string
	}{
		{"matrix", false, ";color", ";color=blue", ";color=blue,black,brown", ";color=R,100,G,200,B,150"},
		{"matrix", true, ";color", ";color=blue", ";color=blue;color=black;color=brown", ";R=100;G=200;B=150"},
		{"label", false, ".", ".blue", ".blue,black,brown", ".R,100,G,200,B,150"},
		{"label", true, ".", ".blue", ".blue.black.brown", ".R=100.G=200.B=150"},
		{"simple", false, "", "blue", "blue,black,brown", "R,100,G,200,B,150"},
		{"simple", true, "", "blue", "blue,black,brown", "R=100,G=200,B=150"},
		{"form", false, "color=", "color=blue", "color=blue,black,brown", "color=R,100,G,200,B,150"},
		{"form", true, "color=", "color=blue", "color=blue&color=black&color=brown", "R=100&G=200&B=150"},
		{"spaceDelimited", false, na, na, "color=blue%20black%20brown", "color=R%20100%20G%20200%20B%20150"},
		{"spaceDelimited", true, na, na, na, na},
		{"pipeDelimited", false, na, na, "color=blue%7Cblack%7Cbrown", "color=R%7C100%7CG%7C200%7CB%7C150"},
		{"pipeDelimited", true, na, na, na, na},
		{"deepObject", false, na, na, na, deep},
		{"deepObject", true, na, na, na, deep},
	}
	type target struct {
		id, in, path, prefix string
	}
	var ops []string
	var targets [][]target
	for i, r := range rows {
		var ts []target
		switch r.style {
		case "matrix", "label":
			ts = append(ts, target{fmt.Sprintf("r%d", i), "path", fmt.Sprintf("/r%d/items{color}", i), fmt.Sprintf("/r%d/items", i)})
		case "simple":
			ts = append(ts, target{fmt.Sprintf("r%d", i), "path", fmt.Sprintf("/r%d/items/{color}", i), fmt.Sprintf("/r%d/items/", i)},
				target{fmt.Sprintf("r%dh", i), "header", fmt.Sprintf("/r%dh", i), ""})
		default:
			ts = append(ts, target{fmt.Sprintf("r%d", i), "query", fmt.Sprintf("/r%d/items", i), fmt.Sprintf("/r%d/items?", i)})
		}
		for _, tg := range ts {
			name := "color"
			if tg.in == "header" {
				name = "Color"
			}
			ops = append(ops, fmt.Sprintf(`%q:{"get":{"operationId":%q,"parameters":[{"name":%q,"in":%q,"required":%t,"style":%q,"explode":%t,"schema":{}}]}}`,
				tg.path, tg.id, name, tg.in, tg.in == "path", r.style, r.explode))
		}
		targets = append(targets, ts)
	}
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(strings.Join(ops, ",")), nil)
	for i, r := range rows {
		for _, tg := range targets[i] {
			name := "color"
			if tg.in == "header" {
				name = "Color"
			}
			for _, cell := range []struct {
				column string
				value  any
				want   string
			}{{"undefined", "", r.empty}, {"string", "blue", r.str}, {"array", colors, r.arr}, {"object", colorRGB, r.obj}} {
				t.Run(fmt.Sprintf("%s/explode=%t/%s/%s", r.style, r.explode, tg.in, cell.column), func(t *testing.T) {
					got, re := callOne(t, w, c, tg.id, name, cell.value)
					if cell.want == na {
						if re == nil {
							t.Fatalf("sent %q, want a refusal", got.RequestURI)
						}
						wantKeys(t, "Inputs", re.Inputs, true, name)
						return
					}
					if re != nil {
						t.Fatalf("refused: %v; want %q", re, cell.want)
					}
					if tg.in == "header" {
						if v := got.Header.Values(name); len(v) != 1 || v[0] != cell.want {
							t.Errorf("%s = %q, want [%q]", name, v, cell.want)
						}
						return
					}
					if want := tg.prefix + cell.want; got.RequestURI != want {
						t.Errorf("request target %q, want %q", got.RequestURI, want)
					}
				})
			}
		}
		// OAS 3.1.2 section 4.8.12.6: explode true with spaceDelimited or
		// pipeDelimited is n/a whatever the value; doc.go, Fixed rules,
		// Styles: refused "with Param.Err set where the document alone
		// decides it".
		if (r.style == "spaceDelimited" || r.style == "pipeDelimited") && r.explode {
			if p := param(t, mustOp(t, c, targets[i][0].id), 0); p.Err == nil {
				t.Errorf("%s explode true: Param.Err = nil", r.style)
			}
		}
	}
}

// doc.go, Values: "null, an empty array and an object whose members are all
// undefined are undefined, as RFC 6570 says ... an undefined optional
// parameter is omitted, and an undefined required one is missing"; "Only a
// nil interface is absent". RFC 6570 section 3.2.1: "If all of the variables
// in an expression are undefined, then the expression's expansion is the
// empty string": no name, no "=", no leading delimiter.
func TestUndefinedValues(t *testing.T) {
	undefined := []struct {
		name string
		v    any
	}{
		{"nil interface", nil},
		{"typed nil", (*int)(nil)},
		{"nil map", map[string]any(nil)},
		{"nil slice", []string(nil)},
		{"empty array", []string{}},
		{"empty object", map[string]string{}},
		{"all members undefined", map[string]any{"a": nil, "b": nil}},
		{"struct of nil pointers", struct {
			A *int `json:"a"`
		}{}},
	}
	w := newWire(t, nil)
	doc := doc31(`
		"/opt/{p}":{"get":{"operationId":"opt","parameters":[
			{"name":"p","in":"path","required":true,"schema":{}},
			{"name":"f","in":"query","schema":{}},
			{"name":"g","in":"query","explode":false,"schema":{}},
			{"name":"X-H","in":"header","schema":{}},
			{"name":"c","in":"cookie","schema":{}},
			{"name":"d","in":"cookie","explode":false,"schema":{}}]}},
		"/m{m}":{"get":{"operationId":"matrix","parameters":[{"name":"m","in":"path","required":true,"style":"matrix","schema":{}}]}},
		"/l{l}":{"get":{"operationId":"label","parameters":[{"name":"l","in":"path","required":true,"style":"label","explode":true,"schema":{}}]}},
		"/req":{"get":{"operationId":"req","parameters":[
			{"name":"f","in":"query","required":true,"schema":{}},
			{"name":"s","in":"query","required":true,"style":"spaceDelimited","explode":false,"schema":{}},
			{"name":"X-H","in":"header","required":true,"schema":{}},
			{"name":"c","in":"cookie","required":true,"schema":{}}]}},
		"/deep":{"get":{"operationId":"deep","parameters":[{"name":"d","in":"query","style":"deepObject","schema":{}}]}},
		"/delim":{"get":{"operationId":"delim","parameters":[
			{"name":"s","in":"query","style":"spaceDelimited","explode":false,"schema":{}},
			{"name":"t","in":"query","style":"pipeDelimited","explode":false,"schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	for _, u := range undefined {
		t.Run(u.name, func(t *testing.T) {
			// Optional parameters are omitted.
			mustCall(t, c, "opt", &openapi.Input{Params: map[string]any{"p": "x", "f": u.v, "g": u.v, "X-H": u.v, "c": u.v, "d": u.v}}, nil)
			got := w.last(t)
			if got.RequestURI != "/opt/x" {
				t.Errorf("request target %q, want /opt/x", got.RequestURI)
			}
			if v, ok := got.Header["X-H"]; ok {
				t.Errorf("X-H = %q, want none", v)
			}
			if v, ok := got.Header["Cookie"]; ok {
				t.Errorf("Cookie = %q, want none", v)
			}
			// Required parameters are missing.
			for _, key := range []string{"opt", "matrix", "label"} {
				name := map[string]string{"opt": "p", "matrix": "m", "label": "l"}[key]
				before := w.count()
				resp, err := c.Call(t.Context(), key, &openapi.Input{Params: map[string]any{name: u.v}}, nil)
				re := refusedSince(t, w, before, resp, err)
				wantKeys(t, key+" Inputs", re.Inputs, true, name)
			}
			before := w.count()
			resp, err := c.Call(t.Context(), "req", &openapi.Input{Params: map[string]any{"f": u.v, "X-H": u.v, "c": u.v, "s": []int{1}}}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "req Inputs", re.Inputs, true, "f", "X-H", "c")
		})
	}
	// The styles that take no primitive: an absent value is omitted, and so
	// are an empty array and an empty object where the style takes arrays or
	// objects (spaceDelimited and pipeDelimited take both; deepObject takes
	// objects).
	for _, u := range []struct {
		name string
		v    any
	}{{"nil interface", nil}, {"empty object", map[string]any{}}, {"all members undefined", map[string]any{"a": nil}}} {
		mustCall(t, c, "deep", &openapi.Input{Params: map[string]any{"d": u.v}}, nil)
		if got := w.last(t).RequestURI; got != "/deep" {
			t.Errorf("deepObject %s: request target %q, want /deep", u.name, got)
		}
		mustCall(t, c, "delim", &openapi.Input{Params: map[string]any{"s": u.v, "t": u.v}}, nil)
		if got := w.last(t).RequestURI; got != "/delim" {
			t.Errorf("delimited %s: request target %q, want /delim", u.name, got)
		}
	}
	mustCall(t, c, "delim", &openapi.Input{Params: map[string]any{"s": []int{}, "t": []string{}}}, nil)
	if got := w.last(t).RequestURI; got != "/delim" {
		t.Errorf("delimited empty arrays: request target %q, want /delim", got)
	}
	before := w.count()
	resp, err := c.Call(t.Context(), "req", &openapi.Input{Params: map[string]any{"f": 1, "X-H": 1, "c": 1, "s": []int{}}}, nil)
	re := refusedSince(t, w, before, resp, err)
	wantKeys(t, "req Inputs", re.Inputs, true, "s")
}

// OAS 3.1.2 Appendix C.1: "Multiple style: "form" parameters are equivalent
// to a single RFC6570 variable list using the ? prefix operator", so an
// undefined first parameter leaves no delimiter behind ({?foo*,bar}, and
// NOT {?foo*}{&bar}); doc.go, Fixed rules, Order: declared order, the path
// item's parameters first. The non-RFC 6570 query styles join the same
// query with "&" (Appendix C.3; section 4.8.12.6). Several path parameters
// in one segment each expand in place.
func TestSeveralParameters(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/s/x{a}{b}.{c}":{
		"parameters":[{"name":"foo","in":"query","schema":{}}],
		"get":{"operationId":"op","parameters":[
			{"name":"a","in":"path","required":true,"style":"matrix","schema":{}},
			{"name":"b","in":"path","required":true,"style":"matrix","explode":true,"schema":{}},
			{"name":"c","in":"path","required":true,"style":"label","schema":{}},
			{"name":"bar","in":"query","schema":{}},
			{"name":"sp","in":"query","style":"spaceDelimited","explode":false,"schema":{}},
			{"name":"dp","in":"query","style":"deepObject","schema":{}},
			{"name":"nx","in":"query","explode":false,"schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	base := map[string]any{"a": "1", "b": map[string]int{"k": 2}, "c": []string{"x", "y"}}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	tests := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"all", with(map[string]any{"foo": map[string]string{"f": "1"}, "bar": "b", "sp": []int{1, 2}, "dp": map[string]string{"m": "n"}, "nx": []string{"p", "q"}}),
			"/s/x;a=1;k=2..x,y?f=1&bar=b&sp=1%202&dp%5Bm%5D=n&nx=p,q"},
		{"first undefined", with(map[string]any{"foo": map[string]any{}, "bar": "b"}), "/s/x;a=1;k=2..x,y?bar=b"},
		{"only a later style", with(map[string]any{"dp": map[string]string{"m": "n"}}), "/s/x;a=1;k=2..x,y?dp%5Bm%5D=n"},
		{"none", base, "/s/x;a=1;k=2..x,y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustCall(t, c, "op", &openapi.Input{Params: tt.params}, nil)
			if got := w.last(t).RequestURI; got != tt.want {
				t.Errorf("request target %q, want %q", got, tt.want)
			}
		})
	}
}

// doc.go, Fixed rules, Percent-encoding: "parameter and member names encode
// every byte outside RFC 3986's unreserved set as %XX in uppercase hex. So
// deepObject nests objects as a%5Bb%5D%5Bc%5D=v"; OAS 3.1.2 Appendix C.4.4:
// the name ❤️ is written %E2%9D%A4%EF%B8%8F, and "love!" as love%21.
func TestNameEncoding(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/heart":{"get":{"operationId":"heart","parameters":[{"name":"❤️","in":"query","schema":{}}]}},
		"/m/x{é t}":{"get":{"operationId":"matrix","parameters":[{"name":"é t","in":"path","required":true,"style":"matrix","schema":{}}]}},
		"/me/x{é t}":{"get":{"operationId":"matrixExplode","parameters":[{"name":"é t","in":"path","required":true,"style":"matrix","explode":true,"schema":{}}]}},
		"/d":{"get":{"operationId":"deep","parameters":[{"name":"a b[c]","in":"query","style":"deepObject","schema":{}}]}},
		"/s":{"get":{"operationId":"space","parameters":[{"name":"a&b=c","in":"query","style":"spaceDelimited","explode":false,"schema":{}}]}},
		"/f":{"get":{"operationId":"form","parameters":[{"name":"a/b","in":"query","explode":false,"schema":{}}]}},
		"/c":{"get":{"operationId":"cookie","parameters":[{"name":"k y","in":"cookie","schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	tests := []struct {
		key, param string
		v          any
		want       string
	}{
		{"heart", "❤️", "love!", "/heart?%E2%9D%A4%EF%B8%8F=love%21"},
		{"matrix", "é t", []string{"a", "b"}, "/m/x;%C3%A9%20t=a,b"},
		{"matrixExplode", "é t", []string{"a", "b"}, "/me/x;%C3%A9%20t=a;%C3%A9%20t=b"},
		{"matrixExplode", "é t", map[string]string{"k y": "v"}, "/me/x;k%20y=v"},
		{"deep", "a b[c]", map[string]any{"x[y]": "1", "z": map[string]string{"é": "2"}}, "/d?a%20b%5Bc%5D%5Bx%5By%5D%5D=1&a%20b%5Bc%5D%5Bz%5D%5B%C3%A9%5D=2"},
		{"space", "a&b=c", []string{"x", "y"}, "/s?a%26b%3Dc=x%20y"},
		{"space", "a&b=c", map[string]string{"k y": "v"}, "/s?a%26b%3Dc=k%20y%20v"},
		{"form", "a/b", map[string]string{"k=y": "v"}, "/f?a%2Fb=k%3Dy,v"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, re := callOne(t, w, c, tt.key, tt.param, tt.v)
			if re != nil {
				t.Fatalf("refused: %v", re)
			}
			if got.RequestURI != tt.want {
				t.Errorf("request target %q, want %q", got.RequestURI, tt.want)
			}
		})
	}
	got, re := callOne(t, w, c, "cookie", "k y", "v w")
	if re != nil {
		t.Fatalf("refused: %v", re)
	}
	if v := got.Header.Values("Cookie"); len(v) != 1 || v[0] != "k%20y=v%20w" {
		t.Errorf("Cookie = %q, want [k%%20y=v%%20w]", v)
	}
}

// deepObject (OAS 3.1.2 section 4.8.12.3: "Allows objects with scalar
// properties to be represented using form parameters"; section 4.8.12.6:
// color%5BR%5D=100&color%5BG%5D=200&color%5BB%5D=150), with doc.go's rules:
// "deepObject ignores explode"; "deepObject nests objects as
// a%5Bb%5D%5Bc%5D=v"; "An undefined member or array item is skipped"; members
// in the order encoding/json writes them; values in their JSON spelling.
func TestDeepObject(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/d":{"get":{"operationId":"d","parameters":[{"name":"f","in":"query","style":"deepObject","schema":{}}]}},
		"/t":{"get":{"operationId":"t","parameters":[{"name":"f","in":"query","style":"deepObject","explode":true,"schema":{}}]}},
		"/n":{"get":{"operationId":"n","parameters":[{"name":"f","in":"query","style":"deepObject","explode":false,"schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"struct order", colorRGB, "f%5BR%5D=100&f%5BG%5D=200&f%5BB%5D=150"},
		{"map sorted", colorMap, "f%5BB%5D=150&f%5BG%5D=200&f%5BR%5D=100"},
		{"nested", map[string]any{"a": map[string]any{"b": map[string]any{"c": "v"}}}, "f%5Ba%5D%5Bb%5D%5Bc%5D=v"},
		{"nested and flat, sorted", map[string]any{"z": 1, "a": map[string]any{"y": true, "b": "x"}}, "f%5Ba%5D%5Bb%5D=x&f%5Ba%5D%5By%5D=true&f%5Bz%5D=1"},
		{"null member skipped", map[string]any{"a": nil, "b": "x"}, "f%5Bb%5D=x"},
		{"nested null skipped", map[string]any{"a": map[string]any{"b": nil, "c": "x"}}, "f%5Ba%5D%5Bc%5D=x"},
		{"nested undefined object skipped", map[string]any{"a": map[string]any{"b": nil}, "c": "x"}, "f%5Bc%5D=x"},
		{"nested empty object skipped", map[string]any{"a": map[string]any{}, "c": "x"}, "f%5Bc%5D=x"},
		{"empty string member", map[string]string{"a": ""}, "f%5Ba%5D="},
		{"values encoded", map[string]string{"a": "x y&z=w/[]"}, "f%5Ba%5D=x%20y%26z%3Dw%2F%5B%5D"},
		{"numbers and booleans", map[string]any{"a": 1.5, "b": false, "c": json.Number("1e3"), "d": 1e21}, "f%5Ba%5D=1.5&f%5Bb%5D=false&f%5Bc%5D=1e3&f%5Bd%5D=1e%2B21"},
		{"struct tags", tagged{A: "1"}, "f%5Balpha%5D=1"},
	}
	for _, tt := range tests {
		for _, key := range []string{"d", "t", "n"} {
			t.Run(tt.name+"/"+key, func(t *testing.T) {
				got, re := callOne(t, w, c, key, "f", tt.v)
				if re != nil {
					t.Fatalf("refused: %v", re)
				}
				if want := "/" + key + "?" + tt.want; got.RequestURI != want {
					t.Errorf("request target %q, want %q", got.RequestURI, want)
				}
			})
		}
	}
	// "an array as or in a deepObject value unless Options.DeepObjectArrays
	// says how to write it, a primitive for ... deepObject" are refused at the
	// key, Options.DeepObjectArrays being zero; Param.Err is not set, since
	// the document alone does not decide it (the schema is {}).
	for _, v := range []any{"x", 1, true, []string{"a"}, map[string]any{"a": []int{1}}, map[string]any{"a": map[string]any{"b": []string{"c"}}}} {
		for _, key := range []string{"d", "t", "n"} {
			_, re := callOne(t, w, c, key, "f", v)
			if re == nil {
				t.Errorf("%s: %#v sent, want a refusal", key, v)
				continue
			}
			wantKeys(t, fmt.Sprintf("%s %#v Inputs", key, v), re.Inputs, true, "f")
		}
	}
	for _, key := range []string{"d", "t", "n"} {
		if p := param(t, mustOp(t, c, key), 0); p.Err != nil {
			t.Errorf("%s: Param.Err = %v; deepObject ignores explode", key, p.Err)
		}
	}
}

// spaceDelimited and pipeDelimited (OAS 3.1.2 section 4.8.12.6:
// "color=blue%20black%20brown", "color=R%20100%20G%20200%20B%20150", and %7C
// for pipeDelimited; doc.go, Fixed rules, Percent-encoding: "the
// spaceDelimited and pipeDelimited delimiters are %20 and %7C"). Items and
// member names are percent-encoded; a delimiter inside an item is encoded as
// every non-unreserved byte is.
func TestDelimitedStyles(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/s":{"get":{"operationId":"s","parameters":[{"name":"f","in":"query","style":"spaceDelimited","explode":false,"schema":{}}]}},
		"/s0":{"get":{"operationId":"s0","parameters":[{"name":"f","in":"query","style":"spaceDelimited","schema":{}}]}},
		"/p":{"get":{"operationId":"p","parameters":[{"name":"f","in":"query","style":"pipeDelimited","explode":false,"schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	tests := []struct {
		key  string
		v    any
		want string
	}{
		{"s", colors, "/s?f=blue%20black%20brown"},
		{"s", colorRGB, "/s?f=R%20100%20G%20200%20B%20150"},
		{"s", colorMap, "/s?f=B%20150%20G%20200%20R%20100"},
		{"s", []string{"a b", "c"}, "/s?f=a%20b%20c"},
		{"s", []string{"", ""}, "/s?f=%20"},
		{"s", []string{"one"}, "/s?f=one"},
		{"s", []any{1, true, 2.5}, "/s?f=1%20true%202.5"},
		{"s", map[string]any{"a": nil, "b": ""}, "/s?f=b%20"},
		// explode defaults to false for spaceDelimited (OAS 3.1.2 section
		// 4.8.12.2.2: "For all other styles, the default value is false").
		{"s0", colors, "/s0?f=blue%20black%20brown"},
		{"p", colors, "/p?f=blue%7Cblack%7Cbrown"},
		{"p", colorRGB, "/p?f=R%7C100%7CG%7C200%7CB%7C150"},
		{"p", []string{"a|b", "c"}, "/p?f=a%7Cb%7Cc"},
		{"p", []string{"x&y=z", "é"}, "/p?f=x%26y%3Dz%7C%C3%A9"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"/"+tt.want, func(t *testing.T) {
			got, re := callOne(t, w, c, tt.key, "f", tt.v)
			if re != nil {
				t.Fatalf("refused: %v", re)
			}
			if got.RequestURI != tt.want {
				t.Errorf("request target %q, want %q", got.RequestURI, tt.want)
			}
		})
	}
}

// Refusals at the parameter's key (errors.go, RequestError.Inputs: "a value its
// style cannot serialize", keyed by Param.Key): doc.go, Fixed rules, Styles:
// "Nesting in any style but deepObject is refused, and so are an array as or in
// a deepObject value unless Options.DeepObjectArrays says how to write it, a
// primitive for spaceDelimited, pipeDelimited or deepObject, explode true with
// spaceDelimited or pipeDelimited ... Each is refused at the parameter's key,
// with Param.Err set where the document alone decides it",
// Options.DeepObjectArrays being zero here. A Param.Err fails a call only when
// the call uses the parameter (describe.go, Operation.Err: "A defect in an
// optional part ... fails a call only when the call uses it"), and a required
// one refuses every call (describe.go, Param.Err).
func TestStyleRefusals(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/se":{"get":{"operationId":"se","parameters":[{"name":"f","in":"query","style":"spaceDelimited","explode":true,"schema":{}}]}},
		"/pe":{"get":{"operationId":"pe","parameters":[{"name":"f","in":"query","style":"pipeDelimited","explode":true,"schema":{}}]}},
		"/ser":{"get":{"operationId":"ser","parameters":[{"name":"f","in":"query","required":true,"style":"spaceDelimited","explode":true,"schema":{}}]}},
		"/s":{"get":{"operationId":"s","parameters":[{"name":"f","in":"query","style":"spaceDelimited","explode":false,"schema":{}}]}},
		"/p":{"get":{"operationId":"p","parameters":[{"name":"f","in":"query","style":"pipeDelimited","explode":false,"schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	for _, key := range []string{"se", "pe", "ser"} {
		if p := param(t, mustOp(t, c, key), 0); p.Err == nil {
			t.Errorf("%s: Param.Err = %v, want the undefined combination", key, p.Err)
		}
	}
	for _, key := range []string{"s", "p"} {
		if p := param(t, mustOp(t, c, key), 0); p.Err != nil {
			t.Errorf("%s: Param.Err = %v, want nil: the value decides", key, p.Err)
		}
	}
	for _, tt := range []struct {
		key string
		v   any
	}{
		{"se", []string{"a", "b"}}, {"se", map[string]string{"a": "b"}}, {"pe", []string{"a", "b"}},
		{"s", "x"}, {"s", 5}, {"s", false}, {"p", "x"}, {"p", json.Number("1")},
		{"s", [][]string{{"a"}}}, {"s", map[string]any{"a": map[string]int{"b": 1}}}, {"p", []any{"a", []string{"b"}}},
	} {
		_, re := callOne(t, w, c, tt.key, "f", tt.v)
		if re == nil {
			t.Errorf("%s with %#v was sent, want a refusal", tt.key, tt.v)
			continue
		}
		wantKeys(t, fmt.Sprintf("%s %#v Inputs", tt.key, tt.v), re.Inputs, true, "f")
	}
	// Unused, the optional defective parameter does not fail the call; a
	// required one refuses every call.
	mustCall(t, c, "se", nil, nil)
	before := w.count()
	resp, err := c.Call(t.Context(), "ser", nil, nil)
	refusedSince(t, w, before, resp, err)
}

// Refusals the RFC 6570 styles share, for each location (doc.go, Fixed
// rules, Styles: "Nesting in any style but deepObject is refused": a defined
// collection as an item or member, the undefined ones being skipped as
// Values says), keyed by Param.Key, and reported together
// (client.go, Prepare: "a *RequestError listing every problem at once").
func TestNestingRefusedEveryStyle(t *testing.T) {
	var cfgs []styleCfg
	for _, c := range styleConfigs() {
		if c.style != "deepObject" {
			cfgs = append(cfgs, c)
		}
	}
	w := newWire(t, nil)
	c := parseFor(t, w, styleDoc(cfgs), nil)
	for _, cfg := range cfgs {
		for _, v := range []any{
			[]any{nil, []int{1}}, [][]int{{1}}, []any{"a", []string{"b"}}, []any{map[string]string{"a": "b"}},
			map[string]any{"a": []int{1}}, map[string]any{"a": map[string]string{"b": "c"}}, map[string]any{"a": []any{nil, 1}},
		} {
			_, re := callOne(t, w, c, cfg.id, cfg.field(), v)
			if re == nil {
				t.Errorf("%s with %#v was sent, want a refusal", cfg.id, v)
				continue
			}
			wantKeys(t, fmt.Sprintf("%s %#v Inputs", cfg.id, v), re.Inputs, true, cfg.field())
		}
	}
	// Two refused parameters and an unknown key in one call.
	doc := doc31(`"/two/{p}":{"get":{"operationId":"two","parameters":[
		{"name":"p","in":"path","required":true,"style":"label","schema":{}},
		{"name":"q","in":"query","style":"pipeDelimited","explode":false,"schema":{}},
		{"name":"X-H","in":"header","schema":{}}]}}`)
	c2 := parseFor(t, w, doc, nil)
	before := w.count()
	resp, err := c2.Call(t.Context(), "two", &openapi.Input{Params: map[string]any{"p": [][]int{{1}}, "q": "x", "X-H": []any{map[string]int{"a": 1}}, "zz": 1}}, nil)
	re := refusedSince(t, w, before, resp, err)
	wantKeys(t, "Inputs", re.Inputs, true, "p", "q", "X-H", "zz")
}

// doc.go, Fixed rules, Percent-encoding: "A path parameter value that would
// form a whole "." or ".." segment is refused, since RFC 3986 section 5.2.4
// removes such segments before the value could reach the server". The rule
// applies to the resulting segment, however many values form it. A label
// expansion that is a whole segment forms "." from "" and ".." from "."
// (RFC 6570 section 3.2.5: X{.empty} is "X.").
func TestLabelDotSegments(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/l/{p}/z":{"get":{"operationId":"l","parameters":[{"name":"p","in":"path","required":true,"style":"label","schema":{}}]}},
		"/e/{p}/z":{"get":{"operationId":"e","parameters":[{"name":"p","in":"path","required":true,"style":"label","explode":true,"schema":{}}]}}`), nil)
	for _, tt := range []struct {
		key string
		v   any
	}{{"l", ""}, {"l", "."}, {"l", []string{""}}, {"e", []string{"", ""}}, {"e", []string{"."}}} {
		_, re := callOne(t, w, c, tt.key, "p", tt.v)
		if re == nil {
			t.Errorf("%s %#v: sent, want a refusal", tt.key, tt.v)
			continue
		}
		wantKeys(t, fmt.Sprintf("%s %#v Inputs", tt.key, tt.v), re.Inputs, true, "p")
	}
	for _, tt := range []struct {
		key  string
		v    any
		want string
	}{{"l", "a", "/l/.a/z"}, {"l", "..", "/l/.../z"}, {"e", []string{"", "", ""}, "/l/.../z"}, {"e", []string{"a", ""}, "/e/.a./z"}} {
		got, re := callOne(t, w, c, tt.key, "p", tt.v)
		want := strings.Replace(tt.want, "/l/", "/"+tt.key+"/", 1)
		if re != nil || got.RequestURI != want {
			t.Errorf("%s %#v: %q, %v; want %q", tt.key, tt.v, got.RequestURI, re, want)
		}
	}
}

// doc.go, Fixed rules, Styles: "an exploded object member whose value is ""
// is written as its name alone except in form style" (RFC 6570 section
// 3.2.1); and matrix without explode writes [""] as ";p", since RFC 6570
// section 3.2.7 appends "=" only "if the variable's value is not empty".
func TestEmptyStringMembersAndItems(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/s/{p}":{"get":{"operationId":"simple","parameters":[{"name":"p","in":"path","required":true,"explode":true,"schema":{}}]}},
		"/l/x{p}":{"get":{"operationId":"label","parameters":[{"name":"p","in":"path","required":true,"style":"label","explode":true,"schema":{}}]}},
		"/m/x{p}":{"get":{"operationId":"matrix","parameters":[{"name":"p","in":"path","required":true,"style":"matrix","explode":true,"schema":{}}]}},
		"/f":{"get":{"operationId":"form","parameters":[{"name":"p","in":"query","schema":{}}]}},
		"/h":{"get":{"operationId":"header","parameters":[{"name":"X-P","in":"header","explode":true,"schema":{}}]}},
		"/c":{"get":{"operationId":"cookie","parameters":[{"name":"p","in":"cookie","schema":{}}]}},
		"/s2/{p}":{"get":{"operationId":"simple2","parameters":[{"name":"p","in":"path","required":true,"schema":{}}]}},
		"/l2/x{p}":{"get":{"operationId":"label2","parameters":[{"name":"p","in":"path","required":true,"style":"label","schema":{}}]}},
		"/m2/x{p}":{"get":{"operationId":"matrix2","parameters":[{"name":"p","in":"path","required":true,"style":"matrix","schema":{}}]}},
		"/f2":{"get":{"operationId":"form2","parameters":[{"name":"p","in":"query","explode":false,"schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	member := map[string]string{"a": "", "b": "x"}
	tests := []struct {
		key, param string
		v          any
		want       string // the request target, or the header or Cookie value
	}{
		// Exploded, the member "a" with "" is "a" except in form style.
		{"simple", "p", member, "/s/a,b=x"},
		{"label", "p", member, "/l/x.a.b=x"},
		{"matrix", "p", member, "/m/x;a;b=x"},
		{"header", "X-P", member, "a,b=x"},
		{"form", "p", member, "/f?a=&b=x"},
		{"cookie", "p", member, "a=; b=x"},
		// Without explode, pairs are "name,value" whatever the value.
		{"simple2", "p", member, "/s2/a,,b,x"},
		{"matrix2", "p", member, "/m2/x;p=a,,b,x"},
		{"form2", "p", member, "/f2?p=a,,b,x"},
		// [""] under matrix without explode is ";p"; with explode each
		// empty item is the name alone (ifemp ""); form writes "p=".
		{"matrix2", "p", []string{""}, "/m2/x;p"},
		{"matrix", "p", []string{""}, "/m/x;p"},
		{"matrix", "p", []string{"", "a"}, "/m/x;p;p=a"},
		{"label2", "p", []string{""}, "/l2/x."},
		{"simple2", "p", []string{""}, "/s2/"},
		{"form2", "p", []string{""}, "/f2?p="},
		{"form", "p", []string{""}, "/f?p="},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s %v", tt.key, tt.v), func(t *testing.T) {
			got, re := callOne(t, w, c, tt.key, tt.param, tt.v)
			if re != nil {
				t.Fatalf("refused: %v", re)
			}
			switch tt.key {
			case "header":
				if v := got.Header.Values("X-P"); len(v) != 1 || v[0] != tt.want {
					t.Errorf("X-P = %q, want [%q]", v, tt.want)
				}
			case "cookie":
				if v := got.Header.Values("Cookie"); len(v) != 1 || v[0] != tt.want {
					t.Errorf("Cookie = %q, want [%q]", v, tt.want)
				}
			default:
				if got.RequestURI != tt.want {
					t.Errorf("request target %q, want %q", got.RequestURI, tt.want)
				}
			}
		})
	}
}

// doc.go, Fixed rules, Styles: "Whether a value is undefined (see Values)
// is settled first; the refusals here apply to defined values": an empty
// array under deepObject, a typed nil under spaceDelimited, pipeDelimited
// and deepObject, and an empty array or object nested as a member are
// undefined: omitted when optional, missing when required, skipped as
// members; none is refused as nesting or as a primitive. An undefined array
// item (null, [] or {}) is skipped too (doc.go, Values: "An undefined member
// or array item is skipped, as RFC 6570 section 3.2.1 expands only defined
// ones"), in every style; a defined collection as an item is still refused
// as nesting.
func TestUndefinedSettledFirst(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/d":{"get":{"operationId":"deep","parameters":[{"name":"p","in":"query","style":"deepObject","schema":{}}]}},
		"/dr":{"get":{"operationId":"deepReq","parameters":[{"name":"p","in":"query","required":true,"style":"deepObject","schema":{}}]}},
		"/sd":{"get":{"operationId":"space","parameters":[{"name":"p","in":"query","style":"spaceDelimited","explode":false,"schema":{}}]}},
		"/sdr":{"get":{"operationId":"spaceReq","parameters":[{"name":"p","in":"query","required":true,"style":"spaceDelimited","explode":false,"schema":{}}]}},
		"/pd":{"get":{"operationId":"pipe","parameters":[{"name":"p","in":"query","style":"pipeDelimited","explode":false,"schema":{}}]}},
		"/pdr":{"get":{"operationId":"pipeReq","parameters":[{"name":"p","in":"query","required":true,"style":"pipeDelimited","explode":false,"schema":{}}]}},
		"/f":{"get":{"operationId":"form","parameters":[{"name":"p","in":"query","schema":{}}]}},
		"/fn":{"get":{"operationId":"formNo","parameters":[{"name":"p","in":"query","explode":false,"schema":{}}]}},
		"/s/{p}":{"get":{"operationId":"simple","parameters":[{"name":"p","in":"path","required":true,"schema":{}}]}},
		"/h":{"get":{"operationId":"header","parameters":[{"name":"X-P","in":"header","explode":true,"schema":{}}]}},
		"/c":{"get":{"operationId":"cookie","parameters":[{"name":"p","in":"cookie","schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	nested := map[string]any{"a": []int{}, "b": map[string]any{}, "c": "x", "d": map[string]any{"e": []string{}}}
	onlyNested := map[string]any{"a": []int{}, "b": map[string]any{"c": map[string]any{}}}
	sentTests := []struct {
		key, param string
		v          any
		want       string // the request target, or the header or Cookie value; "" for none
	}{
		{"deep", "p", []int{}, "/d"},
		{"deep", "p", (*int)(nil), "/d"},
		{"deep", "p", map[string]any(nil), "/d"},
		{"deep", "p", onlyNested, "/d"},
		{"deep", "p", nested, "/d?p%5Bc%5D=x"},
		{"space", "p", (*[]string)(nil), "/sd"},
		{"space", "p", nested, "/sd?p=c%20x"},
		{"pipe", "p", (*[]string)(nil), "/pd"},
		{"form", "p", nested, "/f?c=x"},
		{"form", "p", onlyNested, "/f"},
		{"formNo", "p", nested, "/fn?p=c,x"},
		{"header", "X-P", nested, "c=x"},
		{"header", "X-P", onlyNested, ""},
		{"cookie", "p", nested, "c=x"},
		{"cookie", "p", onlyNested, ""},
	}
	for _, tt := range sentTests {
		t.Run(fmt.Sprintf("%s %#v", tt.key, tt.v), func(t *testing.T) {
			got, re := callOne(t, w, c, tt.key, tt.param, tt.v)
			if re != nil {
				t.Fatalf("refused: %v", re)
			}
			switch tt.key {
			case "header", "cookie":
				name := map[string]string{"header": "X-P", "cookie": "Cookie"}[tt.key]
				v := got.Header.Values(name)
				if tt.want == "" {
					if v != nil {
						t.Errorf("%s = %q, want none", name, v)
					}
					return
				}
				if len(v) != 1 || v[0] != tt.want {
					t.Errorf("%s = %q, want [%q]", name, v, tt.want)
				}
			default:
				if got.RequestURI != tt.want {
					t.Errorf("request target %q, want %q", got.RequestURI, tt.want)
				}
			}
		})
	}
	// Required: missing at the key.
	for _, tt := range []struct {
		key string
		v   any
	}{
		{"deepReq", []int{}}, {"deepReq", (*int)(nil)}, {"deepReq", onlyNested},
		{"spaceReq", (*int)(nil)}, {"spaceReq", onlyNested}, {"pipeReq", (*[]int)(nil)},
		{"simple", onlyNested}, {"simple", map[string]any{"a": map[string]any{}}},
	} {
		_, re := callOne(t, w, c, tt.key, "p", tt.v)
		if re == nil {
			t.Errorf("%s %#v: sent, want the required parameter missing", tt.key, tt.v)
			continue
		}
		wantKeys(t, fmt.Sprintf("%s %#v Inputs", tt.key, tt.v), re.Inputs, true, "p")
	}
	// Undefined array items: skipped, the defined members written as the
	// style writes them.
	items := []any{"a", nil, []int{}, map[string]any{}, "b"}
	for _, tt := range []struct {
		key, param string
		v          any
		want       string
	}{
		{"form", "p", items, "/f?p=a&p=b"},
		{"formNo", "p", items, "/fn?p=a,b"},
		{"formNo", "p", []any{"a", map[string]any{"b": nil}}, "/fn?p=a"},
		{"pipe", "p", items, "/pd?p=a%7Cb"},
		{"space", "p", []any{nil, "a"}, "/sd?p=a"},
		{"simple", "p", items, "/s/a,b"},
		{"header", "X-P", items, "a,b"},
		{"cookie", "p", items, "p=a; p=b"},
	} {
		got, re := callOne(t, w, c, tt.key, tt.param, tt.v)
		if re != nil {
			t.Errorf("%s %#v: refused: %v; want %q", tt.key, tt.v, re, tt.want)
			continue
		}
		switch tt.key {
		case "header":
			if v := got.Header.Values("X-P"); len(v) != 1 || v[0] != tt.want {
				t.Errorf("X-P = %q, want [%q]", v, tt.want)
			}
		case "cookie":
			if v := got.Header.Values("Cookie"); len(v) != 1 || v[0] != tt.want {
				t.Errorf("Cookie = %q, want [%q]", v, tt.want)
			}
		default:
			if got.RequestURI != tt.want {
				t.Errorf("%s: request target %q, want %q", tt.key, got.RequestURI, tt.want)
			}
		}
	}
	// A defined collection as an item is still nesting, refused at the key.
	for _, tt := range []struct {
		key, param string
		v          any
	}{
		{"form", "p", []any{"a", nil, []string{"b"}}},
		{"formNo", "p", []any{map[string]any{"k": "v"}, nil}},
		{"simple", "p", []any{[]any{nil, "b"}}},
		{"header", "X-P", []any{"a", map[string]any{"b": 1}}},
	} {
		_, re := callOne(t, w, c, tt.key, tt.param, tt.v)
		if re == nil {
			t.Errorf("%s %#v: sent, want a refusal", tt.key, tt.v)
			continue
		}
		wantKeys(t, fmt.Sprintf("%s %#v Inputs", tt.key, tt.v), re.Inputs, true, tt.param)
	}
}
