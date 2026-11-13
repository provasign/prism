#!/usr/bin/env python3
"""Tally a run_multirepo.py results dir: resolves, task cost at 5m (startup apart),
requests, output tokens, and prism CLI calls counted from the preserved transcripts
(the harness tool classifier files a `prism ...` Bash call under "bash other").
Usage: tally.py <out-dir>"""
import json
import re
import sys
from pathlib import Path

out = Path(sys.argv[1])
res = json.loads((out / "results.json").read_text())
ARMS = ("native", "prism_cli")


def calls(path):
    n = {"tool_calls": 0, "prism": 0, "grep": 0}
    if not path or not Path(path).exists():
        return n
    for line in open(path):
        try:
            j = json.loads(line)
        except ValueError:
            continue
        if j.get("type") != "assistant":
            continue
        for c in j["message"].get("content", []):
            if c.get("type") != "tool_use":
                continue
            n["tool_calls"] += 1
            cmd = (c.get("input") or {}).get("command", "")
            n["prism"] += len(re.findall(r"(?:^|[;&|(]\s*|&&\s*)prism\s", cmd))
            n["grep"] += 1 if re.search(r"\b(grep|rg)\b", cmd) or c.get("name") == "Grep" else 0
    return n


def task_usd(c, ttl="5m"):
    rp = (c.get("repriced") or {}).get(ttl) or {}
    return (rp.get("usd") or 0) - (rp.get("startup_usd") or 0)


tot = {a: dict(res=0, task=0.0, start=0.0, req=0, out=0, prism=0, grep=0, tools=0, n=0) for a in ARMS}
print(f"{'task':30} {'native':>28} {'prism_cli':>34}")
for tid, row in res.items():
    cells = row["cells"]
    if not all(a in cells for a in ARMS):
        continue
    parts = []
    for a in ARMS:
        c = cells[a]
        k = calls(c.get("transcript_copy"))
        rp = (c.get("repriced") or {}).get("5m") or {}
        t = tot[a]
        t["n"] += 1
        t["res"] += 1 if c.get("resolved") else 0
        t["task"] += task_usd(c)
        t["start"] += rp.get("startup_usd") or 0
        t["req"] += rp.get("requests") or 0
        t["out"] += c.get("tokens_out") or 0
        t["prism"] += k["prism"]
        t["grep"] += k["grep"]
        t["tools"] += k["tool_calls"]
        parts.append(f"{'OK ' if c.get('resolved') else 'NO '}${task_usd(c):.3f} req={rp.get('requests')} "
                     f"tools={k['tool_calls']} prism={k['prism']} app={len(c.get('app_files') or [])}")
    print(f"{tid:30} {parts[0]:>28} | {parts[1]}")
print()
for a in ARMS:
    t = tot[a]
    print(f"{a:10} resolved {t['res']}/{t['n']}  task@5m ${t['task']:.3f}  startup ${t['start']:.3f}  "
          f"requests {t['req']}  output tokens {t['out']}  tool calls {t['tools']}  prism calls {t['prism']}  grep calls {t['grep']}")
n, p = tot["native"], tot["prism_cli"]
if n["task"]:
    print(f"prism/native: task cost {p['task'] / n['task']:.3f}x, total incl. startup "
          f"{(p['task'] + p['start']) / (n['task'] + n['start']):.3f}x, requests {p['req']}/{n['req']}")
