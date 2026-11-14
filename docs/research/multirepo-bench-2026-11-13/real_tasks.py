#!/usr/bin/env python3
"""Real cross-repository tasks from open-source history, validated like SWE-bench.

A task is one change that landed as two commits in two repositories after the
model's training cutoff (June 2026): a library commit (boxo, grove, ...) and the
consumer commit that needed it (kubo, prism, ...). The cell checks out the
library at the parent of its change and the consumer at the parent of its
change, side by side under a go.work, and the agent gets the problem statement
the consumer's PR gave. Scoring applies the test files both gold commits added
or changed and runs the tests they touch: FAIL_TO_PASS are tests that fail at
base and pass at gold (validated here), PASS_TO_PASS tests named in those files
that pass in both. Resolved = every FAIL_TO_PASS and PASS_TO_PASS test passes.

  real_tasks.py validate [--tasks id,...]   writes real_tasks.validated.json
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
CORPUS = Path("~/.cache/prism-research/multirepo-corpus").expanduser()
CELLS = Path("~/.cache/prism-research/mrcells").expanduser()
GO_BIN = "/usr/local/go/bin"


def sh(cmd, cwd=None, env=None, timeout=1800, check=True):
    r = subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout)
    if check and r.returncode != 0:
        raise RuntimeError(f"{' '.join(map(str, cmd))} (rc={r.returncode}): {(r.stdout + r.stderr)[-800:]}")
    return r


def is_test_path(p: str) -> bool:
    """Test files, test data, and test-helper packages (a directory named like
    ipnstest or testutil holding plain .go files only tests import: boxo's
    internal/ipnstest came with the gold tests that use it)."""
    d = os.path.basename(os.path.dirname(p))
    return (p.endswith("_test.go") or "/testdata/" in p or p.startswith("testdata/") or p.startswith("test/cli/")
            or d.endswith("test") or d.endswith("testutil"))


def go_env(parent: Path) -> dict:
    env = dict(os.environ)
    env["PATH"] = GO_BIN + ":" + env.get("PATH", "")
    env.pop("GOFLAGS", None)
    # kubo requires go 1.26.5: let the go command fetch the toolchain a go.mod asks for
    env["GOTOOLCHAIN"] = "auto"
    env["PRISM_STATE_DIR"] = str(parent / ".prism-state")
    env["PRISM_CACHE_DIR"] = str(parent / ".prism-cache")
    return env


def test_patch(repo: str, commits: list[str]) -> str:
    """The test-file part of the gold commits, as one patch against the base."""
    out = []
    for c in commits:
        files = [f for f in sh(["git", "-C", str(CORPUS / repo), "show", "--format=", "--name-only", c]).stdout.split()
                 if is_test_path(f)]
        if files:
            out.append(sh(["git", "-C", str(CORPUS / repo), "show", "--format=", "--binary", c, "--", *files]).stdout)
    return "".join(out)


TEST_FUNC = re.compile(r"^\+func (Test\w+)\(", re.M)
HUNK_FUNC = re.compile(r"^@@[^@]*@@ func (Test\w+)\(", re.M)


def test_targets(patch: str) -> dict[str, set[str]]:
    """package dir -> test names the patch adds or edits."""
    targets: dict[str, set[str]] = {}
    for chunk in re.split(r"(?m)^diff --git ", patch):
        m = re.match(r"a/(\S+) b/(\S+)", chunk)
        if not m or not m.group(2).endswith("_test.go"):
            continue
        names = set(TEST_FUNC.findall(chunk)) | set(HUNK_FUNC.findall(chunk))
        if names:
            pkg = "./" + (os.path.dirname(m.group(2)) or ".")
            targets.setdefault(pkg, set()).update(names)
    return targets


def clone_at(repo: str, sha: str, dest: Path):
    sh(["git", "clone", "--local", "--no-checkout", "-q", str(CORPUS / repo), str(dest)])
    sh(["git", "-C", str(dest), "checkout", "--detach", "-f", "-q", sha])
    for ref in sh(["git", "-C", str(dest), "for-each-ref", "--format=%(refname)"]).stdout.split():
        sh(["git", "-C", str(dest), "update-ref", "-d", ref])
    sh(["git", "-C", str(dest), "remote", "remove", "origin"], check=False)
    # Commits after base would hand the agent the answer (git log --all):
    # with every ref and the remote gone, nothing past base is reachable.
    for key, value in (("gc.auto", "0"), ("maintenance.auto", "false")):
        sh(["git", "-C", str(dest), "config", key, value])


def run_tests(task: dict, side: str, repo_dir: Path, targets: dict[str, set[str]], env: dict) -> dict[str, str]:
    """test name (pkg:Name) -> pass | fail."""
    res = {}
    if task.get("build_bin") and side == "app" and any(p.startswith("./test/cli") for p in targets):
        r = sh(["go", "build", "-o", task["build_bin"], task["build_pkg"]], cwd=repo_dir, env=env, timeout=1800, check=False)
        if r.returncode:
            for pkg, names in targets.items():
                if pkg.startswith("./test/cli"):
                    for n in names:
                        res[f"{side}:{pkg}:{n}"] = "fail"
            targets = {p: n for p, n in targets.items() if not p.startswith("./test/cli")}
    for pkg, names in sorted(targets.items()):
        pattern = "^(" + "|".join(sorted(names)) + ")$"
        r = sh(["go", "test", "-count=1", "-json", "-run", pattern, pkg], cwd=repo_dir, env=env, timeout=2400, check=False)
        seen = {}
        for line in r.stdout.splitlines():
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            t = ev.get("Test")
            if t and "/" not in t and ev.get("Action") in ("pass", "fail", "skip"):
                seen[t] = ev["Action"]
        for n in names:
            res[f"{side}:{pkg}:{n}"] = "pass" if seen.get(n) == "pass" else "fail"
    return res


def setup(task: dict, which: str, arm: str = "validate") -> tuple[Path, Path, Path]:
    CELLS.mkdir(parents=True, exist_ok=True)
    parent = Path(tempfile.mkdtemp(prefix=f"{task['id']}-{arm}-", dir=CELLS)).resolve()
    lib, app = parent / task["lib"], parent / task["app"]
    clone_at(task["lib_repo"], task["lib_base"] if which == "base" else task["lib_gold"], lib)
    clone_at(task["app_repo"], task["app_base"] if which == "base" else task["app_gold"], app)
    sh(["go", "work", "init", f"./{task['lib']}", f"./{task['app']}"], cwd=parent, env=go_env(parent))
    return parent, lib, app


def apply_tests(task: dict, lib: Path, app: Path):
    for d, repo, commits in ((lib, task["lib_repo"], task["lib_test_commits"]), (app, task["app_repo"], task["app_test_commits"])):
        patch = test_patch(repo, commits)
        if patch.strip():
            p = d.parent / f".{d.name}.tests.patch"
            p.write_text(patch)
            sh(["git", "-C", str(d), "apply", "--whitespace=nowarn", str(p)])


def score_tree(task: dict, parent: Path, lib: Path, app: Path, select: dict | None = None) -> dict[str, str]:
    env = go_env(parent)
    lt = test_targets(test_patch(task["lib_repo"], task["lib_test_commits"]))
    at = test_targets(test_patch(task["app_repo"], task["app_test_commits"]))
    if select is not None:
        keep = set(select)
        lt = {p: {n for n in ns if f"lib:{p}:{n}" in keep} for p, ns in lt.items()}
        at = {p: {n for n in ns if f"app:{p}:{n}" in keep} for p, ns in at.items()}
        lt = {p: ns for p, ns in lt.items() if ns}
        at = {p: ns for p, ns in at.items() if ns}
    out = run_tests(task, "lib", lib, lt, env)
    out.update(run_tests(task, "app", app, at, env))
    return out


def validate(task: dict) -> dict:
    parent, lib, app = setup(task, "base")
    try:
        apply_tests(task, lib, app)
        base = score_tree(task, parent, lib, app)
    finally:
        shutil.rmtree(parent, ignore_errors=True)
    parent, lib, app = setup(task, "gold")
    try:
        gold = score_tree(task, parent, lib, app)
    finally:
        shutil.rmtree(parent, ignore_errors=True)
    f2p = sorted(t for t in gold if gold[t] == "pass" and base.get(t) == "fail")
    p2p = sorted(t for t in gold if gold[t] == "pass" and base.get(t) == "pass")
    broken = sorted(t for t in gold if gold[t] != "pass")
    return {"FAIL_TO_PASS": f2p, "PASS_TO_PASS": p2p, "gold_failing": broken}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("cmd", choices=["validate"])
    ap.add_argument("--tasks", default="")
    a = ap.parse_args()
    tasks = json.loads((HERE / "real_tasks.json").read_text())
    if a.tasks:
        keep = set(a.tasks.split(","))
        tasks = [t for t in tasks if t["id"] in keep]
    out_path = HERE / "real_tasks.validated.json"
    done = json.loads(out_path.read_text()) if out_path.exists() else {}
    for t in tasks:
        try:
            v = validate(t)
        except Exception as e:  # noqa: BLE001
            v = {"error": f"{type(e).__name__}: {str(e)[:600]}"}
        done[t["id"]] = v
        out_path.write_text(json.dumps(done, indent=1))
        print(t["id"], {k: (len(x) if isinstance(x, list) else x) for k, x in v.items()}, flush=True)


if __name__ == "__main__":
    main()
