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
)

// A config is a Client's private copy of its Options, with what calls
// derive from them.
type config struct {
	Options
	client    *http.Client     // HTTPClient, following no redirects
	base      *endpoint        // BaseURL, parsed
	mediaType media            // MediaType, parsed
	codecs    map[string]Codec // Codecs, by lowercase key
	refused   map[string]error // settings no call can use, by Settings key
}

// newConfig copies o and checks what it can without a document.
func newConfig(o Options) *config {
	cfg := &config{Options: o, refused: map[string]error{}}
	cfg.Variables = maps.Clone(o.Variables)
	cfg.Credentials = maps.Clone(o.Credentials)
	cfg.Codecs = maps.Clone(o.Codecs)
	cfg.Security = slices.Clone(o.Security)
	cfg.Header = make(http.Header, len(o.Header))
	for k, v := range o.Header {
		k = textproto.CanonicalMIMEHeaderKey(k)
		if reservedField(k) {
			cfg.refused["Options.Header"] = fmt.Errorf("sets %s, which the client generates", k)
		}
		cfg.Header[k] = slices.Clone(v)
	}

	hc := o.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	client := *hc
	client.CheckRedirect = followNone
	cfg.client = &client

	switch {
	case o.Redirects == FollowAll:
		cfg.refused["Options.Redirects"] = notYet("following redirects")
	case o.Redirects != FollowNone:
		cfg.refused["Options.Redirects"] = fmt.Errorf("unknown value %d", o.Redirects)
	}
	if o.BaseURL != "" {
		u, err := url.Parse(o.BaseURL)
		switch {
		case err != nil:
			cfg.refused["Options.BaseURL"] = err
		case u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
			cfg.refused["Options.BaseURL"] = errors.New("needs a scheme and a host, and no userinfo, query or fragment")
		case o.Server != "" || o.ServerID != "":
			cfg.refused["Options.BaseURL"] = errors.New("cannot be set with Options.Server or Options.ServerID")
		default:
			cfg.base = &endpoint{u.Scheme, u.Host, u.EscapedPath()}
		}
	}
	if o.Server != "" && o.ServerID != "" {
		cfg.refused["Options.ServerID"] = errors.New("cannot be set with Options.Server")
	}
	for name := range o.Credentials {
		cfg.refused["Options.Credentials["+strconv.Quote(name)+"]"] = notYet("credentials")
	}
	if o.Security != nil {
		cfg.refused["Options.Security"] = notYet("Options.Security")
	}
	if o.SecurityKey != "" {
		cfg.refused["Options.SecurityKey"] = notYet("Options.SecurityKey")
	}
	if o.MediaType != "" {
		var ok bool
		if cfg.mediaType, ok = parseMedia(o.MediaType); !ok || !cfg.mediaType.concrete() {
			cfg.refused["Options.MediaType"] = fmt.Errorf("%q is not a concrete media type", o.MediaType)
		}
	}
	cfg.codecs = make(map[string]Codec, len(o.Codecs))
	for k, c := range o.Codecs {
		key := strings.ToLower(k)
		m, ok := parseMedia(key)
		suffix := strings.HasPrefix(key, "+") && isToken(key[1:])
		switch {
		case !suffix && (!ok || !m.concrete() || m.params != "" || strings.Contains(key, ";")):
			cfg.refused["Options.Codecs"] = fmt.Errorf("key %q is neither a media type without parameters nor a +suffix", k)
		case suffix && key == "+json-seq", !suffix && (m.class() == sequentialClass || m.typ == "multipart"):
			cfg.refused["Options.Codecs"] = fmt.Errorf("key %q names a sequential or multipart type, whose framing is the client's", k)
		default:
			cfg.codecs[key] = c
		}
	}
	return cfg
}

func followNone(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// reservedField reports whether the canonical field name k is one Header
// settings cannot set.
func reservedField(k string) bool {
	return k == "Content-Type" || k == "Content-Length" || k == "Transfer-Encoding"
}

// options returns a copy of cfg's Options with maps of its own.
func (cfg *config) options() Options {
	o := cfg.Options
	o.Header = cfg.Header.Clone()
	if o.Header == nil {
		o.Header = http.Header{}
	}
	o.Variables = maps.Clone(cfg.Variables)
	if o.Variables == nil {
		o.Variables = map[string]string{}
	}
	o.Credentials = maps.Clone(cfg.Credentials)
	if o.Credentials == nil {
		o.Credentials = map[string]Credential{}
	}
	o.Codecs = maps.Clone(cfg.Codecs)
	if o.Codecs == nil {
		o.Codecs = map[string]Codec{}
	}
	o.Security = slices.Clone(cfg.Security)
	return o
}

// checkNames refuses, as Load does, the names in cfg that no part of d
// uses.
func (d *document) checkNames(cfg *config, re *RequestError) {
	server, serverID, mediaType := cfg.Server == "", cfg.ServerID == "", cfg.MediaType == "" || cfg.refused["Options.MediaType"] != nil
	unused := maps.Clone(cfg.Variables)
	if server && serverID && mediaType && len(unused) == 0 {
		return
	}
	for _, o := range d.ops {
		o.compile()
		for _, s := range o.Servers {
			server = server || s.URL == cfg.Server || s.Name == cfg.Server
			serverID = serverID || s.ID == cfg.ServerID
			for _, v := range s.Variables {
				delete(unused, v.Name)
			}
		}
		if o.Body != nil {
			mediaType = mediaType || match(o.body, o.Body.Media, cfg.mediaType) != nil
		}
	}
	if !server {
		re.setting("Options.Server", fmt.Errorf("no server has the URL or name %q", cfg.Server))
	}
	if !serverID {
		re.setting("Options.ServerID", errors.New("no server has this ID"))
	}
	if !mediaType {
		re.setting("Options.MediaType", fmt.Errorf("no operation declares %s", cfg.MediaType))
	}
	for name := range unused {
		re.setting("Options.Variables["+strconv.Quote(name)+"]", errors.New("no server URL uses this variable"))
	}
}
