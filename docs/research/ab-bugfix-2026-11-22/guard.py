#!/usr/bin/env python3
"""Run the bug-fix A/B one task at a time with a disk guard and the stop rules.

Arms: prism_cli = baseline build (multi-repo before BUG-01: prism 06464fa,
grove 63c042a6); prism_cli_b = candidate (prism c043b79, grove c5aba584).
Sonnet 5.5, no prompt cache, repriced at 5m. One run_cli_product.py call per
task, all writing to one out-dir, so its spend cap counts every cell.

Before each task: stop if the data volume has less than MIN_FREE_GIB free.
After each task: delete leftover cell checkouts, log cache sizes, check
  - any cell error                         -> STOP
  - baseline resolved, candidate did not   -> STOP (resolve loss)
  - after >= 10 pairs, candidate/baseline task cost > 1.25 -> STOP
"""
from __future__ import annotations

import glob
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

HOME = Path.home()
HARNESS = HOME / "Projects/provasign/research/harness"
RUNNER = HOME / "Projects/provasign/prism/docs/research/cli-issue42-2026-10-18/run_cli_product.py"
BASE_BIN = HOME / "Projects/provasign-mr/bin/prism-ab-base"
CAND_BIN = HOME / "Projects/provasign-mr/bin/prism-ab-cand"
OUT = HARNESS / "results" / os.environ.get("AB_OUT", "ab-bugfix-2026-11-22")
CAP_USD = os.environ.get("AB_CAP_USD", "60")
MIN_FREE_GIB = float(os.environ.get("AB_MIN_FREE_GIB", "500"))
WATCH = [HOME / ".gocache-eval", HOME / ".pnpm-eval-store", HOME / ".m2-eval",
         HOME / "Library/Containers/com.docker.docker"]
LANG_ORDER = {"go": 0, "ts": 1, "js": 1, "py": 2, "java": 3}


def log(msg):
    line = f"[{time.strftime('%H:%M:%S')}] {msg}"
    print(line, flush=True)
    with open(OUT / "guard.log", "a") as f:
        f.write(line + "\n")


def free_gib():
    return shutil.disk_usage("/System/Volumes/Data").free / 2**30


def du_gib(p: Path):
    if not p.exists():
        return 0.0
    r = subprocess.run(["du", "-sk", str(p)], capture_output=True, text=True)
    try:
        return int(r.stdout.split()[0]) / 2**20
    except Exception:
        return -1.0


def cleanup_cells():
    tmp = tempfile.gettempdir()
    for d in glob.glob(os.path.join(tmp, "e2e-run-*")) + glob.glob(os.path.join(tmp, "venvsrc-*")):
        shutil.rmtree(d, ignore_errors=True)


def cell(row, arm):
    return (row.get("cells") or {}).get(f"{arm}/nocache") or {}


def limit_hit(c):
    path = c.get("transcript")
    if not path or not os.path.exists(path):
        return False
    text = open(path, errors="replace").read()
    return "hit your" in text and "limit" in text and "resets" in text


def task_cost(c):
    r = ((c.get("repriced") or {}).get("5m") or {})
    if r.get("status") != "ok":
        return None
    return r["usd"] - r.get("startup_usd", 0)


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    ids = json.load(open(HARNESS / "results/issue42-manifest.json"))
    tasks = [json.load(open(HARNESS / "tasks/e2e" / f"{i}.json")) for i in ids]
    tasks.sort(key=lambda t: (LANG_ORDER.get(t.get("lang") or "py", 9), "jackson" in t["repo"], t["instance_id"]))
    env = dict(os.environ, PRISM_INIT_BIN=str(BASE_BIN), PRISM_CLI_B_BIN=str(CAND_BIN),
               CLI_BENCH_SPEND_CAP_USD=CAP_USD)
    pairs = []
    for t in tasks:
        tid = t["instance_id"]
        results = OUT / "results.json"
        prior = json.loads(results.read_text()) if results.exists() else {}
        row = prior.get(tid) or {}
        if cell(row, "prism_cli") and cell(row, "prism_cli_b"):
            pairs.append(tid)
            continue
        free = free_gib()
        if free < MIN_FREE_GIB:
            log(f"STOP disk: {free:.0f} GiB free < {MIN_FREE_GIB:.0f}")
            return 4
        before = {p.name: du_gib(p) for p in WATCH}
        man = OUT / "manifest-one.json"
        man.write_text(json.dumps([tid]))
        t0 = time.time()
        r = subprocess.run([sys.executable, str(RUNNER), "--manifest", str(man), "--out-dir", str(OUT),
                            "--arms", "prism_cli,prism_cli_b", "--no-cache", "--order-seed", "ab-bugfix-2026-11-22"],
                           cwd=HARNESS, env=env, stdout=open(OUT / "run.log", "a"), stderr=subprocess.STDOUT)
        cleanup_cells()
        after = {p.name: du_gib(p) for p in WATCH}
        grew = ", ".join(f"{k} {before[k]:.1f}->{after[k]:.1f}G" for k in after if after[k] - before[k] > 0.05)
        log(f"DISK {tid}: free {free_gib():.0f} GiB; {grew or 'caches unchanged'}; {time.time() - t0:.0f}s rc={r.returncode}")
        if r.returncode == 3:
            log("STOP spend cap reached")
            return 3
        row = json.loads(results.read_text()).get(tid) or {}
        b, c = cell(row, "prism_cli"), cell(row, "prism_cli_b")
        if not b and not c and f"(skipped invalid) {tid}" in (OUT / "run.log").read_text():
            # The harness excludes tasks no agent can pass (hidden tests use
            # names only the gold fix defines); the Oct 20 bed did too.
            log(f"SKIP {tid}: harness marks it invalid")
            continue
        if not b or not c or b.get("error") or c.get("error"):
            log(f"STOP cell error {tid}: base={b.get('error')!r} cand={c.get('error')!r} rc={r.returncode}")
            return 5
        # A usage limit ends a cell early and scores it as a failure; it is
        # not a task result (pr6044's baseline cell, 2026-11-22).
        limited = [a for a, x in (("base", b), ("cand", c)) if limit_hit(x)]
        if limited:
            log(f"STOP usage limit hit in {tid} ({', '.join(limited)}); remove this row and resume after the reset")
            return 8
        bt, ct = task_cost(b), task_cost(c)
        pairs.append(tid)
        log(f"PAIR {len(pairs)} {tid}: base resolved={b.get('resolved')} task=${bt:.3f} | "
            f"cand resolved={c.get('resolved')} task=${ct:.3f} | requests {b.get('api_calls')} -> {c.get('api_calls')}")
        if b.get("resolved") and not c.get("resolved"):
            log(f"STOP resolve loss on {tid}")
            return 6
        if len(pairs) >= 10:
            data = json.loads(results.read_text())
            sb = sum(task_cost(cell(data[p], "prism_cli")) or 0 for p in pairs if p in data)
            sc = sum(task_cost(cell(data[p], "prism_cli_b")) or 0 for p in pairs if p in data)
            if sb and sc / sb > 1.25:
                log(f"STOP cost ratio {sc / sb:.3f} > 1.25 after {len(pairs)} pairs")
                return 7
    log("ALLDONE")
    return 0


if __name__ == "__main__":
    sys.exit(main())
