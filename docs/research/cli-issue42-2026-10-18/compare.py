#!/usr/bin/env python3
"""Compare the CLI arm with the earlier native and MCP no-cache runs on the same
issue bed: resolved, repriced cost, API calls, and how often Prism was really called.

Usage: compare.py CLI_RESULTS_DIR [NATIVE_MCP_DIR ...]   (dirs under research/harness/results)
"""
import json, re, statistics, sys
from pathlib import Path

RES = Path("~/Projects/provasign/research/harness/results").expanduser()
INFO = r"lookup|search|read|query|change-impact|verify|rename-plan|missing-implementations|dead-code"
CLI_CALL = re.compile(r"(?:^|[\s;&|(`])prism\s+(" + INFO + r")\b")


def prism_calls(transcript, arm):
    """(info calls, change-impact calls, other-MCP-or-CLI notes) read from the transcript."""
    n = ci = mcp = 0
    p = RES.parent / transcript if not Path(transcript).is_absolute() else Path(transcript)
    if not p.exists():
        return None
    for line in open(p):
        try:
            d = json.loads(line)
        except ValueError:
            continue
        content = (d.get("message") or {}).get("content")
        if not isinstance(content, list):
            continue
        for b in content:
            if b.get("type") != "tool_use":
                continue
            if b["name"].startswith("mcp__prism"):
                mcp += 1
                n += 1
                if (b.get("input") or {}).get("op") == "change_impact":
                    ci += 1
            elif b["name"] == "Bash":
                for m in CLI_CALL.finditer(b["input"].get("command", "")):
                    n += 1
                    ci += m.group(1) == "change-impact"
    return n, ci, mcp


def load(dirname, arm):
    r = json.load(open(RES / dirname / "results.json"))
    out = {}
    for task, row in r.items():
        if task.startswith("_"):
            continue
        c = (row.get("cells") or {}).get(f"{arm}/nocache")
        if not c or c.get("error"):
            continue
        rp = (c.get("repriced") or {}).get("5m") or {}
        total = rp.get("usd")
        startup = rp.get("startup_usd")
        calls = prism_calls(c.get("transcript", ""), arm) if c.get("transcript") else None
        out[task] = dict(resolved=bool(c["resolved"]), total=total,
                         task_cost=(total - startup) if total is not None and startup is not None else None,
                         startup=startup, api=c.get("api_calls"), observed=c.get("cost_usd"),
                         prism=calls[0] if calls else None, impact=calls[1] if calls else None,
                         mcp=calls[2] if calls else None)
    return out


def med(xs):
    xs = [x for x in xs if x is not None]
    return statistics.median(xs) if xs else float("nan")


def mean(xs):
    xs = [x for x in xs if x is not None]
    return statistics.mean(xs) if xs else float("nan")


def main():
    cli_dir = sys.argv[1]
    base = sys.argv[2:] or ["nocache-issue42-2026-10-17", "nocache-issue42-rep2-2026-10-17"]
    sets = {"native#1": load(base[0], "native"), "mcp#1": load(base[0], "prism_init"),
            "cli": load(cli_dir, "prism_cli")}
    if len(base) > 1:
        sets["native#2"] = load(base[1], "native")
        sets["mcp#2"] = load(base[1], "prism_init")
    tasks = sorted(set(sets["cli"]))
    print(f"# CLI vs native vs MCP, {len(tasks)} tasks with a finished CLI cell\n")
    print("| arm | tasks | resolved | mean total @5m | mean task-only @5m | mean startup | mean API calls | tasks with >=1 prism call | mean prism calls | change-impact calls |")
    print("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
    for name, s in sets.items():
        t = [k for k in tasks if k in s]
        pc = [s[k]["prism"] for k in t if s[k]["prism"] is not None]
        print(f"| {name} | {len(t)} | {sum(s[k]['resolved'] for k in t)} | ${mean([s[k]['total'] for k in t]):.4f} | "
              f"${mean([s[k]['task_cost'] for k in t]):.4f} | ${mean([s[k]['startup'] for k in t]):.4f} | "
              f"{mean([s[k]['api'] for k in t]):.1f} | {sum(1 for x in pc if x > 0)}/{len(pc)} | "
              f"{mean(pc):.2f} | {sum(s[k]['impact'] or 0 for k in t)} |")
    print("\n## Per task\n")
    print("| task | native#1 | mcp#1 | cli | cli/mcp#1 task cost | prism calls mcp#1 -> cli | impact calls mcp -> cli |")
    print("|---|---|---|---|---:|---|---|")
    zero = []
    for k in tasks:
        n, m, c = sets["native#1"].get(k), sets["mcp#1"].get(k), sets["cli"][k]
        def cell(x): return "-" if not x else ("solved" if x["resolved"] else "FAIL")
        ratio = (c["task_cost"] / m["task_cost"]) if c["task_cost"] and m and m["task_cost"] else float("nan")
        print(f"| {k} | {cell(n)} | {cell(m)} | {cell(c)} | {ratio:.2f} | {m['prism'] if m else '-'} -> {c['prism']} | {m['impact'] if m else '-'} -> {c['impact']} |")
        if c["prism"] == 0:
            zero.append(k)
    print(f"\nCLI tasks with ZERO prism calls: {len(zero)}: {', '.join(zero) if zero else 'none'}")
    both = [k for k in tasks if k in sets["mcp#1"]]
    lost = [k for k in both if sets["mcp#1"][k]["resolved"] and not sets["cli"][k]["resolved"]]
    won = [k for k in both if not sets["mcp#1"][k]["resolved"] and sets["cli"][k]["resolved"]]
    print(f"MCP#1 solved / CLI failed: {len(lost)} {lost}\nMCP#1 failed / CLI solved: {len(won)} {won}")


if __name__ == "__main__":
    main()
