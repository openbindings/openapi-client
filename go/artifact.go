package openapiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/yaml"
	yaml3 "github.com/oasdiff/yaml3"
)

// Edition is the exact OpenAPI value that governed a loaded artifact.
// Classification happens from the entry resource before reference resolution.
type Edition string

const (
	EditionOpenAPI300 Edition = "3.0.0"
	EditionOpenAPI301 Edition = "3.0.1"
	EditionOpenAPI302 Edition = "3.0.2"
	EditionOpenAPI303 Edition = "3.0.3"
	EditionOpenAPI304 Edition = "3.0.4"
	EditionOpenAPI310 Edition = "3.1.0"
	EditionOpenAPI311 Edition = "3.1.1"
	EditionOpenAPI312 Edition = "3.1.2"
	EditionOpenAPI320 Edition = "3.2.0"
)

func (e Edition) IsOpenAPI32() bool { return e == EditionOpenAPI320 }

// Artifact is one loaded OpenAPI description. Edition-specific state travels
// with the typed document so concurrent loads cannot observe one another.
type Artifact struct {
	Document *openapi3.T
	Edition  Edition

	entryBytes       []byte
	openAPI32        *OpenAPI32Overlay
	operationTargets map[string]*OperationTarget
	operationErrors  map[string]error
	sourceRefusal    string
	sourceExclusion  string
}

func openAPI32ArtifactDisposition(document *openapi3.T) (refusal, exclusion string) {
	if document == nil || document.OpenAPI != string(EditionOpenAPI320) {
		return "", ""
	}
	if document.Components == nil && document.Paths == nil && document.Webhooks == nil {
		refusal = "OpenAPI 3.2 document omits components, paths, and webhooks, leaving no addressable-target position"
	}
	if document.JSONSchemaDialect != "" && document.JSONSchemaDialect != "https://spec.openapis.org/oas/3.1/dialect/base" {
		exclusion = fmt.Sprintf("OpenAPI 3.2 document jsonSchemaDialect %q is outside the supported default dialect", document.JSONSchemaDialect)
	}
	return refusal, exclusion
}

func (o *OpenAPI32Overlay) artifactDisposition() (refusal, exclusion string) {
	if o == nil {
		return "", ""
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.entry == nil {
		return "", ""
	}
	root, _ := o.entry.root.(map[string]any)
	if root == nil {
		return "", ""
	}
	_, components := root["components"]
	_, paths := root["paths"]
	_, webhooks := root["webhooks"]
	if !components && !paths && !webhooks {
		refusal = "OpenAPI 3.2 document omits components, paths, and webhooks, leaving no addressable-target position"
	}
	if dialect, ok := root["jsonSchemaDialect"].(string); ok && dialect != "" && dialect != "https://spec.openapis.org/oas/3.1/dialect/base" {
		exclusion = fmt.Sprintf("OpenAPI 3.2 document jsonSchemaDialect %q is outside the supported default dialect", dialect)
	}
	return refusal, exclusion
}

// EntryBytes returns a copy of the entry resource before private loader
// normalization. It is useful to native processors that own acceptance or
// coverage policy above this client substrate.
func (a *Artifact) EntryBytes() []byte {
	if a == nil {
		return nil
	}
	return append([]byte(nil), a.entryBytes...)
}

// OpenAPI32 returns the edition-specific raw-resource overlay, or nil for an
// artifact from another edition line.
func (a *Artifact) OpenAPI32() *OpenAPI32Overlay {
	if a == nil {
		return nil
	}
	return a.openAPI32
}

// Refusal reports a post-load whole-source refusal. OpenAPI 3.2 uses this
// when the root omits components, paths, and webhooks: it is deliberately not
// part of the edition's closed load-gate set and not a dialect exclusion.
func (a *Artifact) Refusal() error {
	if a == nil || a.sourceRefusal == "" {
		return nil
	}
	return fmt.Errorf("%s", a.sourceRefusal)
}

// SourceExclusion reports an edition-authoritative source-scope exclusion.
// It is deliberately distinct from the closed load-gate failures.
func (a *Artifact) SourceExclusion() error {
	if a == nil || a.sourceExclusion == "" {
		return nil
	}
	return fmt.Errorf("%s", a.sourceExclusion)
}

// OpenAPI32Resource is the immutable public description of one raw resource
// observed while loading a 3.2 artifact.
type OpenAPI32Resource struct {
	RetrievalURI string
	IdentityURI  string
	Self         string
}

type openAPI32RawResource struct {
	public      OpenAPI32Resource
	root        any
	retrieval   *url.URL
	base        *url.URL
	self        *url.URL
	selfPresent bool
	selfError   string
	entry       bool
}

// OpenAPI32Overlay preserves 3.2 fields that kin-openapi's typed model does
// not own. It is deliberately artifact-local; it is never registered in a
// package global keyed by *openapi3.T.
type OpenAPI32Overlay struct {
	mu           sync.RWMutex
	entry        *openAPI32RawResource
	resources    map[string]*openAPI32RawResource
	schemaScopes map[string]*rawSchemaResourceScope
}

func newOpenAPI32Overlay() *OpenAPI32Overlay {
	return &OpenAPI32Overlay{
		resources:    map[string]*openAPI32RawResource{},
		schemaScopes: map[string]*rawSchemaResourceScope{},
	}
}

// Entry returns the entry resource's 3.2 identity facts.
func (o *OpenAPI32Overlay) Entry() (OpenAPI32Resource, bool) {
	if o == nil {
		return OpenAPI32Resource{}, false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.entry == nil {
		return OpenAPI32Resource{}, false
	}
	return o.entry.public, true
}

func (o *OpenAPI32Overlay) capture(data []byte, requested, retrieval *url.URL, entry bool) error {
	root, err := parseRawOpenAPIResource(data)
	if err != nil {
		return err
	}
	object, _ := root.(map[string]any)
	resource := &openAPI32RawResource{root: root, entry: entry}
	if retrieval != nil {
		resource.retrieval = cloneURL(retrieval)
		resource.base = cloneURL(retrieval)
		resource.public.RetrievalURI = artifactResourceKey(retrieval)
	}
	if object != nil {
		if rawSelf, present := object["$self"]; present {
			resource.selfPresent = true
			selfText, ok := rawSelf.(string)
			switch {
			case !ok:
				resource.selfError = "$self is not a string URI-reference"
			default:
				resource.public.Self = selfText
				if parsed, parseErr := url.Parse(selfText); parseErr != nil {
					resource.selfError = fmt.Sprintf("$self %q is not a URI-reference", selfText)
				} else {
					switch {
					case parsed.IsAbs():
						resource.self = parsed
					case resource.base != nil:
						resource.self = resource.base.ResolveReference(parsed)
					default:
						resource.selfError = fmt.Sprintf("relative $self %q has no retrieval base", selfText)
					}
				}
			}
		}
	}
	if resource.self != nil {
		resource.base = cloneURL(resource.self)
		resource.public.IdentityURI = artifactResourceKey(resource.self)
	} else if resource.selfPresent {
		resource.base = nil
	} else {
		resource.public.IdentityURI = artifactResourceKey(resource.base)
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if entry {
		o.entry = resource
	}
	for _, candidate := range []*url.URL{requested, retrieval, resource.self} {
		if key := artifactResourceKey(candidate); key != "" {
			o.resources[key] = resource
		}
	}
	for identity, scope := range rawSchemaResourceScopes(root, resource.base) {
		o.schemaScopes[identity] = scope
	}
	return nil
}

func (o *OpenAPI32Overlay) baseFor(resource *url.URL) *url.URL {
	if o == nil {
		return resource
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if resource == nil {
		if o.entry != nil {
			return cloneURL(o.entry.base)
		}
		return nil
	}
	if raw := o.resources[artifactResourceKey(resource)]; raw != nil {
		// An unusable $self must not become semantic fallback to the retrieval
		// URI. The typed parser may nevertheless retrieve against that URI so
		// the artifact survives to the selected-closure exclusion below.
		if raw.selfError != "" {
			return cloneURL(raw.retrieval)
		}
		if raw.selfPresent {
			return cloneURL(raw.base)
		}
		if raw.base != nil {
			return cloneURL(raw.base)
		}
	}
	return resource
}

func (o *OpenAPI32Overlay) selectedPathItem(path, method string, additional bool) (map[string]any, bool, error) {
	if o == nil {
		return nil, false, nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.entry == nil {
		return nil, false, nil
	}
	root, _ := o.entry.root.(map[string]any)
	paths, _ := root["paths"].(map[string]any)
	adjacent, _ := paths[path].(map[string]any)
	if adjacent == nil {
		return nil, false, nil
	}
	refText, _ := adjacent["$ref"].(string)
	if refText == "" {
		if err := o.validateSelectedRequestReferences(adjacent, o.entry.base, method, additional); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	parsed, err := url.Parse(refText)
	if err != nil {
		return nil, false, fmt.Errorf("selected Path Item reference %q is invalid", refText)
	}
	base := o.entry.base
	var resolved *url.URL
	switch {
	case strings.HasPrefix(refText, "#"):
		resolved = cloneURL(base)
		if resolved == nil {
			resolved = &url.URL{}
		}
		resolved.Fragment = parsed.Fragment
	case parsed.IsAbs():
		resolved = parsed
	case base != nil:
		resolved = base.ResolveReference(parsed)
	default:
		return nil, false, fmt.Errorf("selected Path Item reference %q has no document base", refText)
	}
	resourceKey := artifactResourceKey(resolved)
	resource := o.resources[resourceKey]
	if resource == nil && strings.HasPrefix(refText, "#") {
		resource = o.entry
	}
	if resource == nil {
		return nil, false, nil // the typed loader owns an ordinary unresolved-reference error
	}
	if resource.selfError != "" {
		return nil, false, fmt.Errorf("selected Path Item reaches a resource with unusable %s", resource.selfError)
	}
	if resource.self != nil && resourceKey != artifactResourceKey(resource.self) {
		return nil, false, fmt.Errorf("selected Path Item reference uses retrieval alias %q instead of declared $self identity %q", resourceKey, artifactResourceKey(resource.self))
	}
	target, ok := rawFragmentTarget(resource.root, resolved.Fragment, rawPathItemTarget)
	if !ok {
		return nil, false, nil
	}
	referenced, _ := target.(map[string]any)
	if referenced == nil {
		return nil, false, nil
	}
	for _, field := range []string{"parameters", "servers"} {
		if _, left := adjacent[field]; left {
			if _, right := referenced[field]; right {
				return nil, false, fmt.Errorf("selected Path Item $ref has undefined adjacent collision at %q", field)
			}
		}
	}
	if additional {
		left, _ := adjacent["additionalOperations"].(map[string]any)
		right, _ := referenced["additionalOperations"].(map[string]any)
		if _, leftPresent := left[method]; leftPresent {
			if _, rightPresent := right[method]; rightPresent {
				return nil, false, fmt.Errorf("selected Path Item $ref has undefined adjacent collision at additional operation %q", method)
			}
		}
	} else if _, left := adjacent[method]; left {
		if _, right := referenced[method]; right {
			return nil, false, fmt.Errorf("selected Path Item $ref has undefined adjacent collision at %q", method)
		}
	}
	if err := o.validateSelectedRequestReferences(referenced, resource.base, method, additional); err != nil {
		return nil, false, err
	}
	if err := o.validateSelectedRequestReferences(adjacent, o.entry.base, method, additional); err != nil {
		return nil, false, err
	}
	merged := make(map[string]any, len(referenced)+len(adjacent))
	for key, value := range referenced {
		merged[key] = value
	}
	for key, value := range adjacent {
		if key == "$ref" {
			continue
		}
		if key == "additionalOperations" {
			combined, _ := merged[key].(map[string]any)
			copyMap := make(map[string]any, len(combined))
			for method, operation := range combined {
				copyMap[method] = operation
			}
			if adjacentOperations, ok := value.(map[string]any); ok {
				for method, operation := range adjacentOperations {
					copyMap[method] = operation
				}
				merged[key] = copyMap
			} else {
				merged[key] = value
			}
			continue
		}
		merged[key] = value
	}
	return merged, true, nil
}

func (o *OpenAPI32Overlay) validateSelectedRequestReferences(pathItem map[string]any, base *url.URL, method string, additional bool) error {
	if pathItem == nil {
		return nil
	}
	projection := map[string]any{}
	for _, field := range []string{"parameters", "servers"} {
		if value, present := pathItem[field]; present {
			projection[field] = value
		}
	}
	var operation any
	if additional {
		operations, _ := pathItem["additionalOperations"].(map[string]any)
		operation = operations[method]
	} else {
		operation = pathItem[method]
	}
	if rawOperation, ok := operation.(map[string]any); ok {
		request := map[string]any{}
		for _, field := range []string{"parameters", "requestBody", "servers", "security"} {
			if value, present := rawOperation[field]; present {
				request[field] = value
			}
		}
		projection["operation"] = request
	}
	return o.validateRawReferences(projection, base, false, map[string]bool{})
}

func (o *OpenAPI32Overlay) validateRawReferences(value any, base *url.URL, schema bool, seen map[string]bool) error {
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			if err := o.validateRawReferences(child, base, schema, seen); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if schema {
			if dialect, present := typed["$schema"]; present {
				value, ok := dialect.(string)
				if !ok || value != "https://spec.openapis.org/oas/3.1/dialect/base" {
					return fmt.Errorf("selected Schema Object resource uses unsupported $schema dialect %q", value)
				}
			}
			if id, ok := typed["$id"].(string); ok {
				parsed, err := url.Parse(id)
				if err == nil {
					switch {
					case parsed.IsAbs():
						base = parsed
					case base != nil:
						base = base.ResolveReference(parsed)
					}
				}
			}
		}
		if refText, ok := typed["$ref"].(string); ok {
			if err := o.validateOneRawReference(refText, base, schema, seen); err != nil {
				return err
			}
			if !schema {
				return nil // Reference Object siblings carry no request behavior.
			}
		}
		for key, child := range typed {
			if key == "$ref" || key == "example" || key == "examples" || (schema && (key == "default" || key == "const" || key == "enum")) {
				continue
			}
			childSchema := schema || key == "schema" || key == "itemSchema"
			if err := o.validateRawReferences(child, base, childSchema, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o *OpenAPI32Overlay) validateOneRawReference(refText string, base *url.URL, schema bool, seen map[string]bool) error {
	parsed, err := url.Parse(refText)
	if err != nil {
		return fmt.Errorf("selected request reference %q is invalid", refText)
	}
	var resolved *url.URL
	switch {
	case strings.HasPrefix(refText, "#"):
		resolved = cloneURL(base)
		if resolved == nil {
			resolved = &url.URL{}
		}
		resolved.Fragment = parsed.Fragment
	case parsed.IsAbs():
		resolved = parsed
	case base != nil:
		resolved = base.ResolveReference(parsed)
	default:
		return fmt.Errorf("selected request reference %q has no document base", refText)
	}
	key := resolved.String()
	if seen[key] {
		return nil
	}
	seen[key] = true
	resourceKey := artifactResourceKey(resolved)
	resource := o.resources[resourceKey]
	if resource == nil && strings.HasPrefix(refText, "#") {
		resource = o.entry
	}
	if resource != nil {
		if resource.selfError != "" {
			return fmt.Errorf("selected request reference reaches a resource with unusable %s", resource.selfError)
		}
		if resource.self != nil && resourceKey != artifactResourceKey(resource.self) {
			return fmt.Errorf("selected request reference uses retrieval alias %q instead of declared $self identity %q", resourceKey, artifactResourceKey(resource.self))
		}
		if schema && rawPointerCrossesSchemaResource(resource.root, resolved.Fragment) {
			return fmt.Errorf("selected Schema Object reference crosses a nearer $id resource boundary noncanonically")
		}
		kind := rawParameterTarget
		if schema {
			kind = rawSchemaTarget
		}
		target, ok := rawFragmentTarget(resource.root, resolved.Fragment, kind)
		if !ok {
			return nil
		}
		return o.validateRawReferences(target, resource.base, schema, seen)
	}
	if schema {
		if scope := o.schemaScopes[resourceKey]; scope != nil {
			target, targetBase, ok := scope.fragment(resolved.Fragment)
			if ok {
				return o.validateRawReferences(target, targetBase, true, seen)
			}
		}
	}
	return nil
}

func rawPointerCrossesSchemaResource(root any, fragment string) bool {
	if !strings.HasPrefix(fragment, "/") {
		return false
	}
	current := root
	for _, token := range rawPointerTokens(fragment) {
		if object, ok := current.(map[string]any); ok {
			if _, hasID := object["$id"].(string); hasID {
				return true
			}
			current = object[token]
			continue
		}
		if sequence, ok := current.([]any); ok {
			index, valid := rawSequenceIndex(token, len(sequence))
			if !valid {
				return false
			}
			current = sequence[index]
			continue
		}
		return false
	}
	if object, ok := current.(map[string]any); ok {
		_, hasID := object["$id"].(string)
		return hasID
	}
	return false
}

func parseRawOpenAPIResource(data []byte) (any, error) {
	// oasdiff/yaml intentionally decodes the first stream document. The 3.2
	// binding admits exactly one, so inspect the stream with the same underlying
	// YAML implementation before performing its YAML-to-JSON conversion.
	decoder := yaml3.NewDecoder(bytes.NewReader(data))
	decoder.DisableTimestamps(true)
	var first any
	if err := decoder.Decode(&first); err != nil {
		return nil, err
	}
	var second any
	if err := decoder.Decode(&second); err == nil {
		return nil, fmt.Errorf("OpenAPI YAML stream must contain exactly one document")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	var root any
	if _, err := yaml.Unmarshal(data, &root, yaml.DecodeOpts{DisableTimestamps: true}); err != nil {
		return nil, err
	}
	// This is the YAML-to-JSON compatibility gate. Marshaling is used only as
	// a representability proof; the raw tree above remains the overlay image.
	if _, err := json.Marshal(root); err != nil {
		return nil, fmt.Errorf("OpenAPI YAML value has no JSON image: %w", err)
	}
	return root, nil
}

// ClassifyOpenAPIEdition applies the closed representation/root/edition load
// gates to an entry resource without resolving a single artifact reference.
func ClassifyOpenAPIEdition(data []byte) (Edition, error) {
	root, err := parseRawOpenAPIResource(data)
	if err != nil {
		return "", err
	}
	object, ok := root.(map[string]any)
	if !ok {
		return "", fmt.Errorf("OpenAPI entry resource must be a JSON object")
	}
	raw, present := object["openapi"]
	if !present {
		return "", fmt.Errorf("OpenAPI entry resource has no required string `openapi` field")
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("OpenAPI entry resource `openapi` field must be a string")
	}
	edition := Edition(value)
	switch edition {
	case EditionOpenAPI300, EditionOpenAPI301, EditionOpenAPI302, EditionOpenAPI303, EditionOpenAPI304,
		EditionOpenAPI310, EditionOpenAPI311, EditionOpenAPI312, EditionOpenAPI320:
		return edition, nil
	default:
		return "", fmt.Errorf("unsupported OpenAPI version %q", value)
	}
}

// LoadArtifact loads a native OpenAPI artifact while preserving any
// edition-specific raw-resource state needed after typed parsing.
func LoadArtifact(ctx context.Context, source Source, options ArtifactLoadOptions) (*Artifact, error) {
	client := options.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	}
	artifact, _, err := loadArtifact(ctx, client, source, options.AllowExternalRefs)
	return artifact, err
}
