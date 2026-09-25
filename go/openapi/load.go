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
// Load also fails, with a *RequestError, when opts would refuse every call:
// a Credentials name the document never uses, a Variables name no server
// URL uses, a Server that matches no server, a Security that matches no
// alternative, a BaseURL that is not absolute or is set with Server, or a
// Header field that is always refused.
func Load(ctx context.Context, uri string, opts *Options) (*Client, error) {
	var l Loader
	return l.Load(ctx, uri, opts)
}

// Parse returns a Client for a document the caller already holds, such as
// one embedded with go:embed, using the zero [Loader], and fails as Load
// does. The content is JSON or YAML text. The uri, if not empty, is the
// absolute URI the document is meant to live at: the base for its relative
// references and relative server URLs, never fetched itself. With an empty
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
// A document is UTF-8, or UTF-16 or UTF-32 with a byte order mark; a UTF-8
// byte order mark is ignored. JSON is detected by a first significant byte
// of '{'; anything else is read as YAML 1.2 under its Core schema, so yes
// and no stay strings, << is an ordinary key, and a scalar key such as an
// unquoted 200 is read as the string it spells. A duplicate key, a key that
// is not a scalar, a tag outside the Core schema, .inf or .nan, or a second
// document in the stream rejects the document. A document whose aliases
// would expand it past 1,000,000 nodes, or past 100 times its own node
// count, or that nests deeper than 1,000 levels, is rejected too. A
// rejection names the document's URI and the line and column of the
// problem. Reference cycles are detected, never followed forever.
//
// References resolve against each document's base: its $self in OpenAPI
// 3.2, else the URI it was retrieved from. Inside a 3.1 or 3.2 schema, the
// nearest $id sets the base, as JSON Schema says. A reference that names a
// 3.2 document by the URI it was retrieved from rather than its $self, or
// that reaches a schema by a JSON Pointer crossing a nearer $id, still
// resolves; it stays visible as written where it is written, in a Schema's
// Raw or in Document at the Source of the object holding it. In OpenAPI
// 3.2, a security requirement's name that is not a component name is a URI
// reference to a Security Scheme Object, resolved like any other.
type Loader struct {
	// Fetch, if set, retrieves each document the loader needs in place of
	// the default (http and https with the Options' HTTPClient, file URLs
	// from disk). It returns the content, which the loader closes, and the
	// URI it was finally retrieved from after any redirects, which becomes
	// that document's base; an empty final means uri. The loader may call
	// Fetch from several goroutines at once, so that a document split into
	// several files loads in parallel.
	//
	// Fetch only retrieves. The loader still allows a reference to reach
	// only the entry document's origin (that of the URI requested, before
	// any redirect: the same scheme, host and port, or local files for a
	// file) and the Origins below. The default retrieval holds every
	// redirect hop to that rule, and a final URI outside it, from Fetch or
	// the default, disables what reaches the document.
	Fetch func(ctx context.Context, uri string) (content io.ReadCloser, final string, err error)

	// Origins lists further origins, as "https://host" or
	// "https://host:port", whose documents references may reach. A
	// reference to any other origin, or its retrieval failing, disables
	// only what reaches it.
	Origins []string

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
// requirement in a referenced document uses are looked up. OpenAPI allows
// two readings, the entry document and the document holding the
// requirement; SchemesInEntry and SchemesInReferrer each follow one
// exactly, and a name that document does not define is a defect of the
// requirement. The default, SchemesInEntryFirst, looks in the entry
// document, then, for a name it does not define, in the referring one.
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
// with l's settings, which govern the documents its references reach.
func (l *Loader) Parse(ctx context.Context, content []byte, uri string, opts *Options) (*Client, error) {
	panic("unimplemented")
}

// Version reports the version the entry document declares: its swagger
// value ("2.0") or its openapi value (such as "3.1.0"). It decides the
// dialect of the document's schemas as written (see [Schema.Dialect]).
func (c *Client) Version() string {
	panic("unimplemented")
}

// Document returns a copy of the document loaded from uri, as JSON (a YAML
// document converted as the client read it), or nil when no document was
// loaded from uri. An empty uri means the entry document. A document is
// named by the URI it was retrieved from (for Parse, the uri given or the
// one derived from the content), which is what the descriptions' Source
// fields name; Document also accepts an OpenAPI 3.2 document's $self. Each
// call copies the whole document, so call it once per document and keep the
// result.
//
// Document lets a caller check what the descriptions do not model, such as
// an extension, or whether a Path Item $ref had sibling fields, without the
// client growing a field for each such fact.
func (c *Client) Document(uri string) []byte {
	panic("unimplemented")
}
