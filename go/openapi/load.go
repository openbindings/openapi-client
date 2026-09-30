package openapi

import (
	"context"
	"io"
)

// Load reads the document at uri, and every document its references reach,
// and returns a Client for it that uses opts. The uri is an http or https
// URL, a file URL, or a file path. A nil opts means the defaults. The Client
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

// Parse returns a Client for a document the caller already holds, such as
// one embedded with go:embed, using the zero [Loader], and fails as Load
// does. The content is JSON or YAML text. The uri, if not empty, is the
// absolute URI the document is meant to live at, which stands for the URI
// it was retrieved from and is never fetched itself. With an empty
// uri, the document may reference only itself, a call whose server URL is
// relative needs Options.BaseURL, and Sources name the document by a
// "urn:uuid:" URI derived from the content (a name-based UUID, RFC 9562
// version 5), so Sources and $defs keys are the same on every run.
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
// as YAML 1.2 under its Core schema, so yes and no stay strings, << is an
// ordinary key, and a scalar key such as an unquoted 200 is read as the string
// it spells. Numbers keep the exact value written. A duplicate key, a key that
// is not a scalar, or a second document in the stream rejects the document, and
// so, in every edition, does a value JSON cannot hold (.inf, .nan, or a tag
// outside the Core schema, such as !!timestamp), since Document and Raw are
// JSON. A document whose aliases would add more than 1,000,000 nodes, or more
// than 100 times its own node count, or that nests deeper than 1,000 levels
// (the outermost value being level 1), is rejected too. A rejection names the
// document's URI and the line and column of the problem, both counted from 1,
// the column in bytes. Reference cycles are detected, never followed forever.
//
// The references followed are $ref in Reference Objects, Path Items and
// Schema Objects, $dynamicRef, Discriminator mapping and defaultMapping
// values that are not component names, and OpenAPI 3.2 security
// requirement URIs, anywhere in a document, webhooks and callbacks
// included; operationRef and externalValue are not retrieved. They resolve
// against each document's base: its OpenAPI 3.2 $self, itself resolved
// first against the URI the document was retrieved from when relative, or
// else that URI.
// Inside a 3.1 or 3.2 schema, the nearest $id sets the base, as JSON Schema
// 2020-12 says (see Schema for other dialects). A fragment is
// percent-decoded as UTF-8 before it is read as a JSON Pointer or a plain
// name.
//
// A reference resolves first to what loaded documents identify: a document
// by its retrieval URI or 3.2 $self, a schema by $id, a plain name by
// $anchor or $dynamicAnchor. This is decided once every document reached is
// parsed. Only a URI no loaded document identifies is admitted and fetched,
// and the fetched document is then searched the same way. A URI claimed by
// two documents or schemas is unresolvable, and the error names both. A
// reference that names a 3.2 document by the URI it was retrieved from
// rather than its $self, or that reaches a schema by a JSON Pointer
// crossing a nearer $id, still resolves; it stays visible as written where
// it is written, in a Schema's Raw or in Document at the Source of the
// object holding it. Security requirement names resolve as [SchemeLookup]
// says.
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
	// another. from is the absolute retrieval URI of the referring document;
	// to is the resolved absolute URI requested, or a redirect hop/final
	// URI. It is called before the fetch or hop, may run concurrently, and
	// must be safe for concurrent use. Returning false disables only the
	// reference that needs to cross that boundary; the rest of the document
	// remains usable. A callback replaces the default boundary: it can
	// admit an arbitrary trusted source graph, or restrict one further.
	// It does not apply to fragment-only references within a document.
	//
	// With nil, http and https references may reach the entry document's
	// original origin and Origins. For a file entry, references may reach
	// only files under the entry file's directory, after cleaning paths and
	// resolving symlinks; Origins does not enlarge that file boundary. The
	// default retrieval checks every redirect hop. A custom Fetch must
	// enforce its own rules for unobservable intermediate hops.
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
	panic("unimplemented")
}

// Parse returns a Client for content as the package's Parse function does,
// with l's settings, which govern the documents its references reach. With
// an empty uri, absolute external references can be fetched only when
// AllowReference admits them; relative external references have no base
// unless an OpenAPI 3.2 absolute $self supplies one.
func (l *Loader) Parse(ctx context.Context, content []byte, uri string, opts *Options) (*Client, error) {
	panic("unimplemented")
}

// Version reports the version the entry document declares: its swagger
// value ("2.0") or its openapi value (such as "3.1.0"). It decides the
// dialect of the document's schemas as written (see [Schema.Dialect]).
func (c *Client) Version() string {
	panic("unimplemented")
}

// DocumentURIs lists every document the Client loaded, including the entry
// document and external references, by retrieval URI. The entry is first;
// the rest are sorted by URI, independent of concurrent fetch order.
// OpenAPI 3.2 $self values are aliases, not extra entries. The returned
// slice is new and may be changed by the caller. Use [Client.Document] to
// obtain a copy of one document's contents. This includes loaded documents
// whose declarations are not exposed by Operations.
func (c *Client) DocumentURIs() []string {
	panic("unimplemented")
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
// A Source's fragment, like a SchemaReference.URI's, is a JSON Pointer
// percent-encoded as RFC 6901 section 6 says, as in
// #/paths/~1pets~1%7BpetId%7D/get; Client.Schema and Document accept
// these URIs.
//
// Document lets a caller check what the descriptions do not model, such as
// an extension, or which of a Path Item's fields were written beside its
// $ref, without the client growing a field for each such fact.
func (c *Client) Document(uri string) []byte {
	panic("unimplemented")
}
