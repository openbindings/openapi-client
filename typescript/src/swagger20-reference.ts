import {
  isSwagger20Object,
  member,
  pathItemMemberResource,
  resourceBase,
  stringMember,
  type Swagger20LoadOptions,
  type Swagger20Method,
  type Swagger20PathItem,
  type Swagger20ReferenceGraphContract,
  type Swagger20Resolution,
  type Swagger20ResolutionMemo,
  type Swagger20Resource,
} from "./swagger20-model.js";
import { parseSwagger20Resource } from "./swagger20-loader.js";

/** @internal */
export function newSwagger20ResolutionMemo(): Swagger20ResolutionMemo {
  return { active: new Set(), pathActive: new Set(), done: new Map() };
}

/** Lazy JSON Reference draft-03 resource graph for one Swagger 2.0 source. */
export class Swagger20ReferenceGraph implements Swagger20ReferenceGraphContract {
  readonly selfContained: boolean;
  readonly allowExternalRefs: boolean;
  private readonly fetchFn: typeof globalThis.fetch | undefined;
  private readonly signal: AbortSignal | undefined;
  private readonly resources = new Map<string, Swagger20Resource>();
  private readonly pending = new Map<string, Promise<Swagger20Resource>>();

  constructor(options: Swagger20LoadOptions, selfContained: boolean) {
    this.fetchFn = options.fetch;
    this.signal = options.signal;
    this.selfContained = selfContained;
    this.allowExternalRefs = options.allowExternalRefs !== false;
  }

  rebind(options: Swagger20LoadOptions): Swagger20ReferenceGraph {
    return new Swagger20ReferenceGraph(options, this.selfContained);
  }

  rememberResolvedResource(requested: string | undefined, retrieval: string | undefined, root: unknown): Swagger20Resource {
    const resource = { requested, retrieval, root };
    if (requested) this.resources.set(resourceKey(requested), resource);
    if (retrieval) this.resources.set(resourceKey(retrieval), resource);
    if (!requested && !retrieval) this.resources.set("", resource);
    return resource;
  }

  async resolveReference(
    raw: string,
    from: Swagger20Resource,
    memo: Swagger20ResolutionMemo = newSwagger20ResolutionMemo(),
  ): Promise<Swagger20Resolution> {
    let target: URL;
    try {
      const base = resourceBase(from);
      const fragmentOnly = raw.startsWith("#");
      if (base) target = new URL(raw, base);
      else if (fragmentOnly) target = new URL(raw, "swagger20:self");
      else {
        target = new URL(raw);
        if (this.selfContained) {
          throw new Error(`reference ${JSON.stringify(raw)} cannot resolve: embedded Swagger 2.0 content with no co-present location must be self-contained`);
        }
      }
    } catch (error: unknown) {
      if (error instanceof Error && error.message.includes("self-contained")) throw error;
      throw new Error(`Swagger 2.0 reference ${JSON.stringify(raw)} is not a URI`, { cause: error });
    }

    const fragment = target.hash.slice(1);
    target.hash = "";
    const resourceURI = target.protocol === "swagger20:" ? "" : target.href;
    const canonical = `${resourceKey(resourceURI)}#${fragment}`;
    const completed = memo.done.get(canonical);
    if (completed) return completed;
    if (memo.active.has(canonical)) return { resource: from, cycle: true };
    memo.active.add(canonical);
    try {
      const resource = await this.resourceForReference(resourceURI, from);
      let result: Swagger20Resolution = { node: resource.root, resource };
      if (fragment !== "") {
        const decoded = decodeURIComponent(fragment);
        if (!decoded.startsWith("/")) {
          throw new Error(`Swagger 2.0 reference ${JSON.stringify(raw)} has unsupported non-JSON-Pointer fragment`);
        }
        result = await this.resolvePointer(resource, decoded, memo);
      }
      memo.done.set(canonical, result);
      return result;
    } finally {
      memo.active.delete(canonical);
    }
  }

  async resolvePathItem(
    item: Swagger20PathItem,
    method: Swagger20Method,
    memo: Swagger20ResolutionMemo = newSwagger20ResolutionMemo(),
  ): Promise<Swagger20PathItem> {
    const reference = stringMember(item.raw, "$ref");
    if (!reference.present) return item;
    if (!reference.valid || reference.value === "") {
      throw new Error("selected Swagger 2.0 Path Item has an invalid $ref");
    }
    const key = `${resourceKey(resourceBase(item.resource) ?? "")}|${reference.value}|${method}`;
    if (memo.pathActive.has(key)) {
      throw new Error(`selected Swagger 2.0 Path Item reference cycle leaves ${method} unresolved`);
    }
    memo.pathActive.add(key);
    try {
      const resolved = await this.resolveReference(reference.value!, item.resource, memo);
      if (resolved.cycle) {
        throw new Error(`selected Swagger 2.0 Path Item reference cycle leaves ${method} unresolved`);
      }
      if (!isSwagger20Object(resolved.node)) {
        throw new Error("selected Swagger 2.0 Path Item $ref does not resolve to an object");
      }
      const target = await this.resolvePathItem({ raw: resolved.node, resource: resolved.resource }, method, memo);
      if (member(item.raw, method).present && member(target.raw, method).present) {
        throw new Error(`selected Swagger 2.0 Path Item has undefined adjacent/ref collision at ${method}`);
      }
      if (member(item.raw, "parameters").present && member(target.raw, "parameters").present) {
        throw new Error("selected Swagger 2.0 Path Item has undefined adjacent/ref collision at parameters");
      }
      const merged = { ...target.raw };
      const resources = new Map<string, Swagger20Resource>();
      for (const name of Object.keys(target.raw)) resources.set(name, pathItemMemberResource(target, name));
      for (const [name, value] of Object.entries(item.raw)) {
        if (name === "$ref" || Object.hasOwn(merged, name)) continue;
        merged[name] = value;
        resources.set(name, pathItemMemberResource(item, name));
      }
      return { raw: merged, resource: target.resource, memberResources: resources };
    } finally {
      memo.pathActive.delete(key);
    }
  }

  private async resourceForReference(uri: string, from: Swagger20Resource): Promise<Swagger20Resource> {
    const key = resourceKey(uri);
    if (key === "") return from;
    const fromKeys = [from.requested, from.retrieval].filter(Boolean).map((value) => resourceKey(value!));
    if (fromKeys.includes(key)) return from;
    const cached = this.resources.get(key);
    if (cached) return cached;
    if (this.selfContained) {
      throw new Error(`reference ${JSON.stringify(uri)} cannot resolve: embedded Swagger 2.0 content with no co-present location must be self-contained`);
    }
    if (!this.allowExternalRefs) throw new Error(`external Swagger 2.0 reference ${JSON.stringify(uri)} is disabled`);
    const existing = this.pending.get(key);
    if (existing) return existing;
    const load = this.readResource(uri).finally(() => this.pending.delete(key));
    this.pending.set(key, load);
    return load;
  }

  private async readResource(uri: string): Promise<Swagger20Resource> {
    const fetchFn = this.fetchFn ?? globalThis.fetch;
    if (!fetchFn) throw new Error(`no fetch implementation is available for Swagger 2.0 resource ${JSON.stringify(uri)}`);
    const response = await fetchFn(uri, { signal: this.signal });
    if (!response.ok) throw new Error(`load Swagger 2.0 resource ${JSON.stringify(uri)}: HTTP ${response.status}`);
    const root = parseSwagger20Resource(await response.text());
    const retrieval = response.url || uri;
    return this.rememberResolvedResource(uri, retrieval, root);
  }

  private async resolvePointer(
    resource: Swagger20Resource,
    pointer: string,
    memo: Swagger20ResolutionMemo,
  ): Promise<Swagger20Resolution> {
    if (pointer === "") return { node: resource.root, resource };
    if (!pointer.startsWith("/")) throw new Error(`fragment ${JSON.stringify(pointer)} is not an RFC 6901 JSON Pointer`);
    let current: Swagger20Resolution = { node: resource.root, resource };
    for (const encoded of pointer.slice(1).split("/")) {
      if (!wellFormedPointerToken(encoded)) throw new Error(`fragment ${JSON.stringify(pointer)} contains malformed RFC 6901 escape`);
      const token = decodePointerToken(encoded);
      while (true) {
        if (isSwagger20Object(current.node)) {
          if (Object.hasOwn(current.node, token)) {
            current.node = current.node[token];
            break;
          }
          const reference = stringMember(current.node, "$ref");
          if (!reference.valid) throw new Error(`JSON Pointer token ${JSON.stringify(token)} identifies no object member`);
          const resolved = await this.resolveReference(reference.value!, current.resource, memo);
          if (resolved.cycle) return resolved;
          current = resolved;
          continue;
        }
        if (Array.isArray(current.node)) {
          if (!/^(0|[1-9][0-9]*)$/u.test(token)) throw new Error(`JSON Pointer token ${JSON.stringify(token)} identifies no array member`);
          const index = Number(token);
          if (index >= current.node.length) throw new Error(`JSON Pointer token ${JSON.stringify(token)} identifies no array member`);
          current.node = current.node[index];
          break;
        }
        throw new Error(`JSON Pointer token ${JSON.stringify(token)} cannot be applied to a scalar`);
      }
    }
    return current;
  }
}

/** @internal */
export function wellFormedPointerToken(value: string): boolean {
  for (let index = 0; index < value.length; index++) {
    if (value[index] === "~" && value[index + 1] !== "0" && value[index + 1] !== "1") return false;
    if (value[index] === "~") index++;
  }
  return true;
}

/** @internal */
export function decodePointerToken(value: string): string {
  return value.replaceAll("~1", "/").replaceAll("~0", "~");
}

/** @internal */
export function escapePointerToken(value: string): string {
  return value.replaceAll("~", "~0").replaceAll("/", "~1");
}

function resourceKey(value: string): string {
  if (value === "") return "";
  const url = new URL(value);
  url.hash = "";
  return url.href;
}
