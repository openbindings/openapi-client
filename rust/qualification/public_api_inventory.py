"""Summarize a default-feature rustdoc JSON inventory, excluding external types.

Generate the input with RUSTC_BOOTSTRAP=1 cargo rustdoc --locked -p PACKAGE
--lib -- -Z unstable-options --output-format json, then pass input and output.
Inherent methods exclude trait implementations; free functions are module items.
"""
import json
from pathlib import Path
import sys

doc = json.loads(Path(sys.argv[1]).read_text())
index = doc['index']


def item(key):
    return index[str(key)]


types = []
for value in index.values():
    if value['crate_id'] != 0 or value['visibility'] != 'public':
        continue
    kind = next(iter(value['inner']))
    if kind not in ('struct', 'enum', 'union', 'trait', 'type_alias'):
        continue
    declaration = value['inner'][kind]
    methods = []
    for impl_id in declaration.get('impls', []):
        implementation = item(impl_id)['inner']['impl']
        if implementation['trait'] is not None:
            continue
        for member_id in implementation['items']:
            member = item(member_id)
            if member['visibility'] == 'public' and 'function' in member['inner']:
                methods.append({'name': member['name'], 'signature': member['inner'],
                                'source': member['span']})
    fields = []
    if kind == 'struct':
        shape = declaration['kind']
        if isinstance(shape, dict) and 'plain' in shape:
            fields = [item(k) for k in shape['plain']['fields']]
    variants = [item(k) for k in declaration.get('variants', [])]
    types.append({'name': value['name'], 'kind': kind, 'source': value['span'],
                  'methods': sorted(methods, key=lambda m: m['name']),
                  'fields': [{'name': f['name'], 'type': f['inner']} for f in fields],
                  'variants': [{'name': v['name'], 'type': v['inner']} for v in variants]})
functions = []
for value in index.values():
    if value['crate_id'] == 0 and 'module' in value['inner']:
        for key in value['inner']['module']['items']:
            member = item(key)
            if member['visibility'] == 'public' and 'function' in member['inner']:
                functions.append({'name': member['name'], 'source': member['span']})
counts = {'types': len(types), 'inherent_methods': sum(len(t['methods']) for t in types),
          'free_functions': len(functions), 'public_struct_fields': sum(len(t['fields']) for t in types),
          'enum_variants': sum(len(t['variants']) for t in types)}
result = {'counts': counts, 'types': sorted(types, key=lambda t: t['name']),
          'free_functions': sorted(functions, key=lambda f: f['name'])}
Path(sys.argv[2]).write_text(json.dumps(result, indent=2) + '\n')
print(json.dumps(counts))
