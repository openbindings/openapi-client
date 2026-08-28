import type {
  OpenAPIDocument,
  OpenAPIMediaType,
  OpenAPIOperation,
  OpenAPIParameter,
  OpenAPIPathItem,
  OpenAPIRequestBody,
} from "./types.js";
import { loadOpenAPIDocument, parseJSONOrYAML, validateDocumentAddress } from "./util.js";
import {
  OPENAPI32_FIXED_METHODS,
  OpenAPIOperationResolutionError,
  openAPI32OperationReferences,
  openAPI32OperationValue,
  parseOpenAPI32OperationReference,
  type OpenAPIOperationReference,
  type OpenAPIResolvedOperation,
} from "./openapi32-operations.js";
import { validateOpenAPI32OperationParameters } from "./openapi32-parameters.js";
import {
  openAPI32SecurityNameKind,
  openAPI32SecurityRequirementNames,
  openAPI32SecurityScheme,
  openAPI32SecuritySchemeReference,
} from "./openapi32-security.js";

export type OpenAPIEdition =
  | "3.0.0" | "3.0.1" | "3.0.2" | "3.0.3" | "3.0.4"
  | "3.1.0" | "3.1.1" | "3.1.2" | "3.2.0";

const ACCEPTED_EDITIONS = new Set<OpenAPIEdition>([
  "3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4",
  "3.1.0", "3.1.1", "3.1.2", "3.2.0",
]);

const OAS_BASE_DIALECT = "https://spec.openapis.org/oas/3.1/dialect/base";

export interface OpenAPIArtifactSource {
  location?: string;
  content?: unknown;
}

export interface OpenAPIArtifactLoadOptions {
  signal?: AbortSignal;
  fetch?: typeof globalThis.fetch;
  allowExternalRefs?: boolean;
  /** Receives the parsed entry resource before any reference is resolved. */
  onRawDocument?: (raw: unknown) => void;
}

export interface OpenAPI32Resource {
  retrievalURI?: string;
  identityURI?: string;
  self?: string;
}

export interface OpenAPIOperationDisposition {
  reference: OpenAPIOperationReference;
  target?: OpenAPIResolvedOperation;
  error?: OpenAPIOperationResolutionError;
}

interface RawResource {
  root: unknown;
  retrieval?: string;
  base?: string;
  self?: string;
  selfText?: string;
  selfPresent: boolean;
  selfError?: string;
  entry: boolean;
}

interface ResolvedRawNode {
  value: unknown;
  resource: RawResource;
  identity: string;
}

interface ResolvedPathItem {
  value: Record<string, unknown>;
  operation: unknown;
  operationOwner: RawResource;
  parameterOwner: RawResource;
  serverOwner: RawResource;
}

/**
 * One loaded OpenAPI artifact. OpenAPI 3.2 state remains artifact-local and
 * resolves only selected operation closures; the legacy whole-document
 * normalizer is never an acceptance gate for that edition.
 */
export class OpenAPIArtifact {
  readonly document: OpenAPIDocument;
  readonly edition: OpenAPIEdition;
  readonly location?: string;
  readonly refusal?: string;
  readonly sourceExclusion?: string;

  private readonly overlay?: OpenAPI32Overlay;
  private readonly operationTargets: ReadonlyMap<string, OpenAPIResolvedOperation>;

  /** @internal */
  constructor(args: {
    document: OpenAPIDocument;
    edition: OpenAPIEdition;
    location?: string;
    refusal?: string;
    sourceExclusion?: string;
    overlay?: OpenAPI32Overlay;
    operationTargets?: ReadonlyMap<string, OpenAPIResolvedOperation>;
  }) {
    this.document = args.document;
    this.edition = args.edition;
    this.location = args.location;
    this.refusal = args.refusal;
    this.sourceExclusion = args.sourceExclusion;
    this.overlay = args.overlay;
    this.operationTargets = args.operationTargets ?? new Map();
  }

  get openAPI32(): OpenAPI32Resource | undefined {
    return this.overlay?.entryInfo();
  }

  async resolveOperation(ref: string): Promise<OpenAPIResolvedOperation> {
    if (this.refusal) {
      throw new OpenAPIOperationResolutionError("excluded", this.refusal);
    }
    if (this.sourceExclusion) {
      throw new OpenAPIOperationResolutionError("excluded", this.sourceExclusion);
    }
    if (this.edition === "3.2.0") {
      parseOpenAPI32OperationReference(ref);
      const prepared = this.operationTargets.get(ref);
      if (prepared) return prepared;
      if (!this.overlay) {
        throw new OpenAPIOperationResolutionError("excluded", "OpenAPI 3.2 artifact has no raw-resource overlay");
      }
      try {
        return await this.overlay.resolveOperation(ref);
      } catch (error: unknown) {
        if (error instanceof OpenAPIOperationResolutionError) throw error;
        throw new OpenAPIOperationResolutionError("excluded", errorMessage(error), { cause: error });
      }
    }
    const { parseRef } = await import("./util.js");
    let parsed: { path: string; method: string };
    try {
      parsed = parseRef(ref);
    } catch (error: unknown) {
      throw new OpenAPIOperationResolutionError("invalid-reference", errorMessage(error), { cause: error });
    }
    const pathItem = this.document.paths?.[parsed.path];
    const operation = pathItem?.[parsed.method] as OpenAPIOperation | undefined;
    if (!pathItem || !operation) {
      throw new OpenAPIOperationResolutionError("not-found", `operation ${JSON.stringify(ref)} was not found`);
    }
    return {
      reference: {
        ref,
        path: parsed.path,
        method: parsed.method,
        additional: false,
        wireMethod: parsed.method.toUpperCase(),
      },
      document: this.document,
      pathItem,
      operation,
    };
  }

  async operationInventory(): Promise<OpenAPIOperationDisposition[]> {
    if (this.edition !== "3.2.0" || !this.overlay) {
      const result: OpenAPIOperationDisposition[] = [];
      for (const [path, pathItem] of Object.entries(this.document.paths ?? {})) {
        for (const method of OPENAPI32_FIXED_METHODS.slice(0, -1)) {
          if (!pathItem[method]) continue;
          const reference = {
            ref: `#/paths/${escapePointer(path)}/${method}`,
            path,
            method,
            additional: false,
            wireMethod: method.toUpperCase(),
          };
          result.push({ reference, target: await this.resolveOperation(reference.ref) });
        }
      }
      return result;
    }
    return this.overlay.operationInventory();
  }

  /**
   * Returns an artifact-local prepared view in which an adapter-resolved
   * operation replaces one inventory entry. The raw 3.2 resource overlay is
   * retained and neither the receiver nor the target is mutated.
   */
  withOperationTarget(target: OpenAPIResolvedOperation): OpenAPIArtifact {
    if (!target.reference.ref || !target.document || !target.pathItem || !target.operation) {
      throw new OpenAPIOperationResolutionError(
        "invalid-reference",
        "prepared operation target is incomplete",
      );
    }
    if (this.edition === "3.2.0") {
      parseOpenAPI32OperationReference(target.reference.ref);
    }
    const operationTargets = new Map(this.operationTargets);
    operationTargets.set(target.reference.ref, target);
    return new OpenAPIArtifact({
      document: this.document,
      edition: this.edition,
      ...(this.location ? { location: this.location } : {}),
      ...(this.refusal ? { refusal: this.refusal } : {}),
      ...(this.sourceExclusion ? { sourceExclusion: this.sourceExclusion } : {}),
      ...(this.overlay ? { overlay: this.overlay } : {}),
      operationTargets,
    });
  }
}

/** Applies the representation/root/exact-edition gates without resolving a reference. */
export function classifyOpenAPIEdition(root: unknown): OpenAPIEdition {
  const object = asRecord(root);
  if (!object) throw new Error("OpenAPI entry resource must be a JSON object");
  if (!Object.hasOwn(object, "openapi")) {
    throw new Error("OpenAPI entry resource has no required string `openapi` field");
  }
  if (typeof object.openapi !== "string") {
    throw new Error("OpenAPI entry resource `openapi` field must be a string");
  }
  if (!ACCEPTED_EDITIONS.has(object.openapi as OpenAPIEdition)) {
    throw new Error(`unsupported OpenAPI version ${JSON.stringify(object.openapi)}`);
  }
  return object.openapi as OpenAPIEdition;
}

/** Loads an artifact after classifying its entry image and before resolving any reference. */
export async function loadOpenAPIArtifact(
  source: OpenAPIArtifactSource,
  options: OpenAPIArtifactLoadOptions = {},
): Promise<OpenAPIArtifact> {
  if (source.location) validateDocumentAddress(source.location);
  const entry = await readEntry(source, options);
  const edition = classifyOpenAPIEdition(entry.root);
  options.onRawDocument?.(entry.root);
  if (edition !== "3.2.0") {
    const document = await loadOpenAPIDocument(
      entry.retrieval ?? source.location,
      entry.root,
      { signal: options.signal, allowExternalRefs: options.allowExternalRefs },
      options.fetch,
    );
    return new OpenAPIArtifact({ document, edition, location: entry.retrieval ?? source.location });
  }

  const overlay = new OpenAPI32Overlay(options);
  const resource = overlay.capture(
    entry.root,
    source.location,
    entry.retrieval ?? source.location,
    true,
  );
  const document = resource.root as OpenAPIDocument;
  const root = asRecord(resource.root)!;
  const hasTargetPosition = Object.hasOwn(root, "components")
    || Object.hasOwn(root, "paths")
    || Object.hasOwn(root, "webhooks");
  const refusal = hasTargetPosition
    ? undefined
    : "OpenAPI 3.2 document omits components, paths, and webhooks, leaving no addressable-target position";
  const sourceExclusion = typeof root.jsonSchemaDialect === "string"
    && root.jsonSchemaDialect !== ""
    && root.jsonSchemaDialect !== OAS_BASE_DIALECT
    ? `OpenAPI 3.2 document jsonSchemaDialect ${JSON.stringify(root.jsonSchemaDialect)} is outside the supported default dialect`
    : undefined;
  return new OpenAPIArtifact({
    document,
    edition,
    location: entry.retrieval ?? source.location,
    ...(refusal ? { refusal } : {}),
    ...(sourceExclusion ? { sourceExclusion } : {}),
    overlay,
  });
}

class OpenAPI32Overlay {
  private entry?: RawResource;
  private readonly resources = new Map<string, RawResource>();
  private readonly options: OpenAPIArtifactLoadOptions;

  constructor(options: OpenAPIArtifactLoadOptions) {
    this.options = options;
  }

  entryInfo(): OpenAPI32Resource | undefined {
    if (!this.entry) return undefined;
    return {
      ...(this.entry.retrieval ? { retrievalURI: this.entry.retrieval } : {}),
      ...(this.entry.base ? { identityURI: this.entry.base } : {}),
      ...(this.entry.selfText !== undefined ? { self: this.entry.selfText } : {}),
    };
  }

  capture(
    raw: unknown,
    requested: string | undefined,
    retrieval: string | undefined,
    entry: boolean,
  ): RawResource {
    assertJSONDomain(raw);
    const root = immutableClone(raw);
    const resource: RawResource = {
      root,
      ...(retrieval ? { retrieval: stripHash(retrieval), base: stripHash(retrieval) } : {}),
      selfPresent: false,
      entry,
    };
    const object = asRecord(root);
    if (object && Object.hasOwn(object, "$self")) {
      resource.selfPresent = true;
      if (typeof object.$self !== "string") {
        resource.selfError = "$self is not a string URI-reference";
      } else {
        resource.selfText = object.$self;
        try {
          const resolved = resolveURL(object.$self, resource.retrieval);
          if (!resolved) {
            resource.selfError = `relative $self ${JSON.stringify(object.$self)} has no retrieval base`;
          } else {
            resource.self = stripHash(resolved);
            resource.base = resource.self;
          }
        } catch {
          resource.selfError = `$self ${JSON.stringify(object.$self)} is not a URI-reference`;
        }
      }
      if (resource.selfError) delete resource.base;
    }
    if (entry) this.entry = resource;
    for (const candidate of [requested, retrieval, resource.self]) {
      if (candidate) this.resources.set(stripHash(candidate), resource);
    }
    return resource;
  }

  async operationInventory(): Promise<OpenAPIOperationDisposition[]> {
    if (!this.entry) return [];
    const references = openAPI32OperationReferences(this.entry.root);
    const result: OpenAPIOperationDisposition[] = [];
    for (const reference of references) {
      try {
        result.push({ reference, target: await this.resolveOperation(reference.ref) });
      } catch (error: unknown) {
        result.push({
          reference,
          error: error instanceof OpenAPIOperationResolutionError
            ? error
            : new OpenAPIOperationResolutionError("excluded", errorMessage(error), { cause: error }),
        });
      }
    }
    return result;
  }

  async resolveOperation(ref: string): Promise<OpenAPIResolvedOperation> {
    if (!this.entry) {
      throw new OpenAPIOperationResolutionError("not-found", "OpenAPI 3.2 overlay has no entry resource");
    }
    const reference = parseOpenAPI32OperationReference(ref);
    const root = asRecord(this.entry.root)!;
    const paths = asRecord(root.paths);
    const adjacent = asRecord(paths?.[reference.path]);
    if (!adjacent) {
      throw new OpenAPIOperationResolutionError("not-found", `path ${JSON.stringify(reference.path)} was not found`);
    }
    let selected: ResolvedPathItem;
    try {
      selected = await this.resolvePathItem(adjacent, this.entry, reference);
    } catch (error: unknown) {
      if (error instanceof OpenAPIOperationResolutionError) throw error;
      throw new OpenAPIOperationResolutionError("excluded", errorMessage(error), { cause: error });
    }
    const rawOperation = selected.operation;
    if (!asRecord(rawOperation)) {
      throw new OpenAPIOperationResolutionError("not-found", `operation ${JSON.stringify(ref)} was not found`);
    }
    const rawResponses = asRecord(asRecord(rawOperation)?.responses);
    if (rawResponses && Object.keys(rawResponses).length === 0) {
      throw new OpenAPIOperationResolutionError("excluded", `operation ${JSON.stringify(ref)} has a present empty Responses Object`);
    }

    const pathItem = await this.materializePathItem(selected, reference);
    const operation = openAPI32OperationValue(pathItem as Record<string, unknown>, reference) as OpenAPIOperation;
    const targetRoot = this.targetDocument(root, reference, pathItem);
    const referringSecuritySchemes = await this.materializeSecurityTarget(
      targetRoot,
      asRecord(rawOperation)!,
      selected.operationOwner,
    );
    validateOpenAPI32OperationParameters(root as OpenAPIDocument, reference.path, pathItem, operation);
    return {
      reference,
      document: targetRoot,
      pathItem,
      operation,
      ...(Object.keys(referringSecuritySchemes).length > 0 ? { referringSecuritySchemes } : {}),
    };
  }

  private async materializeSecurityTarget(
    document: OpenAPIDocument,
    rawOperation: Record<string, unknown>,
    operationOwner: RawResource,
  ): Promise<Record<string, Record<string, unknown>>> {
    if (!this.entry) return {};
    const entryRoot = asRecord(this.entry.root);
    const entrySchemes = asRecord(asRecord(entryRoot?.components)?.securitySchemes) ?? {};
    const operationOwnsSecurity = Object.hasOwn(rawOperation, "security");
    const requirementOwner = operationOwnsSecurity ? operationOwner : this.entry;
    const requirements = operationOwnsSecurity ? rawOperation.security : entryRoot?.security;
    const names = openAPI32SecurityRequirementNames(requirements);
    if (names.length === 0) return {};
    const referringRoot = asRecord(requirementOwner.root);
    const referringSchemes = asRecord(asRecord(referringRoot?.components)?.securitySchemes) ?? {};
    const entryNames = new Set(Object.keys(entrySchemes));
    const referringNames = new Set(Object.keys(referringSchemes));
    const uriSchemes: Record<string, Record<string, unknown>> = {};
    const materializedReferring: Record<string, Record<string, unknown>> = {};

    for (const name of names) {
      const kind = openAPI32SecurityNameKind(
        name,
        entryNames,
        referringNames,
        requirementOwner !== this.entry,
      );
      if (kind === "entry") continue;
      try {
        if (kind === "referring") {
          const resolved = await this.resolveSecuritySchemeObject(
            { value: referringSchemes[name], resource: requirementOwner, identity: `component:${name}` },
            new Set(),
          );
          if (resolved) materializedReferring[name] = resolved;
          continue;
        }
        const target = await this.resolveReference(name, requirementOwner, "securityScheme");
        const resolved = await this.resolveSecuritySchemeObject(target, new Set());
        if (resolved) uriSchemes[name] = resolved;
      } catch {
        // A failed URI owns only its containing security alternative.
      }
    }

    if (Object.keys(uriSchemes).length > 0) {
      const components = asRecord(document.components) ?? {};
      document.components = components;
      const schemes = asRecord(components.securitySchemes) ?? {};
      components.securitySchemes = schemes;
      for (const [name, scheme] of Object.entries(uriSchemes)) schemes[name] = scheme;
    }
    return materializedReferring;
  }

  private async resolveSecuritySchemeObject(
    node: ResolvedRawNode,
    seen: Set<string>,
  ): Promise<Record<string, unknown> | null> {
    if (seen.has(node.identity)) return null;
    seen.add(node.identity);
    const ref = openAPI32SecuritySchemeReference(node.value);
    if (ref) {
      const target = await this.resolveReference(ref, node.resource, "securityScheme");
      return this.resolveSecuritySchemeObject(target, seen);
    }
    const scheme = openAPI32SecurityScheme(node.value);
    return scheme ? cloneJSON(scheme) as Record<string, unknown> : null;
  }

  private async resolvePathItem(
    adjacent: Record<string, unknown>,
    owner: RawResource,
    reference: OpenAPIOperationReference,
  ): Promise<ResolvedPathItem> {
    let referenced: Record<string, unknown> | undefined;
    let referencedOwner = owner;
    if (typeof adjacent.$ref === "string") {
      const target = await this.resolveReference(adjacent.$ref, owner, "pathItem");
      referenced = asRecord(target.value);
      referencedOwner = target.resource;
      if (!referenced) throw new Error(`Path Item reference ${JSON.stringify(adjacent.$ref)} does not name an object`);
      this.checkPathItemCollision(adjacent, referenced, reference);
    }
    const adjacentOperation = openAPI32OperationValue(adjacent, reference);
    const referencedOperation = referenced ? openAPI32OperationValue(referenced, reference) : undefined;
    const operation = adjacentOperation ?? referencedOperation;
    const operationOwner = adjacentOperation !== undefined ? owner : referencedOwner;
    const parameterOwner = Object.hasOwn(adjacent, "parameters") ? owner : referencedOwner;
    const serverOwner = Object.hasOwn(adjacent, "servers") ? owner : referencedOwner;
    const merged: Record<string, unknown> = { ...(referenced ?? {}) };
    for (const [key, value] of Object.entries(adjacent)) {
      if (key === "$ref") continue;
      if (key === "additionalOperations") {
        merged.additionalOperations = {
          ...(asRecord(referenced?.additionalOperations) ?? {}),
          ...(asRecord(value) ?? {}),
        };
      } else {
        merged[key] = value;
      }
    }
    return { value: merged, operation, operationOwner, parameterOwner, serverOwner };
  }

  private checkPathItemCollision(
    adjacent: Record<string, unknown>,
    referenced: Record<string, unknown>,
    reference: OpenAPIOperationReference,
  ): void {
    for (const field of ["parameters", "servers"]) {
      if (Object.hasOwn(adjacent, field) && Object.hasOwn(referenced, field)) {
        throw new Error(`selected Path Item $ref has undefined adjacent collision at ${JSON.stringify(field)}`);
      }
    }
    if (
      openAPI32OperationValue(adjacent, reference) !== undefined
      && openAPI32OperationValue(referenced, reference) !== undefined
    ) {
      throw new Error(reference.additional
        ? `selected Path Item $ref has undefined adjacent collision at additional operation ${JSON.stringify(reference.method)}`
        : `selected Path Item $ref has undefined adjacent collision at ${JSON.stringify(reference.method)}`);
    }
  }

  private async materializePathItem(
    selected: ResolvedPathItem,
    reference: OpenAPIOperationReference,
  ): Promise<OpenAPIPathItem> {
    const result: Record<string, unknown> = {};
    for (const field of ["summary", "description"]) {
      if (Object.hasOwn(selected.value, field)) result[field] = cloneJSON(selected.value[field]);
    }
    if (Object.hasOwn(selected.value, "parameters")) {
      result.parameters = await this.materializeParameters(selected.value.parameters, selected.parameterOwner);
    }
    if (Object.hasOwn(selected.value, "servers")) {
      result.servers = this.materializeServers(selected.value.servers, selected.serverOwner);
    }
    const operation = await this.materializeOperation(selected.operation, selected.operationOwner, reference);
    if (reference.additional) {
      result.additionalOperations = { [reference.method]: operation };
    } else {
      result[reference.method] = operation;
    }
    return result as OpenAPIPathItem;
  }

  private async materializeOperation(
    raw: unknown,
    owner: RawResource,
    reference: OpenAPIOperationReference,
  ): Promise<OpenAPIOperation> {
    const source = asRecord(raw);
    if (!source) throw new Error("selected operation is not an object");
    const result: Record<string, unknown> = {};
    for (const field of ["operationId", "summary", "description", "deprecated", "tags", "security"]) {
      if (Object.hasOwn(source, field)) result[field] = cloneJSON(source[field]);
    }
    if (Object.hasOwn(source, "parameters")) {
      result.parameters = await this.materializeParameters(source.parameters, owner);
    }
    if (Object.hasOwn(source, "servers")) result.servers = this.materializeServers(source.servers, owner);
    if (Object.hasOwn(source, "requestBody") && !(reference.method === "trace" && !reference.additional)) {
      result.requestBody = await this.materializeRequestBody(source.requestBody, owner);
    }
    if (Object.hasOwn(source, "responses")) {
      result.responses = this.unaryResponseBridge(source.responses);
    }
    return result as OpenAPIOperation;
  }

  private async materializeParameters(raw: unknown, owner: RawResource): Promise<OpenAPIParameter[]> {
    if (!Array.isArray(raw)) throw new Error("selected parameters declaration is not an array");
    const result: OpenAPIParameter[] = [];
    for (const value of raw) {
      const resolved = await this.resolveReferenceObject(value, owner, "parameter");
      const parameter = asRecord(resolved.value);
      if (!parameter) throw new Error("selected Parameter is not an object");
      const clone = cloneJSON(parameter) as OpenAPIParameter;
      if (Object.hasOwn(parameter, "schema")) {
        clone.schema = await this.materializeSchema(parameter.schema, resolved.resource);
      }
      if (Object.hasOwn(parameter, "content")) {
        clone.content = await this.materializeContent(parameter.content, resolved.resource);
      }
      result.push(clone);
    }
    return result;
  }

  private async materializeRequestBody(raw: unknown, owner: RawResource): Promise<OpenAPIRequestBody> {
    const resolved = await this.resolveReferenceObject(raw, owner, "requestBody");
    const body = asRecord(resolved.value);
    if (!body) throw new Error("selected Request Body is not an object");
    const result = cloneJSON(body) as OpenAPIRequestBody;
    if (Object.hasOwn(body, "content")) {
      result.content = await this.materializeContent(body.content, resolved.resource);
    }
    return result;
  }

  private async materializeContent(
    raw: unknown,
    owner: RawResource,
  ): Promise<Record<string, OpenAPIMediaType>> {
    const content = asRecord(raw);
    if (!content) throw new Error("selected content declaration is not an object");
    const result: Record<string, OpenAPIMediaType> = {};
    for (const [mediaType, value] of Object.entries(content)) {
      const resolved = await this.resolveReferenceObject(value, owner, "mediaType");
      const media = asRecord(resolved.value);
      if (!media) throw new Error(`selected Media Type ${JSON.stringify(mediaType)} is not an object`);
      const clone = cloneJSON(media) as OpenAPIMediaType;
      if (Object.hasOwn(media, "schema")) clone.schema = await this.materializeSchema(media.schema, resolved.resource);
      if (Object.hasOwn(media, "itemSchema")) clone.itemSchema = await this.materializeSchema(media.itemSchema, resolved.resource);
      for (const field of ["encoding", "prefixEncoding", "itemEncoding"]) {
        if (Object.hasOwn(media, field)) clone[field] = await this.materializeEncoding(media[field], resolved.resource, 0);
      }
      result[mediaType] = clone;
    }
    return result;
  }

  private async materializeEncoding(raw: unknown, owner: RawResource, depth: number): Promise<unknown> {
    if (Array.isArray(raw)) {
      return Promise.all(raw.map((value) => this.materializeEncoding(value, owner, depth)));
    }
    const encoding = asRecord(raw);
    if (!encoding) return cloneJSON(raw);
    const result = cloneJSON(encoding) as Record<string, unknown>;
    if (depth < 2) {
      for (const field of ["encoding", "prefixEncoding", "itemEncoding"]) {
        if (Object.hasOwn(encoding, field)) result[field] = await this.materializeEncoding(encoding[field], owner, depth + 1);
      }
    }
    const headers = asRecord(encoding.headers);
    if (headers) {
      const materialized: Record<string, unknown> = {};
      for (const [name, value] of Object.entries(headers)) {
        materialized[name] = (await this.resolveReferenceObject(value, owner, "header")).value;
      }
      result.headers = materialized;
    }
    return result;
  }

  private async materializeSchema(
    raw: unknown,
    owner: RawResource,
    base = owner.base,
    seen = new Map<string, Record<string, unknown>>(),
  ): Promise<Record<string, unknown> | boolean> {
    if (typeof raw === "boolean") return raw;
    const schema = asRecord(raw);
    if (!schema) throw new Error("selected Schema Object is not an object or boolean");
    if (Object.hasOwn(schema, "$schema") && schema.$schema !== OAS_BASE_DIALECT) {
      throw new Error(`selected Schema Object resource uses unsupported $schema dialect ${JSON.stringify(schema.$schema)}`);
    }
    let schemaBase = base;
    if (typeof schema.$id === "string") schemaBase = resolveURL(schema.$id, schemaBase) ?? schemaBase;
    if (typeof schema.$ref === "string") {
      const target = await this.resolveReference(schema.$ref, owner, "schema", schemaBase);
      const existing = seen.get(target.identity);
      if (existing) return existing;
      const placeholder: Record<string, unknown> = {};
      seen.set(target.identity, placeholder);
      const resolved = await this.materializeSchema(target.value, target.resource, target.resource.base, seen);
      if (typeof resolved === "boolean") {
        seen.delete(target.identity);
        return Object.keys(schema).length === 1 ? resolved : { allOf: [resolved], ...withoutRef(schema) };
      }
      Object.assign(placeholder, resolved);
      const siblings = withoutRef(schema);
      if (Object.keys(siblings).length === 0) return placeholder;
      return { ...siblings, allOf: [placeholder, ...(Array.isArray(siblings.allOf) ? siblings.allOf : [])] };
    }
    const result = cloneJSON(schema) as Record<string, unknown>;
    const mapKeys = ["properties", "patternProperties", "$defs", "definitions", "dependentSchemas"];
    for (const key of mapKeys) {
      const values = asRecord(schema[key]);
      if (!values) continue;
      result[key] = Object.fromEntries(await Promise.all(Object.entries(values).map(async ([name, value]) => [
        name,
        await this.materializeSchema(value, owner, schemaBase, seen),
      ])));
    }
    for (const key of ["allOf", "anyOf", "oneOf", "prefixItems"]) {
      const values = schema[key];
      if (!Array.isArray(values)) continue;
      result[key] = await Promise.all(values.map((value) => this.materializeSchema(value, owner, schemaBase, seen)));
    }
    for (const key of ["items", "contains", "not", "if", "then", "else", "propertyNames", "additionalProperties", "unevaluatedItems", "unevaluatedProperties"]) {
      if (!Object.hasOwn(schema, key) || (typeof schema[key] !== "object" && typeof schema[key] !== "boolean")) continue;
      result[key] = await this.materializeSchema(schema[key], owner, schemaBase, seen);
    }
    return result;
  }

  private materializeServers(raw: unknown, owner: RawResource): unknown {
    if (!Array.isArray(raw)) return cloneJSON(raw);
    return raw.map((value) => {
      const server = cloneJSON(value) as Record<string, unknown>;
      if (owner.retrieval) server["x-openbindings-internal-server-document"] = owner.retrieval;
      return server;
    });
  }

  private unaryResponseBridge(raw: unknown): Record<string, unknown> {
    const responses = asRecord(raw);
    if (!responses) return {};
    const result: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(responses)) {
      if (key !== "default" && !/^\d{3}$/u.test(key)) continue;
      const response = asRecord(value);
      if (!response || typeof response.$ref === "string" || typeof response.description !== "string") continue;
      const headers = asRecord(response.headers);
      if (Object.keys(headers ?? {}).some((name) => name.toLowerCase() === "content-encoding")) continue;
      const clone = cloneJSON(response) as Record<string, unknown>;
      const content = asRecord(response.content);
      if (content) {
        clone.content = Object.fromEntries(Object.entries(content).filter(([mediaType, media]) => {
          const base = mediaType.split(";", 1)[0]!.trim().toLowerCase();
          const sequential = base === "text/event-stream"
            || base === "application/jsonl"
            || base === "application/x-ndjson"
            || base === "application/json-seq"
            || base.endsWith("+json-seq")
            || Object.hasOwn(asRecord(media) ?? {}, "itemSchema");
          return !sequential;
        }));
        if (Object.keys(content).length > 0 && Object.keys(clone.content as object).length === 0) continue;
      }
      result[key] = clone;
    }
    return result;
  }

  private targetDocument(
    root: Record<string, unknown>,
    reference: OpenAPIOperationReference,
    pathItem: OpenAPIPathItem,
  ): OpenAPIDocument {
    const document: Record<string, unknown> = {};
    for (const field of ["openapi", "$self", "info", "jsonSchemaDialect", "servers", "security", "tags", "externalDocs", "components"]) {
      if (Object.hasOwn(root, field)) document[field] = cloneJSON(root[field]);
    }
    if (Array.isArray(document.servers) && this.entry) {
      document.servers = this.materializeServers(document.servers, this.entry);
    }
    document.paths = { [reference.path]: pathItem };
    return document as OpenAPIDocument;
  }

  private async resolveReferenceObject(
    raw: unknown,
    owner: RawResource,
    kind: "parameter" | "requestBody" | "mediaType" | "header",
  ): Promise<ResolvedRawNode> {
    const object = asRecord(raw);
    if (!object || typeof object.$ref !== "string") {
      return { value: raw, resource: owner, identity: objectIdentity(owner, raw) };
    }
    const resolved = await this.resolveReference(object.$ref, owner, kind);
    const target = asRecord(resolved.value);
    if (!target) return resolved;
    const annotations: Record<string, unknown> = {};
    if (Object.hasOwn(object, "summary")) annotations.summary = cloneJSON(object.summary);
    if (Object.hasOwn(object, "description")) annotations.description = cloneJSON(object.description);
    return { ...resolved, value: { ...target, ...annotations } };
  }

  private async resolveReference(
    refText: string,
    owner: RawResource,
    kind: "pathItem" | "parameter" | "requestBody" | "mediaType" | "header" | "schema" | "securityScheme",
    baseOverride?: string,
  ): Promise<ResolvedRawNode> {
    const resolved = resolveReferenceURL(refText, baseOverride ?? owner.base);
    if (!resolved) throw new Error(`selected ${kind} reference ${JSON.stringify(refText)} has no document base`);
    let resource = refText.startsWith("#") ? owner : this.resources.get(resolved.resource);
    if (!resource) {
      resource = await this.fetchResource(resolved.resource);
    }
    if (resource.selfError) {
      throw new Error(`selected ${kind} reference reaches a resource with unusable ${resource.selfError}`);
    }
    if (resource.self && resolved.resource !== resource.self) {
      throw new Error(
        `selected ${kind} reference uses retrieval alias ${JSON.stringify(resolved.resource)} instead of declared $self identity ${JSON.stringify(resource.self)}`,
      );
    }
    if (kind === "schema" && pointerCrossesSchemaResource(resource.root, resolved.fragment)) {
      throw new Error("selected Schema Object reference crosses a nearer $id resource boundary noncanonically");
    }
    const target = fragmentTarget(resource.root, resolved.fragment);
    if (target === undefined) throw new Error(`selected ${kind} reference ${JSON.stringify(refText)} names no target`);
    return {
      value: target,
      resource,
      identity: `${resolved.resource}#${resolved.fragment}`,
    };
  }

  private async fetchResource(resourceURL: string): Promise<RawResource> {
    if (this.options.allowExternalRefs === false) {
      throw new Error(`external reference ${JSON.stringify(resourceURL)} is disabled`);
    }
    const response = await (this.options.fetch ?? fetch)(resourceURL, { signal: this.options.signal });
    if (!response.ok) throw new Error(`failed to fetch ${resourceURL}: ${response.status} ${response.statusText}`);
    const retrieval = response.url || resourceURL;
    const root = parseOpenAPI32Text(await response.text());
    return this.capture(root, resourceURL, retrieval, false);
  }
}

async function readEntry(
  source: OpenAPIArtifactSource,
  options: OpenAPIArtifactLoadOptions,
): Promise<{ root: unknown; retrieval?: string }> {
  if (source.content !== undefined) {
    const root = typeof source.content === "string"
      ? parseOpenAPI32Text(source.content)
      : source.content;
    assertJSONDomain(root);
    return { root, ...(source.location ? { retrieval: source.location } : {}) };
  }
  if (!source.location) throw new Error("OpenAPI source requires location or content");
  const response = await (options.fetch ?? fetch)(source.location, { signal: options.signal });
  if (!response.ok) throw new Error(`failed to fetch ${source.location}: ${response.status} ${response.statusText}`);
  return { root: parseOpenAPI32Text(await response.text()), retrieval: response.url || source.location };
}

function parseOpenAPI32Text(text: string): unknown {
  // js-yaml stringifies collection-valued YAML mapping keys before its value
  // reaches the JSON-domain guard. Refuse those representation-only keys at
  // the 3.2 lane's compatibility gate instead of admitting a changed image.
  if (
    /^\s*\?\s*[\[{]/mu.test(text)
    || /^\s*\?\s*(?:#.*)?\r?\n\s+[-?]\s/mu.test(text)
    || /^\s*[\[{].*[\]}]\s*:/mu.test(text)
  ) {
    throw new Error("OpenAPI YAML mapping key has no JSON object-member-name image");
  }
  return parseJSONOrYAML(text);
}

function assertJSONDomain(root: unknown): void {
  const seen = new WeakSet<object>();
  const walk = (value: unknown): void => {
    if (value === null || typeof value === "string" || typeof value === "boolean") return;
    if (typeof value === "number") {
      if (!Number.isFinite(value)) throw new Error(`OpenAPI YAML value ${String(value)} has no JSON image`);
      return;
    }
    if (typeof value !== "object") throw new Error(`OpenAPI value of type ${typeof value} has no JSON image`);
    if (seen.has(value)) throw new Error("OpenAPI parsed content contains a cycle and has no JSON image");
    seen.add(value);
    if (Array.isArray(value)) {
      for (const member of value) walk(member);
    } else {
      const prototype = Object.getPrototypeOf(value);
      if (prototype !== Object.prototype && prototype !== null) {
        throw new Error("OpenAPI parsed content contains a non-JSON object");
      }
      for (const member of Object.values(value as Record<string, unknown>)) walk(member);
    }
    seen.delete(value);
  };
  walk(root);
}

function immutableClone<T>(value: T): T {
  const clone = structuredClone(value);
  const seen = new WeakSet<object>();
  const freeze = (member: unknown): void => {
    if (!member || typeof member !== "object" || seen.has(member)) return;
    seen.add(member);
    for (const child of Object.values(member)) freeze(child);
    Object.freeze(member);
  };
  freeze(clone);
  return clone;
}

function cloneJSON<T>(value: T): T {
  return structuredClone(value);
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function resolveURL(reference: string, base?: string): string | undefined {
  try {
    return base ? new URL(reference, base).href : new URL(reference).href;
  } catch {
    return undefined;
  }
}

function stripHash(value: string): string {
  const url = new URL(value);
  url.hash = "";
  return url.href;
}

function resolveReferenceURL(
  refText: string,
  base?: string,
): { resource: string; fragment: string } | undefined {
  if (refText.startsWith("#")) {
    return { resource: base ? stripHash(base) : "", fragment: refText.slice(1) };
  }
  const resolved = resolveURL(refText, base);
  if (!resolved) return undefined;
  const url = new URL(resolved);
  const fragment = url.hash.slice(1);
  url.hash = "";
  return { resource: url.href, fragment };
}

function fragmentTarget(root: unknown, encodedFragment: string): unknown {
  let fragment: string;
  try {
    fragment = decodeURIComponent(encodedFragment);
  } catch {
    return undefined;
  }
  if (fragment === "") return root;
  if (!fragment.startsWith("/")) return anchorTarget(root, fragment, new WeakSet());
  let current = root;
  for (const encoded of fragment.slice(1).split("/")) {
    if (/~(?:[^01]|$)/u.test(encoded)) return undefined;
    const token = encoded.replaceAll("~1", "/").replaceAll("~0", "~");
    if (Array.isArray(current)) {
      if (!/^(?:0|[1-9]\d*)$/u.test(token)) return undefined;
      current = current[Number(token)];
    } else if (asRecord(current) && Object.hasOwn(current as object, token)) {
      current = (current as Record<string, unknown>)[token];
    } else {
      return undefined;
    }
  }
  return current;
}

function anchorTarget(root: unknown, anchor: string, seen: WeakSet<object>): unknown {
  if (!root || typeof root !== "object" || seen.has(root)) return undefined;
  seen.add(root);
  const object = asRecord(root);
  if (object?.$anchor === anchor || object?.$dynamicAnchor === anchor) return root;
  for (const child of Object.values(root)) {
    const found = anchorTarget(child, anchor, seen);
    if (found !== undefined) return found;
  }
  return undefined;
}

function pointerCrossesSchemaResource(root: unknown, encodedFragment: string): boolean {
  let fragment: string;
  try {
    fragment = decodeURIComponent(encodedFragment);
  } catch {
    return false;
  }
  if (!fragment.startsWith("/")) return false;
  let current = root;
  for (const encoded of fragment.slice(1).split("/")) {
    if (asRecord(current) && typeof (current as Record<string, unknown>).$id === "string") return true;
    const token = encoded.replaceAll("~1", "/").replaceAll("~0", "~");
    if (Array.isArray(current)) current = current[Number(token)];
    else current = asRecord(current)?.[token];
  }
  return asRecord(current) !== undefined && typeof asRecord(current)!.$id === "string";
}

function withoutRef(value: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(value).filter(([key]) => key !== "$ref"));
}

function objectIdentity(owner: RawResource, value: unknown): string {
  return `${owner.base ?? owner.retrieval ?? "entry"}:${typeof value}`;
}

function escapePointer(value: string): string {
  return value.replaceAll("~", "~0").replaceAll("/", "~1");
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
