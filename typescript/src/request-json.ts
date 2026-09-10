import { stringifyJSON } from "@openbindings/json";
/** Internal wire serializer: native nonfinite numbers have no JSON value.
 * Do not let JSON.stringify silently substitute null for them. This is not
 * a precision policy for finite numbers or a decoder for wire number tokens.
 */
export function stringifyRequestJSON(value: unknown): string {
  return stringifyJSON(value);
}
