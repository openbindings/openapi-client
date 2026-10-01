package openapi_test

import (
	"errors"
	"testing"
)

// Param descriptors for every OpenAPI 3.1 parameter serialization
// (describe.go, Param): Style "as declared or as OpenAPI defaults it for the
// location ("form" for query and cookie, "simple" for path and header), or
// empty when the value is serialized by ContentType instead"; "Explode is
// the effective explode, and ExplodeSet whether the document writes it" (OAS
// 3.1.2 section 4.8.12.2.2: "When style is "form", the default value is
// true. For all other styles, the default value is false"); AllowReserved
// "the effective allowReserved: false where the edition or the media type
// ignores it";
// ContentType for a content parameter; Err "why built-in serialization
// cannot use the value" (doc.go, Fixed rules, Styles: set "where the
// document alone decides it"). Explode is true for deepObject, whatever the
// document writes (stage 2 ledger, Q6; describe.go, Param.Explode: "true for
// deepObject, which ignores the field").
func TestParamDescriptorsStage2(t *testing.T) {
	doc := bare31(`"/d/{s}/{m}/{me}/{l}/{lf}/{pc}":{"get":{"operationId":"d","parameters":[
		{"name":"s","in":"path","required":true,"schema":{}},
		{"name":"m","in":"path","required":true,"style":"matrix","schema":{}},
		{"name":"me","in":"path","required":true,"style":"matrix","explode":true,"schema":{}},
		{"name":"l","in":"path","required":true,"style":"label","schema":{}},
		{"name":"lf","in":"path","required":true,"style":"label","explode":false,"schema":{}},
		{"name":"pc","in":"path","required":true,"content":{"text/plain":{}}},
		{"name":"f","in":"query","schema":{}},
		{"name":"fn","in":"query","style":"form","explode":false,"schema":{}},
		{"name":"fr","in":"query","allowReserved":true,"schema":{}},
		{"name":"sd","in":"query","style":"spaceDelimited","schema":{}},
		{"name":"sdf","in":"query","style":"spaceDelimited","explode":false,"allowReserved":true,"schema":{}},
		{"name":"sdt","in":"query","style":"spaceDelimited","explode":true,"schema":{}},
		{"name":"pd","in":"query","style":"pipeDelimited","schema":{}},
		{"name":"pdt","in":"query","style":"pipeDelimited","explode":true,"schema":{}},
		{"name":"do","in":"query","style":"deepObject","schema":{}},
		{"name":"dof","in":"query","style":"deepObject","explode":false,"schema":{}},
		{"name":"dot","in":"query","style":"deepObject","explode":true,"allowReserved":true,"schema":{}},
		{"name":"qc","in":"query","content":{"application/json":{}}},
		{"name":"X-S","in":"header","schema":{}},
		{"name":"X-E","in":"header","style":"simple","explode":true,"schema":{}},
		{"name":"X-C","in":"header","content":{"application/json":{}}},
		{"name":"c","in":"cookie","schema":{}},
		{"name":"cf","in":"cookie","explode":false,"allowReserved":true,"schema":{}},
		{"name":"cc","in":"cookie","content":{"text/plain":{}}}
	]}}`)
	op := mustOp(t, parseAt(t, doc, "", testDocURI, nil), "d")
	type desc struct {
		style               string
		explode, explodeSet bool
		allowReserved       bool
		contentType         string
		err                 bool
		skipExplode         bool
	}
	want := map[string]desc{
		"s":   {style: "simple"},
		"m":   {style: "matrix"},
		"me":  {style: "matrix", explode: true, explodeSet: true},
		"l":   {style: "label"},
		"lf":  {style: "label", explodeSet: true},
		"pc":  {contentType: "text/plain", skipExplode: true},
		"f":   {style: "form", explode: true},
		"fn":  {style: "form", explodeSet: true},
		"fr":  {style: "form", explode: true, allowReserved: true},
		"sd":  {style: "spaceDelimited"},
		"sdf": {style: "spaceDelimited", explodeSet: true, allowReserved: true},
		"sdt": {style: "spaceDelimited", explode: true, explodeSet: true, err: true},
		"pd":  {style: "pipeDelimited"},
		"pdt": {style: "pipeDelimited", explode: true, explodeSet: true, err: true},
		"do":  {style: "deepObject", explode: true},
		"dof": {style: "deepObject", explode: true, explodeSet: true},
		"dot": {style: "deepObject", explode: true, explodeSet: true, allowReserved: true},
		"qc":  {contentType: "application/json", skipExplode: true},
		"X-S": {style: "simple"},
		"X-E": {style: "simple", explode: true, explodeSet: true},
		"X-C": {contentType: "application/json", skipExplode: true},
		"c":   {style: "form", explode: true},
		"cf":  {style: "form", explodeSet: true},
		"cc":  {contentType: "text/plain", skipExplode: true},
	}
	if len(op.Params) != len(want) {
		t.Fatalf("%d Params, want %d", len(op.Params), len(want))
	}
	for _, p := range op.Params {
		d, ok := want[p.Key]
		if !ok {
			t.Errorf("unexpected parameter %q", p.Key)
			continue
		}
		if p.Style != d.style {
			t.Errorf("%s: Style = %q, want %q", p.Key, p.Style, d.style)
		}
		if !d.skipExplode && p.Explode != d.explode {
			t.Errorf("%s: Explode = %t, want %t", p.Key, p.Explode, d.explode)
		}
		if p.ExplodeSet != d.explodeSet {
			t.Errorf("%s: ExplodeSet = %t, want %t", p.Key, p.ExplodeSet, d.explodeSet)
		}
		if p.AllowReserved != d.allowReserved {
			t.Errorf("%s: AllowReserved = %t, want %t", p.Key, p.AllowReserved, d.allowReserved)
		}
		if p.ContentType != d.contentType {
			t.Errorf("%s: ContentType = %q, want %q", p.Key, p.ContentType, d.contentType)
		}
		switch {
		case d.err && (p.Err == nil || errors.Is(p.Err, errors.ErrUnsupported)):
			// An undefined combination is the document's, not a missing
			// feature (stage 1 ledger, F9).
			t.Errorf("%s: Err = %v, want the undefined combination", p.Key, p.Err)
		case !d.err && p.Err != nil:
			t.Errorf("%s: Err = %v, want nil", p.Key, p.Err)
		}
	}
}
