import { OPENAPI32_FIXED_METHODS } from "./openapi32-operations.js";
import type { OpenAPIDocument, OpenAPIOperation, OpenAPIPathItem } from "./types.js";

/** The OpenAPI declaration family owning an operation initiated toward the consumer. */
export type OpenAPIInboundOperationKind = "callback" | "webhook";

/** One stable source-local callback or webhook operation slot. */
export interface OpenAPIInboundOperationReference {
  ref: string;
  kind: OpenAPIInboundOperationKind;
  parentRef: string;
  name: string;
  expression: string;
  method: string;
  additional: boolean;
  wireMethod: string;
}

/** A resolved inbound operation model, with no binding-model interpretation. */
export interface OpenAPIInboundOperationTarget extends OpenAPIInboundOperationReference {
  document: OpenAPIDocument;
  pathItem: OpenAPIPathItem;
  operation: OpenAPIOperation;
}

/** Retains either one resolved inbound target or its declaration-local defect. */
export interface OpenAPIInboundOperationDisposition {
  reference: OpenAPIInboundOperationReference;
  target?: OpenAPIInboundOperationTarget;
  error?: Error;
}

/** Inventories callbacks for 3.0+ and root webhooks for 3.1+. */
export function documentInboundOperationInventory(
  document: OpenAPIDocument,
): OpenAPIInboundOperationDisposition[] {
  const result: OpenAPIInboundOperationDisposition[] = [];
  const edition32 = document.openapi === "3.2.0";
  const callbackStack = new WeakSet<object>();

  const walkOperation = (operation: OpenAPIOperation, parentRef: string): void => {
    const callbacks = asRecord(operation.callbacks);
    if (!callbacks) return;
    for (const name of Object.keys(callbacks).sort(codePointCompare)) {
      const callbackBase = `${parentRef}/callbacks/${escapePointer(name)}`;
      let callback: Record<string, unknown>;
      try {
        callback = resolveLocalObject(callbacks[name], document);
      } catch (error: unknown) {
        result.push({
          reference: inboundReference(callbackBase, "callback", parentRef, name, "", "", false),
          error: error instanceof Error ? error : new Error(String(error)),
        });
        continue;
      }
      if (callbackStack.has(callback)) continue;
      callbackStack.add(callback);
      for (const expression of Object.keys(callback).sort(codePointCompare)) {
        if (expression.startsWith("x-")) continue;
        const expressionRef = `${callbackBase}/${escapePointer(expression)}`;
        let pathItem: Record<string, unknown>;
        try {
          pathItem = resolveLocalObject(callback[expression], document);
        } catch (error: unknown) {
          result.push({
            reference: inboundReference(expressionRef, "callback", parentRef, name, expression, "", false),
            error: error instanceof Error ? error : new Error(String(error)),
          });
          continue;
        }
        appendInboundPathItem(
          result, document, pathItem as OpenAPIPathItem, expressionRef,
          "callback", parentRef, name, expression, edition32, walkOperation,
        );
      }
      callbackStack.delete(callback);
    }
  };

  for (const path of Object.keys(document.paths ?? {}).sort(codePointCompare)) {
    const pathItem = asRecord(document.paths?.[path]);
    if (pathItem) walkPathOperations(
      pathItem as OpenAPIPathItem,
      `#/paths/${escapePointer(path)}`,
      edition32,
      walkOperation,
    );
  }

  if (edition32 || document.openapi?.startsWith("3.1.")) {
    const webhooks = asRecord(document.webhooks);
    for (const name of Object.keys(webhooks ?? {}).sort(codePointCompare)) {
      const base = `#/webhooks/${escapePointer(name)}`;
      let pathItem: Record<string, unknown>;
      try {
        pathItem = resolveLocalObject(webhooks![name], document);
      } catch (error: unknown) {
        result.push({
          reference: inboundReference(base, "webhook", "", name, "", "", false),
          error: error instanceof Error ? error : new Error(String(error)),
        });
        continue;
      }
      appendInboundPathItem(
        result, document, pathItem as OpenAPIPathItem, base,
        "webhook", "", name, "", edition32, walkOperation,
      );
    }
  }
  return result;
}

function walkPathOperations(
  pathItem: OpenAPIPathItem,
  base: string,
  edition32: boolean,
  walkOperation: (operation: OpenAPIOperation, parentRef: string) => void,
): void {
  const methods = edition32 ? OPENAPI32_FIXED_METHODS : OPENAPI32_FIXED_METHODS.slice(0, -1);
  for (const method of methods) {
    const operation = asRecord(pathItem[method]);
    if (operation) walkOperation(operation as OpenAPIOperation, `${base}/${method}`);
  }
  if (!edition32) return;
  const additional = asRecord(pathItem.additionalOperations);
  for (const method of Object.keys(additional ?? {}).sort(codePointCompare)) {
    const operation = asRecord(additional![method]);
    if (operation) walkOperation(
      operation as OpenAPIOperation,
      `${base}/additionalOperations/${escapePointer(method)}`,
    );
  }
}

function appendInboundPathItem(
  result: OpenAPIInboundOperationDisposition[],
  document: OpenAPIDocument,
  pathItem: OpenAPIPathItem,
  base: string,
  kind: OpenAPIInboundOperationKind,
  parentRef: string,
  name: string,
  expression: string,
  edition32: boolean,
  walkOperation: (operation: OpenAPIOperation, parentRef: string) => void,
): void {
  const methods = edition32 ? OPENAPI32_FIXED_METHODS : OPENAPI32_FIXED_METHODS.slice(0, -1);
  for (const method of methods) {
    const operation = asRecord(pathItem[method]);
    if (!operation) continue;
    const reference = inboundReference(`${base}/${method}`, kind, parentRef, name, expression, method, false);
    result.push({ reference, target: { ...reference, document, pathItem, operation: operation as OpenAPIOperation } });
    walkOperation(operation as OpenAPIOperation, reference.ref);
  }
  if (!edition32) return;
  const additional = asRecord(pathItem.additionalOperations);
  for (const method of Object.keys(additional ?? {}).sort(codePointCompare)) {
    const operation = asRecord(additional![method]);
    if (!operation) continue;
    const reference = inboundReference(
      `${base}/additionalOperations/${escapePointer(method)}`,
      kind, parentRef, name, expression, method, true,
    );
    result.push({ reference, target: { ...reference, document, pathItem, operation: operation as OpenAPIOperation } });
    walkOperation(operation as OpenAPIOperation, reference.ref);
  }
}

function inboundReference(
  ref: string,
  kind: OpenAPIInboundOperationKind,
  parentRef: string,
  name: string,
  expression: string,
  method: string,
  additional: boolean,
): OpenAPIInboundOperationReference {
  return {
    ref, kind, parentRef, name, expression, method, additional,
    wireMethod: additional ? method : method.toUpperCase(),
  };
}

function resolveLocalObject(
  raw: unknown,
  document: OpenAPIDocument,
  seen = new Set<string>(),
): Record<string, unknown> {
  const object = asRecord(raw);
  if (!object) throw new Error("inbound declaration is not an object");
  if (typeof object.$ref !== "string") return object;
  if (!object.$ref.startsWith("#/")) {
    throw new Error(`inbound reference ${JSON.stringify(object.$ref)} is not materialized`);
  }
  if (seen.has(object.$ref)) return {};
  seen.add(object.$ref);
  let target: unknown = document;
  for (const token of object.$ref.slice(2).split("/")) {
    const member = token.replaceAll("~1", "/").replaceAll("~0", "~");
    const record = asRecord(target);
    if (!record || !Object.hasOwn(record, member)) {
      throw new Error(`inbound reference ${JSON.stringify(object.$ref)} names no target`);
    }
    target = record[member];
  }
  return resolveLocalObject(target, document, seen);
}

function escapePointer(value: string): string {
  return value.replaceAll("~", "~0").replaceAll("/", "~1");
}

function codePointCompare(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}
