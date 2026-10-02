package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Shared apparatus for the stage 5 document tests: a site of documents
// served over httptest that counts what it serves, an in-memory Fetch,
// JSON equivalence that compares numbers by exact value, a YAML emitter (the
// tests' own: no YAML library), position checks for rejections, UTF-16 and
// UTF-32 encoders, and a canonical dump of a Client's descriptors.

// A site is an httptest server, one origin, holding documents by path. A
// request for a path that holds a document gets it; a path with a handler
// gets the handler; any other request is an API call, recorded and
// answered 200 with an empty JSON object. Every request is counted by path.
type site struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string]string
	hooks map[string]http.HandlerFunc
	hits  map[string]int
	calls []rec
}

func newSite(t testing.TB) *site {
	t.Helper()
	s := &site{files: map[string]string{}, hooks: map[string]http.HandlerFunc{}, hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *site) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	path := r.URL.Path
	s.hits[path]++
	content, isFile := s.files[path]
	hook := s.hooks[path]
	if !isFile && hook == nil {
		s.calls = append(s.calls, rec{Method: r.Method, RequestURI: r.RequestURI, Path: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery, Header: r.Header.Clone(), Body: body, Host: r.Host})
	}
	s.mu.Unlock()
	switch {
	case hook != nil:
		hook(w, r)
	case isFile:
		io.WriteString(w, content)
	default:
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}
}

// put stores a document at path, with @SELF@ replaced by the site's URL.
func (s *site) put(path, content string) {
	s.mu.Lock()
	s.files[path] = strings.ReplaceAll(content, "@SELF@", s.URL)
	s.mu.Unlock()
}

// handle serves path with h.
func (s *site) handle(path string, h http.HandlerFunc) {
	s.mu.Lock()
	s.hooks[path] = h
	s.mu.Unlock()
}

// uri is the absolute URI of path on the site.
func (s *site) uri(path string) string { return s.URL + path }

// count is the number of requests the site received for path.
func (s *site) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

// total is the number of requests the site received.
func (s *site) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, h := range s.hits {
		n += h
	}
	return n
}

// apiCalls returns the API calls the site received.
func (s *site) apiCalls() []rec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// lastCall returns the most recent API call, failing when there is none.
func (s *site) lastCall(t testing.TB) rec {
	t.Helper()
	calls := s.apiCalls()
	if len(calls) == 0 {
		t.Fatalf("the site received no API call")
	}
	return calls[len(calls)-1]
}

// mustLoad loads uri with l, failing on any error.
func mustLoad(t testing.TB, l *openapi.Loader, uri string, opts *openapi.Options) *openapi.Client {
	t.Helper()
	if l == nil {
		l = &openapi.Loader{}
	}
	c, err := l.Load(t.Context(), uri, opts)
	if err != nil {
		t.Fatalf("Load(%q): %v", uri, err)
	}
	if c == nil {
		t.Fatalf("Load(%q) returned a nil Client and no error", uri)
	}
	return c
}

// A memFetch is a Loader.Fetch serving documents from memory by URI. It
// records each call, counts the contents the loader closes, and can report
// a final URI other than the one requested.
type memFetch struct {
	mu     sync.Mutex
	docs   map[string]string
	finals map[string]string // the final URI to report for a requested one
	errs   map[string]error  // an error to return for a requested one
	calls  []string
	opened int
	closed int
	before func(uri string) // called first, outside the lock, if set
}

func newMemFetch(docs map[string]string) *memFetch {
	return &memFetch{docs: docs, finals: map[string]string{}, errs: map[string]error{}}
}

func (m *memFetch) fetch(ctx context.Context, uri string) (io.ReadCloser, string, error) {
	if m.before != nil {
		m.before(uri)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, uri)
	if err := m.errs[uri]; err != nil {
		return nil, "", err
	}
	content, ok := m.docs[uri]
	if !ok {
		return nil, "", fmt.Errorf("no document at %s", uri)
	}
	m.opened++
	return &closeCounter{Reader: strings.NewReader(content), m: m}, m.finals[uri], nil
}

// callsTo is the number of calls for uri.
func (m *memFetch) callsTo(uri string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c == uri {
			n++
		}
	}
	return n
}

func (m *memFetch) callList() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

type closeCounter struct {
	io.Reader
	m    *memFetch
	once sync.Once
}

func (c *closeCounter) Close() error {
	c.once.Do(func() {
		c.m.mu.Lock()
		c.m.closed++
		c.m.mu.Unlock()
	})
	return nil
}

// canonJSON rewrites a JSON text in one canonical spelling: no whitespace,
// member order kept, each string quoted by strconv.Quote of its decoded
// value, and each number as the exact rational it denotes (big.Rat's
// RatString), so two texts are the same JSON, numbers by exact value
// whatever their spelling, when their canonical forms are equal. Neither
// the client's escaping nor its number spelling for converted YAML is
// stated, only that numbers keep the exact value written (load.go,
// Loader).
func canonJSON(b []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out strings.Builder
	// Each open container's state: whether a value has been written, and,
	// for an object, whether the next token is a name.
	type frame struct {
		object, written, name bool
	}
	var stack []frame
	sep := func() {
		if len(stack) == 0 {
			return
		}
		f := &stack[len(stack)-1]
		switch {
		case f.object && f.name:
			if f.written {
				out.WriteByte(',')
			}
			f.written = true
			f.name = false
		case f.object:
			out.WriteByte(':')
			f.name = true
		default:
			if f.written {
				out.WriteByte(',')
			}
			f.written = true
		}
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				sep()
				out.WriteByte(byte(v))
				stack = append(stack, frame{object: v == '{', name: true})
			default:
				out.WriteByte(byte(v))
				stack = stack[:len(stack)-1]
			}
		case string:
			sep()
			out.WriteString(strconv.Quote(v))
		case json.Number:
			sep()
			r, ok := new(big.Rat).SetString(string(v))
			if !ok {
				return "", fmt.Errorf("number %s", v)
			}
			out.WriteString(r.RatString())
		case bool:
			sep()
			out.WriteString(strconv.FormatBool(v))
		case nil:
			sep()
			out.WriteString("null")
		}
	}
	return out.String(), nil
}

// sameJSON fails unless got and want are the same JSON by canonJSON.
func sameJSON(t testing.TB, what string, got, want []byte) {
	t.Helper()
	g, err := canonJSON(got)
	if err != nil {
		t.Errorf("%s: %q is not JSON: %v", what, got, err)
		return
	}
	w, err := canonJSON(want)
	if err != nil {
		t.Fatalf("%s: the expected %q is not JSON: %v", what, want, err)
	}
	if g != w {
		t.Errorf("%s:\n got %s\nwant %s", what, got, want)
	}
}

// numberRE matches n as a whole number in a text.
func numberRE(n int) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(`(^|[^0-9])%d([^0-9]|$)`, n))
}

// wantPosition checks a rejection (load.go, Loader: "A rejection names the
// document's URI and the line and column of the problem, both counted from
// 1, the column in bytes after any byte order mark"): err names uri, line
// and one of cols, each line and column as a whole number. Callers choose
// documents whose line and column differ from each other and from any
// other number the text could hold.
func wantPosition(t testing.TB, err error, uri string, line int, cols ...int) {
	t.Helper()
	if err == nil {
		t.Errorf("no error, want a rejection at %d:%v", line, cols)
		return
	}
	msg := err.Error()
	if !strings.Contains(msg, uri) {
		t.Errorf("rejection %q does not name the document %q", msg, uri)
	}
	if !numberRE(line).MatchString(msg) {
		t.Errorf("rejection %q does not name line %d", msg, line)
	}
	for _, c := range cols {
		if numberRE(c).MatchString(msg) {
			return
		}
	}
	t.Errorf("rejection %q does not name column %v", msg, cols)
}

// rejected parses content at testDocURI and returns the error, failing
// when it loads.
func rejected(t testing.TB, content string) error {
	t.Helper()
	c, err := openapi.Parse(context.Background(), []byte(content), testDocURI, nil)
	if err == nil || c != nil {
		t.Fatalf("Parse = %v, %v; want a rejection of\n%s", c, err, content)
	}
	return err
}

// parsed parses content at testDocURI, failing on any error.
func parsed(t testing.TB, content []byte) *openapi.Client {
	t.Helper()
	c, err := openapi.Parse(context.Background(), content, testDocURI, nil)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, content)
	}
	return c
}

// utf16Text encodes s as UTF-16, big- or little-endian, with or without a
// byte order mark.
func utf16Text(s string, bigEndian, bom bool) []byte {
	var out []byte
	put := func(u uint16) {
		if bigEndian {
			out = append(out, byte(u>>8), byte(u))
		} else {
			out = append(out, byte(u), byte(u>>8))
		}
	}
	if bom {
		put(0xFEFF)
	}
	for _, u := range utf16.Encode([]rune(s)) {
		put(u)
	}
	return out
}

// utf32Text encodes s as UTF-32, big- or little-endian, with or without a
// byte order mark.
func utf32Text(s string, bigEndian, bom bool) []byte {
	var out []byte
	put := func(r rune) {
		u := uint32(r)
		if bigEndian {
			out = append(out, byte(u>>24), byte(u>>16), byte(u>>8), byte(u))
		} else {
			out = append(out, byte(u), byte(u>>8), byte(u>>16), byte(u>>24))
		}
	}
	if bom {
		put(0xFEFF)
	}
	for _, r := range s {
		put(r)
	}
	return out
}

// readJSON reads a JSON text into a jnode tree (oracle_test.go), keeping
// member order and number spellings.
func readJSON(b []byte) (jnode, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return readJNode(dec)
}

// A yamlStyle chooses how the emitter writes scalars. pick, if set, is
// asked for a choice among n, so a fuzz input can vary them; nil always
// takes the first (double-quoted strings, double-quoted keys).
type yamlStyle struct {
	pick func(n int) int
}

func (s yamlStyle) choose(n int) int {
	if s.pick == nil {
		return 0
	}
	return s.pick(n)
}

// yamlPrintable reports whether r may be written as itself in a YAML
// scalar: the c-printable set (YAML 1.2.2 section 5.1) without the
// characters some readers break lines at (NEL, LS, PS), the tab, and the
// byte order mark, which section 5.2 asks writers to escape.
func yamlPrintable(r rune) bool {
	switch {
	case r == '\t', r == 0x85, r == 0x2028, r == 0x2029, r == 0xFEFF:
		return false
	case 0x20 <= r && r <= 0x7E, 0xA0 <= r && r <= 0xD7FF, 0xE000 <= r && r <= 0xFFFD, 0x10000 <= r && r <= 0x10FFFF:
		return true
	}
	return false
}

// yamlDouble writes s as a YAML double-quoted scalar (YAML 1.2.2 section
// 7.3.1), escaping what is not printable.
func yamlDouble(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case yamlPrintable(r):
			b.WriteRune(r)
		case r <= 0xFFFF:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			fmt.Fprintf(&b, `\U%08X`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// yamlSingleOK reports whether s can be written single-quoted: every
// character printable (section 7.3.2 has no escapes, a quote being
// written twice).
func yamlSingleOK(s string) bool {
	for _, r := range s {
		if !yamlPrintable(r) {
			return false
		}
	}
	return utf8.ValidString(s)
}

// Plain scalars the emitter writes: a value only when the Core schema
// reads it as the string it is (it begins with a letter, and is not a
// boolean or null spelling), a key whenever its characters are safe in
// block and flow context alike, since keys are the strings they spell.
var (
	plainValueRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_./-]*$`)
	plainKeyRE   = regexp.MustCompile(`^[A-Za-z0-9_~][A-Za-z0-9_./~-]*$`)
	coreWords    = map[string]bool{"true": true, "True": true, "TRUE": true, "false": true, "False": true, "FALSE": true, "null": true, "Null": true, "NULL": true}
)

func (st yamlStyle) str(s string) string {
	switch st.choose(3) {
	case 1:
		if yamlSingleOK(s) {
			return "'" + strings.ReplaceAll(s, "'", "''") + "'"
		}
	case 2:
		if plainValueRE.MatchString(s) && !coreWords[s] {
			return s
		}
	}
	return yamlDouble(s)
}

func (st yamlStyle) key(k string) string {
	if st.choose(2) == 1 && plainKeyRE.MatchString(k) {
		return k
	}
	return yamlDouble(k)
}

func (st yamlStyle) scalar(n jnode) string {
	switch n.kind {
	case 's':
		return st.str(n.text)
	case 'z':
		return "null"
	}
	return n.text // numbers, true and false spell the same in YAML
}

// yamlBlock writes n as block YAML: mappings as "key: value" lines,
// sequences as "- " entries, each nested collection on the following lines
// indented by two spaces, and empty collections in flow style.
func yamlBlock(n jnode, st yamlStyle) string {
	var b strings.Builder
	var write func(n jnode, indent int)
	write = func(n jnode, indent int) {
		pad := strings.Repeat(" ", indent)
		for i, c := range n.items {
			if n.kind == 'o' {
				b.WriteString(pad + st.key(n.names[i]) + ":")
			} else {
				b.WriteString(pad + "-")
			}
			if (c.kind == 'o' || c.kind == 'a') && len(c.items) > 0 {
				b.WriteByte('\n')
				write(c, indent+2)
				continue
			}
			b.WriteByte(' ')
			b.WriteString(yamlFlow(c, st))
			b.WriteByte('\n')
		}
	}
	if (n.kind == 'o' || n.kind == 'a') && len(n.items) > 0 {
		write(n, 0)
	} else {
		b.WriteString(yamlFlow(n, st) + "\n")
	}
	return b.String()
}

// yamlFlow writes n as flow YAML: {key: value, ...} and [item, ...].
func yamlFlow(n jnode, st yamlStyle) string {
	switch n.kind {
	case 'o':
		parts := make([]string, len(n.items))
		for i, c := range n.items {
			parts[i] = st.key(n.names[i]) + ": " + yamlFlow(c, st)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case 'a':
		parts := make([]string, len(n.items))
		for i, c := range n.items {
			parts[i] = yamlFlow(c, st)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return st.scalar(n)
}

// toYAML converts a JSON document to block YAML, or to flow YAML after a
// comment line, so that its first significant byte is not '{'.
func toYAML(t testing.TB, doc []byte, flow bool, st yamlStyle) []byte {
	t.Helper()
	n, err := readJSON(doc)
	if err != nil {
		t.Fatalf("readJSON: %v", err)
	}
	if flow {
		return []byte("# flow style\n" + yamlFlow(n, st) + "\n")
	}
	return []byte(yamlBlock(n, st))
}

// describeAll writes every descriptor of c in one canonical text, Server
// IDs excluded (opaque), each Err as whether it is set and whether it wraps
// ErrUnresolved, and each Schema as its canonical Raw and its Source.
func describeAll(c *openapi.Client) string {
	var b strings.Builder
	errOf := func(err error) string {
		switch {
		case err == nil:
			return "-"
		case errors.Is(err, openapi.ErrUnresolved):
			return "unresolved"
		}
		return "err"
	}
	schema := func(s *openapi.Schema) string {
		if s == nil {
			return "nil"
		}
		raw, err := canonJSON(s.Raw())
		if err != nil {
			raw = "invalid " + err.Error()
		}
		return raw + " at " + s.Source() + " base " + s.Base()
	}
	var param func(indent string, p *openapi.Param)
	param = func(indent string, p *openapi.Param) {
		fmt.Fprintf(&b, "%sparam key=%q name=%q in=%q desc=%q req=%t dep=%t style=%q explode=%t/%t reserved=%t empty=%t ct=%q cf=%q src=%q err=%s schema=%s\n",
			indent, p.Key, p.Name, p.In, p.Description, p.Required, p.Deprecated, p.Style, p.Explode, p.ExplodeSet, p.AllowReserved, p.AllowEmptyValue,
			p.ContentType, p.CollectionFormat, p.Source, errOf(p.Err), schema(p.Schema))
		for _, h := range p.Headers {
			param(indent+"  ", h)
		}
	}
	message := func(what string, m *openapi.Message) {
		if m == nil {
			fmt.Fprintf(&b, "  %s nil\n", what)
			return
		}
		fmt.Fprintf(&b, "  %s key=%q desc=%q req=%t src=%q err=%s\n", what, m.Key, m.Description, m.Required, m.Source, errOf(m.Err))
		for _, h := range m.Headers {
			param("    header ", h)
		}
		for _, md := range m.Media {
			fmt.Fprintf(&b, "    media %q seq=%t src=%q err=%s schema=%s item=%s\n", md.Type, md.Sequential, md.Source, errOf(md.Err), schema(md.Schema), schema(md.ItemSchema))
			for _, e := range md.Encoding {
				param("      field ", e)
			}
		}
	}
	for _, op := range c.Operations() {
		fmt.Fprintf(&b, "op key=%q id=%q %s %q sum=%q desc=%q tags=%q dep=%t src=%q err=%s\n",
			op.Key, op.ID, op.Method, op.Path, op.Summary, op.Description, op.Tags, op.Deprecated, op.Source, errOf(op.Err))
		for _, p := range op.Params {
			param("  ", p)
		}
		message("body", op.Body)
		for _, r := range op.Responses {
			message("response", r)
		}
		for _, s := range op.Servers {
			fmt.Fprintf(&b, "  server url=%q name=%q desc=%q src=%q err=%s\n", s.URL, s.Name, s.Description, s.Source, errOf(s.Err))
			for _, v := range s.Variables {
				fmt.Fprintf(&b, "    var %q desc=%q declared=%t default=%q/%t enum=%q\n", v.Name, v.Description, v.Declared, v.Default, v.DefaultSet, v.Enum)
			}
		}
		for _, r := range op.Security {
			fmt.Fprintf(&b, "  security %s\n", r.Key)
			for _, sc := range r.Schemes {
				fmt.Fprintf(&b, "    scheme %q scopes=%q type=%q desc=%q in=%q name=%q scheme=%q bearer=%q oidc=%q src=%q err=%s\n",
					sc.Name, sc.Scopes, sc.Type, sc.Description, sc.In, sc.ParamName, sc.Scheme, sc.BearerFormat, sc.OpenIDConnectURL, sc.Source, errOf(sc.Err))
				for _, f := range sc.Flows {
					keys := make([]string, 0, len(f.Scopes))
					for k := range f.Scopes {
						keys = append(keys, k+"="+f.Scopes[k])
					}
					sort.Strings(keys)
					fmt.Fprintf(&b, "      flow %q %q %q %q %q\n", f.Type, f.AuthorizationURL, f.TokenURL, f.RefreshURL, keys)
				}
			}
		}
	}
	return b.String()
}

// encodingOf lists the fields of the Media Type Object an operation's
// request body declares first, as "name=content type@Source" (describe.go,
// Media.Encoding: "the properties the schema lists at its top level ...
// then those the schemas it reaches by $ref and allOf list"), so a test
// can see which Schema Object a reference reached: its properties, and
// where they are written.
func encodingOf(t testing.TB, c *openapi.Client, key string) []string {
	t.Helper()
	op := mustOp(t, c, key)
	if op.Body == nil || len(op.Body.Media) == 0 {
		t.Fatalf("%s has no request body media", key)
	}
	var out []string
	for _, p := range op.Body.Media[0].Encoding {
		out = append(out, p.Name+"="+p.ContentType+"@"+p.Source)
	}
	return out
}

// wantStrings fails unless got equals want.
func wantStrings(t testing.TB, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}
