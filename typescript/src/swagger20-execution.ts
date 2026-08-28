import { Swagger20ExecutionError, type PreparedSwagger20Operation } from "./swagger20-engine.js";
import {
  routeSwagger20Input,
  swagger20RawQuery,
  type Swagger20Input,
  type Swagger20ParameterSet,
} from "./swagger20-parameters.js";

export interface Swagger20ExecutionResult {
  outputPresent: boolean;
  output?: unknown;
  status: number;
  headers: Headers;
}

/** @internal - pass-two non-payload execution, extended by the media pass. */
export async function executeSwagger20(
  prepared: PreparedSwagger20Operation,
  parameters: Swagger20ParameterSet,
  input: Swagger20Input = {},
): Promise<Swagger20ExecutionResult> {
  let routed;
  try {
    routed = routeSwagger20Input(
      parameters,
      prepared.operation.path,
      input,
      prepared.options.parameterConverter,
      prepared.options.emptyValueForm,
    );
  } catch (error: unknown) {
    throw refused(error);
  }
  if (routed.bodyPresent || routed.formPresent) {
    throw new Swagger20ExecutionError("ERR_REFUSED", "Swagger 2.0 payload execution requires a selected media lane");
  }
  const server = configuredServer(prepared.options.server);
  const query = swagger20RawQuery(routed.query);
  const url = `${server}${routed.resolvedPath}${query === "" ? "" : `?${query}`}`;
  try {
    const parsed = new URL(url);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") throw new Error("target scheme is not HTTP");
    decodeURIComponent(parsed.pathname + parsed.search);
  } catch (error: unknown) {
    throw refused(error);
  }
  const headers = new Headers();
  for (const header of routed.headers) headers.append(header.name, header.value);
  let response: Response;
  try {
    const fetchFn = prepared.options.fetch ?? globalThis.fetch;
    if (!fetchFn) throw new Error("no fetch implementation is available");
    response = await fetchFn(url, {
      method: prepared.operation.method.toUpperCase(),
      headers,
      signal: prepared.options.signal,
    });
  } catch (error: unknown) {
    throw new Swagger20ExecutionError("ERR_CONNECT_FAILED", errorMessage(error), { cause: error });
  }
  const bytes = new Uint8Array(await response.arrayBuffer());
  if (bytes.byteLength !== 0) {
    throw new Swagger20ExecutionError("ERR_RESPONSE_ERROR", "non-empty Swagger 2.0 response requires response-media decoding");
  }
  if (response.status < 200 || response.status >= 300) {
    throw new Swagger20ExecutionError("ERR_HTTP_STATUS", `HTTP ${response.status}`);
  }
  return { outputPresent: false, status: response.status, headers: response.headers };
}

function configuredServer(value: string | undefined): string {
  if (!value) throw new Swagger20ExecutionError("ERR_REFUSED", "Swagger 2.0 target requires a complete server URL");
  let parsed: URL;
  try { parsed = new URL(value); }
  catch (error: unknown) { throw refused(error); }
  if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || parsed.host === "" || parsed.search !== "" || parsed.hash !== "") {
    throw new Swagger20ExecutionError("ERR_REFUSED", "Swagger 2.0 consumer server is not a complete HTTP target URL");
  }
  return value;
}

function refused(error: unknown): Swagger20ExecutionError {
  return error instanceof Swagger20ExecutionError
    ? error
    : new Swagger20ExecutionError("ERR_REFUSED", errorMessage(error), { cause: error });
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
