#!/usr/bin/env python
"""Scan repository content for leaked credentials.

This repository is PUBLIC.  Any credential that reaches a commit is considered
burned, because it may already be mirrored / cached / indexed.

Usage:
    python scripts/check_secrets.py              # scan all git-tracked files
    python scripts/check_secrets.py --staged     # scan files staged for commit
    python scripts/check_secrets.py --path F ...  # scan explicit files (self-test)

Exit codes:
    0  clean
    1  findings present
    2  usage / IO error

Notes:
    * Output is pure ASCII on purpose.  The Windows console decodes child
      process output as cp936, so non-ASCII would be mojibake.
    * Matched values are masked; only a short prefix is printed.
"""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
from pathlib import Path

# ---------------------------------------------------------------------------
# Detectors
# ---------------------------------------------------------------------------
# (rule name, severity, compiled pattern)
#   CRITICAL : almost certainly a live credential
#   HIGH     : credential-shaped assignment
#   MEDIUM   : identifier that must be reviewed (not a secret by itself)
DETECTORS = [
    (
        "private-key-block",
        "CRITICAL",
        re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----"),
    ),
    (
        "github-token",
        "CRITICAL",
        re.compile(r"\bgh[pousr]_[A-Za-z0-9]{36,}"),
    ),
    (
        "aws-access-key-id",
        "CRITICAL",
        re.compile(r"\bAKIA[0-9A-Z]{16}\b"),
    ),
    (
        "slack-token",
        "CRITICAL",
        re.compile(r"\bxox[abposr]-[A-Za-z0-9-]{10,}"),
    ),
    # Feishu / Lark
    (
        "feishu-tenant-access-token",
        "CRITICAL",
        re.compile(r"\bt-[A-Za-z0-9]{20,}"),
    ),
    (
        "feishu-user-access-token",
        "CRITICAL",
        re.compile(r"\bu-[A-Za-z0-9]{20,}"),
    ),
    (
        "feishu-app-id",
        "MEDIUM",
        re.compile(r"\bcli_[a-zA-Z0-9]{10,}"),
    ),
    # Generic "assigned a long literal to something secret-ish"
    (
        "assigned-secret",
        "HIGH",
        re.compile(
            r"""(?ix)
            \b( app_secret | appsecret | secret_key | secretkey
              | access_token | refresh_token | api_key | apikey
              | password | passwd | client_secret
            )\b
            \s*[:=]\s*
            ["']([A-Za-z0-9/+_\-]{16,})["']
            """
        ),
    ),
    # 32-char lowercase alnum literal (classic Feishu app_secret shape)
    (
        "feishu-app-secret-shape",
        "HIGH",
        re.compile(r"""["']([a-z0-9]{32})["']"""),
    ),
]

# A match is ignored when the value itself looks like an obvious placeholder.
PLACEHOLDER_MARKERS = (
    "xxxx",
    "your",
    "example",
    "placeholder",
    "redacted",
    "changeme",
    "replace",
    "dummy",
    "fake",
    "...",
    "<",
    "${",
    "{{",
    "env",
)

MAX_BYTES = 2 * 1024 * 1024  # skip very large blobs
BINARY_SNIFF = 8192


def mask(value: str) -> str:
    """Show enough to locate the line, not enough to reuse."""
    value = value.strip()
    if len(value) <= 6:
        return value[:1] + "***"
    return value[:4] + "*" * 6 + f"({len(value)} chars)"


def is_placeholder(value: str) -> bool:
    low = value.lower()
    return any(marker in low for marker in PLACEHOLDER_MARKERS)


def looks_binary(raw: bytes) -> bool:
    return b"\x00" in raw[:BINARY_SNIFF]


def tracked_files() -> list[Path]:
    out = subprocess.run(
        ["git", "ls-files", "-z"],
        capture_output=True,
        check=True,
    ).stdout
    return [Path(p.decode("utf-8")) for p in out.split(b"\x00") if p]


def staged_files() -> list[Path]:
    out = subprocess.run(
        ["git", "diff", "--cached", "--name-only", "--diff-filter=ACM", "-z"],
        capture_output=True,
        check=True,
    ).stdout
    return [Path(p.decode("utf-8")) for p in out.split(b"\x00") if p]


def scan_file(path: Path) -> list[tuple[int, str, str, str]]:
    """Return (line number, severity, rule, masked value) for each finding."""
    try:
        raw = path.read_bytes()
    except OSError:
        return []
    if len(raw) > MAX_BYTES or looks_binary(raw):
        return []

    text = raw.decode("utf-8", errors="replace")
    findings: list[tuple[int, str, str, str]] = []

    for lineno, line in enumerate(text.splitlines(), start=1):
        for rule, severity, pattern in DETECTORS:
            for match in pattern.finditer(line):
                # Prefer the captured group when the pattern has one, so
                # "password = 'abc...'" reports the value and not the whole line.
                value = match.group(match.lastindex or 0)
                if is_placeholder(value) or is_placeholder(match.group(0)):
                    continue
                findings.append((lineno, severity, rule, mask(value)))
    return findings


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Scan for leaked credentials.")
    parser.add_argument(
        "--staged",
        action="store_true",
        help="scan only files staged for commit (pre-commit hook)",
    )
    parser.add_argument(
        "--path",
        nargs="+",
        metavar="FILE",
        help="scan explicit files instead of the git index (self-test)",
    )
    args = parser.parse_args(argv)

    try:
        if args.path:
            files = [Path(p) for p in args.path]
        elif args.staged:
            files = staged_files()
        else:
            files = tracked_files()
    except (subprocess.CalledProcessError, FileNotFoundError) as exc:
        print(f"error: cannot enumerate files: {exc}", file=sys.stderr)
        return 2

    total = 0
    for path in files:
        if not path.is_file():
            continue
        for lineno, severity, rule, shown in scan_file(path):
            total += 1
            print(f"{severity:8} {path}:{lineno}  [{rule}]  {shown}")

    scope = "staged" if args.staged else "tracked"
    if args.path:
        scope = "explicit"

    if total:
        print()
        print(f"FAIL: {total} potential secret(s) found in {scope} files.")
        print("Do NOT commit.  Move the value to an environment variable.")
        return 1

    print(f"OK: no credentials detected ({len(files)} {scope} file(s) scanned).")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
