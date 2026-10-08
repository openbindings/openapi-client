package schema2020_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openbindings/openapi-client/go/openapi"
	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

const (
	entryURI  = "https://api.example.test/v1/openapi.json"
	modelsURI = "https://api.example.test/v1/models.json"
	commonURI = "https://api.example.test/shared/common.json"
	cdnOrigin = "https://cdn.example.test"
	cdnURI    = "https://cdn.example.test/lib/types.json"

	dialect2020   = "https://json-schema.org/draft/2020-12/schema"
	dialectOAS31  = "https://spec.openapis.org/oas/3.1/dialect/base"
	dialectOAS32  = "https://spec.openapis.org/oas/3.2/dialect/2025-09-17"
	dialectDraft7 = "http://json-schema.org/draft-07/schema#"
)

// The editions, each at the patch release its minor line is read by.
const (
	v20 = "2.0"
	v30 = "3.0.4"
	v31 = "3.1.2"
	v32 = "3.2.1"
)

var (
	editions       = []string{v20, v30, v31, v32}
	legacyEditions = []string{v20, v30}
	modernEditions = []string{v31, v32}
	formats        = []string{"json", "yaml"}
	directions     = []schema2020.Direction{schema2020.Neutral, schema2020.Request, schema2020.Response}
)

func dirName(d schema2020.Direction) string {
	switch d {
	case schema2020.Neutral:
		return "Neutral"
	case schema2020.Request:
		return "Request"
	case schema2020.Response:
		return "Response"
	}
	return fmt.Sprintf("Direction(%d)", d)
}

func isModern(v string) bool { return strings.HasPrefix(v, "3.1") || strings.HasPrefix(v, "3.2") }

// is30 reports an OpenAPI 3.0 version, whatever its patch.
func is30(v string) bool { return strings.HasPrefix(v, "3.0") }

// compPath is where an edition keeps reusable schemas.
func compPath(v string) string {
	if v == v20 {
		return "#/definitions/"
	}
	return "#/components/schemas/"
}

// ed rewrites each "#C/" in a fixture text to the edition's component
// path, so that one fixture serves every edition.
func ed(v, s string) string { return strings.ReplaceAll(s, "#C/", compPath(v)) }

// docText builds a document of version v from Paths members and schema
// components, both JSON member lists in which "#C/" names a component.
func docText(v, paths, schemas string) string {
	paths, schemas = ed(v, paths), ed(v, schemas)
	if v == v20 {
		return `{"swagger":"2.0","info":{"title":"T","version":"1"},"host":"api.example.test","paths":{` + paths + `},"definitions":{` + schemas + `}}`
	}
	return `{"openapi":"` + v + `","info":{"title":"T","version":"1"},"paths":{` + paths + `},"components":{"schemas":{` + schemas + `}}}`
}

// load parses entry as the document at entryURI, serving others by URI,
// with every document written in the given format.
func load(t testing.TB, format, entry string, others map[string]string) *openapi.Client {
	t.Helper()
	return loadWith(t, format, entry, others, nil)
}

func loadWith(t testing.TB, format, entry string, others map[string]string, origins []string) *openapi.Client {
	t.Helper()
	content := encode(t, format, entry)
	served := make(map[string]string, len(others))
	for uri, d := range others {
		served[uri] = encode(t, format, d)
	}
	l := &openapi.Loader{Fetch: serve(served), Origins: origins}
	c, err := l.Parse(context.Background(), []byte(content), entryURI, nil)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, content)
	}
	return c
}

// serve is a Loader.Fetch over documents held in memory.
func serve(docs map[string]string) func(context.Context, string) (io.ReadCloser, string, error) {
	return func(_ context.Context, uri string) (io.ReadCloser, string, error) {
		d, ok := docs[uri]
		if !ok {
			return nil, "", fmt.Errorf("no document at %s", uri)
		}
		return io.NopCloser(strings.NewReader(d)), "", nil
	}
}

func encode(t testing.TB, format, doc string) string {
	t.Helper()
	if format != "yaml" {
		return doc
	}
	y, err := toYAML(doc)
	if err != nil {
		t.Fatalf("toYAML: %v\n%s", err, doc)
	}
	return y
}

// schemaAt returns the schema Client.Schema finds at uri, which may be
// relative to entryURI.
func schemaAt(t testing.TB, c *openapi.Client, uri string) *openapi.Schema {
	t.Helper()
	s, err := c.Schema(abs(uri))
	if err != nil || s == nil {
		t.Fatalf("Schema(%q) = %v, %v", abs(uri), s, err)
	}
	return s
}

// comp is the component name of edition v in the entry document.
func comp(t testing.TB, c *openapi.Client, v, name string) *openapi.Schema {
	t.Helper()
	return schemaAt(t, c, compPath(v)+name)
}

func abs(uri string) string {
	base, _ := url.Parse(entryURI)
	ref, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	return base.ResolveReference(ref).String()
}

// project calls Project, requires success and checks the invariants.
func project(t testing.TB, c *openapi.Client, s *openapi.Schema, d schema2020.Direction) *schema2020.Projection {
	t.Helper()
	p, err := schema2020.Project(c, s, d)
	if err != nil {
		t.Fatalf("Project(%s, %s): %v", s.Source(), dirName(d), err)
	}
	checkProjection(t, c, s, d, p, nil)
	return p
}

// projectLossy calls Project, requires an *Error, and checks the
// invariants.
func projectLossy(t testing.TB, c *openapi.Client, s *openapi.Schema, d schema2020.Direction) (*schema2020.Projection, []schema2020.Issue) {
	t.Helper()
	p, err := schema2020.Project(c, s, d)
	var pe *schema2020.Error
	if !errors.As(err, &pe) {
		t.Fatalf("Project(%s, %s): error %v, want a *schema2020.Error", s.Source(), dirName(d), err)
	}
	if p == nil {
		t.Fatalf("Project(%s, %s) returned no Projection with its *Error", s.Source(), dirName(d))
	}
	checkProjection(t, c, s, d, p, err)
	return p, pe.Issues
}

// ---- JSON values ----

func decode(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("data after the JSON value")
	}
	return v, nil
}

// jstr is s as a JSON string.
func jstr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func mustDecode(t testing.TB, what string, b []byte) any {
	t.Helper()
	v, err := decode(b)
	if err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", what, err, b)
	}
	return v
}

// canon writes v in one spelling: members sorted, numbers as the exact
// rational they denote, so equal JSON values have equal canonical forms.
func canon(v any) string {
	var b strings.Builder
	var write func(v any)
	write = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteByte('{')
			for i, k := range keys {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(strconv.Quote(k))
				b.WriteByte(':')
				write(x[k])
			}
			b.WriteByte('}')
		case []any:
			b.WriteByte('[')
			for i, e := range x {
				if i > 0 {
					b.WriteByte(',')
				}
				write(e)
			}
			b.WriteByte(']')
		case json.Number:
			r, ok := new(big.Rat).SetString(string(x))
			if !ok {
				b.WriteString("NaN(" + string(x) + ")")
				return
			}
			b.WriteString(r.RatString())
		case string:
			b.WriteString(strconv.Quote(x))
		case bool:
			b.WriteString(strconv.FormatBool(x))
		case nil:
			b.WriteString("null")
		default:
			fmt.Fprintf(&b, "%#v", x)
		}
	}
	write(v)
	return b.String()
}

func sameValue(t testing.TB, what string, got, want any) bool {
	t.Helper()
	if g, w := canon(got), canon(want); g != w {
		t.Errorf("%s:\n got %s\nwant %s", what, g, w)
		return false
	}
	return true
}

// clone copies a decoded JSON value.
func clone(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = clone(e)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = clone(e)
		}
		return a
	}
	return v
}

// ---- JSON Pointers (RFC 6901) ----

func parsePointer(p string) ([]string, error) {
	if p == "" {
		return nil, nil
	}
	if !strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("JSON Pointer %q does not start with /", p)
	}
	parts := strings.Split(p[1:], "/")
	for i, part := range parts {
		tok, ok := unescapeToken(part)
		if !ok {
			return nil, fmt.Errorf("JSON Pointer %q has a bad escape", p)
		}
		parts[i] = tok
	}
	return parts, nil
}

func pointer(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(escapeToken(t))
	}
	return b.String()
}

func escapeToken(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func unescapeToken(s string) (string, bool) {
	if !strings.Contains(s, "~") {
		return s, true
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '~' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) || (s[i+1] != '0' && s[i+1] != '1') {
			return "", false
		}
		if s[i+1] == '0' {
			b.WriteByte('~')
		} else {
			b.WriteByte('/')
		}
		i++
	}
	return b.String(), true
}

// ---- references to Defs keys ----

// fragmentByte reports whether c may stand for itself in a URI fragment
// (RFC 3986 sections 2.3, 2.2 and 3.5: unreserved, sub-delims, ":", "@",
// "/" and "?").
func fragmentByte(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("-._~!$&'()*+,;=:@/?", c) >= 0
}

func hexDigit(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// refFor is the reference to key with no more percent-encoding than RFC
// 3986 section 3.5 requires (Project: "a key is escaped as a JSON Pointer
// token and then as RFC 3986 section 3.5 requires of a fragment").
func refFor(key string) string {
	tok := escapeToken(key)
	var b strings.Builder
	b.WriteString("#/$defs/")
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if fragmentByte(c) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// keyOfRef reads the key a "#/$defs/KEY" reference names. It fails for
// any other form, for a fragment that RFC 3986 section 3.5 does not
// allow, and for a token that is not UTF-8 or not a JSON Pointer token.
func keyOfRef(ref string) (string, error) {
	rest, ok := strings.CutPrefix(ref, "#/$defs/")
	if !ok {
		return "", fmt.Errorf("reference %q is not of the form #/$defs/KEY", ref)
	}
	var b []byte
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c == '/':
			return "", fmt.Errorf("reference %q names more than one token", ref)
		case c == '%':
			if i+2 >= len(rest) || !hexDigit(rest[i+1]) || !hexDigit(rest[i+2]) {
				return "", fmt.Errorf("reference %q has a bad percent-encoding", ref)
			}
			n, _ := strconv.ParseUint(rest[i+1:i+3], 16, 8)
			b = append(b, byte(n))
			i += 2
		case fragmentByte(c):
			b = append(b, c)
		default:
			return "", fmt.Errorf("reference %q has %q, which RFC 3986 section 3.5 requires to be percent-encoded", ref, c)
		}
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("reference %q is not UTF-8 when decoded", ref)
	}
	key, ok := unescapeToken(string(b))
	if !ok {
		return "", fmt.Errorf("reference %q is not a JSON Pointer token", ref)
	}
	return key, nil
}

// normalizeRefs rewrites every "#/$defs/..." string in v to refFor of
// the key it names, so that comparisons do not depend on optional
// percent-encoding. Strings that do not decode are left as they are.
func normalizeRefs(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalizeRefs(e)
		}
	case []any:
		for i, e := range x {
			x[i] = normalizeRefs(e)
		}
	case string:
		if strings.HasPrefix(x, "#/$defs/") {
			if key, err := keyOfRef(x); err == nil {
				return refFor(key)
			}
		}
	}
	return v
}

// ---- schema positions in JSON Schema 2020-12 (Core section 10) ----

var (
	mapKeywords        = []string{"properties", "patternProperties", "dependentSchemas", "$defs"}
	arrayKeywords      = []string{"allOf", "anyOf", "oneOf", "prefixItems"}
	singleKeywords     = []string{"additionalProperties", "propertyNames", "items", "contains", "unevaluatedItems", "unevaluatedProperties", "not", "if", "then", "else", "contentSchema"}
	identifierKeywords = []string{"$id", "$schema", "$anchor", "$dynamicAnchor", "$defs"}
)

// eachSchema calls fn for every schema in the tree rooted at v, which is
// a schema, objects and booleans alike, parents before children, with the
// JSON Pointer tokens from v. fn may delete members of an object before
// its children are visited.
func eachSchema(v any, fn func(ptr []string, node any)) {
	var walk func(v any, ptr []string)
	walk = func(v any, ptr []string) {
		fn(ptr, v)
		obj, ok := v.(map[string]any)
		if !ok {
			return
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child, ok := obj[k]
			if !ok {
				continue
			}
			at := func(more ...string) []string { return append(append(slices.Clip(ptr), k), more...) }
			switch {
			case slices.Contains(mapKeywords, k):
				if m, ok := child.(map[string]any); ok {
					for name, s := range m {
						walk(s, at(name))
					}
				}
			case slices.Contains(arrayKeywords, k):
				if a, ok := child.([]any); ok {
					for i, s := range a {
						walk(s, at(strconv.Itoa(i)))
					}
				}
			case slices.Contains(singleKeywords, k) && !isArray(child):
				// An array is not a schema; JSON Schema 2020-12 has no
				// array form of items (Core section 10.3.1.2).
				walk(child, at())
			}
		}
	}
	walk(v, nil)
}

func isArray(v any) bool { _, ok := v.([]any); return ok }

// schemaPositions reports, for each prefix of tokens from a schema, whether
// it is a schema position: positions[i] for tokens[:i]. A token after a
// map or array keyword is a name or index; a token after any other
// keyword leaves the schemas.
func schemaPositions(tokens []string) []bool {
	pos := make([]bool, len(tokens)+1)
	pos[0] = true
	state := "schema"
	for i, tok := range tokens {
		next := "data"
		switch state {
		case "schema":
			switch {
			case slices.Contains(mapKeywords, tok), slices.Contains(arrayKeywords, tok):
				next = "slot"
			case slices.Contains(singleKeywords, tok):
				next = "schema"
			}
		case "slot":
			next = "schema"
		}
		state = next
		pos[i+1] = state == "schema"
	}
	return pos
}

// inDefs reports whether tokens pass through a $defs keyword.
func inDefs(tokens []string) bool {
	pos := schemaPositions(tokens)
	for i, tok := range tokens {
		if pos[i] && tok == "$defs" {
			return true
		}
	}
	return false
}

// nodeAt follows tokens from v. hitBool reports a boolean schema met
// before the end.
func nodeAt(v any, tokens []string) (node any, found, hitBool bool) {
	for _, tok := range tokens {
		switch x := v.(type) {
		case map[string]any:
			next, ok := x[tok]
			if !ok {
				return nil, false, false
			}
			v = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false, false
			}
			v = x[i]
		case bool:
			return x, false, true
		default:
			return nil, false, false
		}
	}
	return v, true, false
}

// setAt replaces the node at tokens in root, returning the new root.
func setAt(root any, tokens []string, val any) any {
	if len(tokens) == 0 {
		return val
	}
	parent, ok, _ := nodeAt(root, tokens[:len(tokens)-1])
	if !ok {
		return root
	}
	last := tokens[len(tokens)-1]
	switch x := parent.(type) {
	case map[string]any:
		if _, ok := x[last]; ok {
			x[last] = val
		}
	case []any:
		if i, err := strconv.Atoi(last); err == nil && i >= 0 && i < len(x) {
			x[i] = val
		}
	}
	return root
}

// deleteAt removes the member at tokens, if there is one.
func deleteAt(root any, tokens []string) {
	if len(tokens) == 0 {
		return
	}
	parent, ok, _ := nodeAt(root, tokens[:len(tokens)-1])
	if !ok {
		return
	}
	if m, ok := parent.(map[string]any); ok {
		delete(m, tokens[len(tokens)-1])
	}
}

// passesFalse reports whether tokens lead through a false schema, as a
// Swagger 2.0 Request projection makes a readOnly property.
func passesFalse(v any, tokens []string) bool {
	for _, tok := range tokens {
		switch x := v.(type) {
		case map[string]any:
			v = x[tok]
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(x) {
				return false
			}
			v = x[i]
		case bool:
			return !x
		default:
			return false
		}
	}
	return false
}

// emptyAsTrue writes every schema with no keywords as true, its equal in
// JSON Schema 2020-12 (Core section 4.3.2), so that comparisons accept
// either where the documentation names no spelling.
func emptyAsTrue(v any) any {
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return true
	}
	empty := func(s any) bool { m, ok := s.(map[string]any); return ok && len(m) == 0 }
	eachSchema(v, func(_ []string, node any) {
		obj, ok := node.(map[string]any)
		if !ok {
			return
		}
		for k, child := range obj {
			switch {
			case slices.Contains(mapKeywords, k):
				if m, ok := child.(map[string]any); ok {
					for name, s := range m {
						if empty(s) {
							m[name] = true
						}
					}
				}
			case slices.Contains(arrayKeywords, k):
				if a, ok := child.([]any); ok {
					for i, s := range a {
						if empty(s) {
							a[i] = true
						}
					}
				}
			case slices.Contains(singleKeywords, k):
				if empty(child) {
					obj[k] = true
				}
			}
		}
	})
	return v
}

// supportedDialects are the dialects whose schemas Project carries: JSON
// Schema 2020-12 and the OpenAPI 3.1 and 3.2 base dialects (Project), as
// the client reads them (Schema.Dialect).
var supportedDialects = []string{
	dialect2020,
	dialectOAS31,
	"https://spec.openapis.org/oas/3.1/dialect/2024-10-25",
	"https://spec.openapis.org/oas/3.1/dialect/2024-11-10",
	dialectOAS32,
	"https://spec.openapis.org/oas/3.2/dialect/2026-02-26",
}

func supportedDialect(d string) bool { return slices.Contains(supportedDialects, d) }

// ---- keywords of Swagger 2.0 and OpenAPI 3.0 ----

// applicatorsAndAssertions are the JSON Schema 2020-12 keywords that apply
// subschemas or assert: $ref and $dynamicRef (Core section 8.2.3), the
// applicators of Core sections 10 and 11, and the assertions of
// Validation section 6, with contentSchema, whose value is a schema
// (Validation section 8.5) whose references Swagger 2.0 and OpenAPI 3.0
// never report. Format, other content and meta-data keywords (Validation
// sections 7 to 9) are annotations.
var applicatorsAndAssertions = []string{
	"$ref", "$dynamicRef", "contentSchema",
	"allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentSchemas",
	"prefixItems", "items", "contains", "properties", "patternProperties", "additionalProperties", "propertyNames",
	"unevaluatedItems", "unevaluatedProperties",
	"type", "enum", "const", "multipleOf", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum",
	"maxLength", "minLength", "pattern", "maxItems", "minItems", "uniqueItems", "maxContains", "minContains",
	"maxProperties", "minProperties", "required", "dependentRequired",
}

// defined20 are the fields of the Swagger 2.0 Schema Object (OAS 2.0
// section 6.4.18.1 and the JSON Schema keywords listed before it).
var defined20 = []string{
	"$ref", "format", "title", "description", "default", "multipleOf", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum",
	"maxLength", "minLength", "pattern", "maxItems", "minItems", "uniqueItems", "maxProperties", "minProperties", "required", "enum", "type",
	"items", "allOf", "properties", "additionalProperties",
	"discriminator", "readOnly", "xml", "externalDocs", "example",
}

// defined30 are the fields of the OpenAPI 3.0 Schema Object (OAS 3.0.4
// sections 4.7.24.1 and 4.7.24.2) and the Reference Object's $ref (section
// 4.7.23).
var defined30 = []string{
	"$ref", "title", "multipleOf", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum",
	"maxLength", "minLength", "pattern", "maxItems", "minItems", "uniqueItems", "maxProperties", "minProperties", "required", "enum",
	"type", "allOf", "oneOf", "anyOf", "not", "items", "properties", "additionalProperties", "description", "format", "default",
	"nullable", "discriminator", "readOnly", "writeOnly", "xml", "externalDocs", "example", "deprecated",
}

// removedKeywords are the keywords Project removes with an Issue in
// edition v (Project: "A keyword that the edition's Schema Object does not
// define and that JSON Schema 2020-12 treats as an applicator or an
// assertion ... is removed with an Issue").
func removedKeywords(v string) []string {
	var def []string
	switch {
	case v == v20:
		def = defined20
	case is30(v):
		def = defined30
	default:
		return nil
	}
	var out []string
	for _, k := range applicatorsAndAssertions {
		if !slices.Contains(def, k) {
			out = append(out, k)
		}
	}
	return out
}

// eachLegacySchema calls fn for every schema object that a Swagger 2.0 or
// OpenAPI 3.0 schema holds where that edition reads one: not beside $ref,
// whose members are ignored, and not inside a keyword the edition does not
// define.
func eachLegacySchema(v string, raw any, fn func(ptr []string, obj map[string]any)) {
	maps, arrays, singles := []string{"properties"}, []string{"allOf"}, []string{"items", "additionalProperties"}
	if is30(v) {
		arrays = append(arrays, "oneOf", "anyOf")
		singles = append(singles, "not")
	}
	var walk func(n any, ptr []string)
	walk = func(n any, ptr []string) {
		obj, ok := n.(map[string]any)
		if !ok {
			return
		}
		fn(ptr, obj)
		if _, ok := obj["$ref"]; ok {
			return
		}
		for k, child := range obj {
			at := func(more ...string) []string { return append(append(slices.Clip(ptr), k), more...) }
			switch {
			case slices.Contains(maps, k):
				if m, ok := child.(map[string]any); ok {
					for name, s := range m {
						walk(s, at(name))
					}
				}
			case slices.Contains(arrays, k):
				if a, ok := child.([]any); ok {
					for i, s := range a {
						walk(s, at(strconv.Itoa(i)))
					}
				}
			case slices.Contains(singles, k):
				walk(child, at())
			}
		}
	}
	walk(raw, nil)
}

// underRefSibling reports whether tokens pass through a schema that has
// $ref, whose other members Swagger 2.0 and OpenAPI 3.0 ignore.
func underRefSibling(raw any, tokens []string) bool {
	pos := schemaPositions(tokens)
	for i := 1; i < len(tokens); i++ {
		if !pos[i] {
			continue
		}
		if n, ok, _ := nodeAt(raw, tokens[:i]); ok {
			if obj, ok := n.(map[string]any); ok {
				if _, has := obj["$ref"]; has && tokens[i] != "$ref" {
					return true
				}
			}
		}
	}
	return false
}

// foreignTree lists, for an OpenAPI 3.1 or 3.2 schema holding a nested
// resource in another dialect, whose References therefore fails, the
// string references outside such resources and outside $defs, and the
// $schema of each such resource, which becomes true.
func foreignTree(v string, raw any) (refs, foreign [][]string) {
	eachModernSchema(raw, func(ptr []string, obj map[string]any) {
		at := func(more ...string) []string { return append(slices.Clip(ptr), more...) }
		for _, kw := range []string{"$ref", "$dynamicRef"} {
			if _, ok := obj[kw].(string); ok {
				refs = append(refs, at(kw))
			}
		}
		if disc, ok := obj["discriminator"].(map[string]any); ok {
			if m, ok := disc["mapping"].(map[string]any); ok {
				for name, val := range m {
					if _, ok := val.(string); ok {
						refs = append(refs, at("discriminator", "mapping", name))
					}
				}
			}
			if _, ok := disc["defaultMapping"].(string); ok && strings.HasPrefix(v, "3.2") {
				refs = append(refs, at("discriminator", "defaultMapping"))
			}
		}
	}, func(ptr []string) {
		foreign = append(foreign, append(slices.Clip(ptr), "$schema"))
	})
	return refs, foreign
}

// eachModernSchema calls fn for every schema object of a JSON Schema
// 2020-12 tree outside $defs and outside nested resources in another
// dialect, for each of which it calls foreign instead.
func eachModernSchema(raw any, fn func(ptr []string, obj map[string]any), foreign func(ptr []string)) {
	var walk func(n any, ptr []string)
	walk = func(n any, ptr []string) {
		obj, ok := n.(map[string]any)
		if !ok {
			return
		}
		if s, ok := obj["$schema"].(string); ok && len(ptr) > 0 && !supportedDialect(s) {
			foreign(ptr)
			return
		}
		fn(ptr, obj)
		at := func(more ...string) []string { return append(slices.Clip(ptr), more...) }
		for k, child := range obj {
			switch {
			case k == "$defs":
			case slices.Contains(mapKeywords, k):
				if m, ok := child.(map[string]any); ok {
					for name, s := range m {
						walk(s, at(k, name))
					}
				}
			case slices.Contains(arrayKeywords, k):
				if a, ok := child.([]any); ok {
					for i, s := range a {
						walk(s, at(k, strconv.Itoa(i)))
					}
				}
			case slices.Contains(singleKeywords, k):
				walk(child, at(k))
			}
		}
	}
	walk(raw, nil)
}

// appendPointer extends a URI whose fragment is a JSON Pointer by tokens,
// percent-encoded as RFC 6901 section 6 says.
func appendPointer(uri string, tokens []string) string {
	for _, tok := range tokens {
		uri += "/" + strings.TrimPrefix(refFor(tok), "#/$defs/")
	}
	return uri
}

// splitReference splits the pointer to a reference into the schema that
// holds it and the reference's pointer from that schema.
func splitReference(tokens []string) (holder []string, rel string) {
	n := len(tokens)
	switch {
	case n >= 3 && tokens[n-2] == "mapping" && tokens[n-3] == "discriminator":
		return tokens[:n-3], pointer(tokens[n-3:])
	case n >= 2 && tokens[n-1] == "defaultMapping" && tokens[n-2] == "discriminator":
		return tokens[:n-2], pointer(tokens[n-2:])
	}
	return tokens[:n-1], pointer(tokens[n-1:])
}

// analysis is what Project must do with one authored schema, derived from
// its Raw and References: the references to rewrite to the Source of their
// targets, the keywords dropped with an Issue, the resources in another
// dialect that become true, and the At of every Issue.
type analysis struct {
	raw       any
	whole     bool // the schema itself is in another dialect
	rewrites  []rewrite
	drops     [][]string
	resources [][]string
	issues    []string
}

type rewrite struct {
	tokens []string
	target string
}

// analyze derives h's analysis, or ok false when the test cannot.
func analyze(c *openapi.Client, h *openapi.Schema) (an analysis, ok bool) {
	raw, err := decode(h.Raw())
	if err != nil {
		return an, false
	}
	an.raw = raw
	v := h.Version()
	seen := map[string]bool{}
	issue := func(tokens []string) {
		if at := pointer(tokens); !seen[at] {
			seen[at] = true
			an.issues = append(an.issues, at)
		}
	}
	drop := func(tokens []string) {
		an.drops = append(an.drops, tokens)
		issue(tokens)
	}
	if isModern(v) && !supportedDialect(h.Dialect()) {
		an.whole = true
		if obj, ok := raw.(map[string]any); ok && obj["$schema"] != nil {
			issue([]string{"$schema"})
		} else {
			issue(nil)
		}
		return an, true
	}
	reported := map[string]bool{}
	consider := func(tokens []string, r openapi.SchemaReference) {
		reported[pointer(tokens)] = true
		if r.Err != nil || r.Keyword == "$dynamicRef" {
			drop(tokens)
			return
		}
		an.rewrites = append(an.rewrites, rewrite{tokens, r.Target.Source()})
	}
	refs, err := h.References()
	if err != nil {
		if !isModern(v) {
			return an, false
		}
		// A resource in another dialect becomes true; each other reference
		// is read through a handle on the schema that holds it.
		rs, fs := foreignTree(v, raw)
		for _, f := range fs {
			an.resources = append(an.resources, f[:len(f)-1])
			issue(f)
		}
		for _, tokens := range rs {
			holder, rel := splitReference(tokens)
			hs, err := c.Schema(appendPointer(h.Source(), holder))
			if err != nil {
				return an, false
			}
			hrefs, err := hs.References()
			if err != nil {
				return an, false
			}
			for _, r := range hrefs {
				if r.At == rel {
					consider(tokens, r)
					break
				}
			}
		}
	} else {
		for _, r := range refs {
			tokens, err := parsePointer(r.At)
			if err != nil {
				return an, false
			}
			reported[r.At] = true
			if inDefs(tokens) || (!isModern(v) && underRefSibling(raw, tokens)) {
				continue
			}
			consider(tokens, r)
		}
	}
	// A $ref that References does not report is removed with an Issue
	// (Project), as are the keywords an older edition does not define.
	unreported := func(ptr []string, obj map[string]any) bool {
		if _, ok := obj["$ref"]; !ok {
			return false
		}
		if at := append(slices.Clip(ptr), "$ref"); !reported[pointer(at)] {
			drop(at)
		}
		return true
	}
	if isModern(v) {
		eachModernSchema(raw, func(ptr []string, obj map[string]any) { unreported(ptr, obj) }, func([]string) {})
		return an, true
	}
	removed := removedKeywords(v)
	eachLegacySchema(v, raw, func(ptr []string, obj map[string]any) {
		if unreported(ptr, obj) {
			return // the other members are ignored
		}
		for k := range obj {
			if slices.Contains(removed, k) {
				drop(append(slices.Clip(ptr), k))
			}
		}
		if isArray(obj["items"]) {
			// items as an array, a form neither edition defines, is
			// removed with what it holds.
			drop(append(slices.Clip(ptr), "items"))
		}
	})
	return an, true
}

// ---- the invariant checker ----

// output is one emitted schema with the authored handle it came from.
type output struct {
	name   string // "Root" or "Defs[KEY]"
	value  any
	handle *openapi.Schema
}

// checkProjection checks the properties every Projection has (Projection
// and Project documentation):
//
//   - Root and each Defs value is a schema; Sources has exactly Defs's keys,
//     each the Source of an authored schema.
//   - Every reference in Root and Defs, mapping and defaultMapping values
//     included, is #/$defs/KEY with KEY in Defs, escaped as RFC 3986 section
//     3.5 allows; no $dynamicRef remains; Defs holds exactly what Root
//     reaches.
//   - No $id, $schema, $anchor, $dynamicAnchor or $defs keyword appears.
//   - Keys follow the component-name and Source-relative rules.
//   - The Issues are exactly the lost references, $dynamicRefs, resources in
//     another dialect and removed keywords, one each, At naming the keyword;
//     each contributes nothing, and a schema they leave with no keywords is
//     true.
//   - In OpenAPI 3.1 and 3.2, each emitted schema equals the authored one
//     apart from rewritten references, removed identifiers and those losses;
//     in Swagger 2.0 and OpenAPI 3.0 it keeps the authored keywords except
//     those the documentation translates or removes.
func checkProjection(t testing.TB, c *openapi.Client, s *openapi.Schema, d schema2020.Direction, p *schema2020.Projection, err error) {
	t.Helper()
	if p == nil {
		t.Fatalf("Project(%s, %s) returned a nil Projection (error %v)", s.Source(), dirName(d), err)
	}
	var issues []schema2020.Issue
	if err != nil {
		var pe *schema2020.Error
		if !errors.As(err, &pe) {
			t.Fatalf("Project(%s, %s): error %v is not a *schema2020.Error", s.Source(), dirName(d), err)
		}
		if len(pe.Issues) == 0 {
			t.Errorf("Project(%s, %s): *Error with no Issues", s.Source(), dirName(d))
		}
		issues = pe.Issues
	}
	ck := &checker{t: t, c: c, d: d, p: p, issues: issues}
	ck.run(s)
}

type checker struct {
	t      testing.TB
	c      *openapi.Client
	d      schema2020.Direction
	p      *schema2020.Projection
	issues []schema2020.Issue

	outputs  []output
	keyOf    map[string]string          // Source to key
	issueAts map[string]map[string]bool // Source to the At of its Issues
}

func (ck *checker) errorf(format string, args ...any) {
	ck.t.Helper()
	ck.t.Errorf(format, args...)
}

func (ck *checker) run(s *openapi.Schema) {
	ck.t.Helper()
	p := ck.p
	root, err := decode(p.Root)
	if err != nil {
		ck.errorf("Root is not JSON: %v: %s", err, p.Root)
		return
	}
	ck.outputs = append(ck.outputs, output{"Root", root, s})
	if len(p.Sources) != len(p.Defs) {
		ck.errorf("Sources has %d keys, Defs %d", len(p.Sources), len(p.Defs))
	}
	ck.keyOf = map[string]string{}
	keys := slices.Sorted(maps.Keys(p.Defs))
	for _, k := range keys {
		v, err := decode(p.Defs[k])
		if err != nil {
			ck.errorf("Defs[%q] is not JSON: %v: %s", k, err, p.Defs[k])
			continue
		}
		src, ok := p.Sources[k]
		if !ok {
			ck.errorf("Defs key %q has no Sources entry", k)
			continue
		}
		if other, dup := ck.keyOf[src]; dup {
			ck.errorf("keys %q and %q have the same Source %s", other, k, src)
		}
		ck.keyOf[src] = k
		h, err := ck.c.Schema(src)
		if err != nil {
			ck.errorf("Sources[%q] = %q is not a schema of the client: %v", k, src, err)
			continue
		}
		if h.Source() != src {
			ck.errorf("Sources[%q] = %q, but the schema there has Source %q", k, src, h.Source())
		}
		ck.checkKey(k, src)
		ck.outputs = append(ck.outputs, output{"Defs[" + k + "]", v, h})
	}
	for k := range p.Sources {
		if _, ok := p.Defs[k]; !ok {
			ck.errorf("Sources key %q is not in Defs", k)
		}
	}
	reached := map[string]bool{}
	edges := map[string][]string{}
	for _, o := range ck.outputs {
		edges[o.name] = ck.checkShape(o)
	}
	queue := slices.Clone(edges["Root"])
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		if reached[k] {
			continue
		}
		reached[k] = true
		queue = append(queue, edges["Defs["+k+"]"]...)
	}
	for _, k := range keys {
		if !reached[k] {
			ck.errorf("Defs[%q] is not reached by reference from Root", k)
		}
	}
	ck.checkIssues()
	for _, o := range ck.outputs {
		if isModern(o.handle.Version()) {
			ck.checkModern(o)
		} else {
			ck.checkLegacy(o)
		}
	}
}

// checkKey checks a key against its Source (Project: "A schema written at
// #/components/schemas/NAME of the entry document, or #/definitions/NAME
// in Swagger 2.0, has the key NAME when NAME contains no "#"; any other key
// is the schema's Source written as a URI reference relative to the entry
// document's URI, so that the key resolves against that URI to the
// Source").
func (ck *checker) checkKey(key, src string) {
	ck.t.Helper()
	entry := ck.c.DocumentURIs()[0]
	doc, frag, ok := strings.Cut(src, "#")
	if !ok {
		ck.errorf("Source %q has no fragment", src)
		return
	}
	if doc == entry {
		decoded, err := url.PathUnescape(frag)
		if err == nil {
			tokens, err := parsePointer(decoded)
			isComp := err == nil &&
				(ck.c.Version() == v20 && len(tokens) == 2 && tokens[0] == "definitions" ||
					ck.c.Version() != v20 && len(tokens) == 3 && tokens[0] == "components" && tokens[1] == "schemas")
			if isComp && !strings.Contains(tokens[len(tokens)-1], "#") {
				if name := tokens[len(tokens)-1]; key != name {
					ck.errorf("key %q for %s, want the component name %q", key, src, name)
				}
				return
			}
		}
		if key != "#"+frag {
			ck.errorf("key %q for %s, want %q, the Source relative to the entry document", key, src, "#"+frag)
		}
		return
	}
	if !strings.Contains(key, "#") {
		ck.errorf("key %q for %s has no fragment, so it could be a component name", key, src)
	}
	ref, err := url.Parse(key)
	if err != nil {
		ck.errorf("key %q is not a URI reference: %v", key, err)
		return
	}
	base, _ := url.Parse(entry)
	if ref.Scheme != "" && ref.Scheme == base.Scheme {
		ck.errorf("key %q is not relative to the entry document %s", key, entry)
	}
	got := base.ResolveReference(ref)
	want, _ := url.Parse(src)
	if got.Scheme != want.Scheme || got.Host != want.Host || got.Path != want.Path || got.RawQuery != want.RawQuery || got.Fragment != want.Fragment {
		ck.errorf("key %q resolves against %s to %s, want the Source %s", key, entry, got, src)
	}
}

// checkShape checks one emitted schema for identifiers and references and
// returns the keys it refers to.
func (ck *checker) checkShape(o output) []string {
	ck.t.Helper()
	var refs []string
	addRef := func(ptr []string, what, ref string) {
		key, err := keyOfRef(ref)
		if err != nil {
			ck.errorf("%s at %s: %s: %v", o.name, pointer(ptr), what, err)
			return
		}
		if _, ok := ck.p.Defs[key]; !ok {
			ck.errorf("%s at %s: %s %q names key %q, which is not in Defs", o.name, pointer(ptr), what, ref, key)
			return
		}
		refs = append(refs, key)
	}
	switch o.value.(type) {
	case map[string]any, bool:
	default:
		ck.errorf("%s is %s, not a schema", o.name, canon(o.value))
		return nil
	}
	eachSchema(o.value, func(ptr []string, node any) {
		obj, ok := node.(map[string]any)
		if !ok {
			return
		}
		for _, kw := range identifierKeywords {
			if _, ok := obj[kw]; ok {
				ck.errorf("%s at %q carries %s", o.name, pointer(ptr), kw)
			}
		}
		if _, ok := obj["$dynamicRef"]; ok {
			ck.errorf("%s at %q carries $dynamicRef", o.name, pointer(ptr))
		}
		if r, ok := obj["$ref"]; ok {
			if rs, ok := r.(string); ok {
				addRef(ptr, "$ref", rs)
			} else {
				ck.errorf("%s at %q: $ref is %s", o.name, pointer(ptr), canon(r))
			}
		}
		disc, ok := obj["discriminator"].(map[string]any)
		if !ok {
			return
		}
		if m, ok := disc["mapping"].(map[string]any); ok {
			for name, val := range m {
				vs, ok := val.(string)
				if !ok {
					ck.errorf("%s at %q: mapping value %q is %s", o.name, pointer(ptr), name, canon(val))
					continue
				}
				addRef(append(slices.Clip(ptr), "discriminator", "mapping", name), "mapping value", vs)
			}
		}
		if dm, ok := disc["defaultMapping"]; ok && strings.HasPrefix(o.handle.Version(), "3.2") {
			if dms, ok := dm.(string); ok {
				addRef(append(slices.Clip(ptr), "discriminator", "defaultMapping"), "defaultMapping", dms)
			} else {
				ck.errorf("%s at %q: defaultMapping is %s", o.name, pointer(ptr), canon(dm))
			}
		}
	})
	return refs
}

// checkIssues checks that the Issues are exactly those the authored
// schemas call for, one each, and that each keyword they name contributes
// nothing to every emitted schema of its Source.
func (ck *checker) checkIssues() {
	ck.t.Helper()
	bySource := map[string][]output{}
	for _, o := range ck.outputs {
		bySource[o.handle.Source()] = append(bySource[o.handle.Source()], o)
	}
	ck.issueAts = map[string]map[string]bool{}
	for _, is := range ck.issues {
		if is.Err == nil {
			ck.errorf("Issue %s %q has no Err", is.Source, is.At)
		}
		tokens, err := parsePointer(is.At)
		if err != nil {
			ck.errorf("Issue %s: At %q: %v", is.Source, is.At, err)
			continue
		}
		if ck.issueAts[is.Source][is.At] {
			ck.errorf("two Issues at %q of %s", is.At, is.Source)
		}
		if ck.issueAts[is.Source] == nil {
			ck.issueAts[is.Source] = map[string]bool{}
		}
		ck.issueAts[is.Source][is.At] = true
		outs := bySource[is.Source]
		if len(outs) == 0 {
			ck.errorf("Issue %s %q names no emitted schema's Source", is.Source, is.At)
			continue
		}
		for _, o := range outs {
			ck.checkEffect(o, is.At, tokens)
		}
	}
	done := map[string]bool{}
	for _, o := range ck.outputs {
		src := o.handle.Source()
		if done[src] {
			continue
		}
		done[src] = true
		an, ok := analyze(ck.c, o.handle)
		if !ok {
			continue
		}
		ats := an.issues
		want := map[string]bool{}
		for _, at := range ats {
			tokens, _ := parsePointer(at)
			if passesFalse(o.value, tokens) {
				continue // inside a property schema Swagger 2.0 replaces by false
			}
			want[at] = true
			if !ck.issueAts[src][at] {
				ck.errorf("%s (%s): no Issue at %q", o.name, src, at)
			}
		}
		for at := range ck.issueAts[src] {
			if !want[at] {
				ck.errorf("%s (%s): Issue at %q, which the authored schema does not call for", o.name, src, at)
			}
		}
	}
}

// checkEffect checks that the keyword an Issue names contributes nothing
// to o (Project: "a reference or removed keyword is dropped, with its
// mapping entry for a mapping value; a resource in another dialect ... becomes
// true ... and a schema left with no keywords becomes true").
func (ck *checker) checkEffect(o output, at string, tokens []string) {
	ck.t.Helper()
	n := len(tokens)
	where := fmt.Sprintf("Issue %q: %s", at, o.name)
	if n == 0 {
		if o.value != true {
			ck.errorf("%s is %s, want true", where, canon(o.value))
		}
		return
	}
	pos := schemaPositions(tokens)
	last := tokens[n-1]
	absent := func(holder []string) {
		node, found, hit := nodeAt(o.value, holder)
		if hit {
			return
		}
		if !found {
			ck.errorf("%s has nothing at %q", where, pointer(holder))
			return
		}
		switch x := node.(type) {
		case map[string]any:
			if _, has := x[last]; has {
				ck.errorf("%s still has %q at %q", where, last, pointer(holder))
			}
		case bool:
			if !x {
				ck.errorf("%s: %q is false", where, pointer(holder))
			}
		}
	}
	switch {
	case n >= 3 && pos[n-3] && tokens[n-3] == "discriminator" && tokens[n-2] == "mapping":
		if _, found, _ := nodeAt(o.value, tokens[:n-1]); found {
			absent(tokens[:n-1])
		}
	case n >= 2 && pos[n-2] && tokens[n-2] == "discriminator" && last == "defaultMapping":
		absent(tokens[:n-1])
	case pos[n-1] && last == "$schema":
		node, found, hit := nodeAt(o.value, tokens[:n-1])
		if !hit && (!found || node != true) {
			ck.errorf("%s: the resource at %q is %s, want true", where, pointer(tokens[:n-1]), canon(node))
		}
	case pos[n-1]:
		absent(tokens[:n-1])
		if node, found, _ := nodeAt(o.value, tokens[:n-1]); found {
			if m, ok := node.(map[string]any); ok && len(m) == 0 {
				ck.errorf("%s: the schema at %q is left with no keywords and is not true", where, pointer(tokens[:n-1]))
			}
		}
	default:
		ck.errorf("%s: At names no keyword of a schema", where)
	}
}

// checkModern checks an OpenAPI 3.1 or 3.2 schema against its authored Raw
// (Project: "such a schema is written the same in every direction: as
// authored, apart from the rewritten references, the keywords removed above
// and the losses Issues report"), less what its Issues drop.
func (ck *checker) checkModern(o output) {
	ck.t.Helper()
	want, emptied, ok := ck.modernExpected(o.handle)
	if !ok {
		return
	}
	got := normalizeRefs(clone(o.value))
	dropEmptyMappings(got)
	dropEmptyMappings(want)
	for _, e := range emptied {
		if node, found, hit := nodeAt(got, e); !hit && (!found || node != true) {
			ck.errorf("%s: the schema at %q is left with no keywords and is %s, want true", o.name, pointer(e), canon(node))
		}
	}
	sameValue(ck.t, fmt.Sprintf("%s (%s) against its authored schema", o.name, o.handle.Source()), emptyAsTrue(got), emptyAsTrue(want))
}

func (ck *checker) modernExpected(h *openapi.Schema) (want any, emptied [][]string, ok bool) {
	ck.t.Helper()
	an, ok := analyze(ck.c, h)
	if !ok {
		return nil, nil, false
	}
	if an.whole {
		return true, nil, true
	}
	raw := an.raw
	for _, rw := range an.rewrites {
		key, ok := ck.keyOf[rw.target]
		if !ok {
			ck.errorf("%s refers at %s to %s, which has no Defs entry", h.Source(), pointer(rw.tokens), rw.target)
			return nil, nil, false
		}
		raw = setAt(raw, rw.tokens, refFor(key))
	}
	for _, res := range an.resources {
		raw = setAt(raw, res, true)
	}
	var touched [][]string
	for _, d := range an.drops {
		deleteAt(raw, d)
		n, pos := len(d), schemaPositions(d)
		if n >= 3 && pos[n-3] && d[n-3] == "discriminator" && d[n-2] == "mapping" ||
			n >= 2 && pos[n-2] && d[n-2] == "discriminator" && d[n-1] == "defaultMapping" {
			continue // a mapping entry or defaultMapping; its schema keeps the discriminator
		}
		touched = append(touched, d[:n-1])
	}
	raw = stripIdentifiers(raw)
	for _, holder := range touched {
		if node, found, _ := nodeAt(raw, holder); found {
			if m, ok := node.(map[string]any); ok && len(m) == 0 {
				raw = setAt(raw, holder, true)
				emptied = append(emptied, holder)
			}
		}
	}
	return raw, emptied, true
}

// dropEmptyMappings removes every discriminator mapping left empty, which
// the documentation neither keeps nor removes.
func dropEmptyMappings(v any) {
	eachSchema(v, func(_ []string, node any) {
		if obj, ok := node.(map[string]any); ok {
			if disc, ok := obj["discriminator"].(map[string]any); ok {
				if m, ok := disc["mapping"].(map[string]any); ok && len(m) == 0 {
					delete(disc, "mapping")
				}
			}
		}
	})
}

// stripIdentifiers removes the identifier keywords from every schema of v.
func stripIdentifiers(v any) any {
	eachSchema(v, func(_ []string, node any) {
		if obj, ok := node.(map[string]any); ok {
			for _, kw := range identifierKeywords {
				delete(obj, kw)
			}
		}
	})
	return v
}

// translated are the keywords Project may add in Swagger 2.0 and OpenAPI
// 3.0 (Project: exclusiveMinimum and exclusiveMaximum from boolean ones,
// contentEncoding from format: byte).
var translated = []string{"exclusiveMinimum", "exclusiveMaximum", "contentEncoding"}

// rewritten are the authored keywords whose output the documentation
// rewrites in Swagger 2.0 and OpenAPI 3.0; every other keyword that is not
// a subschema container is kept unchanged.
var rewritten = []string{"$ref", "type", "format", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
	"required", "discriminator", "properties", "items", "allOf", "oneOf", "anyOf", "not", "additionalProperties", "contentEncoding"}

// withNull is an OpenAPI 3.0 type with nullable: true (Project: "true adds
// "null" to it as a type list unless it already allows null").
func withNull(typ any) any {
	switch t := typ.(type) {
	case string:
		if t == "null" {
			return t
		}
		return []any{t, "null"}
	case []any:
		if slices.Contains(t, any("null")) {
			return t
		}
		return append(slices.Clone(t), "null")
	}
	return typ
}

// binaryish reports a schema that format: binary or the Swagger 2.0 file
// type marks as bytes.
func binaryish(v string, obj map[string]any) bool {
	return obj["type"] == "string" && obj["format"] == "binary" || v == v20 && obj["type"] == "file"
}

// survivors counts the members an older edition's schema keeps.
func survivors(v string, ptr []string, obj map[string]any, issueAts map[string]bool) (n int, issued bool) {
	at := func(k string) string { return pointer(append(slices.Clip(ptr), k)) }
	if _, ok := obj["$ref"]; ok {
		if issueAts[at("$ref")] {
			return 0, true
		}
		return 1, false
	}
	for k, val := range obj {
		switch {
		case slices.Contains(identifierKeywords, k):
		case issueAts[at(k)]:
			issued = true
		case (k == "type" || k == "format") && binaryish(v, obj):
		case k == "nullable" && is30(v):
		case k == "exclusiveMinimum" || k == "exclusiveMaximum":
			bound := map[string]string{"exclusiveMinimum": "minimum", "exclusiveMaximum": "maximum"}[k]
			if b, isBool := val.(bool); !isBool || b && obj[bound] != nil {
				n++
			}
		case k == "minimum" && obj["exclusiveMinimum"] == true, k == "maximum" && obj["exclusiveMaximum"] == true:
		default:
			n++
		}
	}
	return n, issued
}

// checkLegacy checks a Swagger 2.0 or OpenAPI 3.0 schema against its
// authored Raw, position by position.
func (ck *checker) checkLegacy(o output) {
	ck.t.Helper()
	v := o.handle.Version()
	raw, err := decode(o.handle.Raw())
	if err != nil {
		ck.errorf("Raw of %s is not JSON: %v", o.handle.Source(), err)
		return
	}
	issueAts := ck.issueAts[o.handle.Source()]
	removed := removedKeywords(v)
	authoredCMT := bytes.Contains(o.handle.Raw(), []byte(`"contentMediaType"`))
	eachSchema(o.value, func(ptr []string, node any) {
		where := fmt.Sprintf("%s at %q", o.name, pointer(ptr))
		rawNode, found, _ := nodeAt(raw, ptr)
		if !found {
			ck.errorf("%s: no authored schema there (Raw %s)", where, o.handle.Raw())
			return
		}
		rawObj, rawIsObj := rawNode.(map[string]any)
		if b, ok := node.(bool); ok {
			if rb, ok := rawNode.(bool); ok && rb == b {
				return
			}
			n := len(ptr)
			switch {
			case !b:
				if !(v == v20 && ck.d == schema2020.Request && n >= 2 && ptr[n-2] == "properties") {
					ck.errorf("%s: false where the authored schema is %s", where, canon(rawNode))
				}
			case rawIsObj:
				if kept, _ := survivors(v, ptr, rawObj, issueAts); kept != 0 {
					ck.errorf("%s: true where the authored schema %s keeps %d keywords", where, canon(rawNode), kept)
				}
			default:
				ck.errorf("%s: true where the authored schema is %s", where, canon(rawNode))
			}
			return
		}
		obj, ok := node.(map[string]any)
		if !ok {
			return
		}
		if !rawIsObj {
			ck.errorf("%s: an object where the authored schema is %s", where, canon(rawNode))
			return
		}
		if kept, issued := survivors(v, ptr, rawObj, issueAts); kept == 0 && issued && len(obj) == 0 {
			ck.errorf("%s: left with no keywords by its Issues and is not true", where)
		}
		for k := range obj {
			if _, ok := rawObj[k]; !ok && !slices.Contains(translated, k) && !(k == "contentMediaType" && authoredCMT) {
				ck.errorf("%s: %q, which is neither authored nor a named translation", where, k)
			}
			if slices.Contains(removed, k) {
				ck.errorf("%s: %q, which %s does not define", where, k, v)
			}
		}
		if _, ok := obj["$ref"]; ok && len(obj) != 1 {
			ck.errorf("%s: members beside $ref: %s", where, canon(obj))
		}
		if isArray(obj["items"]) {
			ck.errorf("%s: items as an array", where)
		}
		if ref, ok := rawObj["$ref"]; ok {
			if _, isString := ref.(string); isString {
				for k := range obj {
					if k != "$ref" {
						ck.errorf("%s: %q, a member beside the authored $ref, is kept", where, k)
					}
				}
			}
			return // the rest of the authored schema is ignored
		}
		for k, rv := range rawObj {
			if slices.Contains(rewritten, k) || slices.Contains(identifierKeywords, k) || slices.Contains(removed, k) || k == "nullable" && is30(v) {
				continue
			}
			if canon(obj[k]) != canon(rv) {
				ck.errorf("%s: %q is %s, authored %s; every other keyword is kept", where, k, canon(obj[k]), canon(rv))
			}
		}
		if _, ok := obj["contentMediaType"]; ok && !authoredCMT {
			ck.errorf("%s: contentMediaType", where)
		}
		for _, kw := range []string{"exclusiveMinimum", "exclusiveMaximum"} {
			if _, isBool := obj[kw].(bool); isBool {
				ck.errorf("%s: boolean %s", where, kw)
			}
			bound := map[string]string{"exclusiveMinimum": "minimum", "exclusiveMaximum": "maximum"}[kw]
			if rawObj[kw] == true && rawObj[bound] != nil {
				if canon(obj[kw]) != canon(rawObj[bound]) {
					ck.errorf("%s: %s is %s, want the authored %s %s", where, kw, canon(obj[kw]), bound, canon(rawObj[bound]))
				}
				if _, has := obj[bound]; has {
					ck.errorf("%s: %s kept beside the exclusive bound", where, bound)
				}
			}
		}
		if obj["format"] == "byte" {
			ck.errorf("%s: format byte", where)
		}
		if f := rawObj["format"]; f != nil && f != "byte" && !binaryish(v, rawObj) && canon(obj["format"]) != canon(f) {
			ck.errorf("%s: format %s, authored %s", where, canon(obj["format"]), canon(f))
		}
		for kw, bound := range map[string]string{"exclusiveMinimum": "minimum", "exclusiveMaximum": "maximum"} {
			if b := rawObj[bound]; b != nil && rawObj[kw] != true && canon(obj[bound]) != canon(b) {
				ck.errorf("%s: %s is %s, authored %s", where, bound, canon(obj[bound]), canon(b))
			}
		}
		if rawObj["format"] == "byte" && obj["contentEncoding"] != "base64" {
			ck.errorf("%s: format byte without contentEncoding base64", where)
		}
		if is30(v) {
			if _, ok := obj["nullable"]; ok {
				ck.errorf("%s: nullable", where)
			}
		}
		if req, ok := obj["required"].([]any); ok && len(req) == 0 {
			if r, _ := rawObj["required"].([]any); len(r) > 0 {
				ck.errorf("%s: a required list left empty", where)
			}
		}
		typ, hasType := obj["type"]
		switch {
		case binaryish(v, rawObj):
			if hasType || obj["format"] != nil {
				ck.errorf("%s: type %s and format %s kept for bytes", where, canon(typ), canon(obj["format"]))
			}
		case is30(v) && rawObj["nullable"] == true && rawObj["type"] != nil:
			if want := withNull(rawObj["type"]); canon(typ) != canon(want) {
				ck.errorf("%s: type %s, want %s", where, canon(typ), canon(want))
			}
		case canon(typ) != canon(rawObj["type"]):
			ck.errorf("%s: type %s where the authored type is %s", where, canon(typ), canon(rawObj["type"]))
		}
	})
}

// ---- expectations ----

// want describes a whole Projection. Its JSON texts write a reference to a
// Defs entry as "=>URI", where URI (relative to entryURI) is any URI
// Client.Schema accepts for the target; defs is keyed the same way. A
// schema with no keywords compares equal to true.
type want struct {
	root string
	defs map[string]string
}

// keyFor returns the Defs key whose Source is the Source of the schema at
// uri.
func keyFor(t testing.TB, c *openapi.Client, p *schema2020.Projection, uri string) string {
	t.Helper()
	src := schemaAt(t, c, uri).Source()
	for k, s := range p.Sources {
		if s == src {
			return k
		}
	}
	t.Errorf("no Defs entry has Source %s (for %s); Sources = %v", src, uri, p.Sources)
	return "<missing " + src + ">"
}

// sameSchema compares an emitted schema with an expected one, references
// by the key they name and an empty schema as true.
func sameSchema(t testing.TB, what string, got []byte, want any) {
	t.Helper()
	sameValue(t, what, emptyAsTrue(normalizeRefs(mustDecode(t, what, got))), emptyAsTrue(want))
}

func (w want) check(t testing.TB, c *openapi.Client, p *schema2020.Projection) {
	t.Helper()
	expand := func(what, text string) any {
		v, err := decode([]byte(text))
		if err != nil {
			t.Fatalf("expected %s is not JSON: %v\n%s", what, err, text)
		}
		var walk func(v any) any
		walk = func(v any) any {
			switch x := v.(type) {
			case map[string]any:
				for k, e := range x {
					x[k] = walk(e)
				}
			case []any:
				for i, e := range x {
					x[i] = walk(e)
				}
			case string:
				if uri, ok := strings.CutPrefix(x, "=>"); ok {
					return refFor(keyFor(t, c, p, uri))
				}
			}
			return v
		}
		return walk(v)
	}
	sameSchema(t, "Root", p.Root, expand("Root", w.root))
	if len(p.Defs) != len(w.defs) {
		t.Errorf("Defs has %d entries %q, want %d", len(p.Defs), slices.Sorted(maps.Keys(p.Defs)), len(w.defs))
	}
	for uri, text := range w.defs {
		k := keyFor(t, c, p, uri)
		got, ok := p.Defs[k]
		if !ok {
			t.Errorf("Defs has no key %q (for %s)", k, uri)
			continue
		}
		sameSchema(t, "Defs["+k+"]", got, expand("Defs["+k+"]", text))
	}
}

// issueWant is an Issue expected at At of the schema with Source source.
type issueWant struct {
	source, at string
}

// wantIssues requires exactly the Issues listed, in any order.
func wantIssues(t testing.TB, got []schema2020.Issue, want []issueWant) {
	t.Helper()
	g := map[issueWant]int{}
	for _, is := range got {
		g[issueWant{is.Source, is.At}]++
	}
	w := map[issueWant]int{}
	for _, x := range want {
		w[x]++
	}
	if !maps.Equal(g, w) {
		t.Errorf("Issues %+v, want %+v", got, want)
	}
}

// ---- YAML ----

// toYAML writes a JSON document as block-style YAML 1.2, keeping member
// order and number spellings; strings are double-quoted, which takes JSON's
// escapes (YAML 1.2.2 section 7.3.1).
func toYAML(doc string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	v, err := readOrdered(dec)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if _, ok := v.(ordered); !ok {
		return "", fmt.Errorf("document is not an object")
	}
	writeYAML(&b, v, 0)
	return b.String(), nil
}

type member struct {
	key string
	val any
}

type ordered []member

func readOrdered(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch x := tok.(type) {
	case json.Delim:
		if x == '{' {
			var o ordered
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := readOrdered(dec)
				if err != nil {
					return nil, err
				}
				o = append(o, member{k.(string), v})
			}
			_, err := dec.Token()
			if o == nil {
				o = ordered{}
			}
			return o, err
		}
		a := []any{}
		for dec.More() {
			v, err := readOrdered(dec)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err := dec.Token()
		return a, err
	}
	return tok, nil
}

func yamlScalar(v any) string {
	switch x := v.(type) {
	case string:
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(x)
		return strings.TrimSuffix(b.String(), "\n")
	case json.Number:
		return string(x)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return "null"
	case ordered:
		return "{}"
	case []any:
		return "[]"
	}
	return fmt.Sprint(v)
}

func yamlEmpty(v any) bool {
	switch x := v.(type) {
	case ordered:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return true // scalars are written inline
}

func writeYAML(b *strings.Builder, v any, indent int) {
	pad := strings.Repeat(" ", indent)
	switch x := v.(type) {
	case ordered:
		for _, m := range x {
			b.WriteString(pad + yamlScalar(m.key) + ":")
			if yamlEmpty(m.val) {
				b.WriteString(" " + yamlScalar(m.val) + "\n")
				continue
			}
			b.WriteString("\n")
			writeYAML(b, m.val, indent+2)
		}
	case []any:
		for _, e := range x {
			b.WriteString(pad + "-")
			if yamlEmpty(e) {
				b.WriteString(" " + yamlScalar(e) + "\n")
				continue
			}
			b.WriteString("\n")
			writeYAML(b, e, indent+2)
		}
	}
}
