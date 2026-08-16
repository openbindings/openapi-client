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

	"github.com/getkin/kin-openapi/openapi3"
)

func loadDocument(ctx context.Context, client *http.Client, source Source, allowExternalRefs bool) (*openapi3.T, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = defaultHTTPClient()
	}
	if source.Document != nil {
		document := source.Document
		if err := checkAcceptedOpenAPIVersion(document); err != nil {
			return nil, err
		}
		localizeReferenceMetadata(document)
		return document, nil
	}
	loader := openapi3.NewLoader()
	loader.Context = ctx
	loader.IsExternalRefsAllowed = allowExternalRefs
	retrievalURIs := map[string]*url.URL{}
	var retrievalMu sync.RWMutex
	loader.JoinFunc = artifactJoinFunc(retrievalURIs, &retrievalMu)
	normalizer := newRawRefSiblingNormalizer(loader.JoinFunc)
	read := artifactReadFunc(client, source.Content != nil && source.Location == "", retrievalURIs, &retrievalMu)
	composition := newExternalComposition(
		func(resource *url.URL) ([]byte, error) { return read(loader, resource) },
		loader.JoinFunc,
	)
	loader.ReadFromURIFunc = func(loader *openapi3.Loader, resource *url.URL) ([]byte, error) {
		data, err := read(loader, resource)
		if err != nil {
			return nil, err
		}
		data = composition.prune(resource, data)
		return normalizer.normalizeResourceAt(data, resource, artifactRetrievalURI(resource, retrievalURIs, &retrievalMu))
	}

	var document *openapi3.T
	var err error
	if source.Content != nil {
		var resource *url.URL
		if source.Location != "" {
			resource, err = absoluteDocumentURL(source.Location)
			if err != nil {
				return nil, err
			}
		}
		data, normalizeErr := normalizer.normalizeResource(source.Content, resource)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		composition.setEntry(resource, data)
		if resource != nil {
			document, err = loader.LoadFromDataWithPath(data, resource)
		} else {
			document, err = loader.LoadFromData(data)
		}
	} else {
		if source.Location == "" {
			return nil, fmt.Errorf("OpenAPI source requires location or content")
		}
		resource, parseErr := absoluteDocumentURL(source.Location)
		if parseErr != nil {
			return nil, parseErr
		}
		composition.setEntry(resource, nil)
		document, err = loader.LoadFromURI(resource)
	}
	if err != nil {
		return nil, err
	}
	localizeReferenceMetadata(document)
	if err := checkAcceptedOpenAPIVersion(document); err != nil {
		return nil, err
	}
	return document, nil
}

func absoluteDocumentURL(location string) (*url.URL, error) {
	resource, err := url.Parse(location)
	if err != nil || resource.Scheme == "" || resource.Opaque != "" {
		return nil, fmt.Errorf("openapi location %q is not an absolute URI addressing the document", location)
	}
	return resource, nil
}

func artifactReadFunc(client *http.Client, selfContained bool, retrievalURIs map[string]*url.URL, retrievalMu *sync.RWMutex) openapi3.ReadFromURIFunc {
	cache := map[string][]byte{}
	var cacheMu sync.RWMutex
	return func(loader *openapi3.Loader, resource *url.URL) ([]byte, error) {
		key := resource.String()
		cacheMu.RLock()
		cached, found := cache[key]
		cacheMu.RUnlock()
		if found {
			return append([]byte(nil), cached...), nil
		}
		var data []byte
		var err error
		switch strings.ToLower(resource.Scheme) {
		case "http", "https":
			if !resource.IsAbs() || resource.Host == "" {
				return nil, fmt.Errorf("reference %q is not an absolute HTTP URI", resource)
			}
			ctx := loader.Context
			if ctx == nil {
				ctx = context.Background()
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
		cacheMu.Lock()
		cache[key] = append([]byte(nil), data...)
		cacheMu.Unlock()
		return data, nil
	}
}

func artifactJoinFunc(retrievalURIs map[string]*url.URL, retrievalMu *sync.RWMutex) func(*url.URL, *url.URL) *url.URL {
	return func(base, relative *url.URL) *url.URL {
		if base == nil {
			return relative
		}
		retrievalMu.RLock()
		resolvedBase := cloneURL(retrievalURIs[artifactResourceKey(base)])
		retrievalMu.RUnlock()
		if resolvedBase == nil {
			resolvedBase = base
		}
		return resolvedBase.ResolveReference(relative)
	}
}

func artifactRetrievalURI(resource *url.URL, retrievalURIs map[string]*url.URL, retrievalMu *sync.RWMutex) *url.URL {
	if resource == nil {
		return nil
	}
	retrievalMu.RLock()
	resolved := cloneURL(retrievalURIs[artifactResourceKey(resource)])
	retrievalMu.RUnlock()
	if resolved != nil {
		return resolved
	}
	return resource
}

func checkAcceptedOpenAPIVersion(document *openapi3.T) error {
	if document == nil {
		return fmt.Errorf("OpenAPI document is nil")
	}
	version := document.OpenAPI
	accepted := version == "3.0.0" || version == "3.0.1" || version == "3.0.2" || version == "3.0.3" || version == "3.0.4" ||
		version == "3.1.0" || version == "3.1.1" || version == "3.1.2"
	if !accepted {
		return fmt.Errorf("unsupported OpenAPI version %q", version)
	}
	return nil
}

func defaultHTTPClient() *http.Client {
	return &http.Client{}
}

// defaultInvocationHTTPClient keeps the response to the artifact-bound
// operation observable. In particular, an ordinary user-agent redirect can
// rewrite POST to GET or replay a body at another target. Standalone callers
// can opt into that behavior by supplying their own *http.Client.
func defaultInvocationHTTPClient() *http.Client {
	return &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}
