import { describe, expect, it } from "vitest";

import {
  buildOpenAPI32MultipartBody,
  buildOpenAPI32SequentialBody,
  normalizeOpenAPI32JSONNumber,
  openAPI32RequestMediaAdmission,
  serializeOpenAPI32NonJSONText,
  validateOpenAPI32MultipartFields,
} from "./openapi32-media.js";
import type { OpenAPIMediaType } from "./types.js";

describe("OpenAPI 3.2 request media", () => {
  it("copies the Go twin's exact shortest-number table", () => {
    const cases: Record<string, string> = {
      "-0": "0", "1000.00": "1e3", "1.2300e+03": "1230",
      "0.000001": "1e-6", "1e100000000000000000000": "1e100000000000000000000",
    };
    for (const [input, expected] of Object.entries(cases)) {
      expect(normalizeOpenAPI32JSONNumber(input)).toBe(expected);
    }
  });

  it("frames sequential JSON and spells non-JSON scalar text exactly", () => {
    expect(buildOpenAPI32SequentialBody("json-lines", [{ n: 1 }, { n: 2 }]))
      .toBe("{\"n\":1}\n{\"n\":2}\n");
    expect(buildOpenAPI32SequentialBody("json-seq", [true, 12.5]))
      .toBe("\u001etrue\n\u001e12.5\n");
    expect(serializeOpenAPI32NonJSONText({ type: ["boolean", "number"] }, true)).toBe("true");
    expect(serializeOpenAPI32NonJSONText({ type: ["boolean", "number"] }, 1000)).toBe("1e3");
  });

  it("confines request-only SSE and unframed itemSchema alternatives", () => {
    expect(openAPI32RequestMediaAdmission("text/event-stream", { itemSchema: { type: "object" } }))
      .toMatchObject({ handled: true, error: expect.stringContaining("no incorporated request write algorithm") });
    expect(openAPI32RequestMediaAdmission("application/json", { itemSchema: { type: "object" } }))
      .toMatchObject({ handled: true, error: expect.stringContaining("no incorporated sequential request framing") });
    expect(openAPI32RequestMediaAdmission("application/jsonl", {
      schema: { type: "array" },
      itemSchema: { type: "object" },
    })).toEqual({ handled: true, family: "sequential", sequentialKind: "json-lines" });
  });

  it("writes positional and one-level nested multipart overlays", () => {
    const positional: OpenAPIMediaType = {
      itemSchema: { type: "string", contentEncoding: "base64" },
      prefixEncoding: [{
        contentType: "application/octet-stream",
        headers: {
          "Content-Disposition": { schema: { const: "form-data; name=first" } },
          "X-Part": { schema: { enum: ["prefix"] } },
        },
      }],
      itemEncoding: {
        contentType: "application/octet-stream",
        headers: { "Content-Disposition": { schema: { const: "form-data; name=rest" } } },
      },
    };
    const positionalWire = buildOpenAPI32MultipartBody(
      "multipart/form-data",
      positional,
      { bodyFields: {}, bodyValue: ["QUJD", "REVG"], bodySet: true },
    );
    const positionalBody = new TextDecoder().decode(positionalWire.body);
    expect(positionalBody).toContain("Content-Disposition: form-data; name=first");
    expect(positionalBody).toContain("Content-Disposition: form-data; name=rest");
    // R5 (2026-09-01): `contentEncoding` never produces the field. OAS 3.2.0
    // §4.15.4.2 states the equivalence descriptively and RFC 7578 §4.7 says
    // senders SHOULD NOT generate a Content-Transfer-Encoding header field.
    expect(positionalBody).not.toContain("Content-Transfer-Encoding");
    expect(positionalBody).toContain("X-Part: prefix");

    const nested: OpenAPIMediaType = {
      schema: {
        type: "object",
        properties: { bundle: { type: "array", items: { type: "string" } } },
      },
      encoding: {
        bundle: {
          contentType: "multipart/mixed",
          prefixEncoding: [{ contentType: "text/plain" }],
          itemEncoding: { contentType: "text/plain" },
        },
      },
    };
    const nestedWire = buildOpenAPI32MultipartBody(
      "multipart/form-data",
      nested,
      { bodyFields: { bundle: ["alpha", "beta"] }, bodyValue: undefined, bodySet: false },
    );
    const nestedBody = new TextDecoder().decode(nestedWire.body);
    expect(nestedBody).toContain("Content-Type: multipart/mixed; boundary=");
    expect(nestedBody).toContain("\r\n\r\nalpha\r\n");
    expect(nestedBody).toContain("\r\n\r\nbeta\r\n");
  });

  it("refuses a reached part transfer contradiction", () => {
    const media: OpenAPIMediaType = {
      schema: {
        type: "object",
        properties: { file: { type: "string", contentEncoding: "base64" } },
      },
      encoding: {
        file: {
          headers: {
            "Content-Transfer-Encoding": { schema: { type: "string", const: "gzip" } },
          },
        },
      },
    };
    expect(() => validateOpenAPI32MultipartFields(media, { file: "QUJD" }))
      .toThrow(/disallows contentEncoding/u);
    expect(() => validateOpenAPI32MultipartFields(media, {})).not.toThrow();
  });
});
