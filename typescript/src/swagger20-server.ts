import { swagger20ConfigRequired } from "./swagger20-context.js";
import {
  arrayMember,
  stringMember,
  type Swagger20Document,
  type Swagger20ResolvedOperation,
} from "./swagger20-model.js";

/** Resolves the exact effective Swagger 2.0 target base without slash repair. */
export function resolveSwagger20Server(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  configured?: string,
  schemeIndex?: number,
): string {
  if (!operation.path.startsWith("/")) throw new Error(`Swagger 2.0 Paths key ${JSON.stringify(operation.path)} must begin with /`);
  if (configured !== undefined) {
    if (schemeIndex !== undefined) throw new Error("configuration.server cannot combine a complete URL with a scheme index");
    validateConfiguredServer(configured);
    return configured;
  }

  const host = stringMember(document.root, "host");
  if (host.present && (!host.valid || host.value === "" || !swagger20Host(host.value!))) {
    throw new Error("Swagger 2.0 host must contain only an authority host and optional port");
  }
  const basePath = stringMember(document.root, "basePath");
  if (basePath.present && (!basePath.valid || basePath.value === "" || !basePath.value!.startsWith("/") || /[?#]/u.test(basePath.value!))) {
    throw new Error("Swagger 2.0 basePath must be a nonempty absolute path without query or fragment");
  }
  const retrieval = document.entry.retrieval ?? document.entry.requested;
  const { usable, authored } = effectiveSchemes(document, operation, retrieval);
  let selected: string;
  if (schemeIndex !== undefined) {
    if (!Number.isSafeInteger(schemeIndex) || schemeIndex < 0 || schemeIndex >= authored.length) {
      throw new Error(`Swagger 2.0 server scheme index ${schemeIndex} is outside the effective scheme list`);
    }
    selected = authored[schemeIndex]!;
    if (selected !== "http" && selected !== "https") {
      throw new Error(`Swagger 2.0 effective scheme ${JSON.stringify(selected)} is unusable for HTTP execution`);
    }
  } else {
    if (usable.length === 0) throw new Error("Swagger 2.0 target has no usable http or https scheme");
    if (usable.length !== 1) throw swagger20ConfigRequired("server", "");
    selected = usable[0]!;
  }
  let effectiveHost = host.value;
  if (!host.present) {
    if (!retrieval) throw new Error("Swagger 2.0 target omits host without a document retrieval authority");
    let parsed: URL;
    try { parsed = new URL(retrieval); }
    catch (error: unknown) { throw new Error("Swagger 2.0 document retrieval URI is invalid", { cause: error }); }
    if (parsed.host === "") throw new Error("Swagger 2.0 target omits host without a document retrieval authority");
    effectiveHost = parsed.host;
  }
  return `${selected}://${effectiveHost}${basePath.present ? basePath.value : ""}`;
}

function effectiveSchemes(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  retrieval: string | undefined,
): { usable: string[]; authored: string[] } {
  let member = arrayMember(operation.raw, "schemes");
  if (!member.present) member = arrayMember(document.root, "schemes");
  let authored: string[];
  if (!member.present) {
    if (!retrieval) throw new Error("Swagger 2.0 target omits schemes without a document retrieval scheme");
    let parsed: URL;
    try { parsed = new URL(retrieval); }
    catch (error: unknown) { throw new Error("Swagger 2.0 document retrieval URI is invalid", { cause: error }); }
    const inherited = parsed.protocol.slice(0, -1).toLowerCase();
    if (!["http", "https", "ws", "wss"].includes(inherited)) {
      throw new Error(`Swagger 2.0 retrieval scheme ${JSON.stringify(inherited)} is not an effective HTTP or WebSocket scheme`);
    }
    authored = [inherited];
  } else {
    if (!member.valid || member.value!.length === 0) throw new Error("Swagger 2.0 effective schemes must be a nonempty array");
    authored = member.value!.map((value, index) => {
      if (typeof value !== "string") throw new Error(`Swagger 2.0 effective scheme ${index} is not a string`);
      if (!["http", "https", "ws", "wss"].includes(value)) {
        throw new Error(`Swagger 2.0 effective scheme ${JSON.stringify(value)} is outside the closed scheme set`);
      }
      return value;
    });
  }
  return { authored, usable: authored.filter((scheme) => scheme === "http" || scheme === "https") };
}

function swagger20Host(value: string): boolean {
  if (/[/?#@]/u.test(value) || value.includes("://")) return false;
  try {
    const parsed = new URL(`http://${value}`);
    return parsed.host === value && parsed.username === "" && parsed.password === "" && parsed.pathname === "/"
      && parsed.search === "" && parsed.hash === "";
  } catch { return false; }
}

function validateConfiguredServer(value: string): void {
  if (value === "" || /[\r\n]/u.test(value)) throw new Error("Swagger 2.0 consumer server override is not an absolute target URL");
  let parsed: URL;
  try { parsed = new URL(value); }
  catch (error: unknown) { throw new Error("Swagger 2.0 consumer server override is not an absolute target URL", { cause: error }); }
  if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || parsed.host === "" || parsed.search !== "" || parsed.hash !== "") {
    throw new Error("Swagger 2.0 consumer server override is not a complete HTTP target URL");
  }
}
