import { describe, expect, it } from "vitest";
import { decodeBase64MultipartParts } from "./resolved-media.js";

const body = (payload: string) =>
  `------b\r\nContent-Disposition: form-data; name="profile"; filename="profile"\r\nContent-Type: application/octet-stream\r\n\r\n${payload}\r\n------b--\r\n`;

describe("raw-octet multipart caller boundary", () => {
  it("replaces a canonical Base64 payload with its represented octets", () => {
    const rewritten = decodeBase64MultipartParts(body("SGVsbG8="), ["profile"]);
    const text = typeof rewritten === "string"
      ? rewritten
      : new TextDecoder().decode(rewritten as Uint8Array);
    expect(text).toContain("\r\n\r\nHello\r\n");
  });

  it("refuses a non-canonical Base64 payload rather than riding it undecoded", () => {
    // "SGVsbG8" is unpadded: atob accepts it, but it is not the canonical
    // spelling of its octets, and the raw-octet lane's boundary is canonical
    // Base64 (openbindings.openapi-3.1@1 §§9.2–9.3; corpus OAPI31-PS-144).
    expect(() => decodeBase64MultipartParts(body("SGVsbG8"), ["profile"]))
      .toThrow(/canonical Base64/u);
    expect(() => decodeBase64MultipartParts(body("!!!!"), ["profile"]))
      .toThrow(/canonical Base64/u);
  });

  it("leaves parts outside the raw-name set untouched", () => {
    const raw = body("SGVsbG8");
    expect(decodeBase64MultipartParts(raw, ["other"])).toBe(raw);
  });
});
