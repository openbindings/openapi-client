package openapi

import (
	"context"
	"io"
	"maps"
	"net/url"
	"strings"
)

// Load reads the document at uri, and every document its references reach,
// and returns a Client for it that uses opts. The uri is an http or https
// URL, a file URL, or a file path. A uri with a fragment is refused, as is
// one with userinfo, which RFC 9110 section 4.2.4 forbids a sender to
// generate (supply credentials through HTTPClient or Loader.Fetch), and a
// file URL naming a host other than localhost. ctx bounds the whole load,
// reading and parsing included. A nil opts means the defaults. The Client
// keeps a copy of opts, its maps included, so changing them afterwards has
// no effect. Load uses the zero [Loader].
//
// Load fails when the document is unusable as a whole: it cannot be
// retrieved or parsed (see [Loader]), its swagger or openapi field is
// missing or not 2.0, 3.0.x, 3.1.x or 3.2.x, or it lacks the root that holds
// its operations: paths in Swagger 2.0 and OpenAPI 3.0, one of paths,
// components and webhooks in 3.1 and 3.2. Any other defect, a missing
// required field included, is reported on the part it reaches, in its Err,
// or ignored where nothing depends on it, as a missing info is.
//
// Load also fails, with a *RequestError, on Options the document cannot
// use: a Credentials name the document never uses, an empty static
// credential, or a credential its schemes cannot use (see Credentials in
// the package documentation), a Variables name no server URL uses, a
// MediaType no operation declares, a Codecs key Options.Codecs refuses, a
// Server or ServerID that matches no server, a Security or SecurityKey
// that matches no alternative, a BaseURL without a scheme and host, with
// userinfo, a query or a fragment, or set with Server or ServerID,
// conflicting exact and name selectors, or a Header field that is always
// refused.
func Load(ctx context.Context, uri string, opts *Options) (*Client, error) {
	var l Loader
	return l.Load(ctx, uri, opts)
}

// Parse returns a Client for a document the caller already holds, such as one
// embedded with go:embed, using the zero [Loader], and fails as Load does. The
// content is JSON or YAML text. The uri, if not empty, is the absolute URI,
// without a fragment, the document is meant to live at, which stands for the
// URI it was retrieved from and is never fetched itself. With an empty uri, the
// document may reference only itself, a call whose server URL is relative needs
// Options.BaseURL, and Sources name the document by a "urn:uuid:" URI derived
// from the content (a name-based UUID, RFC 9562 version 5), so Sources and
// $defs keys are the same on every run.
func Parse(ctx context.Context, content []byte, uri string, opts *Options) (*Client, error) {
	var l Loader
	return l.Parse(ctx, content, uri, opts)
}

// A Loader reads documents. Its zero value is ready to use. A Loader's
// settings apply only while a document is read; a Client keeps none of
// them.
//
// A document is UTF-8, or UTF-16 or UTF-32 with a byte order mark or, for YAML,
// as YAML 1.2.2 section 5.2 deduces it; a UTF-8 byte order mark is ignored, and
// invalid UTF-8 rejects the document. A document whose first significant byte
// is '{' is read as JSON and, if it is not JSON, as YAML; anything else is read
// as YAML. YAML is parsed by go.yaml.in/yaml/v3, whose syntax rules apply, and
// its scalars are resolved under the YAML 1.2 Core schema, so yes and no stay
// strings, << is an ordinary key, and a scalar key such as an unquoted 200 is
// read as the string it spells. The parser reads a scalar written with the
// non-specific tag ! as if it had no tag. A %YAML directive for any version 1.x
// changes none of this, and any other major version rejects the document (YAML
// 1.2.2 section 6.8.1). Numbers keep the exact value written. A duplicate key,
// a key that is not a scalar, or a second document in the stream rejects the
// document, and so, in every edition, does a value JSON cannot hold (.inf,
// .nan, or a tag outside the Core schema, such as !!timestamp), since Document
// and Raw are JSON. A document whose aliases would add more than 1,000,000
// nodes, more than 100 times its own node count, or more than 100 times its own
// size in bytes, or that nests deeper than 1,000 levels (the outermost value
// being level 1), is rejected too. A rejection names the document's URI and
// where the problem is: for a YAML syntax error, the parser's own message, as
// it gives it; otherwise the line and column, both counted from 1, the column
// in the document's own bytes (two per UTF-16 code unit, four per UTF-32
// character) after any byte order mark, a node's position being where it
// starts, its tag included. Reference cycles are detected, never followed
// forever.
//
// An OpenAPI document uses the edition declared at its root. A referenced
// document with neither an openapi nor a swagger field uses the entry
// document's edition; its object type comes from the reference's context.
//
// If retrieval based on an inferred schema resource later reveals that the
// resource is instance data, references whose scope depends on that inference
// are unresolvable. The documents already retrieved remain available, and parts
// independent of that inference remain usable.
//
// The references followed are $ref in Reference Objects, Path Items and Schema
// Objects, $dynamicRef, Discriminator mapping and defaultMapping values that
// are not component names (a value that could be a component name is read as
// one, as OpenAPI recommends, and never fetched), and OpenAPI 3.2 security
// requirement URIs, anywhere in a document, webhooks and callbacks included;
// operationRef and externalValue are not retrieved. They resolve against each
// document's base: its OpenAPI 3.2 $self, itself resolved first against the URI
// the document was retrieved from when relative, or else that URI. Inside a 3.1
// or 3.2 schema, the nearest $id sets the base, as JSON Schema 2020-12 says
// (see Schema for other dialects). A fragment is percent-decoded as UTF-8
// before it is read as a JSON Pointer or a plain name.
//
// A reference resolves first to what loaded documents identify: a document by
// its retrieval URI (and the URI requested, when a redirect led there) or 3.2
// $self, a schema by $id, a plain name by $anchor or $dynamicAnchor. This is
// decided once every document reached is parsed. Only a URI no loaded document
// identifies is admitted and fetched, and the fetched document is then searched
// the same way. A reference to a URI with userinfo or with leading or trailing
// whitespace, or to a file URL naming a host other than localhost, is
// unresolvable and never fetched, as Load refuses such a uri. A URI claimed by
// two different documents or schemas is unresolvable, and the error names both;
// a document's URI and the $id of the schema at its root claim one schema. A
// reference that names a 3.2 document by the URI it was retrieved from rather
// than its $self, or that reaches a schema by a JSON Pointer crossing a nearer
// $id, still resolves; it stays visible as written where it is written, in a
// Schema's Raw or in Document at the Source of the object holding it. Security
// requirement names resolve as [SchemeLookup] says.
type Loader struct {
	// Fetch, if set, retrieves each document the loader needs in place of
	// the default (http and https with the Options' HTTPClient, file URLs
	// from disk). It returns the content, which the loader closes, and the
	// URI it was finally retrieved from after any redirects, which becomes
	// that document's base; an empty final means uri. The loader may call
	// Fetch from several goroutines at once, so that a document split into
	// several files loads in parallel.
	//
	// Fetch only retrieves. Reference admission is decided by
	// AllowReference, or by the default boundary and Origins when it is nil.
	// The loader checks the requested URI before Fetch and checks the final
	// URI it returns. A custom Fetch owns admission of its intermediate
	// redirect hops, which the loader cannot observe.
	Fetch func(ctx context.Context, uri string) (content io.ReadCloser, final string, err error)

	// Origins lists further origins, as "https://host" or
	// "https://host:port", whose documents references may reach. A
	// reference to any other origin, or its retrieval failing, disables
	// only what reaches it. A Loader that sets both Origins and
	// AllowReference is refused by its Load and Parse.
	Origins []string

	// AllowReference, when set, decides whether one document may retrieve
	// another. from is the absolute retrieval URI of the referring document; to
	// is the resolved absolute URI requested, or a redirect hop/final URI. It
	// is called before the fetch or hop, may run concurrently, and must be safe
	// for concurrent use. Returning false disables only the reference that
	// needs to cross that boundary; the rest of the document remains usable. A
	// document several documents refer to is retrieved when any of them may
	// retrieve it, whatever order they are read in, and every reference to it
	// then resolves, as to any loaded document. A callback replaces the default
	// boundary: it can admit an arbitrary trusted source graph, or restrict one
	// further. It does not apply to fragment-only references within a document.
	//
	// With nil, http and https references may reach the entry document's
	// original origin and Origins. For a file entry, file references may reach
	// only files under the entry file's directory, after cleaning paths and
	// resolving symlinks; Origins does not enlarge that file boundary. The
	// default retrieval checks every redirect hop. A custom Fetch must enforce
	// its own rules for unobservable intermediate hops.
	AllowReference func(from, to string) bool

	// MaxBytes bounds the bytes one load retrieves, all documents together.
	// Zero means 64 MiB, and a negative value means no limit. Past it, an
	// entry document fails the load with an error wrapping
	// *http.MaxBytesError, and a referenced document disables what reaches
	// it. Content passed to Parse is not counted.
	MaxBytes int64

	// SchemeLookup says where the scheme names a security requirement in a
	// referenced document uses are looked up; see [SchemeLookup].
	SchemeLookup SchemeLookup
}

// A SchemeLookup says where the component names that a security
// requirement in a referenced document uses are looked up; it governs
// component names only. OpenAPI allows two readings, the entry document
// and the document holding the requirement; SchemesInEntry and
// SchemesInReferrer each follow one exactly. The default,
// SchemesInEntryFirst, looks in the entry document, then, for a name it
// does not define, in the referring one. In OpenAPI 3.2, a name that is not
// a component name where it is looked up is a URI reference to a Security
// Scheme Object, resolved against the base of the document holding the
// requirement; in earlier editions it is a defect of the requirement.
type SchemeLookup int

const (
	SchemesInEntryFirst SchemeLookup = iota // the entry document, then the referring one (the default)
	SchemesInEntry                          // only the entry document, as OpenAPI recommends
	SchemesInReferrer                       // only the document holding the requirement
)

// Load reads a document as the package's Load function does, with l's
// settings.
func (l *Loader) Load(ctx context.Context, uri string, opts *Options) (*Client, error) {
	ld, err := l.start(ctx, uri, opts)
	if err != nil {
		return nil, err
	}
	content, final, _, err := ld.retrieve(uri, nil, nil)
	if err != nil {
		return nil, err
	}
	return newClient(ld, content, final, opts)
}

// Parse returns a Client for content as the package's Parse function does,
// with l's settings, which govern the documents its references reach. With
// an empty uri, absolute external references can be fetched only when
// AllowReference admits them; relative external references have no base
// unless an OpenAPI 3.2 absolute $self supplies one.
func (l *Loader) Parse(ctx context.Context, content []byte, uri string, opts *Options) (*Client, error) {
	ld, err := l.start(ctx, uri, opts)
	if err != nil {
		return nil, err
	}
	return newClient(ld, string(content), uri, opts)
}

// Version reports the version the entry document declares: its swagger
// value ("2.0") or its openapi value (such as "3.1.0"). It decides the
// dialect of the document's schemas as written (see [Schema.Dialect]).
func (c *Client) Version() string {
	if c.doc == nil {
		return ""
	}
	return c.doc.version
}

// DocumentURIs lists every document the Client loaded, including the entry
// document and external references, by retrieval URI. The entry is first;
// the rest are sorted by URI, independent of concurrent fetch order.
// OpenAPI 3.2 $self values are aliases, not extra entries. The returned
// slice is new and may be changed by the caller. Use [Client.Document] to
// obtain a copy of one document's contents. This includes loaded documents
// whose declarations are not exposed by Operations.
func (c *Client) DocumentURIs() []string {
	if c.doc == nil {
		return nil
	}
	uris := make([]string, len(c.doc.trees))
	for i, t := range c.doc.trees {
		uris[i] = t.uri
	}
	return uris
}

// Document returns a copy of the document loaded from uri, as JSON (a YAML
// document converted as the client read it), or nil when no document was
// loaded from uri. An empty uri means the entry document. A document is
// named by the URI it was retrieved from (for Parse, the uri given or the
// one derived from the content), which is what the descriptions' Source
// fields name; Document also accepts an OpenAPI 3.2 document's $self, as
// resolved. With a JSON Pointer fragment, as any Source has, it returns a
// copy of only that node, or nil when there is none; a Reference Object
// there is returned as written, and resolves against its document's base.
// Without one, each call copies the whole document, so call it once per
// document and keep the result.
//
// A YAML document's JSON has no insignificant whitespace, its members in the
// order written and its strings as encoding/json writes them without HTML
// escaping. A number keeps its spelling where JSON's grammar allows it;
// otherwise only what the grammar requires changes: a leading + is dropped, as
// are zeros leading a whole part of more than one digit, a 0 is written before
// a leading point, a point with no digit after it is dropped, and a
// hexadecimal or octal integer is written in decimal.
//
// A Source's fragment, like a SchemaReference.URI's, is a JSON Pointer
// percent-encoded as RFC 6901 section 6 says, as in
// #/paths/~1pets~1%7BpetId%7D/get; Client.Schema and Document accept
// these URIs.
//
// Document lets a caller check what the descriptions do not model, such as
// an extension, or which of a Path Item's fields were written beside its
// $ref, without the client growing a field for each such fact.
func (c *Client) Document(uri string) []byte {
	d := c.doc
	if d == nil {
		return nil
	}
	base, frag, hasFrag := strings.Cut(uri, "#")
	t := d.tree
	if base != "" {
		if t = d.named[base]; t == nil {
			claim := d.ids[base]
			if claim == nil || claim.other != nil || claim.v.i != 0 || claim.v.t.edition != 32 || claim.v.t.refbase.String() != base {
				return nil
			}
			t = claim.v.t
		}
	}
	v := t.root()
	if hasFrag {
		ptr, err := url.PathUnescape(frag)
		if err != nil {
			return nil
		}
		if v = v.at(ptr); !v.ok() {
			return nil
		}
	}
	return []byte(v.raw())
}

// newClient returns a Client for content, retrieved from uri, with opts.
func newClient(ld *loading, content, uri string, opts *Options) (*Client, error) {
	d, err := newDocument(ld, content, uri)
	if err != nil {
		return nil, err
	}
	var o Options
	if opts != nil {
		o = *opts
	}
	c := &Client{doc: d, cfg: newConfig(o, nil)}
	re := RequestError{Settings: maps.Clone(c.cfg.refused)}
	if c.cfg.mediaTypeErr != nil {
		re.setting("Options.MediaType", c.cfg.mediaTypeErr)
	}
	if c.cfg.codecsErr != nil {
		re.setting("Options.Codecs", c.cfg.codecsErr)
	}
	if err := d.checkNames(ld.ctx, c.cfg, &re); err != nil {
		return nil, err
	}
	if err := re.refused(); err != nil {
		return nil, err
	}
	return c, nil
}
