#!/usr/bin/env python3
"""Recall of prism test-plan tiers on the 42-task issue bed (MKT-08 step 5).

For each task: shallow-fetch the base commit from the public GitHub repo,
commit the held-out test patch (the failing tests exist, as they would for
a developer), apply the gold source patch uncommitted, index, and run
test-plan with two binaries:
  pre  = before the two tiers (focused / transitive / coarse fallback)
  new  = run first (focused, transitive, coverage) + run for safety
A failing test counts as selected by a tier when its file is listed there.
No model is called.

usage: measure.py --tasks DIR --manifest FILE --work DIR --out FILE
                  --pre BIN --new BIN [--jobs N] [--only ID,...]
"""
from __future__ import annotations

import argparse
import ast
import concurrent.futures as cf
import json
import os
import subprocess
import sys
import time
from pathlib import Path


def sh(cmd, cwd=None, timeout=1800, check=True, inp=None):
    r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=timeout, input=inp)
    if check and r.returncode != 0:
        raise RuntimeError(f"{' '.join(cmd[:4])}: rc={r.returncode}: {r.stderr[-800:]}")
    return r


def failing_tests(task):
    f = task["fail_to_pass"]
    if isinstance(f, str):
        try:
            f = ast.literal_eval(f)
        except Exception:
            f = [f]
    return list(f)


def test_files(task, wt: Path, tests):
    """Map each failing test id to repository file(s)."""
    lang = task.get("lang") or "py"
    files = git_files(wt)
    out = {}
    for t in tests:
        if lang == "java":
            cls = t.split("::")[0].split("$")[0]
            suffix = cls.replace(".", "/") + ".java"
            out[t] = sorted(f for f in files if f.endswith("/" + suffix) or f == suffix)
        elif lang == "go":
            name = t.split("/")[0]
            r = sh(["git", "grep", "-l", "-E", rf"^func {name}\(", "--", "*_test.go"], cwd=wt, check=False)
            out[t] = sorted(l for l in r.stdout.splitlines() if l)
        elif lang in ("ts", "js"):
            out[t] = [t.split(" > ")[0]]
        else:
            out[t] = [t.split("::")[0]]
    return out


def git_files(wt: Path):
    return set(sh(["git", "ls-files"], cwd=wt).stdout.splitlines())


def tiers(plan):
    run_first = {}
    for t in plan.get("fileTargets") or []:
        run_first[t["file"]] = t.get("classification", "")
    safety = set()
    for p in plan.get("safety") or []:
        safety.update(p.get("testFiles") or [])
    return run_first, safety


def prepare(task, work: Path):
    wt = work / task["instance_id"]
    marker = wt / ".prepared"
    if marker.exists():
        return wt
    if wt.exists():
        sh(["rm", "-rf", str(wt)])
    wt.mkdir(parents=True)
    sh(["git", "init", "-q"], cwd=wt)
    sh(["git", "remote", "add", "origin", f"https://github.com/{task['repo']}.git"], cwd=wt)
    sh(["git", "fetch", "-q", "--depth", "1", "origin", task["base_commit"]], cwd=wt, timeout=3600)
    sh(["git", "checkout", "-q", "FETCH_HEAD"], cwd=wt)
    env = dict(os.environ, GIT_AUTHOR_NAME="m", GIT_AUTHOR_EMAIL="m@m", GIT_COMMITTER_NAME="m", GIT_COMMITTER_EMAIL="m@m")
    if task.get("test_patch"):
        sh(["git", "apply", "--whitespace=nowarn", "-"], cwd=wt, inp=task["test_patch"])
        sh(["git", "add", "-A"], cwd=wt)
        subprocess.run(["git", "commit", "-qm", "tests"], cwd=wt, env=env, capture_output=True)
    sh(["git", "apply", "--whitespace=nowarn", "-"], cwd=wt, inp=task["patch"])
    # Keep the index out of the diff.
    with open(wt / ".git" / "info" / "exclude", "a") as f:
        f.write("\n.grove/\n")
    marker.write_text("ok")
    return wt


def plan_with(bin_path, wt: Path):
    t0 = time.time()
    r = sh([bin_path, "test-plan", "--format", "json"], cwd=wt, check=False, timeout=3600)
    try:
        plan = json.loads(r.stdout)
    except Exception:
        plan = {"error": (r.stderr or r.stdout)[-800:]}
    return plan, round(time.time() - t0, 1)


def one(task, args):
    rec = {"task": task["instance_id"], "lang": task.get("lang") or "py"}
    try:
        wt = prepare(task, Path(args.work))
        t0 = time.time()
        idx = sh([args.new, "index", "--allow-heuristic"], cwd=wt, check=False, timeout=3600)
        rec["index_rc"], rec["index_s"] = idx.returncode, round(time.time() - t0, 1)
        tests = failing_tests(task)
        files = test_files(task, wt, tests)
        rec["tests"] = files
        for arm, b in (("pre", args.pre), ("new", args.new)):
            plan, secs = plan_with(b, wt)
            if "error" in plan:
                rec[arm] = {"error": plan["error"], "secs": secs}
                continue
            run_first, safety = tiers(plan)
            hits = {}
            for t, fs in files.items():
                cls = [run_first[f] for f in fs if f in run_first]
                hits[t] = {
                    "mapped": bool(fs),
                    "focused": any(c in ("focused file", "file") for c in cls),
                    "run_first": bool(cls),
                    "safety": any(f in safety for f in fs),
                    "classes": sorted(set(cls)),
                }
            rec[arm] = {
                "secs": secs,
                "run_first_files": len(run_first),
                "focused_files": sum(1 for c in run_first.values() if c in ("focused file", "file")),
                "safety_files": len(safety),
                "changed_symbols": len(plan.get("changedSymbols") or []),
                "gaps": len(plan.get("gaps") or []),
                "hits": hits,
            }
    except Exception as e:  # recorded, not fatal
        rec["error"] = str(e)[-800:]
    return rec


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tasks", required=True)
    ap.add_argument("--manifest", required=True)
    ap.add_argument("--work", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--pre", required=True)
    ap.add_argument("--new", required=True)
    ap.add_argument("--jobs", type=int, default=3)
    ap.add_argument("--only", default="")
    args = ap.parse_args()
    ids = json.load(open(args.manifest))
    if args.only:
        keep = set(args.only.split(","))
        ids = [i for i in ids if i in keep]
    done = set()
    if os.path.exists(args.out):
        for line in open(args.out):
            try:
                r = json.loads(line)
                if "error" not in r:
                    done.add(r["task"])
            except Exception:
                pass
    tasks = [json.load(open(Path(args.tasks) / f"{i}.json")) for i in ids if i not in done]
    with cf.ThreadPoolExecutor(args.jobs) as ex, open(args.out, "a") as out:
        for rec in ex.map(lambda t: one(t, args), tasks):
            out.write(json.dumps(rec) + "\n")
            out.flush()
            print(rec["task"], rec.get("error", "")[:120] or "ok", flush=True)
    print("ALLDONE", flush=True)


if __name__ == "__main__":
    sys.exit(main())
