import type { OpenAPIExecutionProfile } from "./profile.js";

/** Revisions whose abstract input surface uses collision-preserving routes. */
export function hasRoutedInputs(profile: OpenAPIExecutionProfile): boolean {
  return profile.routedInputs;
}

/** Revisions using the RFC 9110 request-media and carriage rules introduced by revision 3. */
export function hasMediaFidelity(profile: OpenAPIExecutionProfile): boolean {
  return profile.mediaFidelity;
}

/** Revisions that admit response media ranges and exact raw-byte output carriage. */
export function hasResponseFidelity(profile: OpenAPIExecutionProfile): boolean {
  return profile.responseFidelity;
}

/** Revisions that preserve explicitly dynamic object bodies as one application value. */
export function hasDynamicObjectCarriage(profile: OpenAPIExecutionProfile): boolean {
  return profile.dynamicObjectCarriage;
}

/** Revisions that route declaration-complex JSON schemas as one application value. */
export function hasWholeJSONCarriage(profile: OpenAPIExecutionProfile): boolean {
  return profile.wholeJSONCarriage;
}

/** Revisions that carry exact schema-omitted OAS 3.0 non-JSON representations as bytes. */
export function hasSchemaOmittedOAS30ByteCarriage(profile: OpenAPIExecutionProfile): boolean {
  return profile.schemaOmittedOAS30ByteCarriage;
}

/** Set of valid HTTP methods recognized in OpenAPI path items. */
export const VALID_METHODS = new Set([
  "get", "post", "put", "patch", "delete", "head", "options", "trace",
]);
