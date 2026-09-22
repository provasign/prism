#!/usr/bin/env python3
"""PostToolUse hook on mcp__prism__prism: record which (file, line-range)
windows prism has already delivered this session, so prism_read_guard.py can
block redundant native Read calls over the same ranges.

op=read gives an exact (file, from, to) in its own args -- no parsing needed.
op=lookup also delivers a verbatim body, parsed heuristically from the
response text since its structured args don't carry a line range; a
slightly-wrong end line under-tracks (safe: worst case is an allowed
redundant read) rather than over-tracks (which could wrongly block a
legitimate one).

Appends to .prism-read-tracker.json in the cwd (one file per project).
Installed and removed by `prism init --read-guard` / `--no-read-guard`.
"""
import json
import re
import sys
from pathlib import Path

TRACKER = Path(".prism-read-tracker.json")
LOOKUP_BLOCK_RE = re.compile(
    r"file:\s*(?P<file>\S+)\s*\nline:\s*(?P<line>\d+)\s*\nbody:\s*(?P<body>.*?)"
    r"(?=\nname:\s|\Z)", re.S)


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


def main():
    payload = json.load(sys.stdin)
    tool_input = payload.get("tool_input") or {}
    op = tool_input.get("op")
    args = tool_input.get("args") or {}

    ranges = []
    if op == "read":
        f = args.get("file")
        if f:
            frm = args.get("from") or 1
            to = args.get("to")
            if to:
                ranges.append((f, frm, to))
            for r in args.get("ranges") or []:
                if r.get("file"):
                    ranges.append((r["file"], r.get("from", 1), r.get("to", r.get("from", 1))))
    elif op == "lookup":
        text = _response_text(payload.get("tool_response"))
        for m in LOOKUP_BLOCK_RE.finditer(text):
            body_lines = m.group("body").count("\n") + 1
            start = int(m.group("line"))
            ranges.append((m.group("file"), start, start + body_lines - 1))

    if not ranges:
        return  # search/query results aren't verbatim line-range delivery; nothing to track

    tracked = json.loads(TRACKER.read_text()) if TRACKER.exists() else []
    for f, frm, to in ranges:
        tracked.append({"file": f, "from": frm, "to": to})
    TRACKER.write_text(json.dumps(tracked))


if __name__ == "__main__":
    main()
