package openapiclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
)

func loadDocument(ctx context.Context, client *http.Client, source Source, allowExternalRefs bool) (*openapi3.T, *acceptanceFloor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = defaultHTTPClient()
	}
	if source.Document != nil {
		// A pre-loaded typed document carries no raw artifact image, so the
		// acceptance floor is not computed for this lane (the caller that
		// loaded it owns the floor).
		document := source.Document
		if err := checkAcceptedOpenAPIVersion(document); err != nil {
			return nil, nil, err
		}
		localizeReferenceMetadata(document)
		return document, nil, nil
	}
	// The artifact's own entry image, captured once by the first attempt and
	// never overwritten: it is what the acceptance floor classifies against
	// and what block 8d-2's confinement pass reads.
	var entryBytes []byte
	if source.Content != nil {
		entryBytes = append([]byte(nil), source.Content...)
	}

	// attempt runs one complete shipped load. `entryOverride`, when non-nil,
	// replaces the ENTRY document's bytes at the seam block 8a proved --
	// before normalizeResource, in every lane -- so a confined retry keeps
	// each lane's own base-URI and retrieval semantics.
	attempt := func(entryOverride []byte) (*openapi3.T, error) {
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
		entrySeen := false
		loader.ReadFromURIFunc = func(loader *openapi3.Loader, resource *url.URL) ([]byte, error) {
			data, err := read(loader, resource)
			if err != nil {
				return nil, err
			}
			// The entry document is the loader's first resource read ONLY in
			// the location lane. When content is co-present the entry never
			// passes through here, and this call is an EXTERNAL resource --
			// substituting the entry into it is exactly the confusion that
			// dropped an externally referenced Path Item member.
			if source.Content == nil && !entrySeen {
				entrySeen = true
				if entryBytes == nil {
					entryBytes = append([]byte(nil), data...)
				}
				if entryOverride != nil {
					data = append([]byte(nil), entryOverride...)
				}
			}
			data = composition.prune(resource, data)
			// A reference the edition's own text makes unresolvable is reported
			// here, at the seam that already serves every resource, rather than
			// left for the typed loader to fail on some later symptom.
			if err := composition.refusal(); err != nil {
				return nil, err
			}
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
			entry := source.Content
			if entryOverride != nil {
				entry = entryOverride
			}
			data, normalizeErr := normalizer.normalizeResource(entry, resource)
			if normalizeErr != nil {
				return nil, normalizeErr
			}
			composition.setEntry(resource, data)
			// Embedded content never passes through ReadFromURIFunc, so the entry's
			// own tree is read here instead. It is the same call the location lane
			// makes from `prune`, and it retrieves nothing.
			composition.scanEntry(data)
			if err := composition.refusal(); err != nil {
				return nil, err
			}
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

	document, err := attempt(nil)
	if err != nil {
		// Fast path first: confinement is reached only after the shipped load
		// has already refused. On any confinement failure the ORIGINAL error
		// stands.
		//
		// The emission gate is nil here, and that is a decision rather than an
		// omission. The URef round is the one mechanism that AUTHORS a value,
		// and it may only be admitted by an engine that can show what it
		// authored never reaches emitted content. This engine derives no
		// interface from a document: it prepares one operation at a time and
		// builds a request at execution, so it has no emission of its own to
		// compare and cannot make that showing. It therefore declines the round
		// and keeps the loader's original error -- the behaviour before the
		// round existed. Every other mechanism is unaffected.
		confined, confinedErr, took := confineEntryDocument(entryBytes, attempt, err, nil)
		switch {
		case !took:
			// A load failure does not preempt the whole-source refusal: part 2's
			// refusal is decided over the artifact's raw image, which is in hand,
			// and it is the document's own reason. Block 8h: a confinement that
			// declines because it cannot gate itself now leaves the loader's
			// error standing where it used to leave a confined document that
			// then reached this refusal a few lines later.
			if floor := computeAcceptanceFloorFromBytes(entryBytes); floor != nil && floor.Refusal != "" {
				return nil, nil, errors.New(floor.Refusal)
			}
			return nil, nil, err
		case confinedErr != nil:
			return nil, nil, confinedErr
		default:
			document = confined
		}
	}
	// The invalid-artifact acceptance floor (openbindings.openapi@1 §3),
	// computed over the entry document's raw image. Part 2's single derived
	// whole-source refusal is returned as a load failure; per-target
	// verdicts ride with the document for the prepare-time inventory filter.
	floor := computeAcceptanceFloorFromBytes(entryBytes)
	if floor != nil && floor.Refusal != "" {
		return nil, nil, errors.New(floor.Refusal)
	}
	return document, floor, nil
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
		ctx := loader.Context
		if ctx == nil {
			ctx = context.Background()
		}
		data, err := readArtifactResource(ctx, client, resource, selfContained, retrievalURIs, retrievalMu)
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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Preserve Content-Encoding for declaration governance and configured
	// response decoders; the standard transport's implicit gzip decoder would
	// otherwise erase the field before the OpenAPI response is selected.
	transport.DisableCompression = true
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
