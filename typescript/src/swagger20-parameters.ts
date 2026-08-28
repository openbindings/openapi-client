import {
  Swagger20Number,
  arrayMember,
  booleanMember,
  isSwagger20Object,
  member,
  objectMember,
  pathItemMemberResource,
  stringMember,
  type Swagger20Object,
  type Swagger20ResolvedOperation,
  type Swagger20Resource,
} from "./swagger20-model.js";
import { newSwagger20ResolutionMemo } from "./swagger20-reference.js";

export type Swagger20ParameterLocation = "path" | "query" | "header" | "formData" | "body";

export interface Swagger20ParameterInfo {
  name: string;
  in: Swagger20ParameterLocation;
  type: string;
  required: boolean;
}

export interface Swagger20Parameters {
  path?: Record<string, unknown>;
  query?: Record<string, unknown>;
  header?: Record<string, unknown>;
  formData?: Record<string, unknown>;
}

/** Edition-native input. Body presence distinguishes JSON null from omission. */
export interface Swagger20Input {
  parameters?: Swagger20Parameters;
  body?: unknown;
  bodyPresent?: boolean;
}

export type Swagger20EmptyValueForm = "name-only" | "empty";
export type Swagger20ParameterConverter = (value: boolean | number | null | Swagger20Number) => string;

/** @internal */
export interface Swagger20Parameter {
  raw: Swagger20Object;
  resource: Swagger20Resource;
  name: string;
  in: Swagger20ParameterLocation;
  typeName: string;
  required: boolean;
  allowEmptyValue: boolean;
  collectionFormat: string;
  items?: Swagger20Items;
}

interface Swagger20Items {
  raw: Swagger20Object;
  typeName: string;
  items?: Swagger20Items;
}

/** @internal */
export interface Swagger20ParameterSet {
  all: Swagger20Parameter[];
  nonBody: Swagger20Parameter[];
  body?: Swagger20Parameter;
  byWire: Record<Exclude<Swagger20ParameterLocation, "body">, Map<string, Swagger20Parameter>>;
  qualified: boolean;
}

/** @internal */
export interface Swagger20WireContribution {
  name: string;
  value: string;
  valuePresent: boolean;
  structuralDelimiter?: string;
  parameter?: Swagger20Parameter;
  octets?: Uint8Array;
}

/** @internal */
export interface Swagger20RoutedInput {
  resolvedPath: string;
  query: Swagger20WireContribution[];
  headers: Swagger20WireContribution[];
  formData: Swagger20WireContribution[];
  body?: unknown;
  bodyPresent: boolean;
  formPresent: boolean;
}

/** Resolves override scope and every declaration-only parameter exclusion. */
export async function effectiveSwagger20Parameters(operation: Swagger20ResolvedOperation): Promise<Swagger20ParameterSet> {
  const pathParameters = await parameterScope(
    operation,
    arrayMember(operation.pathItem.raw, "parameters"),
    pathItemMemberResource(operation.pathItem, "parameters"),
    "Path Item",
  );
  const operationParameters = await parameterScope(
    operation,
    arrayMember(operation.raw, "parameters"),
    operation.resource,
    "Operation",
  );
  const overridden = new Set(operationParameters.map(parameterIdentity));
  const effective = [...pathParameters.filter((parameter) => !overridden.has(parameterIdentity(parameter))), ...operationParameters];
  const byWire: Swagger20ParameterSet["byWire"] = {
    path: new Map(), query: new Map(), header: new Map(), formData: new Map(),
  };
  const set: Swagger20ParameterSet = { all: effective, nonBody: [], byWire, qualified: false };
  const names = new Map<string, Swagger20ParameterLocation>();
  const headers = new Map<string, string>();
  let bodyCount = 0;
  let formCount = 0;
  for (const parameter of effective) {
    validateParameterDeclaration(parameter);
    if (parameter.in === "body") {
      bodyCount++;
      set.body = parameter;
      continue;
    }
    if (parameter.in === "formData") formCount++;
    set.nonBody.push(parameter);
    byWire[parameter.in].set(parameter.name, parameter);
    const previous = names.get(parameter.name);
    if (previous !== undefined && previous !== parameter.in) set.qualified = true;
    else names.set(parameter.name, parameter.in);
    if (parameter.in === "header") {
      const folded = parameter.name.toLowerCase();
      const prior = headers.get(folded);
      if (prior !== undefined && prior !== parameter.name) {
        throw new Error(`effective header parameters ${JSON.stringify(prior)} and ${JSON.stringify(parameter.name)} differ only by ASCII case`);
      }
      headers.set(folded, parameter.name);
    }
  }
  if (bodyCount > 1) throw new Error("effective parameter set contains more than one body parameter");
  if (bodyCount > 0 && formCount > 0) throw new Error("effective parameter set mixes body and formData parameters");
  validatePathParameters(operation.path, byWire.path);
  return set;
}

export function swagger20ParameterInfos(set: Swagger20ParameterSet): Swagger20ParameterInfo[] {
  return set.all.map((parameter) => ({
    name: parameter.name,
    in: parameter.in,
    type: parameter.typeName,
    required: parameter.required,
  }));
}

async function parameterScope(
  operation: Swagger20ResolvedOperation,
  declaration: ReturnType<typeof arrayMember>,
  resource: Swagger20Resource,
  owner: string,
): Promise<Swagger20Parameter[]> {
  if (!declaration.present) return [];
  if (!declaration.valid) throw new Error(`selected Swagger 2.0 ${owner} parameters field is not an array`);
  const parameters: Swagger20Parameter[] = [];
  const identities = new Set<string>();
  for (let index = 0; index < declaration.value!.length; index++) {
    const parameter = await resolveParameter(operation, declaration.value![index], resource, new Set());
    if (parameter.name === "" || !["path", "query", "header", "formData", "body"].includes(parameter.in)) {
      throw new Error(`selected Swagger 2.0 ${owner} parameter ${index} requires nonempty name and in`);
    }
    const identity = parameterIdentity(parameter);
    if (identities.has(identity)) throw new Error(`selected Swagger 2.0 ${owner} repeats parameter identity ${identity}`);
    identities.add(identity);
    parameters.push(parameter);
  }
  return parameters;
}

async function resolveParameter(
  operation: Swagger20ResolvedOperation,
  value: unknown,
  resource: Swagger20Resource,
  active: Set<string>,
): Promise<Swagger20Parameter> {
  if (!isSwagger20Object(value)) throw new Error("Parameter or Reference Object is not an object");
  const reference = stringMember(value, "$ref");
  if (reference.present) {
    if (!reference.valid || reference.value === "") throw new Error("Parameter Reference Object has an invalid $ref");
    const key = `${resource.retrieval ?? resource.requested ?? ""}|parameter|${reference.value}`;
    if (active.has(key)) throw new Error("selected Parameter reference cycle is not resolvable");
    active.add(key);
    try {
      const resolved = await operation.graph.resolveReference(reference.value!, resource, newSwagger20ResolutionMemo());
      if (resolved.cycle) throw new Error("selected Parameter reference cycle is not resolvable");
      return resolveParameter(operation, resolved.node, resolved.resource, active);
    } finally {
      active.delete(key);
    }
  }
  const name = stringMember(value, "name");
  const location = stringMember(value, "in");
  return {
    raw: value,
    resource,
    name: name.value ?? "",
    in: (location.value ?? "") as Swagger20ParameterLocation,
    typeName: "",
    required: false,
    allowEmptyValue: false,
    collectionFormat: "",
  };
}

function validateParameterDeclaration(parameter: Swagger20Parameter): void {
  const name = stringMember(parameter.raw, "name");
  const location = stringMember(parameter.raw, "in");
  if (!name.valid || name.value === "" || !location.valid) throw new Error("name and in are required with string values");
  parameter.name = name.value!;
  parameter.in = location.value as Swagger20ParameterLocation;
  if (!["path", "query", "header", "formData", "body"].includes(parameter.in)) {
    throw new Error(`in value ${JSON.stringify(location.value)} is not admitted`);
  }
  const required = booleanMember(parameter.raw, "required");
  if (required.present && !required.valid) throw new Error("required is not a boolean");
  parameter.required = required.value === true;
  if (parameter.in === "path" && !parameter.required) throw new Error("path parameters require required: true");
  if (parameter.in === "body") {
    if (!objectMember(parameter.raw, "schema").valid) throw new Error("body parameter requires an object schema");
    return;
  }
  const type = stringMember(parameter.raw, "type");
  if (!type.valid) throw new Error("non-body parameter requires string type");
  parameter.typeName = type.value!;
  if (!["string", "number", "integer", "boolean", "array"].includes(parameter.typeName)
    && !(parameter.typeName === "file" && parameter.in === "formData")) {
    throw new Error(`type ${JSON.stringify(parameter.typeName)} is not admitted for ${parameter.in}`);
  }
  const allowEmpty = booleanMember(parameter.raw, "allowEmptyValue");
  if (allowEmpty.present && !allowEmpty.valid) throw new Error("allowEmptyValue is not a boolean");
  if (allowEmpty.present && parameter.in !== "query" && parameter.in !== "formData") {
    throw new Error("allowEmptyValue applies only to query and formData");
  }
  parameter.allowEmptyValue = allowEmpty.value === true;
  const collection = stringMember(parameter.raw, "collectionFormat");
  if (collection.present && !collection.valid) throw new Error("collectionFormat is not a string");
  if (parameter.typeName === "array") {
    const items = objectMember(parameter.raw, "items");
    if (!items.valid) throw new Error("array parameter requires an Items Object");
    parameter.items = parseItems(items.value!);
    if (parameter.items.typeName === "array") throw new Error("nested non-body arrays have no unambiguous collection serialization");
    parameter.collectionFormat = collection.value ?? "csv";
    if (!["csv", "ssv", "tsv", "pipes", "multi"].includes(parameter.collectionFormat)) {
      throw new Error(`collectionFormat ${JSON.stringify(parameter.collectionFormat)} is not admitted`);
    }
    if (parameter.collectionFormat === "multi" && parameter.in !== "query" && parameter.in !== "formData") {
      throw new Error(`collectionFormat multi is not admitted for ${parameter.in}`);
    }
  } else if (collection.present) {
    throw new Error("collectionFormat applies only to array parameters");
  }
  if (parameter.in === "header") {
    if (!httpFieldName(parameter.name)) throw new Error("header name is not an HTTP field-name");
    if (["host", "content-length", "content-type"].includes(parameter.name.toLowerCase())) {
      throw new Error(`header parameter ${JSON.stringify(parameter.name)} collides with a processor-owned field`);
    }
  }
  validateAssertionDeclaration(parameter.raw);
  if (member(parameter.raw, "default").present) validateDeclaredValue(parameter, parameter.raw.default);
}

function parseItems(raw: Swagger20Object): Swagger20Items {
  const type = stringMember(raw, "type");
  if (!type.valid) throw new Error("Items Object requires string type");
  if (!["string", "number", "integer", "boolean", "array"].includes(type.value!)) {
    throw new Error(`Items Object type ${JSON.stringify(type.value)} is not admitted`);
  }
  const result: Swagger20Items = { raw, typeName: type.value! };
  if (result.typeName === "array") {
    const nested = objectMember(raw, "items");
    if (!nested.valid) throw new Error("nested array Items Object requires items");
    result.items = parseItems(nested.value!);
  }
  validateAssertionDeclaration(raw);
  if (member(raw, "default").present) validateItemsValue(result, raw.default);
  return result;
}

/** Routes native, location-separated values into exact wire contributions. */
export function routeSwagger20Input(
  set: Swagger20ParameterSet,
  path: string,
  input: Swagger20Input,
  converter: Swagger20ParameterConverter | undefined,
  emptyValueForm: Swagger20EmptyValueForm | undefined,
): Swagger20RoutedInput {
  const supplied = input.parameters ?? {};
  const provided: Swagger20Parameters = {
    path: supplied.path ?? {}, query: supplied.query ?? {}, header: supplied.header ?? {}, formData: supplied.formData ?? {},
  };
  for (const location of ["path", "query", "header", "formData"] as const) {
    for (const name of Object.keys(provided[location]!)) {
      if (!set.byWire[location].has(name)) throw new Error(`unknown ${location} parameter ${JSON.stringify(name)}`);
    }
  }
  const bodyPresent = input.bodyPresent === true;
  if (!set.body && bodyPresent) throw new Error("body was supplied but the operation has no body parameter");
  if (set.body?.required && !bodyPresent) throw new Error("required body is missing");
  const routed: Swagger20RoutedInput = {
    resolvedPath: path, query: [], headers: [], formData: [], body: input.body, bodyPresent, formPresent: false,
  };
  const parameters = [...set.nonBody].sort((left, right) =>
    left.in.localeCompare(right.in) || left.name.localeCompare(right.name));
  for (const parameter of parameters) {
    if (parameter.in === "body") continue;
    const values = provided[parameter.in]!;
    if (!Object.hasOwn(values, parameter.name)) {
      if (parameter.required) throw new Error(`required ${parameter.in} parameter ${JSON.stringify(parameter.name)} is missing`);
      continue;
    }
    const contributions = serializeParameter(parameter, values[parameter.name], converter, emptyValueForm);
    if (parameter.in === "path") {
      if (contributions.length !== 1 || !contributions[0]!.valuePresent) throw new Error(`path parameter ${JSON.stringify(parameter.name)} did not produce one value`);
      routed.resolvedPath = routed.resolvedPath.replaceAll(`{${parameter.name}}`, encodeContribution(contributions[0]!));
    } else if (parameter.in === "query") {
      routed.query.push(...contributions.map((contribution) => ({
        ...contribution,
        name: swagger20PercentEncode(contribution.name),
        value: contribution.valuePresent ? encodeContribution(contribution) : "",
      })));
    } else if (parameter.in === "header") {
      for (const contribution of contributions) {
        if (!httpFieldValue(contribution.value)) throw new Error(`header parameter ${JSON.stringify(parameter.name)} contains a field-invalid byte`);
        routed.headers.push(contribution);
      }
    } else {
      routed.formPresent = true;
      routed.formData.push(...contributions);
    }
  }
  return routed;
}

function serializeParameter(
  parameter: Swagger20Parameter,
  value: unknown,
  converter: Swagger20ParameterConverter | undefined,
  emptyValueForm: Swagger20EmptyValueForm | undefined,
): Swagger20WireContribution[] {
  if (parameter.typeName === "file") {
    if (typeof value !== "string") throw new Error(`parameter ${JSON.stringify(parameter.name)} requires a canonical Base64 file value`);
    return [{ name: parameter.name, value: "", valuePresent: true, parameter, octets: canonicalBase64Bytes(value) }];
  }
  const converted = validateAndConvert(parameter, value, converter);
  if (parameter.typeName !== "array" && converted[0] === "") {
    if (!parameter.allowEmptyValue) throw new Error(`parameter ${JSON.stringify(parameter.name)} does not admit an empty string`);
    if (emptyValueForm === "name-only") return [{ name: parameter.name, value: "", valuePresent: false, parameter }];
    if (emptyValueForm === "empty") return [{ name: parameter.name, value: "", valuePresent: true, parameter }];
    throw new Error(`parameter ${JSON.stringify(parameter.name)} requires emptyValueForm name-only or empty`);
  }
  if (parameter.typeName !== "array") {
    return [{ name: parameter.name, value: converted[0]!, valuePresent: true, parameter }];
  }
  if (parameter.collectionFormat === "multi") {
    return converted.map((value) => ({ name: parameter.name, value, valuePresent: true, parameter }));
  }
  const delimiter = collectionDelimiter(parameter.collectionFormat);
  converted.forEach((value, index) => {
    if (value.includes(delimiter)) throw new Error(`parameter ${JSON.stringify(parameter.name)} array member ${index} contains its structural delimiter`);
  });
  return [{
    name: parameter.name,
    value: converted.join(delimiter),
    valuePresent: true,
    structuralDelimiter: delimiter,
    parameter,
  }];
}

function validateAndConvert(
  parameter: Swagger20Parameter,
  value: unknown,
  converter: Swagger20ParameterConverter | undefined,
): string[] {
  if (parameter.typeName === "array") {
    if (!Array.isArray(value)) throw new Error(`parameter ${JSON.stringify(parameter.name)} requires a JSON array`);
    validateAssertions(parameter.raw, value, "array");
    return value.map((item, index) => {
      if (item !== null) {
        validateValueType(item, parameter.items!.typeName);
        validateAssertions(parameter.items!.raw, item, parameter.items!.typeName);
      }
      try { return convertScalar(item, converter); }
      catch (error: unknown) { throw new Error(`parameter ${JSON.stringify(parameter.name)} array member ${index}: ${errorMessage(error)}`); }
    });
  }
  if (value !== null) {
    validateValueType(value, parameter.typeName);
    validateAssertions(parameter.raw, value, parameter.typeName);
  }
  return [convertScalar(value, converter)];
}

function validateDeclaredValue(parameter: Swagger20Parameter, value: unknown): void {
  if (parameter.typeName === "file" || value === null) throw new Error("default does not conform to the declared type");
  if (parameter.typeName === "array") {
    if (!Array.isArray(value)) throw new Error("default is not an array");
    value.forEach((item) => validateItemsValue(parameter.items!, item));
  } else validateValueType(value, parameter.typeName);
  validateAssertions(parameter.raw, value, parameter.typeName);
}

function validateItemsValue(items: Swagger20Items, value: unknown): void {
  if (value === null) throw new Error("Items Object default is null");
  validateValueType(value, items.typeName);
  validateAssertions(items.raw, value, items.typeName);
  if (items.typeName === "array") (value as unknown[]).forEach((item) => validateItemsValue(items.items!, item));
}

function validateAssertionDeclaration(raw: Swagger20Object): void {
  for (const name of ["multipleOf", "maximum", "minimum"]) {
    if (member(raw, name).present && !finiteNumber(raw[name])) throw new Error(`${name} is not a finite number`);
  }
  for (const name of ["exclusiveMaximum", "exclusiveMinimum", "uniqueItems"]) {
    const value = booleanMember(raw, name);
    if (value.present && !value.valid) throw new Error(`${name} is not a boolean`);
  }
  for (const name of ["maxLength", "minLength", "maxItems", "minItems"]) {
    if (member(raw, name).present && (!literalInteger(raw[name]) || numberValue(raw[name])! < 0)) {
      throw new Error(`${name} is not a nonnegative integer`);
    }
  }
  const pattern = stringMember(raw, "pattern");
  if (pattern.present) {
    if (!pattern.valid) throw new Error("pattern is not a string");
    try { new RegExp(pattern.value!, "u"); } catch (error: unknown) { throw new Error("pattern is not an ECMA-262 regular expression", { cause: error }); }
  }
  if (member(raw, "enum").present && (!Array.isArray(raw.enum) || raw.enum.length === 0)) throw new Error("enum is not a nonempty array");
}

function validateValueType(value: unknown, type: string): void {
  if (type === "string" && typeof value !== "string") throw new Error("value is not string");
  if (type === "boolean" && typeof value !== "boolean") throw new Error("value is not boolean");
  if (type === "number" && !finiteNumber(value)) throw new Error("value is not a finite number");
  if (type === "integer" && !literalInteger(value)) throw new Error("value is not a literal JSON integer");
  if (type === "array" && !Array.isArray(value)) throw new Error("value is not array");
}

function validateAssertions(raw: Swagger20Object, value: unknown, type: string): void {
  if (Array.isArray(raw.enum) && !raw.enum.some((candidate) => jsonEqual(value, candidate))) throw new Error("value is outside enum");
  if (type === "number" || type === "integer") {
    const number = numberValue(value)!;
    if (raw.multipleOf !== undefined && number % numberValue(raw.multipleOf)! !== 0) throw new Error("value violates multipleOf");
    if (raw.maximum !== undefined) {
      const maximum = numberValue(raw.maximum)!;
      if (number > maximum || (number === maximum && raw.exclusiveMaximum === true)) throw new Error("value exceeds maximum");
    }
    if (raw.minimum !== undefined) {
      const minimum = numberValue(raw.minimum)!;
      if (number < minimum || (number === minimum && raw.exclusiveMinimum === true)) throw new Error("value is below minimum");
    }
  } else if (type === "string") {
    const length = [...value as string].length;
    if (raw.maxLength !== undefined && length > numberValue(raw.maxLength)!) throw new Error("string exceeds maxLength");
    if (raw.minLength !== undefined && length < numberValue(raw.minLength)!) throw new Error("string is below minLength");
    if (typeof raw.pattern === "string" && !new RegExp(raw.pattern, "u").test(value as string)) throw new Error("string does not match pattern");
  } else if (type === "array") {
    const array = value as unknown[];
    if (raw.maxItems !== undefined && array.length > numberValue(raw.maxItems)!) throw new Error("array exceeds maxItems");
    if (raw.minItems !== undefined && array.length < numberValue(raw.minItems)!) throw new Error("array is below minItems");
    if (raw.uniqueItems === true && array.some((item, index) => array.slice(0, index).some((prior) => jsonEqual(item, prior)))) {
      throw new Error("array violates uniqueItems");
    }
  }
}

function convertScalar(value: unknown, converter: Swagger20ParameterConverter | undefined): string {
  if (typeof value === "string") return value;
  if (value !== null && typeof value !== "boolean" && typeof value !== "number" && !(value instanceof Swagger20Number)) {
    throw new Error("value is outside the JSON scalar conversion domain");
  }
  if (!converter) throw new Error("JSON boolean, number, or null requires parameterConversion");
  return converter(value as boolean | number | null | Swagger20Number);
}

function validatePathParameters(path: string, parameters: Map<string, Swagger20Parameter>): void {
  const expressions = new Set<string>();
  for (let index = 0; index < path.length;) {
    if (path[index] === "{") {
      const end = path.indexOf("}", index + 1);
      if (end < 0) throw new Error("path template has an unterminated expression");
      const name = path.slice(index + 1, end);
      if (name === "" || /[{}]/u.test(name)) throw new Error("path template has a malformed expression");
      expressions.add(name);
      index = end + 1;
    } else if (path[index] === "}") throw new Error("path template has an unmatched closing brace");
    else index++;
  }
  for (const name of expressions) if (!parameters.has(name)) throw new Error(`path template expression ${JSON.stringify(name)} has no effective path parameter`);
  for (const name of parameters.keys()) if (!expressions.has(name)) throw new Error(`effective path parameter ${JSON.stringify(name)} has no matching template expression`);
}

function parameterIdentity(parameter: Swagger20Parameter): string {
  return `${parameter.in}\u0000${parameter.name}`;
}

function collectionDelimiter(format: string): string {
  return format === "ssv" ? " " : format === "tsv" ? "\t" : format === "pipes" ? "|" : ",";
}

export function swagger20RawQuery(contributions: Swagger20WireContribution[]): string {
  return contributions.map((contribution) =>
    contribution.name + (contribution.valuePresent ? `=${contribution.value}` : "")).join("&");
}

export function swagger20PercentEncode(value: string): string {
  return [...new TextEncoder().encode(value)].map((byte) =>
    isUnreserved(byte) ? String.fromCharCode(byte) : `%${byte.toString(16).toUpperCase().padStart(2, "0")}`).join("");
}

function encodeContribution(contribution: Swagger20WireContribution): string {
  return contribution.structuralDelimiter
    ? contribution.value.split(contribution.structuralDelimiter).map(swagger20PercentEncode).join(contribution.structuralDelimiter)
    : swagger20PercentEncode(contribution.value);
}

function isUnreserved(byte: number): boolean {
  return byte >= 0x41 && byte <= 0x5a || byte >= 0x61 && byte <= 0x7a || byte >= 0x30 && byte <= 0x39
    || byte === 0x2d || byte === 0x2e || byte === 0x5f || byte === 0x7e;
}

function httpFieldName(name: string): boolean {
  return /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/u.test(name);
}

function httpFieldValue(value: string): boolean {
  return !/[\u0000-\u0008\u000a-\u001f\u007f]/u.test(value);
}

function finiteNumber(value: unknown): boolean {
  return numberValue(value) !== undefined;
}

function numberValue(value: unknown): number | undefined {
  if (value instanceof Swagger20Number) return value.value;
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function literalInteger(value: unknown): boolean {
  if (value instanceof Swagger20Number) return /^-?(?:0|[1-9][0-9]*)$/u.test(value.lexeme);
  return typeof value === "number" && Number.isSafeInteger(value);
}

function jsonEqual(left: unknown, right: unknown): boolean {
  if (finiteNumber(left) && finiteNumber(right)) return numberValue(left) === numberValue(right);
  return JSON.stringify(left) === JSON.stringify(right);
}

export function canonicalBase64Bytes(value: string): Uint8Array {
  if (!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u.test(value)) {
    throw new Error("value is not canonical padded Base64");
  }
  const bytes = Uint8Array.from(atob(value), (character) => character.charCodeAt(0));
  if (btoa(String.fromCharCode(...bytes)) !== value) throw new Error("value has nonzero unused Base64 pad bits");
  return bytes;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
