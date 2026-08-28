package openapiclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

// readArtifactResource is the edition-neutral source-acquisition seam. It
// owns transport, retrieval-URI recording, and local-file reads; edition
// loaders own grammar, gates, reference semantics, and typed interpretation.
func readArtifactResource(
	ctx context.Context,
	client *http.Client,
	resource *url.URL,
	selfContained bool,
	retrievalURIs map[string]*url.URL,
	retrievalMu *sync.RWMutex,
) ([]byte, error) {
	if resource == nil {
		return nil, fmt.Errorf("OpenAPI artifact resource is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = defaultHTTPClient()
	}

	var data []byte
	var err error
	switch strings.ToLower(resource.Scheme) {
	case "http", "https":
		if !resource.IsAbs() || resource.Host == "" {
			return nil, fmt.Errorf("reference %q is not an absolute HTTP URI", resource)
		}
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, resource.String(), nil)
		if requestErr != nil {
			return nil, requestErr
		}
		response, requestErr := client.Do(request)
		if requestErr != nil {
			return nil, requestErr
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("load %s: %s", resource, response.Status)
		}
		finalURI := resource
		if response.Request != nil && response.Request.URL != nil {
			finalURI = response.Request.URL
		}
		retrievalMu.Lock()
		retrievalURIs[artifactResourceKey(resource)] = cloneURL(finalURI)
		retrievalMu.Unlock()
		data, err = io.ReadAll(response.Body)
	case "", "file":
		if selfContained {
			return nil, fmt.Errorf("reference %q cannot resolve: embedded content with no co-present location has no base URI and must be self-contained", resource)
		}
		if resource.Scheme == "" {
			return nil, fmt.Errorf("relative reference %q has no absolute artifact base", resource)
		}
		data, err = os.ReadFile(resource.Path)
	default:
		return nil, fmt.Errorf("unsupported OpenAPI artifact URI scheme %q", resource.Scheme)
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}
