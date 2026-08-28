/** A JSON object retained exactly as authored by a Swagger 2.0 resource. */
export type Swagger20Object = Record<string, unknown>;

/** OpenAPI-native source carriage for an exact Swagger 2.0 artifact. */
export interface Swagger20Source {
  /** Canonical document URI and retrieval address when content is absent. */
  location?: string;
  /** Parsed JSON object, JSON/YAML text, or UTF-8 bytes. Content has primacy. */
  content?: unknown;
  /** A document previously returned by loadSwagger20. */
  document?: Swagger20Document;
}

export interface Swagger20LoadOptions {
  fetch?: typeof globalThis.fetch;
  signal?: AbortSignal;
  /** Defaults to true. False keeps inspection strictly side-effect-free. */
  allowExternalRefs?: boolean;
}

/** One native Swagger 2.0 operation identity. */
export interface Swagger20OperationInfo {
  ref: string;
  path: string;
  method: Swagger20Method;
  operationId?: string;
  summary?: string;
  tags: string[];
}

export type Swagger20Method = "get" | "put" | "post" | "delete" | "options" | "head" | "patch";

/**
 * Exact JSON-number carrier for hosts that preserve the source token. Plain
 * JavaScript numbers remain accepted, but this form keeps distinctions such
 * as `1` versus `1.0` available to draft-04's literal-integer rule.
 */
export class Swagger20Number {
  readonly lexeme: string;
  readonly value: number;

  constructor(lexeme: string) {
    if (!/^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$/u.test(lexeme)) {
      throw new Error(`invalid JSON number token ${JSON.stringify(lexeme)}`);
    }
    const value = Number(lexeme);
    if (!Number.isFinite(value)) throw new Error(`JSON number token ${JSON.stringify(lexeme)} is not finite`);
    this.lexeme = lexeme;
    this.value = value;
  }

  toJSON(): number {
    return this.value;
  }

  toString(): string {
    return this.lexeme;
  }
}

export const SWAGGER20_METHODS: readonly Swagger20Method[] =
  Object.freeze(["get", "put", "post", "delete", "options", "head", "patch"]);

/** @internal */
export interface Swagger20Resource {
  requested?: string;
  retrieval?: string;
  root: unknown;
}

/** @internal */
export interface Swagger20ResolvedOperation {
  raw: Swagger20Object;
  resource: Swagger20Resource;
  graph: Swagger20ReferenceGraphContract;
  pathItem: Swagger20PathItem;
  path: string;
  method: Swagger20Method;
}

/** @internal */
export interface Swagger20PathItem {
  raw: Swagger20Object;
  resource: Swagger20Resource;
  memberResources?: Map<string, Swagger20Resource>;
}

/**
 * Native, raw-preserving Swagger 2.0 document model. It is deliberately not
 * assignable to the package's OpenAPI 3.x structural model.
 */
export class Swagger20Document {
  /** @internal */ readonly root: Swagger20Object;
  /** @internal */ readonly entry: Swagger20Resource;
  /** @internal */ readonly graph: Swagger20ReferenceGraphContract;

  /** @internal */
  constructor(root: Swagger20Object, entry: Swagger20Resource, graph: Swagger20ReferenceGraphContract) {
    this.root = root;
    this.entry = entry;
    this.graph = graph;
  }

  /** Exact retained root discriminator. */
  get swagger(): string | undefined {
    return stringMember(this.root, "swagger").value;
  }

  /** Lossless JSON-domain inspection image. */
  toJSON(): Swagger20Object {
    return this.root;
  }
}

/** @internal - breaks the model/reference module cycle without widening the public surface. */
export interface Swagger20ReferenceGraphContract {
  readonly selfContained: boolean;
  readonly allowExternalRefs: boolean;
  resolveReference(raw: string, from: Swagger20Resource, memo?: Swagger20ResolutionMemo): Promise<Swagger20Resolution>;
  resolvePathItem(item: Swagger20PathItem, method: Swagger20Method, memo?: Swagger20ResolutionMemo): Promise<Swagger20PathItem>;
  rebind(options: Swagger20LoadOptions): Swagger20ReferenceGraphContract;
  rememberResolvedResource(requested: string | undefined, retrieval: string | undefined, root: unknown): Swagger20Resource;
}

/** @internal */
export interface Swagger20Resolution {
  node?: unknown;
  resource: Swagger20Resource;
  cycle?: boolean;
}

/** @internal */
export interface Swagger20ResolutionMemo {
  active: Set<string>;
  pathActive: Set<string>;
  done: Map<string, Swagger20Resolution>;
}

/** Presence-aware field result. */
export interface Swagger20Member<T> {
  value?: T;
  present: boolean;
  valid: boolean;
}

/** @internal */
export function member(object: Swagger20Object, name: string): Swagger20Member<unknown> {
  if (!Object.hasOwn(object, name)) return { present: false, valid: false };
  return { value: object[name], present: true, valid: true };
}

/** @internal */
export function stringMember(object: Swagger20Object, name: string): Swagger20Member<string> {
  const found = member(object, name);
  return found.present && typeof found.value === "string"
    ? { value: found.value, present: true, valid: true }
    : { present: found.present, valid: false };
}

/** @internal */
export function booleanMember(object: Swagger20Object, name: string): Swagger20Member<boolean> {
  const found = member(object, name);
  return found.present && typeof found.value === "boolean"
    ? { value: found.value, present: true, valid: true }
    : { present: found.present, valid: false };
}

/** @internal */
export function objectMember(object: Swagger20Object, name: string): Swagger20Member<Swagger20Object> {
  const found = member(object, name);
  return found.present && isSwagger20Object(found.value)
    ? { value: found.value, present: true, valid: true }
    : { present: found.present, valid: false };
}

/** @internal */
export function arrayMember(object: Swagger20Object, name: string): Swagger20Member<unknown[]> {
  const found = member(object, name);
  return found.present && Array.isArray(found.value)
    ? { value: found.value, present: true, valid: true }
    : { present: found.present, valid: false };
}

/** @internal */
export function isSwagger20Object(value: unknown): value is Swagger20Object {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/** @internal */
export function resourceBase(resource: Swagger20Resource | undefined): string | undefined {
  return resource?.retrieval ?? resource?.requested;
}

/** @internal */
export function operationInfo(operation: Swagger20ResolvedOperation, ref: string): Swagger20OperationInfo {
  const operationId = stringMember(operation.raw, "operationId");
  const summary = stringMember(operation.raw, "summary");
  const rawTags = arrayMember(operation.raw, "tags");
  const tags = rawTags.valid ? rawTags.value!.filter((tag): tag is string => typeof tag === "string") : [];
  return {
    ref,
    path: operation.path,
    method: operation.method,
    ...(operationId.valid ? { operationId: operationId.value } : {}),
    ...(summary.valid ? { summary: summary.value } : {}),
    tags,
  };
}

/** @internal */
export function pathItemMemberResource(item: Swagger20PathItem, name: string): Swagger20Resource {
  return item.memberResources?.get(name) ?? item.resource;
}
