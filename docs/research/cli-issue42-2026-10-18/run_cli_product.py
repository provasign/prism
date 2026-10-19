#!/usr/bin/env python3
"""Run the prism_cli arm of the no-cache issue harness with the PRODUCT's CLI setup.

The shared research harness' prism_cli arm runs `prism init`, deletes .mcp.json and
swaps in a hand-written CLI steering block. This overlay (in this process only; the
research repo is not edited) instead runs `prism init --interface cli`, so the arm
tests what `prism init` really writes. It also stops on a spend cap.
Pass normal run_ttl.py arguments after this script's name.
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path

HARNESS = Path(os.environ.get("RESEARCH_HARNESS",
               str(Path("~/Projects/provasign/research/harness").expanduser()))).resolve()
SPEND_CAP_USD = float(os.environ.get("CLI_BENCH_SPEND_CAP_USD", "22"))
for path in (HARNESS / "runners", HARNESS / "aggregate", HARNESS / "build",
             HARNESS / "scoring", HARNESS):
    sys.path.insert(0, str(path))

import types  # noqa: E402

import run_e2e  # noqa: E402

# run_ttl's no-cache pair report reads arms[1], so a single-arm run raised
# IndexError on its first task. Load run_ttl with that one lookup made optional;
# the research repo's file is not edited.
_src = (HARNESS / "runners" / "run_ttl.py").read_text()
_old = 'row["cells"].get(f"{arms[1]}/nocache")'
assert _old in _src, "run_ttl.py changed; revisit this patch"
run_ttl = types.ModuleType("run_ttl")
run_ttl.__file__ = str(HARNESS / "runners" / "run_ttl.py")
exec(compile(_src.replace(_old, 'row["cells"].get(f"{arms[1]}/nocache") if len(arms) > 1 else None'), run_ttl.__file__, "exec"), run_ttl.__dict__)

# A/B: arm prism_cli_b is prism_cli on a second binary (PRISM_CLI_B_BIN).
CLI_ARMS = ("prism_cli", "prism_cli_b")
if os.environ.get("PRISM_CLI_B_BIN"):
    run_e2e.ARMS["prism_cli_b"] = dict(run_e2e.ARMS["prism_cli"])
    run_e2e.PRISM_BIN_FOR_ARM["prism_cli_b"] = os.environ["PRISM_CLI_B_BIN"]

original_index = run_e2e._index_graph
original_env = run_e2e._agent_env
original_cloud = run_e2e._run_cloud
current_arm = None


def agent_env(wt: Path, task):
    # run_e2e puts the prism_cli binary on the agent PATH only for the arm
    # named prism_cli; do the same for prism_cli_b with its own binary.
    env, runtimes = original_env(wt, task)
    if current_arm == "prism_cli_b":
        d = str(run_e2e._prism_cli_bin_dir("prism_cli_b"))
        env["PATH"] = d + ":" + env.get("PATH", "")
        if env.get("E2E_PINNED_PATH"):
            env["E2E_PINNED_PATH"] = d + ":" + env["E2E_PINNED_PATH"]
    return env, runtimes


def run_cloud(model, arm, wt, task):
    global current_arm
    current_arm = arm
    try:
        return original_cloud(model, arm, wt, task)
    finally:
        current_arm = None


run_e2e._agent_env = agent_env
run_e2e._run_cloud = run_cloud


def index_graph(wt: Path, arm: str, task=None):
    if arm not in CLI_ARMS:
        return original_index(wt, arm, task)
    prism = run_e2e._prism_bin(arm)
    r = subprocess.run([prism, "init", "--harness", "claude", "--interface", "cli", "--yes", str(wt)],
                       capture_output=True, text=True, timeout=300)
    md = wt / "CLAUDE.md"
    text = md.read_text() if md.exists() else ""
    if r.returncode or "no MCP server is registered" not in text:
        raise RuntimeError(f"prism init --interface cli failed in {wt} (rc={r.returncode}): "
                           f"{r.stdout[-300:]} {r.stderr[-300:]}")
    # The agent runs with --strict-mcp-config and no --mcp-config in this arm, so
    # a repo's own .mcp.json (zod's checkout carries one) loads nothing. What must
    # not survive init is a Prism server entry.
    mcp_json = wt / ".mcp.json"
    if mcp_json.exists() and "prism" in json.loads(mcp_json.read_text() or "{}").get("mcpServers", {}):
        raise RuntimeError(f"CLI arm worktree {wt} still registers Prism in .mcp.json")
    env, _ = run_e2e._agent_env(wt, task)
    r2 = subprocess.run([prism, "index", str(wt)], capture_output=True, text=True,
                        timeout=3600, env=env)
    if r2.returncode != 0:
        raise RuntimeError(f"prism index rc={r2.returncode} for {task['instance_id']} "
                           f"(compiler-backed analysis required): {r2.stderr[-600:]}")


run_e2e._index_graph = index_graph

original_run_with_retry = run_ttl.run_with_retry


def budgeted_run_with_retry(task, arm, model, log):
    out_flag = sys.argv.index("--out-dir")
    results_path = Path(sys.argv[out_flag + 1]) / "results.json"
    prior = json.loads(results_path.read_text()) if results_path.exists() else {}
    spent = sum((cell.get("cost_usd") or 0) for key, row in prior.items()
                if not key.startswith("_") for cell in row.get("cells", {}).values())
    if spent >= SPEND_CAP_USD:
        log(f"STOP: observed uncached spend ${spent:.3f} reached cap ${SPEND_CAP_USD:.2f}")
        raise SystemExit(3)
    return original_run_with_retry(task, arm, model, log)


run_ttl.run_with_retry = budgeted_run_with_retry

if __name__ == "__main__":
    run_ttl.main()
