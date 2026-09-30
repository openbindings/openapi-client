package openapi

import (
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
	params    []param
	path      []pathPart
	servers   []*server
	body      []parsedMedia // the request body's Media, parsed
	responses []responsePlan
	success   []parsedMedia // the concrete media types of 2xx responses
}

// A param is a parameter with its serialization rules.
type param struct {
	*Param
	required    bool
	field       string // a header parameter's canonical field name
	name        string // a query parameter's percent-encoded name
	unsupported error  // why stage 1 cannot serialize it, or nil
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

func (e *entry) build() *operation {
	d := e.doc
	o := &operation{doc: d}
	op := &o.Operation
	if e.m < 0 {
		op.Path, op.Source, op.Err = e.path, d.source(e.levels.ptr), e.err
		return o
	}
	ptr, n := e.ptr(), e.node
	op.Key, op.ID, op.Method, op.Path, op.Source = e.id, e.id, methods[e.m].upper, e.path, d.source(ptr)
	if e.id == "" || d.byID[e.id] != e {
		op.Key = op.Method + " " + op.Path
	}
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
	errs = o.addParams(params, ptr+"/parameters", ids, errs)
	o.assignKeys()

	if body.ok() && op.Method != "TRACE" {
		var target value
		op.Body, target = d.message(body, ptr+"/requestBody")
		if !target.ok() {
			errs = append(errs, op.Body.Err)
		}
		op.Body.Required = target.flag("required")
		o.body = parseMedias(op.Body.Media)
	}
	if responses.kind() == '{' {
		for key, r := range responses.members() {
			if !strings.HasPrefix(key, "x-") {
				m, _ := d.message(r, ptr+"/responses/"+escapeToken(key))
				m.Key = key
				o.addResponse(m)
			}
		}
	}

	if servers.hasMembers() {
		o.servers = d.parseServers(servers, ptr+"/servers")
	} else if s, at, err := e.field(serversField); s.ok() {
		errs = append(errs, err)
		o.servers = d.parseServers(s, at)
	} else {
		o.servers = d.rootServers()
	}
	for _, s := range o.servers {
		op.Servers = append(op.Servers, s.Server)
	}

	if !security.ok() {
		security = d.root().get("security")
	}
	for _, r := range security.members() {
		if security.kind() == '[' && r.kind() == '{' {
			op.Security = append(op.Security, securityRequirement(r))
		}
	}

	errs = append(errs, o.parsePath())
	op.Err = errors.Join(errs...)
	return o
}

// field returns the Path Item field f, parametersField or serversField,
// from the level of the chain that has it, with its pointer, and an error
// when several do.
func (e *entry) field(f int) (value, string, error) {
	name := "parameters"
	if f == serversField {
		name = "servers"
	}
	l := e.sum.at[f]
	switch {
	case l == nil:
		return value{}, "", nil
	case e.sum.dup&(1<<f) != 0:
		return l.v.get(name), l.ptr + "/" + name, fmt.Errorf("the Path Item and its $ref target both define %s", name)
	}
	return l.v.get(name), l.ptr + "/" + name, nil
}

// A paramID identifies a parameter by location and name, a header's name
// compared without regard to case.
type paramID struct{ in, name string }

// addParams adds the parameters of list, each taking the place of an
// earlier one it identifies.
func (o *operation) addParams(list value, ptr string, ids map[paramID]int, errs []error) []error {
	if list.kind() != '[' {
		return errs
	}
	i := 0
	for _, v := range list.members() {
		p := o.doc.param(v, ptr+"/"+strconv.Itoa(i))
		i++
		if p.In == "header" && (strings.EqualFold(p.Name, "Accept") || strings.EqualFold(p.Name, "Content-Type") || strings.EqualFold(p.Name, "Authorization")) {
			continue
		}
		pp := param{Param: p, required: p.Required || p.In == "path"}
		id := paramID{p.In, p.Name}
		switch p.In {
		case "":
			errs = append(errs, p.Err) // its identity cannot be known
			o.params = append(o.params, pp)
			continue
		case "header":
			pp.field = textproto.CanonicalMIMEHeaderKey(p.Name)
			id.name = strings.ToLower(p.Name)
		case "query":
			pp.name = escape(p.Name)
		}
		switch {
		case p.Err != nil:
		case p.ContentType != "":
			pp.unsupported = notYet("content parameters")
		case p.In == "cookie":
			pp.unsupported = notYet("cookie parameters")
		case p.AllowReserved:
			pp.unsupported = notYet("allowReserved")
		case p.Style != "simple" && p.Style != "form":
			pp.unsupported = notYet("the " + p.Style + " style")
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
	for _, pp := range o.params {
		p := pp.Param
		o.Params = append(o.Params, p)
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

// param describes the Parameter Object v at ptr, following references.
func (d *document) param(v value, ptr string) *Param {
	t, at, desc, err := d.follow(v, ptr)
	if err != nil {
		return &Param{Source: d.source(ptr), Err: err}
	}
	p := &Param{Description: desc, Source: d.source(at)}
	var explode, content value
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
			p.Schema = d.schema(m, at+"/schema")
		case "content":
			content = m
		}
	}
	p.AllowReserved = p.AllowReserved && p.In == "query"
	if content.kind() == '{' && content.hasMembers() {
		for typ, m := range content.members() {
			p.ContentType = typ
			p.Schema = d.schema(m.get("schema"), at+"/content/"+escapeToken(typ)+"/schema")
			break
		}
	} else {
		switch {
		case p.Style != "":
		case p.In == "query" || p.In == "cookie":
			p.Style = "form"
		default:
			p.Style = "simple"
		}
		p.ExplodeSet = explode.ok()
		p.Explode = explode.kind() == 't' || !explode.ok() && p.Style == "form"
	}
	switch {
	case !slices.Contains([]string{"path", "query", "header", "cookie"}, p.In):
		p.Err = fmt.Errorf("parameter location %q is not path, query, header or cookie", p.In)
	case p.ContentType == "" && !styleAllowed(p.In, p.Style):
		p.Err = fmt.Errorf("style %q is not allowed for a %s parameter", p.Style, p.In)
	case p.In == "header" && !isToken(p.Name):
		p.Err = fmt.Errorf("header parameter name %q is not a field name", p.Name)
	case p.In == "header" && strings.EqualFold(p.Name, "Host"):
		p.Err = errors.New("a header parameter cannot set Host, which net/http derives from the URL")
	}
	return p
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

// message describes the Request Body or Response Object v at ptr,
// following references. The target is absent when a reference cannot be
// resolved; the Message then reports it in Err.
func (d *document) message(v value, ptr string) (*Message, value) {
	t, at, desc, err := d.follow(v, ptr)
	if err != nil {
		return &Message{Source: d.source(ptr), Err: err}, value{}
	}
	m := &Message{Description: desc, Source: d.source(at)}
	if c := t.get("content"); c.kind() == '{' {
		for typ, mv := range c.members() {
			mat := at + "/content/" + escapeToken(typ)
			md := &Media{Type: typ, Source: d.source(mat), Schema: d.schema(mv.get("schema"), mat+"/schema")}
			if pm, ok := parseMedia(typ); !ok {
				md.Err = fmt.Errorf("invalid media type %q", typ)
			} else {
				md.Sequential = pm.class() == sequentialClass || strings.EqualFold(pm.typ, "multipart")
			}
			m.Media = append(m.Media, md)
		}
	}
	return m, t
}

func (o *operation) addResponse(m *Message) {
	r := responsePlan{Message: m, media: parseMedias(m.Media)}
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
	if r.status/100 == 2 || r.class == 2 {
		for i, mt := range r.media {
			if m.Media[i].Err == nil && mt.concrete() {
				o.success = append(o.success, mt)
			}
		}
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
// serialized.
func (o *operation) parsePath() error {
	byName := map[string]int{}
	for i, p := range o.params {
		if p.In == "path" {
			byName[p.Name] = i
		}
	}
	named := make([]bool, len(o.params))
	for rest := o.Path; rest != ""; {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			o.path = append(o.path, pathPart{escapePath(rest), -1})
			break
		}
		name, after, ok := strings.Cut(rest[i+1:], "}")
		if !ok {
			return fmt.Errorf("path template %q has an unclosed {", o.Path)
		}
		j, ok := byName[name]
		if !ok {
			return fmt.Errorf("path template %q names %q, which no path parameter declares", o.Path, name)
		}
		o.path = append(o.path, pathPart{escapePath(rest[:i]), -1}, pathPart{param: j})
		named[j] = true
		rest = after
	}
	for i, p := range o.params {
		if p.In == "path" && !named[i] && p.Err == nil {
			p.Err = fmt.Errorf("path parameter %q is not named in the path template", p.Name)
		}
	}
	return nil
}

// securityRequirement describes the Security Requirement Object v.
func securityRequirement(v value) SecurityRequirement {
	var r SecurityRequirement
	for name, scopes := range v.members() {
		r.Schemes = append(r.Schemes, SecurityScheme{Name: name, Scopes: scopes.strs()})
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range slices.SortedFunc(slices.Values(r.Schemes), func(a, b SecurityScheme) int { return strings.Compare(a.Name, b.Name) }) {
		if i > 0 {
			b.WriteByte(',')
		}
		canonicalString(&b, s.Name)
		b.WriteString(":[")
		for j, scope := range slices.Compact(slices.Sorted(slices.Values(s.Scopes))) {
			if j > 0 {
				b.WriteByte(',')
			}
			canonicalString(&b, scope)
		}
		b.WriteByte(']')
	}
	b.WriteByte('}')
	r.Key = b.String()
	return r
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
	text  []string // the literal text around the variables
	vars  []urlVar
	fixed *endpoint // the URL with every variable at its default, if usable
}

// A urlVar is one variable of a server URL.
type urlVar struct {
	index     int  // into Server.Variables
	authority bool // substituted into the scheme or authority
}

// An endpoint is a usable server URL, its variables substituted.
type endpoint struct {
	scheme, host, path string // path percent-encoded
}

func (d *document) rootServers() []*server {
	d.serversOnce.Do(func() {
		if s := d.root().get("servers"); s.hasMembers() {
			d.servers = d.parseServers(s, "/servers")
		} else {
			d.servers = []*server{d.newServer(&Server{ID: "default", URL: "/"}, value{})}
		}
	})
	return d.servers
}

func (d *document) parseServers(list value, ptr string) []*server {
	var servers []*server
	for _, v := range list.members() {
		at := d.source(ptr + "/" + strconv.Itoa(len(servers)))
		s := &Server{ID: at, URL: v.str("url"), Description: v.str("description"), Source: at}
		servers = append(servers, d.newServer(s, v.get("variables")))
	}
	return servers
}

// newServer completes s from its URL template and declared variables.
func (d *document) newServer(s *Server, declared value) *server {
	sv := &server{Server: s}
	var index map[string]int
	// Where the text so far ends: in the scheme, with or without a "/" seen,
	// in the authority, or in the path.
	authority, path, slash := false, false, false
	advance := func(text string) {
		if i := strings.Index(text, "://"); !authority && !path && i >= 0 {
			authority, text = true, text[i+3:]
		}
		slash = slash || strings.Contains(text, "/")
		path = path || authority && slash
		authority = authority && !path
	}
	for rest := s.URL; ; {
		i := strings.IndexByte(rest, '{')
		var name, after string
		found := false
		if i >= 0 {
			name, after, found = strings.Cut(rest[i+1:], "}")
		}
		if !found {
			sv.text = append(sv.text, rest)
			break
		}
		sv.text = append(sv.text, rest[:i])
		advance(rest[:i])
		rest = after
		if index == nil {
			index = map[string]int{}
		}
		j, seen := index[name]
		if !seen {
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
		sv.vars = append(sv.vars, urlVar{j, authority || !path && !slash && strings.HasPrefix(rest, ":")})
	}
	// Only a defect no value can repair makes the server unusable for good:
	// a query or fragment in its text, or userinfo in its authority.
	literal := sv.substitute(func(urlVar) string { return "x" })
	_, rest, absolute := strings.Cut(literal, "://")
	host, _, _ := strings.Cut(rest, "/")
	if len(sv.vars) == 0 {
		if _, err := d.resolveServerURL(literal); err != nil {
			s.Err = fmt.Errorf("server URL %q cannot be used: %w", s.URL, err)
		}
	} else if strings.ContainsAny(literal, "?#") || absolute && strings.Contains(host, "@") ||
		strings.HasPrefix(sv.text[0], "/") && !d.httpBase() {
		s.Err = fmt.Errorf("server URL %q cannot be used whatever its variables' values", s.URL)
	}
	if s.Err == nil && !slices.ContainsFunc(s.Variables, func(v Variable) bool { return !v.DefaultSet }) {
		if ep, err := d.resolveServerURL(sv.substitute(func(v urlVar) string { return s.Variables[v.index].Default })); err == nil {
			sv.fixed = &ep
		}
	}
	return sv
}

// substitute returns the server URL with each variable replaced by value.
func (s *server) substitute(value func(urlVar) string) string {
	var b strings.Builder
	for i, t := range s.text {
		b.WriteString(t)
		if i < len(s.vars) {
			b.WriteString(value(s.vars[i]))
		}
	}
	return b.String()
}

// templateNames returns the names of the variables in a server URL.
func templateNames(u string) []string {
	var names []string
	for {
		i := strings.IndexByte(u, '{')
		if i < 0 {
			return names
		}
		name, after, found := strings.Cut(u[i+1:], "}")
		if !found {
			return names
		}
		names, u = append(names, name), after
	}
}
