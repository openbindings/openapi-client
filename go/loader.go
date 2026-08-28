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
	artifact, floor, err := loadArtifact(ctx, client, source, allowExternalRefs)
	if artifact == nil {
		return nil, floor, err
	}
	return artifact.Document, floor, err
}

func loadArtifact(ctx context.Context, client *http.Client, source Source, allowExternalRefs bool) (*Artifact, *acceptanceFloor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = defaultHTTPClient()
	}
	if source.Artifact != nil {
		artifact := source.Artifact
		if artifact.Document == nil {
			return nil, nil, fmt.Errorf("OpenAPI artifact document is nil")
		}
		if err := checkAcceptedOpenAPIVersion(artifact.Document); err != nil {
			return nil, nil, err
		}
		if Edition(artifact.Document.OpenAPI) != artifact.Edition {
			return nil, nil, fmt.Errorf("OpenAPI artifact edition %q does not match document edition %q", artifact.Edition, artifact.Document.OpenAPI)
		}
		if artifact.Edition.IsOpenAPI32() && artifact.openAPI32 == nil {
			return nil, nil, fmt.Errorf("OpenAPI 3.2 artifact requires its raw-resource overlay")
		}
		localizeReferenceMetadata(artifact.Document)
		return artifact, nil, nil
	}
	if source.Document != nil {
		// A pre-loaded typed document carries no raw artifact image, so the
		// acceptance floor is not computed for this lane (the caller that
		// loaded it owns the floor).
		document := source.Document
		if err := checkAcceptedOpenAPIVersion(document); err != nil {
			return nil, nil, err
		}
		if Edition(document.OpenAPI).IsOpenAPI32() {
			return nil, nil, fmt.Errorf("a preloaded OpenAPI 3.2 document must be supplied as Source.Artifact so its raw-resource overlay is preserved")
		}
		localizeReferenceMetadata(document)
		return &Artifact{Document: document, Edition: Edition(document.OpenAPI)}, nil, nil
	}
	// The artifact's own entry image, captured once by the first attempt and
	// never overwritten: it is what the acceptance floor classifies against
	// and what block 8d-2's confinement pass reads.
	var entryBytes []byte
	if source.Content != nil {
		entryBytes = append([]byte(nil), source.Content...)
	}
	var edition Edition
	if entryBytes != nil {
		classified, err := ClassifyOpenAPIEdition(entryBytes)
		if err != nil {
			return nil, nil, err
		}
		edition = classified
	}
	var artifactOverlay *OpenAPI32Overlay

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
		var laneOverlay *OpenAPI32Overlay
		if edition.IsOpenAPI32() {
			laneOverlay = newOpenAPI32Overlay()
			if artifactOverlay == nil {
				artifactOverlay = laneOverlay
			}
		}
		loader.JoinFunc = func(base, relative *url.URL) *url.URL {
			resolvedBase := artifactRetrievalURI(base, retrievalURIs, &retrievalMu)
			if laneOverlay != nil {
				resolvedBase = laneOverlay.baseFor(resolvedBase)
			}
			if resolvedBase == nil {
				return relative
			}
			return resolvedBase.ResolveReference(relative)
		}
		normalizer := newRawRefSiblingNormalizer(loader.JoinFunc)
		read := artifactReadFunc(client, source.Content != nil && source.Location == "", retrievalURIs, &retrievalMu)
		hydrateSecurityResource := func(resource *url.URL) ([]byte, *url.URL, error) {
			data, readErr := read(loader, resource)
			if readErr != nil {
				return nil, nil, readErr
			}
			return data, artifactRetrievalURI(resource, retrievalURIs, &retrievalMu), nil
		}
		installOverlayResolver := func() {
			if laneOverlay != nil {
				laneOverlay.setResolver(hydrateSecurityResource)
			}
			if artifactOverlay != nil && artifactOverlay != laneOverlay {
				artifactOverlay.setResolver(hydrateSecurityResource)
			}
		}
		installOverlayResolver()
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
			isEntry := source.Content == nil && !entrySeen
			if isEntry {
				entrySeen = true
				if entryBytes == nil {
					entryBytes = append([]byte(nil), data...)
				}
				classified, classifyErr := ClassifyOpenAPIEdition(data)
				if classifyErr != nil {
					return nil, classifyErr
				}
				if edition != "" && edition != classified {
					return nil, fmt.Errorf("OpenAPI entry edition changed from %q to %q between load attempts", edition, classified)
				}
				edition = classified
				if edition.IsOpenAPI32() {
					laneOverlay = newOpenAPI32Overlay()
					if artifactOverlay == nil {
						artifactOverlay = laneOverlay
					}
					installOverlayResolver()
				}
				if entryOverride != nil {
					data = append([]byte(nil), entryOverride...)
				}
			}
			retrieval := artifactRetrievalURI(resource, retrievalURIs, &retrievalMu)
			if laneOverlay != nil {
				if captureErr := laneOverlay.capture(data, resource, retrieval, isEntry); captureErr != nil {
					return nil, captureErr
				}
				laneOverlay.hydrateSecurityRequirementURIs(resource, allowExternalRefs, hydrateSecurityResource)
				if !isEntry && artifactOverlay != nil && artifactOverlay != laneOverlay {
					if captureErr := artifactOverlay.capture(data, resource, retrieval, false); captureErr != nil {
						return nil, captureErr
					}
					artifactOverlay.hydrateSecurityRequirementURIs(resource, allowExternalRefs, hydrateSecurityResource)
				}
			}
			data = composition.prune(resource, data)
			// A reference the edition's own text makes unresolvable is reported
			// here, at the seam that already serves every resource, rather than
			// left for the typed loader to fail on some later symptom.
			if err := composition.refusal(); err != nil {
				return nil, err
			}
			return normalizer.normalizeResourceAt(data, resource, retrieval)
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
			if laneOverlay != nil {
				if captureErr := laneOverlay.capture(entry, resource, resource, true); captureErr != nil {
					return nil, captureErr
				}
				laneOverlay.hydrateSecurityRequirementURIs(resource, allowExternalRefs, hydrateSecurityResource)
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
		if Edition(document.OpenAPI) != edition {
			return nil, fmt.Errorf("pre-resolution OpenAPI edition %q changed to %q during typed loading", edition, document.OpenAPI)
		}
		if artifactOverlay == nil {
			artifactOverlay = laneOverlay
		}
		return document, nil
	}

	document, err := attempt(nil)
	var operationTargets map[string]*OperationTarget
	var operationErrors map[string]error
	fallbackAllTargetsExcluded := false
	if err == nil && edition.IsOpenAPI32() && artifactOverlay != nil {
		composed := buildOpenAPI32Targets(artifactOverlay, artifactOverlay.composedOperationReferences(), attempt)
		operationTargets = composed.targets
		operationErrors = composed.errors
	}
	if err != nil {
		if edition.IsOpenAPI32() && artifactOverlay != nil {
			fallback := buildOpenAPI32Fallback(artifactOverlay, attempt)
			if fallback.used {
				document = fallback.document
				if document == nil {
					document = &openapi3.T{OpenAPI: string(edition), Paths: openapi3.NewPaths()}
				}
				operationTargets = fallback.targets
				operationErrors = fallback.errors
				fallbackAllTargetsExcluded = len(fallback.targets) == 0 && len(fallback.errors) > 0
				err = nil
			} else if len(artifactOverlay.operationReferences()) == 0 {
				// A 3.2 description with no operation target is accepted even if
				// kin rejects unrelated reusable material. The raw overlay remains
				// the authority; the typed image is intentionally empty.
				document = &openapi3.T{OpenAPI: string(edition), Paths: openapi3.NewPaths()}
				err = nil
			}
		}
	}
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
	sourceRefusal, sourceExclusion := openAPI32ArtifactDisposition(document)
	if edition.IsOpenAPI32() && artifactOverlay != nil {
		sourceRefusal, sourceExclusion = artifactOverlay.artifactDisposition()
	}
	if fallbackAllTargetsExcluded {
		sourceRefusal = "every addressable OpenAPI 3.2 operation target is excluded"
	}
	artifact := &Artifact{
		Document:         document,
		Edition:          edition,
		entryBytes:       append([]byte(nil), entryBytes...),
		openAPI32:        artifactOverlay,
		operationTargets: operationTargets,
		operationErrors:  operationErrors,
		sourceRefusal:    sourceRefusal,
		sourceExclusion:  sourceExclusion,
	}
	// Do not fold target-local 3.2 exclusions into a whole-source refusal.
	// Once an operation position is addressable, its parameter, media, server,
	// security, and response defects remain confined to that selected target.
	// Only the raw inventory/fallback path above can establish that every
	// position which could contain a target is itself defective.
	return artifact, floor, nil
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
		version == "3.1.0" || version == "3.1.1" || version == "3.1.2" || version == "3.2.0"
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
