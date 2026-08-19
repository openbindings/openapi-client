package openapiclient

import "strings"

func contextString(ctx map[string]any, key string) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx[key].(string)
	return value
}

func contextBearerToken(ctx map[string]any) string { return contextString(ctx, "bearerToken") }

func contextNamedCredential(ctx map[string]any, name string) any {
	if ctx == nil || name == "" {
		return nil
	}
	values, _ := ctx["credentials"].(map[string]any)
	return values[name]
}

func contextBearerTokenFor(ctx map[string]any, name string) string {
	if value, ok := contextNamedCredential(ctx, name).(string); ok && value != "" {
		return value
	}
	return contextBearerToken(ctx)
}

func contextAPIKeyFor(ctx map[string]any, name string) string {
	if ctx == nil {
		return ""
	}
	if name != "" {
		if value, ok := contextNamedCredential(ctx, name).(string); ok && value != "" {
			return value
		}
		if keys, ok := ctx["apiKeys"].(map[string]any); ok {
			if value, ok := keys[name].(string); ok && value != "" {
				return value
			}
		}
	}
	return contextString(ctx, "apiKey")
}

func contextBasicAuthFor(ctx map[string]any, name string) (string, string, bool) {
	if value, ok := contextNamedCredential(ctx, name).(map[string]any); ok {
		username, _ := value["username"].(string)
		password, _ := value["password"].(string)
		if username != "" || password != "" {
			return username, password, true
		}
	}
	return contextBasicAuth(ctx)
}

func contextAccessTokenFor(ctx map[string]any, name string) string {
	if value, ok := contextNamedCredential(ctx, name).(map[string]any); ok {
		if token, ok := value["accessToken"].(string); ok && token != "" {
			return token
		}
	}
	return contextString(ctx, "accessToken")
}

func contextBasicAuth(ctx map[string]any) (string, string, bool) {
	if ctx == nil {
		return "", "", false
	}
	basic, _ := ctx["basic"].(map[string]any)
	if basic == nil {
		return "", "", false
	}
	username, _ := basic["username"].(string)
	password, _ := basic["password"].(string)
	return username, password, username != "" || password != ""
}

func contextHeaders(ctx map[string]any) map[string]string { return contextStringMap(ctx, "headers") }
func contextCookies(ctx map[string]any) map[string]string { return contextStringMap(ctx, "cookies") }

func contextStringMap(ctx map[string]any, key string) map[string]string {
	if ctx == nil {
		return nil
	}
	switch value := ctx[key].(type) {
	case map[string]string:
		return value
	case map[string]any:
		out := make(map[string]string, len(value))
		for name, member := range value {
			text, ok := member.(string)
			if !ok {
				return nil
			}
			out[name] = text
		}
		return out
	default:
		return nil
	}
}

func contextMetadata(ctx map[string]any) map[string]any {
	if ctx == nil {
		return nil
	}
	value, _ := ctx["metadata"].(map[string]any)
	return value
}

func contextConfiguration(ctx map[string]any) map[string]any {
	if ctx == nil {
		return nil
	}
	value, _ := ctx["configuration"].(map[string]any)
	return value
}

func newConfigValueRequirement(point, path, description string, choices []string, durable *bool) Requirement {
	extra := map[string]any{"point": point, "path": path}
	if len(choices) > 0 {
		extra["choices"] = append([]string(nil), choices...)
	}
	return Requirement{Type: "config.value", Description: description, Durable: durable, Extra: extra}
}

func contextSatisfies(ctx map[string]any, details *Prerequisites) bool {
	if details == nil {
		return true
	}
	for _, alternative := range details.Alternatives {
		if len(alternative.Requirements) == 0 {
			continue
		}
		satisfied := true
		for _, requirement := range alternative.Requirements {
			if !requirementSatisfied(ctx, requirement, flatRequirementIsUnambiguous(details, requirement)) {
				satisfied = false
				break
			}
		}
		if satisfied {
			return true
		}
	}
	return false
}

func flatRequirementIsUnambiguous(details *Prerequisites, requirement Requirement) bool {
	identities := map[string]struct{}{}
	unnamed := 0
	for _, alternative := range details.Alternatives {
		for _, candidate := range alternative.Requirements {
			if candidate.Type != requirement.Type {
				continue
			}
			if candidate.Name == "" {
				unnamed++
			} else {
				identities[candidate.Name] = struct{}{}
			}
		}
	}
	return len(identities)+unnamed == 1
}

// contextAnonymous reports whether the caller asserted that this invocation
// carries no credentials. See the twin in openbindings-go/contextstore.go for
// the full reasoning; in short, an OpenAPI document's `security` states what
// the API accepts while the server decides what it enforces, and a public read
// under a blanket document-level requirement is otherwise unreachable.
func contextAnonymous(ctx map[string]any) bool {
	value, ok := ctx["anonymous"].(bool)
	return ok && value
}

func requirementSatisfied(ctx map[string]any, requirement Requirement, allowFlatNamedCredential bool) bool {
	// Anonymity answers credential requirements and nothing else: a
	// `config.value` point is not a credential, so an alternative mixing the
	// two still has to answer the configuration half.
	if contextAnonymous(ctx) && strings.HasPrefix(requirement.Type, "auth.") {
		return true
	}
	if requirement.Name != "" {
		if configured, ok := ctx["$openapiSecurity"].(map[string]any); ok {
			if present, _ := configured[requirement.Name].(bool); present {
				return true
			}
		}
		if configured, ok := ctx["$openapiSecurity"].(map[string]bool); ok && configured[requirement.Name] {
			return true
		}
	}
	switch requirement.Type {
	case "auth.bearer":
		if value, ok := contextNamedCredential(ctx, requirement.Name).(string); ok && value != "" {
			return true
		}
		return allowFlatNamedCredential && contextBearerToken(ctx) != ""
	case "auth.apiKey":
		if value, ok := contextNamedCredential(ctx, requirement.Name).(string); ok && value != "" {
			return true
		}
		if requirement.Name != "" {
			if keys, ok := ctx["apiKeys"].(map[string]any); ok {
				if value, ok := keys[requirement.Name].(string); ok && value != "" {
					return true
				}
			}
		}
		return allowFlatNamedCredential && contextString(ctx, "apiKey") != ""
	case "auth.basic":
		if value, ok := contextNamedCredential(ctx, requirement.Name).(map[string]any); ok {
			username, hasUsername := value["username"].(string)
			password, hasPassword := value["password"].(string)
			if hasUsername && hasPassword && (username != "" || password != "") {
				return true
			}
		}
		_, _, ok := contextBasicAuth(ctx)
		return allowFlatNamedCredential && ok
	case "auth.oauth2":
		if contextAccessTokenFor(ctx, requirement.Name) != "" && (contextNamedCredential(ctx, requirement.Name) != nil || allowFlatNamedCredential) {
			return true
		}
		// A flat bearer token counts, because an OAuth2 access token reaches
		// the wire AS a Bearer credential and the placement side already
		// falls back to it when no accessToken is present. Until the two
		// agreed, an oauth2-declaring artifact was a dead end: the challenge
		// asked for context, the remedy it printed stored a bearerToken, and
		// the next attempt challenged identically.
		return allowFlatNamedCredential && contextBearerToken(ctx) != ""
	}
	if requirement.Type == "config.value" {
		point, _ := requirement.Extra["point"].(string)
		path, pathPresent := requirement.Extra["path"].(string)
		value, present := contextConfiguration(ctx)[point]
		if point == "" || !pathPresent || !present {
			return false
		}
		selected, selectedPresent := configurationValueAt(value, path)
		return selectedPresent && selected != nil && selected != ""
	}
	field := map[string]string{
		"auth.bearer": "bearerToken",
		"auth.apiKey": "apiKey",
		"auth.basic":  "basic",
		"auth.oauth2": "accessToken",
	}[requirement.Type]
	if field == "" {
		if strings.HasPrefix(requirement.Type, "auth.") {
			return false
		}
		field = requirement.Type
	}
	value, present := ctx[field]
	return present && value != nil && value != ""
}

func configurationValueAt(root any, path string) (any, bool) {
	if path == "" {
		return root, true
	}
	if !strings.HasPrefix(path, "/") {
		return nil, false
	}
	current := root
	for _, raw := range strings.Split(path[1:], "/") {
		for index := 0; index < len(raw); index++ {
			if raw[index] == '~' && (index+1 >= len(raw) || (raw[index+1] != '0' && raw[index+1] != '1')) {
				return nil, false
			}
			if raw[index] == '~' {
				index++
			}
		}
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		record, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = record[token]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
