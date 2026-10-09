"""Replay the Rust foundation and a consumer of its packaged public crate."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile

root = Path(__file__).resolve().parents[1]
output = Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else root / 'target/qualification'
output.mkdir(parents=True, exist_ok=True)
env = os.environ.copy()
env.setdefault('CARGO_TARGET_DIR', str(root / 'target'))
target = Path(env['CARGO_TARGET_DIR']).resolve()
results = []


def run(name, command, cwd=root):
    print('+ ' + ' '.join(map(str, command)), flush=True)
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


run('format', ['cargo', 'fmt', '--all', '--check'])
run('lint', [sys.executable, root / 'qualification/lint.py'])
run('tests', ['cargo', 'test', '--locked', '--workspace'])
for inventory in ['cases', 'additions']:
    run('corpus-' + inventory, ['cargo', 'run', '--locked', '-p', 'oac-qualification',
                              '--bin', 'oac-qualify', '--',
                              root / 'qualification/fixtures' / (inventory + '.json')])
run('dynamic-example', ['cargo', 'run', '--locked', '-p', 'dynamic-openapi-client',
                        '--example', 'dynamic'])
run('package', ['cargo', 'package', '--locked', '--no-verify', '-p', 'dynamic-openapi-client'])
archive = target / 'package/dynamic-openapi-client-0.0.0.crate'
with tempfile.TemporaryDirectory(prefix='openapi-consumer-', dir=output) as temporary:
    work = Path(temporary)
    with tarfile.open(archive) as packed:
        packed.extractall(work, filter='data')
    crate = work / 'dynamic-openapi-client-0.0.0'
    consumer = work / 'consumer'
    (consumer / 'src').mkdir(parents=True)
    (consumer / 'Cargo.toml').write_text(
        '[package]\nname = "openapi-external-consumer"\nversion = "0.0.0"\n'
        'edition = "2024"\n[workspace]\n[dependencies]\n'
        'dynamic-openapi-client = { path = ' + json.dumps(str(crate)) + ' }\n')
    shutil.copy2(crate / 'examples/dynamic.rs', consumer / 'src/main.rs')
    run('external-consumer', ['cargo', 'run', '--manifest-path', consumer / 'Cargo.toml'], consumer)
print(json.dumps({'passed': True, 'commands': len(results), 'output': str(output)}))
