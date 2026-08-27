package openapiclient

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/getkin/kin-openapi/openapi3"
)

type bufferedResponseBody struct {
	*bufio.Reader
	io.Closer
}

// configOrSourceError maps a server-resolution error to the right terminal: a
// resolvable-missing configuration value (a configRequired signal) becomes a
// config.value CONTEXT_REQUIRED challenge — retryable after resolution (R1a) —
// while any other error stays a terminal ERR_SOURCE_CONFIG_ERROR for source
// misconfiguration no runtime can fix. resolveServer already consulted the
// supplied context and found the value absent, so the challenge fires
// unconditionally; the operation-invoker's bounded resolve-and-retry loop is
// the backstop.
//
// target is the engine-asserted scope for the missing value (the
// context-scope model, ratified 2026-08-19). For the server point no
// destination has resolved yet, so the natural scope is the artifact's own
// identity: the source location already threaded into resolveServer (the
// loader admits only an absolute URI there). A content-only source has no
// stable identity, asserts nothing, and the target stays empty — a resolver
// may still satisfy the challenge interactively or from caller-owned policy.
// For the requestMedia point the destination HAS resolved, so callers pass
// the resolved base URL. Configuration is not assumed public; consumers
// decide whether the asserted scope is sufficient for stored-value release.
func configOrSourceError(err error, target string) *ExecutionError {
	var cr *configRequired
	if errors.As(err, &cr) {
		req := newConfigValueRequirementCompat(cr.point, cr.path, cr.description, cr.schema, cr.durable)
		return newContextRequiredError(cr.description, &Prerequisites{
			Target:       target,
			Alternatives: []RequirementAlternative{{Requirements: []Requirement{req}}},
		})
	}
	return &ExecutionError{Code: CodeRefused, Message: err.Error()}
}

// classifyhttpFailureError maps a transport-level error from http.Client.Do to a
// standard SDK error code. Cancellation and deadlines from the caller's context
// are surfaced as cancelled/timeout, network errors as connect_failed, and
// anything else as the generic execution_failed.
func classifyhttpFailureError(ctx context.Context, err error) string {
	if err == nil {
		return CodeExecutionFailed
	}
	// Prefer the context's reason when the caller cancelled or set a deadline.
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.Canceled) {
			return CodeCancelled
		}
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return CodeTimeout
		}
	}
	if errors.Is(err, context.Canceled) {
		return CodeCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeTimeout
	}
	// net.Error covers DNS failures, refused connections, TLS handshake errors,
	// and other transport-layer problems. A timeout flagged at this level is
	// typically a per-dial deadline rather than a caller-set context deadline.
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return CodeTimeout
		}
		return CodeConnectFailed
	}
	return CodeExecutionFailed
}

// runBinding invokes one OpenAPI binding, driving the invocation handle: one
// HTTP exchange per invocation (openapi@1 §8). All pre-dispatch
// refusals — bad ref, unresolvable operation, unflattenable declarations,
// out-of-family request media, unresolvable server, missing context, missing
// path parameters, unmatched input fields, credential collisions — terminate
// the handle before any network side effect, and before consuming input where
// knowable.
func runBinding(ctx context.Context, client *http.Client, args *executionArgs, inv executionHandle[any, any], doc *openapi3.T) {
	// Bound all HTTP I/O to the invocation's lifetime: caller Cancel(), an
	// abandoned output stream, or upstream ctx cancellation tears down the
	// in-flight request or SSE stream.
	bctx, stop := doneContext(ctx, inv.Done())
	defer stop()

	// ----- Pre-side-effect resolution. -----

	pathTemplate, method, err := parseRef(args.Ref)
	if err != nil {
		inv.failExecution(&ExecutionError{Code: CodeInvalidRef, Message: err.Error()})
		return
	}

	if doc.Paths == nil {
		inv.failExecution(&ExecutionError{Code: CodeRefused, Message: "OpenAPI document has no paths defined"})
		return
	}
	// Pointer evaluation follows OAS reference resolution (OAPI-D-03): the
	// loader resolves path-item $refs (including 3.1 components.pathItems
	// targets) at load, before this lookup.
	pathItem := doc.Paths.Find(pathTemplate)
	if pathItem == nil {
		inv.failExecution(&ExecutionError{Code: CodeRefNotFound, Message: fmt.Sprintf("path %q not in OpenAPI doc", pathTemplate)})
		return
	}
	op := pathItem.GetOperation(strings.ToUpper(method))
	if op == nil {
		inv.failExecution(&ExecutionError{Code: CodeRefNotFound, Message: fmt.Sprintf("method %q not in path %q", method, pathTemplate)})
		return
	}

	routedRevision := usesRoutedInput(args.Source.Capability)
	// The input model's structural refusals (§9.1) are declaration-only and
	// precede input consumption. Revision 2 lifts cross-location name
	// collisions through its routed source value; it cannot lift two
	// case-distinct declarations that HTTP itself treats as one header name.
	params := effectiveParameters(pathItem, op)
	if name := unflattenableParamForRevision(params, args.Source.Capability); name != "" {
		inv.failExecution(&ExecutionError{
			Code:    CodeRefused,
			Message: fmt.Sprintf("operation declares parameter %q without a distinct wire identity under %s (OAPI-P-03, unflattenable/unresolvable)", name, args.Source.Capability),
		})
		return
	}
	if err := checkEffectiveParameterOwnership(params); err != nil {
		inv.failExecution(&ExecutionError{
			Code:    CodeRefused,
			Message: err.Error(),
		})
		return
	}
	// Last of the declaration-only checks, and ahead of server resolution: an
	// operation with no addressable target refuses terminally rather than
	// challenging the consumer to configure a server it can never reach.
	if err := checkPathTemplateAddressability(pathTemplate, params); err != nil {
		inv.failExecution(&ExecutionError{
			Code:    CodeRefused,
			Message: err.Error(),
		})
		return
	}
	baseURL, err := resolveServer(doc, pathItem, op, args.Context, args.Source.Location)
	if err != nil {
		inv.failExecution(configOrSourceError(err, args.Source.Location))
		return
	}

	// CONTEXT_REQUIRED is raised before any input is consumed and before any
	// network I/O, so a no-input-consumed retry (after the operation layer
	// resolves context) is safe.
	if err := securityConfigurationError(doc, op); err != nil {
		inv.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error()})
		return
	}
	if err := securityAlternativesCollision(doc, op, baseURL, params); err != nil {
		inv.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error()})
		return
	}
	details := requiredContext(doc, op, args.Context, baseURL, params)
	mediaDetails, mediaRequirementErr := requiredRequestMediaContext(doc, op, args.Source.Capability, args.Context)
	if mediaRequirementErr != nil {
		inv.failExecution(&ExecutionError{Code: CodeRefused, Message: mediaRequirementErr.Error()})
		return
	}
	details = mergeRequirements(details, mediaDetails)
	if details != nil {
		message := "OpenAPI operation requires authentication context"
		if mediaDetails != nil {
			message = "OpenAPI operation requires invocation context"
		}
		inv.failExecution(newContextRequiredError(
			message, details))
		return
	}

	// ----- Input flows through the handle, not the args. Whether this
	// interaction carries an input value is decided by the ARTIFACT and by
	// what the caller writes — never by the presence of the operation's
	// `input` member. Core §6.2: schema absence "means the document makes no
	// portable claim at that boundary", not that the interaction carries zero
	// values. A caller with nothing to say says it by closing. -----
	inputMap := map[string]any{}
	inputSupplied := false
	var envelope *routedEnvelope
	switch {
	case len(params) == 0 && !hasRequestBody(op):
		_ = inv.closeInputBoundary()
	default:
		v, rerr := inv.readInputBoundary(bctx)
		switch {
		case errors.Is(rerr, io.EOF):
			// Bare close: absent input and a supplied empty object are ONE
			// rule (§9.1 required declarations) — the two requests are
			// wire-identical, so they ride the same code below. The only
			// pre-dispatch refusals are a required request body with no
			// value to carry and an unsupplied path parameter; every other
			// missing declaration is sent as supplied, the server's
			// validation authoritative.
		case rerr != nil:
			inv.failExecution(normalizeExecutionError(rerr))
			return
		default:
			inputSupplied = true
			if routedRevision {
				envelope, err = parseRoutedEnvelopeWithKey(v, args.InputRouteKey, args.InputRouteMarker)
				if err != nil {
					inv.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error()})
					return
				}
			}
			if envelope == nil {
				if m, ok := toStringAnyMap(v); ok {
					inputMap = m
				} else {
					inv.failExecution(&ExecutionError{Code: CodeRefused, Message: "OpenAPI input value must be a JSON object"})
					return
				}
			}
		}
		_ = inv.closeInputBoundary()
	}

	// ----- Routing (§9.1) and body construction (§9.2): still pre-dispatch. -----

	if routedRevision && inputSupplied && envelope == nil && flatInputHasAmbiguousParameter(params, inputMap) {
		inv.failExecution(&ExecutionError{
			Code:    CodeRefused,
			Message: "this input supplies one flat field for independently declared same-named parameters and requires an explicit routed-input envelope",
		})
		return
	}

	var plans []*bodyPlan
	willEmitBody := requestWillEmitBody(params, inputMap, op)
	if envelope != nil {
		willEmitBody = envelopeWillEmitBody(envelope, op)
	}
	// §9.1 required declarations: a required request body with no value to
	// carry refuses before dispatch, applied to absent and
	// supplied-but-incomplete input alike — the artifact's own
	// requestBody.required is the ground. An explicitly present body (the
	// routed envelope's body.present, or any field the routes carry to the
	// body) is a value; anything else leaves nothing to send.
	if !willEmitBody && hasRequestBody(op) && op.RequestBody.Value.Required {
		inv.failExecution(&ExecutionError{
			Code:    CodeRefused,
			Message: "operation requires a request body: the input supplies no value to carry (§9.1 required declarations)",
		})
		return
	}
	if willEmitBody || envelope != nil {
		plans, err = planRequestBodiesFor(doc, op, args.Source.Capability)
		if err != nil {
			inv.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error()})
			return
		}
	}
	if envelope != nil {
		if err := validateEnvelopeRoutes(params, plans, envelope); err != nil {
			inv.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error()})
			return
		}
	}
	if !willEmitBody {
		plans = nil
	}

	var routed *routedInput
	var bodyReader io.Reader
	var contentType string
	if len(plans) == 0 {
		if envelope != nil {
			routed, err = routeEnvelopeFor(params, envelope, pathTemplate, &bodyPlan{}, args.Source.Capability)
		} else {
			routed, err = routeInputFor(params, inputMap, pathTemplate, &bodyPlan{}, args.Source.Capability)
		}
	} else {
		var reasons []string
		selectedPlans, selectErr := configuredRequestPlansFor(doc, op, plans, args.Context, args.Source.Capability)
		if selectErr != nil {
			var cr *configRequired
			if errors.As(selectErr, &cr) {
				inv.failExecution(configOrSourceError(selectErr, baseURL))
				return
			}
			err = selectErr
		}
		for _, candidate := range selectedPlans {
			if envelope == nil && candidateCollides(params, candidate) {
				reasons = append(reasons, fmt.Sprintf("request media candidate %s collides with an independently declared parameter", candidate.mediaType))
				continue
			}
			var candidateRouted *routedInput
			var routeErr error
			if envelope != nil {
				candidateRouted, routeErr = routeEnvelopeFor(params, envelope, pathTemplate, candidate, args.Source.Capability)
			} else {
				candidateRouted, routeErr = routeInputFor(params, inputMap, pathTemplate, candidate, args.Source.Capability)
			}
			if routeErr != nil {
				if errors.Is(routeErr, errMissingPathParam) {
					err = routeErr
					break
				}
				reasons = append(reasons, fmt.Sprintf("%s: %v", candidate.mediaType, routeErr))
				continue
			}
			candidateBody, candidateContentType, buildErr := buildRequestBody(doc, candidate, candidateRouted)
			if buildErr != nil {
				reasons = append(reasons, fmt.Sprintf("%s: %v", candidate.mediaType, buildErr))
				continue
			}
			routed, bodyReader, contentType = candidateRouted, candidateBody, candidateContentType
			break
		}
		if routed == nil && err == nil {
			if len(reasons) == 0 {
				reasons = append(reasons, "configured requestMedia selects no declared supported candidate")
			}
			err = fmt.Errorf("no request media candidate can carry this invocation: %s", strings.Join(reasons, "; "))
		}
	}
	if err != nil {
		// Every failure here — the unsupplied-path-parameter case included —
		// is a §9.1/§9.2 pre-dispatch refusal, and ERR_REFUSED is the
		// binding-invoker contract's never-dispatched guarantee (ruled
		// 2026-08-14). ERR_MISSING_INPUT stays a stream-cardinality code and
		// cannot cover the supplied-but-incomplete half of the one rule.
		inv.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error()})
		return
	}

	// ----- Channel assembly (§9.6, OAPI-P-10). -----

	placements, selectedSchemes, credentialOwnership, err := selectCredentialPlacements(doc, op, args.Context, baseURL, params, routed.populated)
	if err != nil {
		inv.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error()})
		return
	}
	if err := contextChannelCollision(args.Context, params, credentialOwnership); err != nil {
		inv.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error()})
		return
	}

	queryUnits := routed.queryUnits
	cookieUnits := routed.cookieUnits
	for _, pl := range placements {
		switch pl.channel {
		case "query":
			queryUnits = append(queryUnits, queryEscape(pl.name, false)+"="+queryEscape(pl.value, false))
		case "cookie":
			cookieUnits = append(cookieUnits, pl.name+"="+pl.value)
		}
	}
	// Context-supplied transport-hint cookies (consumer context, not
	// security-scheme credentials) ride after credentials, sorted for
	// determinism.
	if hintCookies := contextCookies(args.Context); len(hintCookies) > 0 {
		names := make([]string, 0, len(hintCookies))
		for k := range hintCookies {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			cookieUnits = append(cookieUnits, k+"="+hintCookies[k])
		}
	}

	reqURL := baseURL + routed.resolvedPath
	if len(queryUnits) > 0 {
		reqURL += "?" + strings.Join(queryUnits, "&")
	}

	req, err := http.NewRequestWithContext(bctx, strings.ToUpper(method), reqURL, bodyReader)
	if err != nil {
		inv.failExecution(&ExecutionError{Code: CodeExecutionFailed, Message: err.Error()})
		return
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// The Accept header advertises only artifact-declared concrete success
	// media. An empty membership set omits the header.
	if accept := acceptHeaderFor(op, args.Source.Capability); accept != "" {
		req.Header.Set("Accept", accept)
	}

	for _, h := range routed.headers {
		req.Header.Set(h[0], h[1])
	}
	for _, pl := range placements {
		if pl.channel == "header" {
			req.Header.Set(pl.name, pl.value)
		}
	}
	// Context-supplied transport-hint headers (consumer context) apply last.
	for k, v := range contextHeaders(args.Context) {
		req.Header.Set(k, v)
	}
	// One Cookie header (OAPI-P-10): declared cookie parameters in
	// declaration order, credentials appended after.
	if len(cookieUnits) > 0 {
		req.Header.Set("Cookie", strings.Join(cookieUnits, "; "))
	}
	// Extension handlers observe the final built-in request and apply last, so
	// schemes such as Digest or signature auth can cover every finalized field.
	for _, selected := range selectedSchemes {
		handler := args.SecurityHandlers[selected.name]
		if handler == nil {
			continue
		}
		if err := handler(req, SecurityHandlerContext{SchemeName: selected.name, Scheme: selected.scheme}); err != nil {
			inv.failExecution(&ExecutionError{Code: CodeRefused, Message: fmt.Sprintf("OpenAPI security handler %q failed: %v", selected.name, err), Cause: err})
			return
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		if bctx.Err() != nil {
			return // cancelled; the handle is already terminal
		}
		inv.failExecution(&ExecutionError{Code: classifyhttpFailureError(bctx, err), Message: err.Error()})
		return
	}
	inv.setHTTPResponse(resp)

	// Leading metadata precedes the first emit.
	_ = inv.setLeadingMetadata(headerMetadata(resp.Header))

	site := siteFor(args, baseURL)
	responseMatch := governingResponse(op, resp.StatusCode)
	var responseDecl *openapi3.Response
	if responseMatch != nil {
		responseDecl = responseMatch.response
	}
	actualContentType := resp.Header.Get("Content-Type")
	revision3ClassifiedSuccess := false
	var revision3ContentTypeErr error
	if hasMediaFidelity(args.Source.Capability) {
		buffered := bufio.NewReader(resp.Body)
		_, peekErr := buffered.Peek(1)
		if errors.Is(peekErr, io.EOF) {
			_ = resp.Body.Close()
			status := resp.StatusCode
			raw := RawResult{Status: &status, Meta: headerMetadata(resp.Header)}
			ok, classifyErr := args.Hooks.Classify(site, raw, builtinClassify)
			if classifyErr != nil {
				inv.failExecution(normalizeExecutionError(classifyErr))
				return
			}
			if !ok {
				inv.failExecution(openAPIFailureError(resp, []byte{}, responseMatch, args.Source.Capability, doc))
				return
			}
			inv.setTrailingMetadata(decodeClassifyTrailer(args.Hooks, "not-consulted/empty"))
			inv.closeOutputBoundary()
			return
		}
		if peekErr != nil {
			_ = resp.Body.Close()
			if bctx.Err() != nil {
				return
			}
			inv.failExecution(&ExecutionError{Code: CodeResponseError, Message: peekErr.Error()})
			return
		}
		resp.Body = bufferedResponseBody{Reader: buffered, Closer: resp.Body}
		actualContentType, revision3ContentTypeErr = singletonResponseHeader(resp.Header, "Content-Type")
		if revision3ContentTypeErr == nil && isSSEContentTypeFor(actualContentType, args.Source.Capability) {
			// A stream cannot be buffered without destroying its lifecycle. Its
			// classifier sees the real status/headers and no invented body, as in
			// prior revisions; unary lanes retain the complete-body seam below.
			status := resp.StatusCode
			ok, classifyErr := args.Hooks.Classify(site, RawResult{Status: &status, Meta: headerMetadata(resp.Header)}, builtinClassify)
			if classifyErr != nil {
				_ = resp.Body.Close()
				inv.failExecution(normalizeExecutionError(classifyErr))
				return
			}
			if !ok {
				maxUnit := args.DeliveryUnitLimit()
				failureBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxUnit+1))
				_ = resp.Body.Close()
				if readErr != nil {
					inv.failExecution(&ExecutionError{Code: CodeResponseError, Message: readErr.Error()})
					return
				}
				if int64(len(failureBody)) > maxUnit {
					inv.failExecution(&ExecutionError{Code: CodeResponseError, Message: fmt.Sprintf("response exceeds %d byte limit", maxUnit)})
					return
				}
				inv.failExecution(openAPIFailureError(resp, failureBody, responseMatch, args.Source.Capability, doc))
				return
			}
			revision3ClassifiedSuccess = true
		}
	}

	// Interaction-shape dispatch (§8, OAPI-P-06): the shape is bounded by
	// declaration and selected by framing. An operation is streaming-capable
	// iff a declared success response declares text/event-stream; for a
	// streaming-capable operation the response's Content-Type header — never
	// payload bytes — selects between server-streaming and unary. A
	// text/event-stream response on an operation that is NOT
	// streaming-capable contradicts the declaration: a protocol error, never
	// a silent reclassification.
	if isSSEContentTypeFor(actualContentType, args.Source.Capability) {
		// Classification is independent of declaration lookup. A non-success
		// final status remains the native HTTP failure even when its body uses
		// event-stream framing.
		ok := revision3ClassifiedSuccess
		if !hasMediaFidelity(args.Source.Capability) {
			status := resp.StatusCode
			var cerr error
			ok, cerr = args.Hooks.Classify(site, RawResult{Status: &status, Meta: headerMetadata(resp.Header)}, builtinClassify)
			if cerr != nil {
				_ = resp.Body.Close()
				inv.failExecution(normalizeExecutionError(cerr))
				return
			}
		}
		if !ok {
			// A non-2xx event-stream response is one unsuccessful HTTP
			// exchange, not a stream of successful operation values. Preserve
			// its exact response bytes under the same consumer-owned delivery
			// bound as the unary failure lane before emitting the terminal
			// failure. Detecting SSE framing must never discard native evidence.
			maxUnit := args.DeliveryUnitLimit()
			failureBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxUnit+1))
			_ = resp.Body.Close()
			if readErr != nil {
				if bctx.Err() != nil {
					return
				}
				inv.failExecution(&ExecutionError{Code: CodeResponseError, Message: readErr.Error()})
				return
			}
			if int64(len(failureBody)) > maxUnit {
				inv.failExecution(&ExecutionError{
					Code:    CodeResponseError,
					Message: fmt.Sprintf("response exceeds %d byte limit", maxUnit),
				})
				return
			}
			inv.failExecution(openAPIFailureError(resp, failureBody, responseMatch, args.Source.Capability, doc))
			return
		}
		if !isStreamingCapableFor(op, args.Source.Capability) {
			_ = resp.Body.Close()
			inv.failExecution(&ExecutionError{
				Code:    CodeProtocol,
				Message: "response arrived as text/event-stream, but the operation declares no concrete successful event-stream media",
			})
			return
		}
		matched, mediaErr := governingResponseMediaFor(responseDecl, actualContentType, args.Source.Capability)
		if mediaErr != nil || (!hasResponseFidelity(args.Source.Capability) && matched.base != "text/event-stream") {
			_ = resp.Body.Close()
			message := "response arrived as text/event-stream, but the governing response does not declare that media type"
			if mediaErr != nil {
				message += ": " + mediaErr.Error()
			}
			inv.failExecution(&ExecutionError{
				Code:    CodeProtocol,
				Message: message,
			})
			return
		}
		inv.setTrailingMetadata(Metadata{"x-ob-governing-media": {matched.canonical}})
		streamSSE(bctx, resp, args, site, inv)
		return
	}

	defer func() { _ = resp.Body.Close() }()

	// The unary body is one delivery unit: consumer-bounded via
	// executionArgs.MaxDeliveryUnitBytes (default 10 MiB). The +1
	// sentinel distinguishes an at-limit response from an over-limit one.
	maxUnit := args.DeliveryUnitLimit()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxUnit+1))
	if err != nil {
		if bctx.Err() != nil {
			return
		}
		inv.failExecution(&ExecutionError{Code: CodeResponseError, Message: err.Error()})
		return
	}
	if int64(len(respBody)) > maxUnit {
		inv.failExecution(&ExecutionError{
			Code:    CodeResponseError,
			Message: fmt.Sprintf("response exceeds %d byte limit", maxUnit),
		})
		return
	}
	inv.setHTTPResponseBody(respBody)

	// Classify, then decode — both through the consultation seam
	// (per-invocation hook → invoker-level hook → the format builtins
	// below). The binding specification's defaults (OAPI-P-07/P-08),
	// content-independent throughout: classify = success iff status ∈ 2xx
	// (declared `responses` never change classification — they can identify
	// application-authored failure data); decode = the response's Content-Type HEADER decides
	// the lane (wire framing, not payload sniffing): JSON for
	// application/json and +json suffixes, text otherwise, absent /
	// unparseable header → text.
	status := resp.StatusCode
	raw := RawResult{
		Status: &status,
		Body:   respBody,
		Meta:   headerMetadata(resp.Header),
	}

	ok := revision3ClassifiedSuccess
	if !revision3ClassifiedSuccess {
		var cerr error
		ok, cerr = args.Hooks.Classify(site, raw, builtinClassify)
		if cerr != nil {
			inv.failExecution(normalizeExecutionError(cerr))
			return
		}
	}
	if !ok {
		// The format's NATIVE failure: hooks change the verdict, never the
		// error vocabulary. It is not an operation output, but the complete
		// native response and the OpenAPI declaration match remain available
		// on the failure completion. The legacy status/body members remain for
		// callers using HTTPStatus/HTTPResponseBody.
		inv.failExecution(openAPIFailureError(resp, respBody, responseMatch, args.Source.Capability, doc))
		return
	}
	if hasMediaFidelity(args.Source.Capability) && revision3ContentTypeErr != nil {
		inv.failExecution(&ExecutionError{Code: CodeProtocol, Message: revision3ContentTypeErr.Error()})
		return
	}
	// An empty successful response carries absence, not an invented JSON
	// null. It completes without consulting response media or decode.
	if len(respBody) == 0 {
		inv.setTrailingMetadata(decodeClassifyTrailer(args.Hooks, "not-consulted/empty"))
		inv.closeOutputBoundary()
		return
	}

	matched, mediaErr := governingResponseMediaMatchFor(responseDecl, actualContentType, args.Source.Capability)
	if mediaErr != nil {
		inv.failExecution(&ExecutionError{Code: CodeProtocol, Message: mediaErr.Error()})
		return
	}

	decoder := decodeByContentTypeFor(actualContentType, args.Source.Capability)
	if hasResponseFidelity(args.Source.Capability) && responseUsesRawBoundary(doc, matched.media, actualContentType, args.Source.Capability, matched.declared.rangeSpecificity == 2) {
		decoder = func(_ HookSite, raw RawResult) (any, error) {
			return base64.StdEncoding.EncodeToString(raw.Body), nil
		}
	}
	output, derr := args.Hooks.DecodeOutput(site, raw, decoder)
	if derr != nil {
		inv.failExecution(normalizeExecutionError(derr))
		return
	}

	// Success provenance stamps (conventions record): decode provenance is
	// header/content-type when the builtin (the Content-Type lane) decided,
	// hook when overridden; classify is always assumption/2xx unless a hook
	// widened it.
	trailer := decodeClassifyTrailer(args.Hooks, "header/content-type")
	trailer["x-ob-governing-media"] = []string{matched.declared.canonical}
	inv.setTrailingMetadata(trailer)
	if err := inv.emitOutput(output); err != nil {
		return
	}
	inv.closeOutputBoundary()
}

func requiredRequestMediaContext(doc *openapi3.T, op *openapi3.Operation, bindingSpec string, bindCtx map[string]any) (*Prerequisites, error) {
	if !hasMediaFidelity(bindingSpec) || op == nil || op.RequestBody == nil || op.RequestBody.Value == nil || !op.RequestBody.Value.Required {
		return nil, nil
	}
	plans, err := planRequestBodiesFor(doc, op, bindingSpec)
	if err != nil {
		return nil, err
	}
	if !requestMediaUnconfigured(bindCtx) {
		_, err := configuredRequestPlansFor(doc, op, plans, bindCtx, bindingSpec)
		return nil, err
	}
	if !onlyRangePlans(plans) {
		return nil, nil
	}
	requirement := newConfigValueRequirementCompat(
		"requestMedia", "",
		"select a concrete request media type admitted by the OpenAPI declaration",
		nil, nil,
	)
	return &Prerequisites{
		Alternatives: []RequirementAlternative{{Requirements: []Requirement{requirement}}},
	}, nil
}

// mergeRequirements combines two independent context needs. Each
// details value is an OR of alternatives; satisfying the operation requires
// one alternative from each, so the merged value is their Cartesian product.
func mergeRequirements(left, right *Prerequisites) *Prerequisites {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	target := left.Target
	if target == "" {
		target = right.Target
	}
	merged := &Prerequisites{Target: target}
	for _, a := range left.Alternatives {
		for _, b := range right.Alternatives {
			requirements := append([]Requirement(nil), a.Requirements...)
			requirements = append(requirements, b.Requirements...)
			merged.Alternatives = append(merged.Alternatives, RequirementAlternative{Requirements: requirements})
		}
	}
	return merged
}

// decodeClassifyTrailer builds the x-ob-decode/x-ob-classify success
// provenance stamps (the conventions record, spec/binding-specs/README.md) for
// an HTTP-lane invoker, given the axis's builtin provenance token. A hook
// decision on either axis stamps "hook".
func decodeClassifyTrailer(hooks *invokeHooks, builtinDecode string) Metadata {
	decode, classify := builtinDecode, "assumption/2xx"
	if hooks.DecodeDecidedBy() == "hook" {
		decode = "hook"
	}
	if hooks.ClassifyDecidedBy() == "hook" {
		classify = "hook"
	}
	return Metadata{
		"x-ob-decode":   {decode},
		"x-ob-classify": {classify},
	}
}

// builtinClassify is the openapi builtin result classifier (OAPI-P-08):
// success iff the final HTTP status is 2xx (declared responses refine
// application failure data only, never classification).
func builtinClassify(_ HookSite, raw RawResult) (bool, error) {
	return raw.Status != nil && *raw.Status >= 200 && *raw.Status < 300, nil
}

// decodeByContentType returns the builtin decoder implementing the header
// rule (OAPI-P-07): strict JSON for application/json and +json suffixes (a
// declared-JSON body that fails to parse is a lying server — a loud
// CodeResponseError, never a silent string); the text lane otherwise —
// bytes become a string per the header's charset parameter, defaulting to
// UTF-8, with invalid sequences a loud decode error. An empty body (204
// included) yields null.
func decodeByContentType(contentType string) outputDecoder {
	return decodeByContentTypeFor(contentType, profileRoutedCoordinate)
}

func decodeByContentTypeFor(contentType, bindingSpec string) outputDecoder {
	isJSON := isJSONContentTypeFor(contentType, bindingSpec)
	return func(_ HookSite, raw RawResult) (any, error) {
		if len(raw.Body) == 0 {
			return nil, nil
		}
		if isJSON {
			var parsed any
			if err := json.Unmarshal(raw.Body, &parsed); err != nil {
				return nil, &ExecutionError{
					Code:    CodeResponseError,
					Message: fmt.Sprintf("response declares %q but the body is not valid JSON: %v", contentType, err),
				}
			}
			return parsed, nil
		}
		return decodeTextLaneFor(contentType, raw.Body, bindingSpec)
	}
}

// decodePerEventTextFor is the SSE per-event builtin lane (OAPI-P-07's
// per-event text default): the event's U+000A-joined data text, decoded
// under the fixed UTF-8 charset. Unlike decodeByContentTypeFor it carries
// no empty-body rule — a DISPATCHED event whose data text is empty (a lone
// empty `data:` line, §8/WHATWG) is the empty-string value; the
// empty-body→no-value rule is a whole-response rule, never per-event.
func decodePerEventTextFor(contentType, bindingSpec string) outputDecoder {
	return func(_ HookSite, raw RawResult) (any, error) {
		return decodeTextLaneFor(contentType, raw.Body, bindingSpec)
	}
}

// decodeTextLane decodes response bytes as text per the Content-Type
// header's charset parameter, defaulting to UTF-8 (OAPI-P-07). Invalid
// sequences, and charsets this implementation cannot decode, are loud
// decode errors — a consumer needing another charset overrides at the
// decode configuration point.
func decodeTextLane(contentType string, body []byte) (any, error) {
	return decodeTextLaneFor(contentType, body, profileRoutedCoordinate)
}

func decodeTextLaneFor(contentType string, body []byte, bindingSpec string) (any, error) {
	charset := "utf-8"
	if contentType != "" {
		if hasMediaFidelity(bindingSpec) {
			if parsed, err := parseRevision3MediaType(contentType); err == nil {
				if cs, present := parsed.params["charset"]; present {
					charset = cs
				}
			}
		} else if _, params, err := mime.ParseMediaType(contentType); err == nil {
			if cs := params["charset"]; cs != "" {
				charset = cs
			}
		}
	}
	switch strings.ToLower(charset) {
	case "utf-8", "utf8":
		if !utf8.Valid(body) {
			return nil, &ExecutionError{
				Code:    CodeResponseError,
				Message: "response body is not valid UTF-8 (the declared/default charset)",
			}
		}
		return string(body), nil
	case "us-ascii", "ascii":
		for i := 0; i < len(body); i++ {
			if body[i] >= 0x80 {
				return nil, &ExecutionError{
					Code:    CodeResponseError,
					Message: fmt.Sprintf("response body byte %d is not valid US-ASCII (the declared charset)", i),
				}
			}
		}
		return string(body), nil
	case "iso-8859-1", "iso8859-1", "latin-1", "latin1":
		runes := make([]rune, len(body))
		for i, b := range body {
			runes[i] = rune(b)
		}
		return string(runes), nil
	default:
		return nil, &ExecutionError{
			Code:    CodeResponseError,
			Message: fmt.Sprintf("response declares charset %q, which this implementation cannot decode; override at the decode configuration point", charset),
		}
	}
}

// isJSONContentType reports whether a Content-Type header declares a JSON
// body: application/json or any +json structured-suffix type. Absent or
// unparseable headers are NOT JSON (the text lane) — never sniffed.
func isJSONContentType(contentType string) bool {
	return isJSONContentTypeFor(contentType, profileRoutedCoordinate)
}

func isJSONContentTypeFor(contentType, bindingSpec string) bool {
	if hasMediaFidelity(bindingSpec) {
		parsed, err := parseRevision3MediaType(contentType)
		return err == nil && isJSONMediaType(parsed.base)
	}
	return isJSONMediaType(normalizeMediaType(contentType))
}

// siteFor completes the core-stamped site with the format-known Target
// (the resolved base URL) before consultation; a nil args.Site (direct
// format-package call) gets a minimal unstamped-equivalent site whose
// Builtin* dispatch stays loud.
func siteFor(args *executionArgs, baseURL string) HookSite {
	var site HookSite
	if args.Site != nil {
		site = *args.Site
	} else {
		site.Profile = args.Profile
		site.Ref = args.Ref
	}
	if site.Target == "" {
		site.Target = baseURL
	}
	return site
}

// headerMetadata clones HTTP response headers into invocation Metadata.
func headerMetadata(h http.Header) Metadata {
	md := make(Metadata, len(h))
	for k, vs := range h {
		md[k] = append([]string(nil), vs...)
	}
	return md
}

// openAPIFailureError retains expert diagnostic evidence for an unsuccessful
// OpenAPI HTTP exchange. The operation output stays empty and ordinary callers
// need none of these HTTP-shaped facts. httpResponse.body is base64 so the
// standalone runtime can preserve exact bytes for protocol-aware consumers.
func openAPIFailureError(resp *http.Response, body []byte, match *governingResponseMatch, bindingSpec string, doc *openapi3.T) *ExecutionError {
	ierr := httpFailureError(resp.StatusCode, resp.Status)
	contentType := resp.Header.Get("Content-Type")
	// A declared failure body decodes through the same response lanes a
	// successful body would (§9.5 names §9.2's lanes): JSON-family as JSON,
	// raw-byte declarations as the canonical Base64 boundary string, and
	// every other represented selection through the UTF-8 text lane. An
	// undeclared, empty, or SSE-framed failure admits nothing.
	if len(body) > 0 && match != nil && !isSSEContentType(contentType) {
		if matched, err := governingResponseMediaMatchFor(match.response, contentType, bindingSpec); err == nil {
			decoder := decodeByContentTypeFor(contentType, bindingSpec)
			if hasResponseFidelity(bindingSpec) && responseUsesRawBoundary(doc, matched.media, contentType, bindingSpec, matched.declared.rangeSpecificity == 2) {
				decoder = func(_ HookSite, raw RawResult) (any, error) {
					return base64.StdEncoding.EncodeToString(raw.Body), nil
				}
			}
			status := resp.StatusCode
			if value, err := decoder(HookSite{}, RawResult{Status: &status, Body: body, Meta: headerMetadata(resp.Header)}); err == nil {
				ierr.Details = value
				ierr.DetailsPresent = true
			}
		}
	}
	details, _ := ierr.Diagnostics.(map[string]any)
	if details == nil {
		details = map[string]any{"status": resp.StatusCode}
		ierr.Diagnostics = details
	}
	if len(body) > 0 {
		details["body"] = string(body)
	}

	headers := map[string]any{}
	for name, values := range resp.Header {
		headers[strings.ToLower(name)] = append([]string(nil), values...)
	}
	httpResponse := map[string]any{
		"status":  resp.StatusCode,
		"headers": headers,
	}
	if body != nil {
		httpResponse["body"] = map[string]any{
			"base64":     base64.StdEncoding.EncodeToString(body),
			"byteLength": len(body),
		}
	}
	if reason := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(resp.Status), fmt.Sprintf("%d", resp.StatusCode))); reason != "" {
		httpResponse["statusText"] = reason
	}
	if resp.Request != nil && resp.Request.URL != nil {
		httpResponse["url"] = resp.Request.URL.String()
	}
	details["httpResponse"] = httpResponse

	artifact := map[string]any{"declared": match != nil}
	if match != nil {
		artifact["responseKey"] = match.key
		if hasMediaFidelity(bindingSpec) {
			contentType, _ = singletonResponseHeader(resp.Header, "Content-Type")
		}
		if media, err := governingResponseMediaFor(match.response, contentType, bindingSpec); err == nil {
			artifact["governingMedia"] = media.canonical
		}
	}
	details["openapi"] = artifact
	return ierr
}

func singletonResponseHeader(header http.Header, name string) (string, error) {
	values := header.Values(name)
	if len(values) > 1 {
		return "", fmt.Errorf("response contains %d %s field instances; the selected execution profile requires a singleton", len(values), name)
	}
	if len(values) == 0 {
		return "", nil
	}
	return values[0], nil
}

// requiredContext derives the operation's authentication requirements from
// the document's securitySchemes and the operation-level (falling back to
// document-level) security requirements. It returns a Prerequisites
// when the call needs context the caller has not supplied, or nil when the
// operation needs no authentication, allows anonymous access, or the present
// context already satisfies a requirement.
//
// OpenAPI `security` is a disjunction (OR) of requirement objects, each a
// conjunction (AND) of scheme names — exactly the alternatives/requirements
// shape of Prerequisites.
func requiredContext(doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any, baseURL string, params openapi3.Parameters) *Prerequisites {
	plans := viableSecurityPlans(doc, op, baseURL, params)
	if len(plans) == 0 {
		return nil
	}
	alternatives := make([]RequirementAlternative, 0, len(plans))
	for _, plan := range plans {
		if len(plan.context.Requirements) == 0 {
			// An empty requirement object means anonymous access is allowed;
			// the context contract intentionally has no empty alternatives.
			return nil
		}
		alternatives = append(alternatives, plan.context)
	}

	details := &Prerequisites{
		Target:       baseURL,
		Alternatives: alternatives,
	}
	// Already satisfiable from the supplied context: no challenge needed.
	if contextSatisfies(bindCtx, details) {
		return nil
	}
	return details
}

// effectiveSecurityRequirements applies OpenAPI's operation-over-document
// security inheritance rule. A non-nil empty operation list explicitly
// disables document-level security.
func effectiveSecurityRequirements(doc *openapi3.T, op *openapi3.Operation) *openapi3.SecurityRequirements {
	if op != nil && op.Security != nil {
		return op.Security
	}
	return &doc.Security
}

type namedSecurityScheme struct {
	name   string
	scheme *openapi3.SecurityScheme
}

type securityPlan struct {
	context RequirementAlternative
	schemes []namedSecurityScheme
}

// securityConfigurationError refuses every undefined SecurityScheme name.
// An unresolved name is invalid OpenAPI source configuration, not an
// anonymous or skippable OR alternative: dropping it would silently weaken
// the API author's security declaration and diverge from the TS processor.
func securityConfigurationError(doc *openapi3.T, op *openapi3.Operation) error {
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return nil
	}
	missing := map[string]bool{}
	for _, requirement := range *requirements {
		for name := range requirement {
			if doc.Components == nil || doc.Components.SecuritySchemes == nil {
				missing[name] = true
				continue
			}
			ref, found := doc.Components.SecuritySchemes[name]
			if !found || ref == nil || ref.Value == nil {
				missing[name] = true
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	suffix := ""
	if len(names) > 1 {
		suffix = "s"
	}
	return fmt.Errorf("OpenAPI security requirement references undefined security scheme%s: %s", suffix, strings.Join(names, ", "))
}

// securityPlans expands the artifact's OR-of-AND Security Requirement
// Objects without flattening them. An OAuth scheme may contribute multiple
// usable declared flows, so one authored AND-set expands to the Cartesian
// product of its schemes' runtime alternatives. Every expanded plan still
// represents exactly one complete artifact-declared requirement object.
func securityPlans(doc *openapi3.T, op *openapi3.Operation, baseURL string) []securityPlan {
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return nil
	}
	var plans []securityPlan
	for _, secReq := range *requirements {
		if len(secReq) == 0 {
			plans = append(plans, securityPlan{})
			continue
		}
		expanded := []securityPlan{{}}
		expressible := true
		names := make([]string, 0, len(secReq))
		for schemeName := range secReq {
			names = append(names, schemeName)
		}
		sort.Strings(names)
		for _, schemeName := range names {
			if doc.Components == nil || doc.Components.SecuritySchemes == nil {
				expressible = false
				break
			}
			ref, ok := doc.Components.SecuritySchemes[schemeName]
			if !ok || ref.Value == nil {
				expressible = false
				break
			}
			requiredScopes := append([]string(nil), secReq[schemeName]...)
			options := schemeRequirements(ref.Value, baseURL, requiredScopes)
			if len(options) == 0 {
				expressible = false
				break
			}
			next := make([]securityPlan, 0, len(expanded)*len(options))
			for _, plan := range expanded {
				for _, option := range options {
					durable := true
					option.Name = schemeName
					option.Durable = &durable
					if ref.Value.Description != "" {
						option.Description = ref.Value.Description
					}
					reqs := append([]Requirement(nil), plan.context.Requirements...)
					reqs = append(reqs, option)
					schemes := append([]namedSecurityScheme(nil), plan.schemes...)
					schemes = append(schemes, namedSecurityScheme{name: schemeName, scheme: ref.Value})
					next = append(next, securityPlan{
						context: RequirementAlternative{Requirements: reqs},
						schemes: schemes,
					})
				}
			}
			expanded = next
		}
		if expressible {
			plans = append(plans, expanded...)
		}
	}
	return plans
}

func viableSecurityPlans(doc *openapi3.T, op *openapi3.Operation, baseURL string, params openapi3.Parameters) []securityPlan {
	plans := securityPlans(doc, op, baseURL)
	if len(plans) == 0 {
		return nil
	}
	viable := make([]securityPlan, 0, len(plans))
	for _, plan := range plans {
		if err := checkCredentialCollisions(credentialDestinations(plan), params, nil); err == nil {
			viable = append(viable, plan)
		}
	}
	return viable
}

// securityAlternativesCollision reports an ownership conflict only when every
// complete runtime alternative is unusable. A later channel-safe alternative
// remains selectable and is the only one surfaced during context negotiation.
func securityAlternativesCollision(doc *openapi3.T, op *openapi3.Operation, baseURL string, params openapi3.Parameters) error {
	plans := securityPlans(doc, op, baseURL)
	if len(plans) == 0 {
		return nil
	}
	var first error
	for _, plan := range plans {
		err := checkCredentialCollisions(credentialDestinations(plan), params, nil)
		if err == nil {
			return nil
		}
		if first == nil {
			first = err
		}
	}
	return first
}

// schemeToRequirement maps an OpenAPI security scheme to a context
// requirement, carrying the family-specific fields a resolver needs to act
// without out-of-band knowledge (notably oauth2 flow endpoints). Per the
// R2.c ruling, a scheme family the SDK cannot itself resolve is no longer
// dropped: it is surfaced with a type derived from the artifact ("auth.http."
// + the HTTP scheme for an unmapped "http" scheme, e.g. "auth.http.digest";
// "auth." + the artifact's own type otherwise, e.g. "auth.mutualTLS") so the
// alternative stays discoverable to a runtime with a resolver for it, rather
// than silently vanishing into an unauthenticated dispatch. Every switch arm
// now returns true; the bool is kept for signature stability (and as a hook
// for a genuinely inexpressible future case) rather than removed.
func schemeToRequirement(s *openapi3.SecurityScheme, baseURL string) (Requirement, bool) {
	options := schemeRequirements(s, baseURL, nil)
	if len(options) == 0 {
		return Requirement{}, false
	}
	return options[0], true
}

// schemeRequirements maps one security scheme to its runtime requirement
// choices. Only OAuth2 expands: every declared flow capable of granting the
// Security Requirement Object's authoritative scope array becomes a distinct
// alternative. Other schemes contribute exactly one requirement.
func schemeRequirements(s *openapi3.SecurityScheme, baseURL string, requiredScopes []string) []Requirement {
	switch s.Type {
	case "http":
		switch strings.ToLower(s.Scheme) {
		case "basic":
			return []Requirement{{Type: "auth.basic"}}
		case "bearer":
			return []Requirement{{Type: "auth.bearer"}}
		default:
			// digest, negotiate, etc.: not a family this SDK resolves
			// itself, but SURFACED (R2.c ruling), not dropped. A missing
			// scheme value degrades to the bare family, never a trailing
			// dot (TS parity).
			return []Requirement{{
				Type:        httpRequirementType(s.Scheme),
				Description: s.Description,
			}}
		}
	case "apiKey":
		return []Requirement{{Type: "auth.apiKey"}}
	case "oauth2":
		return oauth2Requirements(s, baseURL, requiredScopes)
	case "openIdConnect":
		// OpenID Connect resolves to an OAuth2 access token; the discovery URL
		// lets a resolver fetch the authorize/token endpoints. No flow is
		// selected here (openIdConnect has no `flows` object), so this
		// requirement carries no grantType (R2.b ruling).
		req := Requirement{Type: "auth.oauth2", Extra: map[string]any{
			"scopes": append([]string{}, requiredScopes...),
		}}
		if s.OpenIdConnectUrl != "" {
			req.Extra["openIdConnectUrl"] = absolutizeURL(s.OpenIdConnectUrl, baseURL)
		}
		return []Requirement{req}
	default:
		// Any other artifact type this SDK doesn't itself resolve (e.g.
		// "mutualTLS"): surfaced verbatim (R2.c ruling) rather than dropped.
		return []Requirement{{
			Type:        "auth." + s.Type,
			Description: s.Description,
		}}
	}
}

// httpRequirementType derives the surfaced type for an http scheme this SDK
// does not itself resolve: "auth.http.<scheme>" (lowercased), degrading to
// the bare "auth.http" when the artifact omits the scheme value (TS parity —
// never a trailing dot).
func httpRequirementType(scheme string) string {
	if scheme == "" {
		return "auth.http"
	}
	return "auth.http." + strings.ToLower(scheme)
}

// oauth2Requirements builds one auth.oauth2 requirement per usable declared
// flow. The scopes field carries the Security Requirement Object's required
// scopes, never the scheme's complete advertised catalogue. Canonical flow
// order is deterministic only; it does not invent a preference. When no
// declared flow can grant the required scopes, a bare scoped requirement
// remains discoverable so an already-acquired token may still satisfy it.
func oauth2Requirements(s *openapi3.SecurityScheme, baseURL string, requiredScopes []string) []Requirement {
	type candidate struct {
		grantType string
		flow      *openapi3.OAuthFlow
	}
	var flows *openapi3.OAuthFlows
	if s != nil {
		flows = s.Flows
	}
	var candidates []candidate
	if flows != nil {
		candidates = []candidate{
			{grantType: "authorization_code", flow: flows.AuthorizationCode},
			{grantType: "implicit", flow: flows.Implicit},
			{grantType: "password", flow: flows.Password},
			{grantType: "client_credentials", flow: flows.ClientCredentials},
		}
	}
	var requirements []Requirement
	for _, candidate := range candidates {
		if candidate.flow == nil || !oauthFlowUsable(candidate.grantType, candidate.flow, requiredScopes) {
			continue
		}
		extra := map[string]any{
			"grantType": candidate.grantType,
			"scopes":    append([]string{}, requiredScopes...),
		}
		if candidate.flow.AuthorizationURL != "" {
			extra["authorizeUrl"] = absolutizeURL(candidate.flow.AuthorizationURL, baseURL)
		}
		if candidate.flow.TokenURL != "" {
			extra["tokenUrl"] = absolutizeURL(candidate.flow.TokenURL, baseURL)
		}
		requirements = append(requirements, Requirement{Type: "auth.oauth2", Extra: extra})
	}
	if len(requirements) == 0 {
		return []Requirement{{
			Type:  "auth.oauth2",
			Extra: map[string]any{"scopes": append([]string{}, requiredScopes...)},
		}}
	}
	return requirements
}

func oauthFlowUsable(grantType string, flow *openapi3.OAuthFlow, requiredScopes []string) bool {
	switch grantType {
	case "authorization_code":
		if flow.AuthorizationURL == "" || flow.TokenURL == "" {
			return false
		}
	case "implicit":
		if flow.AuthorizationURL == "" {
			return false
		}
	case "password", "client_credentials":
		if flow.TokenURL == "" {
			return false
		}
	}
	for _, scope := range requiredScopes {
		if _, ok := flow.Scopes[scope]; !ok {
			return false
		}
	}
	return true
}

// absolutizeURL resolves a possibly-relative URL against the server base;
// absolute URLs pass through unchanged.
func absolutizeURL(ref, baseURL string) string {
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if u.IsAbs() {
		return ref
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
}

// validRefMethods are the OAS's HTTP method keys, lowercase exactly as the
// artifact spells them (OAPI-D-03). Acceptance never case-folds.
var validRefMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// parseRef parses a binding ref per OAPI-D-03: a JSON Pointer of the exact
// form `#/paths/<escaped-path>/<method>` addressing an operation object. The
// path segment carries RFC 6901 escaping ("/" → "~1", "~" → "~0"), and the
// method is lowercase exactly as the artifact spells it — an uppercase
// method is non-conformant and refused, never case-folded.
func parseRef(ref string) (path string, method string, err error) {
	const prefix = "#/paths/"
	if !strings.HasPrefix(ref, prefix) {
		return "", "", fmt.Errorf("ref %q must be a JSON Pointer of the form #/paths/<escaped-path>/<method> (OAPI-D-03)", ref)
	}
	parts := strings.Split(ref[len(prefix):], "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("ref %q must be a JSON Pointer of the form #/paths/<escaped-path>/<method>: the path segment carries RFC 6901 escaping (\"/\" → \"~1\") (OAPI-D-03)", ref)
	}
	escapedPath, method := parts[0], parts[1]
	if !validRefMethods[method] {
		if validRefMethods[strings.ToLower(method)] {
			return "", "", fmt.Errorf("ref %q: method %q must be lowercase exactly as the artifact spells it (OAPI-D-03)", ref, method)
		}
		return "", "", fmt.Errorf("invalid HTTP method %q in ref", method)
	}

	// RFC 6901 unescaping, in order: ~1 first, then ~0.
	path = strings.ReplaceAll(escapedPath, "~1", "/")
	path = strings.ReplaceAll(path, "~0", "~")
	return path, method, nil
}

func hasRequestBody(op *openapi3.Operation) bool {
	return op.RequestBody != nil && op.RequestBody.Value != nil
}

// requestWillEmitBody reports whether the flat input carries a value for the
// declared request body: any field the parameter routes do not consume is
// body content. requestBody.required never forces a body into existence —
// a required body with no value to carry refuses instead (§9.1).
func requestWillEmitBody(params openapi3.Parameters, input map[string]any, op *openapi3.Operation) bool {
	if !hasRequestBody(op) {
		return false
	}
	parameterNames := map[string]bool{}
	for _, parameter := range params {
		if parameter != nil && parameter.Value != nil {
			parameterNames[parameter.Value.Name] = true
		}
	}
	for name := range input {
		if !parameterNames[name] {
			return true
		}
	}
	return false
}

// encodePathValue percent-encodes one path parameter value with exactly
// JavaScript's encodeURIComponent byte set (every byte except ALPHA / DIGIT /
// "-" "_" "." "!" "~" "*" "'" "(" ")" is %XX-escaped, UTF-8 bytewise), so the
// Go and TS invokers substitute byte-identical URL path segments.
// url.PathEscape is NOT equivalent: it passes sub-delims like "$&+,:;=@"
// through unescaped.
func encodePathValue(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			b.WriteByte(c)
		default:
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xF])
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Credentials and channel assembly (§9.6: OAPI-P-09 wire application,
// OAPI-P-10 channel assembly)
// ---------------------------------------------------------------------------

// credentialPlacement is one credential's wire application: which channel
// it rides (header, query, or cookie) under which name.
type credentialPlacement struct {
	channel string
	name    string
	value   string
}

// selectCredentialPlacements chooses one complete, satisfiable OpenAPI
// Security Requirement Object in artifact order. Security Requirement Objects
// are alternatives (OR); schemes inside one object are conjunctive (AND).
// Consequently credentials are never pooled across alternatives. A
// collision makes that alternative unusable but does not prevent selecting a
// later complete alternative. No declared security means no credential wire
// application, even when unrelated credentials exist in context.
func selectCredentialPlacements(doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any, baseURL string, params openapi3.Parameters, populated map[string]map[string]bool) ([]credentialPlacement, []namedSecurityScheme, []credentialPlacement, error) {
	for _, plan := range viableSecurityPlans(doc, op, baseURL, params) {
		if len(plan.context.Requirements) == 0 {
			return nil, nil, nil, nil // this complete alternative explicitly allows anonymous access
		}
		if !contextSatisfies(bindCtx, &Prerequisites{
			Alternatives: []RequirementAlternative{plan.context},
		}) {
			continue
		}
		placements := credentialValues(plan, bindCtx)
		ownership := credentialDestinations(plan)
		if err := checkCredentialCollisions(ownership, params, populated); err != nil {
			return nil, nil, nil, err
		}
		return placements, plan.schemes, ownership, nil
	}
	// requiredContext prevents dispatch when no alternative is satisfied. This
	// path is therefore defensive for invalid or extension-only artifacts; it
	// must still not leak unrelated credentials onto the wire.
	return nil, nil, nil, nil
}

// credentialValues applies every scheme in exactly one selected security
// plan. It deliberately retains duplicate wire destinations so collision
// checks can refuse an impossible AND rather than silently dropping one.
func credentialValues(plan securityPlan, bindCtx map[string]any) []credentialPlacement {
	placements := make([]credentialPlacement, 0, len(plan.schemes))
	for _, named := range plan.schemes {
		s := named.scheme
		switch s.Type {
		case "apiKey":
			val := contextAPIKeyFor(bindCtx, named.name)
			if val == "" {
				continue
			}
			switch s.In {
			case "header", "query", "cookie":
				placements = append(placements, credentialPlacement{channel: s.In, name: s.Name, value: val})
			}
		case "http":
			switch strings.ToLower(s.Scheme) {
			case "bearer":
				if token := contextBearerTokenFor(bindCtx, named.name); token != "" {
					placements = append(placements, credentialPlacement{channel: "header", name: "Authorization", value: "Bearer " + token})
				}
			case "basic":
				if u, p, ok := contextBasicAuthFor(bindCtx, named.name); ok {
					placements = append(placements, credentialPlacement{channel: "header", name: "Authorization", value: "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+p))})
				}
			}
		case "oauth2", "openIdConnect":
			token := contextAccessTokenFor(bindCtx, named.name)
			if token == "" {
				token = contextBearerTokenFor(bindCtx, named.name)
			}
			if token != "" {
				placements = append(placements, credentialPlacement{channel: "header", name: "Authorization", value: "Bearer " + token})
			}
		}
	}
	return placements
}

// credentialDestinations is the artifact-only wire footprint of one security
// plan. It lets context negotiation discard OAPI-P-10-colliding alternatives
// before credentials exist, so an unusable alternative is never challenged.
func credentialDestinations(plan securityPlan) []credentialPlacement {
	placements := make([]credentialPlacement, 0, len(plan.schemes))
	for _, named := range plan.schemes {
		s := named.scheme
		switch s.Type {
		case "apiKey":
			switch s.In {
			case "header", "query", "cookie":
				if s.Name != "" {
					placements = append(placements, credentialPlacement{channel: s.In, name: s.Name})
				}
			}
		case "http":
			switch strings.ToLower(s.Scheme) {
			case "basic", "bearer":
				placements = append(placements, credentialPlacement{channel: "header", name: "Authorization"})
			}
		case "oauth2", "openIdConnect":
			placements = append(placements, credentialPlacement{channel: "header", name: "Authorization"})
		}
	}
	return placements
}

// checkCredentialCollisions enforces the OAPI-P-10 refusal: a name collision
// between a credential and a caller-populated declared parameter on the same
// channel is refused before dispatch — loud, never a silent overwrite in
// either direction. Header names compare case-insensitively.
func checkCredentialCollisions(placements []credentialPlacement, params openapi3.Parameters, populated map[string]map[string]bool) error {
	declared := map[string]map[string]bool{"header": {}, "query": {}, "cookie": {}, "path": {}}
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		name := ref.Value.Name
		if ref.Value.In == openapi3.ParameterInHeader {
			name = http.CanonicalHeaderKey(name)
		}
		declared[ref.Value.In][name] = true
	}
	ownedHeaders := map[string]bool{"Host": true, "Content-Length": true, "Content-Type": true, "Accept": true}
	hasRawCookieOwner := populated != nil && populated[openapi3.ParameterInHeader]["Cookie"]
	hasStructuredCookieOwner := populated != nil && len(populated[openapi3.ParameterInCookie]) > 0
	for _, placement := range placements {
		if placement.channel == "header" && http.CanonicalHeaderKey(placement.name) == "Cookie" {
			hasRawCookieOwner = true
		}
		if placement.channel == "cookie" {
			hasStructuredCookieOwner = true
		}
	}
	if hasRawCookieOwner && hasStructuredCookieOwner {
		return fmt.Errorf("raw Cookie header source collides with structured cookie assembly (OAPI-P-10)")
	}
	seen := map[string]bool{}
	for _, pl := range placements {
		name := pl.name
		if pl.channel == "header" {
			name = http.CanonicalHeaderKey(name)
			if ownedHeaders[name] {
				return fmt.Errorf("credential %q collides with processor-owned request field %s (OAPI-P-10)", pl.name, name)
			}
		}
		if declared[pl.channel][name] || populated[pl.channel][name] {
			return fmt.Errorf("credential %q collides with an effective %s parameter of the same name (OAPI-P-10: refused before dispatch, never a silent overwrite in either direction)", pl.name, pl.channel)
		}
		key := pl.channel + "\x00" + name
		if seen[key] {
			return fmt.Errorf("two credentials collide at %s %q (OAPI-P-10)", pl.channel, pl.name)
		}
		seen[key] = true
	}
	return nil
}

// contextChannelCollision keeps the context transport-hint channel from
// overwriting the structured Cookie assembly. Cookie is one HTTP field with
// two intentionally distinct caller surfaces (`headers.Cookie` and
// `cookies`); ambiguous ownership is refused before dispatch.
func contextChannelCollision(bindCtx map[string]any, params openapi3.Parameters, placements []credentialPlacement) error {
	rawContextCookie := false
	for name := range contextHeaders(bindCtx) {
		if http.CanonicalHeaderKey(name) == "Cookie" {
			rawContextCookie = true
			break
		}
	}
	hasRawCookieOwner := false
	hasStructuredCookie := len(contextCookies(bindCtx)) > 0
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		if ref.Value.In == openapi3.ParameterInHeader && http.CanonicalHeaderKey(ref.Value.Name) == "Cookie" {
			hasRawCookieOwner = true
		}
		if ref.Value.In == openapi3.ParameterInCookie {
			hasStructuredCookie = true
		}
	}
	for _, placement := range placements {
		if placement.channel == "header" && http.CanonicalHeaderKey(placement.name) == "Cookie" {
			hasRawCookieOwner = true
		}
		if placement.channel == "cookie" {
			hasStructuredCookie = true
		}
	}
	if rawContextCookie && (hasRawCookieOwner || hasStructuredCookie) {
		return fmt.Errorf("raw Cookie context header collides with another raw or structured cookie source (OAPI-P-10: refused before dispatch, never a silent overwrite)")
	}
	if hasRawCookieOwner && len(contextCookies(bindCtx)) > 0 {
		return fmt.Errorf("raw Cookie header source collides with structured context cookies (OAPI-P-10: refused before dispatch, never a silent overwrite)")
	}
	return nil
}
