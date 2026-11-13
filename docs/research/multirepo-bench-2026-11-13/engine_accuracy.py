#!/usr/bin/env python3
"""Engine accuracy of cross-repo change-impact, no LLM.

For every library function, method and type a consumer repository references
(go/types oracle, mroracle), run `prism change-impact <name>` in the library with
the consumer linked, and compare the consumer sites it lists (file:line) with the
oracle's. A whole-word grep of the member name over the consumer's .go files is
scored the same way as the baseline an agent without Prism starts from.
Usage: engine_accuracy.py <bed-dir> <lib-module> [--prism BIN] [--out json]
"""
import argparse, json, os, re, subprocess, collections
from pathlib import Path

ap = argparse.ArgumentParser()
ap.add_argument("bed"); ap.add_argument("libmod")
ap.add_argument("--prism", default=os.path.expanduser("~/Projects/provasign-mr/bin/prism-mr"))
ap.add_argument("--out")
a = ap.parse_args()
bed = Path(a.bed); lib = bed / "lib"; app = bed / "app"

gold = collections.defaultdict(set); kind = {}
for l in open(bed / "oracle.jsonl"):
    r = json.loads(l)
    if r["kind"] not in ("func", "method", "type", "ifacemethod"):
        continue
    gold[r["key"]].add((r["file"], r["line"])); kind[r["key"]] = r["kind"]

def query_name(key):
    pkg, _, desc = key.partition("#")
    if kind[key] in ("method", "ifacemethod"):
        return desc                      # Type.Method
    short = pkg.rsplit("/", 1)[-1]
    if re.fullmatch(r"v\d+", short):      # module major-version suffix
        short = pkg.rsplit("/", 2)[-2]
    return f"{short}.{desc}"

# pkg name -> declared package name (v6 -> billy etc.) from the oracle's import path
def pkgname(key):
    pkg = key.partition("#")[0]
    d = lib / os.path.relpath(pkg, a.libmod) if pkg != a.libmod else lib
    for f in sorted(d.glob("*.go")):
        m = re.search(r"^package (\w+)", f.read_text(errors="ignore"), re.M)
        if m and not m.group(1).endswith("_test"):
            return m.group(1)
    return None

loaded = {l.split("loaded: ",1)[1].strip() for l in open(bed / "oracle.err") if l.startswith("loaded: ")}
site = re.compile(r"^\s+\.\./app/(\S+?):(\d+):\d+:")
gofiles = [p for p in app.rglob("*.go") if "/vendor/" not in str(p)]
texts = {p: p.read_text(errors="ignore").splitlines() for p in gofiles}

def grep(name):
    w = re.compile(r"\b" + re.escape(name) + r"\b")
    return {(str(p.relative_to(app)), i + 1) for p, ls in texts.items() for i, l in enumerate(ls) if w.search(l)}

rows = []
env = dict(os.environ)
for key in sorted(gold):
    desc = key.partition("#")[2]
    if kind[key] == "func" or kind[key] == "type":
        pn = pkgname(key)
        q = f"{pn}.{desc}" if pn else query_name(key)   # what an agent types: pkg.Name
    else:
        q = desc
    r = subprocess.run([a.prism, "change-impact", q, "--format", "text"], cwd=lib, capture_output=True, text=True, env=env, timeout=300)
    pred = set(); cut = False; scope = ""
    for l in r.stdout.splitlines():
        m = site.match(l)
        if m: pred.add((m.group(1), int(m.group(2))))
        if l.startswith("workspaceScope:"): scope = l
        if "more" in l and "../app" in l and "not shown" in l: cut = True
    outside = {p for p in pred if p[0] not in loaded}
    pred -= outside
    g = gold[key]; tp = len(pred & g)
    gr = {p for p in grep(desc.split(".")[-1]) if p[0] in loaded}; gtp = len(gr & g)
    rows.append(dict(key=key, kind=kind[key], query=q, gold=len(g), pred=len(pred), tp=tp,
                     grep=len(gr), grep_tp=gtp, outside_build=len(outside), scope=scope, rc=r.returncode,
                     missed=sorted(g - pred)[:5], extra=sorted(pred - g)[:5]))

def summ(rs, label):
    G = sum(r["gold"] for r in rs); P = sum(r["pred"] for r in rs); T = sum(r["tp"] for r in rs)
    GP = sum(r["grep"] for r in rs); GT = sum(r["grep_tp"] for r in rs)
    full = sum(1 for r in rs if r["tp"] == r["gold"] and r["pred"] == r["tp"])
    print(f"{label:10} symbols={len(rs):4} sites={G:5}  prism recall={T/G if G else 0:.3f} precision={T/P if P else 0:.3f} exact={full}/{len(rs)}"
          f" outside_build={sum(r['outside_build'] for r in rs)}   grep recall={GT/G if G else 0:.3f} precision={GT/GP if GP else 0:.3f} lines_returned={GP}")
summ(rows, "all")
for k in ("func", "method", "ifacemethod", "type"):
    rs = [r for r in rows if r["kind"] == k]
    if rs: summ(rs, k)
bad = [r for r in rows if r["tp"] != r["gold"] or r["pred"] != r["tp"]]
for r in sorted(bad, key=lambda r: -(r["gold"] - r["tp"]) - (r["pred"] - r["tp"]))[:12]:
    print(f"  {r['query']:40} gold={r['gold']:3} pred={r['pred']:3} tp={r['tp']:3} missed={r['missed'][:2]} extra={r['extra'][:2]}")
if a.out:
    Path(a.out).write_text(json.dumps(rows, indent=1))
