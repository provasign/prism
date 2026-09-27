#!/usr/bin/env python3
"""PostToolUse hook: record which (file, line-range) windows prism has
already delivered this session, so prism_read_guard.py can block redundant
native Read calls over the same ranges -- and forget them once the file may
have changed.

On mcp__prism__prism:
  op=read gives an exact (file, from, to) in its own args (top-level file or
  a ranges list) -- no parsing needed. op=lookup also delivers a verbatim
  body, parsed heuristically from the response text since its structured
  args don't carry a line range; a slightly-wrong end line under-tracks
  (safe: worst case is an allowed redundant read) rather than over-tracks
  (which could wrongly block a legitimate one). Each range records the
  file's mtime and size at delivery; the guard never blocks a Read of a file
  whose mtime/size no longer match.

On Edit/Write/MultiEdit/NotebookEdit: drop that file's ranges.
On Bash: drop ALL of this session's ranges when the command can rewrite
files (git checkout/stash/reset/apply/restore/..., patch, sed -i, mv/cp, a
formatter, codegen, a script that writes files, a redirect into a non-temp
path) -- conservative, since which files it touched isn't knowable from the
command line. A stale range once denied a Read of code the agent had just
edited (jackson pr5959, 2026-09-26).

State: one JSON file per Claude Code session (hook input session_id) under
<project>/.claude/prism-read-guard/, which carries its own `*` .gitignore so
it never shows up in git. Another session's file is never consulted, and
files untouched for a day are deleted.

Installed and removed by `prism init --read-guard` / `--no-read-guard`.
"""
import json
import os
import re
import sys
import time
from pathlib import Path

STATE_DIR = Path(".claude") / "prism-read-guard"
EXPIRE_SECONDS = 24 * 3600
EDIT_TOOLS = ("Edit", "Write", "MultiEdit", "NotebookEdit")

LOOKUP_BLOCK_RE = re.compile(
    r"file:\s*(?P<file>\S+)\s*\nline:\s*(?P<line>\d+)\s*\nbody:\s*(?P<body>.*?)"
    r"(?=\nname:\s|\Z)", re.S)

# Bash commands that can change file contents without naming them to us.
_B = r"(?:^|[\s;&|(`])"
REWRITE_RE = re.compile(r"""(?x)
    """ + _B + r"""git(?:\s+-C\s+\S+|\s+-c\s+\S+)*\s+
        (?:checkout|switch|stash|reset|apply|am|restore|revert|cherry-pick|merge|rebase|pull|clean|mv|rm)\b
  | """ + _B + r"""(?:patch|mv|cp|rsync|ln|rm|truncate|dd|unzip|tar|gofmt|goimports|
        prettier|black|ruff|isort|autopep8|yapf|rustfmt|clang-format|google-java-format|
        ktlint|swiftformat|dos2unix|protoc|buf)(?=\s|$)
  | """ + _B + r"""(?:sed|gsed|perl)\b[^;&|\n]*?\s(?:-[A-Za-z]*i|--in-place)
  | """ + _B + r"""(?:go\s+fmt|go\s+generate|go\s+mod\s+tidy|cargo\s+fmt|npx\s+prettier|
        (?:npx\s+)?eslint\s+.*--fix|mvn\s+.*(?:spotless:apply|fmt:format|formatter:format)|
        gradlew?\s+.*spotlessApply|dotnet\s+format|rubocop\s+.*-a|terraform\s+fmt)
  | \.write_text\s*\( | \.write_bytes\s*\( | \.write\s*\( | open\s*\([^)]*['"][wax]b?\+?['"]
  | writeFileSync | fs\.writeFile | \btee\b
""")
# `> path` / `>> path` into anything but /dev/* or a temp dir.
REDIRECT_RE = re.compile(r"(?<![0-9&>])>>?\s*(?!&)(['\"]?)(?P<target>[^\s;&|'\"]+)")
_TEMP_PREFIXES = ("/dev/", "/tmp/", "/private/tmp/", "/var/folders/", "$TMPDIR", "${TMPDIR")


def project_dir() -> Path:
    return Path(os.environ.get("CLAUDE_PROJECT_DIR") or ".")


def state_file(session_id) -> Path:
    sid = re.sub(r"[^A-Za-z0-9_.-]", "_", str(session_id or "nosession"))[:128]
    return project_dir() / STATE_DIR / f"session-{sid}.json"


def load(path: Path) -> list:
    try:
        data = json.loads(path.read_text())
        return data if isinstance(data, list) else []
    except (OSError, ValueError):
        return []


def save(path: Path, tracked: list) -> None:
    d = path.parent
    d.mkdir(parents=True, exist_ok=True)
    ign = d / ".gitignore"
    if not ign.exists():
        ign.write_text("# prism read-guard session state; never commit\n*\n")
    tmp = path.with_name(path.name + f".{os.getpid()}.tmp")
    tmp.write_text(json.dumps(tracked))
    os.replace(tmp, path)
    now = time.time()
    for old in d.glob("session-*.json"):
        try:
            if old != path and now - old.stat().st_mtime > EXPIRE_SECONDS:
                old.unlink()
        except OSError:
            pass


def file_stat(f: str):
    """(mtime_ns, size) of the delivered file, or None if it can't be found.
    prism reports paths relative to the project root, usually."""
    for cand in (Path(f), project_dir() / f, Path.cwd() / f):
        try:
            st = cand.stat()
            return [st.st_mtime_ns, st.st_size]
        except OSError:
            continue
    return None


def bash_rewrites_files(cmd: str) -> bool:
    cmd = cmd or ""
    if REWRITE_RE.search(cmd):
        return True
    for m in REDIRECT_RE.finditer(cmd):
        if not m.group("target").startswith(_TEMP_PREFIXES):
            return True
    return False


def norm_path(p: str) -> str:
    """Compare paths with '/' separators; Windows paths are case-insensitive."""
    p = p.replace("\\", "/")
    return p.lower() if os.name == "nt" else p


def same_file(path: str, tracked_file: str) -> bool:
    """path is absolute (Edit's file_path); tracked_file is what prism
    reported, usually repo-relative -- the guard's own endswith rule."""
    a = norm_path(path).rstrip("/")
    b = norm_path(tracked_file)
    b = b[2:] if b.startswith("./") else b
    return a == b or a.endswith("/" + b) or b.endswith("/" + a.lstrip("/"))


def invalidate(payload: dict, tracked: list) -> list:
    """The tracked ranges still valid after this Edit/Write/Bash call."""
    tool = payload.get("tool_name") or ""
    ti = payload.get("tool_input") or {}
    if tool in EDIT_TOOLS:
        path = ti.get("file_path") or ti.get("notebook_path") or ""
        if not path:
            return []
        return [t for t in tracked if not same_file(path, t.get("file", ""))]
    if tool == "Bash" and bash_rewrites_files(ti.get("command", "")):
        return []
    return tracked


def _response_text(tool_response) -> str:
    """Defensive extraction: tool_response may be a bare string, a list of
    {"type":"text","text":...} content blocks, or a dict wrapping either."""
    if tool_response is None:
        return ""
    if isinstance(tool_response, str):
        return tool_response
    if isinstance(tool_response, dict):
        if "content" in tool_response:
            return _response_text(tool_response["content"])
        return json.dumps(tool_response)
    if isinstance(tool_response, list):
        parts = []
        for item in tool_response:
            if isinstance(item, dict) and "text" in item:
                parts.append(item["text"])
            elif isinstance(item, str):
                parts.append(item)
        return "\n".join(parts)
    return str(tool_response)


def delivered_ranges(payload: dict) -> list:
    tool_input = payload.get("tool_input") or {}
    op = tool_input.get("op")
    args = tool_input.get("args") or {}
    ranges = []
    if op == "read":
        f = args.get("file")
        if f and args.get("to"):
            ranges.append((f, args.get("from") or 1, args["to"]))
        for r in args.get("ranges") or []:
            if isinstance(r, dict) and r.get("file"):
                frm = r.get("from") or 1
                ranges.append((r["file"], frm, r.get("to") or frm))
    elif op == "lookup":
        text = _response_text(payload.get("tool_response"))
        for m in LOOKUP_BLOCK_RE.finditer(text):
            body_lines = m.group("body").count("\n") + 1
            start = int(m.group("line"))
            ranges.append((m.group("file"), start, start + body_lines - 1))
    return ranges


def main():
    payload = json.load(sys.stdin)
    path = state_file(payload.get("session_id"))
    tool = payload.get("tool_name") or ""

    if tool in EDIT_TOOLS or tool == "Bash":
        if path.exists():
            tracked = load(path)
            kept = invalidate(payload, tracked)
            if len(kept) != len(tracked):
                save(path, kept)
        return

    ranges = delivered_ranges(payload)
    if not ranges:
        return  # search/query results aren't verbatim line-range delivery; nothing to track

    tracked = load(path)
    for f, frm, to in ranges:
        tracked.append({"file": f, "from": frm, "to": to, "stat": file_stat(f)})
    save(path, tracked)


if __name__ == "__main__":
    try:
        main()
    except Exception:  # a broken tracker must never break the agent's tool call
        pass
