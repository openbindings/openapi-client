"""Compile positive and negative companion consumers, including Wasm locality.

Usage: companion_api.py native|fetch CORE COMPANION OUTPUT
The package gates pass extracted archives; verify.py uses workspace paths.
No witness performs I/O. Negative cases must fail for the intended diagnostic.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

kind, core, companion, destination = sys.argv[1:]
output = Path(destination).resolve()
output.mkdir(parents=True, exist_ok=True)
package = 'dynamic-openapi-client-' + ('reqwest' if kind == 'native' else 'fetch')
crate = package.replace('-', '_')
header = f'''#![allow(dead_code)]
use dynamic_openapi_client::{{Cancellation, Outcome, RequestBuilder}};
use dynamic_openapi_client::transport::{{PreparedRequest, ReplayPolicy}};
use {crate}::{{Client, advanced}};
use std::future::Future;
fn local(_: impl Future<Output = Outcome>) {{}}
fn send(_: impl Future<Output = Outcome> + Send) {{}}
'''
cases = {
    'public-prepare': ('''fn witness(r: RequestBuilder) -> (Result<PreparedRequest, dynamic_openapi_client::Diagnostic>, Cancellation) {
    dynamic_openapi_client::advanced::prepare(r)
}''', None),
    'deleted-companion-handoff': ('fn witness(r: RequestBuilder) { dynamic_openapi_client::companion::prepare(r); }', 'E0433'),
}
if kind == 'native':
    cases['positive'] = ('''fn witness(c: &Client, r: RequestBuilder, p: &PreparedRequest) {
    send(c.send(r));
    send(advanced::execute(c, p, Cancellation::default()));
    let _: Result<Client, dynamic_openapi_client::Diagnostic> = Client::new();
    let _: Result<Client, dynamic_openapi_client::Diagnostic> = Client::builder().build();
    let _: Option<dynamic_openapi_client_reqwest::tls::Certificate> = None;
}''', None)
    cases['deleted-request'] = ('fn witness(c: &Client, o: &dynamic_openapi_client::Operation) { c.request(o); }', 'E0599')
    cases['deleted-execute'] = ('fn witness(c: &Client, p: &PreparedRequest) { c.execute(p, Cancellation::default()); }', 'E0599')
    cases['moved-certificate'] = ('use dynamic_openapi_client_reqwest::Certificate;', 'E0603')
else:
    cases['positive'] = ('''fn witness(c: &Client, r: RequestBuilder, p: &PreparedRequest) {
    local(c.send(r, ReplayPolicy::RequireNoReplay));
    local(advanced::execute(c, p, Cancellation::default(), ReplayPolicy::AllowHostManaged));
}''', None)
    cases['deleted-execute'] = ('fn witness(c: &Client, p: &PreparedRequest) { c.execute(p, Cancellation::default()); }', 'E0599')
    cases['deleted-execute-with-replay'] = ('fn witness(c: &Client, p: &PreparedRequest) { c.execute_with_replay(p, Cancellation::default(), ReplayPolicy::AllowHostManaged); }', 'E0599')
    cases['mandatory-replay'] = ('fn witness(c: &Client, r: RequestBuilder) { c.send(r); }', 'E0061')
    cases['local-send'] = ('fn witness(c: &Client, r: RequestBuilder) { send(c.send(r, ReplayPolicy::RequireNoReplay)); }', 'cannot be sent between threads safely')
    cases['local-advanced'] = ('fn witness(c: &Client, p: &PreparedRequest) { send(advanced::execute(c, p, Cancellation::default(), ReplayPolicy::AllowHostManaged)); }', 'cannot be sent between threads safely')

results = []
with tempfile.TemporaryDirectory(prefix=kind + '-api-', dir=output) as directory:
    work = Path(directory)
    (work / 'src').mkdir()
    (work / 'Cargo.toml').write_text(
        '[package]\nname="companion-api-witness"\nversion="0.0.0"\nedition="2024"\n'
        '[workspace]\n[dependencies]\n'
        'dynamic-openapi-client={path=' + json.dumps(str(Path(core).resolve())) + '}\n'
        + package + '={path=' + json.dumps(str(Path(companion).resolve())) + '}\n'
        '[patch.crates-io]\ndynamic-openapi-client={path='
        + json.dumps(str(Path(core).resolve())) + '}\n')
    env = os.environ.copy()
    env.setdefault('CARGO_TARGET_DIR', str(Path(__file__).resolve().parents[1] / 'target'))
    for name, (body, expected) in cases.items():
        source = header + body + '\n'
        (work / 'src/lib.rs').write_text(source)
        (output / (name + '.rs')).write_text(source)
        command = ['cargo', 'check', '--offline', '--manifest-path', str(work / 'Cargo.toml')]
        if kind == 'fetch':
            command += ['--target', 'wasm32-unknown-unknown']
        result = subprocess.run(command, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        (output / (name + '.log')).write_text(result.stdout)
        passed = result.returncode == 0 if expected is None else result.returncode != 0 and expected in result.stdout
        results.append({'case': name, 'expected': expected, 'exitCode': result.returncode, 'passed': passed})
        (output / 'results.json').write_text(json.dumps(results, indent=2) + '\n')
        if not passed:
            raise SystemExit(result.stdout)
print(json.dumps({'passed': True, 'cases': len(results), 'kind': kind}))
