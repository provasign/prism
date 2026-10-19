#!/usr/bin/env python3
"""A/B of two prism_cli binaries (prism_cli = baseline, prism_cli_b = candidate)."""
import json, re, statistics, sys
from pathlib import Path

RES = Path("~/Projects/provasign/research/harness/results").expanduser()
CALL = re.compile(r"(?:^|[\s;&|(`])prism\s+(lookup|search|read|query|change-impact|verify)\b")
LOOK = re.compile(r"(?:^|[\s;&|(`])(grep|rg|find)\s")


def audit(path):
    uses, results = {}, {}
    order = []
    for line in open(RES.parent / path):
        try:
            d = json.loads(line)
        except ValueError:
            continue
        c = (d.get("message") or {}).get("content")
        if not isinstance(c, list):
            continue
        for b in c:
            if b.get("type") == "tool_use":
                uses[b["id"]] = b
                order.append(b["id"])
            elif b.get("type") == "tool_result":
                t = b.get("content")
                if isinstance(t, list):
                    t = "".join(x.get("text", "") for x in t if isinstance(x, dict))
                results[b.get("tool_use_id")] = t or ""
    prism = impact = after_impact_lookups = 0
    impact_bytes = []
    seen_impact = False
    for i in order:
        b = uses[i]
        if b["name"] != "Bash":
            if seen_impact and b["name"] in ("Grep", "Glob"):
                after_impact_lookups += 1
            continue
        cmd = b["input"].get("command", "")
        calls = [m.group(1) for m in CALL.finditer(cmd)]
        prism += len(calls)
        if "change-impact" in calls:
            impact += 1
            seen_impact = True
            impact_bytes.append(len(results.get(i, "")))
        elif seen_impact and (LOOK.search(cmd) or set(calls) & {"search", "query"}):
            after_impact_lookups += 1
    return prism, impact, impact_bytes, after_impact_lookups


def main(d):
    r = json.load(open(RES / d / "results.json"))
    rows = []
    for task, row in r.items():
        if task.startswith("_"):
            continue
        a, b = row["cells"].get("prism_cli/nocache"), row["cells"].get("prism_cli_b/nocache")
        if not (a and b) or a.get("error") or b.get("error"):
            continue
        def tc(c):
            rp = c["repriced"]["5m"]
            return rp["usd"] - rp["startup_usd"]
        rows.append((task, a, b, tc(a), tc(b), audit(a["transcript"]), audit(b["transcript"])))
    print(f"# prism_cli A/B, {len(rows)} complete pairs (A = baseline 52e687e, B = candidate 48f7a8e)\n")
    print("| task | A | B | task $ A | task $ B | B/A | prism calls A/B | impact answer bytes A / B | lookups after impact A/B |")
    print("|---|---|---|---:|---:|---:|---|---|---|")
    for task, a, b, ta, tb, xa, xb in rows:
        s = lambda c: "solved" if c["resolved"] else "FAIL"
        print(f"| {task} | {s(a)} | {s(b)} | {ta:.4f} | {tb:.4f} | {tb/ta:.2f} | {xa[0]}/{xb[0]} | {xa[2]} / {xb[2]} | {xa[3]}/{xb[3]} |")
    ta, tb = sum(x[3] for x in rows), sum(x[4] for x in rows)
    print(f"\nresolved A {sum(x[1]['resolved'] for x in rows)}, B {sum(x[2]['resolved'] for x in rows)}")
    print(f"task cost @5m total A ${ta:.4f}, B ${tb:.4f}, B/A {tb/ta:.3f}; median per-pair B/A {statistics.median(x[4]/x[3] for x in rows):.3f}")
    ia = [n for x in rows for n in x[5][2]]; ib = [n for x in rows for n in x[6][2]]
    print(f"change-impact answers: A {len(ia)} totalling {sum(ia)} B, B {len(ib)} totalling {sum(ib)} B")
    print(f"lookups after impact: A {sum(x[5][3] for x in rows)}, B {sum(x[6][3] for x in rows)}")
    lost = [x[0] for x in rows if x[1]["resolved"] and not x[2]["resolved"]]
    print(f"A solved / B failed: {lost or 'none'}")


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "cli-ab-generic14-2026-10-18")
