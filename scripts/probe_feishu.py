#!/usr/bin/env python
"""Feishu Drive capability probe (M0).

Answers the six open questions E1-E6 documented in PLAN.md section 3,
against a real tenant, and writes the results to docs/feishu-probe.md.

Design constraints
------------------
* Zero third-party dependencies (stdlib urllib only).
* Pure-ASCII console output (the Windows console decodes child output as
  cp936, so non-ASCII would be mojibake).
* Never prints app_secret or access tokens.
* All write operations are confined to ONE sub-folder created underneath
  WEFTSYNC_TEST_FOLDER_TOKEN.  Nothing outside it is touched.
* Cleans up everything it created, even on failure.

Usage
-----
    .venv/Scripts/python.exe scripts/probe_feishu.py
    .venv/Scripts/python.exe scripts/probe_feishu.py --dry-run
    .venv/Scripts/python.exe scripts/probe_feishu.py --out docs/feishu-probe.md

Exit codes
----------
    0  probe completed (regardless of individual answers)
    2  configuration / IO problem, nothing was probed
    3  authentication failed
    4  safety guard tripped (refused to write)
"""

from __future__ import annotations

import argparse
import json
import mimetypes
import os
import re
import secrets as pysecrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from dataclasses import dataclass, field
from pathlib import Path

DEFAULT_BASE = "https://open.feishu.cn"
HTTP_TIMEOUT = 60

# Scopes that let a tenant token read/write Drive.  Listed in the order we
# would prefer them: write first, because weftsync needs to upload.
DRIVE_SCOPES = ("drive:drive", "drive:drive:readonly", "space:document:retrieve")


def extract_folder_token(value: str) -> str:
    """Accept either a bare token or a Feishu folder URL and return the token.

    Handles these shapes:
        KARRfvoYtlX9T3dOoaTcmdELnyv
        https://xxx.feishu.cn/drive/folder/KARRfvoYtlX9T3dOoaTcmdELnyv
        https://xxx.feishu.cn/drive/folder/KARRfvoYtlX9T3dOoaTcmdELnyv?from=x
        https://xxx.feishu.cn/...pylink          (shortcut, token in path)
    """
    value = value.strip().strip("'\"")
    if not value:
        return ""
    if not value.lower().startswith(("http://", "https://")):
        return value

    parsed = urllib.parse.urlparse(value)
    parts = [p for p in parsed.path.split("/") if p]

    # .../drive/folder/<token>
    if "folder" in parts:
        idx = parts.index("folder")
        if idx + 1 < len(parts):
            return parts[idx + 1]

    # .../drive/<token>  or  /docx/<token> etc.
    if "drive" in parts:
        idx = parts.index("drive")
        if idx + 1 < len(parts) and parts[idx + 1] not in ("folder", "file"):
            return parts[idx + 1]

    # fall back to the last path segment
    return parts[-1] if parts else ""


# ---------------------------------------------------------------------------
# .env loading (no python-dotenv dependency)
# ---------------------------------------------------------------------------
def load_env(path: Path) -> dict[str, str]:
    """Parse a simple KEY=VALUE .env file. Existing os.environ wins."""
    values: dict[str, str] = {}
    if path.is_file():
        for raw in path.read_text(encoding="utf-8", errors="replace").splitlines():
            line = raw.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, _, val = line.partition("=")
            key = key.strip()
            val = val.strip().strip("'\"")
            if key:
                values[key] = val
    for key, val in os.environ.items():
        if key.startswith("WEFTSYNC_"):
            values[key] = val
    return values


# ---------------------------------------------------------------------------
# Minimal Feishu API client
# ---------------------------------------------------------------------------
class FeishuError(RuntimeError):
    def __init__(self, where: str, code: int, msg: str, raw: object = None):
        super().__init__(f"{where}: code={code} msg={msg}")
        self.where = where
        self.code = code
        self.msg = msg
        self.raw = raw


@dataclass
class Client:
    base: str
    app_id: str
    app_secret: str
    _token: str | None = None
    _token_expiry: float = 0.0
    probe_log: list[dict] = field(default_factory=list)

    # -- token -------------------------------------------------------------
    def token(self, force: bool = False) -> str:
        now = time.time()
        if not force and self._token and now < self._token_expiry - 1800:
            return self._token

        url = f"{self.base}/open-apis/auth/v3/tenant_access_token/internal"
        body = json.dumps(
            {"app_id": self.app_id, "app_secret": self.app_secret}
        ).encode("utf-8")
        req = urllib.request.Request(
            url,
            data=body,
            headers={"Content-Type": "application/json; charset=utf-8"},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=HTTP_TIMEOUT) as resp:
            payload = json.loads(resp.read().decode("utf-8"))

        code = payload.get("code", -1)
        if code != 0:
            raise FeishuError("tenant_access_token", code, payload.get("msg", ""), payload)

        self._token = payload["tenant_access_token"]
        self._token_expiry = now + int(payload.get("expire", 7200))
        return self._token

    # -- generic request ---------------------------------------------------
    def _headers(self, content_type: str | None = None) -> dict[str, str]:
        h = {"Authorization": f"Bearer {self.token()}"}
        if content_type:
            h["Content-Type"] = content_type
        return h

    def request(
        self,
        where: str,
        method: str,
        path: str,
        body: bytes | None = None,
        content_type: str | None = "application/json; charset=utf-8",
        query: dict[str, str] | None = None,
        allow_fail: bool = False,
        max_redirects: int = 0,
    ) -> dict:
        url = f"{self.base}{path}"
        if query:
            url = f"{url}?{urllib.parse.urlencode(query)}"

        req = urllib.request.Request(
            url, data=body, headers=self._headers(content_type), method=method
        )

        opened: list[str] = []
        handler = _NoRedirect(opened) if max_redirects == 0 else None
        opener = urllib.request.build_opener(handler) if handler else urllib.request.build_opener()

        try:
            with opener.open(req, timeout=HTTP_TIMEOUT) as resp:
                raw_body = resp.read()
                try:
                    payload = json.loads(raw_body.decode("utf-8"))
                except (UnicodeDecodeError, json.JSONDecodeError):
                    payload = {"_raw_len": len(raw_body), "_non_json": True}
                payload["_http_status"] = resp.status
                payload["_redirects"] = opened
                return payload
        except urllib.error.HTTPError as exc:
            err_body = exc.read()
            try:
                payload = json.loads(err_body.decode("utf-8"))
            except (UnicodeDecodeError, json.JSONDecodeError):
                payload = {"_raw": err_body[:500].decode("utf-8", "replace")}
            payload["_http_status"] = exc.code
            payload["_redirects"] = opened
            if allow_fail:
                return payload
            raise FeishuError(where, exc.code, payload.get("msg", "HTTPError"), payload) from None

    # -- api wrappers ------------------------------------------------------
    def list_folder(self, folder_token: str) -> list[dict]:
        """List one folder (non-recursive), following pagination."""
        out: list[dict] = []
        page_token = ""
        for _ in range(50):
            q = {"folder_token": folder_token, "page_size": "200"}
            if page_token:
                q["page_token"] = page_token
            payload = self.request("files.list", "GET", "/open-apis/drive/v1/files", query=q)
            _check(payload, "files.list")
            data = payload.get("data") or {}
            out.extend(data.get("files") or [])
            if not data.get("has_more"):
                break
            page_token = data.get("next_page_token") or ""
            if not page_token:
                break
        return out

    def create_folder(self, parent: str, name: str) -> dict:
        body = json.dumps({"name": name, "folder_token": parent}).encode("utf-8")
        payload = self.request(
            "files.create_folder", "POST", "/open-apis/drive/v1/files/create_folder", body=body
        )
        _check(payload, "files.create_folder")
        return payload.get("data") or {}

    def upload_all(self, parent: str, filename: str, content: bytes) -> dict:
        boundary = "----weftsync" + uuid.uuid4().hex
        parts: list[bytes] = []

        def add_field(name: str, value: str) -> None:
            parts.append(
                f"--{boundary}\r\n"
                f'Content-Disposition: form-data; name="{name}"\r\n\r\n'
                f"{value}\r\n".encode("utf-8")
            )

        add_field("file_name", filename)
        add_field("parent_type", "explorer")
        add_field("parent_node", parent)
        add_field("size", str(len(content)))

        ctype = mimetypes.guess_type(filename)[0] or "application/octet-stream"
        parts.append(
            f"--{boundary}\r\n"
            f'Content-Disposition: form-data; name="file"; filename="{filename}"\r\n'
            f"Content-Type: {ctype}\r\n\r\n".encode("utf-8")
        )
        parts.append(content)
        parts.append(f"\r\n--{boundary}--\r\n".encode("utf-8"))

        payload = self.request(
            "files.upload_all",
            "POST",
            "/open-apis/drive/v1/files/upload_all",
            body=b"".join(parts),
            content_type=f"multipart/form-data; boundary={boundary}",
            allow_fail=True,
        )
        return payload

    def download_meta(self, file_token: str) -> dict:
        """Issue a download request but do not follow the redirect.

        We only want the Content-Length / headers, not the bytes.
        """
        payload = self.request(
            "files.download",
            "GET",
            f"/open-apis/drive/v1/files/{file_token}/download",
            allow_fail=True,
            max_redirects=0,
        )
        return payload

    def folder_meta(self, token: str) -> dict:
        """Return {title, owner_id, url, ...} for a folder token, or {}."""
        body = json.dumps(
            {"request_docs": [{"doc_token": token, "doc_type": "folder"}], "with_url": True}
        ).encode("utf-8")
        payload = self.request(
            "metas.batch_query", "POST", "/open-apis/drive/v1/metas/batch_query",
            body=body, allow_fail=True,
        )
        if payload.get("code") != 0:
            return {"_error": payload.get("msg", ""), "_code": payload.get("code")}
        metas = (payload.get("data") or {}).get("metas") or []
        return metas[0] if metas else {}

    def delete(self, token: str, kind: str = "file") -> dict:
        payload = self.request(
            "files.delete",
            "DELETE",
            f"/open-apis/drive/v1/files/{token}",
            query={"type": kind},
            allow_fail=True,
        )
        return payload


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """Record redirect targets instead of following them."""

    def __init__(self, sink: list[str]):
        self.sink = sink

    def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: D102
        self.sink.append(newurl)
        return None


def _check(payload: dict, where: str) -> None:
    code = payload.get("code", -1)
    if code != 0:
        raise FeishuError(where, code, payload.get("msg", ""), payload)


# ---------------------------------------------------------------------------
# Probe steps
# ---------------------------------------------------------------------------
@dataclass
class Result:
    key: str
    question: str
    answer: str = "NOT RUN"
    evidence: str = ""
    raw: dict = field(default_factory=dict)


class ProbeAbort(RuntimeError):
    """Raised when the probe cannot continue; carries partial results."""

    def __init__(self, why: str, results: dict["Result"]):
        super().__init__(why)
        self.why = why
        self.results = results


def run_probe(client: Client, root_token: str, dry_run: bool) -> dict[str, Result]:
    results: dict[str, Result] = {}

    def rec(key: str, question: str) -> Result:
        r = Result(key=key, question=question)
        results[key] = r
        return r

    r_e3 = rec("E3", "Does the file listing expose size / hash?")
    r_e4 = rec("E4", "Does the app really have WRITE permission?")
    r_e2 = rec("E2", "Can two files with the same name coexist in one folder?")
    r_e1 = rec("E1", "Can an existing file be updated in place (token unchanged)?")
    r_e5 = rec("E5", "When does modified_time change?")

    # ---- baseline: can we read at all -----------------------------------
    meta = client.folder_meta(root_token)
    target_title = meta.get("title") or "<unknown>"
    print(f"target     : folder '{target_title}'")
    if meta.get("owner_id"):
        print(f"owner      : {meta['owner_id']}")
    print()

    all_files = client.list_folder(root_token)
    fields = sorted({k for f in all_files for k in f})
    r_e3.raw["folder_meta"] = meta
    r_e3.raw["existing_entry_count"] = len(all_files)
    r_e3.raw["list_fields"] = fields
    r_e3.raw["sample_entry"] = all_files[0] if all_files else None

    print(f"read       : OK ({len(all_files)} existing entries in target folder)")
    print()

    if dry_run:
        r_e3.answer = "DRY RUN - would inspect fields: " + ", ".join(fields)
        for r in results.values():
            if r.answer == "NOT RUN":
                r.answer = "DRY RUN - skipped"
        return results

    # ---- create an isolated work folder ---------------------------------
    work_name = f"weftsync-probe-{time.strftime('%Y%m%d-%H%M%S')}-{pysecrets.token_hex(3)}"
    try:
        created = client.create_folder(root_token, work_name)
    except FeishuError as exc:
        r_e4.answer = "NO - create_folder failed"
        r_e4.evidence = f"code={exc.code} msg={exc.msg}"
        r_e4.raw = exc.raw or {}
        raise ProbeAbort("cannot create a work folder", results) from None

    work_token = created.get("token") or ""
    if not work_token:
        r_e4.answer = "UNKNOWN - create_folder returned no token"
        r_e4.raw = created
        raise ProbeAbort("cannot create a work folder", results)

    r_e4.answer = "YES - create_folder succeeded"
    r_e4.evidence = f"created folder '{work_name}'"
    r_e4.raw = {"create_folder": created}

    cleanup: list[tuple[str, str]] = [("folder", work_token)]

    try:
        # ---------------- E4 cont: upload small file ---------------------
        small_name = "size-probe.txt"
        small_body = b"weftsync probe payload 0123456789\n" * 40  # ~1.2 KB
        up1 = client.upload_all(work_token, small_name, small_body)
        if up1.get("code") != 0:
            r_e4.answer = "PARTIAL - folder ok, upload failed"
            r_e4.evidence = f"upload_all code={up1.get('code')} msg={up1.get('msg')}"
            r_e4.raw["upload_all"] = up1
            raise ProbeAbort("upload failed", results)
        r_e4.evidence += "; upload_all succeeded"
        r_e4.raw["upload_all"] = up1
        tok1 = (up1.get("data") or {}).get("file_token") or ""
        if tok1:
            cleanup.append(("file", tok1))

        # ---------------- E3: size in listing ---------------------------
        time.sleep(1.2)  # let the backend settle
        listing = client.list_folder(work_token)
        entry = next((f for f in listing if f.get("name") == small_name), None)
        r_e3.raw["listing_after_upload"] = listing
        if entry is None:
            r_e3.answer = "INCONCLUSIVE - uploaded file not visible in listing"
        else:
            has_size = "size" in entry and entry.get("size") not in (None, "")
            r_e3.answer = (
                f"{'YES' if has_size else 'NO'} - listing size = {entry.get('size')!r}"
            )
            r_e3.evidence = f"local bytes = {len(small_body)}; keys = {sorted(entry)}"

            # try to recover size from the download redirect
            if tok1:
                dl = client.download_meta(tok1)
                r_e3.raw["download"] = dl
                redirects = dl.get("_redirects") or []
                r_e3.evidence += f"; download http={dl.get('_http_status')} redirects={len(redirects)}"

        # ---------------- E2: duplicate names ---------------------------
        up2 = client.upload_all(work_token, small_name, b"second payload same name\n")
        r_e2.raw["second_upload"] = up2
        time.sleep(1.2)
        listing2 = client.list_folder(work_token)
        r_e2.raw["listing_after_second_upload"] = listing2
        same_name = [f for f in listing2 if f.get("name") == small_name]
        for f in same_name:
            if f.get("token") and f["token"] != tok1:
                cleanup.append(("file", f["token"]))

        if up2.get("code") == 0:
            r_e2.answer = (
                f"YES - both uploads accepted ({len(same_name)} entries named '{small_name}')"
                if len(same_name) > 1
                else "NO - second upload accepted but listing shows a single entry"
            )
            tok2 = (up2.get("data") or {}).get("file_token") or ""
            r_e2.evidence = f"token1={tok1[:8]}... token2={tok2[:8]}..."
            if tok2:
                cleanup.append(("file", tok2))
        else:
            r_e2.answer = f"NO - second upload rejected (code={up2.get('code')})"
            r_e2.evidence = str(up2.get("msg"))

        # ---------------- E1: in-place update ---------------------------
        # Feishu has no documented "update content" endpoint; the question is
        # what happens on a second upload with the same name.
        if up2.get("code") == 0 and tok1:
            tok2 = (up2.get("data") or {}).get("file_token") or ""
            if tok2 == tok1:
                r_e1.answer = "YES - token unchanged after re-upload"
            elif tok2:
                r_e1.answer = "NO - re-upload created a NEW token (delete+upload only)"
            else:
                r_e1.answer = "INCONCLUSIVE - no token returned"
            r_e1.evidence = f"token1={tok1[:12]} token2={tok2[:12]}"
        else:
            r_e1.answer = "NO - second upload with same name was rejected"

        # ---------------- E5: modified_time semantics -------------------
        first = next((f for f in listing if f.get("name") == small_name), None)
        second = next((f for f in listing2 if f.get("name") == small_name), None)
        r_e5.raw["mt_before"] = first
        r_e5.raw["mt_after"] = second
        mt1 = (first or {}).get("modified_time")
        mt2 = (second or {}).get("modified_time")
        if mt1 and mt2:
            if mt1 == mt2:
                r_e5.answer = "modified_time did NOT change on re-upload"
            else:
                r_e5.answer = "modified_time changed on re-upload"
            r_e5.evidence = f"before={mt1} after={mt2}"
            r_e5.raw["_note"] = "move/rename semantics still need a separate probe"
        else:
            r_e5.answer = "INCONCLUSIVE - modified_time missing"

    finally:
        # ---------------- cleanup --------------------------------------
        for kind, token in reversed(cleanup):
            if not token:
                continue
            resp = client.delete(token, kind)
            print(f"cleanup {kind} {token[:10]}... -> code={resp.get('code')}")

    return results


# ---------------------------------------------------------------------------
# Reporting
# ---------------------------------------------------------------------------
def _dump(results: dict[str, Result], why: str) -> int:
    print(f"\nABORT: {why}", file=sys.stderr)
    for r in results.values():
        print(f"  {r.key}: {r.answer}", file=sys.stderr)
        if r.evidence:
            print(f"      evidence: {r.evidence}", file=sys.stderr)
        if r.raw:
            print(f"      raw: {json.dumps(_redact(r.raw), ensure_ascii=False)[:2000]}", file=sys.stderr)
    return 4


def _redact(obj: object) -> object:
    """Strip anything that looks like a credential before persisting."""
    secret_keys = {
        "app_secret",
        "tenant_access_token",
        "authorization",
        "access_token",
        "user_access_token",
        "refresh_token",
    }

    # Feishu error payloads embed identifiers inside message strings, e.g.
    #   https://open.feishu.cn/app/cli_abc123.../auth?q=...
    app_id_re = re.compile(r"\bcli_[A-Za-z0-9]{8,}")
    # Signed download URLs carry credentials in the query string.
    signed_re = re.compile(r"\?(?:[^\s\"']+)")

    def scrub(text: str) -> str:
        text = app_id_re.sub("cli_<redacted>", text)
        return text

    def walk(node: object) -> object:
        if isinstance(node, dict):
            out = {}
            for k, v in node.items():
                if k.lower() in secret_keys:
                    out[k] = "<redacted>"
                elif k.lower() == "url" and isinstance(v, str):
                    out[k] = signed_re.sub("", v)
                else:
                    out[k] = walk(v)
            return out
        if isinstance(node, list):
            return [walk(x) for x in node]
        if isinstance(node, str):
            if node.startswith(("t-", "u-")) and len(node) > 20:
                return "<redacted>"
            return scrub(node)
        return node

    return walk(obj)


def write_report(path: Path, results: dict[str, Result], meta: dict) -> None:
    lines: list[str] = []
    lines.append("# Feishu Drive probe results (M0)\n")
    lines.append(f"- Run at: {time.strftime('%Y-%m-%d %H:%M:%S')}")
    lines.append(f"- API base: {meta.get('base')}")
    lines.append(f"- App id: {meta.get('app_id_masked')}")
    lines.append(f"- Test folder token: {meta.get('root_token_masked')}")
    lines.append(f"- Python: {sys.version.split()[0]}")
    lines.append("")
    lines.append("> This file is generated by `scripts/probe_feishu.py`.")
    lines.append("> Credentials and tokens are redacted before writing.\n")

    lines.append("## Answers\n")
    lines.append("| # | Question | Answer |")
    lines.append("|---|---|---|")
    for key in sorted(results):
        r = results[key]
        lines.append(f"| {r.key} | {r.question} | {r.answer} |")
    lines.append("")

    lines.append("## Evidence\n")
    for key in sorted(results):
        r = results[key]
        lines.append(f"### {r.key}\n")
        if r.evidence:
            safe_evidence = _redact({"e": r.evidence})["e"]
            lines.append("```")
            lines.append(safe_evidence)
            lines.append("```\n")
        if r.raw:
            lines.append("```json")
            lines.append(json.dumps(_redact(r.raw), ensure_ascii=False, indent=2)[:20000])
            lines.append("```\n")

    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(lines), encoding="utf-8", newline="\n")


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------
def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Probe Feishu Drive capabilities.")
    parser.add_argument("--dry-run", action="store_true", help="read-only checks only")
    parser.add_argument("--out", default="docs/feishu-probe.md", help="report path")
    parser.add_argument("--env", default=".env", help="path to .env")
    args = parser.parse_args(argv)

    env = load_env(Path(args.env))
    app_id = env.get("WEFTSYNC_FEISHU_APP_ID", "").strip()
    app_secret = env.get("WEFTSYNC_FEISHU_APP_SECRET", "").strip()
    raw_root = env.get("WEFTSYNC_TEST_FOLDER_TOKEN", "").strip()
    root_token = extract_folder_token(raw_root)
    base = env.get("WEFTSYNC_FEISHU_BASE_URL", "").strip() or DEFAULT_BASE

    missing = [
        name
        for name, val in (
            ("WEFTSYNC_FEISHU_APP_ID", app_id),
            ("WEFTSYNC_FEISHU_APP_SECRET", app_secret),
            ("WEFTSYNC_TEST_FOLDER_TOKEN", root_token),
        )
        if not val
    ]
    if missing:
        print("error: missing values in .env:", file=sys.stderr)
        for name in missing:
            print(f"  - {name}", file=sys.stderr)
        print(f"\nEdit {Path(args.env).resolve()} and fill them in, then re-run.", file=sys.stderr)
        return 2

    print(f"api base   : {base}")
    print(f"app id     : {app_id[:8]}...{app_id[-4:]}  (len {len(app_id)})")
    print(f"app secret : <set, {len(app_secret)} chars>")
    if raw_root.lower().startswith("http"):
        print(f"test root  : parsed from URL -> {root_token[:10]}...  (len {len(root_token)})")
    else:
        print(f"test root  : {root_token[:10]}...  (len {len(root_token)})")
    print(f"mode       : {'DRY RUN' if args.dry_run else 'FULL (writes inside a temp sub-folder)'}")
    print()

    client = Client(base=base, app_id=app_id, app_secret=app_secret)

    try:
        client.token()
        print("auth       : OK (tenant_access_token obtained)")
    except FeishuError as exc:
        print(f"auth       : FAILED code={exc.code} msg={exc.msg}", file=sys.stderr)
        print("             check app_id / app_secret, and that the app exists.", file=sys.stderr)
        return 3

    results: dict[str, Result] = {}
    abort_why = ""
    try:
        results = run_probe(client, root_token, args.dry_run)
    except ProbeAbort as exc:
        results = exc.results
        abort_why = exc.why
    except FeishuError as exc:
        print(f"probe failed: {exc}", file=sys.stderr)
        if exc.raw:
            print(json.dumps(_redact(exc.raw), ensure_ascii=False, indent=2)[:4000], file=sys.stderr)
        return _explain(exc, app_id)

    write_report(
        Path(args.out),
        results,
        {
            "base": base,
            "app_id_masked": "<redacted - local only>",
            "root_token_masked": "<redacted - local only>",
        },
    )

    print()
    print("=== results ===")
    for key in sorted(results):
        r = results[key]
        print(f"{key}: {r.answer}")
        if r.evidence:
            print(f"    evidence: {r.evidence}")
        if r.raw:
            print(f"    raw: {json.dumps(_redact(r.raw), ensure_ascii=False)[:1500]}")
    print()
    print(f"report written to: {Path(args.out).resolve()}")

    if abort_why:
        print(f"\nABORTED: {abort_why}", file=sys.stderr)
        return 4
    return 0


def _explain(exc: FeishuError, app_id: str) -> int:
    """Turn a Feishu error code into an actionable next step."""
    scope = ",".join(DRIVE_SCOPES)
    console = f"https://open.feishu.cn/app/{app_id}/auth?q={scope}&op_from=openapi&token_type=tenant"

    print(file=sys.stderr)
    if exc.code == 99991672 or "scope" in exc.msg.lower():
        print("=" * 68, file=sys.stderr)
        print("BLOCKED: the app has no Drive permission yet.", file=sys.stderr)
        print("", file=sys.stderr)
        print("Open this link, add the scope, then publish a new version:", file=sys.stderr)
        print(f"  {console}", file=sys.stderr)
        print("", file=sys.stderr)
        print("Choose 'drive:drive' (read+write) -- weftsync needs to upload.", file=sys.stderr)
        print("After approving, the app version must be released for it to take effect.", file=sys.stderr)
        print("=" * 68, file=sys.stderr)
        return 5

    if exc.code == 1061004:
        print("BLOCKED: forbidden on this folder.", file=sys.stderr)
        print("The app is a chat bot; the folder must be shared with the chat", file=sys.stderr)
        print("that contains the app, with EDIT permission.", file=sys.stderr)
        return 6

    if exc.code == 1061044:
        print("BLOCKED: parent folder token not found.", file=sys.stderr)
        print(f"Check WEFTSYNC_TEST_FOLDER_TOKEN -- got '{exc.where}'.", file=sys.stderr)
        return 7

    return 4


if __name__ == "__main__":
    raise SystemExit(main())
