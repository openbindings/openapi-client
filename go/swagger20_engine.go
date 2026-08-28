package openapiclient

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Swagger20PrepareOptions is the edition-specific preparation surface. It is
// deliberately separate from PrepareOptions so Source.Document remains typed
// as *openapi3.T and the two edition lanes never share a structural model.
type Swagger20PrepareOptions struct {
	Source                 Swagger20Source
	Ref                    string
	Context                map[string]any
	HTTPClient             *http.Client
	Server                 string
	RequestMedia           string
	PropertyMedia          map[string]string
	ParameterConverter     ParameterConverter
	EmptyValueForm         Swagger20EmptyValueForm
	RequestContentCodings  map[string]ContentEncoder
	ResponseContentCodings map[string]ContentDecoder
	MaxDeliveryUnitBytes   int64
	AllowExternalRefs      *bool
}

type Swagger20PreparedOperation struct {
	document      *Swagger20Document
	operation     swagger20Operation
	info          OperationInfo
	options       Swagger20PrepareOptions
	parameterOnce sync.Once
	parameterSet  *swagger20ParameterSet
	parameterErr  error
}

func (p *Swagger20PreparedOperation) Ref() string {
	if p == nil {
		return ""
	}
	return p.info.Ref
}

func (p *Swagger20PreparedOperation) Info() OperationInfo {
	if p == nil {
		return OperationInfo{}
	}
	info := p.info
	info.Tags = append([]string(nil), info.Tags...)
	return info
}

// Parameters returns the effective Swagger 2.0 parameter identities after
// reference resolution, override, and declaration-only exclusion checks.
func (p *Swagger20PreparedOperation) Parameters() ([]Swagger20ParameterInfo, error) {
	parameters, err := p.parameters()
	if err != nil {
		return nil, err
	}
	result := make([]Swagger20ParameterInfo, len(parameters.all))
	for index, parameter := range parameters.all {
		result[index] = parameter.info()
	}
	return result, nil
}

func (p *Swagger20PreparedOperation) parameters() (*swagger20ParameterSet, error) {
	if p == nil {
		return nil, &ExecutionError{Code: CodeRefused, Message: "Swagger 2.0 prepared operation is nil"}
	}
	p.parameterOnce.Do(func() {
		p.parameterSet, p.parameterErr = effectiveSwagger20Parameters(p.document.graph, p.operation)
		if p.parameterErr != nil {
			p.parameterErr = &ExecutionError{Code: CodeRefused, Message: p.parameterErr.Error(), Cause: p.parameterErr}
		}
	})
	return p.parameterSet, p.parameterErr
}

// PrepareSwagger20 selects one operation through the exact Swagger 2.0 gate
// and JSON-Pointer selector grammar. It never invokes the OpenAPI 3.x loader.
func (e *Engine) PrepareSwagger20(ctx context.Context, options Swagger20PrepareOptions) (*Swagger20PreparedOperation, error) {
	requestCodings, err := normalizeContentEncoders(options.RequestContentCodings)
	if err != nil {
		return nil, &ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err}
	}
	responseCodings, err := normalizeContentDecoders(options.ResponseContentCodings)
	if err != nil {
		return nil, &ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err}
	}
	options.RequestContentCodings = requestCodings
	options.ResponseContentCodings = responseCodings
	loadClient := options.HTTPClient
	if loadClient == nil && e != nil {
		loadClient = e.client
	}
	if loadClient == nil {
		loadClient = defaultHTTPClient()
	}
	if options.HTTPClient == nil {
		if e != nil {
			options.HTTPClient = e.invocationClient
		} else {
			options.HTTPClient = defaultInvocationHTTPClient()
		}
	}
	allowExternal := true
	if options.AllowExternalRefs != nil {
		allowExternal = *options.AllowExternalRefs
	}
	document, err := loadSwagger20Document(ctx, loadClient, options.Source, allowExternal)
	if err != nil {
		return nil, &ExecutionError{Code: CodeSourceLoadFailed, Message: err.Error(), Cause: err}
	}
	operation, info, err := resolveSwagger20Operation(document, options.Ref)
	if err != nil {
		return nil, err
	}
	return &Swagger20PreparedOperation{document: document, operation: operation, info: info, options: options}, nil
}

func resolveSwagger20Operation(document *Swagger20Document, ref string) (swagger20Operation, OperationInfo, error) {
	path, method, err := parseSwagger20Ref(ref)
	if err != nil {
		return swagger20Operation{}, OperationInfo{}, &ExecutionError{Code: CodeInvalidRef, Message: err.Error(), Cause: err}
	}
	paths := swagger20DocumentObject{raw: document.root, resource: document.entry}.paths()
	if !paths.present || !paths.valid {
		return swagger20Operation{}, OperationInfo{}, &ExecutionError{Code: CodeRefused, Message: "Swagger 2.0 document has no usable paths object"}
	}
	rawItem, present := paths.value.member(path)
	if !present {
		return swagger20Operation{}, OperationInfo{}, &ExecutionError{Code: CodeRefNotFound, Message: fmt.Sprintf("operation %q was not found", ref)}
	}
	itemObject, ok := rawItem.(map[string]any)
	if !ok {
		return swagger20Operation{}, OperationInfo{}, &ExecutionError{Code: CodeRefNotFound, Message: fmt.Sprintf("operation %q was not found", ref)}
	}
	item := swagger20PathItem{raw: swagger20Object(itemObject), resource: document.entry}
	item, err = document.graph.resolvePathItem(item, method, newSwagger20ResolutionMemo())
	if err != nil {
		return swagger20Operation{}, OperationInfo{}, &ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err}
	}
	declared := item.operation(method)
	if !declared.present || !declared.valid {
		return swagger20Operation{}, OperationInfo{}, &ExecutionError{Code: CodeRefNotFound, Message: fmt.Sprintf("operation %q was not found", ref)}
	}
	operation := swagger20Operation{
		raw: declared.value, resource: item.resourceFor(method), pathItem: item, path: path, method: method,
	}
	return operation, operation.operationInfo(ref), nil
}

func parseSwagger20Ref(ref string) (path string, method string, err error) {
	const prefix = "#/paths/"
	if !strings.HasPrefix(ref, prefix) {
		return "", "", fmt.Errorf("selector %q must have exact form #/paths/<escaped-path>/<lowercase-method>", ref)
	}
	parts := strings.Split(ref[len(prefix):], "/")
	if len(parts) != 2 || parts[0] == "" {
		return "", "", fmt.Errorf("selector %q must have exact form #/paths/<escaped-path>/<lowercase-method>", ref)
	}
	if !wellFormedPointerToken(parts[0]) {
		return "", "", fmt.Errorf("selector %q contains a malformed RFC 6901 path token", ref)
	}
	method = parts[1]
	if !swagger20Method(method) {
		return "", "", fmt.Errorf("selector %q has inadmissible lowercase Swagger 2.0 method %q", ref, method)
	}
	path = decodePointerToken(parts[0])
	return path, method, nil
}

// ValidateSwagger20Selector validates the exact Swagger 2.0 operation
// selector spelling without loading or dereferencing an artifact.
func ValidateSwagger20Selector(selector string) error {
	_, _, err := parseSwagger20Ref(selector)
	return err
}

func (d *Swagger20Document) operations() []OperationInfo {
	if d == nil || d.graph == nil || d.entry == nil {
		return nil
	}
	paths := swagger20DocumentObject{raw: d.root, resource: d.entry}.paths()
	if !paths.present || !paths.valid {
		return nil
	}
	pathNames := make([]string, 0, len(paths.value))
	for path := range paths.value {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)
	methods := []string{"get", "put", "post", "delete", "options", "head", "patch"}
	var result []OperationInfo
	for _, path := range pathNames {
		for _, method := range methods {
			ref := "#/paths/" + escapeJSONPointerSegment(path) + "/" + method
			_, info, err := resolveSwagger20Operation(d, ref)
			if err == nil {
				result = append(result, info)
			}
		}
	}
	return result
}
