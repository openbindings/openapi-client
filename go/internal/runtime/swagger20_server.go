package openapiclient

import (
	"fmt"
	"net/url"
	"strings"
)

// resolveSwagger20Server builds the exact Swagger 2.0 target base. A complete
// configured URL replaces the artifact scheme, authority, and basePath; an
// index selects one authored effective scheme when more than one HTTP scheme
// remains usable.
func resolveSwagger20Server(document *Swagger20Document, operation swagger20Operation, configured string, schemeIndex *int) (string, error) {
	if document == nil || document.entry == nil {
		return "", fmt.Errorf("Swagger 2.0 document has no entry resource")
	}
	if !strings.HasPrefix(operation.path, "/") {
		return "", fmt.Errorf("Swagger 2.0 Paths key %q must begin with /", operation.path)
	}
	if configured != "" {
		if schemeIndex != nil {
			return "", fmt.Errorf("configuration.server cannot combine a complete URL with a scheme index")
		}
		if err := validateSwagger20ConfiguredServer(configured); err != nil {
			return "", err
		}
		return configured, nil
	}

	root := document.root
	host := root.string("host")
	if host.present && (!host.valid || host.value == "" || !swagger20Host(host.value)) {
		return "", fmt.Errorf("Swagger 2.0 host must contain only an authority host and optional port")
	}
	basePath := root.string("basePath")
	if basePath.present && (!basePath.valid || basePath.value == "" || !strings.HasPrefix(basePath.value, "/") || strings.ContainsAny(basePath.value, "?#")) {
		return "", fmt.Errorf("Swagger 2.0 basePath must be a nonempty absolute path without query or fragment")
	}

	retrieval := document.entry.base()
	effectiveSchemes, authored, err := swagger20EffectiveSchemes(root, operation, retrieval)
	if err != nil {
		return "", err
	}
	selected := ""
	if schemeIndex != nil {
		if *schemeIndex < 0 || *schemeIndex >= len(authored) {
			return "", fmt.Errorf("Swagger 2.0 server scheme index %d is outside the effective scheme list", *schemeIndex)
		}
		candidate := authored[*schemeIndex]
		if candidate != "http" && candidate != "https" {
			return "", fmt.Errorf("Swagger 2.0 effective scheme %q is unusable for HTTP execution", candidate)
		}
		selected = candidate
	} else {
		if len(effectiveSchemes) != 1 {
			if len(effectiveSchemes) == 0 {
				return "", fmt.Errorf("Swagger 2.0 target has no usable http or https scheme")
			}
			return "", fmt.Errorf("Swagger 2.0 target has %d usable schemes; configuration.server must select one", len(effectiveSchemes))
		}
		selected = effectiveSchemes[0]
	}

	effectiveHost := host.value
	if !host.present {
		if retrieval == nil || retrieval.Host == "" {
			return "", fmt.Errorf("Swagger 2.0 target omits host without a document retrieval authority")
		}
		effectiveHost = retrieval.Host
	}
	// An absent basePath means the API is served directly under the host, so it
	// contributes no path segment: the Paths key appends straight to the
	// authority with no synthetic "/" and no "//" composition. An authored
	// basePath keeps its exact bytes except that a lone root slash is the same
	// root boundary as an omitted basePath. Joining it to a Paths key must not
	// manufacture a double slash.
	effectiveBasePath := ""
	if basePath.present && basePath.value != "/" {
		effectiveBasePath = basePath.value
	}
	return selected + "://" + effectiveHost + effectiveBasePath, nil
}

func swagger20EffectiveSchemes(root swagger20Object, operation swagger20Operation, retrieval *url.URL) (usable, authored []string, err error) {
	member := root.array("schemes")
	if operationMember := operation.raw.array("schemes"); operationMember.present {
		member = operationMember
	}
	if !member.present {
		if retrieval == nil || retrieval.Scheme == "" {
			return nil, nil, fmt.Errorf("Swagger 2.0 target omits schemes without a document retrieval scheme")
		}
		inherited := strings.ToLower(retrieval.Scheme)
		if inherited != "http" && inherited != "https" && inherited != "ws" && inherited != "wss" {
			return nil, nil, fmt.Errorf("Swagger 2.0 retrieval scheme %q is not an effective HTTP or WebSocket scheme", retrieval.Scheme)
		}
		authored = []string{inherited}
	} else {
		if !member.valid || len(member.value) == 0 {
			return nil, nil, fmt.Errorf("Swagger 2.0 effective schemes must be a nonempty array")
		}
		for index, value := range member.value {
			scheme, ok := value.(string)
			if !ok {
				return nil, nil, fmt.Errorf("Swagger 2.0 effective scheme %d is not a string", index)
			}
			if scheme != "http" && scheme != "https" && scheme != "ws" && scheme != "wss" {
				// The defect belongs to this scheme alternative. It is removed
				// before selection; a valid sibling remains usable.
				continue
			}
			authored = append(authored, scheme)
		}
	}
	for _, scheme := range authored {
		if scheme == "http" || scheme == "https" {
			usable = append(usable, scheme)
		}
	}
	return usable, authored, nil
}

func swagger20Host(value string) bool {
	if strings.ContainsAny(value, "/?#@") || strings.Contains(value, "://") {
		return false
	}
	parsed, err := url.Parse("http://" + value)
	return err == nil && parsed.Host == value && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func validateSwagger20ConfiguredServer(value string) error {
	if err := validateServerBaseSpelling(value); err != nil {
		return err
	}
	if !denotesTargetBase(value) {
		return fmt.Errorf("Swagger 2.0 consumer server override is not an absolute target URL")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("Swagger 2.0 consumer server scheme %q is unusable", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("Swagger 2.0 consumer server override has no authority")
	}
	if parsed.User != nil {
		return fmt.Errorf("Swagger 2.0 consumer server override contains forbidden userinfo")
	}
	return nil
}
