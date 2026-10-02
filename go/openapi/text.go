package openapi

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// readTree reads content, the document retrieved from uri, which base is
// parsed, if not nil, in the encoding its byte order mark names or YAML
// 1.2.2 section 5.2 deduces: as JSON when its first significant character is
// '{' and it is JSON, and otherwise as YAML.
func readTree(ctx context.Context, content, uri string, base *url.URL) (*tree, error) {
	var err error
	if base == nil {
		base, err = url.Parse(uri)
	}
	switch {
	case err != nil: // not shown, as its userinfo cannot be found
		err = errors.New("the document URI cannot be parsed (RFC 3986)")
	case !base.IsAbs():
		err = errors.New("the document URI is not absolute")
	default:
		err = checkURI(base, uri)
	}
	if err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	src, unit, bad := decode(content)
	if bad >= 0 {
		return nil, rejection(uri, src, bad, unit, "invalid "+[...]string{1: "UTF-8", 2: "UTF-16", 4: "UTF-32"}[unit])
	}
	var t *tree
	if s := strings.TrimLeft(src, " \t\r\n"); s != "" && s[0] == '{' {
		t, err = parseTree(ctx, src, uri, unit)
	}
	if t == nil && ctx.Err() == nil {
		t, err = parseYAML(ctx, src, uri, unit, len(content))
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("openapi: %s: %w", uri, ctx.Err())
	}
	if t == nil {
		return nil, err
	}
	t.uri, t.base = uri, base
	if t.reaches && base.Opaque == "" && base.Path != "" && base.RawQuery == "" && !base.ForceQuery {
		t.dir = uri[:strings.LastIndexByte(uri, '/')+1]
	}
	return t, nil
}

// decode returns content as UTF-8 text without a byte order mark, the bytes
// one of its code units took in content (1, 2 for UTF-16 or 4 for UTF-32),
// and the offset in the text at which content stops being valid, or -1. A
// byte order mark names the encoding; without one, the null bytes of the
// first character tell UTF-16 and UTF-32 from UTF-8, as YAML 1.2.2 section
// 5.2 says.
func decode(content string) (text string, unit, bad int) {
	s, big := content, false
	unit = 1
	switch {
	case strings.HasPrefix(s, "\x00\x00\xFE\xFF"):
		s, unit, big = s[4:], 4, true
	case strings.HasPrefix(s, "\xFF\xFE\x00\x00"):
		s, unit = s[4:], 4
	case strings.HasPrefix(s, "\xFE\xFF"):
		s, unit, big = s[2:], 2, true
	case strings.HasPrefix(s, "\xFF\xFE"):
		s, unit = s[2:], 2
	case strings.HasPrefix(s, "\xEF\xBB\xBF"):
		s = s[3:]
	case len(s) >= 4 && s[0] == 0 && s[1] == 0 && s[2] == 0:
		unit, big = 4, true
	case len(s) >= 4 && s[1] == 0 && s[2] == 0 && s[3] == 0:
		unit = 4
	case len(s) >= 2 && s[0] == 0:
		unit, big = 2, true
	case len(s) >= 2 && s[1] == 0:
		unit = 2
	}
	if unit == 1 {
		if utf8.ValidString(s) {
			return s, 1, -1
		}
		i := 0
		for r, n := utf8.DecodeRuneInString(s); r != utf8.RuneError || n != 1; r, n = utf8.DecodeRuneInString(s[i:]) {
			i += n
		}
		return s, 1, i
	}
	var b strings.Builder
	b.Grow(len(s) / unit * 3 / 2)
	read := func(i int) rune { // the code unit at i
		var r uint32
		for k := range unit {
			if big {
				r = r<<8 | uint32(s[i+k])
			} else {
				r |= uint32(s[i+k]) << (8 * k)
			}
		}
		return rune(r)
	}
	for i := 0; i < len(s); i += unit {
		if i+unit > len(s) {
			return b.String(), unit, b.Len()
		}
		r := read(i)
		if unit == 2 && utf16.IsSurrogate(r) { // a pair, high then low
			if i+2*unit > len(s) {
				return b.String(), unit, b.Len()
			}
			r, i = utf16.DecodeRune(r, read(i+unit)), i+unit
			if r == utf8.RuneError {
				return b.String(), unit, b.Len()
			}
		} else if !utf8.ValidRune(r) {
			return b.String(), unit, b.Len()
		}
		b.WriteRune(r)
	}
	return b.String(), unit, -1
}

// rejection reports a document's defect at byte offset i of src, its text
// in UTF-8, by line and column, both counted from 1, the column in the bytes
// the document took as retrieved: unit bytes per UTF-16 code unit or UTF-32
// character, and each byte of UTF-8.
func rejection(uri, src string, i, unit int, msg string) error {
	row, start := 1, 0
	for j := 0; j < i; j++ {
		if src[j] == '\r' || src[j] == '\n' {
			if src[j] == '\r' && j+1 < i && src[j+1] == '\n' {
				j++
			}
			row, start = row+1, j+1
		}
	}
	line := src[start:i]
	col := len(line)
	if unit > 1 {
		col = 0
		for _, r := range line {
			col += unit
			if unit == 2 && r > 0xFFFF {
				col += unit
			}
		}
	}
	return fmt.Errorf("openapi: %s:%d:%d: %s", uri, row, col+1, msg)
}
