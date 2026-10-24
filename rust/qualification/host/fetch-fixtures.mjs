// Finite loopback fixtures plus responses that have no completion timer.
export function fetchFixtures() {
  const states = new Map();
  const empty = () => ({ received: 0, active: 0, finished: 0, closed: 0 });
  return (req, res, url) => {
    const [, route, id, kind] = url.pathname.split('/');
    if (route === 'fetch-state') {
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify(states.get(id)?.facts ?? empty()));
      return true;
    }
    if (route === 'fetch-release') {
      const state = states.get(id);
      for (const response of state?.responses ?? []) response.end(state.suffix);
      res.end('released');
      return true;
    }
    if (route !== 'fetch') return false;
    let state = states.get(id);
    if (!state) states.set(id, state = { facts: empty(), responses: new Set() });
    state.facts.received++;
    state.facts.active++;
    state.responses.add(res);
    res.on('finish', () => state.facts.finished++);
    res.on('close', () => {
      state.facts.closed++;
      state.facts.active--;
      state.responses.delete(res);
    });
    if (kind === 'stall-headers') return true;
    res.setHeader('Content-Type', kind === 'echo' ? 'application/json' : 'text/plain');
    if (['stall-body', 'hold-body', 'boundary-later', 'overflow-later'].includes(kind)) {
      state.suffix = kind === 'boundary-later' ? '' : kind === 'overflow-later' ? 'e' : 'cd';
      res.write(kind.endsWith('-later') ? 'abcd' : 'ab');
    } else if (kind === 'echo') {
      res.end(JSON.stringify({ target: req.url, method: req.method, headers: req.headers }));
    } else if (kind === 'boundary') {
      res.end('abcd');
    } else if (kind === 'overflow') {
      res.end('abcde');
    } else if (kind === 'empty') {
      res.end();
    } else if (kind === 'one-byte') {
      res.end('a');
    } else if (kind === 'redirect') {
      res.writeHead(302, { Location: `/fetch/${id}/echo` });
      res.end();
    } else {
      res.writeHead(404);
      res.end();
    }
    return true;
  };
}
