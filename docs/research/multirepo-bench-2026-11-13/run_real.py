#!/usr/bin/env python3
"""Agent A/B on real cross-repository tasks (real_tasks.json, validated by real_tasks.py).

The cell: the library at the parent of its change and the consumer at the parent
of its change, side by side under a go.work. The agent works in the consumer (where
the problem was reported) and gets the PR's problem text plus one sentence saying
the library is checked out next to it. Same measurement as the release benchmark:
Sonnet 5.5, prompt caching disabled per cell, cost repriced from per-request tokens,
startup reported apart. Scoring is SWE-bench style: the gold commits' test files are
written over the agent's tree and the validated FAIL_TO_PASS and PASS_TO_PASS tests run.

  run_real.py --out DIR [--tasks id,...] [--arms native,prism_cli]
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
import sys  # noqa: E402

sys.path.insert(0, str(HERE))
import real_tasks as rt  # noqa: E402
import run_multirepo as rm  # noqa: E402  (harness imports, PRISM, cost helpers)

run_e2e = rm.run_e2e
MODEL = rm.MODEL
PRISM = rm.PRISM
SPEND_CAP = float(os.environ.get("MR_SPEND_CAP_USD", "40"))


def tail_for(task):
    return (f"\n\nThe {task['lib']} library this project depends on is checked out next to it (../{task['lib']}), "
            "and a go.work in the parent directory builds the two together. The change may need edits in both.")


def sanitize_and_commit(repo: Path):
    removed = False
    for name in rm.SANITIZE:
        p = repo / name
        if p.is_dir() and not p.is_symlink():
            shutil.rmtree(p)
            removed = True
        elif p.exists() or p.is_symlink():
            p.unlink()
            removed = True
    if removed:
        rt.sh(["git", "-C", str(repo), "add", "-A"])
    rt.sh(["git", "-C", str(repo), "-c", "user.name=bench", "-c", "user.email=bench@localhost", "commit", "-q",
           "--allow-empty", "--no-verify", "-m", "bench base"])


def setup_cell(task, arm):
    parent, lib, app = rt.setup(task, "base", arm)
    for repo in (lib, app):
        sanitize_and_commit(repo)
    env = rt.go_env(parent)
    if arm == "prism_cli":
        r = rt.sh([PRISM, "init", "--harness", "claude", "--interface", "cli", "--yes", "--workspace", "auto",
                   str(app)], env=env, timeout=3600)
        md = (app / "CLAUDE.md").read_text() if (app / "CLAUDE.md").exists() else ""
        if "Linked repositories" not in md:
            raise RuntimeError(f"prism init did not link {task['lib']}: {r.stdout[-800:]}")
        if os.environ.get("MR_GO_MOD_DOWNLOAD") == "1":
            # grove's Go analyzer runs with GOWORK=off, so it type-checks against the module
            # cache, not the linked checkout; download what each go.mod pins (prism's own fix hint).
            for repo in (lib, app):
                rt.sh(["go", "mod", "download"], cwd=repo, env={**env, "GOWORK": "off"}, timeout=3600)
        rt.sh([PRISM, "index", str(app)], env=env, timeout=3600)
        if not (lib / ".grove" / "grove.db").exists():
            raise RuntimeError("library was linked but not indexed")
    return parent, lib, app


def agent_env(parent):
    def _env(wt, task):
        env = rt.go_env(parent)
        env["ZDOTDIR"] = str(rm.HARNESS / "hooks/pinned-zdotdir")
        env["E2E_PINNED_PATH"] = rt.GO_BIN
        env["SHELL_SESSIONS_DISABLE"] = "1"
        for k in ("GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"):
            env.pop(k, None)
        run_e2e.GH_EMPTY_CONFIG.mkdir(parents=True, exist_ok=True)
        env["GH_CONFIG_DIR"] = str(run_e2e.GH_EMPTY_CONFIG)
        return env, {"go": "auto"}
    return _env


def revert_agent_tests(repo: Path):
    """The agent's own test edits are not scored (the release harness drops them from the
    scored diff too): a test it wrote can collide with a gold test of the same name."""
    rt.sh(["git", "-C", str(repo), "add", "-A"], check=False)
    names = rt.sh(["git", "-C", str(repo), "diff", "--cached", "--name-status", "HEAD"], check=False).stdout
    for line in names.splitlines():
        parts = line.split("\t")
        status, path = parts[0], parts[-1]
        if not rt.is_test_path(path):
            continue
        if status.startswith("A"):
            (repo / path).unlink(missing_ok=True)
        else:
            rt.sh(["git", "-C", str(repo), "checkout", "HEAD", "--", path], check=False)
    rt.sh(["git", "-C", str(repo), "reset", "-q"], check=False)


def write_gold_tests(task, lib, app):
    """SWE-bench style: each test file the gold commits touched is set to its gold content."""
    for repo in (lib, app):
        revert_agent_tests(repo)
    for d, repo, commits in ((lib, task["lib_repo"], task["lib_test_commits"]),
                             (app, task["app_repo"], task["app_test_commits"])):
        for c in commits:
            out = rt.sh(["git", "-C", str(rt.CORPUS / repo), "show", "--format=", "--name-status", c]).stdout
            for line in out.splitlines():
                parts = line.split("\t")
                status, path = parts[0], parts[-1]
                if not rt.is_test_path(path):
                    continue
                dest = d / path
                if status.startswith("D"):
                    dest.unlink(missing_ok=True)
                    continue
                blob = subprocess.run(["git", "-C", str(rt.CORPUS / repo), "show", f"{c}:{path}"],
                                      capture_output=True, check=True).stdout
                dest.parent.mkdir(parents=True, exist_ok=True)
                dest.write_bytes(blob)


def run_cell(task, arm, validated, outdir):
    t0 = time.monotonic()
    parent, lib, app = setup_cell(task, arm)
    setup_s = round(time.monotonic() - t0, 1)
    try:
        run_e2e._agent_env = agent_env(parent)
        run_e2e.MINIMAL_TAIL = tail_for(task)
        orig_cmd = run_e2e._cloud_cmd

        def cmd_with_lib(model, arm_, wt, prompt):
            return orig_cmd(model, arm_, wt, prompt) + ["--add-dir", str(lib)]
        run_e2e._cloud_cmd = cmd_with_lib
        try:
            rec = run_e2e._run_cloud(MODEL, arm, app, {"problem_statement": task["statement"], "kind": "multirepo"})
        finally:
            run_e2e._cloud_cmd = orig_cmd
        lib_diff, app_diff = rm.diff_stat(lib), rm.diff_stat(app)
        cell_dir = outdir / "diffs" / f"{task['id']}.{arm}"
        cell_dir.mkdir(parents=True, exist_ok=True)
        (cell_dir / "lib.diff").write_text(lib_diff["patch"])
        (cell_dir / "app.diff").write_text(app_diff["patch"])
        write_gold_tests(task, lib, app)
        select = validated["FAIL_TO_PASS"] + validated["PASS_TO_PASS"]
        results = rt.score_tree(task, parent, lib, app, select=select)
        resolved = all(results.get(t) == "pass" for t in select)
        if rec.get("session_id"):
            rec["transcript_copy"] = run_e2e._preserve_transcript(rec["session_id"], outdir / "transcripts" / f"{task['id']}.{arm}")
        rec.update(task=task["id"], arm=arm, setup_s=setup_s, resolved=resolved, tests=results,
                   lib_files=lib_diff["files"], app_files=app_diff["files"], repriced=rm.reprice(rec))
        return rec
    finally:
        shutil.rmtree(parent, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True)
    ap.add_argument("--tasks", default="")
    ap.add_argument("--arms", default="native,prism_cli")
    ap.add_argument("--seed", default="multirepo-real-2026-11-13")
    ap.add_argument("--rescore", default="", help="re-score saved diffs with the current scorer; the value is the reason")
    a = ap.parse_args()
    outdir = Path(a.out).resolve()
    outdir.mkdir(parents=True, exist_ok=True)
    tasks = json.loads((HERE / "real_tasks.json").read_text())
    validated = json.loads((HERE / "real_tasks.validated.json").read_text())
    if a.tasks:
        keep = set(a.tasks.split(","))
        tasks = [t for t in tasks if t["id"] in keep]
    logf = (outdir / "run.log").open("a")

    def log(msg):
        line = time.strftime("%H:%M:%S ") + msg
        print(line, flush=True)
        logf.write(line + "\n")
        logf.flush()

    if a.rescore:
        results = json.loads((outdir / "results.json").read_text())
        for t in tasks:
            v = validated.get(t["id"]) or {}
            for arm, rec in (results.get(t["id"]) or {}).get("cells", {}).items():
                d = outdir / "diffs" / f"{t['id']}.{arm}"
                if not d.exists():
                    continue
                parent, lib, app = rt.setup(t, "base", "rescore")
                try:
                    for repo in (lib, app):
                        sanitize_and_commit(repo)
                    for repo, name in ((lib, "lib.diff"), (app, "app.diff")):
                        if (d / name).read_text().strip():
                            rt.sh(["git", "-C", str(repo), "apply", "--whitespace=nowarn", str(d / name)])
                    write_gold_tests(t, lib, app)
                    select = v.get("FAIL_TO_PASS", []) + v.get("PASS_TO_PASS", [])
                    tests = rt.score_tree(t, parent, lib, app, select=select)
                finally:
                    shutil.rmtree(parent, ignore_errors=True)
                resolved = bool(select) and all(tests.get(x) == "pass" for x in select)
                if resolved != rec.get("resolved"):
                    rec.setdefault("rescored", []).append({"was": rec.get("resolved"), "now": resolved, "reason": a.rescore})
                rec["tests"], rec["resolved"] = tests, resolved
                log(f"RESCORE {t['id']} {arm} resolved={resolved} failing={sorted(k for k, x in tests.items() if x != 'pass')}")
        (outdir / "results.json").write_text(json.dumps(results, indent=1))
        return

    os.environ["DISABLE_PROMPT_CACHING"] = "1"
    os.environ["E2E_TAIL"] = "minimal"
    results_path = outdir / "results.json"
    results = json.loads(results_path.read_text()) if results_path.exists() else {}
    arms = a.arms.split(",")
    for t in tasks:
        v = validated.get(t["id"]) or {}
        if not v.get("FAIL_TO_PASS"):
            log(f"SKIP {t['id']}: no validated FAIL_TO_PASS tests ({v.get('error', '')[:200]})")
            continue
        row = results.setdefault(t["id"], {"cells": {}})
        for arm in run_e2e.arm_order(a.seed, t["id"], arms):
            if arm in row["cells"] and row["cells"][arm].get("resolved") is not None:
                continue
            spent = sum((c.get("cost_usd") or 0) for r in results.values() for c in r["cells"].values())
            if spent >= SPEND_CAP:
                log(f"STOP: uncached spend ${spent:.2f} reached cap ${SPEND_CAP:.2f}")
                return
            log(f"CELL {t['id']} {arm} start")
            try:
                rec = run_cell(t, arm, v, outdir)
            except Exception as e:  # noqa: BLE001
                rec = {"task": t["id"], "arm": arm, "resolved": None, "error": f"{type(e).__name__}: {str(e)[:500]}"}
            row["cells"][arm] = rec
            results_path.write_text(json.dumps(results, indent=1))
            rp = (rec.get("repriced") or {}).get("5m") or {}
            log(f"CELL {t['id']} {arm} resolved={rec.get('resolved')} 5m=${rp.get('usd')} startup=${rp.get('startup_usd')} "
                f"requests={rp.get('requests')} raw=${rec.get('cost_usd')} lib_files={len(rec.get('lib_files') or [])} "
                f"app_files={len(rec.get('app_files') or [])} err={rec.get('error') or rec.get('agent_error') or ''}")
        cells = row["cells"]
        if all(arm in cells for arm in arms):
            parts = []
            for arm in arms:
                rp = (cells[arm].get("repriced") or {}).get("5m") or {}
                parts.append(f"{arm}: resolved={cells[arm].get('resolved')} "
                             f"task5m=${(rp.get('usd') or 0) - (rp.get('startup_usd') or 0):.3f} req={rp.get('requests')}")
            log(f"PAIR {t['id']}: " + " | ".join(parts))
    log("ALLDONE")


if __name__ == "__main__":
    main()
