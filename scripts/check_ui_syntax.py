#!/usr/bin/env python3
"""Fail fast when the console's inline JavaScript does not parse.

Why this exists: the whole UI is one `go:embed`ed index.html whose behaviour
lives in a single <script> block. One unbalanced parenthesis there does not
produce a partial page - it produces a page that renders its markup, then sits at
「连接中…」 with every button dead, because nothing after the syntax error is ever
defined. That happened while adding the failure-explanation block: the edit looked
right, and the only symptom was a spinner.

`go build` cannot see it, `go vet` cannot see it, and every browser check script
needs a working page before it can run - so they all fail with something
unrelated, or worse, pass because they only assert on static markup.

Usage:
    python scripts/check_ui_syntax.py

Exit codes: 0 = every script parses (or node is unavailable), 1 = it does not.
"""

from __future__ import annotations

import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PAGE = ROOT / "src" / "go" / "daemon" / "ui" / "index.html"

# <script> ... </script>, ignoring any tag with attributes (none today, but a
# future type="module" would need different handling and should be noticed).
SCRIPT_RE = re.compile(r"<script(?:\s[^>]*)?>(.*?)</script>", re.DOTALL)


def main() -> int:
    if not PAGE.exists():
        print(f"FAIL: {PAGE} not found", file=sys.stderr)
        return 1

    node = shutil.which("node")
    if not node:
        print("skip: node not on PATH, cannot syntax-check the console script")
        return 0

    html = PAGE.read_text(encoding="utf-8")
    matches = list(SCRIPT_RE.finditer(html))
    if not matches:
        print("FAIL: index.html contains no <script> block", file=sys.stderr)
        return 1

    failures = 0
    total_lines = 0
    with tempfile.TemporaryDirectory() as tmp:
        for i, m in enumerate(matches):
            body = m.group(1)
            total_lines += body.count("\n")
            # Keep the block's original line numbers so a reported line can be
            # found in index.html directly: pad with the lines that precede it.
            pad = html[: m.start(1)].count("\n")
            path = Path(tmp) / f"block{i}.js"
            path.write_text("\n" * pad + body, encoding="utf-8")
            proc = subprocess.run(
                [node, "--check", str(path)],
                capture_output=True,
                text=True,
                encoding="utf-8",
                errors="replace",
            )
            if proc.returncode != 0:
                failures += 1
                detail = (proc.stderr or proc.stdout).strip()
                # node reports the temp path; name the real file instead.
                detail = detail.replace(str(path), "index.html")
                print(f"FAIL: script block {i} does not parse\n{detail}", file=sys.stderr)

    if failures:
        return 1

    print(f"OK: {len(matches)} script block(s), {total_lines} line(s), all parse")
    return 0


if __name__ == "__main__":
    sys.exit(main())
