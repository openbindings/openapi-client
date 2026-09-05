/**
 * Standalone, document-driven OpenAPI client.
 *
 * The package exposes one OpenAPI-native application surface. Parser models,
 * execution internals, OpenBindings adapters, and synthesis structures are
 * intentionally private.
 */
export {
  OPENAPI_USE_DEFAULT,
  OpenAPIClient,
  OpenAPIClientError,
} from "./client.js";
export type {
  HTTPMethod,
  OpenAPIAuthValue,
  OpenAPIAnalysis,
  OpenAPICallInput,
  OpenAPICallOptions,
  OpenAPIClientErrorKind,
  OpenAPIClientHookResult,
  OpenAPIClientHooks,
  OpenAPIClientHookSite,
  OpenAPIConfigurationRequirement,
  OpenAPIClientMiddleware,
  OpenAPIClientOptions,
  OpenAPIConfigurationRequirements,
  OpenAPIContentCodec,
  OpenAPIContentCodingResult,
  OpenAPIDeclarationMatch,
  OpenAPIEdition,
  OpenAPIEmptyValueForm,
  OpenAPIFailureResult,
  OpenAPIOperationClient,
  OpenAPIOperationAnalysis,
  OpenAPIOperationInfo,
  OpenAPIOperationSelector,
  OpenAPIParameterInput,
  OpenAPIParameterInfo,
  OpenAPIRequestBodyAnalysis,
  OpenAPIResult,
  OpenAPISecurityHandler,
  OpenAPISecurityHandlerContext,
  OpenAPIServerSelection,
  OpenAPISource,
  OpenAPIStreamEvent,
  OpenAPIStreamResult,
  OpenAPIStreamSuccessResult,
  OpenAPISuccessResult,
} from "./client.js";
export type {
  OpenAPIHostRequest,
  OpenAPIHostTransport,
  OpenAPIPlannedRequest,
  OpenAPIRedirectPolicy,
} from "./host-transport.js";
export type { OpenAPIParameterConverter } from "./params.js";
export type { OpenAPICharacterDecoder, OpenAPICharacterEncoder } from "./response-mechanics.js";
