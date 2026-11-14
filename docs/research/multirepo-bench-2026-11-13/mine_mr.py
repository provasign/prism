import subprocess, sys, re
def sh(*a): return subprocess.run(a, capture_output=True, text=True).stdout
repo, mod = sys.argv[1], sys.argv[2]
since = "2026-06-01"
commits = sh("git","-C",repo,"log","--since",since,"--no-merges","--format=%h","-G",re.escape(mod),"--","*go.mod").split()
for c in commits:
    files = [f for f in sh("git","-C",repo,"show","--format=","--name-only",c).split() if f.endswith(".go")]
    if not files: continue
    # files whose new version imports the library
    imp = [f for f in files if mod in sh("git","-C",repo,"show",f"{c}:{f}")[:4000]]
    if not imp: continue
    stat = sh("git","-C",repo,"show","--format=","--numstat",c,"--",*imp)
    added = sum(int(l.split()[0]) for l in stat.splitlines() if l.split()[0].isdigit())
    gomod = sh("git","-C",repo,"show","--format=",c,"--","*go.mod")
    ver = re.findall(r"^\+\s*"+re.escape(mod)+r"\S*\s+(v\S+)", gomod, re.M)
    subj = sh("git","-C",repo,"show","-s","--format=%cs %s",c).strip()[:85]
    print(f"{c} {subj} | lib_files={len(imp)} +{added} new={sorted(set(ver))[:2]}")
