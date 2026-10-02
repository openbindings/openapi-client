package openapi

import (
	"cmp"
	"errors"
	"fmt"
	"hash/maphash"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
)

// An operation is a compiled operation: its descriptor, and the plan its
// calls follow, so that calls do no document work.
type operation struct {
	Operation
	doc *document
	plan
}

type plan struct {
	params     []param
	dests      map[paramID]int // the parameters at a destination a credential sets, or nil
	pathParams []int           // the path parameters, indexes into params
	path       []pathPart
	servers    []*server
	security   []alternative   // Operation.Security, compiled
	body       []parsedMedia   // the request body's Media, parsed
	encodings  []*formEncoding // and the fields of each, under a form or multipart type it covers
	responses  []responsePlan
	success    [][]parsedMedia // each 2xx response's concrete media types
}

// A param is a parameter with its serialization compiled.
type param struct {
	legacy   value
	cookie32 bool
	*Param
	*style
	set      *charset // how its values are percent-encoded, or nil to write them as given
	required bool
	dotted   bool          // whether its Key is its location and name joined by a dot
	idHash   uint32        // a hash of its identity, by location and name (see find)
	nameHash uint32        // and of its name alone
	field    string        // a header parameter's canonical field name
	name     string        // the name, percent-encoded
	media    *parsedMedia  // a content parameter's ContentType, parsed
	form     *formEncoding // the fields of a form-urlencoded content parameter
}

// A pathPart is literal text of the path template, percent-encoded, or one
// of its parameters.
type pathPart struct {
	text  string
	param int // an index into plan.params, or -1 for text
}

// A responsePlan is a declared response with its parsed media types.
type responsePlan struct {
	*Message
	status   int // an exact status code, or 0
	class    int // the class of a range such as "4XX", or 0
	fallback bool
	media    []parsedMedia
}

// build compiles the entry: what its Operation Object compiles to on any
// path, shared by the entries of its group, then what its own path decides.
func (e *entry) build() *operation {
	d := e.doc
	if e.m < 0 {
		o := &operation{doc: d}
		o.Path, o.Source, o.Err = e.path, e.levels.v.t.source(e.levels.ptr), e.err
		return o
	}
	var o *operation
	if g := e.group; g != nil {
		c := *loadOrMake(&g.op, e.shape)
		o = &c
	} else {
		o = e.shape()
	}
	o.Key, o.Path = e.id, e.path
	if e.forbidden {
		o.Key = ""
		return o
	}
	if e.id == "" || d.byID[e.id] != e {
		o.Key = o.Method + " " + o.Path
	}
	if err := o.parsePath(); err != nil {
		o.Err = errors.Join(o.Err, err)
	}
	return o
}

// shape compiles what the entry's Operation Object compiles to on every
// path its Path Item chain reaches it from: all but the Key and what the
// path template decides.
func (e *entry) shape() *operation {
	d, n, src := e.doc, e.node, e.source()
	o := &operation{doc: d}
	op := &o.Operation
	op.ID, op.Method, op.Source = e.id, e.method(), src
	var params, body, responses, servers, security value
	for name, m := range n.members() { // one pass: a member's name is read from the source
		switch name {
		case "summary":
			op.Summary = m.string()
		case "description":
			op.Description = m.string()
		case "tags":
			op.Tags = m.strs()
		case "deprecated":
			op.Deprecated = m.kind() == 't'
		case "parameters":
			params = m
		case "requestBody":
			body = m
		case "responses":
			responses = m
		case "servers":
			servers = m
		case "security":
			security = m
		}
	}
	errs := []error{e.err}

	ids := map[uint32]int{}
	list, at, err := e.field(parametersField)
	errs = append(errs, err)
	errs = o.addParams(list, at, ids, errs)
	errs = o.addParams(params, src+"/parameters", ids, errs)
	if n.t.edition == 20 {
		errs = append(errs, o.swaggerBody(n))
	}
	o.assignKeys()
	queries, wholes := 0, 0
	for _, p := range o.params {
		if p.In == "query" {
			queries++
		}
		if p.In == "querystring" {
			wholes++
		}
	}
	if wholes > 1 || wholes > 0 && queries > 0 {
		errs = append(errs, errors.New("querystring cannot coexist with another query or querystring parameter"))
	}

	if body.ok() && op.Method != "TRACE" && op.Method != "CONNECT" && !(n.t.edition == 30 && slices.Contains([]string{"GET", "HEAD", "DELETE", "OPTIONS"}, op.Method)) {
		var target value
		var c *content
		op.Body, target, c = d.message(body, src+"/requestBody", true)
		if !target.ok() {
			errs = append(errs, op.Body.Err)
		}
		op.Body.Required = target.flag("required")
		o.body, o.encodings = c.parsed, c.encodings
	}
	if responses.kind() == '{' {
		for key, r := range responses.members() {
			if !strings.HasPrefix(key, "x-") {
				m, _, c := d.message(r, src+"/responses/"+token(key), false)
				if n.t.edition == 20 {
					produces := n.get("produces")
					if !produces.ok() {
						produces = d.root().get("produces")
					}
					m, c = d.swaggerResponse(r, src+"/responses/"+token(key), produces)
				}
				m.Key = key
				o.addResponse(m, c)
			}
		}
	}

	var sl *serverList
	switch s, at, err := e.field(serversField); {
	case n.t.edition == 20:
		sl = d.swaggerServers(n.get("schemes"))
	case servers.hasMembers():
		sl = d.parseServers(servers, src+"/servers")
	case s.ok():
		errs = append(errs, err)
		sl = d.serverLists.get(s.id(), func() *serverList { return d.parseServers(s, at) })
	default:
		sl = d.inherited().servers
	}
	o.servers, op.Servers = sl.servers, sl.desc

	sec := d.inherited().security
	if security.ok() {
		sec = d.compileSecurity(security)
	}
	op.Security, o.security, errs = sec.reqs, sec.alts, append(errs, sec.err)
	for dest := range sec.dests { // keep the parameters where a credential goes
		if k, ok := find(ids, paramHash(dest.in, dest.name), func(j int) bool { return o.params[j].identity() == dest }); ok {
			if o.dests == nil {
				o.dests = map[paramID]int{}
			}
			o.dests[dest] = ids[k]
		}
	}
	op.Err = errors.Join(errs...)
	return o
}

// field returns the Path Item field f, parametersField or serversField,
// from the level of the chain that has it, with its Source, and an error
// when several do.
func (e *entry) field(f int) (value, string, error) {
	name := "parameters"
	if f == serversField {
		name = "servers"
	}
	l := e.sum.at[f]
	if l == nil {
		return value{}, "", nil
	}
	var err error
	if e.sum.dup&(1<<f) != 0 {
		err = fmt.Errorf("the Path Item and its $ref target both define %s", name)
	}
	return l.v.get(name), l.v.t.source(l.ptr + "/" + name), err
}

// A paramID identifies a parameter by location and name, a header's name
// compared without regard to case (see param.identity).
type paramID struct{ in, name string }

// addParams adds the parameters of list, each taking the place of an
// earlier one it identifies.
func (o *operation) addParams(list value, src string, ids map[uint32]int, errs []error) []error {
	if list.kind() != '[' {
		return errs
	}
	i := 0
	for _, v := range list.members() {
		pp := o.doc.param(v, src, i)
		i++
		p := pp.Param
		if p.In == "header" && pp.legacy.t == nil && (strings.EqualFold(p.Name, "Accept") || strings.EqualFold(p.Name, "Content-Type") || strings.EqualFold(p.Name, "Authorization")) {
			continue
		}
		if p.In == "" {
			errs = append(errs, p.Err) // its identity cannot be known
			o.params = append(o.params, pp)
			continue
		}
		if k, ok := find(ids, pp.idHash, func(j int) bool { return o.params[j].identity() == pp.identity() }); ok {
			o.params[ids[k]] = pp
		} else {
			ids[k] = len(o.params)
			o.params = append(o.params, pp)
		}
	}
	return errs
}

// assignKeys sets each parameter's Key and lists the parameters in
// Operation.Params.
func (o *operation) assignKeys() {
	names := make(map[uint32]int, len(o.params)) // the first parameter of each name
	for i, pp := range o.params {
		if k, ok := find(names, pp.nameHash, func(j int) bool { return o.params[j].Name == pp.Name }); ok {
			o.params[names[k]].dotted, o.params[i].dotted = true, true
		} else {
			names[k] = i
		}
	}
	for i, pp := range o.params {
		p := pp.Param
		o.Params = append(o.Params, p)
		if p.In == "path" {
			o.pathParams = append(o.pathParams, i)
		}
		if p.In == "" {
			continue
		}
		p.Key = p.Name
		if pp.dotted {
			p.Key = p.In + "." + p.Name
		}
	}
}

// identity returns the location and name that identify the parameter, its
// header field name for a header, compared without regard to case.
func (pp *param) identity() paramID {
	if pp.In == "header" {
		return paramID{pp.In, pp.field}
	}
	return paramID{pp.In, pp.Name}
}

// paramSeed seeds the hashes of parameter identities and names.
var paramSeed = maphash.MakeSeed()

// paramHash hashes the parameter identity in and name.
func paramHash(in, name string) uint32 {
	return uint32(maphash.String(paramSeed, name) ^ maphash.String(paramSeed, in)<<1)
}

// find returns the key of m, from h on, that holds the index of a
// parameter same reports equal, or the first free one, where such a
// parameter goes. A parameter's key hashes what same compares, computed once
// per node, and those that hash alike take the keys that follow.
func find(m map[uint32]int, h uint32, same func(int) bool) (uint32, bool) {
	for {
		if i, ok := m[h]; !ok || same(i) {
			return h, ok
		}
		h++
	}
}

// param describes and compiles the Parameter Object v, item i of the list
// whose Source is list, following references. A target that references
// reach is compiled once, its descriptor copied for each; only a parameter
// written in the list, or one whose reference cannot be followed, has a
// Source in the list, made only then.
func (d *document) param(v value, list string, i int) param {
	src := func() string { return list + "/" + strconv.Itoa(i) }
	if ref, desc, _ := reference(v); !ref.ok() { // only this place reaches it
		pp := d.newParam(v, src())
		pp.Description = desc
		return pp
	}
	t, at, desc, err := d.follow(v, "")
	if err != nil {
		return param{Param: &Param{Source: src(), Err: err}}
	}
	pp := d.paramForms.get(t.id(), func() param { return d.newParam(t, at) })
	c := *pp.Param
	pp.Param, c.Description = &c, desc
	return pp
}

// newParam describes and compiles the Parameter Object t, whose Source is
// at.
func (d *document) newParam(t value, at string) param {
	p := &Param{Source: at}
	var explode, schema, content, media value
	entries, valid := 0, true
	for name, m := range t.members() { // one pass: a member's name is read from the source
		switch name {
		case "name":
			p.Name = m.string()
		case "in":
			p.In = m.string()
		case "required":
			p.Required = m.kind() == 't'
		case "deprecated":
			p.Deprecated = m.kind() == 't'
		case "allowEmptyValue":
			p.AllowEmptyValue = m.kind() == 't'
		case "allowReserved":
			p.AllowReserved = m.kind() == 't'
		case "style":
			p.Style = m.string()
		case "explode":
			explode = m
		case "schema":
			schema = m
		case "content":
			content = m
		}
	}
	if t.t.edition == 20 {
		return d.swaggerParam(t, at, p)
	}
	if content.ok() {
		for typ, m := range content.members() {
			if entries++; entries == 1 {
				p.ContentType, media = typ, m
				p.Schema = d.schema(m.get("schema"), at, "/content/"+token(typ)+"/schema")
			}
		}
		_, valid = parseMedia(p.ContentType)
		p.Style, p.AllowReserved, p.ExplodeSet = "", false, explode.ok()
	} else if schema.ok() {
		p.Schema = d.schema(schema, at, "/schema")
	}
	switch {
	case !slices.Contains([]string{"path", "query", "header", "cookie"}, p.In) && !(p.In == "querystring" && t.t.edition == 32):
		p.Err = fmt.Errorf("parameter location %q is not path, query, header or cookie", p.In)
	case p.In == "querystring" && (!content.ok() || t.get("style").ok() || explode.ok() || t.get("allowReserved").ok()):
		p.Err = errors.New("querystring requires content and cannot declare style, explode or allowReserved")
	case content.ok() && (content.kind() != '{' || entries != 1 || schema.ok()):
		p.Err = errors.New("a parameter needs a content map of exactly one entry, and then no schema")
	case !valid:
		p.Err = fmt.Errorf("invalid media type %q", p.ContentType)
	case p.In == "header" && !isToken(p.Name):
		p.Err = fmt.Errorf("header parameter name %q is not a field name", p.Name)
	case p.In == "header" && slices.ContainsFunc(derivedFields, func(f string) bool { return strings.EqualFold(f, p.Name) }):
		p.Err = fmt.Errorf("a header parameter cannot set %s, which net/http derives or HTTP forbids", label(p.Name))
	case p.In == "header" && strings.EqualFold(p.Name, "Cookie"):
		p.Err = errors.New("OpenAPI leaves the effect of a header parameter named Cookie undefined")
	}
	var pp param
	if content.ok() {
		m, _ := parseMedia(p.ContentType)
		pp = param{Param: p, style: &noStyle, set: unreservedSet, name: escape(p.Name, unreservedSet), media: &m}
		if isForm(m) { // its fields, of their default types: Encoding applies to bodies only
			enc := value{}
			if p.In == "querystring" {
				enc = media.get("encoding")
			}
			pp.form, _ = d.encodingOf([]value{media.get("schema")}, at+"/content/"+token(p.ContentType)+"/schema", enc, at+"/content/"+token(p.ContentType)+"/encoding", m)
		}
	} else {
		if p.Style == "" {
			p.Style = "simple"
			if p.In == "query" || p.In == "cookie" {
				p.Style = "form"
			}
		}
		p.AllowReserved = p.AllowReserved && (p.In == "query" || t.t.edition == 32 && (p.In == "path" || p.In == "cookie" && p.Style == "form"))
		pp = compileStyle(p, p.In, explode)
		if p.Style == "cookie" {
			if t.t.edition != 32 || p.In != "cookie" {
				p.Err = errors.New("cookie style requires OpenAPI 3.2 cookie location")
			} else {
				pp.style, pp.set, pp.name = &cookieForm, nil, p.Name
				p.Explode = explode.kind() != 'f'
			}
		}
	}
	pp.cookie32 = p.In == "cookie" && t.t.edition == 32
	pp.required = p.Required || p.In == "path"
	switch {
	case p.In == "cookie" && p.Style == "form":
		pp.style = &cookieForm
	case p.In == "header":
		pp.field, pp.set = textproto.CanonicalMIMEHeaderKey(p.Name), nil
	}
	loc, _, dotted := strings.Cut(p.Name, ".")
	pp.idHash, pp.nameHash = paramHash(p.In, pp.identity().name), uint32(maphash.String(paramSeed, p.Name))
	pp.dotted = p.Name == "" || strings.HasPrefix(p.Name, "/") || strings.HasPrefix(p.Name, "Input.Body") ||
		dotted && slices.Contains([]string{"path", "query", "header", "cookie", "querystring"}, loc)
	return pp
}

// compileStyle compiles the RFC 6570 serialization of p, whose Style is set,
// as a parameter in in: its effective explode, true for deepObject, which
// ignores it; an Err, unless p has one, for a style in does not allow or a
// delimited style exploded; and its style, name and percent-encoding.
func compileStyle(p *Param, in string, explode value) param {
	p.ExplodeSet = explode.ok()
	p.Explode = explode.kind() == 't' || !explode.ok() && p.Style == "form" || p.Style == "deepObject"
	switch {
	case p.Err != nil:
	case !styleAllowed(in, p.Style):
		p.Err = fmt.Errorf("style %q is not allowed for a %s value", p.Style, in)
	case p.Explode && (p.Style == "spaceDelimited" || p.Style == "pipeDelimited"):
		p.Err = fmt.Errorf("OpenAPI does not define the %s style with explode true", p.Style)
	}
	pp := param{Param: p, style: cmp.Or(styles[p.Style], &noStyle), set: unreservedSet, name: escape(p.Name, unreservedSet)}
	if p.AllowReserved {
		pp.set = reservedSet
	}
	return pp
}

// styleAllowed reports whether OpenAPI allows style for a parameter in in.
func styleAllowed(in, style string) bool {
	switch in {
	case "path":
		return style == "matrix" || style == "label" || style == "simple"
	case "query":
		return style == "form" || style == "spaceDelimited" || style == "pipeDelimited" || style == "deepObject"
	case "header":
		return style == "simple"
	}
	return style == "form" || in == "cookie" && style == "cookie"
}

// A content is the content map of a Request Body or Response Object, and a
// response's headers, compiled once for every reference to it.
type content struct {
	source    string // the object's Source
	headers   []*Param
	media     []*Media
	parsed    []parsedMedia   // media, parsed
	encodings []*formEncoding // the fields of each Media, under a form or multipart type it covers
	success   []parsedMedia   // the concrete media types among them, for a 2xx response
}

// noContent is the content of an object a reference cannot reach.
var noContent content

// message describes the Request Body, when request, or Response Object v,
// whose Source is src, following references, with its content. The target
// is absent when a reference cannot be resolved; the Message then reports it
// in Err.
func (d *document) message(v value, src string, request bool) (*Message, value, *content) {
	t, at, desc, err := d.follow(v, src)
	if err != nil {
		return &Message{Source: src, Err: err}, value{}, &noContent
	}
	memo := &d.contents
	if request {
		memo = &d.bodies
	}
	var c *content
	if t == v { // only this place reaches it
		c = d.content(t, at, request)
	} else {
		c = memo.get(t.id(), func() *content { return d.content(t, at, request) })
	}
	return &Message{Description: desc, Source: c.source, Headers: c.headers, Media: c.media}, t, c
}

// content compiles the content map of the object t, whose Source is at, a
// Request Body Object when request.
func (d *document) content(t value, at string, request bool) *content {
	c := &content{source: at}
	if h := t.get("headers"); h.ok() && !request {
		c.headers = d.headers(h, at+"/headers")
	}
	if m := t.get("content"); m.kind() == '{' {
		for typ, mv := range m.members() {
			mat := at + "/content/" + token(typ)
			md := &Media{Type: typ, Source: mat, Schema: d.schema(mv.get("schema"), mat, "/schema")}
			if mv.t.edition == 32 {
				md.ItemSchema = d.schema(mv.get("itemSchema"), mat, "/itemSchema")
			}
			pm, ok := parseMedia(typ)
			switch {
			case !ok:
				md.Err = fmt.Errorf("invalid media type %q", typ)
			case pm.concrete():
				c.success = append(c.success, pm)
				fallthrough
			default:
				md.Sequential = pm.class() == sequentialClass || isMultipart(pm)
			}
			// A request's form or multipart type, or range of multipart
			// types, has the fields its schema and Encoding give; */* and
			// application/*, which cover such types, those of its schema alone
			// (OpenAPI 3.1.2 section 4.8.14: Encoding applies to form and
			// multipart types, and to Request Body Objects only). A response's
			// boundary is the response's.
			var enc *formEncoding
			schema := []value{mv.get("schema")}
			switch {
			case !ok || !request:
			case isForm(pm) || isMultipart(pm):
				if _, _, err := pm.boundary(); err != nil {
					md.Err = err
				}
				enc, md.Encoding = d.encodingOf(schema, mat+"/schema", mv.get("encoding"), mat+"/encoding", pm)
			case pm.typ == "*" || pm.sub == "*" && strings.EqualFold(pm.typ, "application"):
				enc, _ = d.encodingOf(schema, mat+"/schema", value{}, "", pm)
			}
			if request && isMultipart(pm) && mv.t.edition == 32 {
				md.Encoding = d.positionalEncoding(enc, mv, schema, mat, md.Encoding...)
			}
			c.media, c.parsed, c.encodings = append(c.media, md), append(c.parsed, pm), append(c.encodings, enc)
		}
	}
	return c
}

func (o *operation) addResponse(m *Message, c *content) {
	r := responsePlan{Message: m, media: c.parsed}
	key := m.Key
	digit := func(i int) bool { return '0' <= key[i] && key[i] <= '9' }
	switch {
	case key == "default":
		r.fallback = true
	case len(key) == 3 && '1' <= key[0] && key[0] <= '5' && key[1:] == "XX":
		r.class = int(key[0] - '0')
	case len(key) == 3 && '1' <= key[0] && key[0] <= '5' && digit(1) && digit(2):
		r.status, _ = strconv.Atoi(key)
	case m.Err == nil:
		m.Err = fmt.Errorf("response key %q is not a status code, a range such as 4XX, or default", key)
	}
	o.Responses = append(o.Responses, m)
	o.responses = append(o.responses, r)
	if (r.status/100 == 2 || r.class == 2) && len(c.success) > 0 {
		o.success = append(o.success, c.success)
	}
}

// declaration returns the response that governs status, or nil.
func (pl *plan) declaration(status int) *responsePlan {
	if status < 100 || status > 599 {
		return nil
	}
	var class, fallback *responsePlan
	for i := range pl.responses {
		switch r := &pl.responses[i]; {
		case r.status == status:
			return r
		case r.class == status/100 && class == nil:
			class = r
		case r.fallback && fallback == nil:
			fallback = r
		}
	}
	if class != nil {
		return class
	}
	return fallback
}

// parsePath splits the path template into its text, percent-encoded, and
// its parameters. A path parameter the template does not name cannot be
// serialized: it is copied with Err set, as other paths may share it.
func (o *operation) parsePath() error {
	text, names, ok := splitTemplate(o.Path)
	if !ok {
		return fmt.Errorf("path template %q has an unclosed {", o.Path)
	}
	byName := make(map[string]int, len(o.pathParams))
	for j, i := range o.pathParams {
		byName[o.params[i].Name] = j
	}
	named := make([]bool, len(o.pathParams))
	for i, name := range names {
		j, ok := byName[name]
		if !ok {
			return fmt.Errorf("path template %q names %q, which no path parameter declares", o.Path, name)
		}
		o.path = append(o.path, pathPart{escape(text[i], pathSet), -1}, pathPart{param: o.pathParams[j]})
		named[j] = true
	}
	o.path = append(o.path, pathPart{escape(text[len(names)], pathSet), -1})
	copied := false
	for j, i := range o.pathParams {
		if p := o.params[i].Param; !named[j] && p.Err == nil {
			if !copied {
				o.params, o.Params, copied = slices.Clone(o.params), slices.Clone(o.Params), true
			}
			c := *p
			c.Err = fmt.Errorf("path parameter %q is not named in the path template", p.Name)
			o.params[i].Param, o.Params[i] = &c, &c
		}
	}
	return nil
}

// canonicalString writes s as a JSON string escaped as RFC 8785 escapes
// strings.
func canonicalString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch e := strings.IndexByte("\"\\\b\t\n\f\r", c); {
		case e >= 0:
			b.WriteByte('\\')
			b.WriteByte(`"\btnfr`[e])
		case c < ' ':
			b.WriteString(`\u00`)
			b.WriteByte(lowerHex[c>>4])
			b.WriteByte(lowerHex[c&15])
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

// A server is a server an operation may be sent to, with its URL template:
// literal text and variables alternating.
type server struct {
	*Server
	t     *tree     // the document that holds it, against whose URI a relative URL resolves
	text  []string  // the literal text around the variables
	vars  []urlVar  // each variable of the template, in order
	fixed *endpoint // the URL with every variable at its default, if usable
}

// A urlVar is a variable of a server URL template: its index into
// Server.Variables, and the part of the URL its default falls in, with
// every default substituted (T1-23).
type urlVar struct {
	index int
	part  urlPart
}

// A urlPart is the part of a URL a variable's values may change.
type urlPart uint8

const (
	inPath          urlPart = iota
	inScheme                // the resulting scheme must be one
	inAuthority             // no "/", "?", "#", "@" or "\"
	authorityOrPath         // an empty default where the authority meets the path: either
	wholeURL                // its default spans "://", or it is the template: any value
)

// An endpoint is a usable server URL, its variables substituted.
type endpoint struct {
	scheme, host, path string // path percent-encoded
}

// A serverList is a Servers list compiled: its servers, and their
// descriptions.
type serverList struct {
	servers []*server
	desc    []*Server
}

// What an operation inherits from the root: its servers, or the default
// one, and its security requirements.
type inheritance struct {
	servers  *serverList
	security securityPlan
}

// inherited returns what an operation inherits from the root, compiled
// once.
func (d *document) inherited() *inheritance {
	return loadOrMake(&d.inherits, func() *inheritance {
		r := &inheritance{}
		if s := d.root().get("servers"); s.hasMembers() {
			r.servers = d.parseServers(s, d.source("/servers"))
		} else {
			sv := newServer(&Server{ID: "default", URL: "/"}, value{}, d.tree)
			r.servers = &serverList{[]*server{sv}, []*Server{sv.Server}}
		}
		if sec := d.root().get("security"); sec.ok() {
			r.security = d.compileSecurity(sec)
		}
		return r
	})
}

// swaggerServers assembles entry host/basePath with the operation's schemes.
func (d *document) swaggerServers(schemes value) *serverList {
	r := d.root()
	if !schemes.hasMembers() {
		schemes = r.get("schemes")
	}
	list := schemes.strs()
	host := r.str("host")
	if d.tree.httpBase() {
		if host == "" {
			host = d.base.Host
		}
		if len(list) == 0 {
			list = []string{d.base.Scheme}
		}
	}
	if len(list) == 0 {
		list = []string{""}
	}
	sl := &serverList{}
	for i, scheme := range list {
		s := &Server{ID: "swagger-" + strconv.Itoa(i) + "-" + scheme, URL: scheme + "://" + host + r.str("basePath")}
		sv := newServer(s, value{}, d.tree)
		if host == "" || scheme == "" {
			s.Err = errors.New("Swagger server requires host and schemes or an HTTP retrieval URI")
			sv.fixed = nil
		}
		sl.servers, sl.desc = append(sl.servers, sv), append(sl.desc, s)
	}
	return sl
}

// parseServers compiles the Server Objects of list, whose Source is src.
func (d *document) parseServers(list value, src string) *serverList {
	sl := &serverList{}
	for _, v := range list.members() {
		at := src + "/" + strconv.Itoa(len(sl.servers))
		s := &Server{ID: idOf(v), URL: v.str("url"), Description: v.str("description"), Source: at}
		if v.t.edition == 32 {
			s.Name = v.str("name")
		}
		sl.servers, sl.desc = append(sl.servers, newServer(s, v.get("variables"), list.t)), append(sl.desc, s)
	}
	return sl
}

// idOf returns the Server.ID of the Server Object v: its node's number,
// which no other declaration in the documents loaded has, and which every
// operation that inherits it shares.
func idOf(v value) string { return strconv.Itoa(int(v.id())) }

// newServer completes s, written in the document t, from its URL template
// and declared variables.
func newServer(s *Server, declared value, t *tree) *server {
	text, names, _ := splitTemplate(s.URL)
	sv := &server{Server: s, t: t, text: text}
	var index map[string]int
	for _, name := range names {
		j, seen := index[name]
		if !seen {
			if index == nil {
				index = map[string]int{}
			}
			j = len(s.Variables)
			index[name] = j
			v := Variable{Name: name}
			if n := declared.get(name); n.ok() {
				v.Declared, v.Description = true, n.str("description")
				v.Default, v.DefaultSet = n.str("default"), n.get("default").ok()
				if e := n.get("enum"); e.kind() == '[' {
					v.Enum = append([]string{}, e.strs()...)
				}
			}
			s.Variables = append(s.Variables, v)
		}
		sv.vars = append(sv.vars, urlVar{index: j})
	}
	at := make([]int, len(sv.vars)) // where each default falls
	defaults := sv.substitute(func(i, pos int) string { at[i] = pos; return s.Variables[sv.vars[i].index].Default })
	colon, authority, path := urlParts(defaults)
	for i := range sv.vars {
		a, b := at[i], at[i]+len(s.Variables[sv.vars[i].index].Default)
		switch {
		case authority >= 0 && a <= colon && b >= colon+3, len(sv.text) == 2 && sv.text[0]+sv.text[1] == "":
			sv.vars[i].part = wholeURL // its default spans "://", or it is the whole template
		case a <= colon:
			sv.vars[i].part = inScheme
		case authority >= 0 && a == b && a == path:
			sv.vars[i].part = authorityOrPath
		case authority >= 0 && a >= authority && a < path:
			sv.vars[i].part = inAuthority
		}
	}
	// Only a defect no value can repair makes the server unusable for good:
	// a query or fragment in its text, or userinfo in its authority.
	literal := sv.substitute(func(int, int) string { return "x" })
	_, authority, path = urlParts(literal)
	if len(sv.vars) == 0 {
		if _, err := t.resolveServerURL(literal); err != nil {
			s.Err = fmt.Errorf("server URL %q cannot be used: %w", s.URL, err)
		}
	} else if strings.ContainsAny(literal, "?#") || authority >= 0 && strings.Contains(literal[authority:path], "@") ||
		strings.HasPrefix(sv.text[0], "/") && !t.httpBase() {
		s.Err = fmt.Errorf("server URL %q cannot be used whatever its variables' values", s.URL)
	}
	if s.Err == nil && !slices.ContainsFunc(s.Variables, func(v Variable) bool { return !v.DefaultSet }) {
		if ep, err := t.resolveServerURL(defaults); err == nil {
			sv.fixed = &ep
		}
	}
	return sv
}

// substitute returns the server URL with the ith variable of its template
// replaced by value(i, at), at being where the value begins.
func (s *server) substitute(value func(i, at int) string) string {
	var b strings.Builder
	for i, t := range s.text {
		b.WriteString(t)
		if i < len(s.vars) {
			b.WriteString(value(i, b.Len()))
		}
	}
	return b.String()
}

// urlParts returns where the scheme of the URL reference u ends, at its
// colon, where its authority begins, after "//", each -1 when u has none,
// and where its path begins (RFC 3986 sections 3 and 4.2).
func urlParts(u string) (colon, authority, path int) {
	colon, authority = -1, -1
	if i := strings.IndexAny(u, ":/?#"); i > 0 && u[i] == ':' {
		colon, path = i, i+1
	}
	if !strings.HasPrefix(u[path:], "//") {
		return colon, authority, path
	}
	authority, path = path+2, len(u)
	if i := strings.IndexAny(u[authority:], "/?#"); i >= 0 {
		path = authority + i
	}
	return colon, authority, path
}

// check returns why value, substituted at at in u, whose parts urlParts
// gives, changes more of u than the part p, or nil.
func (p urlPart) check(u, value string, at, colon, path int) error {
	if p == authorityOrPath {
		if err := inPath.check(u, value, at, colon, path); err != nil && inAuthority.check(u, value, at, colon, path) != nil {
			return err
		}
		return nil
	}
	switch {
	case p == inScheme && (colon < 0 || at+len(value) > colon || !isScheme(u[:colon])):
		return errors.New("the value must leave the scheme a URI scheme (RFC 3986 section 3.1)")
	case p == inAuthority && strings.ContainsAny(value, `/?#@\`):
		return errors.New(`a value in the authority cannot hold "/", "?", "#", "@" or "\"`)
	case p != inPath:
	case at < path:
		return errors.New("a value in the path cannot change the scheme or authority")
	case strings.ContainsAny(value, "?#"):
		return errors.New("a value in the path cannot add a query or fragment")
	case dotSegment(u, max(path, strings.LastIndexByte(u[:at], '/')+1), at+len(value)):
		return errDotSegment
	}
	return nil
}

// splitTemplate splits a URL template into its literal text and the names
// of its {variables}, one more text than names. It reports false when a
// "{" is not closed, the rest then being text.
func splitTemplate(s string) (text, names []string, ok bool) {
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			return append(text, s), names, true
		}
		name, after, found := strings.Cut(s[i+1:], "}")
		if !found {
			return append(text, s), names, false
		}
		text, names, s = append(text, s[:i]), append(names, name), after
	}
}
