package openapi

import (
	"cmp"
	"errors"
	"fmt"
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
	*Param
	*style
	set      *charset // how its values are percent-encoded, or nil to write them as given
	required bool
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
		o.Path, o.Source, o.Err = e.path, d.source(e.levels.ptr), e.err
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
	d, n, src := e.doc, e.node, e.doc.source(e.ptr())
	o := &operation{doc: d}
	op := &o.Operation
	op.ID, op.Method, op.Source = e.id, methods[e.m].upper, src
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

	ids := map[paramID]int{}
	list, at, err := e.field(parametersField)
	errs = append(errs, err)
	errs = o.addParams(list, at, ids, errs)
	errs = o.addParams(params, src+"/parameters", ids, errs)
	o.assignKeys()

	if body.ok() && op.Method != "TRACE" {
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
				m.Key = key
				o.addResponse(m, c)
			}
		}
	}

	var sl *serverList
	switch s, at, err := e.field(serversField); {
	case servers.hasMembers():
		sl = d.parseServers(servers, src+"/servers")
	case s.ok():
		errs = append(errs, err)
		sl = d.serverLists.get(s.i, func() *serverList { return d.parseServers(s, at) })
	default:
		sl = d.inherited().servers
	}
	o.servers, op.Servers = sl.servers, sl.desc

	sec := d.inherited().security
	if security.ok() {
		sec = d.compileSecurity(security)
	}
	op.Security, o.security, errs = sec.reqs, sec.alts, append(errs, sec.err)
	if len(sec.dests) > 0 { // keep the parameters where a credential goes
		for id, i := range ids {
			if !sec.dests[id] {
				continue
			}
			if o.dests == nil {
				o.dests = map[paramID]int{}
			}
			o.dests[id] = i
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
	return l.v.get(name), e.doc.source(l.ptr + "/" + name), err
}

// A paramID identifies a parameter by location and name, a header's name
// compared without regard to case.
type paramID struct{ in, name string }

// addParams adds the parameters of list, each taking the place of an
// earlier one it identifies.
func (o *operation) addParams(list value, src string, ids map[paramID]int, errs []error) []error {
	if list.kind() != '[' {
		return errs
	}
	i := 0
	for _, v := range list.members() {
		pp := o.doc.param(v, src, i)
		i++
		p := pp.Param
		if p.In == "header" && (strings.EqualFold(p.Name, "Accept") || strings.EqualFold(p.Name, "Content-Type") || strings.EqualFold(p.Name, "Authorization")) {
			continue
		}
		id := paramID{p.In, p.Name}
		switch p.In {
		case "":
			errs = append(errs, p.Err) // its identity cannot be known
			o.params = append(o.params, pp)
			continue
		case "header":
			id.name = pp.field // canonical, so compared without regard to case
		}
		if j, ok := ids[id]; ok {
			o.params[j] = pp
		} else {
			ids[id] = len(o.params)
			o.params = append(o.params, pp)
		}
	}
	return errs
}

// assignKeys sets each parameter's Key and lists the parameters in
// Operation.Params.
func (o *operation) assignKeys() {
	names := make(map[string]int, len(o.params))
	for _, p := range o.params {
		names[p.Name]++
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
		loc, _, dotted := strings.Cut(p.Name, ".")
		p.Key = p.Name
		if names[p.Name] > 1 || p.Name == "" || strings.HasPrefix(p.Name, "/") || strings.HasPrefix(p.Name, "Input.Body") ||
			dotted && slices.Contains([]string{"path", "query", "header", "cookie", "querystring"}, loc) {
			p.Key = p.In + "." + p.Name
		}
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
	pp := d.paramForms.get(t.i, func() param { return d.newParam(t, at) })
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
	case !slices.Contains([]string{"path", "query", "header", "cookie"}, p.In):
		p.Err = fmt.Errorf("parameter location %q is not path, query, header or cookie", p.In)
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
			pp.form, _ = d.encodingOf([]value{media.get("schema")}, at+"/content/"+token(p.ContentType)+"/schema", value{}, "", m)
		}
	} else {
		if p.Style == "" {
			p.Style = "simple"
			if p.In == "query" || p.In == "cookie" {
				p.Style = "form"
			}
		}
		p.AllowReserved = p.AllowReserved && p.In == "query"
		pp = compileStyle(p, p.In, explode)
	}
	pp.required = p.Required || p.In == "path"
	switch {
	case p.In == "cookie" && p.Style == "form":
		pp.style = &cookieForm
	case p.In == "header":
		pp.field, pp.set = textproto.CanonicalMIMEHeaderKey(p.Name), nil
	}
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
	return style == "form"
}

// A content is the content map of a Request Body or Response Object,
// compiled once for every reference to it.
type content struct {
	source    string // the object's Source
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
	if t.i == v.i { // only this place reaches it
		c = d.content(t, at, request)
	} else {
		c = memo.get(t.i, func() *content { return d.content(t, at, request) })
	}
	return &Message{Description: desc, Source: c.source, Media: c.media}, t, c
}

// content compiles the content map of the object t, whose Source is at, a
// Request Body Object when request.
func (d *document) content(t value, at string, request bool) *content {
	c := &content{source: at}
	if m := t.get("content"); m.kind() == '{' {
		for typ, mv := range m.members() {
			mat := at + "/content/" + token(typ)
			md := &Media{Type: typ, Source: mat, Schema: d.schema(mv.get("schema"), mat, "/schema")}
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
			sv := d.newServer(&Server{ID: "default", URL: "/"}, value{})
			r.servers = &serverList{[]*server{sv}, []*Server{sv.Server}}
		}
		if sec := d.root().get("security"); sec.ok() {
			r.security = d.compileSecurity(sec)
		}
		return r
	})
}

// parseServers compiles the Server Objects of list, whose Source is src.
func (d *document) parseServers(list value, src string) *serverList {
	sl := &serverList{}
	for _, v := range list.members() {
		at := src + "/" + strconv.Itoa(len(sl.servers))
		s := &Server{ID: idOf(v), URL: v.str("url"), Description: v.str("description"), Source: at}
		sl.servers, sl.desc = append(sl.servers, d.newServer(s, v.get("variables"))), append(sl.desc, s)
	}
	return sl
}

// idOf returns the Server.ID of the Server Object v: its node's index, which
// no other declaration in the document has, and which every operation that
// inherits it shares.
func idOf(v value) string { return strconv.Itoa(int(v.i)) }

// newServer completes s from its URL template and declared variables.
func (d *document) newServer(s *Server, declared value) *server {
	text, names, _ := splitTemplate(s.URL)
	sv := &server{Server: s, text: text}
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
		if _, err := d.resolveServerURL(literal); err != nil {
			s.Err = fmt.Errorf("server URL %q cannot be used: %w", s.URL, err)
		}
	} else if strings.ContainsAny(literal, "?#") || authority >= 0 && strings.Contains(literal[authority:path], "@") ||
		strings.HasPrefix(sv.text[0], "/") && !d.httpBase() {
		s.Err = fmt.Errorf("server URL %q cannot be used whatever its variables' values", s.URL)
	}
	if s.Err == nil && !slices.ContainsFunc(s.Variables, func(v Variable) bool { return !v.DefaultSet }) {
		if ep, err := d.resolveServerURL(defaults); err == nil {
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
