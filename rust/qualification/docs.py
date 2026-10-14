"""Check README excerpts against compiled examples and matrix evidence paths.

Run with --write to refresh include regions after editing an example. The default
is read-only and is run by verify.py. Paths in markers are repository-relative.
"""
import re
import sys
import textwrap
from pathlib import Path

root = Path(__file__).resolve().parents[2]
pattern = re.compile(
    r"<!-- include: (rust/[^ #]+)#([^ ]+) -->\n```rust,ignore\n(.*?)\n```\n"
    r"<!-- endinclude -->", re.DOTALL
)


def excerpt(match):
    path, region, _ = match.groups()
    example = root / path
    assert example.suffix == ".rs" and example.parent.name == "examples", path
    source = example.read_text()
    start = f"// docs-start: {region}\n"
    end = f"// docs-end: {region}"
    assert source.count(start) == source.count(end) == 1, (path, region)
    body = textwrap.dedent(source.split(start, 1)[1].split(end, 1)[0]).strip()
    return (f"<!-- include: {path}#{region} -->\n```rust,ignore\n{body}\n```\n"
            "<!-- endinclude -->")


count = 0
for name in ["rust/README.md", "rust/client/README.md"]:
    path = root / name
    content = path.read_text()
    refreshed, found = pattern.subn(excerpt, content)
    count += found
    assert found, f"No compiled-example includes in {name}"
    assert content.count("```rust") == found, f"Untracked Rust snippet in {name}"
    if "--write" in sys.argv:
        path.write_text(refreshed)
    else:
        assert content == refreshed, f"Stale excerpts in {name}; run python3 rust/qualification/docs.py --write"

matrix = (root / "rust/client/CAPABILITIES.md").read_text()
rows = [line for line in matrix.splitlines() if re.match(r"\| [A-Z][0-9]{2} \|", line)]
assert rows, "No capability/policy rows"
for row in rows:
    columns = row.split("|")[1:-1]
    assert len(columns) == 9, f"Wrong matrix column count: {row}"
    evidence = re.findall(r"\]\(([^)]+)\)", columns[7])
    assert evidence, f"No test or case linked: {columns[0]}"
    for target in evidence:
        path = (root / "rust/client" / target.split("#", 1)[0]).resolve()
        assert path.is_relative_to(root), target
        assert path.is_file(), f"Missing evidence: {target}"
        directories = set(path.relative_to(root).parts[:-1])
        assert (directories & {"tests", "qualification", "examples"}
                or path.name.endswith("_tests.rs")), f"Not a test or case: {target}"
print(f"Checked {count} compiled-example excerpts and {len(rows)} capability/policy rows")

# Keep subject-named example/test links usable in both repository docs and rustdoc.
references = [root / "README.md", root / "rust/README.md", root / "docs/policies.md",
              *[root / "rust/client" / name for name in (
                  "README.md", "EXACT.md", "LOADING.md", "GRAPH.md", "SCHEMAS.md",
                  "FORMS.md", "HOSTS.md", "SERIALIZATION.md", "MIGRATION.md", "CAPABILITIES.md")]]
for document in references:
    for target in re.findall(r"\]\(([^)]+)\)", document.read_text()):
        if ":" in target or target.startswith("#"):
            continue
        assert (document.parent / target.split("#", 1)[0]).exists(), (document, target)
print("Checked local documentation links")
