/** Bounded response reading shared by the unary edition lanes. */
export class ResponseBodyLimitError extends Error {}

export async function readResponseBytes(resp: Response, maxBytes: number): Promise<Uint8Array> {
  if (!resp.body) return new Uint8Array(await resp.arrayBuffer());
  const reader = resp.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > maxBytes) {
        // Tee cancellation may await a retained replay. Signal cancellation
        // now; the owner cancels the replay when the terminal error arrives.
        void reader.cancel().catch(() => undefined);
        throw new ResponseBodyLimitError(`response exceeds ${maxBytes} byte limit`);
      }
      chunks.push(value);
    }
  } finally { reader.releaseLock(); }
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) { out.set(chunk, offset); offset += chunk.byteLength; }
  return out;
}
