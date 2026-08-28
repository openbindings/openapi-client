package openapiclient

import (
	"context"
	"encoding/json"
	"net/http"
)

// Swagger20Source identifies an OpenAPI 2.0 (Swagger) artifact without an
// OpenBindings document. Content is the artifact's JSON or YAML source image;
// a co-present Location supplies its canonical reference base.
type Swagger20Source struct {
	Location string
	Content  []byte
	Document *Swagger20Document
}

// Swagger20Document is the native, raw-preserving OpenAPI 2.0 document model.
// Its representation stays private so callers cannot mutate a prepared
// reference graph; MarshalJSON exposes a lossless JSON image when inspection
// is needed.
type Swagger20Document struct {
	root  swagger20Object
	entry *swagger20Resource
	graph *swagger20ReferenceGraph
}

func (d *Swagger20Document) rebind(ctx context.Context, client *http.Client, allowExternalRefs bool) *Swagger20Document {
	graph := newSwagger20ReferenceGraph(ctx, client, d.graph.selfContained, allowExternalRefs)
	entry := graph.rememberResolvedResource(d.entry.requested, d.entry.retrieval, d.root)
	return &Swagger20Document{root: d.root, entry: entry, graph: graph}
}

func (d *Swagger20Document) MarshalJSON() ([]byte, error) {
	if d == nil {
		return []byte("null"), nil
	}
	return json.Marshal(d.root)
}

// Swagger reports the exact root discriminator retained by the native model.
func (d *Swagger20Document) Swagger() string {
	if d == nil {
		return ""
	}
	return d.root.string("swagger").value
}

// Presence-aware members keep absence, explicit empty values, wrong JSON
// types, and valid values distinct. The 2.0 execution and synthesis lanes use
// these accessors instead of zero-valued Go struct fields.
type swagger20Member[T any] struct {
	value   T
	present bool
	valid   bool
}

type swagger20Object map[string]any

func (o swagger20Object) member(name string) (any, bool) {
	if o == nil {
		return nil, false
	}
	value, present := o[name]
	return value, present
}

func (o swagger20Object) string(name string) swagger20Member[string] {
	value, present := o.member(name)
	if !present {
		return swagger20Member[string]{}
	}
	text, valid := value.(string)
	return swagger20Member[string]{value: text, present: true, valid: valid}
}

func (o swagger20Object) boolean(name string) swagger20Member[bool] {
	value, present := o.member(name)
	if !present {
		return swagger20Member[bool]{}
	}
	boolean, valid := value.(bool)
	return swagger20Member[bool]{value: boolean, present: true, valid: valid}
}

func (o swagger20Object) object(name string) swagger20Member[swagger20Object] {
	value, present := o.member(name)
	if !present {
		return swagger20Member[swagger20Object]{}
	}
	object, valid := value.(map[string]any)
	return swagger20Member[swagger20Object]{value: swagger20Object(object), present: true, valid: valid}
}

func (o swagger20Object) array(name string) swagger20Member[[]any] {
	value, present := o.member(name)
	if !present {
		return swagger20Member[[]any]{}
	}
	array, valid := value.([]any)
	return swagger20Member[[]any]{value: array, present: true, valid: valid}
}

type swagger20DocumentObject struct {
	raw      swagger20Object
	resource *swagger20Resource
}

func (d swagger20DocumentObject) paths() swagger20Member[swagger20Object] {
	return d.raw.object("paths")
}

type swagger20PathItem struct {
	raw             swagger20Object
	resource        *swagger20Resource
	memberResources map[string]*swagger20Resource
}

func (p swagger20PathItem) reference() swagger20Member[string] {
	return p.raw.string("$ref")
}

func (p swagger20PathItem) operation(method string) swagger20Member[swagger20Object] {
	return p.raw.object(method)
}

func (p swagger20PathItem) parameters() swagger20Member[[]any] {
	return p.raw.array("parameters")
}

func (p swagger20PathItem) resourceFor(member string) *swagger20Resource {
	if resource := p.memberResources[member]; resource != nil {
		return resource
	}
	return p.resource
}

type swagger20Operation struct {
	raw      swagger20Object
	resource *swagger20Resource
	pathItem swagger20PathItem
	path     string
	method   string
}

func (o swagger20Operation) operationInfo(ref string) OperationInfo {
	operationID := o.raw.string("operationId")
	summary := o.raw.string("summary")
	info := OperationInfo{
		Ref: ref, Path: o.path, Method: Method(o.method),
		OperationID: operationID.value, Summary: summary.value,
	}
	if tags := o.raw.array("tags"); tags.present && tags.valid {
		for _, value := range tags.value {
			if tag, ok := value.(string); ok {
				info.Tags = append(info.Tags, tag)
			}
		}
	}
	return info
}

func swagger20Method(method string) bool {
	switch method {
	case "get", "put", "post", "delete", "options", "head", "patch":
		return true
	default:
		return false
	}
}

func swagger20HTTPMethod(method string) string {
	switch method {
	case "get":
		return http.MethodGet
	case "put":
		return http.MethodPut
	case "post":
		return http.MethodPost
	case "delete":
		return http.MethodDelete
	case "options":
		return http.MethodOptions
	case "head":
		return http.MethodHead
	case "patch":
		return http.MethodPatch
	default:
		return ""
	}
}
