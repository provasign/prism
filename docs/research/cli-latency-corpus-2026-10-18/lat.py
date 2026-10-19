import json, os, re, shutil, statistics, subprocess, sys, time, platform
SP = os.path.dirname(os.path.abspath(__file__)); BIN = SP + "/prism"
CORP = os.path.expanduser("~/gvg-corpus/e2e-2026")
REPOS = [  # (dir, symbol, text term)
 ("pallets__click", "Command.invoke", "invoke"),
 ("pydantic__pydantic", "BaseModel.model_dump", "model_dump"),
 ("cli__cli", "NewCmdRoot", "NewCmdRoot"),
 ("django__django", "QuerySet.filter", "filter"),
]
N = int(os.environ.get("N", "5"))
env = dict(os.environ, GOPROXY="off", GOTOOLCHAIN="local")
def run(args, cwd, timeout=1800):
    t = time.perf_counter(); p = subprocess.run([BIN, *args], cwd=cwd, capture_output=True, text=True, errors="replace", env=env, timeout=timeout)
    return time.perf_counter() - t, p
def med(x): return statistics.median(x) if x else float("nan")
out = {}
for name, sym, term in REPOS:
    src = f"{CORP}/{name}"
    if not os.path.isdir(src): print("skip", name); continue
    dst = f"{SP}/repos/{name}"
    if not os.path.isdir(dst):
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        subprocess.run(["rsync", "-a", "--exclude", ".git", "--exclude", ".grove", src + "/", dst + "/"], check=True)
    nfiles = int(subprocess.run(f"find {dst} -type f | wc -l", shell=True, capture_output=True, text=True).stdout)
    t_idx, p = run(["index", dst], dst)
    idx_mb = int(subprocess.run(["du", "-sk", dst + "/.grove"], capture_output=True, text=True).stdout.split()[0]) / 1024
    r = {"files": nfiles, "index_s": round(t_idx, 1), "index_mb": round(idx_mb), "index_rc": p.returncode}
    # resolve a real file for read
    _, lk = run(["lookup", sym, "--format", "text"], dst)
    m = re.search(r"([\w./-]+\.\w+):(\d+)", lk.stdout)
    if not m or len(lk.stdout) < 50:
        r["error"] = "symbol not found: " + lk.stdout[:100] + lk.stderr[:100]; out[name] = r; print(name, r); continue
    f, line = m.group(1), int(m.group(2))
    ops = {
     "lookup": (["lookup", sym, "--format", "text"], ("lookup", {"name": [sym]})),
     "search": (["search", term, "--scope", "text", "--limit", "20", "--format", "text"], ("search", {"terms": [term], "scope": "text", "max_results": 20})),
     "change_impact": (["change-impact", sym, "--format", "text"], ("change_impact", {"name": [sym]})),
     "read": (["read", f, "--from", str(max(1, line - 5)), "--to", str(line + 60), "--format", "text"], ("read", {"file": f, "from": max(1, line - 5), "to": line + 60})),
     "query": (["query", "--terms", sym.split(".")[-1], "--format", "text"], ("query", {"terms": [sym.split(".")[-1]]})),
    }
    cli = {k: [] for k in ops}; mcp = {k: [] for k in ops}; size = {}
    for k, (cargs, _) in ops.items():
        run(cargs, dst)  # warm page cache, discard
        for _ in range(N):
            t, p = run(cargs, dst); cli[k].append(t * 1000); size[k] = len(p.stdout)
    # resident MCP
    t0 = time.perf_counter()
    mp = subprocess.Popen([BIN, "mcp", "--compact", dst], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, errors="replace", cwd=dst, env=env)
    def rpc(i, method, params=None):
        mp.stdin.write(json.dumps({"jsonrpc": "2.0", "id": i, "method": method, "params": params or {}}) + "\n"); mp.stdin.flush()
        return mp.stdout.readline()
    rpc(1, "initialize", {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "lat", "version": "0"}})
    mp.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n"); mp.stdin.flush()
    rpc(2, "tools/call", {"name": "prism", "arguments": {"op": "search", "args": {"terms": ["zzNoSuchTermQq"], "scope": "text"}}})
    r["mcp_ready_s"] = round(time.perf_counter() - t0, 2)
    i = 10
    for k, (_, (op, a)) in ops.items():
        for rep in range(N):
            aa = json.loads(json.dumps(a))
            if op == "read": aa["from"] += rep  # avoid session-pointer replies
            if op == "search": aa["terms"] = [term + ("" if rep == 0 else "")]; aa["max_results"] = 20 - rep
            if op == "lookup" and rep: aa["name"] = [sym.split(".")[-1] + ("" if rep == 0 else "")]
            t = time.perf_counter(); rpc(i, "tools/call", {"name": "prism", "arguments": {"op": op, "args": aa}}); mcp[k].append((time.perf_counter() - t) * 1000); i += 1
    mp.kill()
    r["ops"] = {k: {"cli_ms": round(med(cli[k])), "cli_max": round(max(cli[k])), "mcp_ms": round(med(mcp[k])), "bytes": size[k]} for k in ops}
    out[name] = r
    print(name, json.dumps(r), flush=True)
json.dump({"machine": platform.platform(), "cpus": os.cpu_count(), "loadavg": os.getloadavg(), "N": N, "results": out}, open(SP + "/lat_results.json", "w"), indent=1)
