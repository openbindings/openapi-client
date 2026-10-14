import { observeFetch } from './fetch-observer.mjs';

const text = bytes => new TextDecoder().decode(Uint8Array.from(bytes));
export async function runFetch(api, base, host, runId) {
  const observer = observeFetch(), cases = [], owned = new Set();
  const resources = () => ({
    owners: api.live_owners(), js_values: api.live_js_values(), ...observer.snapshot(),
  });
  const initial = resources();
  let client = new api.FetchClient(host);
  const state = async id => (await observer.fetch(`${base}/fetch-state/${id}`)).json();
  async function until(test, label, timeout = 5000) {
    const deadline = Date.now() + timeout;
    while (!await test()) {
      if (Date.now() >= deadline) throw Error(`timeout: ${label}`);
      await observer.tick(5);
    }
  }
  function start(id, kind, { limit = 4096, headers = [], query = {}, allow = true, cancelled = false, suffix = '' } = {}) {
    const path = `/fetch/${id}/${kind}${suffix}`;
    const document = {
      openapi: '3.1.2', info: { title: 'packaged Fetch consumer', version: '1' },
      servers: [{ url: base }],
      paths: { [path]: { get: {
        operationId: 'probe', responses: { '200': { description: 'ok' } },
        parameters: Object.keys(query).map(name => ({ name, in: 'query', schema: { type: 'string' } })),
      } } },
    };
    const call = client.start(JSON.stringify(document), JSON.stringify({ headers, query }), limit, allow, cancelled);
    owned.add(call);
    return call;
  }
  function dispose(call) { call.free(); owned.delete(call); }
  async function finish(call) {
    let result;
    await until(() => (result = call.poll()) !== undefined && result !== null, 'product future completion');
    return JSON.parse(result);
  }
  async function reading(call, previous, prefix = 2) {
    await until(() => {
      const result = call.poll();
      if (result != null) throw Error('stalled fixture unexpectedly completed');
      const now = observer.snapshot();
      return now.pending_reads > 0 && now.read_calls >= previous.read_calls + 2
        && now.delivered_bytes >= previous.delivered_bytes + prefix;
    }, 'pending read after received prefix');
  }
  async function probe(name, action) {
    const id = `${runId}-${host}-${name}`, ids = [id], assertions = [], before = resources();
    const facts = {};
    const check = (label, passed, actual) => {
      assertions.push({ assertion: label, passed: Boolean(passed), actual });
      if (!passed) throw Error(`assertion failed: ${label}`);
    };
    let error;
    try { await action({ id, ids, facts, check, before }); }
    catch (failure) { error = String(failure); }
    finally {
      for (const call of owned) call.free();
      owned.clear();
      try {
        await until(async () => {
          const now = resources();
          return now.owners === before.owners && now.js_values === before.js_values
            && now.pending_promises === 0 && now.reader_locks === 0 && now.timers === 0
            && (await Promise.all(ids.map(state))).every(value => value.active === 0);
        }, 'Rust owners, JS handles, promises, locks, timers and server responses released');
        const after = resources();
        facts.resources = { before, after, server: await Promise.all(ids.map(state)) };
        check('all measured resources returned to baseline after disposal', true, facts.resources);
      } catch (failure) {
        facts.resources = { before, after: resources(), server: await Promise.all(ids.map(state)) };
        assertions.push({ assertion: 'all measured resources returned to baseline after disposal', passed: false, actual: facts.resources });
        error ??= String(failure);
      }
    }
    cases.push({ id: `${host}-fetch-${name}`, group: 'fetch-transport', executed: true, passed: !error, assertions, facts, ...(error ? { error } : {}) });
  }
  try {
    for (const [name, kind, limit, expected, bodyState] of [
      ['at-boundary', 'boundary', 4, 'abcd', 'Complete'],
      ['beyond-boundary', 'overflow', 4, 'abcd', 'Truncated'],
      ['zero-boundary', 'empty', 0, '', 'Complete'],
      ['beyond-zero', 'one-byte', 0, '', 'Truncated'],
    ]) {
      await probe(name, async ({ id, facts, check, before }) => {
        const call = start(id, kind, { limit });
        const out = facts.outcome = await finish(call);
        check('retained bytes equal the admitted prefix', text(out.response?.raw ?? []) === expected, out.response);
        check('EOF and excess bytes have distinct body states', out.response?.body_state === bodyState, out.response);
        check('response is dispatched, HostDelivered, with unknown upload and explicit replay',
          out.dispatch === 'Dispatched' && out.error === null && out.response.status === 200
          && out.response.provenance === 'HostDelivered' && out.upload === 'Unknown' && out.replay === 'HostManaged', out);
        check('opt-in enters Fetch once and reaches the fixture once', observer.snapshot().fetch_calls === before.fetch_calls + 1
          && (await state(id)).received === 1, { fetch_calls: observer.snapshot().fetch_calls - before.fetch_calls, server: await state(id) });
        dispose(call);
      });
    }
    for (const [kind, bodyState] of [['boundary-later', 'Complete'], ['overflow-later', 'Truncated']]) {
      await probe(kind, async ({ id, facts, check, before }) => {
        const call = start(id, kind, { limit: 4 });
        await reading(call, before, 4);
        check('reaching the byte limit waits for additional EOF or positive-byte evidence', call.poll() == null
          && observer.snapshot().pending_reads === 1 && (await state(id)).finished === 0, observer.snapshot());
        await observer.fetch(`${base}/fetch-release/${id}`);
        const out = facts.outcome = await finish(call);
        check('later EOF versus an extra byte produces the expected completion state', out.error === null
          && out.response?.body_state === bodyState && text(out.response.raw) === 'abcd', out);
        dispose(call);
      });
    }
    await probe('url-headers', async ({ id, facts, check, before }) => {
      const call = start(id, 'echo', { suffix: '/%7evalue', query: { q: 'a/b c' }, headers: [['X-Probe', 'preserved, value'], ['X-Second', 'second']] });
      const wire = facts.prepared = JSON.parse(call.wire());
      const out = facts.outcome = await finish(call);
      const echo = facts.server_echo = JSON.parse(text(out.response?.raw ?? []));
      const observed = facts.host_request = observer.calls[before.fetch_calls];
      const target = `${base}/fetch/${id}/echo/%7evalue?q=a%2Fb%20c`;
      check('the echo response completes successfully', out.error === null && out.response?.status === 200
        && out.response.body_state === 'Complete', out);
      check('prepared URL spelling survives Request and the server request target', wire.target === target && observed.target === target && echo.target === target.slice(base.length), { wire, observed, echo });
      check('method and each supplied header value survive preflight and dispatch',
        wire.method === 'GET' && observed.method === 'GET' && echo.method === 'GET'
        && echo.headers['x-probe'] === 'preserved, value' && echo.headers['x-second'] === 'second'
        && observed.headers.some(([name, value]) => name === 'x-probe' && value === 'preserved, value')
        && observed.headers.some(([name, value]) => name === 'x-second' && value === 'second'), echo);
      dispose(call);
    });
    for (const [name, options, reason] of [
      ['replay-default', { allow: false }, 'Replay'],
      ['forbidden-header', { headers: [['Host', 'other.example']] }, 'ForbiddenHeader'],
      ['duplicate-header', { headers: [['X-Probe', 'one'], ['x-probe', 'two']] }, 'ForbiddenHeader'],
      ['encoded-dot', { suffix: '/%2e/child' }, 'EncodedDotSegment'],
    ]) {
      await probe(name, async ({ id, facts, check, before }) => {
        const call = start(id, 'echo', options);
        const out = facts.outcome = await finish(call);
        check('controlled refusal occurs before Fetch and server receipt',
          out.dispatch === 'NotDispatched' && out.upload === 'NotStarted' && out.error === 'HostCapability'
          && out.reason === `Host(${reason})` && out.replay === null && out.response === null
          && observer.snapshot().fetch_calls === before.fetch_calls && (await state(id)).received === 0, out);
        dispose(call);
      });
    }
    await probe('cancel-before', async ({ id, facts, check, before }) => {
      const call = start(id, 'boundary', { cancelled: true });
      const out = facts.outcome = await finish(call);
      check('pre-cancelled token refuses without dispatch or I/O', out.cancelled && out.error === 'Cancelled'
        && out.dispatch === 'NotDispatched' && out.response === null
        && observer.snapshot().fetch_calls === before.fetch_calls && (await state(id)).received === 0, out);
      dispose(call);
    });
    await probe('cancel-headers', async ({ id, facts, check, before }) => {
      const call = start(id, 'stall-headers');
      check('the product future enters a pending Fetch wait', call.poll() == null, observer.snapshot());
      await until(async () => (await state(id)).received === 1, 'server received stalled headers request');
      check('Fetch promise remains pending before cancellation', observer.snapshot().pending_fetches === 1, observer.snapshot());
      call.cancel();
      const out = facts.outcome = await finish(call);
      check('cancellation ends the dispatched wait and aborts its controller', out.cancelled
        && out.dispatch === 'Dispatched' && out.error === 'TransportFailure' && out.response === null
        && observer.snapshot().aborts > before.aborts && observer.calls[before.fetch_calls].aborted, out);
      dispose(call);
    });
    await probe('cancel-stalled-body', async ({ id, facts, check, before }) => {
      const call = start(id, 'stall-body');
      await reading(call, before);
      await observer.tick(50);
      check('body has no EOF or timeout and remains pending until cancellation', call.poll() == null
        && (await state(id)).finished === 0 && observer.snapshot().pending_reads === 1, observer.snapshot());
      call.cancel();
      const out = facts.outcome = await finish(call);
      check('cancellation preserves the prefix as Failed and releases the reader', out.cancelled
        && out.dispatch === 'Dispatched' && out.error === 'TransportFailure'
        && out.response?.body_state === 'Failed' && text(out.response.raw) === 'ab'
        && observer.snapshot().aborts > before.aborts && observer.calls[before.fetch_calls].aborted
        && observer.snapshot().releases === before.releases + 1, out);
      dispose(call);
    });
    for (const kind of ['stall-headers', 'stall-body']) {
      await probe(`drop-${kind}`, async ({ id, facts, check, before }) => {
        const call = start(id, kind);
        if (kind === 'stall-body') await reading(call, before);
        else {
          call.poll();
          await until(async () => (await state(id)).received === 1, 'server received future to drop');
        }
        facts.pending_before_drop = observer.snapshot();
        check('drop occurs while actual host I/O is pending', kind === 'stall-body'
          ? facts.pending_before_drop.pending_reads === 1 : facts.pending_before_drop.pending_fetches === 1, facts.pending_before_drop);
        dispose(call);
        check('dropping the Rust future aborts its Request signal', observer.snapshot().aborts > before.aborts
          && observer.calls[before.fetch_calls].aborted, observer.calls[before.fetch_calls]);
        check('server fixture did not supply completion', (await state(id)).finished === 0, await state(id));
      });
    }
    await probe('independent-tokens', async ({ id, ids, facts, check, before }) => {
      const siblingId = id + '-sibling'; ids.push(siblingId);
      const first = start(id, 'stall-body'), sibling = start(siblingId, 'hold-body');
      await reading(first, before);
      const previous = observer.snapshot();
      await reading(sibling, previous);
      check('two independently owned readers are pending', observer.snapshot().pending_reads === 2, observer.snapshot());
      first.cancel();
      const cancelled = facts.cancelled_outcome = await finish(first);
      dispose(first);
      check('cancelling one token leaves the sibling token and call pending', cancelled.cancelled
        && !sibling.is_cancelled() && sibling.poll() == null && observer.snapshot().reader_locks === 1, observer.snapshot());
      await observer.fetch(`${base}/fetch-release/${siblingId}`);
      const completed = facts.sibling_outcome = await finish(sibling);
      check('the sibling completes after its own server response is released', !completed.cancelled
        && completed.error === null && completed.response?.body_state === 'Complete'
        && text(completed.response.raw) === 'abcd', completed);
      dispose(sibling);
    });
    client.free(); client = null;
    await observer.tick();
    const after = resources(), released = after.owners === initial.owners && after.js_values === initial.js_values;
    cases.push({ id: `${host}-fetch-client-release`, group: 'fetch-transport', executed: true, passed: released,
      assertions: [{ assertion: 'disposing the reusable client returns Rust owners and owned Wasm references to the suite baseline', passed: released, actual: { initial, after } }] });
    return { cases, passed: cases.every(row => row.passed), live_after: after, measurement: 'Rust JSON owners; owned Wasm externrefs; native Fetch/read/closed/cancel promises; reader locks; product timers; active server responses. No GC, RSS or global memory bound.' };
  } finally {
    for (const call of owned) call.free();
    client?.free();
    observer.restore();
  }
}
