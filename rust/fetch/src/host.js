// Static CSP-compatible host plumbing. No protocol serialization or Rust captures.
export function capture() {
  const g = globalThis;
  for (const key of ['fetch', 'Request', 'AbortController', 'setTimeout', 'clearTimeout']) {
    if (typeof g[key] !== 'function') throw new TypeError('unavailable');
  }
  return { global: g, fetch: g.fetch, Request: g.Request, AbortController: g.AbortController,
    setTimeout: g.setTimeout, clearTimeout: g.clearTimeout };
}
export function controller(bindings) { return new bindings.AbortController(); }
export function request(bindings, target, init) { return new bindings.Request(target, init); }
export function fetchOnce(bindings, request) { return bindings.fetch.call(bindings.global, request); }
export function get(object, key) { return object[key]; }
export function call0(object, key) { return object[key](); }
export function call1(object, key, argument) { return object[key](argument); }

// Original promise callbacks own only this JavaScript state. stop always settles
// the wrapper; Rust JsFuture handlers attach only to that wrapper, never original.
export function watch(original) {
  let active = true;
  let settle;
  const promise = new Promise(resolve => { settle = resolve; });
  Promise.resolve(original).then(value => {
    if (!active) return;
    active = false;
    const resolve = settle; settle = undefined;
    resolve([true, value]);
  }, () => {
    if (!active) return;
    active = false;
    const resolve = settle; settle = undefined;
    resolve([false, undefined]);
  });
  return { promise, stop() {
    if (!active) return;
    active = false;
    const resolve = settle; settle = undefined;
    resolve([false, undefined]);
  } };
}
export function stop(observation) { observation.stop(); }
export function yieldTask(bindings) {
  let active = true;
  let settle;
  const promise = new Promise(resolve => { settle = resolve; });
  const timer = bindings.setTimeout.call(bindings.global, () => {
    if (!active) return;
    active = false;
    settle([true, undefined]);
  }, 0);
  return { promise, stop() {
    if (!active) return;
    active = false;
    try { bindings.clearTimeout.call(bindings.global, timer); }
    finally { settle([false, undefined]); }
  } };
}
function observe(promise) { Promise.resolve(promise).then(() => {}, () => {}); }
export function observeClosed(reader) {
  try { observe(reader.closed); } catch (_) {}
}
export function cleanup(controller, reader, abort) {
  // Each attempt is independent, including synchronous throws and rejected cleanup.
  if (abort) { try { controller.abort(); } catch (_) {} }
  if (reader !== undefined) {
    if (abort) { try { observe(reader.cancel()); } catch (_) {} }
    try { reader.releaseLock(); } catch (_) {}
  }
}
