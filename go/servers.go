package openapiclient

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// ServerResolutionRequiredError reports a missing server selection, variable,
// or document base that a caller can supply without changing the OpenAPI
// document. Path is relative to ServerSelection; Enum contains the authored
// choices when the document declares a closed set.
type ServerResolutionRequiredError struct {
	Path        string
	Description string
	Enum        []string
}

func (e *ServerResolutionRequiredError) Error() string { return e.Description }

// ServerSet is the usable effective Server Object list for an OpenAPI
// operation. It owns edition-specific Server Variable validation and RFC 3986
// resolution against each Server Object's declaring document.
type ServerSet struct {
	servers        openapi3.Servers
	edition        string
	sourceLocation string
}

// NewServerSet validates and confines a list of Server Objects. Invalid
// alternatives are excluded when another authored alternative remains usable.
func NewServerSet(servers openapi3.Servers, edition, sourceLocation string) (*ServerSet, error) {
	eligible, err := eligibleServers(servers, edition, sourceLocation)
	if err != nil {
		return nil, err
	}
	return &ServerSet{servers: eligible, edition: edition, sourceLocation: sourceLocation}, nil
}

// EffectiveServerSet applies OpenAPI's operation → path-item → document →
// implied-root inheritance rule and returns its usable Server Objects.
func EffectiveServerSet(doc *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation, sourceLocation string) (*ServerSet, error) {
	edition := ""
	if doc != nil {
		edition = doc.OpenAPI
	}
	return NewServerSet(effectiveServers(doc, pathItem, op), edition, sourceLocation)
}

// Servers returns the usable Server Objects in authored order.
func (s *ServerSet) Servers() openapi3.Servers {
	if s == nil {
		return nil
	}
	return append(openapi3.Servers(nil), s.servers...)
}

// Resolve selects and resolves one usable Server Object. A nil selection uses
// the sole effective entry; multiple entries require an explicit selection.
// BaseURL replaces the authored list with a completed absolute base.
func (s *ServerSet) Resolve(selection *ServerSelection) (string, error) {
	if s == nil || len(s.servers) == 0 {
		return "", fmt.Errorf("the effective server list has no usable alternative")
	}
	if selection != nil && selection.BaseURL != "" {
		if !denotesTargetBase(selection.BaseURL) {
			return "", fmt.Errorf("server base URL %q is not an absolute target URL", selection.BaseURL)
		}
		return absolutizeServerURL(selection.BaseURL, s.sourceLocation)
	}
	if selection == nil && len(s.servers) != 1 {
		urls := make([]string, 0, len(s.servers))
		for _, server := range s.servers {
			if server != nil {
				urls = append(urls, server.URL)
			}
		}
		return "", &ServerResolutionRequiredError{
			Path: "/url", Enum: urls,
			Description: fmt.Sprintf("the effective server list has %d alternatives; select one", len(s.servers)),
		}
	}

	server := s.servers[0]
	variables := map[string]string(nil)
	if selection != nil {
		if selection.URL != "" {
			server = serverByURL(s.servers, selection.URL)
			if server == nil {
				return "", fmt.Errorf("server URL %q matches no effective Server Object", selection.URL)
			}
		} else if selection.Index != nil {
			if *selection.Index < 0 || *selection.Index >= len(s.servers) {
				return "", fmt.Errorf("server index %d is outside the effective Server Object list (%d entries)", *selection.Index, len(s.servers))
			}
			server = s.servers[*selection.Index]
		}
		variables = selection.Variables
	}
	resolved, err := substituteServerVariablesFor(server, variables, s.edition)
	if err != nil {
		return "", err
	}
	return absolutizeServerURL(resolved, declaringDocumentLocation(server, s.sourceLocation))
}

// This file implements the server and target-URL mechanics specified by
// OpenAPI 3.0 and 3.1. Configuration consultation is kept at the caller-facing
// layer; this module owns effective-list inheritance, Server Variable rules,
// relative resolution, and completed URL spelling.

// resolveServer resolves the operation's server per
// openbindings.openapi-3.0@1 §10 and openbindings.openapi-3.1@1 §10:
//
//   - The effective server list is the OAS's: the operation's `servers`,
//     else the path item's, else the document's, else the implied
//     `url: "/"`.
//
//   - A sole effective entry selects itself with declared variable defaults;
//     multiple artifact alternatives require configuration to select one.
//
//   - Consumer configuration (context.configuration.server) may instead
//     select another entry, supply variable values, or supply a complete
//     base URL outright. Accepted shapes:
//
//     "https://api.example.com"                        // absolute base URL outright
//     "https://{env}.example.com"                      // string matching a declared entry's url
//     {"baseUrl": "https://api.example.com"}           // absolute base URL outright
//     {"url": "https://{env}.example.com"}             // select the declared entry with that url
//     {"index": 1}                                     // select the effective list's Nth entry
//     {"variables": {"env": "staging"}}                // server-variable values (validated against enum)
//
//     `url`/`index` and `variables` compose; `baseUrl` stands alone.
//
//   - A relative effective-server URL (the implied "/" included) resolves
//     against the artifact's base URI (§6: the source's location) per
//     RFC 3986. The one pre-dispatch refusal is a server URL that cannot
//     resolve to an absolute URL.
//
// The legacy context.metadata.baseURL override is honored below the
// configuration point (the configuration point is the contract surface).
func resolveServer(doc *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation, bindCtx map[string]any, sourceLocation string) (string, error) {
	set, err := EffectiveServerSet(doc, pathItem, op, sourceLocation)
	if err != nil {
		return "", err
	}

	if cfg := contextConfiguration(bindCtx); cfg != nil {
		if raw, ok := cfg["server"]; ok && raw != nil {
			selection, err := serverSelectionFromConfig(raw, set.servers)
			if err != nil {
				return "", err
			}
			resolved, err := set.Resolve(selection)
			return resolved, serverConfigRequired(err)
		}
	}

	if meta := contextMetadata(bindCtx); meta != nil {
		if base, ok := meta["baseURL"].(string); ok && base != "" {
			resolved, err := set.Resolve(&ServerSelection{BaseURL: base})
			return resolved, serverConfigRequired(err)
		}
	}
	resolved, err := set.Resolve(nil)
	return resolved, serverConfigRequired(err)
}

// effectiveServers returns the OAS effective server list: operation servers,
// else path-item servers, else document servers, else the OAS-defined
// implied server of url "/".
func effectiveServers(doc *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation) openapi3.Servers {
	if op != nil && op.Servers != nil && len(*op.Servers) > 0 {
		return *op.Servers
	}
	if pathItem != nil && len(pathItem.Servers) > 0 {
		return pathItem.Servers
	}
	if doc != nil && len(doc.Servers) > 0 {
		return doc.Servers
	}
	return openapi3.Servers{&openapi3.Server{URL: "/"}}
}

// resolveServerConfig applies one configured `server` value against the
// effective list, returning the (possibly still relative) server URL.
func serverSelectionFromConfig(raw any, servers openapi3.Servers) (*ServerSelection, error) {
	switch v := raw.(type) {
	case string:
		if srv := serverByURL(servers, v); srv != nil {
			return &ServerSelection{URL: v}, nil
		}
		if denotesTargetBase(v) {
			return &ServerSelection{BaseURL: v}, nil
		}
		return nil, fmt.Errorf("configuration.server %q matches no declared server entry and is not an absolute base URL", v)
	case map[string]any:
		if base, ok := v["baseUrl"].(string); ok && base != "" {
			return &ServerSelection{BaseURL: base}, nil
		}
		selection := &ServerSelection{}
		if entryURL, ok := v["url"].(string); ok && entryURL != "" {
			selection.URL = entryURL
		} else if idxRaw, ok := v["index"]; ok {
			idx, ok := configIndex(idxRaw)
			if !ok {
				return nil, fmt.Errorf("configuration.server.index %v is not an integer", idxRaw)
			}
			selection.Index = &idx
		}
		if rawVars, ok := v["variables"].(map[string]any); ok {
			selection.Variables = make(map[string]string, len(rawVars))
			for name, val := range rawVars {
				s, ok := val.(string)
				if !ok {
					return nil, fmt.Errorf("configuration.server.variables[%q] must be a string, got %T", name, val)
				}
				selection.Variables[name] = s
			}
		}
		return selection, nil
	default:
		return nil, fmt.Errorf("configuration.server must be a string or an object, got %T", raw)
	}
}

func serverConfigRequired(err error) error {
	var required *ServerResolutionRequiredError
	if !errors.As(err, &required) {
		return err
	}
	return &configRequired{
		point: "server", path: required.Path, description: required.Description,
		schema: enumSchema(required.Enum), durable: durableServerRequirement(required.Path),
	}
}

func durableServerRequirement(path string) *bool {
	if path == "/url" {
		return &serverChoiceDurable
	}
	return nil
}

// serverByURL selects the declared entry whose url template matches exactly.
func serverByURL(servers openapi3.Servers, u string) *openapi3.Server {
	for _, srv := range servers {
		if srv != nil && srv.URL == u {
			return srv
		}
	}
	return nil
}

func configIndex(raw any) (int, bool) {
	switch n := raw.(type) {
	case int:
		return n, true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}

// substituteServerVariables substitutes each declared server variable with
// the supplied value or its declared default. A variable with neither a
// supplied value nor a declared default, and a supplied variable the entry
// does not declare, are loud errors. A declared enum constrains substitution
// values exactly as the artifact declares; the separate complete-base-URL
// override is an explicit configuration choice, not permission to weaken the
// selected Server Object.
func substituteServerVariables(srv *openapi3.Server, supplied map[string]string) (string, error) {
	return substituteServerVariablesFor(srv, supplied, "")
}

func substituteServerVariablesFor(srv *openapi3.Server, supplied map[string]string, edition string) (string, error) {
	if srv == nil {
		return "", fmt.Errorf("the selected Server Object is absent")
	}
	u := srv.URL
	if strings.ContainsAny(u, "?#") {
		return "", fmt.Errorf("server URL %q contains a query or fragment", srv.URL)
	}
	names := make([]string, 0, len(srv.Variables))
	for name := range srv.Variables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v := srv.Variables[name]
		if v == nil {
			return "", fmt.Errorf("server %q variable %q is not a Server Variable Object", srv.URL, name)
		}
		expression := "{" + name + "}"
		if count := strings.Count(u, expression); count != 1 {
			return "", fmt.Errorf("server %q variable %q must occur exactly once in its URL template (found %d)", srv.URL, name, count)
		}
		defaultPresent := v.Default != "" || extensionBool(v.Extensions, serverVariableDefaultMarker)
		enumPresent := v.Enum != nil || extensionBool(v.Extensions, serverVariableEnumMarker)
		if enumPresent && len(v.Enum) == 0 {
			return "", fmt.Errorf("server %q variable %q declares an empty enum", srv.URL, name)
		}
		if strings.HasPrefix(edition, "3.0.") && !defaultPresent {
			return "", fmt.Errorf("server %q variable %q omits its required default", srv.URL, name)
		}
		if enumPresent && defaultPresent && !containsServerVariableValue(v.Enum, v.Default) {
			return "", fmt.Errorf("server %q variable %q default %q is outside its declared enum", srv.URL, name, v.Default)
		}
		val, ok := supplied[name]
		if !ok {
			if !defaultPresent {
				return "", &ServerResolutionRequiredError{
					Path: "/variables/" + escapeJSONPointerSegment(name), Enum: append([]string(nil), v.Enum...),
					Description: fmt.Sprintf("server %q: variable %q has no supplied value and no declared default", srv.URL, name),
				}
			}
			val = v.Default
		}
		if enumPresent {
			if !containsServerVariableValue(v.Enum, val) {
				return "", fmt.Errorf("server %q variable %q value %q is outside its declared enum", srv.URL, name, val)
			}
		}
		u = strings.Replace(u, expression, val, 1)
	}
	for name := range supplied {
		if _, declared := srv.Variables[name]; !declared {
			return "", fmt.Errorf("server %q declares no variable %q", srv.URL, name)
		}
	}
	if strings.ContainsAny(u, "{}") {
		return "", fmt.Errorf("server URL %q contains an unresolved template variable", srv.URL)
	}
	return u, nil
}

func containsServerVariableValue(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func extensionBool(extensions map[string]any, name string) bool {
	value, _ := extensions[name].(bool)
	return value
}

func declaringDocumentLocation(server *openapi3.Server, fallback string) string {
	if server != nil && server.Extensions != nil {
		if location, ok := server.Extensions[serverDocumentMarker].(string); ok && location != "" {
			return location
		}
	}
	return fallback
}

// absolutizeServerURL resolves a (possibly relative) server URL to an
// absolute target base: a URL that already denotes a target address passes
// through; a relative reference resolves against the artifact's base URI —
// the source's location (§6) — per RFC 3986. A server URL that cannot
// resolve to an absolute URL is the §9.3 pre-dispatch refusal. The returned
// URL carries no trailing slash, so joining with the operation's path
// template is concatenation.
//
// Whether a string denotes a target address is decided by denotesTargetBase
// (target_base.go), which reads RFC 3986's URI production and RFC 9110's
// non-empty-host requirement for the http and https schemes, rather than by
// net/url. See that file for why the host parser was the wrong authority.
func absolutizeServerURL(serverURL, sourceLocation string) (string, error) {
	if err := validateServerBaseSpelling(serverURL); err != nil {
		return "", err
	}
	if denotesTargetBase(serverURL) {
		return serverURL, nil
	}
	// Only a relative reference can be completed by a base URI. A string
	// carrying a scheme has already named an address, so failing the
	// predicate means that address does not exist and no base can supply it.
	if !hasURIScheme(serverURL) && sourceLocation != "" && denotesTargetBase(sourceLocation) {
		base, berr := url.Parse(sourceLocation)
		ref, rerr := url.Parse(serverURL)
		if berr == nil && rerr == nil {
			resolved := base.ResolveReference(ref).String()
			if validateServerBaseSpelling(resolved) == nil && denotesTargetBase(resolved) {
				return resolved, nil
			}
		}
	}
	return "", &ServerResolutionRequiredError{
		Path:        "/url",
		Description: fmt.Sprintf("server URL %q cannot resolve to an absolute URL: supply a base URL", serverURL),
	}
}

func validateServerBaseSpelling(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("server URL %q does not parse under RFC 3986: %w", value, err)
	}
	if parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(value, "#") {
		return fmt.Errorf("server URL %q contains a query or fragment", value)
	}
	return nil
}

// AssembleRequestURL appends a serialized operation path and raw query to a
// resolved Server URL without slash normalization, then validates the
// completed RFC 3986 spelling. A trailing server slash and leading operation
// slash therefore remain a deliberate double slash.
func AssembleRequestURL(serverBase, operationPath, rawQuery string) (*url.URL, error) {
	completed := serverBase + operationPath
	if rawQuery != "" {
		completed += "?" + rawQuery
	}
	parsed, err := url.Parse(completed)
	if err != nil {
		return nil, fmt.Errorf("completed OpenAPI URL does not parse under RFC 3986: %w", err)
	}
	if err := ValidateRequestURL(parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// ValidateRequestURL performs the last pre-dispatch URL check after path and
// query serialization. It validates every percent-bearing RFC 3986 component
// without decoding delimiters that must remain on the wire.
func ValidateRequestURL(completed *url.URL) error {
	if completed == nil {
		return fmt.Errorf("completed OpenAPI URL is absent")
	}
	parsed, err := url.Parse(completed.String())
	if err != nil {
		return fmt.Errorf("completed OpenAPI URL does not parse under RFC 3986: %w", err)
	}
	// The completed target's scheme. Every 3.x document states this and none
	// of them was implemented: openbindings.openapi-3.0@1 §10 and
	// openbindings.openapi-3.2@1 §10 -- "A completed target whose scheme is
	// not `http` or `https` refuses before dispatch, because no incorporated
	// authority defines that scheme's HTTP-semantics mapping" -- and
	// openbindings.openapi-3.1@1 §10, which states the same restriction as a
	// static exclusion of the Server alternative. Nothing here dispatched:
	// `ftp://`, `file://` and `ws://` all reached the transport on all three
	// lines. The check lives at this shared completion point because that is
	// where the scheme is finally decided, after a `server` choice or a
	// consumer-configured URL has had its say.
	//
	// The refusal is what all three documents agree forbids -- no bytes on a
	// scheme with no defined mapping. Whether 3.1's stronger STATIC exclusion
	// should also remove the target from synthesis is a sibling divergence
	// left open rather than guessed here.
	if scheme := strings.ToLower(parsed.Scheme); scheme != "http" && scheme != "https" {
		return fmt.Errorf("completed OpenAPI target scheme %q is not http or https; no incorporated authority defines its HTTP-semantics mapping", parsed.Scheme)
	}
	for name, component := range map[string]string{
		"opaque":   parsed.Opaque,
		"path":     parsed.EscapedPath(),
		"query":    parsed.RawQuery,
		"fragment": parsed.EscapedFragment(),
	} {
		if _, err := url.PathUnescape(component); err != nil {
			return fmt.Errorf("completed OpenAPI URL %s does not percent-decode under RFC 3986: %w", name, err)
		}
	}
	return nil
}

// eligibleServers confines declaration defects to their authored
// alternatives. A missing variable value or relative document base remains a
// configurable alternative; malformed template or enum rules do not.
func eligibleServers(servers openapi3.Servers, edition, sourceLocation string) (openapi3.Servers, error) {
	eligible := make(openapi3.Servers, 0, len(servers))
	var first error
	for _, server := range servers {
		resolved, err := substituteServerVariablesFor(server, nil, edition)
		if err == nil {
			_, err = absolutizeServerURL(resolved, declaringDocumentLocation(server, sourceLocation))
		}
		var required *ServerResolutionRequiredError
		if err == nil || errors.As(err, &required) {
			eligible = append(eligible, server)
			continue
		}
		if first == nil {
			first = err
		}
	}
	if len(eligible) > 0 {
		return eligible, nil
	}
	if first == nil {
		first = fmt.Errorf("the effective server list has no usable alternative")
	}
	return nil, first
}

// configRequired is the typed signal a resolution helper returns when a named
// configuration point cannot resolve because a value is absent (no default,
// no supplied value) — a resolvable-missing value, not a malformed one. The
// invoke path turns it into a config.value CONTEXT_REQUIRED challenge
// (retryable after resolution, R1a) rather than a terminal
// ERR_SOURCE_CONFIG_ERROR. It implements error so it rides the existing
// (…, error) returns unchanged. Configuration may be sensitive according to
// its meaning; consumers decide whether the challenge target is sufficient
// for any stored-value release.
type configRequired struct {
	point       string
	path        string
	description string
	// schema is the engine-asserted JSON Schema for the missing value --
	// artifact-derived where the artifact speaks (a declared closed set
	// becomes {"enum": [...]}), nil where it does not (absent =
	// unconstrained).
	schema  map[string]any
	durable *bool
}

func (c *configRequired) Error() string { return c.description }

// enumSchema lifts an artifact-declared closed value set into the
// engine-asserted schema shape ({"enum": [...]}); an empty set asserts
// nothing (nil = absent = unconstrained).
func enumSchema(values []string) map[string]any {
	if len(values) == 0 {
		return nil
	}
	members := make([]any, len(values))
	for index, value := range values {
		members[index] = value
	}
	return map[string]any{"enum": members}
}

var serverChoiceDurable = true
