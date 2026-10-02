package openapi

import (
	"strings"
)

// A parsedMedia is a media type or range (RFC 9110 section 8.3.1).
type parsedMedia struct {
	full     string // type/subtype, as written
	typ, sub string
	params   string // the parameters, as written after the first ";"
}

var octetStream = parsedMedia{"application/octet-stream", "application", "octet-stream", ""}

// parseMedia parses s, reporting whether it is a media type or range.
func parseMedia(s string) (parsedMedia, bool) {
	full, params, _ := strings.Cut(s, ";")
	full = trimOWS(full)
	typ, sub, ok := strings.Cut(full, "/")
	m := parsedMedia{full, typ, sub, params}
	return m, ok && isToken(typ) && isToken(sub) && (typ != "*" || sub == "*") && m.eachParam(nil)
}

func (m parsedMedia) concrete() bool { return m.typ != "*" && m.sub != "*" }

// eachParam calls f, if not nil, with each parameter's name and value,
// unquoted, while f returns true. It reports whether the parameters are
// well formed and f accepted each.
func (m parsedMedia) eachParam(f func(name, value string) bool) bool {
	s := m.params
	for {
		for s != "" && (s[0] == ' ' || s[0] == '\t') {
			s = s[1:]
		}
		if s == "" {
			return true
		}
		if s[0] == ';' { // an empty parameter
			s = s[1:]
			continue
		}
		i := tokenLen(s)
		if i == 0 || i == len(s) || s[i] != '=' {
			return false
		}
		name, value := s[:i], ""
		if s = s[i+1:]; s != "" && s[0] == '"' {
			var ok bool
			if value, s, ok = unquote(s); !ok {
				return false
			}
		} else if i = tokenLen(s); i > 0 {
			value, s = s[:i], s[i:]
		} else {
			return false
		}
		if f != nil && !f(name, value) {
			return false
		}
		for s != "" && (s[0] == ' ' || s[0] == '\t') {
			s = s[1:]
		}
		if s != "" && s[0] != ';' {
			return false
		}
	}
}

// trimOWS trims spaces and tabs from both ends of s.
func trimOWS(s string) string {
	for s != "" && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for s != "" && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// boundary returns the boundary parameter of a multipart type, reporting
// whether it is given, and refusing an invalid one or two.
func (m parsedMedia) boundary() (b string, given bool, err error) {
	m.eachParam(func(name, value string) bool {
		if strings.EqualFold(name, "boundary") {
			if given {
				err = errBoundary2
				return false
			}
			b, given = value, true
		}
		return true
	})
	if err == nil && given && !validBoundary(b) {
		err = errBoundary
	}
	return b, given, err
}

// param returns the value of the parameter name, compared without regard
// to case.
func (m parsedMedia) param(name string) (value string, found bool) {
	m.eachParam(func(n, v string) bool {
		if strings.EqualFold(n, name) {
			value, found = v, true
		}
		return !found
	})
	return value, found
}

// unquote reads the quoted-string at the start of s (RFC 9110 section
// 5.6.4): qdtext, and quoted-pairs of a tab, space, visible or obs-text
// byte.
func unquote(s string) (value, rest string, ok bool) {
	var b strings.Builder
	escaped := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			if !escaped {
				return s[1:i], s[i+1:], true
			}
			return b.String(), s[i+1:], true
		case c == '\\' && i+1 < len(s) && fieldByte(s[i+1]):
			if !escaped {
				b.WriteString(s[1:i])
				escaped = true
			}
			i++
			b.WriteByte(s[i])
		case c == '\\' || !fieldByte(c):
			return "", "", false
		case escaped:
			b.WriteByte(c)
		}
	}
	return "", "", false
}

// fieldByte reports whether c may appear in a field value: a tab, a space,
// a visible character or obs-text.
func fieldByte(c byte) bool { return c == '\t' || c >= ' ' && c != 0x7f }

// covers reports whether declared m matches the concrete type t, and how
// specifically: a concrete type over type/*, over */*, then more
// parameters over fewer.
func (m parsedMedia) covers(t parsedMedia) (specificity [2]int, ok bool) {
	switch {
	case m.typ == "*":
		specificity[0] = 1
	case !strings.EqualFold(m.typ, t.typ):
		return specificity, false
	case m.sub == "*":
		specificity[0] = 2
	case !strings.EqualFold(m.sub, t.sub):
		return specificity, false
	default:
		specificity[0] = 3
	}
	ok = m.eachParam(func(name, value string) bool {
		specificity[1]++
		v, found := t.param(name)
		return found && (v == value || strings.EqualFold(name, "charset") && strings.EqualFold(v, value))
	})
	return specificity, ok
}

// match returns the Media of ms, parsed as declared, that t matches most
// specifically, or nil when none does or several tie; with errs, one whose
// Err is set may be it.
func match(declared []parsedMedia, ms []*Media, t parsedMedia, errs bool) *Media {
	var best *Media
	var bestSpec [2]int
	tie := false
	for i, m := range declared {
		spec, ok := m.covers(t)
		switch {
		case !ok || !errs && ms[i].Err != nil:
		case best == nil || spec[0] > bestSpec[0] || spec[0] == bestSpec[0] && spec[1] > bestSpec[1]:
			best, bestSpec, tie = ms[i], spec, false
		case spec == bestSpec:
			tie = true
		}
	}
	if tie {
		return nil
	}
	return best
}

// contentType parses the Content-Type field values of a response, an
// absent one being application/octet-stream.
func contentType(values []string) (parsedMedia, bool) {
	switch len(values) {
	case 0:
		return octetStream, true
	case 1:
		return parseMedia(values[0])
	}
	return parsedMedia{}, false
}

// A class is how the client's codecs treat a media type.
type class int

const (
	otherClass class = iota
	sequentialClass
	jsonClass
	xmlClass
	textClass
)

func (m parsedMedia) class() class {
	if m.full == "application/json" {
		return jsonClass
	}
	app, text := strings.EqualFold(m.typ, "application"), strings.EqualFold(m.typ, "text")
	is := func(sub string) bool { return strings.EqualFold(m.sub, sub) }
	switch {
	case app && (is("jsonl") || is("x-ndjson") || is("json-seq")) || text && is("event-stream") || hasSuffixFold(m.sub, "+json-seq"):
		return sequentialClass
	case app && is("json") || hasSuffixFold(m.sub, "+json"):
		return jsonClass
	case (app || text) && is("xml") || hasSuffixFold(m.sub, "+xml"):
		return xmlClass
	case text:
		return textClass
	}
	return otherClass
}

func hasSuffixFold(s, suffix string) bool {
	return len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix)
}

// codec returns the caller's codec for m, and the key it is found by.
func (cfg *config) codec(m parsedMedia) (Codec, string) {
	if len(cfg.codecs) == 0 {
		return nil, ""
	}
	key := strings.ToLower(m.full)
	if c, ok := cfg.codecs[key]; ok {
		return c, key
	}
	if i := strings.LastIndexByte(key, '+'); i >= 0 {
		if c, ok := cfg.codecs[key[i:]]; ok {
			return c, key[i:]
		}
	}
	return nil, ""
}

// isToken reports whether s is an RFC 9110 token.
func isToken(s string) bool { return s != "" && tokenLen(s) == len(s) }

func tokenLen(s string) int {
	for i := 0; i < len(s); i++ {
		if c := s[i]; !unreserved(c) && strings.IndexByte("!#$%&'*+^`|", c) < 0 {
			return i
		}
	}
	return len(s)
}

// unreserved reports whether c is in RFC 3986's unreserved set.
func unreserved(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
}

const (
	upperHex = "0123456789ABCDEF"
	lowerHex = "0123456789abcdef"
)

func hexDigit(c byte) bool { return strings.IndexByte(upperHex+lowerHex, c) >= 0 }

func writeEscaped(b *strings.Builder, c byte) {
	b.WriteByte('%')
	b.WriteByte(upperHex[c>>4])
	b.WriteByte(upperHex[c&15])
}

// A charset says how percent-encoding treats each byte: 0 encodes it, 1
// keeps it, and 2, for "%", keeps it where it begins a %XX triple.
type charset [256]uint8

func newCharset(kept string, triples bool) *charset {
	var s charset
	for c := range 256 {
		if unreserved(byte(c)) || strings.IndexByte(kept, byte(c)) >= 0 {
			s[c] = 1
		}
	}
	if triples {
		s['%'] = 2
	}
	return &s
}

var (
	unreservedSet = newCharset("", false)
	reservedSet   = newCharset(":/?#[]@!$&'()*+,;=", true) // RFC 6570 reserved expansion
	pathSet       = newCharset("!$&'()*+,;=:@/", true)     // path text: pchar and "/"
	fragmentSet   = newCharset("!$&'()*+,;=:@/?", false)   // RFC 3986 section 3.5
)

// escapeTo writes s to b, percent-encoding each byte set does not keep as
// %XX in uppercase hex; a nil set keeps every byte.
func escapeTo(b *strings.Builder, s string, set *charset) {
	if set == nil {
		b.WriteString(s)
		return
	}
	start := 0
	for i := 0; i < len(s); i++ {
		if k := set[s[i]]; k == 1 || k == 2 && i+2 < len(s) && hexDigit(s[i+1]) && hexDigit(s[i+2]) {
			continue
		}
		b.WriteString(s[start:i])
		writeEscaped(b, s[i])
		start = i + 1
	}
	b.WriteString(s[start:])
}

// escape returns s percent-encoded with set: s itself when set keeps every
// byte.
func escape(s string, set *charset) string {
	i := 0
	for i < len(s) && set[s[i]] == 1 {
		i++
	}
	if i == len(s) {
		return s
	}
	var b strings.Builder
	b.WriteString(s[:i])
	escapeTo(&b, s[i:], set)
	return b.String()
}
