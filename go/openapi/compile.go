package openapi

import (
	"errors"
	"fmt"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// An operation is an indexed operation: its descriptor, which compile
// completes on first use, and the plan its calls follow.
type operation struct {
	Operation
	doc      *document
	node     *node   // the Operation Object; nil for a Paths entry that cannot be read
	ptr      string  // the Operation Object's JSON Pointer
	levels   []level // the Path Item chain
	conflict error   // the method is defined on both sides of a Path Item $ref
	once     sync.Once
	plan
}

// A plan is what a call of an operation needs, compiled once so that calls
// do no document work.
type plan struct {
	params    []param
	path      []pathPart
	servers   []*server
	usable    []*server // the servers without Err
	body      []media   // the request body's Media, parsed
	responses []response
	success   []media // the concrete media types of 2xx responses
}

// A param is a parameter with its serialization rules.
type param struct {
	*Param
	required bool
	field    string // the canonical field name of a header parameter
	name     string // the percent-encoded name of a query parameter
	todo     error  // why stage 1 cannot serialize it, or nil
}

// A pathPart is literal text of the path template, or one of its
// parameters.
type pathPart struct {
	text  string
	param int // an index into plan.params, or -1 for text
}

// A response is a declared response with its parsed media types.
type response struct {
	*Message
	status   int // an exact status code, or 0
	class    int // the class of a range such as "4XX", or 0
	fallback bool
	media    []media
}

// compile completes o's descriptor and plan once, and returns o.
func (o *operation) compile() *operation {
	if o.node != nil {
		o.once.Do(o.build)
	}
	return o
}

func (o *operation) build() {
	d, n, op, ptr := o.doc, o.node, &o.Operation, o.ptr
	var errs []error
	if o.conflict != nil {
		errs = append(errs, o.conflict)
	}
	op.Summary, op.Description = n.str("summary"), n.str("description")
	op.Tags, op.Deprecated = n.get("tags").strs(), n.flag("deprecated")

	params, paramsAt, err := o.itemField("parameters")
	if err != nil {
		errs = append(errs, err)
	}
	o.addParams(params, paramsAt, &errs)
	o.addParams(n.get("parameters"), ptr+"/parameters", &errs)
	o.assignKeys()

	if rb := n.get("requestBody"); rb != nil && op.Method != "TRACE" {
		var target *node
		op.Body, target = d.message(rb, ptr+"/requestBody")
		if target == nil {
			errs = append(errs, op.Body.Err)
		}
		op.Body.Required = target.flag("required")
		o.body = parseMedias(op.Body.Media)
	}
	if rs := n.get("responses"); rs != nil && rs.kind == '{' {
		for i := range rs.kids {
			key := rs.kids[i].key
			m, _ := d.message(&rs.kids[i], ptr+"/responses/"+escapeToken(key))
			m.Key = key
			o.addResponse(m)
		}
	}

	if s := n.get("servers"); s != nil && s.kind == '[' && len(s.kids) > 0 {
		o.servers = d.parseServers(s, ptr+"/servers")
	} else if s, at, err := o.itemField("servers"); s != nil && s.kind == '[' && len(s.kids) > 0 {
		if err != nil {
			errs = append(errs, err)
		}
		o.servers = d.parseServers(s, at)
	} else {
		o.servers = d.rootServers()
	}
	for _, s := range o.servers {
		op.Servers = append(op.Servers, s.Server)
		if s.Err == nil {
			o.usable = append(o.usable, s)
		}
	}

	sec := n.get("security")
	if sec == nil {
		sec = d.root.get("security")
	}
	if sec != nil && sec.kind == '[' {
		for i := range sec.kids {
			op.Security = append(op.Security, securityRequirement(&sec.kids[i]))
		}
	}

	if err := o.parsePath(); err != nil {
		errs = append(errs, err)
	}
	op.Err = errors.Join(errs...)
}

// itemField returns the Path Item field name, from whichever level of the
// chain defines it, with its pointer, and an error when several do.
func (o *operation) itemField(name string) (field *node, ptr string, err error) {
	for _, l := range o.levels {
		if m := l.n.get(name); m != nil {
			if field != nil {
				return field, ptr, fmt.Errorf("the Path Item and its $ref target both define %s", name)
			}
			field, ptr = m, l.ptr+"/"+name
		}
	}
	return field, ptr, nil
}

// addParams adds the parameters of list, one taking the place of an
// earlier one of the same name and location.
func (o *operation) addParams(list *node, ptr string, errs *[]error) {
	if list == nil || list.kind != '[' {
		return
	}
	for i := range list.kids {
		p := o.doc.param(&list.kids[i], ptr+"/"+strconv.Itoa(i))
		if p.In == "header" && slices.ContainsFunc([]string{"Accept", "Content-Type", "Authorization"}, func(s string) bool {
			return strings.EqualFold(s, p.Name)
		}) {
			continue
		}
		if p.In == "" && p.Err != nil {
			*errs = append(*errs, p.Err)
		}
		pp := param{Param: p, required: p.Required || p.In == "path"}
		switch {
		case p.In == "header":
			pp.field = textproto.CanonicalMIMEHeaderKey(p.Name)
		case p.In == "query":
			pp.name = escape(p.Name)
		}
		switch {
		case p.ContentType != "":
			pp.todo = notYet("content parameters")
		case p.In == "cookie":
			pp.todo = notYet("cookie parameters")
		case p.AllowReserved:
			pp.todo = notYet("allowReserved")
		case p.Style != "simple" && p.Style != "form" || p.Style == "form" && p.In != "query":
			pp.todo = notYet("the " + p.Style + " style")
		}
		j := slices.IndexFunc(o.params, func(q param) bool { return q.In == p.In && q.Name == p.Name && q.In != "" })
		if j < 0 {
			o.params = append(o.params, pp)
		} else {
			o.params[j] = pp
		}
	}
}

// assignKeys sets each parameter's Key and lists the parameters in
// Operation.Params.
func (o *operation) assignKeys() {
	for i := range o.params {
		p := o.params[i].Param
		o.Params = append(o.Params, p)
		if p.In == "" {
			continue
		}
		p.Key = p.Name
		shared := slices.ContainsFunc(o.params, func(q param) bool { return q.Param != p && q.Name == p.Name })
		loc, _, dotted := strings.Cut(p.Name, ".")
		if shared || p.Name == "" || strings.HasPrefix(p.Name, "/") || strings.HasPrefix(p.Name, "Input.Body") ||
			dotted && slices.Contains([]string{"path", "query", "header", "cookie", "querystring"}, loc) {
			p.Key = p.In + "." + p.Name
		}
	}
}

// param describes the Parameter Object n at ptr, following references.
func (d *document) param(n *node, ptr string) *Param {
	levels, err := d.chain(n, ptr)
	if err != nil {
		return &Param{Source: d.source(ptr), Err: err}
	}
	t := levels[len(levels)-1]
	p := &Param{
		Name:            t.n.str("name"),
		In:              t.n.str("in"),
		Description:     description(levels),
		Required:        t.n.flag("required"),
		Deprecated:      t.n.flag("deprecated"),
		AllowEmptyValue: t.n.flag("allowEmptyValue"),
		Source:          d.source(t.ptr),
		Schema:          d.schema(t.n.get("schema"), t.ptr+"/schema"),
	}
	if c := t.n.get("content"); c != nil && c.kind == '{' && len(c.kids) > 0 {
		p.ContentType = c.kids[0].key
		p.Schema = d.schema(c.kids[0].get("schema"), t.ptr+"/content/"+escapeToken(p.ContentType)+"/schema")
	} else {
		p.Style = t.n.str("style")
		switch {
		case p.Style != "":
		case p.In == "query" || p.In == "cookie":
			p.Style = "form"
		case p.In == "path" || p.In == "header":
			p.Style = "simple"
		}
		e := t.n.get("explode")
		p.ExplodeSet = e != nil
		p.Explode = e != nil && e.kind == 't' || e == nil && p.Style == "form"
	}
	p.AllowReserved = p.In == "query" && t.n.flag("allowReserved")
	if !slices.Contains([]string{"path", "query", "header", "cookie"}, p.In) {
		p.Err = fmt.Errorf("parameter location %q is not path, query, header or cookie", p.In)
	}
	return p
}

// description returns the description of the nearest level that gives
// one: in OpenAPI 3.1 a Reference Object's description replaces its
// target's.
func description(levels []level) string {
	for _, l := range levels {
		if s := l.n.get("description"); s != nil && s.kind == '"' {
			return s.text
		}
	}
	return ""
}

// message describes the Request Body or Response Object n at ptr,
// following references. The target is nil when a reference cannot be
// resolved; the Message then reports it in Err.
func (d *document) message(n *node, ptr string) (*Message, *node) {
	levels, err := d.chain(n, ptr)
	if err != nil {
		return &Message{Source: d.source(ptr), Err: err}, nil
	}
	t := levels[len(levels)-1]
	m := &Message{Description: description(levels), Source: d.source(t.ptr)}
	if c := t.n.get("content"); c != nil && c.kind == '{' {
		for i := range c.kids {
			typ := c.kids[i].key
			at := t.ptr + "/content/" + escapeToken(typ)
			md := &Media{Type: typ, Source: d.source(at), Schema: d.schema(c.kids[i].get("schema"), at+"/schema")}
			if mt, ok := parseMedia(typ); !ok {
				md.Err = fmt.Errorf("invalid media type %q", typ)
			} else {
				md.Sequential = mt.class() == sequentialClass || strings.EqualFold(mt.typ, "multipart")
			}
			m.Media = append(m.Media, md)
		}
	}
	return m, t.n
}

func (o *operation) addResponse(m *Message) {
	r := response{Message: m, media: parseMedias(m.Media)}
	key := m.Key
	switch {
	case key == "default":
		r.fallback = true
	case len(key) == 3 && key[0] >= '1' && key[0] <= '5' && key[1:] == "XX":
		r.class = int(key[0] - '0')
	case len(key) == 3 && key[0] >= '1' && key[0] <= '5' && key[1] >= '0' && key[1] <= '9' && key[2] >= '0' && key[2] <= '9':
		r.status, _ = strconv.Atoi(key)
	default:
		if m.Err == nil {
			m.Err = fmt.Errorf("response key %q is not a status code, a range such as 4XX, or default", key)
		}
	}
	o.Responses = append(o.Responses, m)
	o.responses = append(o.responses, r)
	if r.status/100 == 2 || r.class == 2 {
		for _, mt := range r.media {
			if mt.concrete() {
				o.success = append(o.success, mt)
			}
		}
	}
}

// declaration returns the response that governs status, or nil.
func (pl *plan) declaration(status int) *response {
	var class, fallback *response
	for i := range pl.responses {
		r := &pl.responses[i]
		switch {
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

// parsePath splits the path template into text and parameters.
func (o *operation) parsePath() error {
	rest := o.Path
	for rest != "" {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			o.path = append(o.path, pathPart{rest, -1})
			break
		}
		name, after, ok := strings.Cut(rest[i+1:], "}")
		if !ok {
			return fmt.Errorf("path template %q has an unclosed {", o.Path)
		}
		j := slices.IndexFunc(o.params, func(p param) bool { return p.In == "path" && p.Name == name })
		if j < 0 {
			return fmt.Errorf("path template %q names {%s}, which no path parameter declares", o.Path, name)
		}
		o.path = append(o.path, pathPart{rest[:i], -1}, pathPart{param: j})
		rest = after
	}
	return nil
}

// securityRequirement describes the Security Requirement Object n.
func securityRequirement(n *node) SecurityRequirement {
	var r SecurityRequirement
	type entry struct {
		name   string
		scopes []string
	}
	var entries []entry
	for _, k := range n.kids {
		name, scopes := k.key, k.strs()
		r.Schemes = append(r.Schemes, SecurityScheme{Name: name, Scopes: scopes})
		sorted := slices.Compact(slices.Sorted(slices.Values(scopes)))
		entries = append(entries, entry{name, sorted})
	}
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.name, b.name) })
	var b strings.Builder
	b.WriteByte('{')
	for i, e := range entries {
		if i > 0 {
			b.WriteByte(',')
		}
		canonicalString(&b, e.name)
		b.WriteString(":[")
		for j, s := range e.scopes {
			if j > 0 {
				b.WriteByte(',')
			}
			canonicalString(&b, s)
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
		switch c := s[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(lowerHex[c>>4])
				b.WriteByte(lowerHex[c&15])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}

// A server is a server an operation may be sent to, with its URL template.
type server struct {
	*Server
	parts []string  // the URL's text and variable names, alternating
	fixed *endpoint // the URL with every variable at its default, if usable
}

// An endpoint is a usable server URL, its variables substituted.
type endpoint struct {
	scheme, host, path string // path escaped
}

func (d *document) rootServers() []*server {
	d.serversOnce.Do(func() {
		if s := d.root.get("servers"); s != nil && s.kind == '[' && len(s.kids) > 0 {
			d.servers = d.parseServers(s, "/servers")
		} else {
			d.servers = []*server{d.newServer(&Server{ID: "default", URL: "/"}, nil)}
		}
	})
	return d.servers
}

func (d *document) parseServers(list *node, ptr string) []*server {
	servers := make([]*server, 0, len(list.kids))
	for i := range list.kids {
		n := &list.kids[i]
		at := ptr + "/" + strconv.Itoa(i)
		s := &Server{ID: d.source(at), URL: n.str("url"), Name: n.str("name"), Description: n.str("description"), Source: d.source(at)}
		servers = append(servers, d.newServer(s, n.get("variables")))
	}
	return servers
}

// newServer completes s from its URL template and declared variables.
func (d *document) newServer(s *Server, vars *node) *server {
	sv := &server{Server: s}
	rest := s.URL
	for {
		i := strings.IndexByte(rest, '{')
		j := strings.IndexByte(rest[max(i, 0):], '}')
		if i < 0 || j < 0 {
			sv.parts = append(sv.parts, rest)
			break
		}
		name := rest[i+1 : i+j]
		sv.parts = append(sv.parts, rest[:i], name)
		rest = rest[i+j+1:]
		if slices.ContainsFunc(s.Variables, func(v Variable) bool { return v.Name == name }) {
			continue
		}
		v := Variable{Name: name}
		if n := vars.get(name); n != nil {
			v.Declared, v.Description = true, n.str("description")
			v.Default, v.DefaultSet = n.str("default"), n.get("default") != nil
			if e := n.get("enum"); e != nil && e.kind == '[' {
				v.Enum = e.strs()
			}
		}
		s.Variables = append(s.Variables, v)
	}
	// The document alone decides a server is unusable when its URL is, whatever
	// values its variables take.
	if _, err := d.endpoint(sv.substitute(func(Variable) string { return "x" })); err != nil {
		s.Err = fmt.Errorf("server URL %q cannot be used: %w", s.URL, err)
		return sv
	}
	if !slices.ContainsFunc(s.Variables, func(v Variable) bool { return !v.DefaultSet }) {
		if ep, err := d.endpoint(sv.substitute(func(v Variable) string { return v.Default })); err == nil {
			sv.fixed = &ep
		}
	}
	return sv
}

// substitute returns the server URL with each variable replaced by value.
func (s *server) substitute(value func(Variable) string) string {
	var b strings.Builder
	for i, part := range s.parts {
		if i%2 == 0 {
			b.WriteString(part)
		} else {
			b.WriteString(value(s.Variables[slices.IndexFunc(s.Variables, func(v Variable) bool { return v.Name == part })]))
		}
	}
	return b.String()
}
