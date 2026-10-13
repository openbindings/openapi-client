"""Build the Wasm consumer using only extracted core and Fetch package archives."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import tomllib

root = Path(__file__).resolve().parents[2]
output = root / 'target/qualification/fetch-consumer'
output.mkdir(parents=True, exist_ok=True)
env = os.environ.copy()
env.setdefault('CARGO_TARGET_DIR', str(root / 'target'))
target = Path(env['CARGO_TARGET_DIR']).resolve()
results = []


def run(name, command, cwd=root):
    print('+ ' + ' '.join(map(str, command)), flush=True)
    with (output / (name + '.log')).open('w') as log:
        result = subprocess.run(list(map(str, command)), cwd=cwd, env=env,
                                stdout=log, stderr=subprocess.STDOUT)
    results.append({'name': name, 'command': list(map(str, command)),
                    'exitCode': result.returncode, 'log': name + '.log'})
    (output / 'commands.json').write_text(json.dumps(results, indent=2) + '\n')
    if result.returncode:
        raise SystemExit((output / (name + '.log')).read_text()[-5000:])


lock_before = (root / 'Cargo.lock').read_bytes()
run('package-core', ['cargo', 'package', '--locked', '--allow-dirty', '--no-verify',
                     '-p', 'dynamic-openapi-client'])
run('package-fetch', ['cargo', 'package', '--locked', '--allow-dirty', '--no-verify',
                      '-p', 'dynamic-openapi-client-fetch', '--config',
                      'patch.crates-io.dynamic-openapi-client.path='
                      + json.dumps(str(root / 'client'))])
assert (root / 'Cargo.lock').read_bytes() == lock_before, 'packaging changed workspace lock'
with tempfile.TemporaryDirectory(prefix='archives-', dir=output) as temporary:
    work = Path(temporary)
    archives = []
    for name in ['dynamic-openapi-client', 'dynamic-openapi-client-fetch']:
        archive = target / 'package' / (name + '-0.0.0.crate')
        archives.append({'name': name, 'sha256': hashlib.sha256(archive.read_bytes()).hexdigest()})
        with tarfile.open(archive) as packed:
            packed.extractall(work, filter='data')
    core = work / 'dynamic-openapi-client-0.0.0'
    fetch = work / 'dynamic-openapi-client-fetch-0.0.0'
    # The supplementary controls also consume the archived JS plumbing.
    shutil.copy2(fetch / 'src/host.js', output / 'host.mjs')
    consumer = work / 'consumer'
    shutil.copytree(root / 'qualification/fetch-consumer', consumer)
    manifest = consumer / 'Cargo.toml'
    with manifest.open('a') as stream:
        stream.write('\n[patch.crates-io]\n'
                     'dynamic-openapi-client = { path = ' + json.dumps(str(core)) + ' }\n'
                     'dynamic-openapi-client-fetch = { path = ' + json.dumps(str(fetch)) + ' }\n')
    shutil.copy2(manifest, output / 'consumer-Cargo.toml')
    # Retain reviewed versions while Cargo adds this standalone consumer's root.
    (consumer / 'Cargo.lock').write_bytes(lock_before)
    run('consumer-lock', ['cargo', 'metadata', '--format-version', '1',
                          '--manifest-path', manifest], consumer)
    shutil.copy2(consumer / 'Cargo.lock', output / 'consumer-Cargo.lock')
    # Pin the consumer's selected registry dependencies to the reviewed workspace graph.
    qualified = {(p['name'], p['version'], p.get('checksum'))
                 for p in tomllib.loads(lock_before.decode())['package'] if 'source' in p}
    selected = tomllib.loads((consumer / 'Cargo.lock').read_text())['package']
    assert all((p['name'], p['version'], p.get('checksum')) in qualified
               for p in selected if 'source' in p), 'consumer dependency graph changed'
    run('consumer-format', ['cargo', 'fmt', '--manifest-path', manifest, '--check'], consumer)
    run('consumer-clippy', ['cargo', 'clippy', '--locked', '--manifest-path', manifest,
                           '--target', 'wasm32-unknown-unknown', '--', '-D', 'warnings'], consumer)
    run('consumer-build', ['cargo', 'build', '--locked', '--manifest-path', manifest,
                          '--release', '--target', 'wasm32-unknown-unknown'], consumer)
    # A metadata assertion ensures both product crates came from the archives.
    metadata = json.loads(subprocess.check_output(
        ['cargo', 'metadata', '--locked', '--format-version', '1', '--manifest-path', str(manifest)],
        cwd=consumer, env=env))
    for name, directory in [('dynamic-openapi-client', core), ('dynamic-openapi-client-fetch', fetch)]:
        packages = [p for p in metadata['packages'] if p['name'] == name]
        assert len(packages) == 1 and Path(packages[0]['manifest_path']).parent == directory
    (output / 'archives.json').write_text(json.dumps(archives, indent=2) + '\n')
    run('bindgen', ['wasm-bindgen', target / 'wasm32-unknown-unknown/release/oac_fetch_consumer.wasm',
                    '--target', 'web', '--out-dir', output / 'generated'])
    run('js-lifecycle', ['node', root / 'fetch/tests/lifecycle.mjs', output / 'host.mjs'])
print(json.dumps({'passed': True, 'commands': len(results), 'output': str(output)}))
