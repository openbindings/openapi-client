/**
 * Artifact-execution capabilities selected by a native client or adapter.
 * The engine intentionally knows capabilities, not OpenBindings binding IDs.
 */
export interface OpenAPIExecutionProfile {
  readonly name: string;
  readonly routedInputs: boolean;
  readonly mediaFidelity: boolean;
  readonly responseFidelity: boolean;
  readonly dynamicObjectCarriage: boolean;
  readonly wholeJSONCarriage: boolean;
  readonly schemaOmittedOAS30ByteCarriage: boolean;
  /** Exact private marker expected when routed input is supplied. */
  readonly inputRouteMarker: string;
}

function profile(
  name: string,
  capabilities: Partial<Omit<OpenAPIExecutionProfile, "name" | "inputRouteMarker">> = {},
): OpenAPIExecutionProfile {
  return Object.freeze({
    name,
    routedInputs: false,
    mediaFidelity: false,
    responseFidelity: false,
    dynamicObjectCarriage: false,
    wholeJSONCarriage: false,
    schemaOmittedOAS30ByteCarriage: false,
    inputRouteMarker: "openapi-client.routed@1",
    ...capabilities,
  });
}

export const OPENAPI_PROFILE_BASE = profile("base");
export const OPENAPI_PROFILE_ROUTED = profile("routed", {
  routedInputs: true,
});
export const OPENAPI_PROFILE_MEDIA = profile("media", {
  routedInputs: true,
  mediaFidelity: true,
});
export const OPENAPI_PROFILE_RESPONSE = profile("response", {
  routedInputs: true,
  mediaFidelity: true,
  responseFidelity: true,
});
export const OPENAPI_PROFILE_DYNAMIC_OBJECT = profile("dynamic-object", {
  routedInputs: true,
  mediaFidelity: true,
  responseFidelity: true,
  dynamicObjectCarriage: true,
});
export const OPENAPI_PROFILE_WHOLE_JSON = profile("whole-json", {
  routedInputs: true,
  mediaFidelity: true,
  responseFidelity: true,
  dynamicObjectCarriage: true,
  wholeJSONCarriage: true,
});
export const OPENAPI_PROFILE_FULL = profile("full", {
  routedInputs: true,
  mediaFidelity: true,
  responseFidelity: true,
  dynamicObjectCarriage: true,
  wholeJSONCarriage: true,
  schemaOmittedOAS30ByteCarriage: true,
});

/** Returns a profile with an adapter-owned private routed-input marker. */
export function withInputRouteMarker(
  base: OpenAPIExecutionProfile,
  inputRouteMarker: string,
): OpenAPIExecutionProfile {
  if (!inputRouteMarker) throw new Error("input route marker must be non-empty");
  return Object.freeze({ ...base, inputRouteMarker });
}
