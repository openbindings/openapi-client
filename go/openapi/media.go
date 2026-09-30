package openapi

import (
	"strings"
)

// A media is a parsed media type or range (RFC 9110 section 8.3.1).
type media struct {
	full     string // type/subtype, as written
	typ, sub string
	params   string // the parameters, as written after the first ";"
}

var octetStream = media{"application/octet-stream", "application", "octet-stream", ""}

// parseMedia parses s, reporting whether it is a media type or range.
func parseMedia(s string) (media, bool) {
	full, params, _ := strings.Cut(s, ";")
	full = strings.Trim(full, " \t")
	typ, sub, ok := strings.Cut(full, "/")
	m := media{full, typ, sub, params}
	return m, ok && isToken(typ) && isToken(sub) && (typ != "*" || sub == "*") && m.eachParam(nil)
}

func parseMedias(ms []*Media) []media {
	parsed := make([]media, len(ms))
	for i, m := range ms {
		parsed[i], _ = parseMedia(m.Type)
	}
	return parsed
}

func (m media) concrete() bool { return m.typ != "*" && m.sub != "*" }

// eachParam calls f, if not nil, with each parameter's name and value,
// unquoted, while f returns true. It reports whether the parameters are
// well formed and f accepted each.
func (m media) eachParam(f func(name, value string) bool) bool {
	s := m.params
	for {
		s = strings.TrimLeft(s, " \t")
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
		s = s[i+1:]
		if s != "" && s[0] == '"' {
			var ok bool
			if value, s, ok = unquote(s); !ok {
				return false
			}
		} else {
			i = tokenLen(s)
			if i == 0 {
				return false
			}
			value, s = s[:i], s[i:]
		}
		if f != nil && !f(name, value) {
			return false
		}
		s = strings.TrimLeft(s, " \t")
		if s != "" && s[0] != ';' {
			return false
		}
	}
}

// param returns the value of the parameter name, compared without regard
// to case.
func (m media) param(name string) (value string, found bool) {
	m.eachParam(func(n, v string) bool {
		if strings.EqualFold(n, name) {
			value, found = v, true
		}
		return !found
	})
	return value, found
}

// unquote reads the quoted-string at the start of s.
func unquote(s string) (value, rest string, ok bool) {
	var b strings.Builder
	escaped := false
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			if !escaped {
				b.WriteString(s[1:i])
				escaped = true
			}
			i++
			b.WriteByte(s[i])
		case c == '"':
			if !escaped {
				return s[1:i], s[i+1:], true
			}
			return b.String(), s[i+1:], true
		case escaped:
			b.WriteByte(c)
		}
	}
	return "", "", false
}

// covers reports whether declared m matches the concrete type t, and how
// specifically: a concrete type over type/*, over */*, then more
// parameters over fewer.
func (m media) covers(t media) (int, bool) {
	var score int
	switch {
	case m.typ == "*":
		score = 1000
	case !strings.EqualFold(m.typ, t.typ):
		return 0, false
	case m.sub == "*":
		score = 2000
	case !strings.EqualFold(m.sub, t.sub):
		return 0, false
	default:
		score = 3000
	}
	ok := m.eachParam(func(name, value string) bool {
		score++
		v, found := t.param(name)
		return found && (v == value || strings.EqualFold(name, "charset") && strings.EqualFold(v, value))
	})
	return score, ok
}

// match returns the Media of ms, parsed as declared, that t matches, or nil
// when none does or several tie.
func match(declared []media, ms []*Media, t media) *Media {
	best, bestScore, tie := -1, 0, false
	for i, m := range declared {
		if ms[i].Err != nil {
			continue
		}
		score, ok := m.covers(t)
		switch {
		case !ok:
		case score > bestScore:
			best, bestScore, tie = i, score, false
		case score == bestScore:
			tie = true
		}
	}
	if best < 0 || tie {
		return nil
	}
	return ms[best]
}

// contentType parses the Content-Type field values of a response, an
// absent one being application/octet-stream.
func contentType(values []string) (media, bool) {
	switch len(values) {
	case 0:
		return octetStream, true
	case 1:
		return parseMedia(values[0])
	}
	return media{}, false
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

func (m media) class() class {
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
func (cfg *config) codec(m media) (Codec, string) {
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
		c := s[i]
		if !(unreserved(c) || strings.IndexByte("!#$%&'*+^`|", c) >= 0) {
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

// escape percent-encodes every byte of s outside the unreserved set.
func escape(s string) string {
	var b strings.Builder
	escapeTo(&b, s)
	return b.String()
}

func escapeTo(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		if c := s[i]; unreserved(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&15])
		}
	}
}
