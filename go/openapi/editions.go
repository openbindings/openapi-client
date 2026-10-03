package openapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"io"
	"net/textproto"
	"slices"
	"strings"
)

// swaggerSchema keeps the Schema subset of a Parameter or Items Object.
func swaggerSchemaField(name string) bool {
	return slices.Contains([]string{"type", "format", "items", "default", "enum", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum", "maxLength", "minLength", "pattern", "maxItems", "minItems", "uniqueItems", "multipleOf"}, name)
}

func swaggerSchema(v value) json.RawMessage {
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for name, m := range v.members() {
		if !swaggerSchemaField(name) {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		canonicalString(&b, name)
		b.WriteByte(':')
		b.WriteString(m.raw())
	}
	b.WriteByte('}')
	return json.RawMessage(b.String())
}

func (d *document) swaggerParam(t value, at string, p *Param) param {
	p.Style, p.AllowReserved, p.Explode, p.ExplodeSet = "", false, false, false
	for name := range t.members() {
		if swaggerSchemaField(name) {
			p.Schema = &Schema{doc: d, v: t, src: at, legacy: true}
			break
		}
	}
	st := styles["simple"]
	if p.In == "query" || p.In == "formData" {
		st = styles["form"]
	}
	pp := param{Param: p, legacy: t, style: st, set: unreservedSet, name: escape(p.Name, unreservedSet), required: p.Required || p.In == "path"}
	if p.In == "header" {
		pp.field, pp.set = textproto.CanonicalMIMEHeaderKey(p.Name), nil
	}
	if t.str("type") == "array" {
		p.CollectionFormat = t.str("collectionFormat")
		if p.CollectionFormat == "" {
			p.CollectionFormat = "csv"
		}
		for v, depth := t, 0; v.str("type") == "array"; v, depth = v.get("items"), depth+1 {
			cf := v.str("collectionFormat")
			if !slices.Contains([]string{"", "csv", "ssv", "tsv", "pipes", "multi"}, cf) || cf == "multi" && (depth > 0 || p.In != "query" && p.In != "formData") {
				p.Err = fmt.Errorf("unsupported collectionFormat %q for %s", cf, p.In)
			}
		}
	}
	if !slices.Contains([]string{"path", "query", "header", "body", "formData"}, p.In) {
		p.Err = fmt.Errorf("unsupported Swagger parameter location %q", p.In)
	}
	if p.In == "header" && (!isToken(p.Name) || slices.Contains(derivedFields, pp.field) || pp.field == "Cookie") {
		p.Err = errors.New("unsupported header parameter field")
	}
	pp.idHash = paramHash(p.In, pp.identity().name)
	// Keep the same name hash that assignKeys uses for all parameters.
	pp.nameHash = uint32(maphash.String(paramSeed, p.Name))
	loc, _, dot := strings.Cut(p.Name, ".")
	pp.dotted = p.Name == "" || strings.HasPrefix(p.Name, "/") || strings.HasPrefix(p.Name, "Input.Body") || dot && slices.Contains([]string{"path", "query", "header", "cookie", "querystring"}, loc)
	return pp
}

// legacyValues serializes nested arrays from the inside out; undefined
// members contribute nothing. Body fields have no request-target bound.
func legacyValues(r *jsonReader, schema value) ([]string, error) {
	switch r.s[r.i] {
	case 'n':
		r.skip()
		return nil, nil
	case '{':
		if !r.skip() {
			return nil, nil
		}
		return nil, errors.New("Swagger parameters cannot serialize objects")
	case '[':
		if r.s[r.i+1] == ']' {
			r.i += 2
			return nil, nil
		}
		var items []string
		err := r.each(func(string) error {
			xs, err := legacyValues(r, schema.get("items"))
			if err == nil {
				items = append(items, xs...)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		cf := schema.str("collectionFormat")
		if cf == "multi" {
			return items, nil
		}
		delim := map[string]string{"": ",", "csv": ",", "ssv": " ", "tsv": "\t", "pipes": "|"}[cf]
		if delim == "" {
			return nil, fmt.Errorf("unsupported collectionFormat %q", cf)
		}
		return []string{strings.Join(items, delim)}, nil
	default:
		return []string{r.scalar()}, nil
	}
}

func (c *Client) writeLegacy(b *strings.Builder, lead string, p *param, v any, form bool) (bool, error) {
	// Exact []string has no custom marshaling or undefined members. jsonText
	// applies encoding/json's invalid-UTF-8 rule without an intermediate JSON
	// document, as the ordinary parameter emitter does.
	if values, ok := v.([]string); ok && !form && p.CollectionFormat != "" {
		if len(values) == 0 {
			return false, nil
		}
		if p.Err != nil {
			return false, p.Err
		}
		delim := map[string]string{"csv": ",", "ssv": " ", "tsv": "\t", "pipes": "|"}[p.CollectionFormat]
		if p.set != nil && delim != "," {
			delim = escape(delim, p.set)
		}
		for i, v := range values {
			sep := lead
			if i > 0 {
				sep = delim
				if p.CollectionFormat == "multi" {
					sep = "&"
				}
			}
			named := p.In == "query" && (i == 0 || p.CollectionFormat == "multi")
			room := maxLength - b.Len() - len(sep)
			if named {
				room -= len(p.name) + 1
			}
			if len(v) > room {
				return false, errTooLong
			}
			s := jsonText(v)
			if len(s) > room || p.set != nil && len(s) > room/3 && escapedSize(s, p.set, false) > room {
				return false, errTooLong
			}
			b.WriteString(sep)
			if named {
				b.WriteString(p.name)
				b.WriteByte('=')
			}
			escapeTo(b, s, p.set)
		}
		return true, nil
	}
	if !form {
		w := collectionWriter{c: c, b: b, p: p, lead: lead}
		given, err := w.value(v, p.legacy, 1, nil)
		if given && err == nil {
			err = p.Err
		}
		return given, err
	}
	s, _, err := encodeJSON(c.doc, v, marshal)
	if err != nil {
		return false, err
	}
	vals, err := legacyValues(&jsonReader{s: s}, p.legacy)
	given := len(vals) > 0 || s[0] == '[' && s[1] != ']'
	if err != nil || !given {
		return false, err
	}
	if p.Err != nil {
		return false, p.Err
	}
	for i, v := range vals {
		if i == 0 {
			b.WriteString(lead)
		} else {
			b.WriteByte('&')
		}
		b.Write(appendForm(nil, p.Name))
		if !(s == `""` && v == "" && c.cfg.NameOnlyEmpty && p.AllowEmptyValue) {
			b.WriteByte('=')
		}
		b.Write(appendForm(nil, v))
	}
	return true, nil
}

// swaggerBody removes body/formData declarations from the parameter plan
// and normalizes their media without rewriting the authored document.
func (o *operation) swaggerBody(n value) error {
	var fields []param
	var body *param
	kept := o.params[:0]
	for i := range o.params {
		p := o.params[i]
		switch p.In {
		case "body":
			if body != nil {
				return errors.New("several Swagger body parameters")
			}
			body = &p
		case "formData":
			fields = append(fields, p)
		default:
			kept = append(kept, p)
		}
	}
	o.params = kept
	if body == nil && len(fields) == 0 {
		return nil
	}
	if body != nil && len(fields) > 0 {
		return errors.New("Swagger body and formData parameters cannot coexist")
	}
	types := n.get("consumes")
	if !types.ok() {
		types = o.doc.root().get("consumes")
	}
	var schema *Schema
	var src, desc string
	required := false
	var encoding *formEncoding
	var params []*Param
	list := types.strs()
	if body != nil {
		schema = o.doc.schema(body.legacy.get("schema"), body.Source, "/schema")
		src, desc, required = body.Source, body.Description, body.Required
	} else {
		encoding = &formEncoding{byName: map[string]*field{}, swagger: true}
		for _, p := range fields {
			f := &field{param: p, roots: []value{p.legacy}, types: textField.types, parsed: textField.parsed, class: textClass}
			f.ContentType = "text/plain"
			if p.legacy.str("type") == "file" {
				f.types, f.parsed, f.class = []string{octetStream.full}, []parsedMedia{octetStream}, otherClass
				f.ContentType = octetStream.full
			}
			encoding.byName[p.Name] = f
			params = append(params, f.Param)
			if p.Required {
				encoding.required = true
				required = true
			}
		}
		schema = &Schema{doc: o.doc, v: n, synthetic: params}
		list = slices.DeleteFunc(list, func(typ string) bool {
			m, ok := parseMedia(typ)
			return !ok || !(isForm(m) || strings.EqualFold(m.full, "multipart/form-data"))
		})
	}
	if len(list) == 0 {
		list = []string{""}
	}
	c := o.doc.swaggerContent(schema, list, src)
	if encoding != nil {
		for i, md := range c.media {
			md.Encoding = params
			c.encodings[i] = encoding
		}
	}
	o.Body = &Message{Required: required, Source: src, Description: desc, Media: c.media}
	o.body, o.encodings = c.parsed, c.encodings
	return nil
}

func (d *document) swaggerContent(schema *Schema, types []string, src string) *content {
	c := &content{source: src}
	if len(types) == 0 {
		types = []string{""}
	}
	for _, typ := range types {
		md := &Media{Type: typ, Schema: schema, Source: src}
		pm, ok := parseMedia(typ)
		if typ == "" {
			pm, _ = parseMedia("*/*")
			ok = true
		}
		if !ok {
			md.Err = fmt.Errorf("invalid media type %q", typ)
		}
		md.Sequential = pm.class() == sequentialClass || isMultipart(pm)
		var enc *formEncoding
		if schema != nil && (isForm(pm) || isMultipart(pm) || pm.typ == "*" || pm.sub == "*" && strings.EqualFold(pm.typ, "application")) {
			enc, md.Encoding = d.encodingOf([]value{schema.v}, schema.Source(), value{}, "", pm, schema.v.t.edition)
		} else {
			enc = noFields
		}
		if !isForm(pm) && !isMultipart(pm) {
			md.Encoding = nil
		}
		c.media, c.parsed, c.encodings = append(c.media, md), append(c.parsed, pm), append(c.encodings, enc)
		if ok && pm.concrete() {
			c.success = append(c.success, pm)
		}
	}
	return c
}

func (d *document) swaggerResponse(v value, src string, produces value) (*Message, *content) {
	t, at, desc, err := d.follow(v, src)
	if err != nil {
		return &Message{Source: src, Err: err}, &noContent
	}
	c := &content{source: at}
	if schema := d.schema(t.get("schema"), at, "/schema"); schema != nil {
		c = d.swaggerContent(schema, produces.strs(), at)
	}
	c.headers = d.headers(t.get("headers"), at+"/headers")
	return &Message{Source: at, Description: desc, Headers: c.headers, Media: c.media}, c
}

// membersChecked adds Swagger required-field checks without making a second
// pass through caller values or changing the general object encoder.
func (c *Client) membersChecked(v any, enc *formEncoding, body string, re *RequestError, b *builder, f func(string, *field, any) bool) error {
	if !enc.required {
		return c.doc.members(v, enc, func(name string, fd *field, v any) { f(name, fd, v) })
	}
	seen := map[string]bool{}
	err := c.doc.members(v, enc, func(name string, fd *field, v any) {
		buf, parts := len(b.buf), len(b.parts)
		given := f(name, fd, v)
		seen[name] = given || len(b.buf) != buf || len(b.parts) != parts
	})
	for name, fd := range enc.byName {
		if fd.Required && !seen[name] {
			re.input((key{body, name, -1}).String(), errMissing)
		}
	}
	return err
}

// positionalEncoding compiles all positional schema/Encoding combinations
// once; looking up an item does no document traversal.
func (d *document) positionalEncoding(enc *formEncoding, v value, roots []value, src string, m parsedMedia, named ...*Param) []*Param {
	enc.ordered = true
	var schemas []value
	var sources []string
	rest, restSource := v.get("itemSchema"), src+"/itemSchema"
	d.closure(roots, src+"/schema", func(s value, at string) {
		if schemas == nil {
			for _, p := range s.get("prefixItems").members() {
				sources = append(sources, at+"/prefixItems/"+fmt.Sprint(len(schemas)))
				schemas = append(schemas, p)
			}
		}
		if !rest.ok() {
			rest, restSource = s.get("items"), at+"/items"
		}
	})
	var prefix []value
	for _, e := range v.get("prefixEncoding").members() {
		prefix = append(prefix, e)
	}
	if len(prefix) == 0 && !v.get("itemEncoding").ok() {
		named = slices.Clone(named)
	} else {
		named = nil
	}
	for i := range max(len(prefix), len(schemas)) {
		var schema, e value
		schemaSource := restSource
		if i < len(schemas) {
			schema = schemas[i]
			schemaSource = sources[i]
		} else {
			schema = rest
		}
		if i < len(prefix) {
			e = prefix[i]
		} else {
			e = v.get("itemEncoding")
		}
		name := fmt.Sprint(i)
		f, p := d.newField(name, d.schema(schema, schemaSource, ""), schemaRoots(schema), e, src+"/prefixEncoding/"+name, m, true)
		enc.positional = append(enc.positional, f)
		if i < len(prefix) {
			named = append(named, p)
		}
	}
	f, p := d.newField("*", d.schema(rest, restSource, ""), schemaRoots(rest), v.get("itemEncoding"), src+"/itemEncoding", m, true)
	enc.rest = f
	if v.get("itemEncoding").ok() {
		named = append(named, p)
	}
	return named
}

func (w *partWriter) position(enc *formEncoding, v any, body string, i int) {
	at := key{body, fmt.Sprint(i), -1}
	if null(v) {
		return
	}
	f := enc.rest
	if i < len(enc.positional) {
		f = enc.positional[i]
	}
	if !w.styles {
		w.write(f, "", v, at)
		return
	}
	// Form-data arrays retain explicit names, rather than assigning index names.
	switch p := v.(type) {
	case Part:
		if len(p.Header.Values("Content-Disposition")) > 0 {
			w.write(f, "", p, at)
			return
		}
	case *Part:
		if p != nil && len(p.Header.Values("Content-Disposition")) > 0 {
			w.write(f, "", p, at)
			return
		}
	}
	count := 0
	var name string
	var content any
	err := w.c.doc.members(v, noFields, func(n string, _ *field, x any) { count++; name, content = n, x })
	if err != nil || count != 1 {
		w.re.input(at.String(), errors.New("a form-data array item requires a one-property object or Part with Content-Disposition"))
		return
	}
	if !null(content) {
		if f.styled {
			w.styled(f, name, content, at)
		} else {
			w.write(f, name, content, at)
		}
	}
}

func schemaRoots(v value) []value {
	if v.ok() {
		return []value{v}
	}
	return nil
}

func rawField(v any) bool {
	switch v.(type) {
	case []byte, io.Reader, Part, *Part:
		return true
	}
	return false
}
func emptyString(v any) bool { s, ok := v.(string); return ok && s == "" }
