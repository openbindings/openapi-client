import {
  isSwagger20Object,
  stringMember,
  type Swagger20Object,
  type Swagger20ReferenceGraphContract,
  type Swagger20Resource,
} from "./swagger20-model.js";
import { newSwagger20ResolutionMemo } from "./swagger20-reference.js";

/** Binding-relevant resolved declaration facts; no 3.x Schema type is reused. */
export interface Swagger20SchemaDeclaration {
  typed: boolean;
  types: Set<string>;
  formats: Set<string>;
}

export async function resolveSwagger20SchemaDeclaration(
  graph: Swagger20ReferenceGraphContract,
  value: unknown,
  resource: Swagger20Resource,
  allowFile = false,
  active = new Set<string>(),
): Promise<Swagger20SchemaDeclaration> {
  if (!isSwagger20Object(value)) throw new Error("Schema Object is not an object");
  const reference = stringMember(value, "$ref");
  if (reference.present) {
    if (!reference.valid || reference.value === "") throw new Error("Schema Object has an invalid $ref");
    const key = `${resource.retrieval ?? resource.requested ?? ""}|schema|${reference.value}`;
    if (active.has(key)) throw new Error("selected Schema reference cycle does not resolve a carriage declaration");
    active.add(key);
    try {
      const resolved = await graph.resolveReference(reference.value!, resource, newSwagger20ResolutionMemo());
      if (resolved.cycle) throw new Error("selected Schema reference cycle does not resolve a carriage declaration");
      return resolveSwagger20SchemaDeclaration(graph, resolved.node, resolved.resource, allowFile, active);
    } finally {
      active.delete(key);
    }
  }
  let declaration: Swagger20SchemaDeclaration = { typed: false, types: new Set(), formats: new Set() };
  if (Object.hasOwn(value, "type")) {
    declaration.typed = true;
    declaration.types = schemaTypes(value.type, allowFile);
  }
  const format = stringMember(value, "format");
  if (format.present) {
    if (!format.valid) throw new Error("Schema Object format is not a string");
    declaration.formats.add(format.value!);
  }
  if (Object.hasOwn(value, "allOf")) {
    if (!Array.isArray(value.allOf) || value.allOf.length === 0) throw new Error("Schema Object allOf is not a nonempty array");
    for (let index = 0; index < value.allOf.length; index++) {
      try {
        declaration = conjoin(declaration, await resolveSwagger20SchemaDeclaration(
          graph, value.allOf[index], resource, allowFile, active,
        ));
      } catch (error: unknown) {
        throw new Error(`Schema Object allOf member ${index}: ${errorMessage(error)}`, { cause: error });
      }
    }
  }
  return declaration;
}

function schemaTypes(value: unknown, allowFile: boolean): Set<string> {
  const values = Array.isArray(value) ? value : [value];
  if (values.length === 0) throw new Error("Schema Object type array is empty");
  const result = new Set<string>();
  for (const member of values) {
    if (typeof member !== "string" || member === "") throw new Error("Schema Object type contains a non-string or empty member");
    if (!["null", "boolean", "object", "array", "number", "string", "integer"].includes(member)
      && !(allowFile && member === "file")) throw new Error(`Schema Object type ${JSON.stringify(member)} is not admitted`);
    if (result.has(member)) throw new Error(`Schema Object type repeats ${JSON.stringify(member)}`);
    result.add(member);
  }
  return result;
}

function conjoin(left: Swagger20SchemaDeclaration, right: Swagger20SchemaDeclaration): Swagger20SchemaDeclaration {
  const typed = left.typed || right.typed;
  let types = new Set<string>();
  if (left.typed && right.typed) types = new Set([...left.types].filter((type) => right.types.has(type)));
  else if (left.typed) types = new Set(left.types);
  else if (right.typed) types = new Set(right.types);
  return { typed, types, formats: new Set([...left.formats, ...right.formats]) };
}

export function swagger20SoleString(declaration: Swagger20SchemaDeclaration): boolean {
  return declaration.typed && declaration.types.has("string")
    && [...declaration.types].every((type) => type === "string" || type === "null");
}

export function swagger20RawOctets(declaration: Swagger20SchemaDeclaration): boolean {
  if (declaration.typed && declaration.types.size === 1 && declaration.types.has("file")) return true;
  return swagger20SoleString(declaration) && declaration.formats.size === 1 && declaration.formats.has("binary");
}

export function swagger20ByteString(declaration: Swagger20SchemaDeclaration): boolean {
  return swagger20SoleString(declaration) && declaration.formats.size === 1 && declaration.formats.has("byte");
}

export function swagger20SchemaObject(value: unknown): Swagger20Object {
  if (!isSwagger20Object(value)) throw new Error("Schema Object is not an object");
  return value;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
