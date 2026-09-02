import { Swagger20CredentialsRequired, swagger20CredentialRequirement } from "./swagger20-context.js";
import type { ContextRequirement } from "./internal/index.js";
import { swagger20ConfigRequired } from "./swagger20-context.js";
import {
  arrayMember,
  isSwagger20Object,
  objectMember,
  stringMember,
  type Swagger20Document,
  type Swagger20Object,
  type Swagger20ResolvedOperation,
} from "./swagger20-model.js";
import {
  swagger20PercentEncode,
  type Swagger20ParameterSet,
  type Swagger20RoutedInput,
  type Swagger20WireContribution,
} from "./swagger20-parameters.js";

export interface Swagger20BasicCredential { userId: string; password: string }
export interface Swagger20OAuth2Credential { accessToken: string; scopes: string[] }
export interface Swagger20SecurityCredentials {
  basic?: Record<string, Swagger20BasicCredential>;
  apiKeys?: Record<string, string>;
  oauth2?: Record<string, Swagger20OAuth2Credential>;
}

interface Swagger20CredentialPlacement { query: boolean; name: string; value: string }

/** Selects exactly one complete security alternative and validates only its closure. */
export function selectSwagger20Security(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  parameters: Swagger20ParameterSet,
  selection: number | undefined,
  credentials: Swagger20SecurityCredentials = {},
): Swagger20CredentialPlacement[] {
  let member = arrayMember(operation.raw, "security");
  if (!member.present) member = arrayMember(document.root, "security");
  if (!member.present) return [];
  if (!member.valid) throw new Error("Swagger 2.0 effective security field is not an array");
  const requirements = member.value!;
  if (requirements.length === 0) return [];
  let selected = selection;
  if (selected === undefined) {
    if (requirements.length !== 1) {
      throw swagger20ConfigRequired("security", "");
    }
    selected = 0;
  }
  if (!Number.isSafeInteger(selected) || selected < 0 || selected >= requirements.length) {
    throw new Error(`Swagger 2.0 security alternative index ${selected} is outside the effective requirement list`);
  }
  const requirement = requirements[selected];
  if (!isSwagger20Object(requirement)) throw new Error(`Swagger 2.0 security alternative ${selected} is not an object`);
  if (Object.keys(requirement).length === 0) return [];
  const definitions = objectMember(document.root, "securityDefinitions");
  if (!definitions.valid) throw new Error("Swagger 2.0 selected security alternative has no usable root securityDefinitions object");

  const placements: Swagger20CredentialPlacement[] = [];
  const owned = new Map<string, string>();
  for (const name of Object.keys(requirement).sort()) {
    const scopes = requirement[name];
    if (!Array.isArray(scopes) || scopes.some((scope) => typeof scope !== "string")) {
      throw new Error(`Swagger 2.0 security requirement ${JSON.stringify(name)} scopes must be a string array`);
    }
    const rawDefinition = definitions.value![name];
    if (!isSwagger20Object(rawDefinition)) {
      throw new Error(`Swagger 2.0 security requirement ${JSON.stringify(name)} names no usable root definition`);
    }
    const placement = credentialPlacement(name, rawDefinition, scopes as string[], credentials, () =>
      swagger20AlternativeRequirements(requirement, definitions.value!));
    const key = `${placement.query ? "query" : "header"}\u0000${placement.query ? placement.name : placement.name.toLowerCase()}`;
    const previous = owned.get(key);
    if (previous) throw new Error(`Swagger 2.0 credentials ${JSON.stringify(previous)} and ${JSON.stringify(name)} collide at one wire destination`);
    if (credentialCollides(placement, parameters)) {
      throw new Error(`Swagger 2.0 credential ${JSON.stringify(name)} collides with an effective Parameter`);
    }
    if (!placement.query && ["host", "content-length", "content-type"].includes(placement.name.toLowerCase())) {
      throw new Error(`Swagger 2.0 credential ${JSON.stringify(name)} collides with processor-owned header ${JSON.stringify(placement.name)}`);
    }
    owned.set(key, name);
    placements.push(placement);
  }
  return placements;
}

export function applySwagger20Security(routed: Swagger20RoutedInput, placements: Swagger20CredentialPlacement[]): void {
  for (const placement of placements) {
    const contribution: Swagger20WireContribution = placement.query
      ? { name: swagger20PercentEncode(placement.name), value: swagger20PercentEncode(placement.value), valuePresent: true }
      : { name: placement.name, value: placement.value, valuePresent: true };
    if (placement.query) routed.query.push(contribution);
    else routed.headers.push(contribution);
  }
}

function credentialPlacement(
  name: string,
  definition: Swagger20Object,
  requiredScopes: string[],
  credentials: Swagger20SecurityCredentials,
  alternative: () => ContextRequirement[] | undefined,
): Swagger20CredentialPlacement {
  const type = stringMember(definition, "type");
  if (!type.valid) throw new Error(`Swagger 2.0 security definition ${JSON.stringify(name)} requires a string type`);
  if (type.value === "basic") {
    if (requiredScopes.length !== 0) throw new Error(`Swagger 2.0 basic requirement ${JSON.stringify(name)} must have an empty scopes array`);
    const credential = credentials.basic?.[name];
    if (!credential) throw missingCredentials(alternative, `Swagger 2.0 basic credential ${JSON.stringify(name)} is required`);
    if (credential.userId.includes(":") || !validBasicText(credential.userId) || !validBasicText(credential.password)) {
      throw new Error(`Swagger 2.0 basic credential ${JSON.stringify(name)} violates RFC 7617 constraints`);
    }
    return { query: false, name: "Authorization", value: `Basic ${btoa(`${credential.userId}:${credential.password}`)}` };
  }
  if (type.value === "apiKey") {
    if (requiredScopes.length !== 0) throw new Error(`Swagger 2.0 apiKey requirement ${JSON.stringify(name)} must have an empty scopes array`);
    const destination = stringMember(definition, "in");
    const wireName = stringMember(definition, "name");
    if (!destination.valid || !wireName.valid || wireName.value === "" || !["query", "header"].includes(destination.value!)) {
      throw new Error(`Swagger 2.0 apiKey definition ${JSON.stringify(name)} requires a nonempty name and query or header destination`);
    }
    if (destination.value === "header" && !httpFieldName(wireName.value!)) {
      throw new Error(`Swagger 2.0 apiKey definition ${JSON.stringify(name)} has an invalid header destination`);
    }
    if (!Object.hasOwn(credentials.apiKeys ?? {}, name)) throw missingCredentials(alternative, `Swagger 2.0 apiKey credential ${JSON.stringify(name)} is required`);
    const value = credentials.apiKeys![name]!;
    if (destination.value === "header" && !httpFieldValue(value)) {
      throw new Error(`Swagger 2.0 apiKey credential ${JSON.stringify(name)} contains a field-invalid byte`);
    }
    return { query: destination.value === "query", name: wireName.value!, value };
  }
  if (type.value === "oauth2") {
    validateOAuth2(name, definition, requiredScopes);
    const credential = credentials.oauth2?.[name];
    // An absent credential is awaited and names its resolution path; a supplied
    // one this lane cannot use is a value the caller already chose, so no
    // further context changes the answer (§3.2).
    if (!credential) {
      throw missingCredentials(alternative, `Swagger 2.0 OAuth2 credential ${JSON.stringify(name)} requires an RFC 6750 Bearer access token`);
    }
    if (!/^[A-Za-z0-9\-._~+/]+={0,}$/u.test(credential.accessToken)) {
      throw new Error(`Swagger 2.0 OAuth2 credential ${JSON.stringify(name)} requires an RFC 6750 Bearer access token`);
    }
    // R1 (ratified 2026-09-01, stated identically at openapi-2.0:567 and in all
    // three 3.x siblings): whether a supplied credential satisfies a required
    // scope is the counterparty's own determination and is never evaluated by
    // this binding. `scopes` is declared by binding-invoker 0.1 only on the
    // REQUIREMENT, never on the credential, and the three 3.x lanes evaluate no
    // scopes at all. See the Go twin.
    return { query: false, name: "Authorization", value: `Bearer ${credential.accessToken}` };
  }
  throw new Error(`Swagger 2.0 security definition ${JSON.stringify(name)} has inadmissible type ${JSON.stringify(type.value)}`);
}

function validateOAuth2(name: string, definition: Swagger20Object, requiredScopes: string[]): void {
  const flow = stringMember(definition, "flow");
  if (!flow.valid) throw new Error(`Swagger 2.0 OAuth2 definition ${JSON.stringify(name)} requires a string flow`);
  const url = (field: string): boolean => {
    const value = stringMember(definition, field);
    return value.valid && value.value !== "";
  };
  if (flow.value === "implicit" && !url("authorizationUrl")) throw new Error(`Swagger 2.0 implicit OAuth2 definition ${JSON.stringify(name)} requires authorizationUrl`);
  else if (["password", "application"].includes(flow.value!) && !url("tokenUrl")) throw new Error(`Swagger 2.0 ${flow.value} OAuth2 definition ${JSON.stringify(name)} requires tokenUrl`);
  else if (flow.value === "accessCode" && (!url("authorizationUrl") || !url("tokenUrl"))) {
    throw new Error(`Swagger 2.0 accessCode OAuth2 definition ${JSON.stringify(name)} requires authorizationUrl and tokenUrl`);
  } else if (!["implicit", "password", "application", "accessCode"].includes(flow.value!)) {
    throw new Error(`Swagger 2.0 OAuth2 definition ${JSON.stringify(name)} has inadmissible flow ${JSON.stringify(flow.value)}`);
  }
  const scopes = objectMember(definition, "scopes");
  if (!scopes.valid) throw new Error(`Swagger 2.0 OAuth2 definition ${JSON.stringify(name)} requires a scopes object`);
  for (const [scope, description] of Object.entries(scopes.value!)) {
    if (typeof description !== "string") throw new Error(`Swagger 2.0 OAuth2 definition ${JSON.stringify(name)} scope ${JSON.stringify(scope)} description is not a string`);
  }
  for (const required of requiredScopes) if (!Object.hasOwn(scopes.value!, required)) {
    throw new Error(`Swagger 2.0 OAuth2 requirement ${JSON.stringify(name)} names undeclared scope ${JSON.stringify(required)}`);
  }
}

function credentialCollides(placement: Swagger20CredentialPlacement, parameters: Swagger20ParameterSet): boolean {
  if (placement.query) return parameters.byWire.query.has(placement.name);
  return [...parameters.byWire.header.keys()].some((name) => name.toLowerCase() === placement.name.toLowerCase());
}

function validBasicText(value: string): boolean {
  return [...value].every((character) => {
    const code = character.codePointAt(0)!;
    return code >= 0x20 && code <= 0x7e;
  });
}

function httpFieldName(value: string): boolean { return /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/u.test(value); }
function httpFieldValue(value: string): boolean { return !/[\u0000-\u0008\u000a-\u001f\u007f]/u.test(value); }

/**
 * The auth requirements of one selected security alternative, in the same
 * name order the selection walks. Returns undefined where a scheme's declared
 * type has no requirement family, so the refusal stays the plain species
 * rather than naming a resolution path no runtime could take.
 */
function swagger20AlternativeRequirements(
  requirement: Swagger20Object,
  definitions: Swagger20Object,
): ContextRequirement[] | undefined {
  const result: ContextRequirement[] = [];
  for (const name of Object.keys(requirement).sort()) {
    const definition = definitions[name];
    if (!isSwagger20Object(definition)) return undefined;
    const scopes = Array.isArray(requirement[name])
      ? (requirement[name] as unknown[]).filter((scope): scope is string => typeof scope === "string")
      : [];
    const entry = swagger20CredentialRequirement(stringMember(definition, "type").value ?? "", name, scopes);
    if (entry === undefined) return undefined;
    result.push(entry);
  }
  return result.length === 0 ? undefined : result;
}

function missingCredentials(alternative: () => ContextRequirement[] | undefined, message: string): Error {
  const requirements = alternative();
  return requirements === undefined
    ? new Error(message)
    : new Swagger20CredentialsRequired(requirements, message);
}
