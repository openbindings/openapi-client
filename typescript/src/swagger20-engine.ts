import {
  SWAGGER20_METHODS,
  Swagger20Document,
  isSwagger20Object,
  objectMember,
  operationInfo,
  pathItemMemberResource,
  type Swagger20LoadOptions,
  type Swagger20Method,
  type Swagger20OperationInfo,
  type Swagger20ResolvedOperation,
  type Swagger20Source,
} from "./swagger20-model.js";
import { loadSwagger20Document } from "./swagger20-loader.js";
import { decodePointerToken, escapePointerToken, newSwagger20ResolutionMemo, wellFormedPointerToken } from "./swagger20-reference.js";

export interface Swagger20PrepareOptions extends Swagger20LoadOptions {
  source: Swagger20Source;
  ref: string;
  /** OpenAPI-native execution configuration; later passes fill this surface. */
  context?: Record<string, unknown>;
}

/** A failure produced by the exact Swagger 2.0 engine lane. */
export class Swagger20ExecutionError extends Error {
  readonly code: string;
  readonly details?: unknown;
  readonly evidence?: unknown;

  constructor(code: string, message: string, options: { cause?: unknown; details?: unknown; evidence?: unknown } = {}) {
    super(message, options.cause === undefined ? undefined : { cause: options.cause });
    this.name = "Swagger20ExecutionError";
    this.code = code;
    if (options.details !== undefined) this.details = options.details;
    if (options.evidence !== undefined) this.evidence = options.evidence;
  }
}

/** One selected operation in the edition-specific native model. */
export class PreparedSwagger20Operation {
  readonly document: Swagger20Document;
  readonly operation: Swagger20ResolvedOperation;
  readonly info: Swagger20OperationInfo;
  /** @internal */ readonly options: Swagger20PrepareOptions;

  /** @internal */
  constructor(document: Swagger20Document, operation: Swagger20ResolvedOperation, info: Swagger20OperationInfo, options: Swagger20PrepareOptions) {
    this.document = document;
    this.operation = operation;
    this.info = info;
    this.options = options;
  }

  /** Pass-one boundary: later edition files add native request execution. */
  async execute(_input?: unknown): Promise<{ outputPresent: boolean; output?: unknown }> {
    throw new Swagger20ExecutionError("ERR_REFUSED", "Swagger 2.0 operation execution requires request-surface preparation");
  }
}

/** Loads and selects one operation through only the exact Swagger 2.0 lane. */
export async function prepareSwagger20(options: Swagger20PrepareOptions): Promise<PreparedSwagger20Operation> {
  let document: Swagger20Document;
  try {
    document = await loadSwagger20Document(options.source, options);
  } catch (error: unknown) {
    throw new Swagger20ExecutionError("SOURCE_LOAD_FAILED", errorMessage(error), { cause: error });
  }
  const operation = await resolveSwagger20Operation(document, options.ref);
  return new PreparedSwagger20Operation(document, operation, operationInfo(operation, options.ref), options);
}

/** Exact selector validation without artifact loading. */
export function validateSwagger20Selector(ref: string): void {
  parseSwagger20Ref(ref);
}

/** Enumerates every resolvable fixed Swagger 2.0 operation slot. */
export async function listSwagger20Operations(document: Swagger20Document): Promise<Swagger20OperationInfo[]> {
  const paths = objectMember(document.root, "paths");
  if (!paths.valid) return [];
  const result: Swagger20OperationInfo[] = [];
  for (const path of Object.keys(paths.value!).sort()) {
    for (const method of SWAGGER20_METHODS) {
      const ref = `#/paths/${escapePointerToken(path)}/${method}`;
      try {
        const operation = await resolveSwagger20Operation(document, ref);
        result.push(operationInfo(operation, ref));
      } catch { /* smallest-owner confinement omits only this target */ }
    }
  }
  return result;
}

/** @internal */
export async function resolveSwagger20Operation(
  document: Swagger20Document,
  ref: string,
): Promise<Swagger20ResolvedOperation> {
  let parsed: { path: string; method: Swagger20Method };
  try {
    parsed = parseSwagger20Ref(ref);
  } catch (error: unknown) {
    throw new Swagger20ExecutionError("INVALID_OPERATION_REF", errorMessage(error), { cause: error });
  }
  const paths = objectMember(document.root, "paths");
  if (!paths.present || !paths.valid) {
    throw new Swagger20ExecutionError("ERR_REFUSED", "Swagger 2.0 document has no usable paths object");
  }
  const rawItem = paths.value![parsed.path];
  if (!isSwagger20Object(rawItem)) {
    throw new Swagger20ExecutionError("OPERATION_NOT_FOUND", `operation ${JSON.stringify(ref)} was not found`);
  }
  let item;
  try {
    item = await document.graph.resolvePathItem({ raw: rawItem, resource: document.entry }, parsed.method, newSwagger20ResolutionMemo());
  } catch (error: unknown) {
    throw new Swagger20ExecutionError("ERR_REFUSED", errorMessage(error), { cause: error });
  }
  const rawOperation = item.raw[parsed.method];
  if (!isSwagger20Object(rawOperation)) {
    throw new Swagger20ExecutionError("OPERATION_NOT_FOUND", `operation ${JSON.stringify(ref)} was not found`);
  }
  return {
    raw: rawOperation,
    resource: pathItemMemberResource(item, parsed.method),
    pathItem: item,
    path: parsed.path,
    method: parsed.method,
  };
}

function parseSwagger20Ref(ref: string): { path: string; method: Swagger20Method } {
  const prefix = "#/paths/";
  if (!ref.startsWith(prefix)) throw new Error(`selector ${JSON.stringify(ref)} must have exact form #/paths/<escaped-path>/<lowercase-method>`);
  const parts = ref.slice(prefix.length).split("/");
  if (parts.length !== 2 || parts[0] === "") throw new Error(`selector ${JSON.stringify(ref)} must have exact form #/paths/<escaped-path>/<lowercase-method>`);
  if (!wellFormedPointerToken(parts[0]!)) throw new Error(`selector ${JSON.stringify(ref)} contains a malformed RFC 6901 path token`);
  const method = parts[1] as Swagger20Method;
  if (!SWAGGER20_METHODS.includes(method)) throw new Error(`selector ${JSON.stringify(ref)} has inadmissible lowercase Swagger 2.0 method ${JSON.stringify(parts[1])}`);
  return { path: decodePointerToken(parts[0]!), method };
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
