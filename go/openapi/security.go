package openapi

import (
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
	desc SecurityScheme // without the Name and Scopes of a use
	kind schemeKind
	dest paramID // the header field, query name or cookie name a credential sets
	pair string  // a query or cookie credential's name as written
	auth string  // the auth-scheme and space before a credential in the Authorization field
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

// securityScheme returns the scheme a security requirement names, compiled
// once for each declaration: the component of that name, following
// references.
func (d *document) securityScheme(name string) *scheme {
	v := d.root().get("components").get("securitySchemes").get(name)
	if !v.ok() {
		return &scheme{desc: SecurityScheme{Err: fmt.Errorf("the document declares no security scheme %q", name)}}
	}
	return d.schemeNames.get(v.i, func() *scheme {
		src := d.source("/components/securitySchemes/" + escapeToken(name))
		t, at, desc, err := d.follow(v, src)
		switch {
		case err != nil:
			return &scheme{desc: SecurityScheme{Source: src, Err: err}}
		case t.i == v.i: // only this name reaches it
			return newScheme(t, at)
		}
		sc := d.schemeForms.get(t.i, func() *scheme { return newScheme(t, at) })
		if desc != sc.desc.Description {
			c := *sc
			c.desc.Description = desc
			sc = &c
		}
		return sc
	})
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
	var err error
	switch s.Type {
	case "apiKey":
		s.In, s.ParamName = t.str("in"), t.str("name")
		name := s.ParamName
		switch {
		case name == "":
			err = errors.New("an apiKey scheme needs a name")
		case s.In == "header" && (!isToken(name) || strings.EqualFold(name, "Content-Type") || strings.EqualFold(name, "Cookie") ||
			slices.ContainsFunc(derivedFields, func(f string) bool { return strings.EqualFold(f, name) })):
			err = fmt.Errorf("an apiKey cannot be sent in the header field %q", name)
		case s.In == "header":
			sc.kind, sc.dest = apiKeyHeader, paramID{"header", textproto.CanonicalMIMEHeaderKey(name)}
		case s.In == "query":
			sc.kind, sc.dest, sc.pair = apiKeyQuery, paramID{"query", name}, escape(name, unreservedSet)
		case s.In == "cookie" && isToken(name): // RFC 6265 section 4.1.1
			sc.kind, sc.dest, sc.pair = apiKeyCookie, paramID{"cookie", name}, name
		case s.In == "cookie":
			err = fmt.Errorf("cookie name %q is not a token (RFC 6265 section 4.1.1)", name)
		default:
			err = fmt.Errorf("apiKey location %q is not query, header or cookie", s.In)
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
		flows := t.get("flows")
		if flows.kind() != '{' {
			err = errors.New("an oauth2 scheme needs flows")
		}
		s.Flows = oauthFlows(flows)
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

// oauthFlows describes the OAuth Flows Object v, in document order.
func oauthFlows(v value) []Flow {
	var flows []Flow
	for typ, f := range v.members() {
		switch typ {
		case "implicit", "password", "clientCredentials", "authorizationCode":
		default:
			continue
		}
		if f.kind() != '{' {
			continue
		}
		flow := Flow{Type: typ, AuthorizationURL: f.str("authorizationUrl"), TokenURL: f.str("tokenUrl"), RefreshURL: f.str("refreshUrl")}
		if scopes := f.get("scopes"); scopes.kind() == '{' {
			flow.Scopes = map[string]string{}
			for name, desc := range scopes.members() {
				flow.Scopes[name] = desc.string()
			}
		}
		flows = append(flows, flow)
	}
	return flows
}

// An alternative is a security alternative with its schemes compiled.
type alternative struct {
	*SecurityRequirement
	schemes []*scheme // each of Schemes
	clash   bool      // two schemes set one header field, query name or cookie name
}

// securityList describes and compiles the Security Requirement Objects of
// list, returning the descriptions and the alternatives, which share them.
func (d *document) securityList(list value) ([]SecurityRequirement, []alternative) {
	var reqs []SecurityRequirement
	var alts []alternative
	for _, r := range list.members() {
		if list.kind() != '[' || r.kind() != '{' {
			continue
		}
		var req SecurityRequirement
		var a alternative
		for name, scopes := range r.members() {
			sc := d.securityScheme(name)
			s := sc.desc
			s.Name, s.Scopes = name, scopes.strs()
			req.Schemes, a.schemes = append(req.Schemes, s), append(a.schemes, sc)
		}
		req.Key = requirementKey(req.Schemes)
		_, j := clash(a.schemes, nil)
		a.clash = j > 0
		reqs, alts = append(reqs, req), append(alts, a)
	}
	for i := range alts {
		alts[i].SecurityRequirement = &reqs[i]
	}
	return reqs, alts
}

// requirementKey returns the SecurityRequirement.Key of an alternative
// with schemes.
func requirementKey(schemes []SecurityScheme) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range slices.SortedFunc(slices.Values(schemes), func(a, b SecurityScheme) int { return strings.Compare(a.Name, b.Name) }) {
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

// noAlternative places no credentials.
var noAlternative alternative

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
	switch {
	case len(alts) == 0:
		return nil
	case !slices.ContainsFunc(alts, func(a alternative) bool { return a.Key != alts[0].Key }):
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
			if err := secured(ep); err != nil {
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
	errFieldValue  = errors.New("a header field cannot carry the credential: it holds a CR, LF, NUL or other control character, or leading or trailing whitespace")
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
		return errors.New("a Basic username cannot hold a colon, nor either value a control character (RFC 7617 section 2)")
	case c.kind != sourceCredential:
		return sc.checkValue(c.secret())
	}
	return nil
}

// loadError returns why Load refuses c for a name whose scheme is sc, or
// nil.
func (sc *scheme) loadError(c Credential) error {
	switch {
	case c.kind == noCredential:
		return errors.New("the credential is empty")
	case c.kind == transportCredential:
		return nil
	case sc.kind == mutualTLS:
		return errMutualTLS
	case c.basic() && sc.kind != httpBasic:
		return errNotBasic
	case c.kind == secretCredential:
		return sc.checkValue(c.secret())
	}
	return nil
}

// checkValue returns why a credential for sc cannot carry secret, or nil.
func (sc *scheme) checkValue(secret string) error {
	switch sc.kind {
	case apiKeyHeader, httpBearer, httpOther:
		if !validFieldValue(secret) {
			return errFieldValue
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
// ep, or nil: it goes over https or wss, or plain http or ws to a loopback
// host (RFC 6750 section 5.3, RFC 7617 section 4).
func secured(ep endpoint) error {
	switch ep.scheme {
	case "https", "wss":
		return nil
	case "http", "ws":
		if loopback(ep.host) {
			return nil
		}
		return fmt.Errorf("a bearer token or Basic credential cannot be sent over plain %s to a host other than a loopback one; use https, or place it with FromTransport", ep.scheme)
	}
	return fmt.Errorf("a bearer token or Basic credential sent over %q needs FromTransport", ep.scheme)
}

// loopback reports whether the host of hostport is a loopback address, an
// IPv4-mapped one included, or localhost or a name under .localhost, with
// or without a trailing dot (RFC 6761 section 6.3), matched without
// resolving.
func loopback(hostport string) bool {
	u := url.URL{Host: hostport}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.IsLoopback()
	}
	host = strings.TrimSuffix(host, ".")
	return strings.EqualFold(host, "localhost") || len(host) > len(".localhost") && hasSuffixFold(host, ".localhost")
}

// sameOrigin reports whether a and b have the same origin: scheme, host and
// port, a scheme's default port being the same as none (RFC 6454 section
// 4).
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

// port returns u's port, or its scheme's default.
func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "ws":
		return "80"
	case "https", "wss":
		return "443"
	}
	return ""
}

// sign returns req with the credentials of the call's alternative placed,
// calling their sources with the call's context. That is a copy of req, or
// req itself when the client places none and the http.Client has no cookie
// jar, which adds cookies to the header it is given. A *RequestError says
// why the credentials cannot be placed.
func (x *exchange) sign(req *http.Request) (*http.Request, error) {
	a := x.alt
	switch {
	case !x.places && x.cfg.client.Jar == nil:
		return req, nil
	case !x.places:
		a = &noAlternative
	}
	var re RequestError
	h := maps.Clone(req.Header)
	if h == nil {
		h = http.Header{}
	}
	var query, cookies []pair
	for i, sc := range a.schemes {
		name := a.Schemes[i].Name
		c := x.cfg.Credentials[name]
		if !c.places() {
			continue
		}
		secret, err := c.source(x.Context)
		switch {
		case err != nil:
			re.fail(withContext(x.Context, fmt.Errorf("the credential source for %q: %w", name, err)))
			continue
		case c.kind != sourceCredential: // checked when the call was prepared
		case secret == "":
			err = fmt.Errorf("the credential source for %q returned an empty secret", name)
		default:
			err = sc.checkValue(secret)
		}
		if err != nil {
			re.setting(credentialKey(name), err)
			continue
		}
		switch sc.kind {
		case apiKeyHeader:
			h[sc.dest.name] = []string{secret}
		case apiKeyQuery:
			query = append(query, pair{sc.dest.name, sc.pair + "=" + escape(secret, unreservedSet)})
		case apiKeyCookie:
			cookies = append(cookies, pair{sc.dest.name, sc.pair + "=" + secret})
		case httpBasic:
			h["Authorization"] = []string{sc.auth + base64.StdEncoding.EncodeToString([]byte(secret))}
		default:
			h["Authorization"] = []string{sc.auth + secret}
		}
	}
	if err := re.refused(); err != nil {
		return nil, err
	}
	s := new(http.Request)
	*s = *req
	s.Header = h
	if query != nil {
		u := *req.URL
		u.RawQuery = withPairs(u.RawQuery, "&", query, queryName)
		s.URL = &u
	}
	if cookies != nil {
		h["Cookie"] = []string{withPairs(strings.Join(h["Cookie"], "; "), "; ", cookies, cookieName)}
	}
	return s, nil
}

// A pair is a query or cookie credential: the name it replaces, and the
// pair as written.
type pair struct{ name, text string }

// withPairs returns list, pairs separated by the first byte of sep, less
// each pair whose name one of add replaces, followed by add, all separated
// by sep. name returns a pair of list, as sent, and its name.
func withPairs(list, sep string, add []pair, name func(string) (string, string)) string {
	replaced := func(n string) bool { return n == add[0].name }
	if len(add) > 1 {
		names := make(map[string]bool, len(add))
		for _, p := range add {
			names[p.name] = true
		}
		replaced = func(n string) bool { return names[n] }
	}
	var b strings.Builder
	b.Grow(len(list) + len(add)*32)
	for s := range strings.SplitSeq(list, sep[:1]) {
		if s, n := name(s); s != "" && !replaced(n) {
			if b.Len() > 0 {
				b.WriteString(sep)
			}
			b.WriteString(s)
		}
	}
	for _, p := range add {
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(p.text)
	}
	return b.String()
}

// queryName returns the query pair s and its name, percent-decoded.
func queryName(s string) (string, string) {
	n, _, _ := strings.Cut(s, "=")
	if strings.ContainsAny(n, "%+") {
		if d, err := url.QueryUnescape(n); err == nil {
			n = d
		}
	}
	return s, n
}

// cookieName returns the cookie pair s, without surrounding whitespace, and
// its name.
func cookieName(s string) (string, string) {
	s = strings.Trim(s, " \t")
	n, _, _ := strings.Cut(s, "=")
	return s, strings.TrimRight(n, " \t")
}

// redact returns err, from the http.Client, with the URL a *url.Error names
// replaced by u, the request's URL without the credentials the client
// placed, redacted as net/http does.
func redact(err error, u *url.URL) error {
	if ue, ok := err.(*url.Error); ok {
		return &url.Error{Op: ue.Op, URL: u.Redacted(), Err: ue.Err}
	}
	return err
}

// A securityCheck is Load's check of the security settings: what it has not
// yet found among the requirements of the document.
type securityCheck struct {
	d     *document
	re    *RequestError
	creds map[string]Credential // those whose name no requirement has used
	names map[string]bool       // Options.Security, until an alternative has these schemes
	key   string                // Options.SecurityKey, until an alternative has it
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
			if c, ok := s.creds[name]; ok {
				delete(s.creds, name)
				if err := s.d.securityScheme(name).loadError(c); err != nil {
					s.re.setting(credentialKey(name), err)
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
		s.re.setting(credentialKey(name), errors.New("no security requirement of the document uses this name"))
	}
	if s.names != nil {
		s.re.setting("Options.Security", errors.New("no security alternative of the document has exactly these schemes"))
	}
	if s.key != "" {
		s.re.setting("Options.SecurityKey", errors.New("no security alternative of the document has this key"))
	}
}
