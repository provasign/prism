import re, sys, collections
sys.path.insert(0, ".")
from seq import sessions, PRISM
GREPS=re.compile(r"(?:^|[\s;&|(])(?:grep|rg)\s+((?:-[A-Za-z]+(?:\s+\d+)?\s+|--[\w-]+(?:=\S+)?\s+)*)(?:-e\s+)?(['\"]?)(.+?)\2(?:\s+([^|;&\n]+))?(?=\s*(?:\||;|&&|$))",re.M)
READS=re.compile(r"(?:sed -n\s+'?\d+,\d+p'?|cat|head(?: -n? ?\d+)?)\s+([\w./-]+\.\w+)")
def core(pat): return re.sub(r"\\[bws]|[\\^$.*+?()\[\]{}|]","",pat).split("\\|")[0].strip()
stats=collections.Counter(); rows=[]; per_session=[]
for agent,name,seq in sessions():
    prism_out=""; n_prism=0; edited=False; pers=0; piped=0; rename_old=None
    m=re.search(r"wide__\w+?__(\w+)_(\w+)$",name.split(".")[0]); 
    if m: rename_old=m.group(2)
    counts=collections.Counter(s["kind"] for s in seq)
    for k,s in enumerate(seq):
        if s["kind"]=="edit": edited=True
        if s["kind"]=="prism":
            n_prism+=1; prism_out+="\n"+s["out"]
            if "persisted-output" in s["out"][:200]:
                pers+=1; mm=re.search(r"Output too large \((\d+)KB\)",s["out"]); rows.append((agent,name,k,"HARNESS-TRUNCATED",mm.group(0) if mm else "", s["cmd"][:100]))
            if re.search(r"\|\s*(head|tail|grep|cut|sed)\b",s["cmd"]): piped+=1; rows.append((agent,name,k,"AGENT-PIPED",re.search(r"\|.*$",s["cmd"]).group(0)[:60],s["cmd"][:100]))
            continue
        if n_prism==0 or s["kind"]!="grep": continue
        for g in GREPS.finditer(s["cmd"]) if s["tool"] in("Bash","exec") else [None]:
            if g is None: pat,tgt=s["cmd"].split(" ",1)[1].rsplit(" ",1)[0], ""
            else: pat,tgt=g.group(3).strip("'\""),(g.group(4) or "").strip()
            c=core(pat)
            if not c: continue
            phase="POST-EDIT" if edited else "PRE-EDIT"
            if rename_old and re.search(r"\b"+re.escape(rename_old)+r"\b",c) and not re.search(r"\|",s["cmd"].split("grep")[0]): kind="RENAME-TARGET"
            elif c in prism_out: kind="SEEN"
            else: kind="NEW"
            tag=f"GREP-{phase}-{kind}"
            stats[tag]+=1; rows.append((agent,name,k,tag,c[:40],s["intent"][:160].replace("\n"," ")))
    per_session.append((agent,name,n_prism,counts["grep"],counts["read"],counts["edit"],counts["test"],pers,piped))
print(dict(stats))
A=[p for p in per_session if p[0]=="claude"]; C=[p for p in per_session if p[0]=="codex"]
for lab,P in(("claude",A),("codex",C)):
    n=len(P); print(f"{lab}: sessions {n}; mean prism {sum(p[2] for p in P)/n:.2f} grep {sum(p[3] for p in P)/n:.2f} read {sum(p[4] for p in P)/n:.2f} edit {sum(p[5] for p in P)/n:.2f} test {sum(p[6] for p in P)/n:.2f}; harness-truncated prism answers {sum(p[7] for p in P)}; agent-piped {sum(p[8] for p in P)}; sessions with exactly 1 prism call {sum(1 for p in P if p[2]==1)}")
with open(sys.argv[1],"w") as w:
    for r in rows: w.write("\t".join(map(str,r))+"\n")
