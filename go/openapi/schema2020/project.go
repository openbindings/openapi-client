// Package schema2020 offers an optional JSON Schema 2020-12 view of
// OpenAPI schemas. It does not participate in loading or calling operations.
// Callers that need another dialect or cannot use a projection can inspect
// openapi.Schema.Raw, References, Source, Base, Dialect and Version instead.
package schema2020

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Direction says which use of a schema a projection represents.
type Direction uint8

const (
	Neutral  Direction = iota // preserve authored readOnly/writeOnly annotations
	Request                   // a value the client sends
	Response                  // a value the server sends
)

// Projection is a composable JSON Schema 2020-12 view. Root has no $schema
// or $defs; references in Root and Defs point to #/$defs/KEY. Defs contains
// the complete transitive closure. All three fields are caller-owned
// copies. Keys are stable within one openapi.Client and direction, so
// projections of the same direction may merge their Defs without
// collision. A key may have a different value in another direction.
type Projection struct {
	Root json.RawMessage
	Defs map[string]json.RawMessage

	// Sources maps each Defs key to the openapi.Schema.Source of the
	// authored schema it came from.
	Sources map[string]string
}

// Issue names one schema location that cannot be translated faithfully.
type Issue struct {
	// Source is the openapi.Schema.Source of the Root or Defs entry holding the
	// problem: for Root, that of the schema given to Project; for a Defs entry,
	// its Sources value.
	Source string
	At     string // JSON Pointer from that schema's Raw to the problem
	Err    error
}

// Error reports every independently detectable loss in a projection, each an
// Issue whose At names what was lost: a $ref, mapping value or defaultMapping
// that is unresolved or unreported, whose Err is the reference's own Err where
// openapi.Schema.References reports one; a $dynamicRef, for dynamic scope; a
// keyword Project removes as undefined in the authored edition; or, for a
// resource in another dialect, its $schema or, when it has none, its root.
type Error struct {
	Issues []Issue
}

// Error names each Source and At with its reason.
func (e *Error) Error() string {
	var parts []string
	for _, issue := range e.Issues {
		parts = append(parts, fmt.Sprintf("%s%s: %v", issue.Source, issue.At, issue.Err))
	}
	return "openapi/schema2020: " + strings.Join(parts, "; ")
}

// Unwrap returns each issue's Err in order.
func (e *Error) Unwrap() []error {
	out := make([]error, len(e.Issues))
	for i, issue := range e.Issues {
		out[i] = issue.Err
	}
	return out
}

// Project converts a schema s of the Client c to a standalone JSON Schema
// 2020-12 schema for direction. s must come from c or from a Client derived
// from c with With; Project returns an error, and no Projection, when c has
// loaded no document at the URI s is written in. Root is the schema itself.
// Each $ref, discriminator mapping value and defaultMapping value that Project
// keeps and openapi.Schema.References reports with a Target is rewritten to
// #/$defs/KEY for that Target, and Defs holds every schema these references
// reach, transitively, and nothing else; a $dynamicRef is lost, as below. No
// emitted schema carries $id, $schema, $anchor, $dynamicAnchor or $defs: a
// referenced nested $defs entry becomes a Defs entry, and an unreferenced one
// is dropped. A schema written at #/components/schemas/NAME of the entry
// document, or #/definitions/NAME in Swagger 2.0, has the key NAME when NAME
// contains no "#"; any other key is the schema's Source written as a URI
// reference relative to the entry document's URI, so that the key resolves
// against that URI to the Source. In a reference, a key is escaped as a JSON
// Pointer token and then as RFC 3986 section 3.5 requires of a fragment. The same input gives the same output.
//
// Each schema, the Root and every Defs entry, is read under its own
// openapi.Schema.Version. In OpenAPI 3.1 and 3.2, readOnly and writeOnly are
// annotations, so such a schema is written the same in every direction: as
// authored, apart from the rewritten references, the keywords removed above and
// the losses Issues report. A Defs entry it reaches in a Swagger 2.0 or OpenAPI
// 3.0 document follows that edition. In OpenAPI 3.0, a Request projection
// removes each readOnly property from required, and a Response projection each
// writeOnly one. In Swagger 2.0, whose readOnly properties must not be sent, a
// Request projection removes each readOnly property from required and replaces
// its schema with false. A required list left empty is removed. A property is
// readOnly or writeOnly when any declaration of it says so at the root of its
// schema, after following $ref; its declarations are those in the object's
// properties and in the properties of every schema the object reaches through
// allOf.
//
// A Swagger 2.0 or OpenAPI 3.0 schema is also translated: members beside a
// reported $ref are dropped; type: string with format: binary loses both
// keywords, and Swagger 2.0 type: file loses its type and any format other
// than byte; and format: byte becomes contentEncoding: base64, which replaces
// any contentEncoding the schema has. A boolean exclusiveMinimum or
// exclusiveMaximum that is true becomes exclusiveMinimum or exclusiveMaximum
// with the value of minimum or maximum, which is removed; one that is false or
// has no bound is removed. In OpenAPI 3.0, nullable is removed and, where the
// schema then has a type, true adds "null" to it as a type list unless it
// already allows null; enum is unchanged. A keyword that the edition's Schema
// Object does not define and that JSON Schema 2020-12 treats as an applicator
// or an assertion, such as oneOf in Swagger 2.0 or const in OpenAPI 3.0, is
// removed with an Issue, since it has no meaning in the authored edition. So
// are contentSchema, whose schema those editions do not define, and items
// written as an array, a form neither edition defines. Every other keyword is
// kept, as an annotation where the edition does not define it. Project never
// validates an instance, invents a fact or repairs a schema.
//
// Project returns an *Error, and with it the Projection, when part of the
// schema cannot be carried: a reference that cannot be resolved, or that
// openapi.Schema.References does not report for the schema holding it (such as
// a $ref in a Swagger 2.0 items object, or one that is not a string), which is
// removed alone; a $dynamicRef, whose target depends on dynamic scope; a
// keyword removed with an Issue, as above; or a schema resource in a dialect
// other than those openapi.Schema reads. Each Issue's At names the keyword
// concerned, which then contributes nothing: a reference or removed keyword is
// dropped, with its mapping entry for a mapping value; a resource in another
// dialect, named by its $schema or, when it has none, by its root, becomes
// true, the schema that accepts anything; and a schema left with no keywords
// becomes true when an Issue removed one of them, and is written {} otherwise.
// Callers that need the lost parts can use the authored graph through
// openapi.Schema and openapi.Client.DocumentURIs with their own dialect-aware
// processor. Project is lazy, retrieves no documents and retains nothing after
// it returns. It is safe to call concurrently, and each call returns what it
// would alone.
func Project(c *openapi.Client, s *openapi.Schema, direction Direction) (*Projection, error) {
	if c == nil || s == nil {
		return nil, errors.New("openapi/schema2020: Project needs a client and a schema")
	}
	uris := c.DocumentURIs()
	if doc, _, _ := strings.Cut(s.Source(), "#"); !slices.Contains(uris, doc) {
		return nil, fmt.Errorf("openapi/schema2020: the client has not loaded the document of %s", s.Source())
	}
	p := &projector{c: c, dir: direction, entry: uris[0], components: "/components/schemas/", loaded: map[string]*schema{}}
	if c.Version() == "2.0" {
		p.components = "/definitions/"
	}
	root := p.load(s)
	p.emit(root)
	proj := &Projection{Root: root.out, Defs: map[string]json.RawMessage{}, Sources: map[string]string{}}
	// Emitting an entry can queue more; the queue is the closure, each
	// schema in it emitted once.
	for i := 0; i < len(p.queue); i++ {
		t := p.queue[i]
		if t == root {
			proj.Defs[t.key] = bytes.Clone(root.out)
		} else {
			p.emit(t)
			proj.Defs[t.key] = t.out
		}
		proj.Sources[t.key] = t.src
	}
	if len(p.issues) > 0 {
		return proj, &Error{Issues: p.issues}
	}
	return proj, nil
}

// A projector holds the state of one call to Project, none of which
// outlives it.
type projector struct {
	c          *openapi.Client
	dir        Direction
	entry      string             // the entry document's URI
	components string             // where the entry document names its schemas
	loaded     map[string]*schema // by Source
	queue      []*schema          // the Defs entries, in the order first referred to
	issues     []Issue
	flags      map[node]map[string]bool // the names flagged finds, by object
	walking    map[node]int             // the objects flagged is in, by their place in its walk
	component  []node                   // walked objects whose component is not yet complete

	cur     *schema      // the schema being emitted
	ptr     []byte       // the JSON Pointer in cur's Raw of the node being emitted
	covered bool         // a handle on a subtree of cur has read the references at ptr
	out     bytes.Buffer // cur's output
}

// A schema is one authored schema, read once per call.
type schema struct {
	src     string
	root    node
	edition int    // 20, 30, 31 or 32: Swagger 2.0 or OpenAPI 3.0, 3.1 or 3.2
	foreign string // the dialect it is written in, if Project does not support it
	refs    map[string]*openapi.SchemaReference
	refsErr error // why References failed, which leaves every reference unresolved
	queued  bool
	key     string // its Defs key, once queued
	out     []byte
}

func (p *projector) load(h *openapi.Schema) *schema {
	src := h.Source()
	if s := p.loaded[src]; s != nil {
		return s
	}
	s := &schema{src: src, root: parse(h.Raw()), edition: 31}
	switch v := h.Version(); {
	case v == "2.0":
		s.edition = 20
	case strings.HasPrefix(v, "3.0"):
		s.edition = 30
	case strings.HasPrefix(v, "3.2"):
		s.edition = 32
	}
	if s.edition >= 31 {
		if d := h.Dialect(); !supported(d) {
			s.foreign = d
		}
	}
	refs, err := h.References()
	if err != nil {
		s.refsErr = fmt.Errorf("reference not resolvable: %w", err)
	}
	if len(refs) > 0 {
		s.refs = make(map[string]*openapi.SchemaReference, len(refs))
		for i := range refs {
			s.refs[refs[i].At] = &refs[i]
		}
	}
	p.loaded[src] = s
	return s
}

// supported reports whether Project carries a dialect: JSON Schema 2020-12
// and the OpenAPI 3.1 and 3.2 dialects, the ones the client reads.
func supported(dialect string) bool {
	switch dialect {
	case "https://json-schema.org/draft/2020-12/schema",
		"https://spec.openapis.org/oas/3.1/dialect/base",
		"https://spec.openapis.org/oas/3.1/dialect/2024-10-25",
		"https://spec.openapis.org/oas/3.1/dialect/2024-11-10",
		"https://spec.openapis.org/oas/3.2/dialect/2025-09-17",
		"https://spec.openapis.org/oas/3.2/dialect/2026-02-26":
		return true
	}
	return false
}

// target returns the Defs key of the schema h, queueing it when first
// referred to.
func (p *projector) target(h *openapi.Schema) string {
	t := p.load(h)
	if !t.queued {
		t.queued, t.key = true, p.keyOf(t.src)
		p.queue = append(p.queue, t)
	}
	return t.key
}

// keyOf is the key of the schema with Source src: a component name of the
// entry document, or else src relative to the entry document, which has a
// fragment and so cannot equal a name.
func (p *projector) keyOf(src string) string {
	doc, frag, _ := strings.Cut(src, "#")
	if doc != p.entry {
		return relative(p.entry, doc) + "#" + frag
	}
	if ptr, err := url.PathUnescape(frag); err == nil {
		if name, ok := strings.CutPrefix(ptr, p.components); ok && !strings.ContainsAny(name, "/#") {
			return strings.ReplaceAll(strings.ReplaceAll(name, "~1", "/"), "~0", "~")
		}
	}
	return "#" + frag
}

// relative writes the absolute URI target as a reference relative to
// base, also absolute and without a fragment: a relative path where both
// have the same scheme, authority and a path, a network-path reference
// where only the scheme is shared, and otherwise target itself.
func relative(base, target string) string {
	scheme, rest, ok := strings.Cut(target, ":")
	bscheme, brest, _ := strings.Cut(base, ":")
	if !ok || scheme != bscheme || !strings.HasPrefix(rest, "//") || !strings.HasPrefix(brest, "//") {
		return target
	}
	auth, path := splitAuthority(rest[2:])
	bauth, bpath := splitAuthority(brest[2:])
	path, query, hasQuery := strings.Cut(path, "?")
	bpath, _, _ = strings.Cut(bpath, "?")
	if auth != bauth || path == "" || bpath == "" {
		return rest
	}
	// Both paths start with "/". Climb from base's directory to the deepest
	// directory the two share, then descend to target.
	dir := bpath[:strings.LastIndexByte(bpath, '/')+1]
	i := 0
	for i < len(dir) && i < len(path) && dir[i] == path[i] {
		i++
	}
	i = strings.LastIndexByte(dir[:i], '/') + 1
	rel := strings.Repeat("../", strings.Count(dir[i:], "/")) + path[i:]
	// An empty first segment would make an empty or absolute path, and a
	// colon in it a scheme (RFC 3986 section 4.2).
	if seg, _, _ := strings.Cut(rel, "/"); seg == "" || strings.Contains(seg, ":") {
		rel = "./" + rel
	}
	if hasQuery {
		rel += "?" + query
	}
	return rel
}

func splitAuthority(s string) (authority, rest string) {
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}
