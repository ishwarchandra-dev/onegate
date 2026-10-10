#!/usr/bin/env python3
"""check_cli_docs.py — CLI reference freshness gate (p9.cli-polish).

Technical-writer charter: "never let a flag exist that isn't in the CLI
reference". This script makes that mechanical, the same way
make api-check guards the OpenAPI artifacts:

  1. Regex-scan cmd/onegate/*.go for every flag registration
     (String/Int/Bool/Float64/Duration, plain and Var-binding forms)
     and collect the flag names.
  2. Require each name to appear as `-<name>` in docs/cli.md.
  3. Require the docs to mention every subcommand and the exit-code
     table rows (binary 0/1/2 and launcher 1/2/3/4).

Usage: check_cli_docs.py [--check]   (behavior identical; reserved for
CI symmetry with other generators)
"""

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CMD = ROOT / "cmd" / "onegate"
DOC = ROOT / "docs" / "cli.md"

# fs.StringVar(&o.host, "host", "", "...")  — name is arg 2
VAR_FORM = re.compile(
    r'\.(?:String|Int|Bool|Float64|Duration)Var\(\s*[^,]+,\s*"([A-Za-z0-9_-]+)"'
)
# fs.String("host", "", "...")              — name is arg 1
PLAIN_FORM = re.compile(
    r'\.(?:String|Int|Bool|Float64|Duration)\(\s*"([A-Za-z0-9_-]+)"'
)

SUBCOMMANDS = ["serve", "import", "import-keys", "config", "help"]

REQUIRED_DOC_STRINGS = [
    "-version",               # the one top-level flag
    "ONEGATE_ADMIN_TOKEN",    # env table present
    "128+N",                  # signal convention documented
]

# exit-code table rows that must exist (binary + launcher)
REQUIRED_EXIT_CODES = ["| 0 |", "| 1 |", "| 2 |", "| 3 |", "| 4 |"]


def flag_names_from_go(path: Path):
    """Return the set of flag names registered in a Go file.

    gofmt keeps the calls on one line with the name as a string literal
    right after the receiver binding (Var forms) or first (plain forms).
    """
    text = path.read_text()
    names = set(VAR_FORM.findall(text))
    # plain forms would also match inside Var matches; subtract by
    # scanning again with the Var prefix stripped out.
    stripped = re.sub(r'\.(?:String|Int|Bool|Float64|Duration)Var\([^)]*\)', '', text, flags=re.S)
    names |= set(PLAIN_FORM.findall(stripped))
    return names


def main() -> int:
    problems = []

    go_flags = set()
    for go_file in sorted(CMD.glob("*.go")):
        if go_file.name.endswith("_test.go"):
            continue
        go_flags |= flag_names_from_go(go_file)

    if not go_flags:
        print("check_cli_docs: no flags found in cmd/onegate — wrong directory?", file=sys.stderr)
        return 1

    doc = DOC.read_text()

    for name in sorted(go_flags):
        if f"-{name}" not in doc:
            problems.append(f"flag -{name} is registered in Go but missing from docs/cli.md")

    for sub in SUBCOMMANDS:
        if sub not in doc:
            problems.append(f"subcommand {sub!r} missing from docs/cli.md")

    for s in REQUIRED_DOC_STRINGS:
        if s not in doc:
            problems.append(f"required mention {s!r} missing from docs/cli.md")

    for row in REQUIRED_EXIT_CODES:
        if row not in doc:
            problems.append(f"exit-code row {row!r} missing from docs/cli.md")

    if problems:
        print("check_cli_docs: FAIL", file=sys.stderr)
        for p in problems:
            print(f"  - {p}", file=sys.stderr)
        return 1

    print(f"check_cli_docs: OK ({len(go_flags)} flags, {len(SUBCOMMANDS)} commands, "
          f"{len(REQUIRED_EXIT_CODES)} exit rows documented)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
