import {
  SWAGGER20_METHODS,
  arrayMember,
  booleanMember,
  isSwagger20Object,
  member,
  objectMember,
  stringMember,
  type Swagger20Document,
  type Swagger20Object,
  type Swagger20ResolvedOperation,
  type Swagger20Resource,
} from "./swagger20-model.js";
import { escapePointerToken } from "./swagger20-reference.js";
import { resolveSwagger20Operation } from "./swagger20-engine.js";
import {
  effectiveSwagger20Parameters,
  type Swagger20Parameter,
  type Swagger20ParameterLocation,
  type Swagger20ParameterSet,
} from "./swagger20-parameters.js";
import {
  effectiveSwagger20MediaSet,
  parseSwagger20ConcreteMedia,
  resolveSwagger20ResponseValue,
  swagger20PayloadFor,
  swagger20RangeHasUsableLane,
  swagger20RequestLane,
  swagger20ResponseLane,
  swagger20ResponsesFor,
  type Swagger20PayloadModel,
} from "./swagger20-media.js";
import { resolveSwagger20SchemaDeclaration } from "./swagger20-schema.js";
import { resolveSwagger20Server } from "./swagger20-server.js";
import { selectSwagger20Security, type Swagger20SecurityCredentials } from "./swagger20-security.js";

export interface Swagger20SynthesisDocument {
  name?: string;
  version?: string;
  description?: string;
  operations: Swagger20SynthesisOperation[];
}

export interface Swagger20SynthesisOperation {
  ref: string;
  path: string;
  method: string;
  operationId?: string;
  description?: string;
  deprecated: boolean;
  tags: string[];
  parameters: Swagger20SynthesisParameter[];
  body?: Swagger20SynthesisBody;
  responses: Swagger20SynthesisResponse[];
  alternatives: Swagger20SynthesisAlternative[];
  security: Swagger20SynthesisSecurityAlternative[];
  requirements: string[];
  excluded: boolean;
  reason?: string;
}

export interface Swagger20SynthesisParameter {
  name: string;
  in: Swagger20ParameterLocation;
  required: boolean;
  allowEmptyValue: boolean;
  schema: Swagger20Object;
}

export interface Swagger20SynthesisBody { required: boolean; schema: Swagger20Object }
export interface Swagger20SynthesisResponse {
  key: string;
  sourceRef: string;
  schemaPresent: boolean;
  schema?: Swagger20Object;
  canSucceed: boolean;
  usable: boolean;
  reason?: string;
  headers: Swagger20SynthesisResponseHeader[];
}
export interface Swagger20SynthesisResponseHeader {
  name?: string;
  sourceRef: string;
  usable: boolean;
  reason?: string;
  needsContentCodec?: boolean;
}
export interface Swagger20SynthesisAlternative {
  sourceRef: string;
  kind: "requestMedia" | "response" | "security" | "server";
  index?: number;
  usable: boolean;
  reason?: string;
  requirements: string[];
}
export interface Swagger20SynthesisSecurityAlternative {
  sourceRef: string;
  index: number;
  anonymous: boolean;
  usable: boolean;
  reason?: string;
  schemes: Swagger20SynthesisSecurityScheme[];
}
export interface Swagger20SynthesisSecurityScheme { name: string; type?: string; scopes: string[] }

/** Analyzes every authored fixed operation slot with per-target confinement. */
export async function swagger20SynthesisModel(document: Swagger20Document): Promise<Swagger20SynthesisDocument> {
  const model: Swagger20SynthesisDocument = { operations: [] };
  const info = objectMember(document.root, "info");
  if (info.valid) {
    model.name = stringMember(info.value!, "title").value;
    model.version = stringMember(info.value!, "version").value;
    model.description = stringMember(info.value!, "description").value;
  }
  const paths = objectMember(document.root, "paths");
  if (!paths.valid) return model;
  for (const path of Object.keys(paths.value!).sort()) {
    const rawItem = paths.value![path];
    if (!isSwagger20Object(rawItem)) continue;
    const referenced = member(rawItem, "$ref").present;
    for (const method of SWAGGER20_METHODS) {
      if (!Object.hasOwn(rawItem, method) && !referenced) continue;
      const ref = `#/paths/${escapePointerToken(path)}/${method}`;
      try {
        const operation = await resolveSwagger20Operation(document, ref);
        model.operations.push(await analyzeSwagger20Operation(document, operation, ref));
      } catch (error: unknown) {
        if (!referenced) model.operations.push(excludedOperation(ref, path, method, errorMessage(error)));
      }
    }
  }
  return model;
}

export function analyzePreparedSwagger20Operation(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  ref: string,
): Promise<Swagger20SynthesisOperation> {
  return analyzeSwagger20Operation(document, operation, ref);
}

async function analyzeSwagger20Operation(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  ref: string,
): Promise<Swagger20SynthesisOperation> {
  const result = baseOperation(operation, ref);
  let parameters: Swagger20ParameterSet;
  let responses: Swagger20Object;
  try {
    parameters = await effectiveSwagger20Parameters(operation);
    responses = swagger20ResponsesFor(operation);
  } catch (error: unknown) { return exclude(result, error); }
  try { resolveSwagger20Server(document, operation); }
  catch (error: unknown) {
    try { resolveSwagger20Server(document, operation, "https://configured.invalid"); }
    catch { return exclude(result, error); }
    addRequirement(result.requirements, "configuration.server");
  }

  for (const parameter of parameters.nonBody) {
    result.parameters.push({
      name: parameter.name,
      in: parameter.in,
      required: parameter.required,
      allowEmptyValue: parameter.allowEmptyValue,
      schema: parameterSchemaImage(parameter),
    });
    if (parameter.allowEmptyValue) addRequirement(result.requirements, "configuration.emptyValueForm");
    if (parameterNeedsConversion(parameter)) addRequirement(result.requirements, "configuration.parameterConversion");
    if (parameter.in === "header" && parameter.name.toLowerCase() === "content-encoding"
      && codingDeclarationNeedsCodec(parameter.raw)) addRequirement(result.requirements, "configuration.requestContentCodings");
  }
  if (parameters.body) {
    try {
      result.body = {
        required: parameters.body.required,
        schema: await materializeSwagger20Schema(operation.graph, parameters.body.raw.schema, parameters.body.resource),
      };
    } catch (error: unknown) { return exclude(result, error); }
  }

  let payload: Swagger20PayloadModel;
  try { payload = await swagger20PayloadFor(parameters, operation); }
  catch (error: unknown) { return exclude(result, error); }
  let consumes;
  try { consumes = effectiveSwagger20MediaSet(document, operation, "consumes"); }
  catch (error: unknown) { return exclude(result, error); }
  const consumesPrefix = arrayMember(operation.raw, "consumes").present ? `${ref}/consumes` : "#/consumes";
  let usableConsumes = 0;
  let soleConcrete = false;
  for (const [index, entry] of consumes.entries.entries()) {
    if (!payload.kind) break;
    const alternative: Swagger20SynthesisAlternative = {
      sourceRef: `${consumesPrefix}/${index}`, kind: "requestMedia", index, usable: false, requirements: [],
    };
    if (entry.error) alternative.reason = entry.error.message;
    else if (entry.colliding) alternative.reason = "media declaration collides after normalized identity comparison";
    else if (entry.parsed!.specificity < 2) {
      alternative.usable = swagger20RangeHasUsableLane(entry.parsed!, payload);
      if (alternative.usable) addRequirement(alternative.requirements, "configuration.requestMedia");
      else alternative.reason = "media range selects no usable request carriage lane";
    } else {
      try { swagger20RequestLane(entry.parsed!, payload); alternative.usable = true; }
      catch (error: unknown) { alternative.reason = errorMessage(error); }
    }
    if (alternative.usable) {
      usableConsumes++;
      soleConcrete = usableConsumes === 1 && entry.parsed!.specificity === 2;
      if (entry.parsed!.base === "multipart/form-data" && payload.form.some((parameter) => parameter.typeName === "file")) {
        addRequirement(alternative.requirements, "configuration.propertyMedia");
      }
    }
    result.alternatives.push(alternative);
  }
  if (payload.kind) {
    const required = payload.body?.required === true || payload.form.some((parameter) => parameter.required);
    if (usableConsumes === 0) {
      if (required) return exclude(result, "required request payload has no usable effective consumes alternative");
      result.body = undefined;
      result.parameters = result.parameters.filter((parameter) => parameter.in !== "formData");
    } else {
      if (usableConsumes !== 1 || !soleConcrete) {
        for (const alternative of result.alternatives) if (alternative.kind === "requestMedia" && alternative.usable) {
          addRequirement(alternative.requirements, "configuration.requestMedia");
        }
        if (required) addRequirement(result.requirements, "configuration.requestMedia");
      }
      if (required && payload.form.some((parameter) => parameter.required && parameter.typeName === "file")) {
        addRequirement(result.requirements, "configuration.propertyMedia");
      }
    }
  }

  result.security = analyzeSecurity(document, operation, parameters, ref);
  if (result.security.length > 0 && !result.security.some((alternative) => alternative.usable)) {
    return exclude(result, "effective security declaration has no usable complete alternative");
  }
  if (result.security.length > 1) addRequirement(result.requirements, "configuration.security");
  for (const alternative of result.security) if (!alternative.usable) result.alternatives.push({
    sourceRef: alternative.sourceRef, kind: "security", index: alternative.index,
    usable: false, reason: alternative.reason, requirements: [],
  });
  result.alternatives.push(...serverAlternatives(document, operation, ref));

  result.responses = await analyzeResponses(document, operation, responses, ref);
  for (const response of result.responses) if (response.schemaPresent && !response.usable) result.alternatives.push({
    sourceRef: response.sourceRef, kind: "response", usable: false, reason: response.reason, requirements: [],
  });
  if (responsesUseContentCoding(result.responses)) addRequirement(result.requirements, "configuration.responseContentCodings");
  result.requirements.sort();
  return result;
}

function baseOperation(operation: Swagger20ResolvedOperation, ref: string): Swagger20SynthesisOperation {
  const description = stringMember(operation.raw, "description");
  const summary = stringMember(operation.raw, "summary");
  const tags = arrayMember(operation.raw, "tags");
  return {
    ref, path: operation.path, method: operation.method,
    operationId: stringMember(operation.raw, "operationId").value,
    description: description.valid && description.value !== "" ? description.value : summary.value,
    deprecated: booleanMember(operation.raw, "deprecated").value === true,
    tags: tags.valid ? tags.value!.filter((tag): tag is string => typeof tag === "string") : [],
    parameters: [], responses: [], alternatives: [], security: [], requirements: [], excluded: false,
  };
}

function excludedOperation(ref: string, path: string, method: string, reason: string): Swagger20SynthesisOperation {
  return { ref, path, method, deprecated: false, tags: [], parameters: [], responses: [], alternatives: [], security: [], requirements: [], excluded: true, reason };
}

function exclude(result: Swagger20SynthesisOperation, error: unknown): Swagger20SynthesisOperation {
  result.excluded = true;
  result.reason = errorMessage(error);
  return result;
}

const PARAMETER_SCHEMA_KEYS = [
  "type", "format", "default", "multipleOf", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum",
  "maxLength", "minLength", "pattern", "maxItems", "minItems", "uniqueItems", "enum", "items",
] as const;

function parameterSchemaImage(parameter: Swagger20Parameter): Swagger20Object {
  const schema: Swagger20Object = {};
  for (const key of PARAMETER_SCHEMA_KEYS) if (Object.hasOwn(parameter.raw, key)) schema[key] = structuredClone(parameter.raw[key]);
  return schema;
}

function parameterNeedsConversion(parameter: Swagger20Parameter): boolean {
  if (parameter.typeName !== "array") return parameter.typeName !== "string" && parameter.typeName !== "file";
  let type = parameter.items?.typeName;
  let items = parameter.items?.items;
  while (type === "array") { type = items?.typeName; items = items?.items; }
  return type !== undefined && type !== "string";
}

function analyzeSecurity(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  parameters: Swagger20ParameterSet,
  ref: string,
): Swagger20SynthesisSecurityAlternative[] {
  let member = arrayMember(operation.raw, "security");
  let prefix = `${ref}/security`;
  if (!member.present) { member = arrayMember(document.root, "security"); prefix = "#/security"; }
  if (!member.present) return [];
  if (!member.valid) return [{ sourceRef: `${prefix}/0`, index: 0, anonymous: false, usable: false, reason: "effective security field is not an array", schemes: [] }];
  const definitions = objectMember(document.root, "securityDefinitions");
  return member.value!.map((raw, index) => {
    const alternative: Swagger20SynthesisSecurityAlternative = {
      sourceRef: `${prefix}/${index}`, index, anonymous: false, usable: false, schemes: [],
    };
    if (!isSwagger20Object(raw)) { alternative.reason = "Security Requirement is not an object"; return alternative; }
    alternative.anonymous = Object.keys(raw).length === 0;
    const credentials: Swagger20SecurityCredentials = { basic: {}, apiKeys: {}, oauth2: {} };
    for (const name of Object.keys(raw).sort()) {
      const scopes = Array.isArray(raw[name]) ? (raw[name] as unknown[]).filter((scope): scope is string => typeof scope === "string") : [];
      const definition = definitions.valid && isSwagger20Object(definitions.value![name]) ? definitions.value![name] as Swagger20Object : undefined;
      const type = definition ? stringMember(definition, "type").value : undefined;
      alternative.schemes.push({ name, type, scopes });
      if (type === "basic") credentials.basic![name] = { userId: "", password: "" };
      else if (type === "apiKey") credentials.apiKeys![name] = "";
      else if (type === "oauth2") credentials.oauth2![name] = { accessToken: "token", scopes };
    }
    try { selectSwagger20Security(document, operation, parameters, index, credentials); alternative.usable = true; }
    catch (error: unknown) { alternative.reason = errorMessage(error); }
    return alternative;
  });
}

function serverAlternatives(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  ref: string,
): Swagger20SynthesisAlternative[] {
  let member = arrayMember(operation.raw, "schemes");
  let prefix = `${ref}/schemes`;
  if (!member.present) { member = arrayMember(document.root, "schemes"); prefix = "#/schemes"; }
  if (!member.valid) return [];
  return member.value!.map((raw, index) => {
    const usable = raw === "http" || raw === "https";
    return { sourceRef: `${prefix}/${index}`, kind: "server", index, usable, requirements: [], ...(usable ? {} : { reason: `effective scheme ${JSON.stringify(raw)} is unusable` }) };
  });
}

async function analyzeResponses(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  responses: Swagger20Object,
  ref: string,
): Promise<Swagger20SynthesisResponse[]> {
  let produces;
  let producesError: string | undefined;
  try { produces = effectiveSwagger20MediaSet(document, operation, "produces"); }
  catch (error: unknown) { producesError = errorMessage(error); }
  const result: Swagger20SynthesisResponse[] = [];
  for (const key of Object.keys(responses).filter((key) => key === "default" || /^[1-5][0-9][0-9]$/u.test(key)).sort()) {
    const entry: Swagger20SynthesisResponse = {
      key, sourceRef: `${ref}/responses/${escapePointerToken(key)}`, schemaPresent: false,
      canSucceed: key === "default" || key.startsWith("2"), usable: false, headers: [],
    };
    let resolved;
    try { resolved = await resolveSwagger20ResponseValue(operation, responses[key], operation.resource, key); }
    catch (error: unknown) { entry.reason = errorMessage(error); result.push(entry); continue; }
    entry.headers = analyzeResponseHeaders(resolved.raw, entry.sourceRef);
    entry.schemaPresent = Object.hasOwn(resolved.raw, "schema");
    if (!entry.schemaPresent) { entry.usable = true; result.push(entry); continue; }
    try { entry.schema = await materializeSwagger20Schema(operation.graph, resolved.raw.schema, resolved.resource); }
    catch (error: unknown) { entry.reason = errorMessage(error); result.push(entry); continue; }
    if (producesError) { entry.reason = producesError; result.push(entry); continue; }
    try {
      const declaration = await resolveSwagger20SchemaDeclaration(operation.graph, resolved.raw.schema, resolved.resource, true);
      for (const media of produces!.entries) {
        if (!media.parsed || media.colliding) continue;
        const candidates = media.parsed.specificity === 2 ? [media.parsed] : [
          "application/json", "text/plain", "application/octet-stream", "image/png",
        ].map(parseSwagger20ConcreteMedia).filter((candidate) => declarationMatches(media.parsed!, candidate));
        if (candidates.some((candidate) => { try { swagger20ResponseLane(candidate, declaration); return true; } catch { return false; } })) {
          entry.usable = true;
          break;
        }
      }
    } catch (error: unknown) { entry.reason = errorMessage(error); }
    if (!entry.usable && !entry.reason) entry.reason = "response schema and effective produces define no usable response carriage lane";
    result.push(entry);
  }
  return result;
}

function analyzeResponseHeaders(response: Swagger20Object, responseRef: string): Swagger20SynthesisResponseHeader[] {
  const headers = objectMember(response, "headers");
  if (!headers.present) return [];
  if (!headers.valid) return [{ sourceRef: `${responseRef}/headers`, usable: false, reason: "Response headers is not an object" }];
  const identities = new Map<string, number>();
  for (const name of Object.keys(headers.value!)) identities.set(name.toLowerCase(), (identities.get(name.toLowerCase()) ?? 0) + 1);
  return Object.keys(headers.value!).sort().map((name) => {
    const entry: Swagger20SynthesisResponseHeader = { name, sourceRef: `${responseRef}/headers/${escapePointerToken(name)}`, usable: false };
    const raw = headers.value![name];
    const object = isSwagger20Object(raw) ? raw : undefined;
    if (!object) entry.reason = "response Header Object is not an object";
    else if (identities.get(name.toLowerCase())! > 1) entry.reason = "response Header Object name collides under ASCII case-insensitive identity";
    else entry.reason = responseHeaderDefect(object);
    entry.usable = entry.reason === undefined;
    if (entry.usable && name.toLowerCase() === "content-encoding") entry.needsContentCodec = codingDeclarationNeedsCodec(object!);
    return entry;
  });
}

function responseHeaderDefect(raw: Swagger20Object): string | undefined {
  const type = stringMember(raw, "type");
  if (!type.valid) return "response Header Object requires string type";
  if (type.value === "array") {
    if (!objectMember(raw, "items").valid) return "array response Header Object requires an Items Object";
    const collection = stringMember(raw, "collectionFormat");
    if (collection.present && (!collection.valid || !["csv", "ssv", "tsv", "pipes"].includes(collection.value!))) {
      return `response Header Object collectionFormat ${JSON.stringify(collection.value)} is not admitted`;
    }
  } else if (!["string", "number", "integer", "boolean"].includes(type.value!)) {
    return `response Header Object type ${JSON.stringify(type.value)} is not admitted`;
  } else if (stringMember(raw, "collectionFormat").present) return "response Header Object collectionFormat applies only to arrays";
  return undefined;
}

function responsesUseContentCoding(responses: Swagger20SynthesisResponse[]): boolean {
  return responses.some((response) => response.headers.some((header) =>
    header.name?.toLowerCase() === "content-encoding" && header.usable && header.needsContentCodec === true));
}

function codingDeclarationNeedsCodec(raw: Swagger20Object): boolean {
  if (!Array.isArray(raw.enum) || raw.enum.length === 0) return true;
  return raw.enum.some((value) => typeof value !== "string" || value.toLowerCase() !== "identity");
}

function declarationMatches(declaration: { base: string; params: Record<string, string> }, concrete: { base: string; params: Record<string, string> }): boolean {
  const [declaredType, declaredSubtype] = declaration.base.split("/");
  const [actualType, actualSubtype] = concrete.base.split("/");
  return (declaredType === "*" || declaredType === actualType) && (declaredSubtype === "*" || declaredSubtype === actualSubtype)
    && Object.entries(declaration.params).every(([name, value]) => concrete.params[name] === value);
}

async function materializeSwagger20Schema(
  graph: Swagger20ResolvedOperation["graph"],
  value: unknown,
  resource: Swagger20Resource,
): Promise<Swagger20Object> {
  const defs: Swagger20Object = {};
  const names = new Map<string, string>();
  let next = 0;
  const schema = async (rawValue: unknown, rawResource: Swagger20Resource): Promise<Swagger20Object> => {
    if (!isSwagger20Object(rawValue)) throw new Error("Schema Object is not an object");
    const reference = stringMember(rawValue, "$ref");
    if (reference.present) {
      if (!reference.valid || reference.value === "") throw new Error("Schema Object has an invalid $ref");
      const key = `${rawResource.retrieval ?? rawResource.requested ?? ""}\u0000${reference.value}`;
      const known = names.get(key);
      if (known) return { $ref: `#/$defs/${escapePointerToken(known)}` };
      const name = `schema${next++}`;
      names.set(key, name);
      defs[name] = {};
      const resolved = await graph.resolveReference(reference.value!, rawResource);
      defs[name] = await schema(resolved.node, resolved.resource);
      return { $ref: `#/$defs/${escapePointerToken(name)}` };
    }
    const result: Swagger20Object = {};
    for (const key of Object.keys(rawValue).sort()) {
      const child = rawValue[key];
      if ((key === "items" || key === "additionalProperties") && isSwagger20Object(child)) result[key] = await schema(child, rawResource);
      else if ((key === "properties" || key === "definitions") && isSwagger20Object(child)) {
        const projected: Swagger20Object = {};
        for (const name of Object.keys(child).sort()) projected[name] = await schema(child[name], rawResource);
        result[key] = projected;
      } else if (key === "allOf" && Array.isArray(child)) result[key] = await Promise.all(child.map((branch) => schema(branch, rawResource)));
      else result[key] = structuredClone(child);
    }
    return result;
  };
  const root = await schema(value, resource);
  if (Object.keys(defs).length > 0) root.$defs = defs;
  return root;
}

function addRequirement(values: string[], value: string): void { if (!values.includes(value)) values.push(value); }
function errorMessage(error: unknown): string { return error instanceof Error ? error.message : String(error); }
