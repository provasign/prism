#!/usr/bin/env python3
"""PreToolUse hook on Read: deny a native Read call whose requested line
range substantially overlaps a range prism already delivered in THIS session
(tracked by prism_read_tracker.py under .claude/prism-read-guard/).

Re-reading content prism already delivered was measured to be a real driver
of token blowup in agentic sessions; advisory steering alone ("don't
re-read") doesn't hold up over multi-turn sessions. This denies the
redundant Read and points the agent back at what it already has.

Never blocks when in doubt:
  - only ranges delivered in the current session (hook input session_id)
    are consulted;
  - a range counts only if the file's mtime and size still match what they
    were at delivery -- a file changed on disk since (by any means) is
    always readable;
  - OVERLAP_THRESHOLD: deny only when the requested window is MOSTLY covered
    by an already-delivered range, so legitimate pagination past the end of
    a capped prism read (e.g. lines 200-475 after a 1-200 delivery) is never
    blocked;
  - if the agent's latest edit of the file FAILED (old_string not found, not
    unique, file not read yet), the Read is allowed: the file is unchanged,
    so its range still matches, but the agent needs the exact text. A failed
    Edit is rejected at input validation, before any hook runs, so the
    tracker never hears of it; the session transcript is the only record
    (chi tree.go, 2026-10: wrong tab count, then a denied 16-line re-read).

Installed and removed by `prism init --read-guard` / `--no-read-guard`.
"""
import json
import os
import re
import sys
from pathlib import Path

STATE_DIR = Path(".claude") / "prism-read-guard"
OVERLAP_THRESHOLD = 0.7  # fraction of the REQUESTED window that must already be covered
EDIT_TOOLS = ("Edit", "Write", "MultiEdit", "NotebookEdit")
TRANSCRIPT_TAIL_BYTES = 512 * 1024  # recent turns only; bounded cost per Read


def state_file(session_id) -> Path:
    sid = re.sub(r"[^A-Za-z0-9_.-]", "_", str(session_id or "nosession"))[:128]
    root = Path(os.environ.get("CLAUDE_PROJECT_DIR") or ".")
    return root / STATE_DIR / f"session-{sid}.json"


def norm_path(p: str) -> str:
    """Compare paths with '/' separators; Windows paths are case-insensitive."""
    p = p.replace("\\", "/")
    return p.lower() if os.name == "nt" else p


def overlap_fraction(req_from: int, req_to: int, cov_from: int, cov_to: int) -> float:
    lo = max(req_from, cov_from)
    hi = min(req_to, cov_to)
    if hi < lo:
        return 0.0
    req_len = req_to - req_from + 1
    return (hi - lo + 1) / req_len if req_len > 0 else 0.0


def latest_edit_failed(transcript_path: str, want: str) -> bool:
    """True when the most recent edit-tool call on `want` in the transcript
    tail came back as an error. Unknown or unreadable -> False (the normal
    overlap rule applies)."""
    if not transcript_path:
        return False
    try:
        with open(transcript_path, "rb") as f:
            f.seek(0, os.SEEK_END)
            size = f.tell()
            f.seek(max(0, size - TRANSCRIPT_TAIL_BYTES))
            tail = f.read().decode("utf-8", "replace").splitlines()
    except OSError:
        return False
    if size > TRANSCRIPT_TAIL_BYTES:
        tail = tail[1:]  # first line is partial
    last_id, failed = None, {}
    for line in tail:
        try:
            rec = json.loads(line)
        except ValueError:
            continue
        content = (rec.get("message") or {}).get("content")
        if not isinstance(content, list):
            continue
        for c in content:
            if not isinstance(c, dict):
                continue
            if c.get("type") == "tool_use" and c.get("name") in EDIT_TOOLS:
                ti = c.get("input") or {}
                path = ti.get("file_path") or ti.get("notebook_path") or ""
                if path and norm_path(path) == want:
                    last_id = c.get("id")
            elif c.get("type") == "tool_result" and c.get("tool_use_id"):
                failed[c["tool_use_id"]] = bool(c.get("is_error"))
    return last_id is not None and failed.get(last_id, False)


def main():
    payload = json.load(sys.stdin)
    tool_input = payload.get("tool_input") or {}
    file_path = tool_input.get("file_path") or ""
    offset = tool_input.get("offset") or 1
    limit = tool_input.get("limit")
    req_to = (offset + limit - 1) if limit else offset + 1999  # unbounded read: treat as huge window

    path = state_file(payload.get("session_id"))
    if not file_path or not path.exists():
        return  # nothing tracked this session, allow
    try:
        tracked = json.loads(path.read_text())
        st = os.stat(file_path)
    except (OSError, ValueError):
        return
    now = [st.st_mtime_ns, st.st_size]
    want = norm_path(file_path)

    best_cov, best_frac = None, 0.0
    for t in tracked:
        f = norm_path(t.get("file") or "")
        rel = f[2:] if f.startswith("./") else f
        if not rel or not (want == rel or want.endswith("/" + rel.lstrip("/"))):
            continue
        if t.get("stat") != now:
            continue  # changed on disk since delivery (or unverifiable): not redundant
        frac = overlap_fraction(offset, req_to, t["from"], t["to"])
        if frac > best_frac:
            best_frac, best_cov = frac, t

    if best_cov and best_frac >= OVERLAP_THRESHOLD and not latest_edit_failed(payload.get("transcript_path"), want):
        f, frm, to = best_cov["file"], best_cov["from"], best_cov["to"]
        print(json.dumps({
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny",
                "permissionDecisionReason": (
                    f"prism already delivered lines {frm}-{to} of {f} earlier in this "
                    f"session, and the file has not changed since -- use that result "
                    f"instead of re-reading. If it is no longer in your context, fetch "
                    f"it again with prism op=read args={{\"file\":\"{f}\",\"from\":{frm},"
                    f"\"to\":{to}}}. If you need content past line {to}, Read only that "
                    f"range. (Edits and file-rewriting commands clear this automatically, "
                    f"and a re-read after a failed edit of this file is allowed.)"
                ),
            }
        }))


if __name__ == "__main__":
    try:
        main()
    except Exception:  # a broken guard must never block a Read
        pass
