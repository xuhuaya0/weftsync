#!/usr/bin/env python
"""Install local git hooks (not tracked; machine-local only).

Installs a pre-commit hook that runs scripts/check_secrets.py --staged.

Usage:
    .venv/Scripts/python.exe scripts/install_hooks.py
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

HOOK_TEMPLATE = """#!/bin/sh
# Installed by scripts/install_hooks.py -- do not edit, re-run instead.
exec "{python}" "{checker}" --staged
"""


def main() -> int:
    repo = Path(
        subprocess.run(
            ["git", "rev-parse", "--show-toplevel"],
            capture_output=True,
            check=True,
            text=True,
        ).stdout.strip()
    )
    hooks_dir = repo / ".git" / "hooks"
    hooks_dir.mkdir(parents=True, exist_ok=True)

    checker = (repo / "scripts" / "check_secrets.py").as_posix()
    if not Path(checker).is_file():
        print(f"error: {checker} not found", file=sys.stderr)
        return 2

    hook_path = hooks_dir / "pre-commit"
    hook_path.write_text(
        HOOK_TEMPLATE.format(python=Path(sys.executable).as_posix(), checker=checker),
        encoding="utf-8",
        newline="\n",
    )
    try:
        hook_path.chmod(0o755)
    except OSError:
        pass  # Windows: git honours the shebang regardless

    print(f"installed: {hook_path}")
    print(f"interpreter: {sys.executable}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
