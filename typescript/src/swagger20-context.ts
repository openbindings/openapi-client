import { ConfigRequired } from "./servers.js";
import { Swagger20ExecutionError } from "./swagger20-engine.js";
import { configValueRequirement, type ContextRequirement } from "./internal/index.js";
import { CONTEXT_REQUIRED, ERR_REFUSED } from "./internal/errcodes.js";

/**
 * openbindings.openapi-2.0@1 §3.2 gives a pre-dispatch refusal two species:
 * **context-required**, "where a named configuration point or credential is
 * awaited and the refusal carries its own resolution path", and plain
 * **refusal**, "where no supplied context could change the answer". Both occur
 * before dispatch and both guarantee no observable side effect, so the species
 * changes what a refusal CARRIES and never whether it happens.
 *
 * This module carries that answer out from the sites that know it. The
 * condition is decided where it always was — the parameter, media, form,
 * server, and security lanes — and each of those sites already knows whether a
 * §12.1 configuration point or a declared credential would repair it. This is
 * the twin of the Go engine's `swagger20_context.go`; the boundary strings
 * below are the shared authored shape both engines emit.
 */

/**
 * The scheme names and scopes of one selected security alternative whose
 * credentials the caller has not supplied. §12.1 lists no configuration point
 * for a credential, so these are auth-family requirements. Every scheme of the
 * alternative is carried, because an alternative is an AND: a resolution path
 * naming one of two required credentials is not a resolution path.
 */
export class Swagger20CredentialsRequired extends Error {
  constructor(readonly requirements: ContextRequirement[], message: string) {
    super(message);
    this.name = "Swagger20CredentialsRequired";
  }
}

/**
 * States the point's own documented boundary and nothing more
 * (openbindings.openapi-2.0@1 §12.1). `emptyValueForm` is "exactly
 * `name-only` or `empty`", which is a closed admissible set and therefore an
 * `enum`; `security` is "one complete declared alternative" selected by index;
 * `server` states no shape here, and an absent schema means unconstrained
 * rather than unknown.
 */
export function swagger20ConfigurationDescription(point: string): string {
  return point === "propertyMedia"
    ? "select one concrete media type for this present file form parameter"
    : `supply the Swagger 2.0 ${point} configuration value`;
}

export function swagger20ConfigurationSchema(point: string): Record<string, unknown> | undefined {
  switch (point) {
    case "requestMedia":
    case "propertyMedia":
      return { type: "string" };
    case "security":
      return {
        type: "object",
        properties: { index: { type: "integer", minimum: 0 } },
        required: ["index"],
        additionalProperties: false,
      };
    case "emptyValueForm":
      return { enum: ["name-only", "empty"] };
    default:
      return undefined;
  }
}

/**
 * Names a §12.1 configuration point the selected target needs and the caller
 * has not supplied. It reuses {@link ConfigRequired} so this lane's challenge
 * is built by the same constructor the OpenAPI 3.x lane uses.
 */
export function swagger20ConfigRequired(point: string, path: string): ConfigRequired {
  return new ConfigRequired(
    point,
    path,
    swagger20ConfigurationDescription(point),
    swagger20ConfigurationSchema(point),
    true,
  );
}

export function swagger20ConfigurationRequirement(point: string, path: string): ContextRequirement {
  return configValueRequirement(
    point,
    path,
    swagger20ConfigurationDescription(point),
    swagger20ConfigurationSchema(point),
    true,
  );
}

/** Builds the auth requirement for one declared Swagger 2.0 security scheme. */
export function swagger20CredentialRequirement(
  schemeType: string,
  name: string,
  scopes: readonly string[],
): ContextRequirement | undefined {
  const type = schemeType === "basic" ? "auth.basic"
    : schemeType === "apiKey" ? "auth.apiKey"
      : schemeType === "oauth2" ? "auth.oauth2"
        : undefined;
  if (type === undefined) return undefined;
  const requirement: ContextRequirement = { type, name };
  if (type === "auth.oauth2" && scopes.length > 0) requirement.scopes = [...scopes];
  return requirement;
}

/**
 * Applies §3.2's discriminator to one pre-dispatch refusal. A refusal a named
 * §12.1 point or a declared credential would repair is the context-required
 * species and carries that resolution path; every other refusal is the plain
 * species and carries nothing. `target` is the asserted context scope, which
 * for this lane is the source location the caller supplied — the same scope
 * the side-effect-free preflight asserts.
 */
export function swagger20RefusalError(error: unknown, target: string): Swagger20ExecutionError {
  if (error instanceof Swagger20ExecutionError) return error;
  if (error instanceof ConfigRequired) {
    return new Swagger20ExecutionError(CONTEXT_REQUIRED, error.message, {
      cause: error,
      details: {
        target,
        alternatives: [{
          requirements: [
            configValueRequirement(error.point, error.path, error.message, error.schema, error.durable),
          ],
        }],
      },
    });
  }
  if (error instanceof Swagger20CredentialsRequired) {
    return new Swagger20ExecutionError(CONTEXT_REQUIRED, error.message, {
      cause: error,
      details: { target, alternatives: [{ requirements: error.requirements }] },
    });
  }
  return new Swagger20ExecutionError(ERR_REFUSED, error instanceof Error ? error.message : String(error), { cause: error });
}
