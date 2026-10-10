"""Exercise the optional native client from actual Cargo package archives.

The core is unpublished. A command-local patch permits Cargo to package the
companion without querying a nonexistent registry release. Consumption then
patches to the extracted core archive, never the workspace implementation.
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import tomllib

root = Path(__file__).resolve().parents[1]
output = Path(sys.argv[1]).resolve()
output.mkdir(parents=True, exist_ok=True)
env = os.environ.copy()
env.setdefault('CARGO_TARGET_DIR', str(root / 'target'))
target = Path(env['CARGO_TARGET_DIR']).resolve()
results = []


def run(name, command, cwd=root):
    log = output / (name + '.log')
    with log.open('w') as stream:
        result = subprocess.run(list(map(str, command)), cwd=cwd, env=env,
                                stdout=stream, stderr=subprocess.STDOUT)
    results.append({'name': name, 'command': list(map(str, command)),
                    'exitCode': result.returncode, 'log': log.name})
    (output / 'commands.json').write_text(json.dumps(results, indent=2) + '\n')
    if result.returncode:
        print(log.read_text()[-5000:], file=sys.stderr)
        raise SystemExit(result.returncode)


lock_before = (root / 'Cargo.lock').read_bytes()
run('package-native', ['cargo', 'package', '--locked', '--no-verify',
                      '-p', 'dynamic-openapi-client-reqwest', '--config',
                      'patch.crates-io.dynamic-openapi-client.path='
                      + json.dumps(str(root / 'client'))])
assert (root / 'Cargo.lock').read_bytes() == lock_before, 'packaging changed workspace lock'
with tempfile.TemporaryDirectory(prefix='native-consumer-', dir=output) as temporary:
    work = Path(temporary)
    for name in ['dynamic-openapi-client', 'dynamic-openapi-client-reqwest']:
        with tarfile.open(target / 'package' / (name + '-0.0.0.crate')) as archive:
            archive.extractall(work, filter='data')
    core = work / 'dynamic-openapi-client-0.0.0'
    native = work / 'dynamic-openapi-client-reqwest-0.0.0'
    consumer = work / 'consumer'
    (consumer / 'src').mkdir(parents=True)
    (consumer / 'src/bin').mkdir()
    deps = tomllib.loads((native / 'Cargo.toml').read_text())['dependencies']
    core_deps = tomllib.loads((core / 'Cargo.toml').read_text())['dependencies']
    (consumer / 'Cargo.toml').write_text(
        '[package]\nname="openapi-native-package-consumer"\nversion="0.0.0"\n'
        'edition="2024"\n[workspace]\n'
        '[features]\narbitrary_precision=["serde_json/arbitrary_precision"]\n'
        'backend_unified=["reqwest/gzip","reqwest/http2"]\n[dependencies]\n'
        'dynamic-openapi-client={path=' + json.dumps(str(core)) + '}\n'
        'dynamic-openapi-client-reqwest={path=' + json.dumps(str(native)) + '}\n'
        'tokio={version=' + json.dumps(deps['tokio']['version'])
        + ',features=["macros","rt-multi-thread","net","io-util","sync","time"]}\n'
        'serde={version=' + json.dumps(core_deps['serde']['version'])
        + ',features=["derive"]}\n'
        'serde_json={version=' + json.dumps(core_deps['serde_json']['version']) + ',features=["raw_value"]}\n'
        'reqwest={version=' + json.dumps(deps['reqwest']['version'])
        + ',default-features=false}\n'
        '[patch.crates-io]\ndynamic-openapi-client={path=' + json.dumps(str(core)) + '}\n')
    shutil.copy2(native / 'examples/real_http.rs', consumer / 'src/main.rs')
    shutil.copy2(root / 'qualification/native_consumer.rs', consumer / 'src/bin/independent-consumer.rs')
    run('example', ['cargo', 'run', '--manifest-path', consumer / 'Cargo.toml',
                    '--bin', 'openapi-native-package-consumer'], consumer)
    for label, features in [('default', None),
                            ('arbitrary-precision', 'arbitrary_precision'),
                            ('backend-unified', 'backend_unified'),
                            ('combined', 'arbitrary_precision,backend_unified')]:
        command = ['cargo', 'run', '--locked', '--manifest-path', consumer / 'Cargo.toml',
                   '--bin', 'independent-consumer']
        if features:
            command += ['--features', features]
        run('independent-consumer-' + label, command, consumer)
    shutil.copy2(consumer / 'Cargo.lock', output / 'consumer-Cargo.lock')
print(json.dumps({'passed': True, 'commands': len(results)}))
