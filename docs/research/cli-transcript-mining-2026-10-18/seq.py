"""Unified call sequences for Claude (.jsonl) and Codex (events.jsonl) CLI transcripts."""
import json, re, glob, os, sys
RES=os.path.expanduser("~/Projects/provasign/research/harness/results")
DOC=os.path.expanduser("~/Projects/provasign/prism/docs/research")
PRISM=re.compile(r"(?:^|[\s;&|(`])prism\s+(lookup|search|read|query|change-impact|verify|index|status|rename-plan)\b([^;|&\n]*)")
def classify(cmd):
    c=cmd.strip()
    if re.search(r"(sed -i|perl -(p)?i|python3 - <<|cat > |cat <<|apply_patch|applypatch)",c): return "edit"
    if PRISM.search(c): return "prism"
    if re.search(r"(go test|go build|go vet|mvn|mvnw|pytest|npm (test|run)|pnpm|vitest|bun (test|run)|cargo|make\b|gradle|tsc\b|node .*test)",c): return "test"
    if re.search(r"(?:^|[\s;&|(])(grep|rg|ag)\s",c): return "grep"
    if re.search(r"(?:^|[\s;&|(])(sed -n|cat|head|tail|awk|nl)\s",c): return "read"
    if re.search(r"(?:^|[\s;&|(])(find|fd|ls|tree)\s",c): return "find"
    if re.search(r"(?:^|[\s;&|(])git\s",c): return "git"
    return "other"
def claude(path):
    uses={}; order=[]; intents={}; results={}
    for l in open(path):
        try:d=json.loads(l)
        except: continue
        m=d.get("message") or {}; c=m.get("content")
        if not isinstance(c,list): continue
        text=""
        for b in c:
            t=b.get("type")
            if t in("text","thinking"): text+=(b.get("text") or b.get("thinking") or "")+" "
            elif t=="tool_use":
                uses[b["id"]]=b; order.append(b["id"]); intents[b["id"]]=text.strip()[:300]
            elif t=="tool_result":
                r=b.get("content")
                if isinstance(r,list): r="".join(x.get("text","") for x in r if isinstance(x,dict))
                results[b.get("tool_use_id")]=r or ""
    out=[]
    for i in order:
        b=uses[i]; n=b["name"]; inp=b.get("input") or {}
        if n=="Bash": cmd=inp.get("command",""); kind=classify(cmd)
        elif n in("Read",): cmd=f"Read {inp.get('file_path')} off={inp.get('offset')} lim={inp.get('limit')}"; kind="read"
        elif n in("Grep","Glob"): cmd=f"{n} {inp.get('pattern')} {inp.get('path','')}"; kind="grep"
        elif n in("Edit","Write","MultiEdit"): cmd=f"{n} {inp.get('file_path')}"; kind="edit"
        else: cmd=n+" "+json.dumps(inp)[:120]; kind="other"
        out.append(dict(kind=kind,tool=n,cmd=cmd,intent=intents.get(i,""),out=results.get(i,"")))
    return out
def codex(path):
    out=[]; pending=""
    for l in open(path):
        try:d=json.loads(l)
        except: continue
        if d.get("type")!="item.completed": continue
        it=d["item"]; t=it.get("type")
        if t in("reasoning","agent_message"): pending+=(it.get("text") or "")+" "; continue
        if t=="command_execution":
            cmd=it.get("command",""); cmd=re.sub(r"^/bin/zsh -lc '(.*)'$",r"\1",cmd,flags=re.S)
            out.append(dict(kind=classify(cmd),tool="exec",cmd=cmd,intent=pending.strip()[:300],out=it.get("aggregated_output") or ""))
            pending=""
        elif t=="file_change":
            out.append(dict(kind="edit",tool="apply_patch",cmd="apply_patch "+", ".join(c.get("path","") for c in it.get("changes",[])),intent=pending.strip()[:300],out=""))
            pending=""
    return out
def sessions():
    for f in sorted(glob.glob(f"{RES}/cli-issue42-product-2026-10-18/transcripts/*.jsonl")+glob.glob(f"{RES}/cli-ab-generic14-2026-10-18/transcripts/*.jsonl")):
        name=os.path.basename(f).replace(".claude-sonnet-5-5","").replace(".nocache.jsonl","")
        yield "claude",name,claude(f)
    for d in sorted(glob.glob(f"{DOC}/cli-release-sol61-warm-2026-10-18-stopped/cells/*/prism_cli")):
        yield "codex",os.path.basename(os.path.dirname(d))+".codex",codex(d+"/events.jsonl")
if __name__=="__main__":
    tl=sys.argv[1]
    for agent,name,seq in sessions():
        with open(f"{tl}/{name}.txt","w") as w:
            for k,s in enumerate(seq):
                w.write(f"\n### {k} [{s['kind']}] {s['tool']}\nINTENT: {s['intent']}\nCMD: {s['cmd'][:600]}\nOUT({len(s['out'])}B): {s['out'][:700]}\n")
        print(agent,name,len(seq),"calls:"," ".join(s["kind"][0] for s in seq))
