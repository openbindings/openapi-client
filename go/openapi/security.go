package openapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// A scheme is a Security Scheme Object compiled: its description, and where
// a credential for it goes.
type scheme struct {
	desc    SecurityScheme // without the Name and Scopes of a use
	kind    schemeKind
	dest    paramID // the header field, query name or cookie name a credential sets
	written string  // a query or cookie credential's name as written
	auth    string  // the auth-scheme and space before a credential in the Authorization field
}

type schemeKind uint8

const (
	unusable schemeKind = iota // defective or undeclared: only FromTransport satisfies it
	mutualTLS
	apiKeyHeader
	apiKeyQuery
	apiKeyCookie
	httpBearer // http bearer, oauth2 and openIdConnect
	httpBasic
	httpOther
)

var authorization = paramID{"header", "Authorization"}

// securityScheme returns the scheme a security requirement in the document
// from names, compiled once for each declaration: the component of that name
// where the Loader's SchemeLookup looks, following references.
func (d *document) securityScheme(name string, from *tree) *scheme {
	v := d.schemeComponent(name, from)
	if !v.ok() {
		if from.edition == 32 {
			t, ptr, err := d.targetName(from, name)
			if err != nil {
				return &scheme{desc: SecurityScheme{Err: err}}
			}
			v = t
			return d.schemeForms.get(v.id(), func() *scheme {
				t, at, _, err := d.follow(v, v.t.source(ptr))
				if err != nil {
					return &scheme{desc: SecurityScheme{Err: err}}
				}
				return newScheme(t, at)
			})
		}
		return &scheme{desc: SecurityScheme{Err: fmt.Errorf("the document declares no security scheme %q", name)}}
	}
	return d.schemeNames.get(v.id(), func() *scheme {
		prefix := "/components/securitySchemes/"
		if v.t.edition == 20 {
			prefix = "/securityDefinitions/"
		}
		src := v.t.source(prefix + escapeToken(name))
		t, at, desc, err := d.follow(v, src)
		switch {
		case err != nil:
			return &scheme{desc: SecurityScheme{Source: src, Err: err}}
		case t == v: // only this name reaches it
			return newScheme(t, at)
		}
		sc := d.schemeForms.get(t.id(), func() *scheme { return newScheme(t, at) })
		if desc != sc.desc.Description {
			c := *sc
			c.desc.Description = desc
			sc = &c
		}
		return sc
	})
}

func (d *document) schemeComponent(name string, from *tree) value {
	in := [...]*tree{d.tree, from}
	switch d.schemes {
	case SchemesInEntry:
		in[1] = d.tree
	case SchemesInReferrer:
		in[0] = from
	}
	for _, t := range in {
		v := t.root().get("components").get("securitySchemes").get(name)
		if t.edition == 20 {
			v = t.root().get("securityDefinitions").get(name)
		}
		if v.ok() {
			return v
		}
	}
	return value{}
}

// newScheme compiles the Security Scheme Object t, whose Source is at.
func newScheme(t value, at string) *scheme {
	sc := &scheme{desc: SecurityScheme{Source: at}}
	s := &sc.desc
	if t.kind() != '{' {
		s.Err = errors.New("a Security Scheme Object must be an object")
		return sc
	}
	s.Type, s.Description = t.str("type"), t.str("description")
	if t.t.edition == 32 {
		s.Deprecated, s.OAuth2MetadataURL = t.flag("deprecated"), t.str("oauth2MetadataUrl")
	}
	var err error
	switch s.Type {
	case "apiKey":
		s.In, s.ParamName = t.str("in"), t.str("name")
		name, field := s.ParamName, textproto.CanonicalMIMEHeaderKey(s.ParamName)
		switch {
		case name == "":
			err = errors.New("an apiKey scheme needs a name")
		case s.In == "header" && (!isToken(name) || field == "Content-Type" || field == "Cookie" || slices.Contains(derivedFields, field)):
			err = fmt.Errorf("an apiKey cannot be sent in the header field %q", name)
		case s.In == "header":
			sc.kind, sc.dest = apiKeyHeader, paramID{"header", field}
		case s.In == "query":
			sc.kind, sc.dest, sc.written = apiKeyQuery, paramID{"query", name}, escape(name, unreservedSet)
		case s.In == "cookie" && isToken(name): // RFC 6265 section 4.1.1
			sc.kind, sc.dest, sc.written = apiKeyCookie, paramID{"cookie", name}, name
		case s.In == "cookie":
			err = fmt.Errorf("cookie name %q is not a token (RFC 6265 section 4.1.1)", name)
		default:
			err = fmt.Errorf("apiKey location %q is not query, header or cookie", s.In)
		}
	case "basic":
		if t.t.edition == 20 {
			s.Type, s.Scheme, sc.kind, sc.dest, sc.auth = "http", "basic", httpBasic, authorization, "Basic "
		} else {
			err = errors.New("basic is a Swagger security type")
		}
	case "http":
		s.Scheme, s.BearerFormat = t.str("scheme"), t.str("bearerFormat")
		sc.dest = authorization
		switch {
		case !isToken(s.Scheme):
			err = fmt.Errorf("http scheme %q is not an auth-scheme (RFC 9110 section 11.1)", s.Scheme)
		case strings.EqualFold(s.Scheme, "bearer"):
			sc.kind, sc.auth = httpBearer, "Bearer "
		case strings.EqualFold(s.Scheme, "basic"):
			sc.kind, sc.auth = httpBasic, "Basic "
		default:
			sc.kind, sc.auth = httpOther, s.Scheme+" "
		}
	case "oauth2":
		if t.t.edition == 20 {
			typ := t.str("flow")
			switch typ {
			case "application":
				typ = "clientCredentials"
			case "accessCode":
				typ = "authorizationCode"
			}
			s.Flows, err = appendFlow(nil, typ, t)
		} else {
			s.Flows, err = oauthFlows(t.get("flows"))
		}
		sc.kind, sc.dest, sc.auth = httpBearer, authorization, "Bearer "
	case "openIdConnect":
		u := t.get("openIdConnectUrl")
		if u.kind() != '"' {
			err = errors.New("an openIdConnect scheme needs an openIdConnectUrl")
		}
		s.OpenIDConnectURL = u.string()
		sc.kind, sc.dest, sc.auth = httpBearer, authorization, "Bearer "
	case "mutualTLS":
		sc.kind = mutualTLS
	default:
		err = fmt.Errorf("security scheme type %q is not apiKey, http, mutualTLS, oauth2 or openIdConnect", s.Type)
	}
	if err != nil {
		*sc = scheme{desc: sc.desc} // unusable
		sc.desc.Err = err
	}
	return sc
}

// oauthFlows describes the flows of the OAuth Flows Object v that are
// objects, in document order, with the fields they have, and says why v is
// defective: a flow that is not an OAuth Flow Object, or that lacks a URL
// its type requires, as a string, or its map of scopes (OpenAPI 3.1 section
// 4.8.29).
func oauthFlows(v value) (flows []Flow, err error) {
	if v.kind() != '{' {
		return nil, errors.New("an oauth2 scheme needs flows")
	}
	for typ, f := range v.members() {
		if typ == "deviceAuthorization" && v.t.edition != 32 {
			continue
		}
		var e error
		flows, e = appendFlow(flows, typ, f)
		if err == nil {
			err = e
		}
	}
	return flows, err
}

func appendFlow(flows []Flow, typ string, f value) ([]Flow, error) {
	var needs []string
	switch typ {
	case "implicit":
		needs = []string{"authorizationUrl"}
	case "password", "clientCredentials":
		needs = []string{"tokenUrl"}
	case "authorizationCode":
		needs = []string{"authorizationUrl", "tokenUrl"}
	case "deviceAuthorization":
		needs = []string{"deviceAuthorizationUrl", "tokenUrl"}
	default:
		return flows, nil
	}
	var err error
	scopes := f.get("scopes")
	if f.kind() != '{' || scopes.kind() != '{' || slices.ContainsFunc(needs, func(n string) bool { return f.get(n).kind() != '"' }) {
		err = fmt.Errorf("the %s flow needs %s and scopes", typ, strings.Join(needs, " and "))
	}
	if f.kind() != '{' {
		return flows, err
	}
	flow := Flow{Type: typ, AuthorizationURL: f.str("authorizationUrl"), TokenURL: f.str("tokenUrl"), RefreshURL: f.str("refreshUrl")}
	if f.t.edition == 32 {
		flow.DeviceAuthorizationURL = f.str("deviceAuthorizationUrl")
	}
	if scopes.kind() == '{' {
		flow.Scopes = map[string]string{}
		for name, desc := range scopes.members() {
			flow.Scopes[name] = desc.string()
		}
	}
	return append(flows, flow), err
}

// An alternative is a security alternative with its schemes compiled.
type alternative struct {
	*SecurityRequirement
	schemes []*scheme // each of Schemes
	clash   bool      // two schemes set one header field, query name or cookie name
}

// A securityPlan is a security value compiled: its descriptions, and the
// alternatives, which share them; the header fields, query names and cookie
// names their schemes set; and why the value is not an array of Security
// Requirement Objects mapping names to arrays of strings.
type securityPlan struct {
	reqs  []SecurityRequirement
	alts  []alternative // identical ones as one, as the key selects the first (see SecurityRequirement.Key)
	dests map[paramID]bool
	err   error
}

// compileSecurity compiles the security value list.
func (d *document) compileSecurity(list value) securityPlan {
	if list.kind() != '[' {
		return securityPlan{err: errSecurityValue}
	}
	var p securityPlan
	for _, r := range list.members() {
		if r.kind() != '{' {
			return securityPlan{err: errSecurityValue}
		}
		var req SecurityRequirement
		var a alternative
		for name, scopes := range r.members() {
			roles, ok := stringList(scopes)
			if !ok {
				return securityPlan{err: errSecurityValue}
			}
			sc := d.securityScheme(name, list.t)
			s := sc.desc
			s.Name, s.Scopes = name, roles
			req.Schemes, a.schemes = append(req.Schemes, s), append(a.schemes, sc)
			if sc.dest.in != "" {
				if p.dests == nil {
					p.dests = map[paramID]bool{}
				}
				p.dests[sc.dest] = true
			}
		}
		req.Key = requirementKey(req.Schemes)
		_, j := clash(a.schemes, nil)
		a.clash = j > 0
		p.reqs, p.alts = append(p.reqs, req), append(p.alts, a)
	}
	for i := range p.alts {
		p.alts[i].SecurityRequirement = &p.reqs[i]
	}
	if len(p.alts) > 1 && !slices.ContainsFunc(p.alts[1:], func(a alternative) bool { return a.Key != p.alts[0].Key }) {
		p.alts = p.alts[:1]
	}
	return p
}

var errSecurityValue = errors.New("security is not an array of Security Requirement Objects mapping scheme names to arrays of strings")

// stringList returns the strings of v, and whether v is an array of
// strings.
func stringList(v value) ([]string, bool) {
	if v.kind() != '[' {
		return nil, false
	}
	var s []string
	for _, item := range v.members() {
		if item.kind() != '"' {
			return nil, false
		}
		s = append(s, item.text())
	}
	return s, true
}

// requirementKey returns the SecurityRequirement.Key of an alternative
// with schemes.
func requirementKey(schemes []SecurityScheme) string {
	order := make([]int, len(schemes))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(i, j int) int { return strings.Compare(schemes[i].Name, schemes[j].Name) })
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range order {
		if i > 0 {
			b.WriteByte(',')
		}
		canonicalString(&b, schemes[k].Name)
		b.WriteString(":[")
		for j, scope := range slices.Compact(slices.Sorted(slices.Values(schemes[k].Scopes))) {
			if j > 0 {
				b.WriteByte(',')
			}
			canonicalString(&b, scope)
		}
		b.WriteByte(']')
	}
	b.WriteByte('}')
	return b.String()
}

// clash returns the positions of the first two of schemes, but those skip
// names, that set the same header field, query name or cookie name, or 0
// and 0 when no two do.
func clash(schemes []*scheme, skip func(int) bool) (i, j int) {
	if len(schemes) < 2 {
		return 0, 0
	}
	seen := make(map[paramID]int, len(schemes))
	for j, sc := range schemes {
		if sc.dest.in == "" || skip != nil && skip(j) {
			continue
		}
		if i, ok := seen[sc.dest]; ok {
			return i, j
		}
		seen[sc.dest] = j
	}
	return 0, 0
}

// A selection is the security alternative a call applies, or nil for none,
// and whether the client places credentials of it.
type selection struct {
	alt    *alternative
	places bool
}

// key returns the Key of the alternative, or "" for none.
func (s selection) key() string {
	if s.alt == nil {
		return ""
	}
	return s.alt.Key
}

// selectSecurity returns the alternative the call applies, or nil for none,
// recording why none can be chosen.
func (c *Client) selectSecurity(o *operation, in *Input, re *RequestError) *alternative {
	alts, cfg := o.security, c.cfg
	find := func(key string) *alternative {
		for i := range alts {
			if alts[i].Key == key {
				return &alts[i]
			}
		}
		return nil
	}
	switch {
	case in.Security != "":
		a := find(in.Security)
		if a == nil && (in.Security != "{}" || len(alts) > 0) {
			re.setting("Input.Security", errors.New("the operation does not offer this security alternative"))
		}
		return a
	case cfg.SecurityKey != "":
		if a := find(cfg.SecurityKey); a != nil {
			return a
		}
	case cfg.Security != nil:
		var match *alternative
		for i := range alts {
			switch a := &alts[i]; {
			case !cfg.hasSecurity(a.Schemes):
			case match == nil:
				match = a
			case match.Key != a.Key:
				re.setting("Options.Security", errors.New("several of the operation's security alternatives have these schemes, with different scopes; select one with Options.SecurityKey or Input.Security"))
				return nil
			}
		}
		if match != nil {
			return match
		}
	}
	switch len(alts) {
	case 0:
		return nil
	case 1:
		return &alts[0]
	}
	re.setting("Options.Security", fmt.Errorf("the operation offers %d security alternatives; select one with Options.Security, Options.SecurityKey or Input.Security", len(alts)))
	return nil
}

// hasSecurity reports whether schemes are exactly those Options.Security
// names.
func (cfg *config) hasSecurity(schemes []SecurityScheme) bool {
	return len(schemes) == len(cfg.securityNames) &&
		!slices.ContainsFunc(schemes, func(s SecurityScheme) bool { return !cfg.securityNames[s.Name] })
}

// checkCredentials checks the credentials of alternative a for a call to
// ep, recording why they cannot be used. It reports whether the client
// places any of them, and returns the parameters whose destinations they
// set, as flags by position, or nil for none.
func (c *Client) checkCredentials(o *operation, a *alternative, in *Input, ep endpoint, re *RequestError) (places bool, supplied []bool) {
	cfg := c.cfg
	checked := ep.scheme == "" // no server resolved: nothing to check
	for i, sc := range a.schemes {
		name := a.Schemes[i].Name
		cred := cfg.Credentials[name]
		if err := sc.callError(cred); err != nil {
			re.setting(credentialKey(name), err)
			continue
		}
		if !cred.places() {
			continue
		}
		places = true
		if (sc.kind == httpBearer || sc.kind == httpBasic) && !checked {
			checked = true
			if err := secured(&url.URL{Scheme: ep.scheme, Host: ep.host}); err != nil {
				re.fail(err)
			}
		}
		if sc.kind != apiKeyQuery {
			field := sc.dest.name
			if sc.kind == apiKeyCookie {
				field = "Cookie"
			}
			if s := setter(in.Header, cfg.Header, field); s != "" {
				re.setting(s, fmt.Errorf("sets %s, which the credential for %q sets", field, name))
			}
		}
		if j, ok := o.dests[sc.dest]; ok {
			if supplied == nil {
				supplied = make([]bool, len(o.params))
			}
			supplied[j] = true
		}
	}
	if a.clash {
		if i, j := clash(a.schemes, func(i int) bool { return !cfg.Credentials[a.Schemes[i].Name].places() }); j > 0 {
			dest := a.schemes[i].dest
			re.fail(fmt.Errorf("the security schemes %q and %q both set %s %q", a.Schemes[i].Name, a.Schemes[j].Name, dest.in, dest.name))
		}
	}
	return places, supplied
}

var (
	errMutualTLS   = errors.New("a mutualTLS scheme takes no credential but FromTransport")
	errNotBasic    = errors.New("a Basic credential is for an http basic scheme")
	errBasicValue  = errors.New("an http basic credential is a user-id without a colon, a colon and a password, neither holding a control character (RFC 7617 section 2)")
	errFieldValue  = errors.New("a header field cannot carry the credential: it holds a control character other than a tab, or leading or trailing whitespace")
	errCookieValue = errors.New(`a cookie cannot carry the credential: it holds a ";", a control character, or leading or trailing whitespace`)
)

// callError returns why a call refuses c for sc, or nil.
func (sc *scheme) callError(c Credential) error {
	switch {
	case c.kind == transportCredential:
		return nil
	case sc.kind == unusable:
		return fmt.Errorf("only FromTransport can satisfy the scheme: %w", sc.desc.Err)
	case sc.kind == mutualTLS && c.kind != noCredential:
		return errMutualTLS
	case sc.kind == mutualTLS:
		return nil
	case c.kind == noCredential:
		return errors.New("the selected security alternative needs a credential for the scheme")
	case c.basic() && sc.kind != httpBasic:
		return errNotBasic
	case c.kind == badBasicCredential:
		return errBasicValue
	case c.kind != sourceCredential:
		return sc.checkValue(c.secret())
	}
	return nil
}

// loadError returns why Load refuses c for a name whose scheme is sc, or
// nil: every problem but a source's value, which only a call has.
func (sc *scheme) loadError(c Credential) error {
	if c.kind == noCredential {
		return errors.New("the credential is empty")
	}
	return sc.callError(c)
}

// checkValue returns why a credential for sc cannot carry secret, or nil.
func (sc *scheme) checkValue(secret string) error {
	switch sc.kind {
	case apiKeyHeader, httpBearer, httpOther:
		if !validFieldValue(secret) {
			return errFieldValue
		}
	case httpBasic:
		if !strings.Contains(secret, ":") || strings.ContainsFunc(secret, isCTL) {
			return errBasicValue
		}
	case apiKeyCookie:
		if !validFieldValue(secret) || strings.ContainsAny(secret, ";\t") {
			return errCookieValue
		}
	}
	return nil
}

// credentialKey is the Settings key of the credential for name.
func credentialKey(name string) string { return "Options.Credentials[" + strconv.Quote(name) + "]" }

// secured returns why a bearer token or Basic credential cannot be sent to
// u, or nil: it goes over https or wss, or plain http or ws to a loopback
// host, where it does not leave the machine (RFC 6750 section 5.3, RFC 7617
// section 4).
func secured(u *url.URL) error {
	switch u.Scheme {
	case "https", "wss":
		return nil
	case "http", "ws":
		if loopback(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("a bearer token or Basic credential cannot be sent over plain %s to a host other than a loopback one; use https, or place it with FromTransport", u.Scheme)
	}
	return fmt.Errorf("a bearer token or Basic credential sent over %q needs FromTransport", u.Scheme)
}

// loopback reports whether host is a loopback IP address, an IPv4-mapped
// one included, or the name localhost written exactly so, the one name
// net/http's proxy settings never apply to; it is matched without
// resolving.
func loopback(host string) bool {
	ip, err := netip.ParseAddr(host)
	return host == "localhost" || err == nil && ip.IsLoopback()
}

// sameOrigin reports whether a and b have the same origin: scheme, host and
// port, a scheme's default port being the same as none (RFC 6454 section
// 4). Only ASCII letters compare without regard to case (RFC 3986 section
// 6.2.2.1), so two hosts are never one origin.
func sameOrigin(a, b *url.URL) bool {
	return equalFoldASCII(a.Scheme, b.Scheme) && equalFoldASCII(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

// equalFoldASCII reports whether a and b are equal, ASCII letters compared
// without regard to case and every other byte exactly.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if c, d := a[i]|0x20, b[i]|0x20; a[i] != b[i] && (c != d || c < 'a' || c > 'z') { // 0x20 lowers a letter
			return false
		}
	}
	return true
}

// defaultPorts are the default ports of the schemes requests go to.
var defaultPorts = map[string]string{"http": "80", "ws": "80", "https": "443", "wss": "443"}

// port returns u's port, or its scheme's default.
func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	return defaultPorts[strings.ToLower(u.Scheme)]
}

// sign returns req as it is sent: with the cookies the HTTPClient's jar
// has for it and, when creds is set, the credentials of the call's
// alternative, calling their sources with src. That is a copy of req, or req
// itself when nothing is added. Why the credentials cannot be placed is
// recorded in re, the setting's key naming the scheme.
func (x *exchange) sign(req *http.Request, src context.Context, creds bool, re *RequestError) *http.Request {
	places := creds && x.places
	var jarCookies []*http.Cookie
	if x.cfg.jar != nil && req.URL != nil {
		jarCookies = x.cfg.jar.Cookies(req.URL) // a jar keys cookies by scheme, host and path, which credentials leave
	}
	if !places && len(jarCookies) == 0 {
		return req
	}
	s := new(http.Request)
	*s = *req
	if s.Header = maps.Clone(req.Header); s.Header == nil {
		s.Header = http.Header{}
	}
	for _, c := range jarCookies {
		s.AddCookie(c) // after the request's own cookies, as net/http adds them
	}
	if !places {
		return s
	}
	// A header or cookie credential replaces every spelling of its field
	// (RFC 9110 section 5.1), other spellings of Cookie joining its pairs.
	h := s.Header
	var spelled []string
	for k, vs := range h {
		if ck := textproto.CanonicalMIMEHeaderKey(k); ck != k && x.sets(ck) {
			if ck == "Cookie" {
				spelled = append(spelled, vs...)
			}
			delete(h, k)
		}
	}
	a := x.alt
	var query, qnames, cookies, cnames []string // the pairs placed, and their names
	checked := false                            // the plain-http rule
	for i, sc := range a.schemes {
		name := a.Schemes[i].Name
		c := x.cfg.Credentials[name]
		if !c.places() {
			continue
		}
		if (sc.kind == httpBearer || sc.kind == httpBasic) && !checked {
			checked = true
			if err := secured(req.URL); err != nil {
				re.fail(err)
				return req
			}
		}
		secret, err := c.source(src)
		switch {
		case err != nil:
			re.fail(withContext(src, fmt.Errorf("the credential source for %q: %w", name, err)))
			continue
		case c.kind != sourceCredential: // checked when the call was prepared
		case secret == "":
			err = errors.New("the credential source returned an empty secret")
		default:
			err = sc.checkValue(secret)
		}
		if err != nil {
			re.setting(credentialKey(name), err)
			continue
		}
		switch sc.kind {
		case apiKeyQuery:
			query, qnames = append(query, sc.written+"="+escape(secret, unreservedSet)), append(qnames, sc.dest.name)
		case apiKeyCookie:
			cookies, cnames = append(cookies, sc.written+"="+secret), append(cnames, sc.dest.name)
		case httpBasic:
			h[sc.dest.name] = []string{sc.auth + base64.StdEncoding.EncodeToString([]byte(secret))}
		default:
			h[sc.dest.name] = []string{sc.auth + secret}
		}
	}
	if re.Err != nil || len(re.Settings) > 0 {
		return req
	}
	if cookies != nil {
		h["Cookie"] = []string{withCookies(strings.Join(append(slices.Clip(h["Cookie"]), spelled...), "; "), isOneOf(cnames), cookies)}
	}
	if query != nil {
		u := *req.URL
		u.RawQuery = withQuery(u.RawQuery, isOneOf(qnames), query)
		s.URL = &u
	}
	return s
}

// sets reports whether a credential the call places sets the header field.
func (x *exchange) sets(field string) bool {
	for i, sc := range x.alt.schemes {
		if (sc.dest.in == "header" && sc.dest.name == field || sc.kind == apiKeyCookie && field == "Cookie") &&
			x.cfg.Credentials[x.alt.Schemes[i].Name].places() {
			return true
		}
	}
	return false
}

// queryNames returns the names of the query credentials the call places.
func (x *exchange) queryNames() []string {
	if !x.places {
		return nil
	}
	var names []string
	for i, sc := range x.alt.schemes {
		if sc.kind == apiKeyQuery && x.cfg.Credentials[x.alt.Schemes[i].Name].places() {
			names = append(names, sc.dest.name)
		}
	}
	return names
}

// isOneOf returns a test of whether a name is one of names.
func isOneOf(names []string) func(string) bool {
	if len(names) == 1 {
		return func(n string) bool { return n == names[0] }
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return func(n string) bool { return set[n] }
}

// withQuery returns the query q less each pair whose name, decoded as
// application/x-www-form-urlencoded, drop reports, followed by add; every
// other pair is kept as written, empty ones included.
func withQuery(q string, drop func(string) bool, add []string) string {
	var b strings.Builder
	b.Grow(len(q) + 32*len(add))
	n := 0 // the pairs written
	if q != "" {
		for s := range strings.SplitSeq(q, "&") {
			name, _, _ := strings.Cut(s, "=")
			if strings.ContainsAny(name, "%+") {
				if d, err := url.QueryUnescape(name); err == nil {
					name = d
				}
			}
			if !drop(name) {
				if n++; n > 1 {
					b.WriteByte('&')
				}
				b.WriteString(s)
			}
		}
	}
	for _, p := range add {
		if n++; n > 1 {
			b.WriteByte('&')
		}
		b.WriteString(p)
	}
	return b.String()
}

// withCookies returns the cookie pairs of list, without surrounding
// whitespace, less the empty ones and each whose name drop reports,
// followed by add, joined by "; ".
func withCookies(list string, drop func(string) bool, add []string) string {
	var b strings.Builder
	b.Grow(len(list) + 32*len(add))
	for s := range strings.SplitSeq(list, ";") {
		s = strings.Trim(s, " \t")
		if name, _, _ := strings.Cut(s, "="); s != "" && !drop(strings.TrimRight(name, " \t")) {
			if b.Len() > 0 {
				b.WriteString("; ")
			}
			b.WriteString(s)
		}
	}
	for _, p := range add {
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(p)
	}
	return b.String()
}

// redact returns err, from the http.Client, with the URL a *url.Error names
// replaced by u, the request's URL without the credentials the client
// placed, redacted as net/http does.
func redact(err error, u *url.URL) error {
	ue := err.(*url.Error) // as http.Client.Do returns every error
	return &url.Error{Op: ue.Op, URL: u.Redacted(), Err: ue.Err}
}

// A securityCheck is Load's check of the security settings: what it has not
// yet found among the requirements of the document.
type securityCheck struct {
	d       *document
	re      *RequestError
	creds   map[string]Credential // those no scheme their name selects has yet taken
	refused map[string]error      // why the first scheme their name selects, if any, refuses each of creds
	names   map[string]bool       // Options.Security, until an alternative has these schemes
	key     string                // Options.SecurityKey, until an alternative has it
}

func (s *securityCheck) done() bool { return len(s.creds) == 0 && s.names == nil && s.key == "" }

// list checks the Security Requirement Objects of list, reporting whether
// it holds none, which makes an operation that has it credential-free.
func (s *securityCheck) list(list value) (none bool) {
	none = true
	for _, r := range list.members() {
		if list.kind() != '[' || r.kind() != '{' {
			continue
		}
		none = false
		var schemes []SecurityScheme // for the key
		n, named := 0, 0             // the schemes, and those Options.Security names
		for name, scopes := range r.members() {
			n++
			if c, ok := s.creds[name]; ok { // the scheme a name selects may differ between documents (see SchemeLookup)
				if err := s.d.securityScheme(name, list.t).loadError(c); err == nil {
					delete(s.creds, name)
				} else if _, seen := s.refused[name]; !seen {
					if s.refused == nil {
						s.refused = map[string]error{}
					}
					s.refused[name] = err
				}
			}
			if s.names[name] {
				named++
			}
			if s.key != "" {
				schemes = append(schemes, SecurityScheme{Name: name, Scopes: scopes.strs()})
			}
		}
		if s.names != nil && named == n && n == len(s.names) {
			s.names = nil
		}
		if s.key != "" && requirementKey(schemes) == s.key {
			s.key = ""
		}
	}
	return none
}

// refuse records the settings no requirement of the document can use.
func (s *securityCheck) refuse() {
	for name := range s.creds {
		err := s.refused[name]
		if err == nil {
			err = errors.New("no security requirement of the document uses this name")
		}
		s.re.setting(credentialKey(name), err)
	}
	if s.names != nil {
		s.re.setting("Options.Security", errors.New("no security alternative of the document has exactly these schemes"))
	}
	if s.key != "" {
		s.re.setting("Options.SecurityKey", errors.New("no security alternative of the document has this key"))
	}
}
