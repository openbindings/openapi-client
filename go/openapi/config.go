package openapi

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// A config is a Client's private copy of its Options, with what calls
// derive from them.
type config struct {
	Options
	client        *http.Client                               // HTTPClient, following no redirects and with no cookie jar
	jar           http.CookieJar                             // HTTPClient's Jar, which the client applies itself
	checkRedirect func(*http.Request, []*http.Request) error // HTTPClient's CheckRedirect, or net/http's default
	base          *endpoint                                  // BaseURL, parsed
	securityNames map[string]bool                            // Security, as a set
	mediaType     parsedMedia                                // MediaType, parsed
	mediaTypeErr  error                                      // why MediaType cannot be used, refusing calls that send a body
	codecs        map[string]Codec                           // Codecs, by lowercase key
	codecsErr     error                                      // why a Codecs key cannot be used, refusing calls that use a codec
	refused       map[string]error                           // settings no call can use, by Settings key
	endpoints     sync.Map                                   // *server to the endpoint it resolves to with Variables
}

// newConfig copies o, its maps and slices included, and checks what it can
// without a document. It reuses parent's http.Client when o has the same
// HTTPClient.
func newConfig(o Options, parent *config) *config {
	cfg := &config{Options: o}
	refuse := func(key string, err error) {
		if cfg.refused == nil {
			cfg.refused = map[string]error{}
		}
		cfg.refused[key] = err
	}
	if err := checkHeader(o.Header); err != nil {
		refuse("Options.Header", err)
	}
	cfg.Header = o.Header.Clone()
	canonicalize(cfg.Header)
	cfg.Variables = clone(o.Variables)
	cfg.Credentials = clone(o.Credentials)
	cfg.Codecs = clone(o.Codecs)
	cfg.Security = slices.Clone(o.Security)

	if parent != nil && parent.HTTPClient == o.HTTPClient {
		cfg.client, cfg.jar, cfg.checkRedirect = parent.client, parent.jar, parent.checkRedirect
	} else {
		hc := o.HTTPClient
		if hc == nil {
			hc = http.DefaultClient
		}
		cfg.client = &http.Client{Transport: noFollow{hc.Transport}, Timeout: hc.Timeout}
		cfg.jar, cfg.checkRedirect = hc.Jar, hc.CheckRedirect
		if cfg.checkRedirect == nil {
			cfg.checkRedirect = tenRedirects
		}
	}
	if o.Redirects != FollowNone && o.Redirects != FollowAll {
		refuse("Options.Redirects", fmt.Errorf("unknown value %d", o.Redirects))
	}
	if o.BaseURL != "" {
		u, err := url.Parse(o.BaseURL)
		switch {
		case err != nil:
			refuse("Options.BaseURL", errors.New("not a URL"))
		case u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(o.BaseURL, "#"):
			refuse("Options.BaseURL", errors.New("needs a scheme and a host, and no userinfo, query or fragment"))
		case o.Server != "" || o.ServerID != "":
			refuse("Options.BaseURL", errors.New("cannot be set with Options.Server or Options.ServerID"))
		default:
			cfg.base = &endpoint{u.Scheme, u.Host, escape(u.EscapedPath(), pathSet)}
		}
	}
	if o.Server != "" && o.ServerID != "" {
		refuse("Options.ServerID", errors.New("cannot be set with Options.Server"))
	}
	if o.Security != nil {
		if o.SecurityKey != "" {
			refuse("Options.SecurityKey", errors.New("cannot be set with Options.Security"))
		}
		cfg.securityNames = make(map[string]bool, len(o.Security))
		for _, name := range o.Security {
			cfg.securityNames[name] = true
		}
	}
	if o.MediaType != "" {
		var ok bool
		if cfg.mediaType, ok = parseMedia(o.MediaType); !ok || !cfg.mediaType.concrete() {
			cfg.mediaTypeErr = fmt.Errorf("%q is not a concrete media type", o.MediaType)
		}
	}
	for k, c := range o.Codecs {
		key := strings.ToLower(k)
		m, ok := parseMedia(key)
		suffix := strings.HasPrefix(key, "+") && isToken(key[1:])
		switch {
		case !suffix && (!ok || !m.concrete() || m.full != key):
			cfg.codecsErr = fmt.Errorf("key %q is neither a media type without parameters nor a +suffix", k)
		case key == "+json-seq" || !suffix && (m.class() == sequentialClass || m.typ == "multipart"):
			cfg.codecsErr = fmt.Errorf("key %q names a sequential or multipart type, whose framing is the client's", k)
		default:
			if cfg.codecs == nil {
				cfg.codecs = make(map[string]Codec, len(o.Codecs))
			}
			cfg.codecs[key] = c
		}
	}
	return cfg
}

// derivedFields are the header fields net/http derives or HTTP forbids a
// client to set (RFC 9110 sections 6.6.2 and 8.6, RFC 9113 section 8.2.2).
var derivedFields = []string{"Host", "Content-Length", "Transfer-Encoding", "Trailer", "Connection", "Keep-Alive", "Proxy-Connection", "Upgrade"}

// checkHeader reports why fields cannot be sent: a name that is not a
// token, a value HTTP cannot carry, a field the client generates or net/http
// derives, or two spellings of one field.
func checkHeader(fields http.Header) error {
	for k, vs := range fields {
		ck := textproto.CanonicalMIMEHeaderKey(k)
		switch {
		case !isToken(k):
			return fmt.Errorf("field name %q is not a token", k)
		case ck == "Content-Type":
			return errors.New("sets Content-Type, which the MediaType settings choose")
		case slices.Contains(derivedFields, ck):
			return fmt.Errorf("sets %s, which net/http derives or HTTP forbids", ck)
		case slices.ContainsFunc(vs, func(v string) bool { return !validFieldValue(v) }):
			return fmt.Errorf("field %s has a value HTTP cannot carry", ck)
		}
		for other := range fields {
			if other != k && strings.EqualFold(other, k) {
				return fmt.Errorf("holds two spellings of field %s", ck)
			}
		}
	}
	return nil
}

// canonicalize puts every field name of h in canonical form, joining the
// values of spellings of one field.
func canonicalize(h http.Header) {
	for k, vs := range h {
		if ck := textproto.CanonicalMIMEHeaderKey(k); ck != k {
			delete(h, k)
			h[ck] = append(h[ck], vs...)
		}
	}
}

// validFieldValue reports whether s can be sent as a field value: no
// control characters but tabs within it, and no whitespace around it.
func validFieldValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if !fieldByte(s[i]) {
			return false
		}
	}
	return s == strings.Trim(s, " \t")
}

// options returns a copy of cfg's Options with maps of its own.
func (cfg *config) options() Options {
	o := cfg.Options
	o.Header = cfg.Header.Clone()
	if o.Header == nil {
		o.Header = http.Header{}
	}
	o.Variables = own(cfg.Variables)
	o.Credentials = own(cfg.Credentials)
	o.Codecs = own(cfg.Codecs)
	o.Security = slices.Clone(cfg.Security)
	return o
}

// own returns a copy of m that is never nil.
func own[M ~map[K]V, K comparable, V any](m M) M {
	if c := clone(m); c != nil {
		return c
	}
	return M{}
}

// clone returns a copy of m, or nil when m is empty.
func clone[M ~map[K]V, K comparable, V any](m M) M {
	if len(m) == 0 {
		return nil
	}
	return maps.Clone(m)
}
