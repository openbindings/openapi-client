package openapiclient

import (
	"errors"
	"fmt"
	"sort"
)

// openbindings.openapi-2.0@1 §3.2 gives a pre-dispatch refusal two species:
// **context-required**, "where a named configuration point or credential is
// awaited and the refusal carries its own resolution path", and plain
// **refusal**, "where no supplied context could change the answer". Both occur
// before dispatch and both guarantee no observable side effect, so the species
// changes what a refusal CARRIES and never whether it happens.
//
// This file carries that answer out from the sites that know it. The condition
// is decided where it always was -- the parameter, media, form, server, and
// security lanes -- and each of those sites already knows whether a §12.1
// configuration point or a declared credential would repair it. Classifying
// anywhere else would mean re-deriving a condition the lane already evaluated,
// which is how one document comes to disagree with itself.

// swagger20ConfigRequired names a §12.1 configuration point the selected
// target needs and the caller has not supplied. It reuses configRequired so
// this lane's challenge is built by the same constructor the OpenAPI 3.x lane
// uses; only the boundary strings below are the Swagger 2.0 document's.
func swagger20ConfigRequired(point, path string) *configRequired {
	durable := true
	return &configRequired{
		point:       point,
		path:        path,
		description: swagger20ConfigurationDescription(point),
		schema:      swagger20ConfigurationSchema(point),
		durable:     &durable,
	}
}

// swagger20ConfigurationDescription and swagger20ConfigurationSchema state the
// point's own documented boundary and nothing more (openbindings.openapi-2.0@1
// §12.1). `emptyValueForm` is "exactly `name-only` or `empty`" (§12.1 row,
// §8.1), which is a closed admissible set and therefore an `enum`; `security`
// is "one complete declared alternative" selected by index; `server` states no
// shape here, and an absent schema means unconstrained rather than unknown.
func swagger20ConfigurationDescription(point string) string {
	if point == "propertyMedia" {
		return "select one concrete media type for this present file form parameter"
	}
	return "supply the Swagger 2.0 " + point + " configuration value"
}

func swagger20ConfigurationSchema(point string) map[string]any {
	switch point {
	case "requestMedia", "propertyMedia":
		return map[string]any{"type": "string"}
	case "security":
		return map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"index": map[string]any{"type": "integer", "minimum": 0}},
			"required":             []any{"index"},
			"additionalProperties": false,
		}
	case "emptyValueForm":
		return map[string]any{"enum": []any{"name-only", "empty"}}
	}
	return nil
}

// swagger20CredentialsRequired reports that the selected security alternative
// cannot be satisfied from the supplied credentials. §12.1 lists no
// configuration point for a credential, so these are auth-family requirements.
// Every scheme of the selected alternative is carried, because an alternative
// is an AND: a resolution path naming one of two required credentials is not a
// resolution path.
type swagger20CredentialsRequired struct {
	requirements []Requirement
	message      string
}

func (e *swagger20CredentialsRequired) Error() string { return e.message }

// Swagger20CredentialRequirement builds the auth-family requirement for one
// declared Swagger 2.0 security scheme: `basic`, `apiKey`, or `oauth2` with
// the requirement's declared scopes. It is the one builder both surfaces use
// -- the invocation challenge here and a side-effect-free preflight built from
// the synthesis model -- so the two cannot state different boundaries.
//
// Every credential requirement is `durable: true`. The binding-invoker
// contract makes that flag the invoker's permission to persist the resolved
// value keyed from the challenge's target, granted "only when reuse is safe";
// a declared credential is resolved once per binding and reused on every
// later invocation, which is the amortization claim the context/input
// discriminator states for context generally, and the same claim the OpenAPI
// 3.x lane already makes on its own security requirements. The false return
// reports a scheme type outside the closed set, which has no requirement
// family: the refusal then stays the plain species rather than naming a
// resolution path no runtime could take.
func Swagger20CredentialRequirement(schemeType, name string, scopes []string) (Requirement, bool) {
	durable := true
	entry := Requirement{Name: name, Durable: &durable}
	switch schemeType {
	case "basic":
		entry.Type = "auth.basic"
	case "apiKey":
		entry.Type = "auth.apiKey"
	case "oauth2":
		entry.Type = "auth.oauth2"
		if len(scopes) > 0 {
			entry.Extra = map[string]any{"scopes": append([]string(nil), scopes...)}
		}
	default:
		return Requirement{}, false
	}
	return entry, true
}

func swagger20CredentialRequirements(requirement map[string]any, definitions swagger20Member[swagger20Object]) ([]Requirement, bool) {
	names := make([]string, 0, len(requirement))
	for name := range requirement {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]Requirement, 0, len(names))
	for _, name := range names {
		if !definitions.present || !definitions.valid {
			return nil, false
		}
		rawDefinition, found := definitions.value.member(name)
		definitionMap, valid := rawDefinition.(map[string]any)
		if !found || !valid {
			return nil, false
		}
		var scopes []string
		if rawScopes, ok := requirement[name].([]any); ok {
			for _, rawScope := range rawScopes {
				if scope, ok := rawScope.(string); ok {
					scopes = append(scopes, scope)
				}
			}
		}
		entry, ok := Swagger20CredentialRequirement(swagger20Object(definitionMap).string("type").value, name, scopes)
		if !ok {
			return nil, false
		}
		result = append(result, entry)
	}
	if len(result) == 0 {
		return nil, false
	}
	return result, true
}

// ContextTarget is the context scope a CONTEXT_REQUIRED challenge from this
// operation asserts, and the scope a side-effect-free preflight asserts for
// the same operation. The binding-invoker contract defines the target as "the
// concrete destination or context scope the invoker is about to use". Once
// the §10 server resolves -- scheme, host, and basePath, or the configured
// replacement URL, never userinfo -- that destination is known and is the
// target, exactly as the OpenAPI 3.x lane asserts its resolved server base on
// every credential and media challenge. Where the server itself cannot
// resolve, the only scope this operation can assert is the source location
// the caller supplied, which is what the 3.x lane asserts on its own server
// challenge; a content-only source then asserts nothing. Resolution is pure,
// so this can be answered before any challenge without a side effect.
func (p *Swagger20PreparedOperation) ContextTarget() string {
	if p == nil {
		return ""
	}
	serverBase, err := resolveSwagger20Server(p.document, p.operation, p.options.Server, p.options.ServerSchemeIndex)
	if err != nil {
		return p.options.Source.Location
	}
	return serverBase
}

// swagger20RefusalError applies §3.2's discriminator to one pre-dispatch
// refusal. A refusal a named §12.1 point or a declared credential would repair
// is the context-required species and carries that resolution path; every
// other refusal is the plain species and carries nothing. `target` is the
// asserted context scope, ContextTarget above -- the same scope the
// side-effect-free preflight asserts.
func swagger20RefusalError(err error, target string) *ExecutionError {
	if err == nil {
		return nil
	}
	var existing *ExecutionError
	if errors.As(err, &existing) {
		return existing
	}
	var config *configRequired
	if errors.As(err, &config) {
		return newContextRequiredError(config.description, &Prerequisites{
			Target: target,
			Alternatives: []RequirementAlternative{{Requirements: []Requirement{
				newConfigValueRequirementCompat(config.point, config.path, config.description, config.schema, config.durable),
			}}},
		})
	}
	var credentials *swagger20CredentialsRequired
	if errors.As(err, &credentials) {
		return newContextRequiredError(credentials.message, &Prerequisites{
			Target:       target,
			Alternatives: []RequirementAlternative{{Requirements: credentials.requirements}},
		})
	}
	return &ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err}
}

func swagger20MissingCredentials(requirement map[string]any, definitions swagger20Member[swagger20Object], format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	requirements, ok := swagger20CredentialRequirements(requirement, definitions)
	if !ok {
		return errors.New(message)
	}
	return &swagger20CredentialsRequired{requirements: requirements, message: message}
}

// serverRefusal decides the species of a server-resolution failure. §12.1's
// `server` row admits either one effective scheme with the artifact's own
// host and basePath, or "one complete URL ... replacing the resolved server
// base", so the question "would a supplied server repair this?" is answered by
// resolving again with one. That is the same probe the synthesis surface
// already uses to decide whether to declare the requirement, so the two
// surfaces cannot drift apart. A caller who already configured the point is
// looking at their own value, not at an awaited one.
func (p *Swagger20PreparedOperation) serverRefusal(err error) error {
	if err == nil || p.options.Server != "" || p.options.ServerSchemeIndex != nil {
		return err
	}
	if _, repaired := resolveSwagger20Server(p.document, p.operation, "https://configured.invalid", nil); repaired != nil {
		return err
	}
	return swagger20ConfigRequired("server", "")
}
