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
    if cwd != root:
        # Preserve resolved consumer inputs even when a first attempt fails.
        for filename in ['Cargo.toml', 'Cargo.lock']:
            source = cwd / filename
            if source.exists():
                shutil.copy2(source, output / ('consumer-' + filename))
    results.append({'name': name, 'command': list(map(str, command)),
                    'exitCode': result.returncode, 'log': log.name})
    (output / 'commands.json').write_text(json.dumps(results, indent=2) + '\n')
    if result.returncode:
        print(log.read_text()[-5000:], file=sys.stderr)
        raise SystemExit(result.returncode)


lock_before = (root / 'Cargo.lock').read_bytes()
backend_names = {'reqwest', 'hyper', 'hyper-util'}
qualified_backend = sorted([
    (package['name'], package['version'], package['checksum'])
    for package in tomllib.loads(lock_before.decode())['package']
    if package['name'] in backend_names
])
assert {name for name, _, _ in qualified_backend} == backend_names
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
    native_manifest = tomllib.loads((native / 'Cargo.toml').read_text())
    deps = native_manifest['dependencies']
    native_dev_deps = native_manifest['dev-dependencies']
    core_deps = tomllib.loads((core / 'Cargo.toml').read_text())['dependencies']
    # Run the archived companion's socket tests as an external public consumer,
    # including its test-only observation dependencies. Read constraints/features
    # from the actual normalized archive instead of duplicating their versions.
    extra_dev_deps = []
    # Archived integration tests can use their crate's ordinary dependencies as
    # well as its dev dependencies. Expose the former only to this consumer's
    # tests, so the ordinary example still uses the original public API inputs.
    test_dependencies = {
        name: dependency for name, dependency in deps.items()
        if name not in {'dynamic-openapi-client', 'reqwest', 'tokio'}
    }
    test_dependencies.update(native_dev_deps)
    for name, dependency in test_dependencies.items():
        if name in {'serde', 'serde_json'}:
            continue
        assert set(dependency) <= {'version', 'features', 'default-features', 'package'}, dependency
        extra_dev_deps.append(name + '={' + ','.join(
            key + '=' + json.dumps(value) for key, value in dependency.items()) + '}\n')
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
        '[dev-dependencies]\n' + ''.join(extra_dev_deps)
        + '[patch.crates-io]\ndynamic-openapi-client={path=' + json.dumps(str(core)) + '}\n')
    shutil.copy2(native / 'examples/real_http.rs', consumer / 'src/main.rs')
    shutil.copy2(root / 'qualification/native_consumer.rs', consumer / 'src/bin/independent-consumer.rs')
    shutil.copytree(native / 'tests', consumer / 'tests')
    run('example', ['cargo', 'run', '--manifest-path', consumer / 'Cargo.toml',
                    '--bin', 'openapi-native-package-consumer'], consumer)
    selected_packages = tomllib.loads((consumer / 'Cargo.lock').read_text())['package']
    selected_backend = sorted([
        (package['name'], package['version'], package['checksum'])
        for package in selected_packages
        if package['name'] in backend_names
    ])
    backend_edges = {
        package['name']: [dependency for dependency in package.get('dependencies', [])
                          if dependency.split()[0] in backend_names]
        for package in selected_packages if package['name'] in backend_names
    }
    (output / 'backend-graph.json').write_text(json.dumps({
        'qualified': qualified_backend, 'selected': selected_backend,
        'dependencyEdges': backend_edges,
        'matches': selected_backend == qualified_backend,
    }, indent=2) + '\n')
    assert selected_backend == qualified_backend, 'consumer backend needs a new no-replay audit'
    assert {edge.split()[0] for edge in backend_edges['reqwest']} == {'hyper', 'hyper-util'}
    assert {edge.split()[0] for edge in backend_edges['hyper-util']} == {'hyper'}
    for label, features in [('default', None),
                            ('arbitrary-precision', 'arbitrary_precision'),
                            ('backend-unified', 'backend_unified'),
                            ('combined', 'arbitrary_precision,backend_unified')]:
        command = ['cargo', 'run', '--locked', '--manifest-path', consumer / 'Cargo.toml',
                   '--bin', 'independent-consumer']
        if features:
            command += ['--features', features]
        run('independent-consumer-' + label, command, consumer)
        command = ['cargo', 'test', '--locked', '--manifest-path', consumer / 'Cargo.toml', '--tests']
        if features:
            command += ['--features', features]
        run('archived-native-tests-' + label, command, consumer)
print(json.dumps({'passed': True, 'commands': len(results)}))
