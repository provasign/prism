#!/usr/bin/env python3
"""Cross-repository agent A/B: native Claude Code vs Prism CLI with linked repositories.

Each cell is a fresh parent directory holding two clones side by side, the way a
developer has them: the library (the agent's working directory) and a repository
that uses it, plus a go.work in the parent that builds them together. The task
asks for an API change in the library; the consumer must be updated too. Both
arms get the identical prompt and the identical tree. The prism arm additionally
runs `prism init --interface cli --workspace auto` in the library (the product
path: it links the consumer from go.work and writes CLAUDE.md steering) with the
multi-repo branch binary on PATH.

Measurement follows the standard cost runner (research harness run_ttl.py):
DISABLE_PROMPT_CACHING=1 so no cell reads another's cache; cost repriced from
per-request tokens at the 5m and 1h cache rates; startup (first request's
prompt) reported apart from task cost. Sonnet 5.5.

Scoring is mechanical, no LLM: both modules `go vet -atomic ./...` (type-checks
every package and test) for darwin, linux and windows (darwin only where cgo is required); `go doc` shows the new API and
not the old; a hidden behaviour test where the task has one; the library's
changed packages' tests pass.

Usage:
  run_multirepo.py --out DIR [--tasks id,id] [--arms native,prism_cli] [--baseline]
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
HARNESS = Path(os.environ.get("RESEARCH_HARNESS", "~/Projects/provasign/research/harness")).expanduser()
for p in (HARNESS / "runners", HARNESS / "aggregate", HARNESS / "build", HARNESS / "scoring", HARNESS):
    sys.path.insert(0, str(p))
import run_e2e  # noqa: E402
import usage_account  # noqa: E402

CORPUS = Path("~/.cache/prism-research/multirepo-corpus").expanduser()
BED = Path("~/.cache/prism-research/multirepo-bed").expanduser()
CELLS = Path("~/.cache/prism-research/mrcells").expanduser()
GO_BIN = os.environ.get("MR_GO_BIN", "/usr/local/go/bin")
MODEL = "claude-sonnet-5-5"
PRISM = os.environ.get("MR_PRISM_BIN", str(Path("~/Projects/provasign-mr/bin/prism-mr").expanduser()))
SPEND_CAP = float(os.environ.get("MR_SPEND_CAP_USD", "40"))
PAIR_REPOS = {
    "pflag-cobra": ("spf13_pflag", "spf13_cobra"),
    "billy-gogit": ("go-git_go-billy", "go-git_go-git"),
    "common-clientgolang": ("prometheus_common", "prometheus_client_golang"),
    "bubbletea-bubbles": ("charmbracelet_bubbletea", "charmbracelet_bubbles"),
    "grove-prism": ("provasign_grove", "provasign_prism"),
}
# Agent-instruction and tool files a clone may carry (prism's own repo does):
# removed from both arms so the native arm carries no Prism steering.
SANITIZE = ("CLAUDE.md", "AGENTS.md", "GEMINI.md", ".mcp.json", "prism.yaml", ".claude", ".codex", ".grove",
            ".cursorrules", ".windsurfrules", ".github/copilot-instructions.md")
ARTIFACTS = (".grove", "prism.yaml", "CLAUDE.md", "AGENTS.md", ".claude", ".mcp.json", ".prism-read-tracker.json")

TAIL = ("\n\nThis repository is a Go library. Repositories that use it are checked out next to it, and a "
        "go.work in the parent directory builds them together against this checkout. Make the change here "
        "and update every place that needs it, in this repository and in those, so that every module "
        "builds and its tests pass.")
# Variant: the prompt does not say where the dependents are (a library
# maintainer's request). Selected with MR_PROMPT=unprompted.
TAIL_UNPROMPTED = ("\n\nThis repository is a Go library. Make the change and update whatever depends on it, "
                   "so that everything still builds and its tests pass.")
if os.environ.get("MR_PROMPT") == "unprompted":
    TAIL = TAIL_UNPROMPTED
run_e2e.MINIMAL_TAIL = TAIL
os.environ["E2E_TAIL"] = "minimal"
run_e2e.PRISM_BIN_FOR_ARM["prism_cli"] = PRISM


def sh(cmd, cwd=None, env=None, timeout=900, check=True):
    r = subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout)
    if check and r.returncode != 0:
        raise RuntimeError(f"{' '.join(map(str, cmd))} (rc={r.returncode}): {(r.stdout + r.stderr)[-600:]}")
    return r


def base_env(parent: Path) -> dict:
    env = dict(os.environ)
    env["PATH"] = GO_BIN + ":" + env.get("PATH", "")
    env["GOTOOLCHAIN"] = "local"
    env.pop("GOFLAGS", None)
    env["PRISM_STATE_DIR"] = str(parent / ".prism-state")
    env["PRISM_CACHE_DIR"] = str(parent / ".prism-cache")
    return env


def agent_env(parent: Path):
    def _env(wt: Path, task):
        env = base_env(parent)
        env["ZDOTDIR"] = str(HARNESS / "hooks/pinned-zdotdir")
        env["E2E_PINNED_PATH"] = GO_BIN
        env["SHELL_SESSIONS_DISABLE"] = "1"
        for k in ("GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"):
            env.pop(k, None)
        run_e2e.GH_EMPTY_CONFIG.mkdir(parents=True, exist_ok=True)
        env["GH_CONFIG_DIR"] = str(run_e2e.GH_EMPTY_CONFIG)
        return env, {"go": "1.26"}
    return _env


def clone(repo: str, sha: str, dest: Path):
    sh(["git", "clone", "--local", "--no-checkout", "-q", str(CORPUS / repo), str(dest)])
    sh(["git", "-C", str(dest), "checkout", "--detach", "-f", "-q", sha])
    for ref in sh(["git", "-C", str(dest), "for-each-ref", "--format=%(refname)"]).stdout.split():
        sh(["git", "-C", str(dest), "update-ref", "-d", ref])
    sh(["git", "-C", str(dest), "remote", "remove", "origin"], check=False)
    for key, value in (("gc.auto", "0"), ("maintenance.auto", "false")):
        sh(["git", "-C", str(dest), "config", key, value])
    removed = False
    for name in SANITIZE:
        p = dest / name
        if p.is_dir() and not p.is_symlink():
            shutil.rmtree(p)
            removed = True
        elif p.exists() or p.is_symlink():
            p.unlink()
            removed = True
    if removed:
        sh(["git", "-C", str(dest), "add", "-A"])
    # A baseline commit, so the scored diff is the agent's alone.
    sh(["git", "-C", str(dest), "-c", "user.name=bench", "-c", "user.email=bench@localhost", "commit", "-q",
        "--allow-empty", "--no-verify", "-m", "bench base"])


def setup_cell(task: dict, arm: str) -> tuple[Path, Path, Path]:
    CELLS.mkdir(parents=True, exist_ok=True)
    parent = Path(tempfile.mkdtemp(prefix=f"{task['id']}-{arm}-", dir=CELLS)).resolve()
    lib_repo, app_repo = PAIR_REPOS[task["pair"]]
    lib, app = parent / task["lib"], parent / task["app"]
    bed = BED / task["pair"]
    clone(lib_repo, sh(["git", "-C", str(bed / "lib"), "rev-parse", "HEAD"]).stdout.strip(), lib)
    clone(app_repo, sh(["git", "-C", str(bed / "app"), "rev-parse", "HEAD"]).stdout.strip(), app)
    env = base_env(parent)
    sh(["go", "work", "init", f"./{task['lib']}", f"./{task['app']}"], cwd=parent, env=env)
    if arm == "prism_cli":
        r = sh([PRISM, "init", "--harness", "claude", "--interface", "cli", "--yes", "--workspace", "auto",
                str(lib)], env=env, timeout=1800)
        md = (lib / "CLAUDE.md").read_text() if (lib / "CLAUDE.md").exists() else ""
        if "Linked repositories" not in md or "no MCP server is registered" not in md:
            raise RuntimeError(f"prism init did not link the consumer or write CLI steering: {r.stdout[-800:]}")
        sh([PRISM, "index", str(lib)], env=env, timeout=1800)
        if not (app / ".grove" / "grove.db").exists():
            raise RuntimeError("consumer was linked but not indexed")
    return parent, lib, app


def diff_stat(repo: Path) -> dict:
    sh(["git", "-C", str(repo), "add", "-A"], check=False)
    excludes = [f":(exclude){a}" for a in ARTIFACTS]
    names = sh(["git", "-C", str(repo), "diff", "--cached", "--name-only", "--", ".", *excludes], check=False).stdout.split()
    patch = sh(["git", "-C", str(repo), "diff", "--cached", "--", ".", *excludes], check=False).stdout
    return {"files": names, "patch": patch}


def score(task: dict, parent: Path, lib: Path, app: Path) -> dict:
    env = base_env(parent)
    out = {"vet": {}, "doc": [], "hidden": None, "lib_tests": None}
    ok = True
    for name, mod in (("lib", lib), ("app", app)):
        for goos in task.get("goos", ("darwin", "linux", "windows")):
            e = dict(env, GOOS=goos)
            # One cheap analyzer: vet still type-checks every package and
            # test file, which is the check; the repos' own vet findings
            # (go-billy's stdmethods, go-git's unsafeptr) are not the task.
            r = sh(["go", "vet", "-atomic", "./..."], cwd=mod, env=e, timeout=1800, check=False)
            out["vet"][f"{name}/{goos}"] = {"rc": r.returncode, "tail": (r.stdout + r.stderr)[-800:] if r.returncode else ""}
            ok &= r.returncode == 0
    for side, pkg, sym, needle, expect in task["doc"]:
        r = sh(["go", "doc", "-u", pkg, sym], cwd=lib if side == "lib" else app, env=env, check=False)
        text = r.stdout
        if expect:
            passed = r.returncode == 0 and (needle is None or needle in text)
        else:
            passed = r.returncode != 0 if needle is None else needle not in text
        out["doc"].append({"sym": sym, "needle": needle, "expect": expect, "passed": passed})
        ok &= passed
    if task.get("hidden_test"):
        h = task["hidden_test"]
        d = lib if h["dir"] == "lib" else app
        (d / h["file"]).write_text(h["body"])
        r = sh(["go", "test", "-count=1", "-run", "^" + h["run"] + "$", "."], cwd=d, env=env, timeout=900, check=False)
        (d / h["file"]).unlink()
        out["hidden"] = {"rc": r.returncode, "tail": (r.stdout + r.stderr)[-600:]}
        ok &= r.returncode == 0
    if task.get("lib_tests"):
        r = sh(["go", "test", "-count=1", *task["lib_tests"]], cwd=lib, env=env, timeout=1800, check=False)
        out["lib_tests"] = {"rc": r.returncode, "tail": (r.stdout + r.stderr)[-800:] if r.returncode else ""}
        ok &= r.returncode == 0
    out["resolved"] = bool(ok)
    return out


def reprice(rec: dict) -> dict:
    pr = ((rec.get("usage") or {}).get("transcript") or {}).get("per_request") or []
    return {t: usage_account.reprice(pr, t, rec.get("model")) for t in ("5m", "1h")}


def run_cell(task: dict, arm: str, outdir: Path, log) -> dict:
    t0 = time.monotonic()
    parent, lib, app = setup_cell(task, arm)
    setup_s = round(time.monotonic() - t0, 1)
    try:
        run_e2e._agent_env = agent_env(parent)
        orig_cmd = run_e2e._cloud_cmd

        def cmd_with_app(model, arm_, wt, prompt):
            return orig_cmd(model, arm_, wt, prompt) + ["--add-dir", str(app)]
        run_e2e._cloud_cmd = cmd_with_app
        try:
            rec = run_e2e._run_cloud(MODEL, arm, lib, {"problem_statement": task["statement"], "kind": "multirepo"})
        finally:
            run_e2e._cloud_cmd = orig_cmd
        lib_diff, app_diff = diff_stat(lib), diff_stat(app)
        sc = score(task, parent, lib, app)
        cell_dir = outdir / "diffs" / f"{task['id']}.{arm}"
        cell_dir.mkdir(parents=True, exist_ok=True)
        (cell_dir / "lib.diff").write_text(lib_diff["patch"])
        (cell_dir / "app.diff").write_text(app_diff["patch"])
        if rec.get("session_id"):
            tp = run_e2e._preserve_transcript(rec["session_id"], outdir / "transcripts" / f"{task['id']}.{arm}")
            rec["transcript_copy"] = tp
        rec.update(task=task["id"], arm=arm, setup_s=setup_s, score=sc, resolved=sc["resolved"],
                   lib_files=lib_diff["files"], app_files=app_diff["files"], repriced=reprice(rec))
        return rec
    finally:
        shutil.rmtree(parent, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True)
    ap.add_argument("--tasks", default="")
    ap.add_argument("--arms", default="native,prism_cli")
    ap.add_argument("--seed", default="multirepo-2026-11-13")
    ap.add_argument("--baseline", action="store_true", help="set up and score each task with no agent")
    ap.add_argument("--rescore", default="", help="re-score saved diffs with the current checks; the value is the reason")
    a = ap.parse_args()
    outdir = Path(a.out).resolve()
    outdir.mkdir(parents=True, exist_ok=True)
    tasks = json.loads((HERE / "tasks.json").read_text())
    if a.tasks:
        keep = set(a.tasks.split(","))
        tasks = [t for t in tasks if t["id"] in keep]
    logf = (outdir / "run.log").open("a")

    def log(msg):
        line = time.strftime("%H:%M:%S ") + msg
        print(line, flush=True)
        logf.write(line + "\n")
        logf.flush()

    if a.baseline:
        for t in tasks:
            parent, lib, app = setup_cell(t, "native")
            try:
                sc = score(t, parent, lib, app)
            finally:
                shutil.rmtree(parent, ignore_errors=True)
            vet_bad = {k: v["tail"][-300:] for k, v in sc["vet"].items() if v["rc"]}
            log(f"BASELINE {t['id']}: vet_failures={vet_bad or 'none'} doc={[d['passed'] for d in sc['doc']]} "
                f"hidden={(sc['hidden'] or {}).get('rc')} lib_tests={(sc['lib_tests'] or {}).get('rc')}")
        return

    if a.rescore:
        # Score the saved diffs again on a fresh tree, with the current task
        # checks (a check fixed after the run; the agent's work is unchanged).
        results_path = outdir / "results.json"
        results = json.loads(results_path.read_text())
        for t in tasks:
            for arm, rec in results.get(t["id"], {}).get("cells", {}).items():
                d = outdir / "diffs" / f"{t['id']}.{arm}"
                if not d.exists():
                    continue
                parent, lib, app = setup_cell(t, "native")
                try:
                    for repo, name in ((lib, "lib.diff"), (app, "app.diff")):
                        patch = (d / name).read_text()
                        if patch.strip():
                            sh(["git", "-C", str(repo), "apply", "--whitespace=nowarn", str(d / name)])
                    sc = score(t, parent, lib, app)
                finally:
                    shutil.rmtree(parent, ignore_errors=True)
                if sc["resolved"] != rec.get("resolved"):
                    rec.setdefault("rescored", []).append({"was": rec.get("resolved"), "now": sc["resolved"],
                                                           "reason": a.rescore})
                rec["score"], rec["resolved"] = sc, sc["resolved"]
                log(f"RESCORE {t['id']} {arm} resolved={sc['resolved']}")
        results_path.write_text(json.dumps(results, indent=1))
        return

    os.environ["DISABLE_PROMPT_CACHING"] = "1"
    results_path = outdir / "results.json"
    results = json.loads(results_path.read_text()) if results_path.exists() else {}
    arms = a.arms.split(",")
    for t in tasks:
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
                rec = run_cell(t, arm, outdir, log)
            except Exception as e:  # noqa: BLE001
                rec = {"task": t["id"], "arm": arm, "resolved": None, "error": f"{type(e).__name__}: {str(e)[:500]}"}
            row["cells"][arm] = rec
            results_path.write_text(json.dumps(results, indent=1))
            rp = (rec.get("repriced") or {}).get("5m") or {}
            log(f"CELL {t['id']} {arm} resolved={rec.get('resolved')} 5m=${rp.get('usd')} startup=${rp.get('startup_usd')} "
                f"requests={rp.get('requests')} raw=${rec.get('cost_usd')} app_files={len(rec.get('app_files') or [])} "
                f"err={rec.get('error') or rec.get('agent_error') or ''}")
        cells = row["cells"]
        if all(arm in cells for arm in arms):
            parts = []
            for arm in arms:
                rp = (cells[arm].get("repriced") or {}).get("5m") or {}
                task_usd = (rp.get("usd") or 0) - (rp.get("startup_usd") or 0)
                parts.append(f"{arm}: resolved={cells[arm].get('resolved')} task5m=${task_usd:.3f} req={rp.get('requests')}")
            log(f"PAIR {t['id']}: " + " | ".join(parts))
    log("ALLDONE")


if __name__ == "__main__":
    main()
