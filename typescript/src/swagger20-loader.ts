import { CORE_SCHEMA, loadAll } from "js-yaml";
import {
  Swagger20Document,
  isSwagger20Object,
  stringMember,
  type Swagger20LoadOptions,
  type Swagger20Source,
} from "./swagger20-model.js";
import { Swagger20ReferenceGraph } from "./swagger20-reference.js";

/** Loaded native view of one exact Swagger 2.0 artifact. */
export class Swagger20Client {
  readonly document: Swagger20Document;
  readonly source: Swagger20Source;

  /** @internal */
  constructor(document: Swagger20Document, source: Swagger20Source) {
    this.document = document;
    this.source = source;
  }

  async operations(): Promise<import("./swagger20-model.js").Swagger20OperationInfo[]> {
    const { listSwagger20Operations } = await import("./swagger20-engine.js");
    return listSwagger20Operations(this.document);
  }

  /** Detached native declaration analysis for thin binding adapters. */
  async synthesisModel(): Promise<import("./swagger20-synthesis.js").Swagger20SynthesisDocument> {
    const { swagger20SynthesisModel } = await import("./swagger20-synthesis.js");
    return swagger20SynthesisModel(this.document);
  }
}

/** Loads only the exact Swagger 2.0 family, with no cross-edition fallback. */
export async function loadSwagger20(
  source: Swagger20Source,
  options: Swagger20LoadOptions = {},
): Promise<Swagger20Client> {
  const document = await loadSwagger20Document(source, options);
  return new Swagger20Client(document, { location: source.location, document });
}

/** @internal */
export async function loadSwagger20Document(
  source: Swagger20Source,
  options: Swagger20LoadOptions = {},
): Promise<Swagger20Document> {
  if (source.document) {
    if (source.document.swagger !== "2.0") throw new Error("unsupported Swagger version: expected exact string \"2.0\"");
    const graph = source.document.graph.rebind(options);
    const entry = graph.rememberResolvedResource(
      source.document.entry.requested,
      source.document.entry.retrieval,
      source.document.root,
    );
    return new Swagger20Document(source.document.root, entry, graph);
  }

  let location: string | undefined;
  if (source.location !== undefined) {
    try {
      location = new URL(source.location).href;
    } catch (error: unknown) {
      throw new Error(`Swagger 2.0 location ${JSON.stringify(source.location)} is not an absolute URI`, { cause: error });
    }
  }
  if (source.content === undefined && !location) throw new Error("Swagger 2.0 source requires location or content");

  const selfContained = source.content !== undefined && location === undefined;
  const graph = new Swagger20ReferenceGraph(options, selfContained);
  let representation = source.content;
  let retrieval = location;
  if (representation === undefined) {
    const fetchFn = options.fetch ?? globalThis.fetch;
    if (!fetchFn) throw new Error("no fetch implementation is available for Swagger 2.0 source retrieval");
    const response = await fetchFn(location!, { signal: options.signal });
    if (!response.ok) throw new Error(`load Swagger 2.0 source ${JSON.stringify(location)}: HTTP ${response.status}`);
    representation = await response.text();
    retrieval = response.url || location;
  }
  const root = parseSwagger20Resource(representation);
  if (!isSwagger20Object(root)) throw new Error("Swagger 2.0 document root must be a JSON object");
  const swagger = stringMember(root, "swagger");
  if (!swagger.present) throw new Error("Swagger 2.0 document requires root swagger field with exact string value \"2.0\"");
  if (!swagger.valid || swagger.value !== "2.0") {
    throw new Error("unsupported Swagger version: root swagger must be exact string \"2.0\"");
  }
  const entry = graph.rememberResolvedResource(location, retrieval, root);
  return new Swagger20Document(root, entry, graph);
}

/** Parses one JSON/YAML 1.2 Core resource and enforces its RFC 7159 image. */
export function parseSwagger20Resource(content: unknown): unknown {
  if (isSwagger20Object(content)) return cloneAndCheckJSONImage(content);
  if (content instanceof Uint8Array) content = new TextDecoder("utf-8", { fatal: true }).decode(content);
  if (typeof content !== "string") throw new Error("Swagger 2.0 content must be an object, string, or UTF-8 bytes");
  const documents: unknown[] = [];
  try {
    loadAll(content, (document) => documents.push(document), {
      schema: CORE_SCHEMA,
      json: false,
    });
  } catch (error: unknown) {
    throw new Error("parse Swagger 2.0 representation", { cause: error });
  }
  if (documents.length !== 1) throw new Error("Swagger 2.0 representation must contain exactly one YAML document");
  return cloneAndCheckJSONImage(documents[0]);
}

function cloneAndCheckJSONImage(value: unknown): unknown {
  try {
    const image = JSON.stringify(value);
    if (image === undefined) throw new Error("value has no JSON image");
    return JSON.parse(image) as unknown;
  } catch (error: unknown) {
    throw new Error("Swagger 2.0 representation has no RFC 7159 JSON image", { cause: error });
  }
}
