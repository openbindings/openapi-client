package openapiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	yaml3 "github.com/oasdiff/yaml3"
)

// Swagger20Client is the OpenAPI-native client view of one loaded Swagger 2.0
// artifact. Execution mechanics are added edition-by-edition without lowering
// the document into an OpenAPI 3.x model.
type Swagger20Client struct {
	document *Swagger20Document
	source   Swagger20Source
	options  ClientOptions
}

// LoadSwagger20 loads only the exact Swagger 2.0 family. It never falls back
// to the OpenAPI 3.x lane when the swagger gate refuses.
func LoadSwagger20(ctx context.Context, source Swagger20Source, options ClientOptions) (*Swagger20Client, error) {
	client := options.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	}
	document, err := loadSwagger20Document(ctx, client, source, true)
	if err != nil {
		return nil, &ClientError{Kind: ErrorSource, Code: CodeSourceLoadFailed, Message: err.Error(), Cause: err}
	}
	source.Document = document
	source.Content = nil
	return &Swagger20Client{document: document, source: source, options: options}, nil
}

func (c *Swagger20Client) Document() *Swagger20Document {
	if c == nil {
		return nil
	}
	return c.document
}

func (c *Swagger20Client) Operations() []OperationInfo {
	if c == nil || c.document == nil {
		return nil
	}
	return c.document.operations()
}

func loadSwagger20Document(ctx context.Context, client *http.Client, source Swagger20Source, allowExternalRefs bool) (*Swagger20Document, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = defaultHTTPClient()
	}
	if source.Document != nil {
		if source.Document.Swagger() != "2.0" || source.Document.graph == nil || source.Document.entry == nil {
			return nil, fmt.Errorf("unsupported Swagger version %q: expected exact string \"2.0\"", source.Document.Swagger())
		}
		return source.Document.rebind(ctx, client, allowExternalRefs), nil
	}

	var requested *url.URL
	var err error
	if source.Location != "" {
		requested, err = absoluteDocumentURL(source.Location)
		if err != nil {
			return nil, err
		}
	}
	if source.Content == nil && requested == nil {
		return nil, fmt.Errorf("Swagger 2.0 source requires location or content")
	}

	graph := newSwagger20ReferenceGraph(ctx, client, source.Content != nil && requested == nil, allowExternalRefs)
	var data []byte
	if source.Content != nil {
		data = append([]byte(nil), source.Content...)
	} else {
		data, err = graph.read(requested)
		if err != nil {
			return nil, err
		}
	}

	root, err := parseSwagger20Resource(data)
	if err != nil {
		return nil, err
	}
	object, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Swagger 2.0 document root must be a JSON object")
	}
	model := swagger20Object(object)
	swagger := model.string("swagger")
	if !swagger.present || !swagger.valid || swagger.value != "2.0" {
		if !swagger.present {
			return nil, fmt.Errorf("Swagger 2.0 document requires root swagger field with exact string value \"2.0\"")
		}
		return nil, fmt.Errorf("unsupported Swagger version: root swagger must be exact string \"2.0\"")
	}
	entry := graph.rememberResource(requested, root)
	document := &Swagger20Document{root: model, entry: entry, graph: graph}
	return document, nil
}

func parseSwagger20Resource(data []byte) (any, error) {
	document, err := decodeSwagger20YAMLDocument(data)
	if err != nil {
		return nil, err
	}
	root, err := swagger20YAMLValue(document, map[*yaml3.Node]bool{}, map[*yaml3.Node]any{}, map[*yaml3.Node]bool{})
	if err != nil {
		return nil, fmt.Errorf("Swagger 2.0 representation has no RFC 7159 JSON image: %w", err)
	}

	// Round-trip through encoding/json to enforce the binding's JSON-image
	// gate after exact YAML 1.2.2 Core Schema scalar/tag/key resolution.
	image, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("Swagger 2.0 representation has no RFC 7159 JSON image: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(image))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("normalize Swagger 2.0 JSON image: %w", err)
	}
	return normalized, nil
}

func decodeSwagger20YAMLDocument(data []byte) (*yaml3.Node, error) {
	decoder := yaml3.NewDecoder(bytes.NewReader(data))
	decoder.DisableTimestamps(true)
	var document yaml3.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse Swagger 2.0 representation: %w", err)
	}
	var extra yaml3.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("parse Swagger 2.0 representation: %w", err)
		}
		return nil, fmt.Errorf("Swagger 2.0 representation must contain exactly one YAML document")
	}
	return &document, nil
}

func swagger20YAMLValue(node *yaml3.Node, active map[*yaml3.Node]bool, memo map[*yaml3.Node]any, done map[*yaml3.Node]bool) (any, error) {
	if node == nil {
		return nil, fmt.Errorf("empty YAML node")
	}
	if done[node] {
		return memo[node], nil
	}
	if active[node] {
		return nil, fmt.Errorf("cyclic YAML alias graph")
	}
	active[node] = true
	defer delete(active, node)

	if node.Style&yaml3.TaggedStyle != 0 && !swagger20JSONCompatibleYAMLTag(node.Tag) {
		return nil, fmt.Errorf("tag %q is outside the JSON-compatible YAML tag set", node.Tag)
	}

	var value any
	var err error
	switch node.Kind {
	case yaml3.DocumentNode:
		if len(node.Content) != 1 {
			return nil, fmt.Errorf("YAML document has %d roots", len(node.Content))
		}
		value, err = swagger20YAMLValue(node.Content[0], active, memo, done)
	case yaml3.MappingNode:
		if node.Style&yaml3.TaggedStyle != 0 && node.Tag != "!!map" {
			return nil, fmt.Errorf("tag %q cannot construct a mapping", node.Tag)
		}
		if len(node.Content)%2 != 0 {
			return nil, fmt.Errorf("malformed YAML mapping")
		}
		object := make(map[string]any, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			keyValue, keyErr := swagger20YAMLValue(node.Content[index], active, memo, done)
			if keyErr != nil {
				return nil, keyErr
			}
			key, ok := keyValue.(string)
			if !ok {
				return nil, fmt.Errorf("mapping key at line %d is not a scalar string", node.Content[index].Line)
			}
			if _, duplicate := object[key]; duplicate {
				return nil, fmt.Errorf("duplicate mapping key %q at line %d", key, node.Content[index].Line)
			}
			member, memberErr := swagger20YAMLValue(node.Content[index+1], active, memo, done)
			if memberErr != nil {
				return nil, memberErr
			}
			object[key] = member
		}
		value = object
	case yaml3.SequenceNode:
		if node.Style&yaml3.TaggedStyle != 0 && node.Tag != "!!seq" {
			return nil, fmt.Errorf("tag %q cannot construct a sequence", node.Tag)
		}
		sequence := make([]any, len(node.Content))
		for index, child := range node.Content {
			sequence[index], err = swagger20YAMLValue(child, active, memo, done)
			if err != nil {
				return nil, err
			}
		}
		value = sequence
	case yaml3.ScalarNode:
		value, err = swagger20YAMLScalar(node)
	case yaml3.AliasNode:
		value, err = swagger20YAMLValue(node.Alias, active, memo, done)
	default:
		return nil, fmt.Errorf("unsupported YAML node kind %d", node.Kind)
	}
	if err != nil {
		return nil, err
	}
	memo[node] = value
	done[node] = true
	return value, nil
}

func swagger20JSONCompatibleYAMLTag(tag string) bool {
	switch tag {
	case "!!map", "!!seq", "!!str", "!!null", "!!bool", "!!int", "!!float":
		return true
	default:
		return false
	}
}

var (
	swagger20YAMLDecimalInteger = regexp.MustCompile(`^[-+]?[0-9]+$`)
	swagger20YAMLOctalInteger   = regexp.MustCompile(`^0o[0-7]+$`)
	swagger20YAMLHexInteger     = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	swagger20YAMLFloat          = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?$`)
)

func swagger20YAMLScalar(node *yaml3.Node) (any, error) {
	tagged := node.Style&yaml3.TaggedStyle != 0
	if !tagged && node.Style&(yaml3.DoubleQuotedStyle|yaml3.SingleQuotedStyle|yaml3.LiteralStyle|yaml3.FoldedStyle) != 0 {
		return node.Value, nil
	}
	if tagged {
		switch node.Tag {
		case "!!str":
			return node.Value, nil
		case "!!null":
			if swagger20YAMLNull(node.Value) {
				return nil, nil
			}
		case "!!bool":
			if value, ok := swagger20YAMLBoolean(node.Value); ok {
				return value, nil
			}
		case "!!int":
			if value, ok := swagger20YAMLInteger(node.Value); ok {
				return value, nil
			}
		case "!!float":
			if value, ok := swagger20YAMLNumber(node.Value); ok {
				return value, nil
			}
		}
		return nil, fmt.Errorf("scalar %q is invalid for explicit tag %q", node.Value, node.Tag)
	}
	if swagger20YAMLNonJSONFloat(node.Value) {
		return nil, fmt.Errorf("resolved float %q has no RFC 7159 JSON image", node.Value)
	}

	if swagger20YAMLNull(node.Value) {
		return nil, nil
	}
	if value, ok := swagger20YAMLBoolean(node.Value); ok {
		return value, nil
	}
	if value, ok := swagger20YAMLInteger(node.Value); ok {
		return value, nil
	}
	if value, ok := swagger20YAMLNumber(node.Value); ok {
		return value, nil
	}
	return node.Value, nil
}

func swagger20YAMLNonJSONFloat(value string) bool {
	switch strings.ToLower(value) {
	case ".nan", ".inf", "+.inf", "-.inf":
		return true
	default:
		return false
	}
}

func swagger20YAMLNull(value string) bool {
	switch value {
	case "", "~", "null", "Null", "NULL":
		return true
	default:
		return false
	}
}

func swagger20YAMLBoolean(value string) (bool, bool) {
	switch value {
	case "true", "True", "TRUE":
		return true, true
	case "false", "False", "FALSE":
		return false, true
	default:
		return false, false
	}
}

func swagger20YAMLInteger(value string) (json.Number, bool) {
	base := 10
	digits := value
	if swagger20YAMLOctalInteger.MatchString(value) {
		base = 8
		digits = strings.Replace(value, "0o", "", 1)
	} else if swagger20YAMLHexInteger.MatchString(value) {
		base = 16
		digits = strings.Replace(value, "0x", "", 1)
	} else if !swagger20YAMLDecimalInteger.MatchString(value) {
		return "", false
	}
	integer := new(big.Int)
	if _, ok := integer.SetString(digits, base); !ok {
		return "", false
	}
	return json.Number(integer.String()), true
}

func swagger20YAMLNumber(value string) (json.Number, bool) {
	if !swagger20YAMLFloat.MatchString(value) {
		return "", false
	}
	return json.Number(normalizeSwagger20YAMLFloat(value)), true
}

func normalizeSwagger20YAMLFloat(value string) string {
	sign := ""
	if strings.HasPrefix(value, "+") {
		value = value[1:]
	} else if strings.HasPrefix(value, "-") {
		sign, value = "-", value[1:]
	}
	mantissa, exponent := value, ""
	if index := strings.IndexAny(value, "eE"); index >= 0 {
		mantissa, exponent = value[:index], value[index:]
	}
	integer, fraction, dotted := strings.Cut(mantissa, ".")
	integer = strings.TrimLeft(integer, "0")
	if integer == "" {
		integer = "0"
	}
	if dotted {
		if fraction == "" {
			fraction = "0"
		}
		mantissa = integer + "." + fraction
	} else {
		mantissa = integer
	}
	return sign + mantissa + exponent
}

type swagger20Resource struct {
	requested *url.URL
	retrieval *url.URL
	root      any
}

func (r *swagger20Resource) base() *url.URL {
	if r == nil {
		return nil
	}
	if r.retrieval != nil {
		return cloneURL(r.retrieval)
	}
	return cloneURL(r.requested)
}

type swagger20ReferenceGraph struct {
	ctx               context.Context
	client            *http.Client
	selfContained     bool
	allowExternalRefs bool

	mu            sync.RWMutex
	resources     map[string]*swagger20Resource
	bytes         map[string][]byte
	retrievalURIs map[string]*url.URL
	retrievalMu   sync.RWMutex
}

func newSwagger20ReferenceGraph(ctx context.Context, client *http.Client, selfContained, allowExternalRefs bool) *swagger20ReferenceGraph {
	return &swagger20ReferenceGraph{
		ctx: ctx, client: client, selfContained: selfContained, allowExternalRefs: allowExternalRefs,
		resources: map[string]*swagger20Resource{}, bytes: map[string][]byte{}, retrievalURIs: map[string]*url.URL{},
	}
}

func (g *swagger20ReferenceGraph) read(resource *url.URL) ([]byte, error) {
	if resource == nil {
		return nil, fmt.Errorf("Swagger 2.0 resource has no absolute URI")
	}
	key := artifactResourceKey(resource)
	g.mu.RLock()
	data, found := g.bytes[key]
	g.mu.RUnlock()
	if found {
		return append([]byte(nil), data...), nil
	}
	data, err := readArtifactResource(g.ctx, g.client, resource, false, g.retrievalURIs, &g.retrievalMu)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.bytes[key] = append([]byte(nil), data...)
	g.mu.Unlock()
	return data, nil
}

func (g *swagger20ReferenceGraph) rememberResource(requested *url.URL, root any) *swagger20Resource {
	retrieval := artifactRetrievalURI(requested, g.retrievalURIs, &g.retrievalMu)
	return g.rememberResolvedResource(requested, retrieval, root)
}

func (g *swagger20ReferenceGraph) rememberResolvedResource(requested, retrieval *url.URL, root any) *swagger20Resource {
	resource := &swagger20Resource{requested: cloneURL(requested), retrieval: cloneURL(retrieval), root: root}
	g.mu.Lock()
	if requested != nil {
		g.resources[artifactResourceKey(requested)] = resource
	}
	if retrieval != nil {
		g.resources[artifactResourceKey(retrieval)] = resource
	}
	if requested == nil && retrieval == nil {
		g.resources[""] = resource
	}
	g.mu.Unlock()
	return resource
}
