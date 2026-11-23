#!/usr/bin/env python3
"""Render a ci_invariants.py log as the published engine benchmark (MKT-03).

Engine-only track: no LLM. change-impact's recall and precision against
committed ground truth, per case and per language, plus the structural
invariants. Pins come from the log (corpus commits) and the arguments.

usage: engine_benchmark.py LOG --prism-commit SHA --research-commit SHA > ENGINE.md
"""
import argparse
import re
import sys
from collections import defaultdict

LANG = {
    "jackson": "Java", "guava": "Java", "commons-collections": "Java", "gson": "Java",
    "typeorm": "TypeScript", "django": "Python", "grafana": "Go", "gin": "Go",
}

CASE = re.compile(r"^\s+(\S+)\s+GT=(\d+)\s+recall=([\d.]+) \(base ([\d.]+)\) precision=([\d.]+) \(base ([\d.]+)\) sites=(\d+)\s+\[(\w+)\]")
FETCH = re.compile(r"^\[fetch\] (\S+) @ (\w+) <- (\S+)")
CHECK = re.compile(r"^\s+(.+?)\s+\[(OK|FAIL|REGRESSION|[A-Z]+)\]\s*$")


def lang_of(name):
    for prefix, lang in LANG.items():
        if name.startswith(prefix):
            return lang
    return "?"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("log")
    ap.add_argument("--prism-commit", required=True)
    ap.add_argument("--research-commit", required=True)
    a = ap.parse_args()
    cases, fetches, sections = [], [], defaultdict(list)
    section = ""
    held = False
    for line in open(a.log):
        line = line.rstrip("\n")
        if line.startswith("=== "):
            section = line.strip("= ").strip()
            continue
        if "All invariants held." in line:
            held = True
        m = FETCH.match(line)
        if m:
            fetches.append(m.groups())
            continue
        m = CASE.match(line)
        if m:
            name, gt, rec, brec, prec, bprec, sites, status = m.groups()
            cases.append(dict(name=name, gt=int(gt), recall=float(rec), precision=float(prec), sites=int(sites), status=status))
            continue
        m = CHECK.match(line)
        if m and section:
            sections[section].append((m.group(1), m.group(2)))
        elif section and line.strip().startswith("skipped:"):
            sections[section].append((line.strip(), "SKIPPED"))
    out = []
    w = out.append
    w("# Engine benchmark: change-impact completeness")
    w("")
    w("Engine-only track (MKT-03): no model is involved. For each case, change-impact's sites are scored against")
    w("committed ground truth: every site the merged upstream change had to touch. Recall is the share of")
    w("required sites reported. Precision is the share of reported non-test sites that are required; sites in")
    w("test files are neutral (harness/scoring/score.py).")
    w("")
    w(f"- prism commit: `{a.prism_commit}`")
    w(f"- invariant suite: `provasign/research` at `{a.research_commit}`, `harness/scoring/ci_invariants.py`")
    w(f"- result: {'all invariants held' if held else 'NOT all invariants held'}")
    w("")
    w("## Per language")
    w("")
    w("| Language | Cases | Required sites | Recall (site-weighted) | Precision (mean) |")
    w("|---|---|---|---|---|")
    by = defaultdict(list)
    for c in cases:
        by[lang_of(c["name"])].append(c)
    for lang in sorted(by):
        cs = by[lang]
        gt = sum(c["gt"] for c in cs)
        found = sum(round(c["recall"] * c["gt"]) for c in cs)
        prec = sum(c["precision"] for c in cs) / len(cs)
        w(f"| {lang} | {len(cs)} | {gt} | {found}/{gt} = {found / gt:.1%} | {prec:.1%} |")
    w("")
    w("## Per case")
    w("")
    w("| Case | Language | Required sites | Recall | Precision | Sites reported |")
    w("|---|---|---|---|---|---|")
    for c in cases:
        w(f"| {c['name']} | {lang_of(c['name'])} | {c['gt']} | {c['recall']:.4f} | {c['precision']:.4f} | {c['sites']} |")
    w("")
    w("Recall below 1.0 is a real miss; it is reported, not hidden. \"Sites reported\" counts every site")
    w("change-impact returned, including test-file sites (neutral in scoring) and a site reached through more")
    w("than one group (a declaration that is also a family member), which scoring counts once.")
    w("")
    w("## Structural invariants")
    w("")
    for name, rows in sections.items():
        if name.startswith("Ceiling regression") or not rows:
            continue
        w(f"**{name}**")
        w("")
        for text, status in rows:
            w(f"- {re.sub(r'  +', ' ', text)}: {status}")
        w("")
    w("## Corpus")
    w("")
    w("| Repository | Commit | Source |")
    w("|---|---|---|")
    for repo, sha, url in fetches:
        w(f"| {repo} | `{sha}` | {url} |")
    w("")
    w("## Reproduce")
    w("")
    w("```sh")
    w(f"git -C research checkout {a.research_commit}")
    w("python3 harness/scoring/ci_invariants.py --corpus-root /tmp/ci-corpus --prism ./prism \\")
    w("  --decoy-injector $(go env GOPATH)/bin/decoy > ci.log")
    w(f"python3 scripts/engine_benchmark.py ci.log --prism-commit {a.prism_commit} --research-commit {a.research_commit}")
    w("```")
    w("")
    w("Use a fresh corpus root: the harness treats any existing corpus directory as already fetched.")
    sys.stdout.write("\n".join(out) + "\n")


if __name__ == "__main__":
    main()
