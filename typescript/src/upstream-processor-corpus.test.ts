import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  OpenAPIClient,
  OpenAPIClientError,
  type OpenAPICharacterDecoder,
  type OpenAPICharacterEncoder,
  type OpenAPICallInput,
  type OpenAPICallOptions,
  type OpenAPIAuthValue,
  type OpenAPIClientOptions,
  type OpenAPIContentCodec,
  type OpenAPIHostTransport,
} from "./index.js";

type ObjectValue = Record<string, unknown>;

interface ScenarioFile {
  family: string;
  scenarios: Scenario[];
}

interface Scenario {
  id: string;
  given: {
    source: ObjectValue;
    binding: ObjectValue;
    configuration?: ObjectValue;
    invocation: ObjectValue & { inputPresent: boolean };
    peer?: ObjectValue;
    runtime?: ObjectValue;
    resources?: ObjectValue;
  };
  expected: Expected[];
}

interface Expected {
  disposition: string;
  phase: string;
  assertions: Assertion[];
}

interface Assertion extends ObjectValue {
  path: string;
}

interface Observation {
  disposition: string;
  phase: string;
  data: ObjectValue;
}

const corpusEnabled = process.env.OPENAPI_UPSTREAM_CORPUS === "1";
const corpusRoot = resolve(
  process.env.OPENAPI_UPSTREAM_CORPUS_ROOT
    ?? resolve(import.meta.dirname, "../../conformance/upstream/openbindings-0.2/processor"),
);
const corpora = ["openapi-2.0", "openapi-3.0", "openapi-3.1", "openapi-3.2"].map((family) =>
  JSON.parse(readFileSync(resolve(corpusRoot, `${family}.json`), "utf8")) as ScenarioFile,
);

describe.runIf(corpusEnabled)("pinned OpenBindings OpenAPI processor corpus", () => {
  for (const corpus of corpora) {
    describe(corpus.family, () => {
      for (const scenario of corpus.scenarios) {
        it(scenario.id, async () => {
          const observation = await runScenario(scenario, corpus.family);
          const failures: string[] = [];
          for (const alternative of scenario.expected) {
            try {
              matchAlternative(observation, alternative);
              return;
            } catch (error: unknown) {
              failures.push(error instanceof Error ? error.message : String(error));
            }
          }
          throw new Error(
            `${scenario.id} matched no expected alternative:\n${failures.join("\n---\n")}\nobservation: ${JSON.stringify(observation)}`,
          );
        });
      }
    });
  }
});

async function runScenario(scenario: Scenario, family: string): Promise<Observation> {
  const dispatches: ObjectValue[] = [];
  const outputs: unknown[] = [];
  const transport = scenarioTransport(scenario, dispatches);
  const source = scenario.given.source;
  const content = Object.hasOwn(source, "content") ? source.content : undefined;
  const location = typeof source.location === "string" ? source.location : undefined;
  let client: OpenAPIClient;
  try {
    client = await OpenAPIClient.load(
      { ...(location ? { location } : {}), ...(content !== undefined ? { content } : {}) },
      {
        documentFetch: transport,
        fetch: transport,
        transport: scenarioHostTransport(scenario, dispatches),
      },
    );
    const expectedFamily = {
      "openapi-2.0": "2.0",
      "openapi-3.0": "3.0",
      "openapi-3.1": "3.1",
      "openapi-3.2": "3.2",
    }[family];
    if (client.edition !== expectedFamily && !client.edition.startsWith(`${expectedFamily}.`)) {
      throw new Error(`artifact edition ${JSON.stringify(client.edition)} does not match binding family ${JSON.stringify(expectedFamily)}`);
    }
  } catch (error: unknown) {
    return terminalObservation(error, "load", dispatches);
  }

  const selector = scenario.given.binding.selector;
  if (typeof selector !== "string") {
    return terminalObservation(new Error("scenario selector is absent"), "resolution", dispatches);
  }
  try {
    client.operation({ ref: selector });
  } catch (error: unknown) {
    return terminalObservation(error, "resolution", dispatches);
  }

  try {
    const input = materializeInput(scenario, content) as OpenAPICallInput;
    const options = scenarioOptions(scenario);
    const result = await client.stream({ ref: selector }, input, options);
    if (!result.ok) {
      const data: ObjectValue = { outputs };
      attachDispatches(data, dispatches);
      return { disposition: "error", phase: "response", data };
    }
    for await (const event of result.events) {
      outputs.push(normalizeOutput(event.sse ? { ...event.sse, data: event.data } : event.data));
    }
    await result.closed;
    const data: ObjectValue = { outputs };
    attachDispatches(data, dispatches);
    return { disposition: "complete", phase: "completion", data };
  } catch (error: unknown) {
    return terminalObservation(error, dispatches.length > 0 ? "response" : "pre-dispatch", dispatches, outputs);
  }
}

function scenarioOptions(scenario: Scenario): OpenAPICallOptions {
  const configuration = scenario.given.configuration ?? {};
  const runtime = scenario.given.runtime ?? {};
  const options: OpenAPICallOptions = {};
  const server = configuration.server;
  if (typeof server === "string") options.server = { url: server };
  else if (isObject(server) && typeof server.baseUrl === "string") options.server = { url: server.baseUrl };
  else if (isObject(server) && typeof server.index === "number") {
    options.server = {
      index: server.index,
      ...(isObject(server.variables) ? { variables: stringRecord(server.variables) } : {}),
    };
  } else if (isObject(server) && isObject(server.variables)) {
    options.server = { variables: stringRecord(server.variables) };
  }
  if (isObject(configuration.security) && typeof configuration.security.index === "number") {
    options.securityAlternative = configuration.security.index;
  }
  if (configuration.implicitConnectionScope === "entry" || configuration.implicitConnectionScope === "referring") {
    options.implicitConnectionScope = configuration.implicitConnectionScope;
  }
  if (typeof configuration.emptyValueForm === "string") {
    options.emptyValueForm = configuration.emptyValueForm as OpenAPICallOptions["emptyValueForm"];
  }
  if (isObject(configuration.parameterConversion)) {
    options.parameterConverter = (value) => {
      const key = JSON.stringify(value);
      const converted = configuration.parameterConversion as ObjectValue;
      if (typeof converted[key] !== "string") throw new Error(`parameterConversion has no result for ${key}`);
      return converted[key];
    };
  }
  if (typeof runtime.redirectPolicy === "string") {
    options.redirect = runtime.redirectPolicy === "follow" || runtime.redirectPolicy === "ordinary-user-agent"
      ? "follow"
      : "manual";
  }
  if (typeof runtime.maxDeliveryUnitBytes === "number") options.maxDeliveryUnitBytes = runtime.maxDeliveryUnitBytes;
  if (isObject(runtime.credentials)) options.auth = credentialValues(runtime.credentials);
  options.requestContentCodings = scenarioCodecs(runtime.requestContentCodings);
  options.responseContentCodings = scenarioCodecs(runtime.responseContentCodings);
  options.requestCharacterEncodings = scenarioCharacterEncoders(runtime.requestCharacterEncodings);
  options.responseCharacterEncodings = scenarioCharacterDecoders(runtime.responseCharacterEncodings);
  return options;
}

function materializeInput(scenario: Scenario, content: unknown): unknown {
  const invocation = scenario.given.invocation;
  const abstract = structuredClone(Object.hasOwn(invocation, "input") ? invocation.input : {});
  if (!isObject(abstract)) return abstract;
  for (const raw of Array.isArray(invocation.inputMaterializations) ? invocation.inputMaterializations : []) {
    if (!isObject(raw) || typeof raw.path !== "string" || !Array.isArray(raw.codeUnits)) continue;
    setPointer(abstract, raw.path, String.fromCharCode(...raw.codeUnits.map(Number)));
  }
  const input = nativeScenarioInput(abstract, content, scenario.given.binding.selector);
  const configuration = scenario.given.configuration ?? {};
  if (
    typeof abstract.body === "string"
    && isOAS3RawRequest(content, scenario.given.binding.selector, configuration.requestMedia)
  ) {
    // The binding corpus crosses raw-octet positions as canonical Base64. The
    // standalone client deliberately exposes the more natural bytes boundary.
    input.body = base64ToBytes(abstract.body);
  }
  if (typeof configuration.requestMedia === "string") input.mediaType = configuration.requestMedia;
  if (isObject(configuration.propertyMedia)) input.propertyMediaTypes = { ...configuration.propertyMedia };
  return input;
}

function nativeScenarioInput(abstract: ObjectValue, content: unknown, selector: unknown): ObjectValue {
  const result: ObjectValue = {};
  if (Object.hasOwn(abstract, "body")) result.body = abstract.body;
  for (const [name, value] of Object.entries(abstract)) {
    if (name !== "parameters" && name !== "body") result[name] = value;
  }
  if (Object.hasOwn(abstract, "parameters") && !isObject(abstract.parameters)) {
    result.parameters = abstract.parameters;
    return result;
  }
  const rawParameters = isObject(abstract.parameters) ? abstract.parameters : {};
  const declarations = operationParameters(content, selector);
  const locationsByName = new Map<string, Set<string>>();
  for (const declaration of declarations) {
    const locations = locationsByName.get(declaration.name) ?? new Set<string>();
    locations.add(declaration.in);
    locationsByName.set(declaration.name, locations);
  }
  const qualified = [...locationsByName.values()].some((locations) => locations.size > 1);
  const grouped: Record<string, ObjectValue> = {};
  const formData: ObjectValue = {};
  for (const [field, value] of Object.entries(rawParameters)) {
    const slash = field.indexOf("/");
    const qualifiedLocation = slash > 0 ? field.slice(0, slash) : undefined;
    if (qualifiedLocation !== undefined && !qualified) throw new Error(`unknown qualified caller key ${JSON.stringify(field)}`);
    const name = (slash > 0 ? field.slice(slash + 1) : field)
      .replace(/~1/gu, "/")
      .replace(/~0/gu, "~");
    const matches = declarations.filter((parameter) =>
      parameter.name === name && (qualifiedLocation === undefined || parameter.in === qualifiedLocation));
    const parameter = matches.length === 1 ? matches[0] : undefined;
    const location = parameter?.in;
    if (location === "formData") {
      formData[name] = value;
      continue;
    }
    const nativeLocation = location === "querystring" ? "querystring" : location;
    if (nativeLocation && ["path", "query", "querystring", "header", "cookie"].includes(nativeLocation)) {
      (grouped[nativeLocation] ??= {})[name] = value;
      continue;
    }
    // Preserve an unresolved flattened field as an explicit unknown query
    // input so the native client refuses rather than silently dropping it.
    (grouped.query ??= {})[name] = value;
  }
  if (Object.keys(formData).length > 0) result.body = formData;
  if (Object.keys(grouped).length > 0) result.parameters = grouped;
  return result;
}

function operationParameters(content: unknown, selector: unknown): Array<{ name: string; in: string; required: boolean }> {
  const located = operationObject(content, selector);
  if (!located) return [];
  const { item, operation } = located;
  const byIdentity = new Map<string, { name: string; in: string; required: boolean }>();
  for (const raw of [...(Array.isArray(item.parameters) ? item.parameters : []), ...(Array.isArray(operation.parameters) ? operation.parameters : [])]) {
    if (!isObject(raw) || typeof raw.name !== "string" || typeof raw.in !== "string") continue;
    if (raw.in === "header" && raw.required !== true && !httpToken(raw.name)) continue;
    byIdentity.set(`${raw.in}\0${raw.name}`, { name: raw.name, in: raw.in, required: raw.required === true });
  }
  return [...byIdentity.values()];
}

function operationObject(content: unknown, selector: unknown): { item: ObjectValue; operation: ObjectValue } | undefined {
  if (!isObject(content) || typeof selector !== "string") return undefined;
  const match = /^#\/paths\/([^/]+)\/(?:additionalOperations\/)?([^/]+)$/u.exec(selector);
  if (!match) return undefined;
  const path = match[1]!.replace(/~1/gu, "/").replace(/~0/gu, "~");
  const method = match[2]!.replace(/~1/gu, "/").replace(/~0/gu, "~");
  const paths = isObject(content.paths) ? content.paths : {};
  const item = isObject(paths[path]) ? paths[path] : {};
  const additional = isObject(item.additionalOperations) ? item.additionalOperations : {};
  const operation = isObject(additional[method]) ? additional[method] : isObject(item[method]) ? item[method] : {};
  return { item, operation };
}

function isOAS3RawRequest(content: unknown, selector: unknown, configuredMedia: unknown): boolean {
  if (!isObject(content) || typeof content.openapi !== "string" || !content.openapi.startsWith("3.")) return false;
  const located = operationObject(content, selector);
  if (!located || !isObject(located.operation.requestBody)) return false;
  const requestContent = isObject(located.operation.requestBody.content) ? located.operation.requestBody.content : {};
  const declared = Object.keys(requestContent);
  const media = typeof configuredMedia === "string"
    ? configuredMedia
    : declared.length === 1 && !declared[0]!.includes("*") ? declared[0]! : undefined;
  if (!media || isJSONMedia(media) || isFormMedia(media)) return false;
  const mediaObject = requestContent[media];
  if (!isObject(mediaObject)) return false;
  if (!Object.hasOwn(mediaObject, "schema")) return true;
  const signature = schemaSignature(mediaObject.schema, content, new Set());
  return !signature.typed || signature.types.size === 1
    && signature.types.has("string")
    && signature.formats.size === 1
    && signature.formats.has("binary");
}

function schemaSignature(
  value: unknown,
  root: ObjectValue,
  active: Set<string>,
): { typed: boolean; types: Set<string>; formats: Set<string> } {
  if (!isObject(value)) return { typed: false, types: new Set(), formats: new Set() };
  if (typeof value.$ref === "string" && value.$ref.startsWith("#/")) {
    if (active.has(value.$ref)) return { typed: false, types: new Set(), formats: new Set() };
    active.add(value.$ref);
    const resolved = selectPointer(root, value.$ref.slice(1));
    const signature = schemaSignature(resolved.value, root, active);
    active.delete(value.$ref);
    return signature;
  }
  let types = new Set<string>();
  let typed = false;
  if (typeof value.type === "string") {
    typed = true;
    types.add(value.type);
  } else if (Array.isArray(value.type) && value.type.every((type) => typeof type === "string")) {
    typed = true;
    types = new Set(value.type as string[]);
  }
  let formats = typeof value.format === "string" ? new Set([value.format]) : new Set<string>();
  for (const branch of Array.isArray(value.allOf) ? value.allOf : []) {
    const nested = schemaSignature(branch, root, active);
    if (nested.typed) {
      types = typed ? new Set([...types].filter((type) => nested.types.has(type))) : nested.types;
      typed = true;
    }
    if (nested.formats.size > 0) {
      formats = formats.size > 0
        ? new Set([...formats].filter((format) => nested.formats.has(format)))
        : nested.formats;
    }
  }
  return { typed, types, formats };
}

function isJSONMedia(value: string): boolean {
  const base = value.split(";", 1)[0]!.trim().toLowerCase();
  return base === "application/json" || base.endsWith("+json");
}

function httpToken(value: string): boolean {
  return /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/u.test(value);
}

function isFormMedia(value: string): boolean {
  const base = value.split(";", 1)[0]!.trim().toLowerCase();
  return base === "application/x-www-form-urlencoded" || base === "multipart/form-data";
}

function scenarioTransport(scenario: Scenario, dispatches: ObjectValue[]): typeof fetch {
  let responseIndex = 0;
  return async (input, init) => {
    const plannedURL = input instanceof Request ? input.url : String(input);
    const request = new Request(input, init);
    const resource = scenario.given.resources?.[request.url];
    if (resource !== undefined) {
      return new Response(typeof resource === "string" ? resource : JSON.stringify(resource), { status: 200 });
    }
    const source = scenario.given.source;
    if (!Object.hasOwn(source, "content") && source.location === request.url) {
      const entry = scenario.given.resources?.[request.url];
      if (entry === undefined) throw new Error(`scenario provides no entry resource for ${request.url}`);
    }
    const dispatch = await normalizedDispatch(request);
    dispatch.url = plannedURL;
    dispatches.push(dispatch);
    const peer = scenario.given.peer ?? {};
    const responses = Array.isArray(peer.responses) ? peer.responses.filter(isObject) : [peer];
    let selected = responses[Math.min(responseIndex, responses.length - 1)] ?? {};
    responseIndex += 1;
    if (request.redirect === "follow" && isRedirect(selected.status) && responseIndex < responses.length) {
      const location = isObject(selected.headers) && typeof selected.headers.Location === "string"
        ? selected.headers.Location
        : isObject(selected.headers) && typeof selected.headers.location === "string"
          ? selected.headers.location
          : undefined;
      if (location) {
        const next = new Request(new URL(location, request.url), {
          method: request.method,
          headers: redirectedHeaders(
            request.headers,
            request.url,
            new URL(location, request.url).toString(),
            credentialHeaderNames(scenario.given.source.content),
          ),
          body: request.body,
          redirect: "follow",
        });
        dispatches.push(await normalizedDispatch(next));
        selected = responses[Math.min(responseIndex, responses.length - 1)] ?? selected;
        responseIndex += 1;
      }
    }
    const status = typeof selected.status === "number" ? selected.status : 599;
    const headers = isObject(selected.headers) ? stringRecord(selected.headers) : {};
    const bytes = typeof selected.bodyBase64 === "string"
      ? Uint8Array.from(atob(selected.bodyBase64), (character) => character.charCodeAt(0))
      : typeof selected.body === "string"
        ? new TextEncoder().encode(selected.body)
        : new Uint8Array();
    const body = bytes.byteLength === 0 && [101, 204, 205, 304].includes(status) ? null : bytes;
    return peerResponse(body, status, headers);
  };
}

function peerResponse(body: BodyInit | null, status: number, headers: HeadersInit): Response {
  const constructorAccepts = status >= 200
    && status <= 599
    && !(body !== null && [204, 205, 304].includes(status));
  const response = new Response(body, { status: constructorAccepts ? status : 200, headers });
  if (!constructorAccepts) {
    Object.defineProperties(response, {
      status: { configurable: true, value: status },
      ok: { configurable: true, value: status >= 200 && status <= 299 },
    });
  }
  return response;
}

function scenarioHostTransport(scenario: Scenario, dispatches: ObjectValue[]): OpenAPIHostTransport {
  let responseIndex = 0;
  return async (url, request) => {
    const bytes = request.body == null
      ? new Uint8Array()
      : new Uint8Array(await new Response(request.body).arrayBuffer());
    const peer = scenario.given.peer ?? {};
    const responses = Array.isArray(peer.responses) ? peer.responses.filter(isObject) : [peer];
    let currentURL = url;
    let currentHeaders = new Headers(request.headers);
    for (;;) {
      const dispatch: ObjectValue = {
        method: request.method,
        url: currentURL,
        headers: normalizedHeaders(currentHeaders),
      };
      if (bytes.byteLength > 0) {
        const text = new TextDecoder().decode(bytes);
        const parsed = parseJSON(text);
        dispatch.body = parsed.parsed ? parsed.value : text;
        dispatch.bodyBase64 = bytesToBase64(bytes);
        dispatch.bodyByteLength = bytes.byteLength;
        dispatch.byteLength = bytes.byteLength;
      }
      dispatches.push(dispatch);
      const selected = responses[Math.min(responseIndex, responses.length - 1)] ?? {};
      responseIndex += 1;
      const location = isObject(selected.headers) && typeof selected.headers.Location === "string"
        ? selected.headers.Location
        : isObject(selected.headers) && typeof selected.headers.location === "string"
          ? selected.headers.location
          : undefined;
      if (request.redirect === "follow" && isRedirect(selected.status) && location && responseIndex < responses.length) {
        const nextURL = new URL(location, currentURL).toString();
        currentHeaders = redirectedHeaders(
          currentHeaders,
          currentURL,
          nextURL,
          credentialHeaderNames(scenario.given.source.content),
        );
        currentURL = nextURL;
        continue;
      }
      const status = typeof selected.status === "number" ? selected.status : 599;
      const responseHeaders = isObject(selected.headers) ? stringRecord(selected.headers) : {};
      const responseBytes = typeof selected.bodyBase64 === "string"
        ? Uint8Array.from(atob(selected.bodyBase64), (character) => character.charCodeAt(0))
        : typeof selected.body === "string"
          ? new TextEncoder().encode(selected.body)
          : new Uint8Array();
      const body = responseBytes.byteLength === 0 && [101, 204, 205, 304].includes(status)
        ? null
        : responseBytes;
      return peerResponse(body, status, responseHeaders);
    }
  };
}

async function normalizedDispatch(request: Request): Promise<ObjectValue> {
  const headers = normalizedHeaders(request.headers);
  const bytes = new Uint8Array(await request.clone().arrayBuffer());
  const dispatch: ObjectValue = { method: request.method, url: request.url, headers };
  if (bytes.byteLength > 0) {
    const text = new TextDecoder().decode(bytes);
    const parsed = parseJSON(text);
    dispatch.body = parsed.parsed ? parsed.value : text;
    dispatch.bodyBase64 = bytesToBase64(bytes);
    dispatch.bodyByteLength = bytes.byteLength;
    dispatch.byteLength = bytes.byteLength;
  }
  return dispatch;
}

function terminalObservation(
  error: unknown,
  phase: string,
  dispatches: ObjectValue[],
  outputs: unknown[] = [],
): Observation {
  const clientError = error instanceof OpenAPIClientError ? error : undefined;
  const contextRequired = clientError?.code === "CONFIGURATION_REQUIRED";
  const data: ObjectValue = {
    outputs,
    error: {
      code: dispatches.length > 0 ? "ERR_EXECUTION_FAILED" : "ERR_REFUSED",
      message: error instanceof Error ? error.message : String(error),
    },
  };
  attachDispatches(data, dispatches);
  return {
    disposition: contextRequired ? "context-required" : dispatches.length > 0 ? "error" : "refusal",
    phase,
    data,
  };
}

function attachDispatches(data: ObjectValue, dispatches: ObjectValue[]): void {
  if (dispatches.length > 0) {
    data.dispatch = dispatches[0];
    data.dispatches = dispatches;
  }
}

function matchAlternative(observation: Observation, expected: Expected): void {
  expect(observation.disposition).toBe(expected.disposition);
  expect(observation.phase).toBe(expected.phase);
  for (const assertion of expected.assertions) {
    const selected = selectPointer(observation.data, assertion.path);
    if (assertion.absent === true) {
      expect(selected.present, assertion.path).toBe(false);
    } else if (Object.hasOwn(assertion, "equals")) {
      expect(selected.value, assertion.path).toEqual(assertion.equals);
    } else if (Array.isArray(assertion.oneOf)) {
      expect(assertion.oneOf, assertion.path).toContainEqual(selected.value);
    } else if (Array.isArray(assertion.setEquals)) {
      expect([...new Set(selected.value as unknown[])].sort(), assertion.path).toEqual([...assertion.setEquals].sort());
    } else if (Object.hasOwn(assertion, "contains")) {
      expect(selected.value as string | unknown[], assertion.path).toContain(assertion.contains);
    } else if (Object.hasOwn(assertion, "notContains")) {
      expect(selected.value as string | unknown[], assertion.path).not.toContain(assertion.notContains);
    } else if (isObject(assertion.semanticEquals)) {
      expect(semanticValue(selected.value, assertion.semanticEquals), assertion.path).toEqual(assertion.semanticEquals.value);
    }
  }
}

function semanticValue(actual: unknown, assertion: ObjectValue): unknown {
  const kind = assertion.as;
  if (kind === "json-lines") {
    if (typeof actual !== "string" || !actual.endsWith("\n")) throw new Error("invalid JSON Lines framing");
    return actual.slice(0, -1).split("\n").map((line) => JSON.parse(line) as unknown);
  }
  if (kind === "json-sequence") {
    if (typeof actual !== "string") throw new Error("invalid JSON sequence body");
    const values: unknown[] = [];
    let offset = 0;
    while (offset < actual.length) {
      if (actual.charCodeAt(offset++) !== 0x1e) throw new Error("JSON sequence frame omits RS");
      const end = actual.indexOf("\n", offset);
      if (end < 0) throw new Error("JSON sequence frame omits LF");
      values.push(JSON.parse(actual.slice(offset, end)) as unknown);
      offset = end + 1;
    }
    return values;
  }
  if (kind === "querystring-json") {
    if (typeof actual !== "string") throw new Error("querystring assertion requires a URL");
    const mark = actual.indexOf("?");
    if (mark < 0) throw new Error("URL has no query component");
    return JSON.parse(strictPercentDecode(actual.slice(mark + 1), false)) as unknown;
  }
  if (kind === "query-json-parameter") {
    if (typeof actual !== "string") throw new Error("query assertion requires a URL");
    const units = namedUnits(new URL(actual).search.slice(1), false);
    checkNames(units, assertion);
    return JSON.parse(selectedUnit(units, assertion.name).value) as unknown;
  }
  if (kind === "form-json-field") {
    if (typeof actual !== "string") throw new Error("form assertion requires a string body");
    const units = namedUnits(actual, true);
    checkNames(units, assertion);
    return JSON.parse(selectedUnit(units, assertion.name).value) as unknown;
  }
  if (kind === "multipart-json-part") {
    if (!isObject(actual)) throw new Error("multipart assertion requires dispatch");
    const headers = isObject(actual.headers) ? actual.headers : {};
    const contentType = String(headers["content-type"] ?? headers["Content-Type"] ?? "");
    const boundary = /boundary=(?:"([^"]+)"|([^;]+))/iu.exec(contentType)?.slice(1).find(Boolean);
    if (!boundary || typeof actual.body !== "string") throw new Error("invalid multipart wrapper");
    const parts = actual.body.split(`--${boundary}`).slice(1, -1).map(parsePart);
    checkNames(parts, assertion);
    const selected = selectedUnit(parts, assertion.name);
    if (selected.contentType.toLowerCase() !== "application/json") throw new Error("multipart JSON part has wrong content type");
    return JSON.parse(selected.value) as unknown;
  }
  throw new Error(`unknown semantic interpreter ${JSON.stringify(kind)}`);
}

interface NamedUnit { name: string; value: string; contentType: string }

function namedUnits(raw: string, plusAsSpace: boolean): NamedUnit[] {
  if (raw === "") return [];
  return raw.split("&").map((unit) => {
    const split = unit.indexOf("=");
    const name = split < 0 ? unit : unit.slice(0, split);
    const value = split < 0 ? "" : unit.slice(split + 1);
    return {
      name: strictPercentDecode(name, plusAsSpace),
      value: strictPercentDecode(value, plusAsSpace),
      contentType: "",
    };
  });
}

function parsePart(raw: string): NamedUnit {
  const value = raw.replace(/^\r\n/u, "");
  const split = value.indexOf("\r\n\r\n");
  if (split < 0) throw new Error("multipart part has no header boundary");
  const headerLines = value.slice(0, split).split("\r\n");
  const disposition = headerLines.find((line) => /^content-disposition:/iu.test(line)) ?? "";
  const exactName = /; name="((?:[^"\\]|\\.)*)"$/u.exec(disposition);
  if (!exactName || /filename\*?=/iu.test(disposition)) throw new Error("multipart part name is not the exact generated form");
  const contentType = (headerLines.find((line) => /^content-type:/iu.test(line)) ?? "").slice("content-type:".length).trim();
  return {
    name: exactName[1]!.replace(/\\(["\\])/gu, "$1"),
    value: value.slice(split + 4).replace(/\r\n$/u, ""),
    contentType,
  };
}

function checkNames(units: NamedUnit[], assertion: ObjectValue): void {
  const actual = units.map((unit) => unit.name).sort();
  const expected = Array.isArray(assertion.names) ? assertion.names.map(String).sort() : [];
  expect(actual).toEqual(expected);
}

function selectedUnit(units: NamedUnit[], rawName: unknown): NamedUnit {
  const selected = units.filter((unit) => unit.name === rawName);
  if (selected.length !== 1) throw new Error(`expected exactly one ${JSON.stringify(rawName)} contribution`);
  return selected[0]!;
}

function strictPercentDecode(value: string, plusAsSpace: boolean): string {
  const normalized = plusAsSpace ? value.replace(/\+/gu, " ") : value;
  if (/%(?![0-9A-F]{2})/u.test(normalized)) throw new Error("percent triplets must use uppercase hex");
  return decodeURIComponent(normalized);
}

function selectPointer(root: unknown, pointer: string): { present: boolean; value?: unknown } {
  let value = root;
  for (const raw of pointer.slice(1).split("/")) {
    const key = raw.replace(/~1/gu, "/").replace(/~0/gu, "~");
    if (!isObject(value) && !Array.isArray(value)) return { present: false };
    if (!Object.hasOwn(value, key)) return { present: false };
    value = (value as ObjectValue)[key];
  }
  return { present: true, value };
}

function setPointer(root: ObjectValue, pointer: string, replacement: unknown): void {
  const tokens = pointer.slice(1).split("/").map((raw) => raw.replace(/~1/gu, "/").replace(/~0/gu, "~"));
  let value: unknown = root;
  for (const token of tokens.slice(0, -1)) value = (value as ObjectValue)[token];
  (value as ObjectValue)[tokens.at(-1)!] = replacement;
}

function credentialValues(credentials: ObjectValue): Record<string, OpenAPIAuthValue> {
  return Object.fromEntries(Object.entries(credentials).map(([name, value]) => {
    if (isObject(value) && typeof value.userId === "string" && typeof value.password === "string") {
      return [name, { username: value.userId, password: value.password }];
    }
    if (isObject(value) && typeof value.accessToken === "string" && (value.tokenType === undefined || value.tokenType === "Bearer")) {
      return [name, value.accessToken];
    }
    return [name, value];
  })) as Record<string, OpenAPIAuthValue>;
}

function scenarioCodecs(raw: unknown): Record<string, OpenAPIContentCodec> | undefined {
  if (!isObject(raw)) return undefined;
  const result: Record<string, OpenAPIContentCodec> = {};
  for (const [name, action] of Object.entries(raw)) {
    if (action === "unavailable") continue;
    result[name] = async (input) => {
      if (action === "fail") throw new Error("sentinel codec invoked");
      if (action === "identity") return input;
      if (action === "reverse") return Uint8Array.from(input).reverse();
      if (action === "unwrap") {
        const text = new TextDecoder().decode(input);
        const prefix = `${name.toLowerCase()}(`;
        if (!text.startsWith(prefix) || !text.endsWith(")")) throw new Error("malformed codec wrapper");
        return new TextEncoder().encode(text.slice(prefix.length, -1));
      }
      throw new Error(`unknown codec action ${JSON.stringify(action)}`);
    };
  }
  return result;
}

function scenarioCharacterEncoders(raw: unknown): Record<string, OpenAPICharacterEncoder> | undefined {
  if (!isObject(raw)) return undefined;
  const result: Record<string, OpenAPICharacterEncoder> = {};
  for (const [name, action] of Object.entries(raw)) {
    if (action === "unavailable") continue;
    result[name] = (value) => {
      if (action === "fail") throw new Error("sentinel character encoder invoked");
      if (action === "identity") return new TextEncoder().encode(value);
      throw new Error(`unknown character encoder action ${JSON.stringify(action)}`);
    };
  }
  return result;
}

function scenarioCharacterDecoders(raw: unknown): Record<string, OpenAPICharacterDecoder> | undefined {
  if (!isObject(raw)) return undefined;
  const result: Record<string, OpenAPICharacterDecoder> = {};
  for (const [name, action] of Object.entries(raw)) {
    if (action === "unavailable") continue;
    result[name] = (bytes) => {
      if (action === "fail") throw new Error("sentinel character decoder invoked");
      if (action === "identity") return new TextDecoder().decode(bytes);
      throw new Error(`unknown character decoder action ${JSON.stringify(action)}`);
    };
  }
  return result;
}

function redirectedHeaders(headers: Headers, from: string, to: string, credentials: Set<string>): Headers {
  const result = new Headers(headers);
  if (new URL(from).origin !== new URL(to).origin) {
    for (const name of credentials) result.delete(name);
  }
  return result;
}

function credentialHeaderNames(content: unknown): Set<string> {
  const result = new Set(["authorization", "cookie"]);
  if (!isObject(content)) return result;
  const definitions = isObject(content.securityDefinitions)
    ? content.securityDefinitions
    : isObject(content.components) && isObject(content.components.securitySchemes)
      ? content.components.securitySchemes
      : {};
  for (const value of Object.values(definitions)) {
    if (!isObject(value)) continue;
    if (value.type === "apiKey" && value.in === "header" && typeof value.name === "string") {
      result.add(value.name.toLowerCase());
    }
  }
  return result;
}

function isRedirect(status: unknown): boolean {
  return typeof status === "number" && [301, 302, 303, 307, 308].includes(status);
}

function normalizedHeaders(headers: Headers): ObjectValue {
  const result: ObjectValue = {};
  headers.forEach((value, name) => {
    const wire = wireHeaderValue(value);
    result[name] = wire;
    result[name.split("-").map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join("-")] = wire;
  });
  return result;
}

function stringRecord(value: ObjectValue): Record<string, string> {
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, String(item)]));
}

function parseJSON(value: string): { parsed: true; value: unknown } | { parsed: false } {
  try { return { parsed: true, value: JSON.parse(value) as unknown }; } catch { return { parsed: false }; }
}

function normalizeOutput(value: unknown): unknown {
  if (value instanceof Uint8Array) return bytesToBase64(value);
  if (value instanceof ArrayBuffer) return bytesToBase64(new Uint8Array(value));
  if (ArrayBuffer.isView(value)) {
    return bytesToBase64(new Uint8Array(value.buffer, value.byteOffset, value.byteLength));
  }
  return value;
}

function base64ToBytes(value: string): Uint8Array {
  let binary: string;
  try {
    binary = atob(value);
  } catch (error: unknown) {
    throw new Error("raw-octet input is not canonical Base64", { cause: error });
  }
  if (btoa(binary) !== value) throw new Error("raw-octet input is not canonical Base64");
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

function wireHeaderValue(value: string): string {
  if (![...value].some((character) => character.charCodeAt(0) >= 0x80)) return value;
  if ([...value].some((character) => character.charCodeAt(0) > 0xff)) return value;
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(
      Uint8Array.from(value, (character) => character.charCodeAt(0)),
    );
  } catch {
    return value;
  }
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function isObject(value: unknown): value is ObjectValue {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
