/**
 * jsonImage renders one application value as the JSON text that rides the
 * wire. Every lane in this engine that turns a caller value into JSON bytes
 * goes through it: the JSON request body, a multipart part whose selected part
 * media type is JSON-family, an `application/x-www-form-urlencoded` property on
 * the CONTENT path, a content-form parameter declaring a JSON media type, and
 * the revision-2 routing transform expression.
 *
 * WHY THE CHOICE IS THIS ENGINE'S TO MAKE, AND WHOSE IT IS NOT.
 *
 * [RFC 8259] Section 7 requires exactly three things to be escaped inside a
 * JSON string — "quotation mark, reverse solidus, and the control characters
 * (U+0000 through U+001F)" — and then permits, without ever requiring, any
 * other character to be escaped as well: "Any character may be escaped." Its
 * `char` production spells both alternatives, `unescaped = %x20-21 / %x23-5B /
 * %x5D-10FFFF` for the literal form and `escape %x75 4HEXDIG` for the
 * six-character one. AMPERSAND (U+0026), LESS-THAN SIGN (U+003C) and
 * GREATER-THAN SIGN (U+003E) all fall inside `unescaped`, so `{"a":"x&y"}` and
 * `{"a":"x\u0026y"}` are two conformant JSON texts carrying one value. (Read at
 * the pinned bytes: corpus-lab authority pin
 * `authorities/texts/openapi/json/rfc8259.txt`, SHA-256
 * 61a5378f4255c720beb2a4b4a63b29540147c140f36988bf086291989b4cd2d7.)
 *
 * `openbindings.openapi@1` Section 9.2, "The JSON image of an application
 * value", governs these lanes and DELIBERATELY DOES NOT DECIDE THIS. It
 * preserves RFC 8259's permitted set, states no preference inside it, and says
 * in as many words that "[w]hich member an implementation emits is that
 * implementation's own documented behavior, not portable meaning under this
 * identifier." (OAPI-P-04 carries the same sentence in the conformance
 * summary.) So the specification has answered the question it owns — the set —
 * and left this one open on purpose. It is not a gap and it is not silence.
 *
 * Exactly one byte string still goes on the wire, so this engine must choose,
 * and THIS COMMENT IS WHERE THAT CHOICE IS DOCUMENTED. The same choice is made
 * and documented in the two Go twins (`openapi-client/go/json_image.go`,
 * `openbindings-go/formats/openapi/json_image.go`), and the three are held to
 * one emission by the shared case table `testdata/json-image-cases.json`.
 *
 * THE CHOICE IS: EMIT THE LITERAL CHARACTERS. Two grounds.
 *
 *  1. The literal characters are what the application value contains. Escaping
 *     is a representation choice with no meaning at the operation boundary, and
 *     the spelling that adds nothing is the one that states nothing extra.
 *  2. Go's `encoding/json` escapes `<`, `>` and `&` by default so that the
 *     result is safe to embed inside HTML — that is what `SetEscapeHTML`
 *     documents itself as controlling. No lane here embeds JSON into HTML: the
 *     bytes go into a request body, a multipart part, a form field or a header.
 *     The default is a host-language safety measure aimed at a context this
 *     engine is not in, and it was never a decision anyone made about this wire.
 *
 * `JSON.stringify` already emits the literal characters, so this function does
 * not change what TypeScript produces. It exists so the convention is stated AT
 * THE CALL SITE in both languages rather than resting on a host default in
 * either: the Go twins have to disable an escape, and a reader of either engine
 * should be able to see that the agreement was chosen rather than inherited.
 *
 * Null handling is deliberately NOT folded in here. `JSON.stringify(undefined)`
 * returns `undefined`, and each lane already decides for itself whether an
 * absent value is `null` or no emission at all; that decision belongs to the
 * lane, not to the encoder.
 */
export function jsonImage(value: unknown): string {
  return JSON.stringify(value) as string;
}
