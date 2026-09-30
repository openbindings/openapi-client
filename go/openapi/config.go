package openapi

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// A config is a Client's private copy of its Options, with what calls
// derive from them.
type config struct {
	Options
	client       *http.Client     // HTTPClient, following no redirects
	base         *endpoint        // BaseURL, parsed
	mediaType    parsedMedia      // MediaType, parsed
	mediaTypeErr error            // why MediaType cannot be used, refusing calls that send a body
	codecs       map[string]Codec // Codecs, by lowercase key
	codecsErr    error            // why a Codecs key cannot be used, refusing calls that use a codec
	refused      map[string]error // settings no call can use, by Settings key
	endpoints    sync.Map         // *server to the endpoint it resolves to with Variables
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
	for k, v := range cfg.Header {
		if ck := textproto.CanonicalMIMEHeaderKey(k); ck != k {
			delete(cfg.Header, k)
			cfg.Header[ck] = v
		}
	}
	cfg.Variables = clone(o.Variables)
	cfg.Credentials = clone(o.Credentials)
	cfg.Codecs = clone(o.Codecs)
	cfg.Security = slices.Clone(o.Security)

	if parent != nil && parent.HTTPClient == o.HTTPClient {
		cfg.client = parent.client
	} else {
		hc := o.HTTPClient
		if hc == nil {
			hc = http.DefaultClient
		}
		client := *hc
		client.CheckRedirect = followNone
		cfg.client = &client
	}
	switch {
	case o.Redirects == FollowAll:
		refuse("Options.Redirects", notYet("following redirects"))
	case o.Redirects != FollowNone:
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
			cfg.base = &endpoint{u.Scheme, u.Host, escapePath(u.EscapedPath())}
		}
	}
	if o.Server != "" && o.ServerID != "" {
		refuse("Options.ServerID", errors.New("cannot be set with Options.Server"))
	}
	for name := range o.Credentials {
		refuse("Options.Credentials["+strconv.Quote(name)+"]", notYet("credentials"))
	}
	if o.Security != nil {
		refuse("Options.Security", notYet("Options.Security"))
	}
	if o.SecurityKey != "" {
		refuse("Options.SecurityKey", notYet("Options.SecurityKey"))
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

func followNone(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// checkHeader reports why fields cannot be sent: a name that is not a
// token, a value HTTP cannot carry, a field the client generates or net/http
// derives, or two spellings of one field.
func checkHeader(fields http.Header) error {
	for k, vs := range fields {
		ck := textproto.CanonicalMIMEHeaderKey(k)
		switch {
		case !isToken(k):
			return fmt.Errorf("field name %q is not a token", k)
		case ck == "Content-Type" || ck == "Content-Length" || ck == "Transfer-Encoding":
			return fmt.Errorf("sets %s, which the client generates", ck)
		case ck == "Host":
			return errors.New("sets Host, which net/http derives from the URL")
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
