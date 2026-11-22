#!/usr/bin/env python3
"""Summarize results.jsonl: per-test recall of each tier, and sizes."""
import json
import sys
from collections import defaultdict

path = sys.argv[1] if len(sys.argv) > 1 else "results.jsonl"
recs = {}
for line in open(path):
    r = json.loads(line)
    recs[r["task"]] = r  # last record per task wins

errors = [r for r in recs.values() if "error" in r]
ok = [r for r in recs.values() if "error" not in r]


def tests_of(r, arm):
    a = r.get(arm) or {}
    return a.get("hits") or {}


print(f"tasks: {len(recs)} recorded, {len(ok)} measured, {len(errors)} errored")
for r in errors:
    print(f"  ERROR {r['task']}: {r['error'][:160]}")

for arm in ("pre", "new"):
    n = mapped = focused = first = safety = either = 0
    by_lang = defaultdict(lambda: [0, 0, 0, 0])
    sizes = defaultdict(list)
    for r in ok:
        a = r.get(arm) or {}
        if "error" in a:
            print(f"  {arm} plan error {r['task']}: {a['error'][:120]}")
            continue
        for key in ("run_first_files", "focused_files", "safety_files"):
            sizes[key].append(a.get(key, 0))
        for t, h in tests_of(r, arm).items():
            n += 1
            if not h["mapped"]:
                continue
            mapped += 1
            focused += h["focused"]
            first += h["run_first"]
            safety += h["safety"]
            either += h["run_first"] or h["safety"]
            bl = by_lang[r["lang"]]
            bl[0] += 1
            bl[1] += h["focused"]
            bl[2] += h["run_first"]
            bl[3] += h["safety"]
    print(f"\n[{arm}] failing tests: {n}, mapped to a file: {mapped}")
    if mapped:
        print(f"  focused   {focused}/{mapped} = {focused / mapped:.0%}")
        print(f"  run first {first}/{mapped} = {first / mapped:.0%}   (pre: any file target incl. coarse fallback)")
        print(f"  safety    {safety}/{mapped} = {safety / mapped:.0%}")
        print(f"  either    {either}/{mapped} = {either / mapped:.0%}")
    for lang, (m, f, rf, s) in sorted(by_lang.items()):
        print(f"  {lang:5} mapped {m:3}  focused {f:3}  run-first {rf:3}  safety {s:3}")
    for key, vals in sizes.items():
        if vals:
            vals = sorted(vals)
            print(f"  {key}: median {vals[len(vals) // 2]}, max {vals[-1]}")

print("\nmisses in the new safety tier:")
for r in ok:
    for t, h in tests_of(r, "new").items():
        if h["mapped"] and not h["safety"]:
            print(f"  {r['task']}: {t[:110]}")
print("unmapped tests:")
for r in ok:
    for t, h in tests_of(r, "new").items():
        if not h["mapped"]:
            print(f"  {r['task']}: {t[:110]}")
