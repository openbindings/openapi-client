package openapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// An RFC 6570 expander, written from the RFC's text alone: section 2.3
// (defined and undefined values), section 3.1 (literals), section 3.2.1
// (variable expansion), the per-operator sections 3.2.2 to 3.2.9, and the
// operator table of Appendix A. It is the independent oracle for the RFC
// 6570-backed styles, by OAS 3.1.2 Appendix C.1: simple has no operator,
// matrix is ";", label is ".", form is "?", and allowReserved true is "+".
// Prefix modifiers (section 2.4.1) are not used by OpenAPI and are not
// implemented. TestOracleRFC6570Examples checks the expander against the
// RFC's own examples.
//
// Where the normative section 3.2.1 and the informative Appendix A read
// differently, section 3.2.1 is followed (doc.go, Fixed rules, Styles:
// "RFC 6570's normative text governs where its informative Appendix A
// differs"): an exploded
// associative array's member names are encoded "in the same way as simple
// string values", and an exploded pair whose value is the empty string is
// written as its name alone except for the "?" and "&" operators, where
// Appendix A would write "name=" under an operator that is not named.

// ukind is the kind of an RFC 6570 value (section 2.3).
type ukind int

const (
	uundef ukind = iota // undefined, such as null
	ustring
	ulist
	uassoc
)

// uval is a variable's value in RFC 6570's model.
type uval struct {
	kind  ukind
	s     string   // ustring
	list  []string // ulist: the list's defined members
	pairs []upair  // uassoc: the associative array's pairs, in order
	// allUndef marks a list whose members are all undefined, such as
	// [null]: defined, since it has members (section 2.3), with no defined
	// member to expand.
	allUndef bool
}

// upair is one (name, value) pair; a pair whose value is undefined is
// skipped by expansion (section 2.4.2: "only the defined pairs are present").
type upair struct {
	name, value string
	undef       bool
}

func ustr(s string) uval               { return uval{kind: ustring, s: s} }
func ulistOf(items ...string) uval     { return uval{kind: ulist, list: items} }
func uassocOf(pairs ...upair) uval     { return uval{kind: uassoc, pairs: pairs} }
func upairOf(name, value string) upair { return upair{name: name, value: value} }

// defined reports whether v has a value (section 2.3): "A variable defined
// as a list value is considered undefined if the list contains zero members.
// A variable defined as an associative array of (name, value) pairs is
// considered undefined if the array contains zero members or if all member
// names in the array are associated with undefined values." The empty string
// is defined.
func (v uval) defined() bool {
	switch v.kind {
	case ustring:
		return true
	case ulist:
		return len(v.list) > 0 || v.allUndef
	case uassoc:
		for _, p := range v.pairs {
			if !p.undef {
				return true
			}
		}
	}
	return false
}

// uspec is one varspec: the variable name as the template writes it
// (percent-encoded beforehand where it is not a legal varname, as OAS 3.1.2
// Appendix C.3 requires), the explode modifier, and the value.
type uspec struct {
	name    string
	explode bool
	value   uval
}

// uallow is an expansion's allowed set (Appendix A, "allow").
type uallow int

const (
	allowU    uallow = iota // unreserved (U)
	allowUR                 // unreserved, reserved and pct-encoded (U+R)
	allowNone               // no percent-encoding at all: OAS 3.1.2 section 4.8.12.2.2, for headers
	// U+R in a query, but for "#", which would end the query (doc.go,
	// Fixed rules, Percent-encoding: under allowReserved, "#" in a query is
	// encoded)
	allowURQuery
)

// uop is an operator's row of the Appendix A table.
type uop struct {
	first, sep string
	named      bool
	ifemp      string
	allow      uallow
}

var uops = map[string]uop{
	"":  {"", ",", false, "", allowU},
	"+": {"", ",", false, "", allowUR},
	".": {".", ".", false, "", allowU},
	"/": {"/", "/", false, "", allowU},
	";": {";", ";", true, "", allowU},
	"?": {"?", "&", true, "=", allowU},
	"&": {"&", "&", true, "=", allowU},
	"#": {"#", ",", false, "", allowUR},
}

// Constructions for OpenAPI configurations with no single RFC 6570
// operator (OAS 3.1.2 Appendix C.3): form with allowReserved true is the
// "?" operator's structure with reserved expansion of the values; a header
// is simple expansion with URI percent-encoding removed (section
// 4.8.12.2.2: "URI percent-encoding MUST NOT be applied").
var (
	uformReserved = uop{"?", "&", true, "=", allowUR}
	uheader       = uop{"", ",", false, "", allowNone}
)

// uexpand expands the expression {<op><specs>} (section 3.2).
func uexpand(op string, specs ...uspec) string {
	o, ok := uops[op]
	if !ok {
		panic("oracle: unknown operator " + op)
	}
	return uexpandOp(o, specs...)
}

func uexpandOp(o uop, specs ...uspec) string {
	var b strings.Builder
	enc := func(s string) string { return uencode(s, o.allow) }
	lit := func(s string) string {
		if o.allow == allowNone {
			return s
		}
		return uliteral(s)
	}
	first := true
	for _, sp := range specs {
		v := sp.value
		if !v.defined() {
			continue // section 3.2.1: an undefined variable "is ignored"
		}
		if v.allUndef && sp.explode {
			// Exploded, a list with no defined member writes nothing, the
			// operator's prefix included (RFC 6570 does not settle it).
			continue
		}
		if first {
			b.WriteString(o.first)
			first = false
		} else {
			b.WriteString(o.sep)
		}
		switch {
		case v.kind == ustring:
			// A string: explode has no effect (section 3.2.1).
			if o.named {
				b.WriteString(lit(sp.name))
				if v.s == "" {
					b.WriteString(o.ifemp)
					continue
				}
				b.WriteString("=")
			}
			b.WriteString(enc(v.s))
		case !sp.explode:
			// A composite without explode: "a comma-separated concatenation"
			// of the members, or of each defined pair as "name,value". A named
			// operator appends "=" only "if the variable's value is not
			// empty" (section 3.2.7), so a list whose expansion is empty, as
			// [""] is, takes ifemp (";p" under matrix).
			var parts []string
			if v.kind == ulist {
				for _, m := range v.list {
					parts = append(parts, enc(m))
				}
			} else {
				for _, p := range v.pairs {
					if !p.undef {
						parts = append(parts, enc(p.name)+","+enc(p.value))
					}
				}
			}
			joined := strings.Join(parts, ",")
			if o.named {
				b.WriteString(lit(sp.name))
				if joined == "" {
					b.WriteString(o.ifemp)
					continue
				}
				b.WriteString("=")
			}
			b.WriteString(joined)
		case o.named:
			// Exploded, named: a list's members each paired with the
			// variable's name, a pair's name and value; ifemp for an empty
			// value.
			n := 0
			emit := func(name, value string) {
				if n > 0 {
					b.WriteString(o.sep)
				}
				n++
				b.WriteString(name)
				if value == "" {
					b.WriteString(o.ifemp)
					return
				}
				b.WriteString("=")
				b.WriteString(enc(value))
			}
			if v.kind == ulist {
				for _, m := range v.list {
					emit(lit(sp.name), m)
				}
			} else {
				for _, p := range v.pairs {
					if !p.undef {
						emit(enc(p.name), p.value)
					}
				}
			}
		default:
			// Exploded, not named: members separated by the operator's
			// separator; pairs as "name=value", or the name alone for an empty
			// value (section 3.2.1).
			var parts []string
			if v.kind == ulist {
				for _, m := range v.list {
					parts = append(parts, enc(m))
				}
			} else {
				for _, p := range v.pairs {
					if p.undef {
						continue
					}
					if p.value == "" {
						parts = append(parts, enc(p.name))
					} else {
						parts = append(parts, enc(p.name)+"="+enc(p.value))
					}
				}
			}
			b.WriteString(strings.Join(parts, o.sep))
		}
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
}

func isReserved(c byte) bool {
	return strings.IndexByte(":/?#[]@!$&'()*+,;=", c) >= 0
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'A' <= c && c <= 'F' || 'a' <= c && c <= 'f'
}

// uencode applies an allowed set to a value (section 3.2.1): a character
// outside it is written as the pct-encoded triplets of its UTF-8 octets; for
// U+R, a "%" passes only as part of a pct-encoded triplet, whose HEXDIG are
// case-insensitive (section 1.5).
func uencode(s string, allow uallow) string {
	if allow == allowNone {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case isUnreserved(c):
			b.WriteByte(c)
		case allow == allowUR && isReserved(c), allow == allowURQuery && isReserved(c) && c != '#':
			b.WriteByte(c)
		case (allow == allowUR || allow == allowURQuery) && c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b.WriteString(s[i : i+3])
			i += 2
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// uliteral encodes template literal text, and a variable name written by a
// named operator "as if it were a literal string" (section 3.1): characters
// allowed anywhere in a URI (unreserved, reserved, pct-encoded) are copied.
func uliteral(s string) string { return uencode(s, allowUR) }

// pctName is a parameter or member name percent-encoded as the package
// documentation says (doc.go, Fixed rules, Percent-encoding: names "encode
// every byte outside RFC 3986's unreserved set as %XX in uppercase hex"),
// which is also how OAS 3.1.2 Appendix C.3 makes a name that is not a legal
// varname usable in a template.
func pctName(s string) string { return uencode(s, allowU) }

// utemplate expands a template of literals and expressions (section 3),
// with vars giving values by name; a varspec ending in "*" is exploded.
func utemplate(tmpl string, vars map[string]uval) string {
	var b strings.Builder
	for {
		open := strings.IndexByte(tmpl, '{')
		if open < 0 {
			b.WriteString(uliteral(tmpl))
			return b.String()
		}
		b.WriteString(uliteral(tmpl[:open]))
		end := strings.IndexByte(tmpl[open:], '}')
		expr := tmpl[open+1 : open+end]
		tmpl = tmpl[open+end+1:]
		op := ""
		if expr != "" && strings.IndexByte("+#./;?&", expr[0]) >= 0 {
			op, expr = expr[:1], expr[1:]
		}
		var specs []uspec
		for _, name := range strings.Split(expr, ",") {
			explode := strings.HasSuffix(name, "*")
			name = strings.TrimSuffix(name, "*")
			specs = append(specs, uspec{name: name, explode: explode, value: vars[name]})
		}
		b.WriteString(uexpand(op, specs...))
	}
}

// The expander reproduces the examples of RFC 6570 sections 2.4.2 and 3.2.1
// to 3.2.9, with the variable values section 3.2 defines, except those with
// a prefix modifier.
func TestOracleRFC6570Examples(t *testing.T) {
	vars := map[string]uval{
		"count":      ulistOf("one", "two", "three"),
		"dom":        ulistOf("example", "com"),
		"dub":        ustr("me/too"),
		"hello":      ustr("Hello World!"),
		"half":       ustr("50%"),
		"var":        ustr("value"),
		"who":        ustr("fred"),
		"base":       ustr("http://example.com/home/"),
		"path":       ustr("/foo/bar"),
		"list":       ulistOf("red", "green", "blue"),
		"keys":       uassocOf(upairOf("semi", ";"), upairOf("dot", "."), upairOf("comma", ",")),
		"v":          ustr("6"),
		"x":          ustr("1024"),
		"y":          ustr("768"),
		"empty":      ustr(""),
		"empty_keys": uassocOf(),
		"undef":      {},
		"year":       ulistOf("1965", "2000", "2012"),
	}
	examples := [][2]string{
		// Section 2.4.2.
		{"find{?year*}", "find?year=1965&year=2000&year=2012"},
		{"www{.dom*}", "www.example.com"},
		// Section 3.2.1.
		{"{count}", "one,two,three"},
		{"{count*}", "one,two,three"},
		{"{/count}", "/one,two,three"},
		{"{/count*}", "/one/two/three"},
		{"{;count}", ";count=one,two,three"},
		{"{;count*}", ";count=one;count=two;count=three"},
		{"{?count}", "?count=one,two,three"},
		{"{?count*}", "?count=one&count=two&count=three"},
		{"{&count*}", "&count=one&count=two&count=three"},
		// Section 3.2.2.
		{"{var}", "value"},
		{"{hello}", "Hello%20World%21"},
		{"{half}", "50%25"},
		{"O{empty}X", "OX"},
		{"O{undef}X", "OX"},
		{"{x,y}", "1024,768"},
		{"{x,hello,y}", "1024,Hello%20World%21,768"},
		{"?{x,empty}", "?1024,"},
		{"?{x,undef}", "?1024"},
		{"?{undef,y}", "?768"},
		{"{list}", "red,green,blue"},
		{"{list*}", "red,green,blue"},
		{"{keys}", "semi,%3B,dot,.,comma,%2C"},
		{"{keys*}", "semi=%3B,dot=.,comma=%2C"},
		// Section 3.2.3.
		{"{+var}", "value"},
		{"{+hello}", "Hello%20World!"},
		{"{+half}", "50%25"},
		{"{base}index", "http%3A%2F%2Fexample.com%2Fhome%2Findex"},
		{"{+base}index", "http://example.com/home/index"},
		{"O{+empty}X", "OX"},
		{"O{+undef}X", "OX"},
		{"{+path}/here", "/foo/bar/here"},
		{"here?ref={+path}", "here?ref=/foo/bar"},
		{"up{+path}{var}/here", "up/foo/barvalue/here"},
		{"{+x,hello,y}", "1024,Hello%20World!,768"},
		{"{+path,x}/here", "/foo/bar,1024/here"},
		{"{+list}", "red,green,blue"},
		{"{+list*}", "red,green,blue"},
		{"{+keys}", "semi,;,dot,.,comma,,"},
		{"{+keys*}", "semi=;,dot=.,comma=,"},
		// Section 3.2.4.
		{"{#var}", "#value"},
		{"{#hello}", "#Hello%20World!"},
		{"{#half}", "#50%25"},
		{"foo{#empty}", "foo#"},
		{"foo{#undef}", "foo"},
		{"{#x,hello,y}", "#1024,Hello%20World!,768"},
		{"{#path,x}/here", "#/foo/bar,1024/here"},
		{"{#list}", "#red,green,blue"},
		{"{#list*}", "#red,green,blue"},
		{"{#keys}", "#semi,;,dot,.,comma,,"},
		{"{#keys*}", "#semi=;,dot=.,comma=,"},
		// Section 3.2.5.
		{"{.who}", ".fred"},
		{"{.who,who}", ".fred.fred"},
		{"{.half,who}", ".50%25.fred"},
		{"www{.dom*}", "www.example.com"},
		{"X{.var}", "X.value"},
		{"X{.empty}", "X."},
		{"X{.undef}", "X"},
		{"X{.list}", "X.red,green,blue"},
		{"X{.list*}", "X.red.green.blue"},
		{"X{.keys}", "X.semi,%3B,dot,.,comma,%2C"},
		{"X{.keys*}", "X.semi=%3B.dot=..comma=%2C"},
		{"X{.empty_keys}", "X"},
		{"X{.empty_keys*}", "X"},
		// Section 3.2.6.
		{"{/who}", "/fred"},
		{"{/who,who}", "/fred/fred"},
		{"{/half,who}", "/50%25/fred"},
		{"{/who,dub}", "/fred/me%2Ftoo"},
		{"{/var}", "/value"},
		{"{/var,empty}", "/value/"},
		{"{/var,undef}", "/value"},
		{"{/var,x}/here", "/value/1024/here"},
		{"{/list}", "/red,green,blue"},
		{"{/list*}", "/red/green/blue"},
		{"{/keys}", "/semi,%3B,dot,.,comma,%2C"},
		{"{/keys*}", "/semi=%3B/dot=./comma=%2C"},
		// Section 3.2.7.
		{"{;who}", ";who=fred"},
		{"{;half}", ";half=50%25"},
		{"{;empty}", ";empty"},
		{"{;v,empty,who}", ";v=6;empty;who=fred"},
		{"{;v,bar,who}", ";v=6;who=fred"},
		{"{;x,y}", ";x=1024;y=768"},
		{"{;x,y,empty}", ";x=1024;y=768;empty"},
		{"{;x,y,undef}", ";x=1024;y=768"},
		{"{;list}", ";list=red,green,blue"},
		{"{;list*}", ";list=red;list=green;list=blue"},
		{"{;keys}", ";keys=semi,%3B,dot,.,comma,%2C"},
		{"{;keys*}", ";semi=%3B;dot=.;comma=%2C"},
		// Section 3.2.8.
		{"{?who}", "?who=fred"},
		{"{?half}", "?half=50%25"},
		{"{?x,y}", "?x=1024&y=768"},
		{"{?x,y,empty}", "?x=1024&y=768&empty="},
		{"{?x,y,undef}", "?x=1024&y=768"},
		{"{?list}", "?list=red,green,blue"},
		{"{?list*}", "?list=red&list=green&list=blue"},
		{"{?keys}", "?keys=semi,%3B,dot,.,comma,%2C"},
		{"{?keys*}", "?semi=%3B&dot=.&comma=%2C"},
		// Section 3.2.9.
		{"{&who}", "&who=fred"},
		{"{&half}", "&half=50%25"},
		{"?fixed=yes{&x}", "?fixed=yes&x=1024"},
		{"{&x,y,empty}", "&x=1024&y=768&empty="},
		{"{&x,y,undef}", "&x=1024&y=768"},
		{"{&list}", "&list=red,green,blue"},
		{"{&list*}", "&list=red&list=green&list=blue"},
		{"{&keys}", "&keys=semi,%3B,dot,.,comma,%2C"},
		{"{&keys*}", "&semi=%3B&dot=.&comma=%2C"},
	}
	for _, ex := range examples {
		if got := utemplate(ex[0], vars); got != ex[1] {
			t.Errorf("%s = %q, want %q", ex[0], got, ex[1])
		}
	}
	// OAS 3.1.2 Appendix C.4.1, C.4.3 and C.4.4.
	formulas := uassocOf(upairOf("a", "x+y"), upairOf("b", "x/y"), upairOf("c", "x^y"))
	words := ulistOf("math", "is", "fun")
	if got := utemplate("{?formulas*,words}", map[string]uval{"formulas": formulas, "words": words}); got != "?a=x%2By&b=x%2Fy&c=x%5Ey&words=math,is,fun" {
		t.Errorf("C.4.1: %q", got)
	}
	if got := utemplate("{?formulas*,words}", map[string]uval{"formulas": uassocOf(), "words": ulistOf("hello", "world")}); got != "?words=hello,world" {
		t.Errorf("C.4.3: %q", got)
	}
	heart := pctName("❤️")
	if got := uexpand("?", uspec{name: heart, value: ustr("love!")}); got != "?%E2%9D%A4%EF%B8%8F=love%21" {
		t.Errorf("C.4.4: %q", got)
	}
}

// jnode is JSON data in the order encoding/json writes it.
type jnode struct {
	kind  byte // 'z' null, 's' string, 'p' number or boolean, 'a' array, 'o' object
	text  string
	items []jnode  // an array's items, or an object's member values
	names []string // an object's member names
}

// jsonData converts v to JSON data as encoding/json would (doc.go, Values:
// "The client first converts a value to JSON data as encoding/json would
// (struct tags, MarshalJSON, TextMarshaler map keys)"), keeping member order
// and number spellings.
func jsonData(t testing.TB, v any) jnode {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(%#v): %v", v, err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	n, err := readJNode(dec)
	if err != nil {
		t.Fatalf("reading %s: %v", b, err)
	}
	return n
}

func readJNode(dec *json.Decoder) (jnode, error) {
	tok, err := dec.Token()
	if err != nil {
		return jnode{}, err
	}
	switch tok := tok.(type) {
	case nil:
		return jnode{kind: 'z'}, nil
	case string:
		return jnode{kind: 's', text: tok}, nil
	case json.Number:
		return jnode{kind: 'p', text: tok.String()}, nil
	case bool:
		return jnode{kind: 'p', text: fmt.Sprint(tok)}, nil
	case json.Delim:
		n := jnode{kind: 'a'}
		if tok == '{' {
			n.kind = 'o'
		}
		for dec.More() {
			if n.kind == 'o' {
				name, err := dec.Token()
				if err != nil {
					return n, err
				}
				n.names = append(n.names, name.(string))
			}
			item, err := readJNode(dec)
			if err != nil {
				return n, err
			}
			n.items = append(n.items, item)
		}
		if _, err := dec.Token(); err != nil {
			return n, err
		}
		return n, nil
	}
	return jnode{}, io.ErrUnexpectedEOF
}

var errOracleNesting = errors.New("oracle: nested value")

// jUndefined reports JSON data that is undefined (doc.go, Values: "null, an
// empty array and an object whose members are all undefined are undefined,
// as RFC 6570 says"), at any depth.
func jUndefined(n jnode) bool {
	switch n.kind {
	case 'z':
		return true
	case 'a':
		return len(n.items) == 0
	case 'o':
		for _, it := range n.items {
			if !jUndefined(it) {
				return false
			}
		}
		return true
	}
	return false
}

// uvalOf is JSON data as an RFC 6570 value (doc.go, Values: "An undefined
// member or array item is skipped, as RFC 6570 section 3.2.1 expands only
// defined ones"; a number or boolean in its JSON spelling). Undefinedness is
// settled first (doc.go, Fixed rules, Styles: "Whether a value is undefined
// ... is settled first; the refusals here apply to defined values"): an
// undefined value is never nesting, and an undefined member or array item
// (null, [] or {}) is skipped. Nesting of a defined collection, which the
// RFC 6570 styles refuse, is an error. A list whose items are all
// undefined, such as [null], is defined with no defined member (RFC 6570
// section 2.3: a list is undefined only "if the list contains zero
// members"): without explode it expands as "" does in every style,
// spaceDelimited and pipeDelimited included, and exploded it writes nothing,
// prefix included, a case RFC 6570 does not settle.
func uvalOf(n jnode) (uval, error) {
	if jUndefined(n) {
		return uval{}, nil
	}
	switch n.kind {
	case 's', 'p':
		return ustr(n.text), nil
	case 'a':
		v := uval{kind: ulist, list: []string{}}
		for _, it := range n.items {
			switch {
			case jUndefined(it):
				continue
			case it.kind == 'a' || it.kind == 'o':
				return uval{}, errOracleNesting
			}
			v.list = append(v.list, it.text)
		}
		v.allUndef = len(v.list) == 0
		return v, nil
	default:
		v := uval{kind: uassoc}
		for i, it := range n.items {
			switch {
			case jUndefined(it):
				v.pairs = append(v.pairs, upair{name: n.names[i], undef: true})
			case it.kind == 'a' || it.kind == 'o':
				return uval{}, errOracleNesting
			default:
				v.pairs = append(v.pairs, upairOf(n.names[i], it.text))
			}
		}
		return v, nil
	}
}

// oracleValue is v converted by jsonData and uvalOf.
func oracleValue(t testing.TB, v any) (uval, error) {
	t.Helper()
	return uvalOf(jsonData(t, v))
}

// delimited builds a spaceDelimited or pipeDelimited query pair from the OAS
// 3.1.2 style table (section 4.8.12.6: "color=blue%20black%20brown",
// "color=R%20100%20G%20200%20B%20150", and %7C for pipeDelimited) and
// Appendix C.3 (values by regular or reserved expansion, based on
// allowReserved, member names included: doc.go, Fixed rules,
// Percent-encoding), with delim the
// encoded delimiter; the parameter name always follows the name rule. It
// returns "" for an undefined value.
func delimited(name string, v uval, delim string, allow uallow) string {
	var parts []string
	switch v.kind {
	case ulist:
		for _, m := range v.list {
			parts = append(parts, uencode(m, allow))
		}
	case uassoc:
		for _, p := range v.pairs {
			if !p.undef {
				parts = append(parts, uencode(p.name, allow), uencode(p.value, allow))
			}
		}
	}
	if !v.defined() {
		return ""
	}
	return pctName(name) + "=" + strings.Join(parts, delim)
}

// deepObject builds a deepObject query from JSON data (OAS 3.1.2 section
// 4.8.12.6: "color%5BR%5D=100&color%5BG%5D=200&color%5BB%5D=150"; doc.go,
// Fixed rules, Percent-encoding: "deepObject nests objects as
// a%5Bb%5D%5Bc%5D=v"), skipping undefined members; member names and values
// take reserved expansion under allowReserved (doc.go, Fixed rules,
// Percent-encoding). It
// returns "" for an undefined value.
func deepObject(name string, n jnode, allow uallow) string {
	var pairs []string
	var walk func(prefix string, n jnode)
	walk = func(prefix string, n jnode) {
		for i, it := range n.items {
			key := prefix + "%5B" + uencode(n.names[i], allow) + "%5D"
			switch {
			case jUndefined(it):
			case it.kind == 'o':
				walk(key, it)
			default:
				pairs = append(pairs, key+"="+uencode(it.text, allow))
			}
		}
	}
	walk(pctName(name), n)
	return strings.Join(pairs, "&")
}
