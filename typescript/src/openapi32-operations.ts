import type { OpenAPIDocument, OpenAPIOperation, OpenAPIPathItem } from "./types.js";

/** Fixed Operation Object fields admitted by OpenAPI 3.2. */
export const OPENAPI32_FIXED_METHODS = [
  "get", "put", "post", "delete", "options", "head", "patch", "trace", "query",
] as const;

const FIXED_METHODS = new Set<string>(OPENAPI32_FIXED_METHODS);
const HTTP_TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/u;

export interface OpenAPIOperationReference {
  ref: string;
  path: string;
  method: string;
  additional: boolean;
  wireMethod: string;
}

export interface OpenAPIResolvedOperation {
  reference: OpenAPIOperationReference;
  document: OpenAPIDocument;
  pathItem: OpenAPIPathItem;
  operation: OpenAPIOperation;
  /** Referring-document component names retained for explicit scope election. */
  referringSecuritySchemes?: Record<string, Record<string, unknown>>;
  /** Response media alternatives excluded by confined closure defects. */
  responseMediaExclusions?: OpenAPI32ResponseMediaExclusion[];
}

export interface OpenAPI32ResponseMediaExclusion {
  responseKey: string;
  mediaType: string;
  reason: string;
}

export type OpenAPIOperationResolutionKind = "invalid-reference" | "not-found" | "excluded";

export class OpenAPIOperationResolutionError extends Error {
  readonly kind: OpenAPIOperationResolutionKind;

  constructor(kind: OpenAPIOperationResolutionKind, message: string, options?: { cause?: unknown }) {
    super(message, options?.cause !== undefined ? { cause: options.cause } : undefined);
    this.name = "OpenAPIOperationResolutionError";
    this.kind = kind;
  }
}

/** Parses one of OpenAPI 3.2's two operation-address forms. */
export function parseOpenAPI32OperationReference(ref: string): OpenAPIOperationReference {
  const prefix = "#/paths/";
  if (!ref.startsWith(prefix)) {
    throw new OpenAPIOperationResolutionError(
      "invalid-reference",
      `operation ref ${JSON.stringify(ref)} must begin #/paths/`,
    );
  }
  const parts = ref.slice(prefix.length).split("/");
  if (parts.length === 3 && parts[1] === "additionalOperations") {
    if (!wellFormedPointerToken(parts[0]!) || !wellFormedPointerToken(parts[2]!)) {
      throw new OpenAPIOperationResolutionError(
        "invalid-reference",
        `operation ref ${JSON.stringify(ref)} is not a well-formed RFC 6901 pointer`,
      );
    }
    const method = unescapePointerToken(parts[2]!);
    if (!HTTP_TOKEN.test(method)) {
      throw new OpenAPIOperationResolutionError(
        "invalid-reference",
        `additional operation method ${JSON.stringify(method)} is not an HTTP token`,
      );
    }
    return {
      ref,
      path: unescapePointerToken(parts[0]!),
      method,
      additional: true,
      wireMethod: method,
    };
  }
  if (parts.length !== 2 || !wellFormedPointerToken(parts[0]!) || !wellFormedPointerToken(parts[1]!)) {
    throw new OpenAPIOperationResolutionError(
      "invalid-reference",
      `operation ref ${JSON.stringify(ref)} must address one escaped path and operation field`,
    );
  }
  const method = parts[1]!;
  if (!FIXED_METHODS.has(method)) {
    throw new OpenAPIOperationResolutionError(
      "invalid-reference",
      `invalid fixed operation field ${JSON.stringify(method)} in ref`,
    );
  }
  return {
    ref,
    path: unescapePointerToken(parts[0]!),
    method,
    additional: false,
    wireMethod: method.toUpperCase(),
  };
}

export function openAPI32OperationValue(
  pathItem: Record<string, unknown>,
  reference: OpenAPIOperationReference,
): unknown {
  if (!reference.additional) return pathItem[reference.method];
  return asRecord(pathItem.additionalOperations)?.[reference.method];
}

export function openAPI32OperationReferences(root: unknown): OpenAPIOperationReference[] {
  const paths = asRecord(asRecord(root)?.paths);
  if (!paths) return [];
  const result: OpenAPIOperationReference[] = [];
  for (const path of Object.keys(paths).sort(codePointCompare)) {
    const pathItem = asRecord(paths[path]);
    if (!pathItem) continue;
    for (const method of OPENAPI32_FIXED_METHODS) {
      if (!Object.hasOwn(pathItem, method)) continue;
      result.push(parseOpenAPI32OperationReference(
        `#/paths/${escapePointerToken(path)}/${method}`,
      ));
    }
    const additional = asRecord(pathItem.additionalOperations);
    for (const method of Object.keys(additional ?? {}).sort(codePointCompare)) {
      const ref = `#/paths/${escapePointerToken(path)}/additionalOperations/${escapePointerToken(method)}`;
      try {
        result.push(parseOpenAPI32OperationReference(ref));
      } catch (error: unknown) {
        if (error instanceof OpenAPIOperationResolutionError) {
          // Inventory retains excluded positions. Construct the reference
          // without weakening the parser used for actual selection. Invalid
          // reference spellings remain absent so an explicit selector fails
          // at selector resolution rather than making artifact loading fail.
          if (error.kind === "excluded") {
            result.push({ ref, path, method, additional: true, wireMethod: method });
          }
          continue;
        }
        throw error;
      }
    }
  }
  return result;
}

export function escapePointerToken(value: string): string {
  return value.replaceAll("~", "~0").replaceAll("/", "~1");
}

export function unescapePointerToken(value: string): string {
  return value.replaceAll("~1", "/").replaceAll("~0", "~");
}

function wellFormedPointerToken(value: string): boolean {
  return !/~(?:[^01]|$)/u.test(value);
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function codePointCompare(a: string, b: string): number {
  const left = [...a];
  const right = [...b];
  const length = Math.min(left.length, right.length);
  for (let index = 0; index < length; index++) {
    const l = left[index]!.codePointAt(0)!;
    const r = right[index]!.codePointAt(0)!;
    if (l !== r) return l < r ? -1 : 1;
  }
  return left.length - right.length;
}
