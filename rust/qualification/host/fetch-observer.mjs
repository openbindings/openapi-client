// Observe the real host APIs while forwarding their original arguments/results.
// No synthetic response or replacement transport participates in the host run.
export function observeFetch() {
  const originals = Object.fromEntries(
    ['fetch', 'AbortController', 'setTimeout', 'clearTimeout'].map(key => [key, globalThis[key]]),
  );
  const pending = new Set(), readers = new Set(), timers = new Set();
  const calls = [];
  let aborts = 0, readCalls = 0, deliveredBytes = 0, cancels = 0, releases = 0;
  function watch(promise, kind) {
    const entry = { kind };
    pending.add(entry);
    Promise.resolve(promise).then(() => pending.delete(entry), () => pending.delete(entry));
    return promise;
  }
  function responseReader(response) {
    if (!response.body) return;
    const stream = response.body;
    const getReader = stream.getReader;
    stream.getReader = function (...args) {
      const reader = getReader.apply(this, args);
      readers.add(reader);
      watch(reader.closed, 'closed');
      const read = reader.read, cancel = reader.cancel, releaseLock = reader.releaseLock;
      reader.read = function (...args) {
        readCalls++;
        const promise = watch(read.apply(this, args), 'read');
        promise.then(result => { deliveredBytes += result.value?.byteLength ?? 0; }, () => {});
        return promise;
      };
      reader.cancel = function (...args) {
        cancels++;
        return watch(cancel.apply(this, args), 'cancel');
      };
      reader.releaseLock = function (...args) {
        const result = releaseLock.apply(this, args);
        releases++;
        readers.delete(reader);
        return result;
      };
      return reader;
    };
  }
  globalThis.fetch = function (...args) {
    const request = args[0];
    const record = { target: request.url, method: request.method, headers: [...request.headers], aborted: request.signal.aborted };
    calls.push(record);
    request.signal.addEventListener('abort', () => { record.aborted = true; }, { once: true });
    // The product receives the same native Fetch promise and Response.
    const promise = originals.fetch.apply(this, args);
    watch(promise, 'fetch');
    promise.then(responseReader, () => {});
    return promise;
  };
  globalThis.AbortController = class extends originals.AbortController {
    abort(...args) {
      aborts++;
      return super.abort(...args);
    }
  };
  globalThis.setTimeout = function (callback, delay, ...args) {
    const timer = originals.setTimeout.call(this, (...values) => {
      timers.delete(timer);
      callback(...values);
    }, delay, ...args);
    timers.add(timer);
    return timer;
  };
  globalThis.clearTimeout = function (timer) {
    timers.delete(timer);
    return originals.clearTimeout.call(this, timer);
  };
  return {
    calls,
    // Harness controls bypass observation, including their timers and HTTP requests.
    fetch: originals.fetch.bind(globalThis),
    tick: (ms = 0) => new Promise(resolve => originals.setTimeout.call(globalThis, resolve, ms)),
    snapshot: () => ({
      pending_promises: pending.size,
      pending_fetches: [...pending].filter(entry => entry.kind === 'fetch').length,
      pending_reads: [...pending].filter(entry => entry.kind === 'read').length,
      reader_locks: readers.size,
      timers: timers.size,
      fetch_calls: calls.length,
      read_calls: readCalls,
      delivered_bytes: deliveredBytes,
      aborts, cancels, releases,
    }),
    restore() { Object.assign(globalThis, originals); },
  };
}
