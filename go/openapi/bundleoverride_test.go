package openapi_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// overrideBad and overrideGood are the content of a header parameter: one
// whose encoding map is written as a reference, and one without encoding.
const (
	overrideBad  = `"content":{"multipart/mixed":{"schema":{"type":"object"},"encoding":{"$ref":"e.yaml"}}}`
	overrideGood = `"content":{"multipart/mixed":{"schema":{"type":"object"}}}`
)

// An operation's parameter overrides the Path Item's with the same location and
// name: describe.go, Operation.Params is "the path item's and the operation's
// merged", and doc.go, Fixed rules, Order, "an overriding parameter (one with
// the same location and name, a header parameter's name, if a valid field name,
// compared without regard to case, as HTTP compares field names) taking the
// place of the one it overrides". An overridden parameter is not the
// operation's, so a value written as a reference in it is held by no part of
// that operation: an operation whose own Accept overrides a path-level Accept
// holding one has no Err and its calls are sent, while a sibling that inherits
// the path-level Accept, which no Param describes (Operation.Params holds "no
// header parameter that OpenAPI 3.x tells clients to ignore"), is unusable
// ("another value whose nearest part is the operation"), every call refused
// wrapping its Err. For an ordinary header, the inheriting sibling describes
// the parameter, whose Err says to bundle, and the overriding one describes its
// own, usable. Inline parameters and components behave alike.
func TestBundleIgnoredHeaderOverride(t *testing.T) {
	ref := func(name string) string { return `{"$ref":"#/components/parameters/` + name + `"}` }
	var cases []shapeCase
	for _, v := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		doc := shapeDoc(v, `
			"/b":{"parameters":[`+ref("Bad")+`],
				"get":{"operationId":"bInherits",`+partErrResponse+`},
				"put":{"operationId":"bOverrides","parameters":[`+ref("Good")+`],`+partErrResponse+`}},
			"/c":{"parameters":[`+ref("BadX")+`],
				"get":{"operationId":"cInherits",`+partErrResponse+`},
				"put":{"operationId":"cOverrides","parameters":[`+ref("GoodX")+`],`+partErrResponse+`}},
			"/e":{"parameters":[{"name":"Accept","in":"header",`+overrideBad+`}],
				"get":{"operationId":"eInherits",`+partErrResponse+`},
				"put":{"operationId":"eOverrides","parameters":[{"name":"accept","in":"header",`+overrideGood+`}],`+partErrResponse+`}}`,
			`"components":{"parameters":{
				"Bad":{"name":"Accept","in":"header",`+overrideBad+`},"Good":{"name":"accept","in":"header",`+overrideGood+`},
				"BadX":{"name":"X-H","in":"header",`+overrideBad+`},"GoodX":{"name":"x-h","in":"header",`+overrideGood+`}}}`)
		inherits := func(t *testing.T, op *openapi.Operation) {
			wantBundled(t, "Operation", op.Err)
			if len(op.Params) != 0 {
				t.Errorf("Params %q; want the ignored header parameter not described", paramNames(op.Params))
			}
		}
		overrides := func(t *testing.T, op *openapi.Operation) {
			wantUsable(t, "Operation", op.Err)
			if len(op.Params) != 0 {
				t.Errorf("Params %q; want the ignored header parameter not described", paramNames(op.Params))
			}
		}
		ordinary := func(name string, bundled bool) func(*testing.T, *openapi.Operation) {
			return func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				if len(op.Params) != 1 || op.Params[0].Name != name {
					t.Fatalf("Params %q; want %s alone", paramNames(op.Params), name)
				}
				if bundled {
					wantBundled(t, "parameter "+name, op.Params[0].Err)
				} else {
					wantUsable(t, "parameter "+name, op.Params[0].Err)
				}
			}
		}
		refusedOp := []refusal{{"a call", nil, "Err", "", opErr}}
		cases = append(cases,
			shapeCase{orderCase: orderCase{name: v + " component Accept, inherited", key: "bInherits", bad: refusedOp}, doc: doc, check: inherits},
			shapeCase{orderCase: orderCase{name: v + " component Accept, overridden", key: "bOverrides", ok: &openapi.Input{}}, doc: doc, check: overrides},
			shapeCase{orderCase: orderCase{name: v + " inline Accept, inherited", key: "eInherits", bad: refusedOp}, doc: doc, check: inherits},
			shapeCase{orderCase: orderCase{name: v + " inline Accept, overridden", key: "eOverrides", ok: &openapi.Input{}}, doc: doc, check: overrides},
			shapeCase{orderCase: orderCase{name: v + " ordinary header, inherited", key: "cInherits", ok: &openapi.Input{},
				bad: []refusal{{"a value", &openapi.Input{Params: map[string]any{"X-H": map[string]string{"a": "x"}}}, "Inputs", "X-H", paramNamed("X-H")}}},
				doc: doc, check: ordinary("X-H", true)},
			shapeCase{orderCase: orderCase{name: v + " ordinary header, overridden", key: "cOverrides", ok: &openapi.Input{}}, doc: doc, check: ordinary("x-h", false)},
		)
	}
	runShapeCases(t, cases)
}

// pathLevelDoc is an OpenAPI 3.2.1 document with one Path Item whose inline
// path-level header parameter, named name, has a multipart content Media
// holding e encoding entries, and n additional operations inheriting it.
func pathLevelDoc(name string, n, e int) []byte {
	entries := make([]string, e)
	for i := range entries {
		entries[i] = fmt.Sprintf(`"e%d":{"contentType":"text/plain"}`, i)
	}
	ops := make([]string, n)
	for i := range ops {
		ops[i] = fmt.Sprintf(`"M%d":{"operationId":"op%d",%s}`, i, i, partErrResponse)
	}
	return []byte(shapeDoc("3.2.1", fmt.Sprintf(`"/p":{"parameters":[{"name":%q,"in":"header","content":{"multipart/mixed":{"schema":{"type":"object"},"encoding":{%s}}}}],"additionalOperations":{%s}}`,
		name, strings.Join(entries, ","), strings.Join(ops, ","))))
}

// Regression check, not contract: describing searches each object of the
// document for values written as references a bounded number of times
// (TestBundleCostNestedHeaderReferences), so that search of a path-level
// parameter's content runs once, not once for each operation inheriting it,
// whether the parameter is described or, as an ignored Accept, is not. Other
// work on the content, such as the fields each operation compiles from a
// form-content parameter, is not bounded here: the content is multipart/mixed,
// which cannot serialize a parameter (doc.go, Values), so its entries describe
// no fields and the search is all they cost. The measure is what its e encoding
// entries add to describing: with sixteen times the operations, it must stay
// within 3 times (flatBound), in time, not checked under -short, and in bytes,
// a difference below 1 ms or 16 bytes an entry counting as that floor.
func TestBundleCostPathLevelParameter(t *testing.T) {
	const entries, small = 4000, 16
	for _, name := range []string{"X-H", "Accept"} {
		t.Run(name, func(t *testing.T) {
			added := func(n int) (time.Duration, int64) {
				with, without := bestCosts(timedOperations(t, pathLevelDoc(name, n, entries)), timedOperations(t, pathLevelDoc(name, n, 0)))
				return with.d - without.d, int64(with.bytes) - int64(without.bytes)
			}
			ds, bs := added(small)
			dl, bl := added(16 * small)
			timeRatio := float64(dl) / float64(max(ds, time.Millisecond))
			byteRatio := float64(bl) / float64(max(bs, 16*entries))
			t.Logf("entries add %v, %d bytes beside %d operations; %v, %d bytes beside %d (time %.1fx, bytes %.1fx)", ds, bs, small, dl, bl, 16*small, timeRatio, byteRatio)
			if byteRatio > flatBound {
				t.Errorf("beside sixteen times the operations, the entries allocated %.1f times the bytes; want them read once", byteRatio)
			}
			if !testing.Short() && timeRatio > flatBound {
				t.Errorf("beside sixteen times the operations, the entries took %.1f times as long; want them read once", timeRatio)
			}
		})
	}
}
