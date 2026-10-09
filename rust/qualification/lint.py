"""Keep the qualified candidate's explicit lint debt visible; reject new warnings."""
import json
from pathlib import Path
import subprocess
import sys

root = Path(__file__).resolve().parents[1]
# The prior product qualification explicitly retained five production warnings.
# Workspace-wide checking also finds one existing qualification-runner warning.
# These exact locations are temporary debt, not blanket lint exemptions. Fixing
# them is allowed; introducing another warning makes this check fail.
baseline = {
    ('clippy::collapsible_if', 'client/src/document.rs', 282),
    ('clippy::op_ref', 'client/src/document.rs', 310),
    ('clippy::collapsible_if', 'client/src/prepare.rs', 868),
    ('clippy::type_complexity', 'client/src/prepare.rs', 969),
    ('clippy::too_many_arguments', 'client/src/prepare.rs', 1056),
    ('clippy::manual_is_multiple_of', 'qualification/runner/src/lib.rs', 497),
}
result = subprocess.run(
    ['cargo', 'clippy', '--locked', '--workspace', '--all-targets', '--message-format=json'],
    cwd=root, capture_output=True, text=True,
)
sys.stderr.write(result.stderr)
unexpected = []
observed = set()
for line in result.stdout.splitlines():
    event = json.loads(line)
    if event.get('reason') != 'compiler-message':
        continue
    message = event['message']
    if message['level'] not in ('warning', 'error'):
        continue
    sys.stderr.write(message.get('rendered') or message['message'] + '\n')
    spans = [s for s in message['spans'] if s['is_primary']]
    key = ((message.get('code') or {}).get('code'),
           spans[0]['file_name'].replace('\\', '/') if len(spans) == 1 else None,
           spans[0]['line_start'] if len(spans) == 1 else None)
    observed.add(key)
    if message['level'] != 'warning' or key not in baseline:
        unexpected.append(key)
print(json.dumps({'retainedBaselineWarnings': len(observed & baseline),
                  'unexpectedWarningsOrErrors': unexpected}))
raise SystemExit(result.returncode or bool(unexpected))
