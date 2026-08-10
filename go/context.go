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

func contextAPIKeyFor(ctx map[string]any, name string) string {
	if ctx == nil {
		return ""
	}
	if name != "" {
		if keys, ok := ctx["apiKeys"].(map[string]any); ok {
			if value, ok := keys[name].(string); ok && value != "" {
				return value
			}
		}
	}
	return contextString(ctx, "apiKey")
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

func newConfigValueRequirement(point, key, description string, choices []string, durable *bool) Requirement {
	extra := map[string]any{"point": point, "key": key}
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
			if !requirementSatisfied(ctx, requirement) {
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

func requirementSatisfied(ctx map[string]any, requirement Requirement) bool {
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
	if requirement.Type == "auth.apiKey" && requirement.Name != "" {
		return contextAPIKeyFor(ctx, requirement.Name) != ""
	}
	if requirement.Type == "config.value" {
		point, _ := requirement.Extra["point"].(string)
		value, present := contextConfiguration(ctx)[point]
		return point != "" && present && value != nil && value != ""
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
