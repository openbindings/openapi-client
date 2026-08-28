import {
  InvocationError,
  contextRequiredError,
  configValueRequirement,
  contextSatisfies,
  classifyThroughHooks,
  decodeThroughHooks,
  contextBearerTokenFor,
  contextApiKeyFor,
  contextBasicAuthFor,
  contextAccessTokenFor,
  contextHeaders,
  contextCookies,
  contextConfiguration,
  httpErrorCode,
  ERR_INVALID_REF,
  ERR_REFUSED,
  ERR_SOURCE_CONFIG_ERROR,
  ERR_REF_NOT_FOUND,
  ERR_CONNECT_FAILED,
  ERR_RESPONSE_ERROR,
  ERR_VALIDATION_FAILED,
  ERR_PROTOCOL,
  USE_DEFAULT,
  type BindingHandle,
  type InvokeHooks,
  type InvokeSite,
  type OutputDecoder,
  type RawResult,
  type BindingInvocationArgs,
  type ContextAlternative,
  type ContextRequirement,
  type ContextRequiredDetails,
  type Metadata,
  resolveDeliveryUnitLimit,
} from "./internal/index.js";
import type {
  OpenAPIDocument,
  OpenAPIOperation,
  OpenAPIParameter,
  OpenAPISecurityScheme,
  OpenAPIOAuthFlow,
} from "./types.js";
import { codePointCompare, errorMessage, parseRef } from "./util.js";
import {
  MissingPathParamError,
  effectiveParameters,
  queryEscape,
  routeInput,
  unflattenableParam,
  validateParameterSerialization,
} from "./params.js";
import { hasMediaFidelity, hasResponseFidelity, hasRoutedInputs } from "./constants.js";
import {
  envelopeWillEmitBody,
  flatInputHasAmbiguousParameter,
  parseRoutedEnvelope,
  routeEnvelope,
  validateEnvelopeRoutes,
  type RoutedEnvelope,
} from "./input-routes-v2.js";
import {
  acceptHeader,
  buildRequestBody,
  candidateCollides,
  configureRequestMedia,
  finalizeRequestBody,
  governingResponse,
  governingResponseMedia,
  governingResponseMediaMatch,
  isJSONMediaType,
  normalizeMediaType,
  parseMediaType,
  planRequestBodies,
  responseUsesRawBoundary,
  type BodyPlan,
} from "./media.js";
import { ConfigRequired, resolveServer } from "./servers.js";
import {
  buildOpenAPICredentialPlacements,
  encodeOpenAPICredentialQuery,
  openAPICredentialCollision,
  openAPICredentialDestinations,
  type OpenAPICredentialPlacement,
} from "./security-wire.js";
import type { OpenAPIExecutionProfile } from "./profile.js";

/**
 * Maps a server-resolution failure to the right terminal: a resolvable-missing
 * configuration value (a ConfigRequired signal) becomes a config.value
 * CONTEXT_REQUIRED challenge — retryable after resolution (R1a) — while any
 * other error stays a terminal ERR_SOURCE_CONFIG_ERROR. resolveServer already
 * consulted the supplied context; the operation-invoker's bounded
 * resolve-and-retry loop is the backstop. The point precedes destination
 * resolution — no server target exists yet — so the challenge's asserted
 * scope is the artifact's own identity: the canonicalized source location
 * already threaded to resolveServer as the base URI (context-scope model,
 * ratified 2026-08-19). A referenceless binding has no stable identity and
 * asserts nothing (empty target); consumers file and fetch by the exact
 * asserted key, never derive.
 */
function configOrSourceError(e: unknown, sourceLocation: string | undefined): InvocationError {
  if (e instanceof ConfigRequired) {
    return contextRequiredError(e.message, {
      target: sourceLocation ?? "",
      alternatives: [
        { requirements: [configValueRequirement(e.point, e.path, e.message, e.schema, e.durable)] },
      ],
    });
  }
  return new InvocationError(ERR_REFUSED, errorMessage(e));
}
import { isSSEContentType, streamSSE } from "./sse.js";

/**
 * Drives one OpenAPI binding invocation over the binding-facing handle: one
 * HTTP exchange per invocation (openbindings.openapi@1 §8). All
 * pre-dispatch refusals — bad ref, unresolvable operation, unflattenable
 * declarations, out-of-family request media, unresolvable server, missing
 * context, missing path parameters, unmatched input fields, credential
 * collisions — terminate the handle BEFORE any network side effect, and
 * before consuming input where knowable.
 */
export async function runBinding(
  args: BindingInvocationArgs,
  inv: BindingHandle<unknown, unknown>,
  doc: OpenAPIDocument,
  onReady?: () => void,
): Promise<void> {
  // ----- Pre-side-effect resolution. -----

  let path: string, method: string;
  try {
    ({ path, method } = parseRef(args.ref));
  } catch (e: unknown) {
    inv.fireError(new InvocationError(ERR_INVALID_REF, errorMessage(e)));
    return;
  }

  if (!doc.paths) {
    inv.fireError(
      new InvocationError(ERR_REFUSED, "OpenAPI document has no paths defined"),
    );
    return;
  }
  // Pointer evaluation follows OAS reference resolution (OAPI-D-03): the
  // loader dereferences path-item $refs (including 3.1 components.pathItems
  // targets) at load, before this lookup.
  const pathItem = doc.paths[path];
  if (!pathItem) {
    inv.fireError(new InvocationError(ERR_REF_NOT_FOUND, `path "${path}" not in OpenAPI doc`));
    return;
  }
  const op = pathItem[method] as OpenAPIOperation | undefined;
  if (!op) {
    inv.fireError(
      new InvocationError(ERR_REF_NOT_FOUND, `method "${method}" not in path "${path}"`),
    );
    return;
  }

  const routedRevision = hasRoutedInputs(args.source.profile);
  const revision3 = hasMediaFidelity(args.source.profile);
  const responseFidelity = hasResponseFidelity(args.source.profile);
  const planningOptions = {
    profile: args.source.profile,
    openapiVersion: doc.openapi,
    inventoryUnsupported: revision3,
  };
  const configuredMedia = contextConfiguration(args.context)["requestMedia"];
  // Revision 2 lifts cross-location name collisions through its routed
  // source value. Case-distinct declarations that HTTP itself treats as one
  // header name remain unresolvable in both revisions.
  const params = effectiveParameters(pathItem, op);
  const ownershipConflict = parameterOwnershipConflict(params);
  if (ownershipConflict !== "") {
    inv.fireError(new InvocationError(ERR_REFUSED, ownershipConflict));
    return;
  }
  const securityConfigurationFailure = securityConfigurationError(doc, op);
  if (securityConfigurationFailure !== "") {
    inv.fireError(new InvocationError(ERR_REFUSED, securityConfigurationFailure));
    return;
  }
  const unflattenable = unflattenableParam(params, args.source.profile);
  if (unflattenable !== "") {
    inv.fireError(
      new InvocationError(
        ERR_REFUSED,
        `operation declares parameter "${unflattenable}" without a distinct wire identity under execution profile ${args.source.profile.name} (OAPI-P-03, unflattenable/unresolvable)`,
      ),
    );
    return;
  }
  if (revision3) {
    try {
      for (const parameter of params) validateParameterSerialization(parameter);
    } catch (error: unknown) {
      inv.fireError(new InvocationError(ERR_REFUSED, errorMessage(error)));
      return;
    }
  }
  // Last of the declaration-only checks, and ahead of server resolution: an
  // operation with no addressable target refuses terminally rather than
  // challenging the consumer to configure a server it can never reach.
  const unaddressableTarget = pathTemplateAddressabilityConflict(path, params);
  if (unaddressableTarget !== "") {
    inv.fireError(new InvocationError(ERR_REFUSED, unaddressableTarget));
    return;
  }
  let baseURL: string;
  try {
    baseURL = resolveServer(doc, pathItem, op, args.context, args.source.location);
  } catch (e: unknown) {
    inv.fireError(configOrSourceError(e, args.source.location));
    return;
  }

  // CONTEXT_REQUIRED is raised before any input is consumed and before any
  // network I/O, so a no-input-consumed retry (after the operation layer
  // resolves context) is safe.
  const securityConflict = securityAlternativesCollision(doc, op, baseURL, params);
  if (securityConflict !== "") {
    inv.fireError(new InvocationError(ERR_REFUSED, securityConflict));
    return;
  }

  const details = requiredContext(doc, op, args.context, baseURL, params);
  if (details) {
    inv.fireError(
      contextRequiredError("OpenAPI operation requires authentication context", details),
    );
    return;
  }

  // A required body will necessarily consult request-media selection. Its
  // candidate set is artifact-only, so an unsupported/degenerate set is a
  // knowable pre-dispatch refusal and must not wait for caller input.
  let requiredBodyPlans: BodyPlan[] | undefined;
  if (op.requestBody?.required === true) {
    try {
      requiredBodyPlans = planRequestBodies(op, planningOptions);
    } catch (e: unknown) {
      inv.fireError(new InvocationError(ERR_REFUSED, errorMessage(e)));
      return;
    }
    const supportedBodyPlans = requiredBodyPlans.filter((plan) => !plan.unsupported);
    if (revision3 && supportedBodyPlans.length === 0) {
      inv.fireError(new InvocationError(
        ERR_REFUSED,
        "required request body has no declaration with a supported carriage in the selected execution profile",
      ));
      return;
    }
    if (
      revision3
      && supportedBodyPlans.length > 0
      && supportedBodyPlans.every((plan) => plan.range)
      && (configuredMedia === undefined || configuredMedia === null)
    ) {
      inv.fireError(requestMediaContextRequired(baseURL));
      return;
    }
  }

  // Everything knowable without caller input has now been accepted. The
  // neutral engine resolves start() at this boundary, allowing an adapter to
  // defer consuming its own input channel until all artifact/configuration
  // refusals have surfaced.
  onReady?.();

  // ----- Input flows through the handle, not the args. Whether this
  // interaction carries an input value is decided by the ARTIFACT and by what
  // the caller writes — never by the presence of the operation's `input`
  // member. Core §6.2: schema absence "means the document makes no portable
  // claim at that boundary", not that the interaction carries zero values. A
  // caller with nothing to say says it by closing. -----
  let inputMap: Record<string, unknown>;
  let envelope: RoutedEnvelope | null = null;
  let inputSupplied = false;
  if (params.length === 0 && op.requestBody == null) {
    // No-input operation: close input on entry so the caller never has to,
    // and dispatch immediately.
    void inv.closeInput();
    inputMap = {};
  } else {
    const first = await readFirst(inv.inputs());
    void inv.closeInput();
    if (first === undefined) {
      // Bare close: absent input and a supplied empty object are ONE rule
      // (§9.1 required declarations) — the two requests are wire-identical,
      // so they ride the same code below. The only pre-dispatch refusals are
      // a required request body with no value to carry and an unsupplied
      // path parameter; every other missing declaration is sent as supplied,
      // the server's validation authoritative.
      inputMap = {};
    } else {
      inputSupplied = true;
      if (routedRevision) {
        try {
          envelope = parseRoutedEnvelope(first, args.source.profile);
        } catch (e: unknown) {
          inv.fireError(new InvocationError(ERR_REFUSED, errorMessage(e)));
          return;
        }
      }
      if (envelope === null) {
        if (first === null || typeof first !== "object" || Array.isArray(first)) {
          inv.fireError(new InvocationError(ERR_REFUSED, "OpenAPI input value must be a JSON object"));
          return;
        }
        inputMap = first as Record<string, unknown>;
      } else {
        inputMap = {};
      }
    }
  }

  // ----- Routing (§9.1) and body construction (§9.2): still pre-dispatch. -----

  if (routedRevision && inputSupplied && envelope === null && flatInputHasAmbiguousParameter(params, inputMap)) {
    inv.fireError(new InvocationError(
      ERR_REFUSED,
      "this input supplies one flat field for independently declared same-named parameters and requires an explicit routed-input envelope",
    ));
    return;
  }

  let plans: BodyPlan[] = [];
  const willEmitBody = envelope
    ? envelopeWillEmitBody(envelope, op)
    : requestWillEmitBody(params, inputMap, op);
  // §9.1 required declarations: a required request body with no value to
  // carry refuses before dispatch, applied to absent and
  // supplied-but-incomplete input alike — the artifact's own
  // requestBody.required is the ground. An explicitly present body (the
  // routed envelope's body.present, or any field the routes carry to the
  // body) is a value; anything else leaves nothing to send.
  if (!willEmitBody && op.requestBody != null && op.requestBody.required === true) {
    inv.fireError(new InvocationError(
      ERR_REFUSED,
      "operation requires a request body: the input supplies no value to carry (§9.1 required declarations)",
    ));
    return;
  }
  if (willEmitBody || envelope) {
    try {
      plans = requiredBodyPlans ?? planRequestBodies(op, planningOptions);
    } catch (e: unknown) {
      inv.fireError(new InvocationError(ERR_REFUSED, errorMessage(e)));
      return;
    }
  }
  if (envelope) {
    try {
      validateEnvelopeRoutes(params, plans.filter((plan) => !plan.unsupported), envelope, args.source.profile);
    } catch (e: unknown) {
      inv.fireError(new InvocationError(ERR_REFUSED, errorMessage(e)));
      return;
    }
  }
  if (!willEmitBody) plans = [];

  let routed: ReturnType<typeof routeInput> | undefined;
  let wire: ReturnType<typeof buildRequestBody> | undefined;
  let routeFailure: unknown;
  const reasons: string[] = [];
  const candidates = plans.length === 0
    ? [null]
    : configuredRequestPlans(plans, args.context, planningOptions);
  for (const candidate of candidates) {
    if (envelope === null && candidate && candidateCollides(params, candidate)) {
      reasons.push(
        `request media candidate ${candidate.mediaType} collides with an independently declared parameter`,
      );
      continue;
    }
    try {
      const candidateRouted = envelope
        ? routeEnvelope(params, envelope, path, candidate, args.source.profile, {
          converter: args.parameterConverter,
        })
        : routeInput(params, inputMap, path, candidate, args.source.profile, {
          converter: args.parameterConverter,
        });
      const candidateWire = await finalizeRequestBody(
        buildRequestBody(doc, candidate, candidateRouted),
      );
      routed = candidateRouted;
      wire = candidateWire;
      break;
    } catch (e: unknown) {
      if (e instanceof MissingPathParamError) {
        routeFailure = e;
        break;
      }
      reasons.push(`${candidate?.mediaType ?? "no body"}: ${errorMessage(e)}`);
    }
  }
  if (!routed || !wire) {
    const failure = routeFailure ?? new Error(
      `no request media candidate can carry this invocation: ${
        reasons.length > 0 ? reasons.join("; ") : "configured requestMedia selects no declared supported candidate"
      }`,
    );
    // Every failure here — the unsupplied-path-parameter case included — is
    // a §9.1/§9.2 pre-dispatch refusal, and ERR_REFUSED is the
    // binding-invoker contract's never-dispatched guarantee (ruled
    // 2026-08-14; Go twin parity). ERR_MISSING_INPUT stays a
    // stream-cardinality code and cannot cover the supplied-but-incomplete
    // half of the one rule.
    inv.fireError(new InvocationError(ERR_REFUSED, errorMessage(failure)));
    return;
  }

  // ----- Channel assembly (§9.6, OAPI-P-10). -----

  const selectedSecurity = selectedSecurityPlan(doc, op, args.context, baseURL, params);
  const placements = selectedSecurity && args.context
    ? credentialValues(selectedSecurity, args.context)
    : [];
  const collision = credentialCollision(placements, params, routed.populated);
  if (collision !== "") {
    inv.fireError(new InvocationError(ERR_REFUSED, collision));
    return;
  }
  const contextCollision = contextChannelCollision(args.context, placements, routed.populated);
  if (contextCollision !== "") {
    inv.fireError(new InvocationError(ERR_REFUSED, contextCollision));
    return;
  }

  const queryUnits = [...routed.queryUnits];
  const cookieUnits = [...routed.cookieUnits];
  for (const pl of placements) {
    if (pl.channel === "query") {
      queryUnits.push(encodeOpenAPICredentialQuery(pl.name) + "=" + encodeOpenAPICredentialQuery(pl.value));
    } else if (pl.channel === "cookie") {
      cookieUnits.push(pl.name + "=" + pl.value);
    }
  }
  // Context-supplied transport-hint cookies (consumer context, not
  // security-scheme credentials) ride after credentials, sorted for
  // determinism.
  const hintCookies = contextCookies(args.context);
  for (const k of Object.keys(hintCookies).sort()) {
    cookieUnits.push(`${k}=${hintCookies[k]}`);
  }

  let reqURL = baseURL + routed.resolvedPath;
  if (queryUnits.length > 0) {
    reqURL += "?" + queryUnits.join("&");
  }

  const fetchHeaders = new Headers();
  if (wire.contentType !== "") {
    fetchHeaders.set("Content-Type", wire.contentType);
  }
  // Advertise only artifact-declared concrete success media; an empty set
  // leaves Accept absent.
  const accept = acceptHeader(op, revision3, responseFidelity);
  if (accept !== "") fetchHeaders.set("Accept", accept);

  for (const [k, v] of routed.headers) {
    fetchHeaders.set(k, v);
  }
  for (const pl of placements) {
    if (pl.channel === "header") {
      fetchHeaders.set(pl.name, pl.value);
    }
  }
  // Context-supplied transport-hint headers (consumer context) apply last.
  for (const [k, v] of Object.entries(contextHeaders(args.context))) {
    fetchHeaders.set(k, v);
  }
  // One Cookie header (OAPI-P-10): declared cookie parameters in
  // declaration order, credentials appended after.
  if (cookieUnits.length > 0) {
    fetchHeaders.set("Cookie", cookieUnits.join("; "));
  }

  const doFetch = args.fetch ?? fetch;
  let resp: Response;
  const requestInit: RequestInit = {
    method: method.toUpperCase(),
    headers: fetchHeaders,
    body: wire.body,
    signal: inv.signal,
    redirect: args.redirect ?? "manual",
  };
  let securedRequest: Request | undefined;
  if (selectedSecurity && args.securityHandlers) {
    securedRequest = new Request(reqURL, requestInit);
    for (const { name, scheme } of selectedSecurity.schemes) {
      const handler = args.securityHandlers[name];
      if (!handler) continue;
      try {
        securedRequest = await handler(securedRequest, { schemeName: name, scheme }) ?? securedRequest;
      } catch (error: unknown) {
        inv.fireError(new InvocationError(ERR_REFUSED, errorMessage(error)));
        return;
      }
    }
  }
  try {
    resp = securedRequest
      ? await doFetch(securedRequest)
      : await doFetch(reqURL, requestInit);
  } catch (e: unknown) {
    // Aborted while in flight: the handle is already terminal (caller
    // cancel or another terminal transition); stay silent.
    if (inv.signal.aborted) return;
    // The request never produced a response: a transport-level failure.
    inv.fireError(new InvocationError(ERR_CONNECT_FAILED, errorMessage(e)));
    return;
  }

  // Leading metadata (HTTP response headers) precedes the first emit.
  const invocationMeta = responseMetadata(resp);
  inv.setHeader(invocationMeta);

  const contentType = resp.headers.get("content-type");
  const site = siteFor(args, baseURL);
  const responseDeclaration = governingResponse(op, resp.status);

  // A truly empty 2xx carries no output regardless of a stray streaming
  // Content-Type. Peek without buffering the stream: a non-empty first
  // chunk is replayed into a replacement Response for normal SSE handling.
  if (isSSEContentType(contentType)) {
    try {
      const peeked = await peekResponseBody(resp);
      resp = peeked.response;
      if (peeked.empty) {
        const raw: RawResult = { status: resp.status, body: "", meta: invocationMeta };
        const ok = await classifyThroughHooks(args.hooks, site, raw, builtinClassify);
        if (!ok) {
          inv.fireError(new InvocationError(
            httpErrorCode(resp.status),
            "Invocation completed unsuccessfully",
            undefined,
            openAPIFailureDetails(
              resp,
              new Uint8Array(),
              "",
              invocationMeta,
              responseDeclaration,
              contentType,
              revision3,
              responseFidelity,
            ),
          ));
          return;
        }
        inv.setTrailer(decodeClassifyTrailer(args.hooks, "not-consulted/empty"));
        inv.closeOutput();
        return;
      }
    } catch (error: unknown) {
      inv.fireError(toInvocationError(error));
      return;
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
  if (isSSEContentType(contentType)) {
    // Classification is independent of declaration lookup. A non-success
    // final status is the native HTTP failure even if its body happens to
    // use event-stream framing.
    let ok: boolean;
    try {
      ok = await classifyThroughHooks(
        args.hooks,
        site,
        { status: resp.status, body: "", meta: invocationMeta },
        builtinClassify,
      );
    } catch (e: unknown) {
      await resp.body?.cancel().catch(() => {});
      inv.fireError(toInvocationError(e));
      return;
    }
    if (!ok) {
      // A non-2xx event-stream response is one unsuccessful HTTP exchange,
      // not a stream of successful operation values. Preserve its exact
      // response bytes under the same consumer-owned delivery-unit bound as
      // the unary failure lane. Detecting SSE framing must never discard
      // native evidence.
      let failureBody: Uint8Array;
      try {
        failureBody = await readResponseBytes(resp, resolveDeliveryUnitLimit(args));
      } catch (e: unknown) {
        if (inv.signal.aborted) return;
        inv.fireError(new InvocationError(ERR_RESPONSE_ERROR, errorMessage(e)));
        return;
      }
      if (inv.signal.aborted) return;
      inv.fireError(
        new InvocationError(
          httpErrorCode(resp.status),
          "Invocation completed unsuccessfully",
          undefined,
          openAPIFailureDetails(
            resp,
            failureBody,
            failureBody.length > 0 ? new TextDecoder().decode(failureBody) : "",
            invocationMeta,
            responseDeclaration,
            contentType,
            revision3,
            responseFidelity,
          ),
        ),
      );
      return;
    }
    let governingMedia: string | null;
    try {
      governingMedia = responseDeclaration
        ? governingResponseMedia(responseDeclaration.response, contentType, revision3, responseFidelity)
        : null;
    } catch (e: unknown) {
      await resp.body?.cancel().catch(() => {});
      inv.fireError(
        new InvocationError(
          ERR_PROTOCOL,
          `response arrived as text/event-stream, but the governing response does not declare that media type: ${errorMessage(e)}`,
        ),
      );
      return;
    }
    if (
      !governingMedia
      || (!responseFidelity && parseMediaType(governingMedia).base !== "text/event-stream")
    ) {
      await resp.body?.cancel().catch(() => {});
      inv.fireError(
        new InvocationError(
          ERR_PROTOCOL,
          "response arrived as text/event-stream, but the governing response does not declare that media type",
        ),
      );
      return;
    }
    inv.setTrailer({ "x-ob-governing-media": [governingMedia] });
    await streamSSE(resp, args, site, inv, invocationMeta, decodeByContentType(contentType));
    return;
  }

  let bodyBytes: Uint8Array;
  try {
    // The unary body is one delivery unit: the consumer-configurable
    // delivery-unit bound applies (args.maxDeliveryUnitBytes, default 10MB).
    bodyBytes = await readResponseBytes(resp, resolveDeliveryUnitLimit(args));
  } catch (e: unknown) {
    if (inv.signal.aborted) return;
    inv.fireError(new InvocationError(ERR_RESPONSE_ERROR, errorMessage(e)));
    return;
  }

  // Cancelled while in flight: the handle is already terminal.
  if (inv.signal.aborted) return;

  // Classify, then decode — both through the consultation seam
  // (per-invocation hook → invoker-level hook → the format builtins
  // below). The binding specification's defaults (OAPI-P-07/P-08),
  // content-independent throughout: classify = success iff status ∈ 2xx
  // (declared `responses` never change classification — they can identify
  // application-authored failure data); decode = the response's Content-Type HEADER decides
  // the lane (wire framing, not payload sniffing): JSON for
  // application/json and +json suffixes, the charset-honoring text lane
  // otherwise, absent/unparseable header → text.
  //
  // The seam's RawResult carries the unit's text; the builtin decoder
  // below closes over the BYTES, so the charset rule (OAPI-P-07) applies
  // to the wire bytes, not a pre-decoded string.
  const lossyText = bodyBytes.length > 0 ? new TextDecoder().decode(bodyBytes) : "";
  const raw: RawResult = { status: resp.status, body: lossyText, meta: invocationMeta };

  let ok: boolean;
  try {
    ok = await classifyThroughHooks(args.hooks, site, raw, builtinClassify);
  } catch (e: unknown) {
    inv.fireError(toInvocationError(e));
    return;
  }
  if (!ok) {
    // The format's NATIVE failure: hooks change the verdict, never the
    // error vocabulary. It is not an operation output, but the complete
    // native response and the OpenAPI declaration match remain available on
    // the failure completion. The legacy status/body members remain for
    // callers that already consume them.
    const failureValue = openAPIFailureValue(
      resp,
      bodyBytes,
      responseDeclaration,
      contentType,
      doc.openapi ?? "3.0",
      args.source.profile,
      revision3,
      responseFidelity,
    );
    inv.fireError(
      new InvocationError(
        httpErrorCode(resp.status),
        "Invocation completed unsuccessfully",
        failureValue.present ? failureValue.value : undefined,
        openAPIFailureDetails(
          resp,
          bodyBytes,
          lossyText,
          invocationMeta,
          responseDeclaration,
          contentType,
          revision3,
          responseFidelity,
        ),
      ),
    );
    return;
  }

  // An empty successful response carries absence, not an invented null.
  if (bodyBytes.length === 0) {
    inv.setTrailer(decodeClassifyTrailer(args.hooks, "not-consulted/empty"));
    inv.closeOutput();
    return;
  }

  let governingMedia: string;
  let mediaMatch: ReturnType<typeof governingResponseMediaMatch>;
  try {
    if (!responseDeclaration) {
      throw new Error(`status ${resp.status} has no governing Response Object`);
    }
    mediaMatch = governingResponseMediaMatch(
      responseDeclaration.response,
      contentType,
      revision3,
      responseFidelity,
    );
    governingMedia = mediaMatch?.declared.canonical ?? "";
    if (!mediaMatch || !governingMedia) {
      throw new Error("the governing Response Object declares no response content");
    }
  } catch (e: unknown) {
    inv.fireError(new InvocationError(ERR_PROTOCOL, errorMessage(e)));
    return;
  }

  let output: unknown;
  try {
    const builtin = responseFidelity
      && contentType !== null
      && mediaMatch !== null
      && responseUsesRawBoundary(
        mediaMatch.media,
        contentType,
        doc.openapi ?? "3.0",
        args.source.profile,
        !("specificity" in mediaMatch.declared),
      )
      ? ((_site: InvokeSite, _raw: RawResult): unknown => bytesToBase64(bodyBytes))
      : decodeBytesByContentType(contentType, bodyBytes, revision3);
    output = await decodeThroughHooks(args.hooks, site, raw, builtin);
  } catch (e: unknown) {
    inv.fireError(toInvocationError(e));
    return;
  }

  // Success provenance stamps (conventions record): decode provenance is
  // header/content-type when the builtin (the Content-Type lane) decided,
  // hook when overridden; classify is always assumption/2xx unless a hook
  // widened it.
  const trailer = decodeClassifyTrailer(args.hooks, "header/content-type");
  trailer["x-ob-governing-media"] = [governingMedia];
  inv.setTrailer(trailer);
  args.observeOutput?.(output, invocationMeta);
  await inv.emitOutput(output);
  inv.closeOutput();
}

/** Reads the first input message from the handle, or undefined when the input side closed bare. */
async function readFirst<T>(inputs: AsyncIterable<T>): Promise<T | undefined> {
  for await (const v of inputs) {
    return v;
  }
  return undefined;
}

/**
 * Reports whether the flat input carries a value for the declared request
 * body: any field the parameter routes do not consume is body content.
 * requestBody.required never forces a body into existence — a required body
 * with no value to carry refuses instead (§9.1).
 */
function requestWillEmitBody(
  params: OpenAPIParameter[],
  input: Record<string, unknown>,
  op: OpenAPIOperation,
): boolean {
  if (op.requestBody == null) return false;
  const parameterNames = new Set(params.map((parameter) => parameter.name).filter((name): name is string => Boolean(name)));
  return Object.keys(input).some((name) => !parameterNames.has(name));
}

/** Applies the optional artifact-neutral request-media configuration point. */
function configuredRequestPlans(
  plans: BodyPlan[],
  ctx: Record<string, unknown> | undefined,
  options: { profile: OpenAPIExecutionProfile; openapiVersion?: string },
): BodyPlan[] {
  const raw = contextConfiguration(ctx)["requestMedia"];
  if (raw == null) return hasMediaFidelity(options.profile)
    ? plans.filter((plan) => !plan.range && !plan.unsupported)
    : plans;
  if (typeof raw !== "string") return [];
  if (hasMediaFidelity(options.profile)) {
    return configureRequestMedia(plans, raw, options);
  }
  let wanted: string;
  try {
    wanted = parseMediaType(raw).identity;
  } catch {
    return [];
  }
  return plans.filter((plan) => {
    try {
      return parseMediaType(plan.mediaKey).identity === wanted;
    } catch {
      return false;
    }
  });
}

function requestMediaContextRequired(target: string): InvocationError {
  return contextRequiredError(
    "OpenAPI request media range requires a concrete requestMedia choice",
    requestMediaContextDetails(target),
  );
}

function requestMediaContextDetails(target: string): ContextRequiredDetails {
  return {
    target,
    alternatives: [{
      requirements: [configValueRequirement(
        "requestMedia",
        "",
        "select a concrete request media type admitted by the OpenAPI content declarations",
        undefined,
        true,
      )],
    }],
  };
}

/** Side-effect-free requestMedia preflight for a required represented range-only body. */
export function requiredRequestMediaContext(
  doc: OpenAPIDocument,
  op: OpenAPIOperation,
  profile: OpenAPIExecutionProfile,
  ctx: Record<string, unknown> | undefined,
  target: string,
): ContextRequiredDetails | null {
  if (
    !hasMediaFidelity(profile)
    || op.requestBody?.required !== true
    || contextConfiguration(ctx)["requestMedia"] != null
  ) {
    return null;
  }
  try {
    const supported = planRequestBodies(op, { profile, openapiVersion: doc.openapi });
    return supported.length > 0 && supported.every((plan) => plan.range)
      ? requestMediaContextDetails(target)
      : null;
  } catch {
    return null;
  }
}

/**
 * The openapi builtin result classifier (OAPI-P-08): success iff the final
 * HTTP status is 2xx (declared responses may identify application failure data,
 * never classification).
 */
export function builtinClassify(_site: InvokeSite, raw: RawResult): boolean | typeof USE_DEFAULT {
  return raw.status != null && raw.status >= 200 && raw.status < 300;
}

/**
 * Returns the builtin decoder implementing the header rule (OAPI-P-07)
 * over one delivery unit's TEXT (the SSE per-event lane): strict JSON for
 * application/json and +json suffixes (a declared-JSON body that fails to
 * parse is a lying server — a loud ERR_RESPONSE_ERROR, never a silent
 * string); the text itself otherwise. It carries no empty-body rule — a
 * DISPATCHED event whose data text is empty (a lone empty `data:` line,
 * §8/WHATWG) is the empty-string value; the empty-body→no-value rule is a
 * whole-response rule (decodeBytesByContentType), never per-event.
 */
export function decodeByContentType(contentType: string | null): OutputDecoder {
  const isJSON = isJSONMediaType(normalizeMediaType(contentType ?? ""));
  return (_site: InvokeSite, raw: RawResult): unknown => {
    if (isJSON) {
      try {
        return JSON.parse(raw.body);
      } catch (e: unknown) {
        throw new InvocationError(
          ERR_RESPONSE_ERROR,
          `response declares ${JSON.stringify(contentType)} but the body is not valid JSON: ${errorMessage(e)}`,
        );
      }
    }
    return raw.body;
  };
}

/**
 * Returns the builtin decoder implementing the header rule (OAPI-P-07)
 * over the response BYTES: strict JSON for application/json and +json
 * suffixes; the charset-honoring text lane otherwise (UTF-8 default,
 * us-ascii/latin-1 supported, invalid sequences and unsupported charsets
 * are loud decode errors). An empty body (204 included) yields null.
 */
export function decodeBytesByContentType(
  contentType: string | null,
  bytes: Uint8Array,
  revision3 = false,
): OutputDecoder {
  const isJSON = isJSONMediaType(normalizeMediaType(contentType ?? ""));
  return (_site: InvokeSite, _raw: RawResult): unknown => {
    if (bytes.length === 0) return null;
    if (isJSON) {
      try {
        return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
      } catch (e: unknown) {
        throw new InvocationError(
          ERR_RESPONSE_ERROR,
          `response declares ${JSON.stringify(contentType)} but the body is not valid JSON: ${errorMessage(e)}`,
        );
      }
    }
    return decodeTextLane(contentType, bytes, revision3);
  };
}

/**
 * Decodes response bytes as text per the Content-Type header's charset
 * parameter, defaulting to UTF-8 (OAPI-P-07). Invalid sequences, and
 * charsets this implementation cannot decode, are loud decode errors — a
 * consumer needing another charset overrides at the decode configuration
 * point.
 */
export function decodeTextLane(
  contentType: string | null,
  bytes: Uint8Array,
  revision3 = false,
): string {
  let charset = "utf-8";
  if (contentType) {
    if (revision3) {
      let parsed;
      try {
        parsed = parseMediaType(contentType, true);
      } catch (error: unknown) {
        throw new InvocationError(
          ERR_RESPONSE_ERROR,
          `response Content-Type is invalid: ${errorMessage(error)}`,
        );
      }
      if (Object.hasOwn(parsed.params, "charset")) charset = parsed.params["charset"]!;
    } else {
      const m = /;\s*charset\s*=\s*"?([^";]+)"?/i.exec(contentType);
      if (m?.[1]) charset = m[1].trim();
    }
  }
  switch (charset.toLowerCase()) {
    case "utf-8":
    case "utf8":
      try {
        return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
      } catch {
        throw new InvocationError(
          ERR_RESPONSE_ERROR,
          "response body is not valid UTF-8 (the declared/default charset)",
        );
      }
    case "us-ascii":
    case "ascii": {
      for (const [i, b] of bytes.entries()) {
        if (b >= 0x80) {
          throw new InvocationError(
            ERR_RESPONSE_ERROR,
            `response body byte ${i} is not valid US-ASCII (the declared charset)`,
          );
        }
      }
      return latin1String(bytes);
    }
    case "iso-8859-1":
    case "iso8859-1":
    case "latin-1":
    case "latin1":
      // True latin-1: each byte IS its code point (a TextDecoder
      // "iso-8859-1" label would decode windows-1252, which differs in
      // 0x80–0x9F).
      return latin1String(bytes);
    default:
      throw new InvocationError(
        ERR_RESPONSE_ERROR,
        `response declares charset ${JSON.stringify(charset)}, which this implementation cannot decode; override at the decode configuration point`,
      );
  }
}

function latin1String(bytes: Uint8Array): string {
  let out = "";
  for (const b of bytes) {
    out += String.fromCharCode(b);
  }
  return out;
}

/**
 * Completes the site for one dispatch with the format-known target (the
 * resolved base URL). A missing site (direct format-package call) gets a
 * minimal one so hook tables keyed on format/ref still match.
 */
function siteFor(args: BindingInvocationArgs, baseURL: string): InvokeSite {
  const site: InvokeSite = args.site
    ? { ...args.site }
    : {
        operation: "",
        invokedAs: "",
        bindingKey: "",
        bindingSpec: `openapi/${args.source.profile.name}`,
        ref: args.ref,
        target: "",
      };
  if (site.target === "") site.target = baseURL;
  return site;
}

/**
 * Builds the x-ob-decode/x-ob-classify success stamps (the provenance the
 * conventions record's recommended built-in defaults call for,
 * spec/binding-specs/README.md) for the HTTP lane, given the decode axis's
 * builtin provenance token. A hook decision on either axis stamps "hook".
 */
function decodeClassifyTrailer(hooks: InvokeHooks | null | undefined, builtinDecode: string): Metadata {
  let decode = builtinDecode;
  let classify = "assumption/2xx";
  if (hooks?.decodeDecidedBy() === "hook") decode = "hook";
  if (hooks?.classifyDecidedBy() === "hook") classify = "hook";
  return { "x-ob-decode": [decode], "x-ob-classify": [classify] };
}

/** Converts a seam failure into the terminal InvocationError to surface. */
function toInvocationError(e: unknown): InvocationError {
  if (e instanceof InvocationError) return e;
  return new InvocationError(ERR_RESPONSE_ERROR, errorMessage(e));
}

/** Converts fetch Response headers into multi-valued invocation metadata. */
function responseMetadata(resp: Response): Metadata {
  const md: Metadata = {};
  resp.headers.forEach((value, key) => {
    const existing = md[key];
    if (existing) {
      existing.push(value);
    } else {
      md[key] = [value];
    }
  });
  return md;
}

/**
 * Builds the binding-native evidence carried by an unsuccessful OpenAPI HTTP
 * exchange. `httpResponse.body.base64` is the fidelity record: it preserves
 * arbitrary bytes for protocol-aware standalone-runtime consumers. The older
 * top-level `status`/`body` members remain a convenience text view.
 */
function openAPIFailureDetails(
  resp: Response,
  bodyBytes: Uint8Array | null,
  textView: string,
  metadata: Metadata,
  declaration: ReturnType<typeof governingResponse>,
  contentType: string | null,
  revision3: boolean,
  responseFidelity: boolean,
): Record<string, unknown> {
  const httpResponse: Record<string, unknown> = {
    status: resp.status,
    headers: Object.fromEntries(
      Object.entries(metadata).map(([name, values]) => [name.toLowerCase(), [...values]]),
    ),
  };
  if (bodyBytes !== null) {
    httpResponse.body = {
      base64: bytesToBase64(bodyBytes),
      byteLength: bodyBytes.byteLength,
    };
  }
  if (resp.statusText !== "") httpResponse.statusText = resp.statusText;
  if (resp.url !== "") httpResponse.url = resp.url;

  const artifact: Record<string, unknown> = { declared: declaration !== null };
  if (declaration) {
    artifact.responseKey = declaration.key;
    try {
      const governingMedia = governingResponseMedia(
        declaration.response,
        contentType,
        revision3,
        responseFidelity,
      );
      if (governingMedia) artifact.governingMedia = governingMedia;
    } catch {
      // The mismatch is already preserved by the actual headers and matched
      // Response Object key. Failure evidence must not replace the native
      // status with a new decode/protocol failure.
    }
  }

  return {
    status: resp.status,
    ...(textView.length > 0 ? { body: textView } : {}),
    httpResponse,
    openapi: artifact,
  };
}

/**
 * Identifies the application value inside an unsuccessful OpenAPI response.
 * Protocol evidence remains on the standalone runtime's native error; only a
 * declaration-selected, faithfully decoded JSON-domain value is eligible for
 * the OpenBindings adapter's opaque failure-data lane.
 */
function openAPIFailureValue(
  resp: Response,
  bodyBytes: Uint8Array,
  declaration: ReturnType<typeof governingResponse>,
  contentType: string | null,
  openAPIVersion: string,
  profile: OpenAPIExecutionProfile,
  revision3: boolean,
  responseFidelity: boolean,
): { present: false } | { present: true; value: unknown } {
  if (
    bodyBytes.byteLength === 0
    || contentType === null
    || isSSEContentType(contentType)
    || declaration === null
  ) {
    return { present: false };
  }
  try {
    const match = governingResponseMediaMatch(
      declaration.response,
      contentType,
      revision3,
      responseFidelity,
    );
    if (!match) return { present: false };
    // A declared failure body decodes through the same response lanes a
    // successful body would (openbindings.openapi §9.5 names §9.2's lanes,
    // ruled 2026-08-13): JSON-family as JSON, a declared raw-byte lane as
    // the canonical Base64 boundary string, every other represented
    // selection through the UTF-8 text lane (Go twin: openAPIFailureError).
    if (
      responseFidelity
      && responseUsesRawBoundary(
        match.media,
        contentType,
        openAPIVersion,
        profile,
        !("specificity" in match.declared),
      )
    ) {
      return { present: true, value: bytesToBase64(bodyBytes) };
    }
    const decode = decodeBytesByContentType(contentType, bodyBytes, revision3);
    return {
      present: true,
      value: decode(
        { operation: "", invokedAs: "", bindingKey: "", bindingSpec: "openapi", ref: "", target: "" },
        { status: resp.status, body: "", meta: {} },
      ),
    };
  } catch {
    return { present: false };
  }
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunkSize = 0x8000;
  for (let offset = 0; offset < bytes.byteLength; offset += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunkSize));
  }
  return btoa(binary);
}

// ---------------------------------------------------------------------------
// Context requirements (openbindings.binding-invoker role negotiation)
// ---------------------------------------------------------------------------

/**
 * Derives the context requirements for an operation from the OpenAPI
 * document's securitySchemes and the operation's (or document's) security
 * requirements, and checks them against the supplied context. Returns the
 * CONTEXT_REQUIRED details when the context is insufficient, or null when
 * no auth is required or the context satisfies one alternative.
 *
 * Each OpenAPI Security Requirement Object remains one authored AND-set;
 * OAuth flow choices expand that set into equivalent runtime alternatives.
 * The document array remains the outer OR.
 */
export function requiredContext(
  doc: OpenAPIDocument,
  op: OpenAPIOperation,
  ctx: Record<string, unknown> | undefined,
  baseURL: string,
  params: OpenAPIParameter[] = [],
): ContextRequiredDetails | null {
  const plans = viableSecurityPlans(doc, op, baseURL, params);
  if (!plans) return null;
  // An empty Security Requirement Object is an anonymous alternative. The
  // binding-invoker context shape intentionally has no empty alternatives,
  // so consume it here rather than emitting a malformed challenge.
  if (plans.some((plan) => plan.context.requirements.length === 0)) return null;
  const details: ContextRequiredDetails = {
    target: baseURL,
    alternatives: plans.map((plan) => plan.context),
  };
  if (ctx && plans.some((plan) => securityPlanSatisfied(plan, ctx, baseURL))) return null;
  return details;
}

interface SecurityPlan {
  context: ContextAlternative;
  schemes: NamedSecurityScheme[];
}

/**
 * A Security Requirement Object can only be interpreted when every named
 * scheme resolves through components.securitySchemes. An unresolved name is
 * invalid source configuration, never an anonymous alternative: treating it
 * as absent would weaken the API author's security declaration.
 */
function securityConfigurationError(
  doc: OpenAPIDocument,
  op: OpenAPIOperation,
): string {
  const opSec = op.security as Array<Record<string, unknown>> | undefined;
  const docSec = (doc as Record<string, unknown>)["security"] as
    | Array<Record<string, unknown>>
    | undefined;
  const requirements = opSec ?? docSec;
  if (!requirements?.length) return "";

  const components = (doc as Record<string, unknown>)["components"] as
    | Record<string, unknown>
    | undefined;
  const securitySchemes = components?.["securitySchemes"] as
    | Record<string, OpenAPISecurityScheme>
    | undefined;
  const missing = new Set<string>();
  for (const requirement of requirements) {
    for (const name of Object.keys(requirement)) {
      if (!securitySchemes?.[name]) missing.add(name);
    }
  }
  if (missing.size === 0) return "";
  return `OpenAPI security requirement references undefined security scheme${missing.size === 1 ? "" : "s"}: ${[...missing].sort().join(", ")}`;
}

/**
 * Expands the artifact's OR-of-AND Security Requirement Objects without
 * flattening them. An OAuth scheme can contribute more than one usable flow,
 * so one authored AND-set expands to the Cartesian product of its schemes'
 * context alternatives. Every expanded plan still represents exactly one
 * complete artifact-declared Security Requirement Object.
 */
function securityPlans(
  doc: OpenAPIDocument,
  op: OpenAPIOperation,
  baseURL: string,
): SecurityPlan[] | null {
  const opSec = op.security as Array<Record<string, unknown>> | undefined;
  const docSec = (doc as Record<string, unknown>)["security"] as
    | Array<Record<string, unknown>>
    | undefined;
  // Operation-level security replaces document-level entirely (including
  // an explicit empty array, which removes auth for the operation).
  const requirements = opSec ?? docSec;
  if (!requirements?.length) return null;

  const components = (doc as Record<string, unknown>)["components"] as
    | Record<string, unknown>
    | undefined;
  const securitySchemes = components?.["securitySchemes"] as
    | Record<string, OpenAPISecurityScheme>
    | undefined;

  const alternatives: SecurityPlan[] = [];
  for (const req of requirements) {
    const names = Object.keys(req);
    if (names.length === 0) {
      alternatives.push({ context: { requirements: [] }, schemes: [] });
      continue;
    }

    let expanded: SecurityPlan[] = [{ context: { requirements: [] }, schemes: [] }];
    let expressible = true;
    for (const name of names.sort()) {
      const scheme = securitySchemes?.[name];
      if (!scheme) {
        // Invocation validates this as source configuration before reaching
        // plan construction. Retain a defensive skip for direct helper calls;
        // it must never become a dispatch path.
        expressible = false;
        break;
      }
      const scopes = Array.isArray(req[name])
        ? req[name].filter((scope): scope is string => typeof scope === "string")
        : [];
      const options = schemeRequirements(scheme, baseURL, name, scopes);
      expanded = expanded.flatMap((plan) =>
        options.map((requirement) => ({
          context: { requirements: [...plan.context.requirements, requirement] },
          schemes: [...plan.schemes, { scheme, name }],
        })),
      );
    }
    // The invocation path already refused an undefined scheme name as source
    // configuration. A scheme that resolves but maps to no
    // known family is NOT skipped — schemeRequirement surfaces it as a typed
    // "auth.<T>" requirement instead (R2.c ruling), so the alternative stays
    // discoverable even though the built-in satisfaction check can never
    // select it (contextSatisfies treats an unrecognized type as
    // unsatisfiable).
    if (!expressible || expanded.length === 0) continue;
    alternatives.push(...expanded);
  }
  return alternatives.length > 0 ? alternatives : null;
}

function viableSecurityPlans(
  doc: OpenAPIDocument,
  op: OpenAPIOperation,
  baseURL: string,
  params: OpenAPIParameter[],
): SecurityPlan[] | null {
  const plans = securityPlans(doc, op, baseURL);
  if (!plans) return null;
  const populated = { header: new Set<string>(), query: new Set<string>(), cookie: new Set<string>() };
  const viable = plans.filter(
    (plan) => credentialCollision(credentialDestinations(plan), params, populated) === "",
  );
  return viable.length > 0 ? viable : null;
}

/** Reports a collision only when every declared security alternative is unusable. */
function securityAlternativesCollision(
  doc: OpenAPIDocument,
  op: OpenAPIOperation,
  baseURL: string,
  params: OpenAPIParameter[],
): string {
  const plans = securityPlans(doc, op, baseURL);
  if (!plans) return "";
  const populated = { header: new Set<string>(), query: new Set<string>(), cookie: new Set<string>() };
  const collisions = plans.map((plan) =>
    credentialCollision(credentialDestinations(plan), params, populated),
  );
  return collisions.every((collision) => collision !== "") ? collisions[0] ?? "" : "";
}

/**
 * Maps an OpenAPI security scheme to a context requirement, carrying the
 * family-specific fields a resolver needs to act without out-of-band
 * knowledge (notably oauth2 flow endpoints), the requirement's `name` (the
 * securitySchemes key — rule A), and its `description` when the artifact
 * declares one. Every scheme maps to SOMETHING: a recognized family, or —
 * per the R2.c ruling — a surfaced `auth.<T>` requirement for a scheme this
 * invoker has no resolver for, so the alternative stays discoverable (a
 * runtime with a resolver for that family could still satisfy it) instead of
 * being silently dropped. A document whose every alternative is unmappable
 * this way now produces a pre-dispatch CONTEXT_REQUIRED challenge instead of
 * dispatching unauthenticated into a blind 401.
 */
function schemeRequirements(
  scheme: OpenAPISecurityScheme,
  baseURL: string,
  name: string,
  requiredScopes: string[],
): ContextRequirement[] {
  const requirements = scheme.type === "oauth2"
    ? oauth2Requirements(scheme, baseURL, requiredScopes)
    : [mapScheme(scheme, baseURL, requiredScopes)];
  return requirements.map((requirement) => ({
    ...requirement,
    name,
    durable: true,
    ...(scheme.description ? { description: scheme.description } : {}),
  }));
}

/** The type-specific mapping `schemeRequirements` wraps with name/description. */
function mapScheme(
  scheme: OpenAPISecurityScheme,
  baseURL: string,
  requiredScopes: string[],
): ContextRequirement {
  switch (scheme.type) {
    case "http": {
      const httpScheme = (scheme.scheme ?? "").toLowerCase();
      if (httpScheme === "bearer") return { type: "auth.bearer" };
      if (httpScheme === "basic") return { type: "auth.basic" };
      // An http scheme this invoker cannot itself apply (e.g. digest): surface
      // it typed by the artifact's own scheme name (R2.c ruling), never drop it.
      return { type: httpScheme ? `auth.http.${httpScheme}` : "auth.http" };
    }
    case "apiKey":
      return { type: "auth.apiKey" };
    case "openIdConnect": {
      // OpenID Connect resolves to an OAuth2 access token. The discovery URL
      // lets a resolver fetch the authorize/token endpoints. No flows are
      // declared on an openIdConnect scheme, so no grantType is selected.
      const req: ContextRequirement = { type: "auth.oauth2" };
      if (scheme.openIdConnectUrl) {
        req.openIdConnectUrl = absolutize(scheme.openIdConnectUrl, baseURL);
      }
      req.scopes = [...requiredScopes];
      return req;
    }
    default:
      // Any other artifact type (e.g. mutualTLS): surface it verbatim as
      // "auth.<type>" (R2.c ruling) rather than dropping the alternative.
      return { type: `auth.${scheme.type}` };
  }
}

/**
 * Builds one `auth.oauth2` requirement per usable declared flow. `scopes`
 * carries the scopes required by the Security Requirement Object—not every
 * scope the flow happens to advertise. Canonical ordering is deterministic
 * only; it does not collapse the artifact's flow alternatives into a policy
 * preference. A malformed/empty flow set remains discoverable as a bare
 * OAuth requirement so an already-acquired access token can still satisfy it.
 */
function oauth2Requirements(
  scheme: OpenAPISecurityScheme,
  baseURL: string,
  requiredScopes: string[],
): ContextRequirement[] {
  const flows = scheme.flows;
  const candidates: Array<[string, OpenAPIOAuthFlow | undefined]> = [
    ["authorization_code", flows?.authorizationCode],
    ["implicit", flows?.implicit],
    ["password", flows?.password],
    ["client_credentials", flows?.clientCredentials],
  ];
  const requirements: ContextRequirement[] = [];
  for (const [grantType, flow] of candidates) {
    if (!flow || !oauthFlowUsable(grantType, flow, requiredScopes)) continue;
    const req: ContextRequirement = {
      type: "auth.oauth2",
      scopes: [...requiredScopes],
      grantType,
    };
    if (flow.authorizationUrl) req.authorizeUrl = absolutize(flow.authorizationUrl, baseURL);
    if (flow.tokenUrl) req.tokenUrl = absolutize(flow.tokenUrl, baseURL);
    requirements.push(req);
  }
  return requirements.length > 0
    ? requirements
    : [{ type: "auth.oauth2", scopes: [...requiredScopes] }];
}

function oauthFlowUsable(
  grantType: string,
  flow: OpenAPIOAuthFlow,
  requiredScopes: string[],
): boolean {
  if (grantType === "authorization_code" && (!flow.authorizationUrl || !flow.tokenUrl)) return false;
  if (grantType === "implicit" && !flow.authorizationUrl) return false;
  if ((grantType === "password" || grantType === "client_credentials") && !flow.tokenUrl) return false;
  const available = flow.scopes ?? {};
  return requiredScopes.every((scope) => Object.hasOwn(available, scope));
}

/** Resolves a possibly-relative URL against the server base; passes absolute URLs through. */
function absolutize(url: string, baseURL: string): string {
  try {
    return new URL(url, baseURL).toString();
  } catch {
    return url;
  }
}

// ---------------------------------------------------------------------------
// Credentials and channel assembly (§9.6: OAPI-P-09 wire application,
// OAPI-P-10 channel assembly)
// ---------------------------------------------------------------------------

/**
 * One credential's wire application: which channel it rides (header,
 * query, or cookie) under which name.
 */
export type CredentialPlacement = OpenAPICredentialPlacement;

/** A securityScheme paired with its addressable name (the securitySchemes key). */
interface NamedSecurityScheme {
  scheme: OpenAPISecurityScheme;
  name: string;
}

/**
 * Derives the credential wire applications for an operation from the
 * artifact's security declarations (read at invocation time, never
 * extracted into the OBI) and the supplied context (OAPI-P-09): an apiKey
 * scheme's credential rides its declared in/name; http basic and bearer,
 * oauth2, and openIdConnect ride the Authorization header. Exactly one
 * complete, satisfiable, channel-safe Security Requirement alternative is
 * selected; credentials from separate OR alternatives are never unioned.
 * Duplicate destinations are deliberately retained so OAPI-P-10 can refuse
 * two ANDed schemes instead of silently deduplicating them.
 */
export function credentialPlacements(
  doc: OpenAPIDocument,
  op: OpenAPIOperation,
  ctx: Record<string, unknown> | undefined,
  baseURL: string,
  params: OpenAPIParameter[],
): CredentialPlacement[] {
  const plan = selectedSecurityPlan(doc, op, ctx, baseURL, params);
  if (!plan || plan.context.requirements.length === 0 || !ctx) return [];
  return credentialValues(plan, ctx);
}

/**
 * Selects the one Security Requirement alternative whose credentials will be
 * applied, or undefined when none will be. Both the dispatch path (channel
 * assembly) and {@link credentialPlacements} funnel through here, so it is the
 * single point at which an invocation decides to put a credential on the wire.
 */
function selectedSecurityPlan(
  doc: OpenAPIDocument,
  op: OpenAPIOperation,
  ctx: Record<string, unknown> | undefined,
  baseURL: string,
  params: OpenAPIParameter[],
): SecurityPlan | undefined {
  const plans = viableSecurityPlans(doc, op, baseURL, params);
  if (!plans) return undefined;
  return plans.find((candidate) =>
    candidate.context.requirements.length === 0
      || (!!ctx && securityPlanSatisfied(candidate, ctx, baseURL)),
  );
}

function securityPlanSatisfied(
  plan: SecurityPlan,
  ctx: Record<string, unknown>,
  target: string,
): boolean {
  const custom = ctx["$openapiSecurity"];
  const handled = custom && typeof custom === "object" && !Array.isArray(custom)
    ? custom as Record<string, unknown>
    : {};
  return plan.context.requirements.every((requirement) =>
    (typeof requirement.name === "string" && handled[requirement.name] === true)
    || contextSatisfies(ctx, {
      target,
      alternatives: [{ requirements: [requirement] }],
    }),
  );
}

function credentialValues(plan: SecurityPlan, ctx: Record<string, unknown>): CredentialPlacement[] {
  return buildOpenAPICredentialPlacements(plan.schemes, {
    apiKey: (name) => contextApiKeyFor(ctx, name),
    basic: (name) => contextBasicAuthFor(ctx, name) ?? undefined,
    bearer: (name) => contextBearerTokenFor(ctx, name),
    accessToken: (name) => contextAccessTokenFor(ctx, name),
  });
}

/** Wire destinations for collision analysis before credential values exist. */
function credentialDestinations(plan: SecurityPlan): CredentialPlacement[] {
  return openAPICredentialDestinations(plan.schemes);
}

/**
 * Artifact-only OAPI-P-10 ownership checks that make an operation
 * unresolvable regardless of caller input or credential selection.
 */
export function parameterOwnershipConflict(params: OpenAPIParameter[]): string {
  const headers = params
    .filter((parameter) => parameter.in === "header" && !!parameter.name)
    .map((parameter) => parameter.name!.toLowerCase());
  for (const name of headers) {
    if (name === "host" || name === "content-length") {
      return `effective header parameter "${name}" collides with a processor-owned request field (OAPI-P-10)`;
    }
  }
  return "";
}

/**
 * Enforces §9.3 (OAPI-P-05): the target URL is the resolved server joined
 * with the operation's path template, so a template variable that no declared
 * path parameter can supply leaves no target to address and refuses before
 * dispatch — the same ground §9.1 states for the neighbouring case of a
 * declared path parameter the caller omitted ("the URL cannot be built").
 * Every accepted OAS edition requires a path template variable to have a
 * corresponding `in: path` Parameter Object with `required: true`, so this
 * reaches only artifacts that violate that requirement. Emitting the
 * unsubstituted template instead would percent-encode the braces and put a
 * meaningless segment on a live service.
 *
 * The predicate is the exact inverse of the substitution routeParameter
 * performs (replaceAll of `{` + name + `}`): an expression is addressable iff
 * a declared path parameter's name equals the enclosed text. It is
 * declaration-only — independent of any invocation input — and so is checked
 * before input consumption. Empty string means addressable.
 */
export function pathTemplateAddressabilityConflict(
  pathTemplate: string,
  params: OpenAPIParameter[],
): string {
  const declared = new Set(
    params.filter((parameter) => parameter.in === "path" && !!parameter.name)
      .map((parameter) => parameter.name!),
  );
  const unaddressable: string[] = [];
  for (const name of pathTemplateVariables(pathTemplate)) {
    if (declared.has(name) || unaddressable.includes(name)) continue;
    unaddressable.push(name);
  }
  if (unaddressable.length === 0) return "";
  unaddressable.sort(codePointCompare);
  return `path template variable(s) ${unaddressable.join(", ")} have no declared path parameter: `
    + "the target URL cannot be built (OAPI-P-05: unresolvable target)";
}

/**
 * The names of the brace-delimited expressions in a path template, in order.
 * An unclosed `{` delimits nothing and is an ordinary literal; an inner `{`
 * restarts the expression, matching the innermost pair the substitution would
 * otherwise have to match.
 */
function pathTemplateVariables(pathTemplate: string): string[] {
  const names: string[] = [];
  let open = -1;
  for (let index = 0; index < pathTemplate.length; index += 1) {
    const char = pathTemplate[index];
    if (char === "{") open = index;
    else if (char === "}" && open >= 0) {
      names.push(pathTemplate.slice(open + 1, index));
      open = -1;
    }
  }
  return names;
}

/**
 * Context transport hints are a separate request source. A raw Cookie hint
 * is admissible only when no artifact or context source contributes a
 * structured cookie and no declared raw Cookie parameter owns the header.
 */
function contextChannelCollision(
  ctx: Record<string, unknown> | undefined,
  placements: CredentialPlacement[],
  populated: { header: Set<string>; query: Set<string>; cookie: Set<string> },
): string {
  const rawCookieHints = Object.keys(contextHeaders(ctx)).filter(
    (name) => name.toLowerCase() === "cookie",
  );
  const hasRawCookieOwner = populated.header.has("cookie") || placements.some(
    (placement) => placement.channel === "header" && placement.name.toLowerCase() === "cookie",
  );
  const hasStructuredCookie = populated.cookie.size > 0
    || placements.some((placement) => placement.channel === "cookie")
    || Object.keys(contextCookies(ctx)).length > 0;

  if (rawCookieHints.length > 0 && (hasRawCookieOwner || hasStructuredCookie)) {
    return "raw Cookie context header collides with another raw or structured cookie source (OAPI-P-10: refused before dispatch, never a silent overwrite)";
  }
  if (hasRawCookieOwner && Object.keys(contextCookies(ctx)).length > 0) {
    return "raw Cookie header source collides with structured context cookies (OAPI-P-10: refused before dispatch, never a silent overwrite)";
  }
  return "";
}

/**
 * The OAPI-P-10 refusal: a name collision between a credential and a
 * caller-populated declared parameter on the same channel is refused
 * before dispatch — loud, never a silent overwrite in either direction.
 * Header names compare case-insensitively. Returns the refusal message, or
 * "" when the channels are collision-free.
 */
export function credentialCollision(
  placements: CredentialPlacement[],
  params: OpenAPIParameter[],
  populated: { header: Set<string>; query: Set<string>; cookie: Set<string> },
): string {
  return openAPICredentialCollision(placements, params, populated);
}

// ---------------------------------------------------------------------------
// Response reading
// ---------------------------------------------------------------------------

async function peekResponseBody(
  response: Response,
): Promise<{ empty: boolean; response: Response }> {
  if (!response.body) return { empty: true, response };
  const reader = response.body.getReader();
  let first: Uint8Array;
  for (;;) {
    const chunk = await reader.read();
    if (chunk.done) {
      reader.releaseLock();
      return {
        empty: true,
        response: new Response(null, {
          status: response.status,
          statusText: response.statusText,
          headers: response.headers,
        }),
      };
    }
    if (chunk.value.length > 0) {
      first = chunk.value;
      break;
    }
  }
  let replayedFirst = false;
  const replay = new ReadableStream<Uint8Array>({
    async pull(controller) {
      if (!replayedFirst) {
        replayedFirst = true;
        controller.enqueue(first);
        return;
      }
      try {
        const chunk = await reader.read();
        if (chunk.done) {
          controller.close();
          reader.releaseLock();
          return;
        }
        controller.enqueue(chunk.value);
      } catch (error: unknown) {
        controller.error(error);
        reader.releaseLock();
      }
    },
    async cancel(reason) {
      await reader.cancel(reason);
      reader.releaseLock();
    },
  });
  return {
    empty: false,
    response: new Response(replay, {
      status: response.status,
      statusText: response.statusText,
      headers: response.headers,
    }),
  };
}

async function readResponseBytes(resp: Response, maxBytes: number): Promise<Uint8Array> {
  if (!resp.body) {
    return new Uint8Array(await resp.arrayBuffer());
  }

  const reader = resp.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;

  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > maxBytes) {
        // Cancel the body stream before bailing; releasing the lock alone
        // leaves the response socket pinned on the remaining bytes.
        await reader.cancel().catch(() => {});
        throw new Error(`response exceeds ${maxBytes} byte limit`);
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }

  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}

// ---------------------------------------------------------------------------
// Preflight support (prepareBinding)
// ---------------------------------------------------------------------------

/**
 * Resolves the pieces prepareBinding needs from a loaded document: the
 * addressed operation and its resolved base URL. Returns null when the ref
 * does not resolve or the server cannot (the invocation surfaces those as
 * its own pre-dispatch refusals; there is no context to report).
 */
export function preflightTarget(
  doc: OpenAPIDocument,
  ref: string,
  ctx: Record<string, unknown> | undefined,
  sourceLocation: string | undefined,
): { op: OpenAPIOperation; params: OpenAPIParameter[]; baseURL: string } | null {
  let path: string, method: string;
  try {
    ({ path, method } = parseRef(ref));
  } catch {
    return null;
  }
  const pathItem = doc.paths?.[path];
  const op = pathItem?.[method] as OpenAPIOperation | undefined;
  if (!pathItem || !op) return null;
  try {
    const params = effectiveParameters(pathItem, op);
    if (parameterOwnershipConflict(params) !== "") return null;
    if (securityConfigurationError(doc, op) !== "") return null;
    return { op, params, baseURL: resolveServer(doc, pathItem, op, ctx, sourceLocation) };
  } catch {
    return null;
  }
}
