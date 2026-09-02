package openapiclient

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// Swagger20SecurityCredentials is the OpenAPI-native credential surface for
// Swagger 2.0 security definitions. Map presence denotes a supplied
// credential; credentials never enter Swagger20Input.
type Swagger20SecurityCredentials struct {
	Basic   map[string]Swagger20BasicCredential
	APIKeys map[string]string
	OAuth2  map[string]Swagger20OAuth2Credential
}

type Swagger20BasicCredential struct {
	UserID   string
	Password string
}

type Swagger20OAuth2Credential struct {
	AccessToken string
	Scopes      []string
}

type swagger20CredentialPlacement struct {
	query bool
	name  string
	value string
}

func selectSwagger20Security(document *Swagger20Document, operation swagger20Operation, parameters *swagger20ParameterSet, selection *int, credentials Swagger20SecurityCredentials) ([]swagger20CredentialPlacement, error) {
	requirements, err := effectiveSwagger20Security(document, operation)
	if err != nil {
		return nil, err
	}
	if len(requirements) == 0 {
		return nil, nil
	}
	selected := 0
	if selection != nil {
		selected = *selection
	} else if len(requirements) != 1 {
		return nil, swagger20ConfigRequired("security", "")
	}
	if selected < 0 || selected >= len(requirements) {
		return nil, fmt.Errorf("Swagger 2.0 security alternative index %d is outside the effective requirement list", selected)
	}
	requirement, ok := requirements[selected].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Swagger 2.0 security alternative %d is not an object", selected)
	}
	if len(requirement) == 0 {
		return nil, nil
	}
	definitions := document.root.object("securityDefinitions")
	if !definitions.present || !definitions.valid {
		return nil, fmt.Errorf("Swagger 2.0 selected security alternative has no usable root securityDefinitions object")
	}

	names := make([]string, 0, len(requirement))
	for name := range requirement {
		names = append(names, name)
	}
	sort.Strings(names)
	owned := map[string]string{}
	placements := make([]swagger20CredentialPlacement, 0, len(names))
	for _, name := range names {
		rawScopes, ok := requirement[name].([]any)
		if !ok {
			return nil, fmt.Errorf("Swagger 2.0 security requirement %q scopes must be an array", name)
		}
		requiredScopes := make([]string, len(rawScopes))
		for index, raw := range rawScopes {
			scope, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("Swagger 2.0 security requirement %q scope %d is not a string", name, index)
			}
			requiredScopes[index] = scope
		}
		rawDefinition, found := definitions.value.member(name)
		definitionMap, valid := rawDefinition.(map[string]any)
		if !found || !valid {
			return nil, fmt.Errorf("Swagger 2.0 security requirement %q names no usable root definition", name)
		}
		definition := swagger20Object(definitionMap)
		typeName := definition.string("type")
		if !typeName.present || !typeName.valid {
			return nil, fmt.Errorf("Swagger 2.0 security definition %q requires a string type", name)
		}

		var placement swagger20CredentialPlacement
		switch typeName.value {
		case "basic":
			if len(requiredScopes) != 0 {
				return nil, fmt.Errorf("Swagger 2.0 basic requirement %q must have an empty scopes array", name)
			}
			credential, supplied := credentials.Basic[name]
			if !supplied {
				return nil, swagger20MissingCredentials(requirement, definitions, "Swagger 2.0 basic credential %q is required", name)
			}
			if strings.Contains(credential.UserID, ":") || !validBasicCredentialText(credential.UserID) || !validBasicCredentialText(credential.Password) {
				return nil, fmt.Errorf("Swagger 2.0 basic credential %q violates RFC 7617 user-id or character-encoding constraints", name)
			}
			placement = swagger20CredentialPlacement{name: "Authorization", value: "Basic " + base64.StdEncoding.EncodeToString([]byte(credential.UserID+":"+credential.Password))}
		case "apiKey":
			if len(requiredScopes) != 0 {
				return nil, fmt.Errorf("Swagger 2.0 apiKey requirement %q must have an empty scopes array", name)
			}
			destination := definition.string("in")
			wireName := definition.string("name")
			if !destination.present || !destination.valid || !wireName.present || !wireName.valid || wireName.value == "" || (destination.value != "query" && destination.value != "header") {
				return nil, fmt.Errorf("Swagger 2.0 apiKey definition %q requires a nonempty name and query or header destination", name)
			}
			if destination.value == "header" && !httpguts.ValidHeaderFieldName(wireName.value) {
				return nil, fmt.Errorf("Swagger 2.0 apiKey definition %q has an invalid header destination", name)
			}
			value, supplied := credentials.APIKeys[name]
			if !supplied {
				return nil, swagger20MissingCredentials(requirement, definitions, "Swagger 2.0 apiKey credential %q is required", name)
			}
			if destination.value == "header" && !swagger20HTTPFieldValue(value) {
				return nil, fmt.Errorf("Swagger 2.0 apiKey credential %q contains a field-invalid byte", name)
			}
			placement = swagger20CredentialPlacement{query: destination.value == "query", name: wireName.value, value: value}
		case "oauth2":
			if err := validateSwagger20OAuth2Definition(name, definition, requiredScopes); err != nil {
				return nil, err
			}
			credential, supplied := credentials.OAuth2[name]
			// An absent credential is awaited and names its resolution path; a
			// supplied one this lane cannot use is a value the caller already
			// chose, so no further context changes the answer (§3.2).
			if !supplied {
				return nil, swagger20MissingCredentials(requirement, definitions, "Swagger 2.0 OAuth2 credential %q requires an RFC 6750 Bearer access token", name)
			}
			if !validBearerToken(credential.AccessToken) {
				return nil, fmt.Errorf("Swagger 2.0 OAuth2 credential %q requires an RFC 6750 Bearer access token", name)
			}
			// R1 (ratified 2026-09-01, stated identically at openapi-2.0:567 and in
			// all three 3.x siblings): whether a supplied credential satisfies a
			// required scope is the counterparty's own determination and is never
			// evaluated by this binding. No accepted edition assigns a client a
			// verification duty, none gives a credential's GRANTED scopes any
			// representation, and `scopes` is declared by binding-invoker 0.1 only
			// on the REQUIREMENT -- what the challenge tells a caller the operation
			// needs -- never on the credential. The three 3.x lanes evaluate no
			// scopes at all; this lane used to, which made one credential refuse on
			// 2.0 and dispatch on 3.0/3.1/3.2. A token the counterparty finds
			// insufficient produces that counterparty's own response, which
			// classifies under section 9 like any other outcome.
			placement = swagger20CredentialPlacement{name: "Authorization", value: "Bearer " + credential.AccessToken}
		default:
			return nil, fmt.Errorf("Swagger 2.0 security definition %q has inadmissible type %q", name, typeName.value)
		}

		key := swagger20CredentialDestinationKey(placement)
		if previous := owned[key]; previous != "" {
			return nil, fmt.Errorf("Swagger 2.0 credentials %q and %q collide at one wire destination", previous, name)
		}
		if swagger20CredentialCollidesWithParameter(placement, parameters) {
			return nil, fmt.Errorf("Swagger 2.0 credential %q collides with an effective Parameter", name)
		}
		if !placement.query && swagger20ProcessorOwnedHeader(placement.name) {
			return nil, fmt.Errorf("Swagger 2.0 credential %q collides with processor-owned header %q", name, placement.name)
		}
		owned[key] = name
		placements = append(placements, placement)
	}
	return placements, nil
}

func effectiveSwagger20Security(document *Swagger20Document, operation swagger20Operation) ([]any, error) {
	member := document.root.array("security")
	if operationMember := operation.raw.array("security"); operationMember.present {
		member = operationMember
	}
	if !member.present {
		return nil, nil
	}
	if !member.valid {
		return nil, fmt.Errorf("Swagger 2.0 effective security field is not an array")
	}
	return member.value, nil
}

func validateSwagger20OAuth2Definition(name string, definition swagger20Object, requiredScopes []string) error {
	flow := definition.string("flow")
	if !flow.present || !flow.valid {
		return fmt.Errorf("Swagger 2.0 OAuth2 definition %q requires a string flow", name)
	}
	requireURL := func(member string) bool {
		value := definition.string(member)
		return value.present && value.valid && value.value != ""
	}
	switch flow.value {
	case "implicit":
		if !requireURL("authorizationUrl") {
			return fmt.Errorf("Swagger 2.0 implicit OAuth2 definition %q requires authorizationUrl", name)
		}
	case "password", "application":
		if !requireURL("tokenUrl") {
			return fmt.Errorf("Swagger 2.0 %s OAuth2 definition %q requires tokenUrl", flow.value, name)
		}
	case "accessCode":
		if !requireURL("authorizationUrl") || !requireURL("tokenUrl") {
			return fmt.Errorf("Swagger 2.0 accessCode OAuth2 definition %q requires authorizationUrl and tokenUrl", name)
		}
	default:
		return fmt.Errorf("Swagger 2.0 OAuth2 definition %q has inadmissible flow %q", name, flow.value)
	}
	scopes := definition.object("scopes")
	if !scopes.present || !scopes.valid {
		return fmt.Errorf("Swagger 2.0 OAuth2 definition %q requires a scopes object", name)
	}
	for scope, rawDescription := range scopes.value {
		if _, ok := rawDescription.(string); !ok {
			return fmt.Errorf("Swagger 2.0 OAuth2 definition %q scope %q description is not a string", name, scope)
		}
	}
	for _, required := range requiredScopes {
		if _, present := scopes.value[required]; !present {
			return fmt.Errorf("Swagger 2.0 OAuth2 requirement %q names undeclared scope %q", name, required)
		}
	}
	return nil
}

func swagger20CredentialDestinationKey(placement swagger20CredentialPlacement) string {
	if placement.query {
		return "query\x00" + placement.name
	}
	return "header\x00" + strings.ToLower(placement.name)
}

func swagger20CredentialCollidesWithParameter(placement swagger20CredentialPlacement, parameters *swagger20ParameterSet) bool {
	if parameters == nil {
		return false
	}
	if placement.query {
		return parameters.byWire[Swagger20ParameterQuery][placement.name] != nil
	}
	for name := range parameters.byWire[Swagger20ParameterHeader] {
		if strings.EqualFold(name, placement.name) {
			return true
		}
	}
	return false
}

func swagger20ProcessorOwnedHeader(name string) bool {
	return strings.EqualFold(name, "Host") || strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, "Content-Type")
}

func applySwagger20Security(routed *swagger20RoutedInput, placements []swagger20CredentialPlacement) {
	for _, placement := range placements {
		if placement.query {
			routed.query = append(routed.query, swagger20WireContribution{
				name: swagger20PercentEncode(placement.name), value: swagger20PercentEncode(placement.value), valuePresent: true,
			})
			continue
		}
		routed.headers = append(routed.headers, swagger20WireContribution{name: placement.name, value: placement.value, valuePresent: true})
	}
}
