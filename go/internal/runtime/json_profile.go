package openapiclient

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// The strict-JSON profile every OpenAPI edition of this binding parses
// responses under. RFC 8259 documents latitude in three places that receivers
// resolve differently, and the four sibling binding specifications pin the same
// resolution for all of them (openbindings.openapi-2.0@1 §9.2,
// openbindings.openapi-3.0@1 §9.2, openbindings.openapi-3.1@1 §9.2,
// openbindings.openapi-3.2@1 §9.2):
//
//   - Duplicate object member names resolve to the lexically last member.
//     encoding/json already does exactly that, so nothing here implements it.
//
//   - A leading byte-order mark is IGNORED and is never part of the value.
//     RFC 8259 §8.1 forbids emitting one and explicitly permits a parser to
//     ignore one it receives; encoding/json does not, so the mark is dropped
//     before the parse rather than reported as `invalid character 'ï'`.
//
//   - A lone surrogate escape yields NO value: it is a loud protocol error.
//     encoding/json silently substitutes U+FFFD, which alters the supplied
//     text without saying so; passing invalid Unicode through instead is no
//     more faithful. Neither preserves what the peer sent, so the interaction
//     completes unsuccessfully.
//
// The surrogate check runs only when the body actually contains a `\u` escape,
// so an ordinary body pays one bytes.Contains over bytes already in memory.

// utf8BOM is U+FEFF encoded as UTF-8, the only encoding this lane reads
// (RFC 8259 §8.1 requires UTF-8 for JSON text exchanged between systems).
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// parseStrictJSON parses one JSON body under the pinned profile. The returned
// error names the profile rule it failed, so callers can wrap it with their own
// media-type context.
func parseStrictJSON(body []byte, value any) error {
	if err := checkLoneSurrogateEscape(body); err != nil {
		return err
	}
	return json.Unmarshal(bytes.TrimPrefix(body, utf8BOM), value)
}

// checkLoneSurrogateEscape reports a `\uD800`–`\uDFFF` escape that is not half
// of a well-formed surrogate pair. It walks string literals only: a backslash
// outside a string is not an escape, and `\\u` is a literal backslash followed
// by the letter u.
func checkLoneSurrogateEscape(body []byte) error {
	if !bytes.Contains(body, []byte(`\u`)) {
		return nil
	}
	inString := false
	for index := 0; index < len(body); index++ {
		switch body[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			if index+1 >= len(body) {
				return nil
			}
			if body[index+1] != 'u' {
				// Any other escape consumes exactly its one marker byte.
				index++
				continue
			}
			code, ok := hexEscapeValue(body, index)
			if !ok {
				return nil
			}
			if code < 0xD800 || code > 0xDFFF {
				index += 5
				continue
			}
			if code >= 0xDC00 {
				return loneSurrogateError(code)
			}
			low, paired := hexEscapeValue(body, index+6)
			if !paired || body[index+6] != '\\' || low < 0xDC00 || low > 0xDFFF {
				return loneSurrogateError(code)
			}
			index += 11
		}
	}
	return nil
}

// hexEscapeValue reads the `\uXXXX` escape beginning at start, if one is there.
func hexEscapeValue(body []byte, start int) (rune, bool) {
	if start < 0 || start+6 > len(body) || body[start] != '\\' || body[start+1] != 'u' {
		return 0, false
	}
	value := rune(0)
	for _, digit := range body[start+2 : start+6] {
		switch {
		case digit >= '0' && digit <= '9':
			value = value<<4 | rune(digit-'0')
		case digit >= 'a' && digit <= 'f':
			value = value<<4 | rune(digit-'a'+10)
		case digit >= 'A' && digit <= 'F':
			value = value<<4 | rune(digit-'A'+10)
		default:
			return 0, false
		}
	}
	return value, true
}

func loneSurrogateError(code rune) error {
	return fmt.Errorf("JSON body contains the lone surrogate escape \\u%04X, which denotes no character", code)
}
