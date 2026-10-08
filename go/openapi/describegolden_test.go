package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Describing operations is pinned by a golden rendering. describe.go,
// Client.Operations: "Operations describes every operation of the document
// ... Operations that cannot be called are included, with Err set"; "The
// slice is new on each call; the Operations it points to are shared by every
// caller and must not be modified. Listing them does no schema work." The
// descriptors are the package's public description of a document, so the
// same documents must keep giving the same descriptors, field for field,
// whatever the client builds or stops building while describing.
//
// Regression check, not contract: two parts of the rendering are snapshots
// the documentation does not promise. One is each Err's text; an Err's
// presence, and whether it wraps ErrUnresolved (its "unresolved" flag), are
// contract. The other is the Raw of a schema the client makes, such as one
// made from a Swagger 2.0 parameter or response header (describe.go,
// Param.Schema) or from formData parameters, whose member order and spelling
// are the client's; the Raw of an authored schema is contract (describe.go,
// Schema.Raw: "exactly as written, as JSON").
//
// The golden file is generated from the client, not written by hand. Run
//
//	GOWORK=off go test ./openapi -run TestDescribeGolden -update-describe-golden
//
// to rewrite it, and justify every changed line against the package
// documentation.

var updateDescribeGolden = flag.Bool("update-describe-golden", false, "rewrite testdata/describe_golden.json from the client")

const describeGoldenFile = "testdata/describe_golden.json"

// goldenBase is the origin every golden document is served from.
const goldenBase = "https://golden.example.test/"

// goldenEdition is one edition's entry document and the documents its
// references reach, served from memory by Loader.Fetch. A reference to a
// URI missing here fails to retrieve, which the descriptors report.
type goldenEdition struct {
	name  string
	entry string // URI of the entry document
	docs  map[string]string
}

// goldenEditions are varied synthetic documents, one per edition. They
// exercise every descriptor field, inherited declarations, references
// (local, external and unresolvable), and the defects the descriptors
// report, but keep to declarations whose description the package
// documentation settles: no $ref member where the edition defines no
// Reference Object, and no deepObject array.
func goldenEditions() []goldenEdition {
	return []goldenEdition{
		{
			name:  "2.0",
			entry: goldenBase + "v2/swagger.json",
			docs: map[string]string{
				goldenBase + "v2/swagger.json": `{
 "swagger": "2.0",
 "info": {"title": "golden 2.0", "version": "1"},
 "host": "api.example.test",
 "basePath": "/v2",
 "schemes": ["https", "http"],
 "consumes": ["application/json"],
 "produces": ["application/json"],
 "securityDefinitions": {
  "basicAuth": {"type": "basic", "description": "basic credentials"},
  "key": {"type": "apiKey", "in": "header", "name": "X-Key"},
  "qkey": {"type": "apiKey", "in": "query", "name": "api_key"},
  "oauth": {"type": "oauth2", "flow": "accessCode", "authorizationUrl": "https://auth.example.test/authorize", "tokenUrl": "https://auth.example.test/token", "scopes": {"read": "read things", "write": "write things"}},
  "app": {"type": "oauth2", "flow": "application", "tokenUrl": "/token", "scopes": {}}
 },
 "security": [{"key": []}, {"oauth": ["read", "write", "read"]}],
 "parameters": {
  "limit": {"name": "limit", "in": "query", "type": "integer", "minimum": 1, "maximum": 100, "default": 10, "description": "page size"},
  "trace": {"name": "X-Trace", "in": "header", "type": "string"}
 },
 "responses": {
  "Error": {"description": "error", "schema": {"$ref": "#/definitions/Error"}, "headers": {"X-Request-Id": {"type": "string", "description": "request id"}}}
 },
 "definitions": {
  "Pet": {"type": "object", "required": ["name"], "properties": {"id": {"type": "integer", "format": "int64"}, "name": {"type": "string"}, "tag": {"$ref": "#/definitions/Tag"}}},
  "Tag": {"type": "string", "enum": ["a", "b"]},
  "Error": {"type": "object", "properties": {"code": {"type": "integer"}, "message": {"type": "string"}}}
 },
 "paths": {
  "/pets": {
   "parameters": [{"$ref": "#/parameters/trace"}],
   "get": {
    "operationId": "listPets", "summary": "List", "description": "Lists pets", "tags": ["pets", "read"],
    "parameters": [
     {"$ref": "#/parameters/limit"},
     {"name": "tags", "in": "query", "type": "array", "items": {"type": "string"}, "collectionFormat": "multi"},
     {"name": "ids", "in": "query", "type": "array", "items": {"type": "array", "items": {"type": "integer"}, "collectionFormat": "pipes"}},
     {"name": "flag", "in": "query", "type": "boolean", "allowEmptyValue": true},
     {"name": "Accept", "in": "header", "type": "string"}
    ],
    "produces": ["application/json", "application/xml"],
    "responses": {
     "200": {"description": "ok", "schema": {"type": "array", "items": {"$ref": "#/definitions/Pet"}}, "headers": {"X-Next": {"type": "string"}, "X-Rate": {"type": "array", "items": {"type": "integer"}, "collectionFormat": "csv"}}},
     "default": {"$ref": "#/responses/Error"}
    }
   },
   "post": {
    "operationId": "createPet",
    "security": [],
    "parameters": [
     {"name": "body", "in": "body", "required": true, "schema": {"$ref": "#/definitions/Pet"}},
     {"name": "Content-Type", "in": "header", "type": "string"}
    ],
    "responses": {"201": {"description": "created"}, "2xx": {"description": "lowercase range"}}
   }
  },
  "/pets/{petId}": {
   "parameters": [{"name": "petId", "in": "path", "required": true, "type": "string"}],
   "put": {
    "operationId": "uploadPhoto",
    "consumes": ["multipart/form-data", "application/x-www-form-urlencoded"],
    "security": [{"basicAuth": []}, {"qkey": [], "app": []}],
    "parameters": [
     {"name": "photo", "in": "formData", "type": "file", "required": true},
     {"name": "note", "in": "formData", "type": "string", "maxLength": 10},
     {"name": "sizes", "in": "formData", "type": "array", "items": {"type": "integer"}, "collectionFormat": "ssv"}
    ],
    "responses": {"204": {"description": "stored"}}
   },
   "delete": {
    "deprecated": true,
    "parameters": [{"$ref": "params.json#/hard"}],
    "responses": {"204": {"description": "gone"}}
   },
   "patch": {
    "parameters": [{"$ref": "#/parameters/missing"}],
    "responses": {"200": {"description": "ok"}}
   }
  },
  "/things/{thingId}": {"get": {"operationId": "badTemplate", "responses": {"200": {"description": "ok"}}}},
  "/ext": {"$ref": "pathitem.json"},
  "/gone": {"$ref": "missing.json"},
  "/schemes": {"get": {"schemes": ["wss"], "security": [{"nodef": []}], "responses": {"200": {"description": "ok"}}}}
 }
}`,
				goldenBase + "v2/params.json":   `{"hard": {"name": "hard", "in": "query", "type": "string", "required": true, "description": "remote"}}`,
				goldenBase + "v2/pathitem.json": `{"get": {"operationId": "extGet", "parameters": [{"$ref": "params.json#/hard"}], "responses": {"200": {"description": "ok", "schema": {"type": "string"}}}}}`,
			},
		},
		{
			name:  "3.0",
			entry: goldenBase + "v30/openapi.json",
			docs: map[string]string{
				goldenBase + "v30/openapi.json": `{
 "openapi": "3.0.4",
 "info": {"title": "golden 3.0", "version": "1"},
 "servers": [
  {"url": "https://{region}.api.example.test/{base}", "description": "regional", "variables": {"region": {"default": "eu", "enum": ["eu", "us"], "description": "region"}, "base": {"default": "v1"}}},
  {"url": "/relative"}
 ],
 "security": [{"bearer": []}],
 "components": {
  "securitySchemes": {
   "bearer": {"type": "http", "scheme": "bearer", "bearerFormat": "JWT"},
   "cookieKey": {"type": "apiKey", "in": "cookie", "name": "sid"},
   "oauth": {"type": "oauth2", "flows": {
    "implicit": {"authorizationUrl": "https://auth.example.test/a", "scopes": {"r": "read"}},
    "password": {"tokenUrl": "/t", "refreshUrl": "/r", "scopes": {}},
    "clientCredentials": {"tokenUrl": "/cc", "scopes": {"w": "write"}},
    "authorizationCode": {"authorizationUrl": "/ac", "tokenUrl": "/act", "scopes": {}}
   }},
   "oidc": {"type": "openIdConnect", "openIdConnectUrl": "https://auth.example.test/.well-known/openid-configuration"},
   "ext": {"$ref": "security.json#/scheme"}
  },
  "parameters": {"Page": {"name": "page", "in": "query", "schema": {"type": "integer"}}},
  "requestBodies": {"Pet": {"description": "a pet", "required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Pet"}}}}},
  "responses": {"NotFound": {"description": "not found", "content": {"application/problem+json": {"schema": {"$ref": "#/components/schemas/Problem"}}}}},
  "headers": {"Rate": {"description": "rate", "schema": {"type": "integer"}}},
  "schemas": {
   "Pet": {"type": "object", "nullable": true, "properties": {"name": {"type": "string"}, "photo": {"type": "string", "format": "binary"}, "meta": {"type": "object"}}},
   "Problem": {"type": "object"}
  }
 },
 "paths": {
  "/pets/{petId}": {
   "summary": "pet item", "description": "item description",
   "servers": [{"url": "https://pets.example.test"}],
   "parameters": [{"name": "petId", "in": "path", "required": true, "schema": {"type": "string"}, "style": "label", "explode": true}],
   "get": {
    "operationId": "getPet",
    "parameters": [
     {"$ref": "#/components/parameters/Page"},
     {"name": "filter", "in": "query", "style": "deepObject", "schema": {"type": "object", "properties": {"a": {"type": "string"}}}},
     {"name": "sort", "in": "query", "style": "pipeDelimited", "explode": false, "schema": {"type": "array", "items": {"type": "string"}}},
     {"name": "q", "in": "query", "content": {"application/json": {"schema": {"type": "object"}}}},
     {"name": "X-Flags", "in": "header", "explode": true, "schema": {"type": "array", "items": {"type": "string"}}},
     {"name": "sid2", "in": "cookie", "schema": {"type": "string"}},
     {"name": "Accept", "in": "header", "schema": {"type": "string"}},
     {"name": "bad", "in": "query", "style": "spaceDelimited", "explode": true, "schema": {"type": "array"}}
    ],
    "requestBody": {"content": {"application/json": {}}},
    "responses": {
     "200": {"description": "ok", "headers": {"X-Rate": {"$ref": "#/components/headers/Rate"}, "Content-Type": {"schema": {"type": "string"}}}, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Pet"}}, "text/*": {}}},
     "404": {"$ref": "#/components/responses/NotFound"},
     "5XX": {"description": "server"}
    },
    "security": [{"oauth": ["r"]}, {"oidc": []}, {}],
    "callbacks": {"cb": {"{$request.body#/url}": {"post": {"responses": {"200": {"description": "ok"}}}}}}
   },
   "put": {
    "operationId": "putPet",
    "requestBody": {"$ref": "#/components/requestBodies/Pet"},
    "responses": {"default": {"description": "any"}}
   },
   "post": {
    "operationId": "formPet",
    "requestBody": {"content": {
     "application/x-www-form-urlencoded": {"schema": {"$ref": "#/components/schemas/Pet"}, "encoding": {"meta": {"style": "form", "explode": false}, "name": {"contentType": "text/plain"}}},
     "multipart/form-data": {"schema": {"$ref": "#/components/schemas/Pet"}, "encoding": {"photo": {"contentType": "image/png, image/jpeg", "headers": {"X-Part": {"schema": {"type": "string"}}}}}}
    }},
    "responses": {"200": {"description": "ok"}},
    "security": [{"cookieKey": []}, {"ext": []}]
   },
   "delete": {
    "requestBody": {"$ref": "bodies.json#/missing"},
    "responses": {"204": {"description": "gone"}}
   },
   "patch": {
    "requestBody": {"$ref": "bodies.json#/missing"},
    "responses": {"204": {"description": "patched"}}
   }
  },
  "/empty": {"post": {"operationId": "emptyContent", "servers": [], "requestBody": {"content": {}}, "responses": {"200": {"description": "ok", "content": {"application/x-ndjson": {"schema": {"type": "object"}}, "multipart/mixed": {}}}}}}
 }
}`,
				goldenBase + "v30/security.json": `{"scheme": {"type": "apiKey", "in": "header", "name": "X-Ext", "description": "external"}}`,
			},
		},
		{
			name:  "3.1",
			entry: goldenBase + "v31/openapi.json",
			docs: map[string]string{
				goldenBase + "v31/openapi.json": `{
 "openapi": "3.1.2",
 "info": {"title": "golden 3.1", "version": "1"},
 "jsonSchemaDialect": "https://spec.openapis.org/oas/3.1/dialect/base",
 "servers": [{"url": "https://api.example.test/v3"}],
 "paths": {
  "/items": {"$ref": "items.json", "summary": "local summary", "get": {"operationId": "conflict"}},
  "/widgets/{id}": {
   "parameters": [{"$ref": "#/components/parameters/Id", "description": "the widget id"}],
   "get": {
    "operationId": "getWidget",
    "security": [{"mtls": []}, {"bearer": [], "key": []}],
    "responses": {"200": {"$ref": "#/components/responses/Widget", "description": "the widget"}}
   },
   "post": {
    "operationId": "badSecurity",
    "security": {"bearer": []},
    "responses": {"200": {"description": "ok"}}
   },
   "trace": {"requestBody": {"content": {"application/json": {}}}, "responses": {}}
  },
  "/stream": {"post": {"operationId": "stream", "requestBody": {"required": true, "content": {"application/jsonl": {"schema": {"type": "array"}}, "text/event-stream": {}}}, "responses": {"200": {"description": "events", "content": {"application/json-seq": {}, "text/event-stream": {}}}}}}
 },
 "webhooks": {"newPet": {"post": {"responses": {"200": {"description": "ok"}}}}},
 "components": {
  "securitySchemes": {"mtls": {"type": "mutualTLS"}, "bearer": {"type": "http", "scheme": "Bearer"}, "key": {"type": "apiKey", "in": "query", "name": "k", "description": "query key"}},
  "parameters": {"Id": {"name": "id", "in": "path", "required": true, "description": "target description", "schema": {"$id": "https://schemas.example.test/id", "type": "string", "$defs": {"x": {"type": "integer"}}}}},
  "responses": {"Widget": {"description": "target", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget", "description": "sibling"}}}}},
  "schemas": {"Widget": {"$dynamicAnchor": "node", "type": "object", "properties": {"next": {"$dynamicRef": "#node"}, "ext": {"$ref": "schemas.json#/Ext"}}}}
 }
}`,
				goldenBase + "v31/items.json":   `{"summary": "remote summary", "parameters": [{"name": "page", "in": "query", "schema": {"type": "integer"}}], "get": {"operationId": "listItems", "responses": {"200": {"description": "ok"}}}, "post": {"operationId": "addItem", "requestBody": {"content": {"text/plain": {"schema": {"type": "string"}}}}}}`,
				goldenBase + "v31/schemas.json": `{"Ext": {"type": "string", "format": "uuid"}}`,
			},
		},
		{
			name:  "3.2",
			entry: goldenBase + "v32/openapi.json",
			docs: map[string]string{
				goldenBase + "v32/openapi.json": `{
 "openapi": "3.2.1",
 "$self": "https://golden.example.test/v32/self.json",
 "info": {"title": "golden 3.2", "version": "1"},
 "servers": [{"url": "https://api.example.test", "name": "prod", "description": "production"}, {"url": "https://staging.example.test", "name": "staging"}],
 "paths": {
  "/search": {
   "query": {
    "operationId": "search",
    "requestBody": {"content": {"application/json": {"$ref": "#/components/mediaTypes/Query"}}},
    "responses": {"200": {"description": "ok", "content": {"application/jsonl": {"itemSchema": {"type": "object"}}, "text/event-stream": {"itemSchema": {"type": "object", "properties": {"data": {"type": "string"}}}}}}}
   },
   "additionalOperations": {
    "LINK": {"operationId": "link", "responses": {"204": {"description": "linked"}}},
    "POST": {"responses": {"200": {"description": "forbidden"}}},
    "Post": {"responses": {"200": {"description": "ordinary"}}}
   },
   "get": {
    "operationId": "qs",
    "parameters": [
     {"name": "qs", "in": "querystring", "content": {"application/x-www-form-urlencoded": {"schema": {"type": "object"}, "encoding": {"f": {"style": "form", "explode": false}}}}},
     {"name": "c", "in": "cookie", "style": "cookie", "schema": {"type": "string"}},
     {"name": "p", "in": "header", "schema": {"type": "string"}}
    ],
    "security": [{"https://golden.example.test/v32/self.json#/components/securitySchemes/device": ["s"]}, {"meta": []}],
    "responses": {"200": {"description": "ok"}}
   }
  },
  "/upload": {"post": {
   "operationId": "upload",
   "requestBody": {"content": {
    "multipart/mixed": {"prefixEncoding": [{"contentType": "application/json"}, {"contentType": "image/png", "headers": {"X-Seq": {"schema": {"type": "integer"}}}}], "itemEncoding": {"contentType": "text/plain"}},
    "multipart/form-data": {"schema": {"type": "object", "properties": {"a": {"type": "string"}}}, "encoding": {"a": {"contentType": "text/plain"}, "extra": {"contentType": "application/octet-stream"}}}
   }},
   "responses": {"200": {"description": "ok"}}
  }}
 },
 "components": {
  "mediaTypes": {"Query": {"schema": {"type": "object", "properties": {"term": {"type": "string"}}}}},
  "securitySchemes": {
   "device": {"type": "oauth2", "deprecated": true, "oauth2MetadataUrl": "https://auth.example.test/.well-known/oauth-authorization-server", "flows": {"deviceAuthorization": {"deviceAuthorizationUrl": "https://auth.example.test/device", "tokenUrl": "https://auth.example.test/token", "scopes": {"s": "scope"}}}},
   "meta": {"type": "http", "scheme": "digest"}
  }
 }
}`,
			},
		},
	}
}

// goldenClient loads an edition through a Fetch from memory.
func goldenClient(tb testing.TB, e goldenEdition) *openapi.Client {
	tb.Helper()
	l := &openapi.Loader{Fetch: mapFetch(e.docs)}
	c, err := l.Load(context.Background(), e.entry, nil)
	if err != nil {
		tb.Fatalf("%s: Load: %v", e.name, err)
	}
	return c
}

// The rendering mirrors the descriptor types field for field. An Err is
// its message and whether it wraps ErrUnresolved. Server IDs, which are
// opaque (describe.go, Server.ID: "do not compose it yourself"), are
// renumbered in order of first appearance, so the rendering keeps which
// servers share a declaration without depending on the spelling.

type goldenErr struct {
	Message    string `json:"message"`
	Unresolved bool   `json:"unresolved,omitempty"`
}

type goldenSchemaRef struct {
	At      string     `json:"at"`
	Keyword string     `json:"keyword"`
	Value   string     `json:"value"`
	URI     string     `json:"uri"`
	Target  string     `json:"target,omitempty"`
	Err     *goldenErr `json:"err,omitempty"`
}

type goldenSchema struct {
	Raw        json.RawMessage   `json:"raw"`
	Source     string            `json:"source"`
	Base       string            `json:"base"`
	Dialect    string            `json:"dialect,omitempty"`
	Version    string            `json:"version"`
	References []goldenSchemaRef `json:"references,omitempty"`
	RefsErr    *goldenErr        `json:"referencesErr,omitempty"`
}

type goldenParam struct {
	Key              string        `json:"key"`
	Name             string        `json:"name"`
	In               string        `json:"in"`
	Description      string        `json:"description"`
	Required         bool          `json:"required"`
	Deprecated       bool          `json:"deprecated"`
	Style            string        `json:"style"`
	Explode          bool          `json:"explode"`
	ExplodeSet       bool          `json:"explodeSet"`
	AllowReserved    bool          `json:"allowReserved"`
	AllowEmptyValue  bool          `json:"allowEmptyValue"`
	ContentType      string        `json:"contentType"`
	CollectionFormat string        `json:"collectionFormat"`
	Schema           *goldenSchema `json:"schema"`
	Headers          []goldenParam `json:"headers"`
	Source           string        `json:"source"`
	Err              *goldenErr    `json:"err"`
}

type goldenMedia struct {
	Type       string        `json:"type"`
	Schema     *goldenSchema `json:"schema"`
	ItemSchema *goldenSchema `json:"itemSchema"`
	Sequential bool          `json:"sequential"`
	Encoding   []goldenParam `json:"encoding"`
	Source     string        `json:"source"`
	Err        *goldenErr    `json:"err"`
}

type goldenMessage struct {
	Key         string        `json:"key"`
	Description string        `json:"description"`
	Required    bool          `json:"required"`
	Headers     []goldenParam `json:"headers"`
	Media       []goldenMedia `json:"media"`
	Source      string        `json:"source"`
	Err         *goldenErr    `json:"err"`
}

type goldenVariable struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Declared    bool     `json:"declared"`
	Default     string   `json:"default"`
	DefaultSet  bool     `json:"defaultSet"`
	Enum        []string `json:"enum"`
}

type goldenServer struct {
	ID          string           `json:"id"`
	URL         string           `json:"url"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Variables   []goldenVariable `json:"variables"`
	Source      string           `json:"source"`
	Err         *goldenErr       `json:"err"`
}

type goldenFlow struct {
	Type                   string            `json:"type"`
	AuthorizationURL       string            `json:"authorizationURL"`
	TokenURL               string            `json:"tokenURL"`
	RefreshURL             string            `json:"refreshURL"`
	DeviceAuthorizationURL string            `json:"deviceAuthorizationURL"`
	Scopes                 map[string]string `json:"scopes"`
}

type goldenScheme struct {
	Name              string       `json:"name"`
	Scopes            []string     `json:"scopes"`
	Type              string       `json:"type"`
	Description       string       `json:"description"`
	In                string       `json:"in"`
	ParamName         string       `json:"paramName"`
	Scheme            string       `json:"scheme"`
	BearerFormat      string       `json:"bearerFormat"`
	Flows             []goldenFlow `json:"flows"`
	OpenIDConnectURL  string       `json:"openIdConnectURL"`
	OAuth2MetadataURL string       `json:"oauth2MetadataURL"`
	Deprecated        bool         `json:"deprecated"`
	Source            string       `json:"source"`
	Err               *goldenErr   `json:"err"`
}

type goldenRequirement struct {
	Key     string         `json:"key"`
	Schemes []goldenScheme `json:"schemes"`
}

type goldenOperation struct {
	Key         string              `json:"key"`
	ID          string              `json:"id"`
	Method      string              `json:"method"`
	Path        string              `json:"path"`
	Summary     string              `json:"summary"`
	Description string              `json:"description"`
	Tags        []string            `json:"tags"`
	Deprecated  bool                `json:"deprecated"`
	Params      []goldenParam       `json:"params"`
	Body        *goldenMessage      `json:"body"`
	Responses   []goldenMessage     `json:"responses"`
	Servers     []goldenServer      `json:"servers"`
	Security    []goldenRequirement `json:"security"`
	Source      string              `json:"source"`
	Err         *goldenErr          `json:"err"`
}

type goldenDocument struct {
	Edition    string            `json:"edition"`
	Version    string            `json:"version"`
	Documents  []string          `json:"documents"`
	Operations []goldenOperation `json:"operations"`
}

// goldenRenderer renders descriptors, renumbering server IDs.
type goldenRenderer struct {
	ids map[string]string
}

func renderErr(err error) *goldenErr {
	if err == nil {
		return nil
	}
	return &goldenErr{Message: err.Error(), Unresolved: errors.Is(err, openapi.ErrUnresolved)}
}

func (r *goldenRenderer) schema(s *openapi.Schema) *goldenSchema {
	if s == nil {
		return nil
	}
	g := &goldenSchema{Raw: s.Raw(), Source: s.Source(), Base: s.Base(), Dialect: s.Dialect(), Version: s.Version()}
	refs, err := s.References()
	g.RefsErr = renderErr(err)
	for _, ref := range refs {
		gr := goldenSchemaRef{At: ref.At, Keyword: ref.Keyword, Value: ref.Value, URI: ref.URI, Err: renderErr(ref.Err)}
		if ref.Target != nil {
			gr.Target = ref.Target.Source()
		}
		g.References = append(g.References, gr)
	}
	return g
}

func (r *goldenRenderer) params(ps []*openapi.Param) []goldenParam {
	var out []goldenParam
	for _, p := range ps {
		out = append(out, goldenParam{
			Key: p.Key, Name: p.Name, In: p.In, Description: p.Description,
			Required: p.Required, Deprecated: p.Deprecated, Style: p.Style,
			Explode: p.Explode, ExplodeSet: p.ExplodeSet, AllowReserved: p.AllowReserved,
			AllowEmptyValue: p.AllowEmptyValue, ContentType: p.ContentType,
			CollectionFormat: p.CollectionFormat, Schema: r.schema(p.Schema),
			Headers: r.params(p.Headers), Source: p.Source, Err: renderErr(p.Err),
		})
	}
	return out
}

func (r *goldenRenderer) message(m *openapi.Message) *goldenMessage {
	if m == nil {
		return nil
	}
	g := &goldenMessage{Key: m.Key, Description: m.Description, Required: m.Required,
		Headers: r.params(m.Headers), Source: m.Source, Err: renderErr(m.Err)}
	for _, md := range m.Media {
		g.Media = append(g.Media, goldenMedia{
			Type: md.Type, Schema: r.schema(md.Schema), ItemSchema: r.schema(md.ItemSchema),
			Sequential: md.Sequential, Encoding: r.params(md.Encoding), Source: md.Source, Err: renderErr(md.Err),
		})
	}
	return g
}

func (r *goldenRenderer) serverID(id string) string {
	if r.ids == nil {
		r.ids = map[string]string{}
	}
	if id == "" {
		return ""
	}
	if n, ok := r.ids[id]; ok {
		return n
	}
	n := fmt.Sprintf("server-%d", len(r.ids))
	r.ids[id] = n
	return n
}

func (r *goldenRenderer) operation(op *openapi.Operation) goldenOperation {
	g := goldenOperation{
		Key: op.Key, ID: op.ID, Method: op.Method, Path: op.Path, Summary: op.Summary,
		Description: op.Description, Tags: op.Tags, Deprecated: op.Deprecated,
		Params: r.params(op.Params), Body: r.message(op.Body), Source: op.Source, Err: renderErr(op.Err),
	}
	for _, m := range op.Responses {
		g.Responses = append(g.Responses, *r.message(m))
	}
	for _, s := range op.Servers {
		gs := goldenServer{ID: r.serverID(s.ID), URL: s.URL, Name: s.Name, Description: s.Description, Source: s.Source, Err: renderErr(s.Err)}
		for _, v := range s.Variables {
			gs.Variables = append(gs.Variables, goldenVariable{v.Name, v.Description, v.Declared, v.Default, v.DefaultSet, v.Enum})
		}
		g.Servers = append(g.Servers, gs)
	}
	for _, req := range op.Security {
		gr := goldenRequirement{Key: req.Key}
		for _, sc := range req.Schemes {
			gsc := goldenScheme{
				Name: sc.Name, Scopes: sc.Scopes, Type: sc.Type, Description: sc.Description,
				In: sc.In, ParamName: sc.ParamName, Scheme: sc.Scheme, BearerFormat: sc.BearerFormat,
				OpenIDConnectURL: sc.OpenIDConnectURL, OAuth2MetadataURL: sc.OAuth2MetadataURL,
				Deprecated: sc.Deprecated, Source: sc.Source, Err: renderErr(sc.Err),
			}
			for _, f := range sc.Flows {
				gsc.Flows = append(gsc.Flows, goldenFlow{f.Type, f.AuthorizationURL, f.TokenURL, f.RefreshURL, f.DeviceAuthorizationURL, f.Scopes})
			}
			gr.Schemes = append(gr.Schemes, gsc)
		}
		g.Security = append(g.Security, gr)
	}
	return g
}

// renderOperations renders ops as indented JSON, map keys sorted by
// encoding/json.
func renderOperations(tb testing.TB, edition string, c *openapi.Client, ops []*openapi.Operation) goldenDocument {
	tb.Helper()
	r := &goldenRenderer{}
	d := goldenDocument{Edition: edition, Version: c.Version(), Documents: c.DocumentURIs()}
	for _, op := range ops {
		d.Operations = append(d.Operations, r.operation(op))
	}
	return d
}

func marshalGolden(tb testing.TB, v any) []byte {
	tb.Helper()
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		tb.Fatalf("render: %v", err)
	}
	return append(b, '\n')
}

// Regression check, not contract: each Err's text, and the Raw of a schema the
// client makes, match the committed rendering (see the file comment). The rest
// is contract: the descriptors of every golden document equal the committed
// rendering, field for field. Each edition is loaded twice, and the two
// Clients, a Client.With of one, and a second Operations call all describe the
// same operations; Client.Operation reaches the very descriptor Operations
// lists for every Key (describe.go, Client.Operation: "describes the operation
// named key, by the rules Call uses").
func TestDescribeGolden(t *testing.T) {
	var docs []goldenDocument
	for _, e := range goldenEditions() {
		c := goldenClient(t, e)
		ops := c.Operations()
		d := renderOperations(t, e.name, c, ops)
		docs = append(docs, d)
		want := marshalGolden(t, d)

		again := goldenClient(t, e)
		if got := marshalGolden(t, renderOperations(t, e.name, again, again.Operations())); !bytes.Equal(got, want) {
			t.Errorf("%s: a second load describes differently: %s", e.name, firstDiff(string(got), string(want)))
		}
		if got := marshalGolden(t, renderOperations(t, e.name, c, c.Operations())); !bytes.Equal(got, want) {
			t.Errorf("%s: a second Operations call describes differently: %s", e.name, firstDiff(string(got), string(want)))
		}
		derived := c.With(func(o *openapi.Options) { o.NameOnlyEmpty = true })
		if got := marshalGolden(t, renderOperations(t, e.name, derived, derived.Operations())); !bytes.Equal(got, want) {
			t.Errorf("%s: a Client from With describes differently: %s", e.name, firstDiff(string(got), string(want)))
		}
		r := &goldenRenderer{}
		for _, op := range ops {
			if op.Key == "" {
				continue
			}
			byKey, err := c.Operation(op.Key)
			if err != nil {
				t.Errorf("%s: Operation(%q): %v", e.name, op.Key, err)
				continue
			}
			if !bytes.Equal(marshalGolden(t, r.operation(byKey)), marshalGolden(t, r.operation(op))) {
				t.Errorf("%s: Operation(%q) differs from the descriptor Operations lists", e.name, op.Key)
			}
		}
	}
	got := marshalGolden(t, docs)
	if *updateDescribeGolden {
		if err := os.WriteFile(describeGoldenFile, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(describeGoldenFile)
	if err != nil {
		t.Fatalf("%v (run with -update-describe-golden to create it)", err)
	}
	// A checkout may write the file with CRLF line ends; JSON escapes any
	// CR inside a string, so a raw CR is only a line end.
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Errorf("descriptors differ from %s at %s", describeGoldenFile, firstDiff(string(got), string(want)))
	}
}

// The golden documents cover what they are meant to: every edition, and in
// each, operations with and without Err, so a change to either kind shows.
func TestDescribeGoldenCoverage(t *testing.T) {
	for _, e := range goldenEditions() {
		c := goldenClient(t, e)
		var withErr, without int
		walkOps(c.Operations(), func(where string, err error) {
			if err != nil {
				withErr++
			} else {
				without++
			}
		})
		if withErr == 0 || without == 0 {
			t.Errorf("%s: %d descriptors with Err, %d without; want both", e.name, withErr, without)
		}
	}
}

// walkOps calls f with every descriptor's Err in ops: each Operation and its
// Params, Body, Responses, their Headers, Media and Encoding, Servers and
// SecuritySchemes, with a name for where it is.
func walkOps(ops []*openapi.Operation, f func(where string, err error)) {
	var params func(where string, ps []*openapi.Param)
	params = func(where string, ps []*openapi.Param) {
		for _, p := range ps {
			w := where + "/" + p.Name
			f(w, p.Err)
			params(w+" headers", p.Headers)
		}
	}
	message := func(where string, m *openapi.Message) {
		if m == nil {
			return
		}
		f(where, m.Err)
		params(where+" headers", m.Headers)
		for _, md := range m.Media {
			f(where+" media "+md.Type, md.Err)
			params(where+" media "+md.Type+" encoding", md.Encoding)
		}
	}
	for _, op := range ops {
		name := op.Key
		if name == "" {
			name = op.Method + " " + op.Path
		}
		f(name, op.Err)
		params(name+" params", op.Params)
		message(name+" body", op.Body)
		for _, r := range op.Responses {
			message(name+" response "+r.Key, r)
		}
		for i, s := range op.Servers {
			f(fmt.Sprintf("%s server %d %s", name, i, s.URL), s.Err)
		}
		for _, req := range op.Security {
			for _, sc := range req.Schemes {
				f(name+" security "+req.Key+" "+sc.Name, sc.Err)
			}
		}
	}
}

// BenchmarkDescribeRetained reports the live heap a Client keeps once
// every operation has been described, per byte of the document, for the
// synthetic 2,100-operation document (largeDoc): retained-B/doc-B is the
// whole Client after Operations, and describe-B/doc-B the part Operations
// added over a Client only loaded, which is all of describing where the
// client describes lazily and nothing where it describes at Load. The
// package documentation says only that listing the operations does no
// schema work; the metrics measure what describing keeps.
func BenchmarkDescribeRetained(b *testing.B) {
	doc, _ := largeDoc(700, 500)
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	ctx := context.Background()
	for b.Loop() {
		c, err := openapi.Parse(ctx, doc, largeDocURI, nil)
		if err != nil {
			b.Fatal(err)
		}
		if len(c.Operations()) == 0 {
			b.Fatal("no operations")
		}
	}
	var before, loaded, described runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	c, err := openapi.Parse(ctx, doc, largeDocURI, nil)
	if err != nil {
		b.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&loaded)
	ops := c.Operations()
	runtime.GC()
	runtime.ReadMemStats(&described)
	runtime.KeepAlive(c)
	runtime.KeepAlive(ops)
	size := float64(len(doc))
	b.ReportMetric(float64(int64(described.HeapAlloc)-int64(before.HeapAlloc))/size, "retained-B/doc-B")
	b.ReportMetric(float64(int64(described.HeapAlloc)-int64(loaded.HeapAlloc))/size, "describe-B/doc-B")
}
