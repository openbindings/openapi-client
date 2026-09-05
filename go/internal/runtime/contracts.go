package openapiclient

import (
	"net/http"

	"github.com/getkin/kin-openapi/openapi3"
)

// Source identifies an OpenAPI artifact without an OBI.
type Source struct {
	Location string
	Content  []byte
	Document *openapi3.T
	// Artifact carries a document together with edition-specific raw-resource
	// state that a typed OpenAPI model cannot preserve. Callers that preload a
	// 3.2 description must pass the Artifact, not only Document.
	Artifact *Artifact
}

// ArtifactLoadOptions configures OpenAPI document retrieval without adding
// binding- or OBI-specific policy to the native client.
type ArtifactLoadOptions struct {
	HTTPClient        *http.Client
	AllowExternalRefs bool
}

type Requirement struct {
	Type        string
	Name        string
	Durable     *bool
	Description string
	Extra       map[string]any
}

type RequirementAlternative struct {
	Requirements []Requirement
}

type Prerequisites struct {
	Target       string
	Alternatives []RequirementAlternative
}

type Metadata map[string][]string

type HookSite struct {
	Ref     string
	Target  string
	Profile string
}

type RawResult struct {
	Status *int
	Body   []byte
	Meta   Metadata
}

// Hooks are OpenAPI-native customization points. handled=false declines to
// the engine builtin.
type Hooks struct {
	Decode   func(HookSite, RawResult) (value any, handled bool, err error)
	Classify func(HookSite, RawResult) (value bool, handled bool, err error)
}

type SecurityHandlerContext struct {
	SchemeName string
	Scheme     any
	Operation  OperationInfo
}

type SecurityHandler func(*http.Request, SecurityHandlerContext) error

// ParameterConverter deterministically converts a JSON boolean, number, or
// (where the governing edition admits a conversion seam) null to the string
// consumed by OpenAPI parameter serialization. Strings pass through
// unchanged. The function must be safe for concurrent calls.
type ParameterConverter func(value any) (string, error)

// ParameterInQueryString is the OpenAPI 3.2 Parameter Object location whose
// application value supplies the complete query component. kin-openapi's
// typed Parameter retains the literal but does not publish a constant for it.
const ParameterInQueryString = "querystring"

// ContentEncoder and ContentDecoder are deterministic whole-representation
// HTTP content-coding capabilities. Request encoders run in field order;
// response decoders run in reverse field order.
type ContentEncoder func([]byte) ([]byte, error)
type ContentDecoder func([]byte) ([]byte, error)
type CharacterEncoder func(string) ([]byte, error)
type CharacterDecoder func([]byte) (string, error)

type ImplicitConnectionScope string

const (
	ImplicitConnectionEntry     ImplicitConnectionScope = "entry"
	ImplicitConnectionReferring ImplicitConnectionScope = "referring"
)

type PrepareOptions struct {
	Source                     Source
	Ref                        string
	Profile                    Profile
	Context                    map[string]any
	HTTPClient                 *http.Client
	Hooks                      *Hooks
	MaxDeliveryUnitBytes       int64
	SecurityHandlers           map[string]SecurityHandler
	ParameterConverter         ParameterConverter
	RequestContentCodings      map[string]ContentEncoder
	ResponseContentCodings     map[string]ContentDecoder
	RequestCharacterEncodings  map[string]CharacterEncoder
	ResponseCharacterEncodings map[string]CharacterDecoder
	// BufferEventStreams selects unary buffering for the OpenAPI 3.0/3.1 SSE
	// compatibility lane. OpenAPI 3.2 sequential responses always stream.
	BufferEventStreams bool
	// OmitAcceptHeader suppresses the client's artifact-derived response media
	// preference while leaving response selection and decoding unchanged.
	OmitAcceptHeader  bool
	AllowExternalRefs *bool
}

type Event struct {
	Value    any
	Metadata Metadata
}

type Diagnostics struct {
	Leading  Metadata
	Trailing Metadata
}
