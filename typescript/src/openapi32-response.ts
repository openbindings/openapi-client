import type { OpenAPIArtifact } from "./openapi32-artifact.js";
import type { OpenAPIResolvedOperation } from "./openapi32-operations.js";
import type { OpenAPIResponse } from "./types.js";

/** The artifact-governed OpenAPI 3.2 response declaration for one final status. */
export interface OpenAPI32ResponseSelection {
  statusCode: number;
  success: boolean;
  responseKey?: string;
  response?: OpenAPIResponse;
}

/** Reports whether a Responses Object key is admitted by OpenAPI 3.2. */
export function admittedOpenAPI32ResponseKey(key: string): boolean {
  return key === "default"
    || key.startsWith("x-")
    || /^[1-5](?:[0-9]{2}|XX)$/u.test(key);
}

/**
 * Applies OpenAPI 3.2's exact, class-range, then default response lookup.
 * The declaration never changes native final-status classification.
 */
export function selectOpenAPI32Response(
  artifact: OpenAPIArtifact,
  target: OpenAPIResolvedOperation,
  statusCode: number,
): OpenAPI32ResponseSelection {
  const selection: OpenAPI32ResponseSelection = {
    statusCode,
    success: statusCode >= 200 && statusCode < 300,
  };
  if (artifact.edition !== "3.2.0") {
    throw new Error("OpenAPI 3.2 response selection requires a 3.2 artifact");
  }
  if (!target?.operation) {
    throw new Error("OpenAPI 3.2 response selection requires a resolved operation target");
  }
  const responses = target.operation.responses ?? {};
  const exact = String(statusCode);
  const range = `${Math.floor(statusCode / 100)}XX`;
  for (const key of [exact, range, "default"]) {
    const response = responses[key];
    if (response && typeof response === "object") {
      return { ...selection, responseKey: key, response };
    }
  }
  return selection;
}
