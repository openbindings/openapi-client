/**
 * Standalone, document-driven OpenAPI client.
 *
 * The package exposes one OpenAPI-native application surface. Parser models,
 * execution internals, OpenBindings adapters, and synthesis structures are
 * intentionally private.
 */
export {
  OpenAPIClient,
  OpenAPIClientError,
} from "./client.js";
export type {
  HTTPMethod,
  OpenAPIAuthValue,
  OpenAPICallInput,
  OpenAPICallOptions,
  OpenAPIClientErrorKind,
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
  OpenAPIOperationInfo,
  OpenAPIOperationSelector,
  OpenAPIParameterInput,
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
