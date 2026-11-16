import json,re,collections,sys
S=sys.argv[1]
EDIT=re.compile(r"(sed -i|s\.replace|\.write\(|cat >+ |>> )")
miss=[];dup=[];opsz=collections.defaultdict(list)
post=collections.Counter(); pre=collections.Counter()
repo_search=[]
for n in ('c15','c16'):
    d=json.load(open(f'{S}/{n}.json'))
    cells=collections.defaultdict(list)
    for c in d:
        if c['arm']=='prism_cli': cells[c['task']].append(c)
    for t,v in cells.items():
        if len(v)<=3: continue
        fe=None
        for c in v:
            cmd=c['input'].get('command','') if c['tool']=='Bash' else ''
            if fe is None and (c['tool'] in('Edit','Write') or EDIT.search(cmd)): fe=c['n']
        for c in v:
            if c['tool']!='Bash': continue
            cmd=c['input']['command']; r=c['res'] or ''
            segs=[s.strip() for s in re.split(r'\s*(?:;|&&|\|\|)\s*|\n',cmd) if s.strip()]
            ps=[s for s in segs if re.match(r'\(?\s*prism\s+(search|lookup|query|read|change-impact)',s)]
            ispost = fe is not None and c['n']>fe
            bucket = post if ispost else pre
            for s in segs:
                first=s.lstrip('( ').split()[0] if s.lstrip('( ').split() else ''
                if first in('grep','rg'):
                    k='grep_src'
                    if 'go/pkg/mod' in s or '$D' in s or '$d' in s: k='grep_modcache'
                    elif re.search(r'\^func|\^type',s): k='grep_outline(^func/^type)'
                    elif re.search(r'-[ABC]\s?\d',s): k='grep_with_context(-A/-B)'
                    bucket[k]+=1
                elif first=='sed' and '-n' in s: bucket['sed_n_range']+=1
                elif first=='cat' and '>' not in s and '<<' not in s: bucket['cat_file']+=1
                elif first=='prism': bucket['prism']+=1
            if len(ps)==1 and len([s for s in segs if not s.startswith('cd ')])==1:
                op=re.match(r'\(?\s*prism\s+(\S+)',ps[0]).group(1)
                key=op+('_repo' if '--repo' in ps[0] else '')
                opsz[key].append(len(r))
                if op=='search' and '--repo' in ps[0]:
                    lines=r.split('\n'); tl=sum('_test.go:' in l for l in lines)
                    repo_search.append((n,t,c['n'],len(lines),tl,len(r)))
            if ps:
                for m in re.finditer(r'^.*(no method or function|is not indexed|not found|no symbol|0 match\(es\)|unknown flag|takes one|usage:|no match|No match|not defined|NOT FOUND|cannot|outside).*$',r,re.M):
                    miss.append((n,t,c['n'],m.group(0)[:260]))
                # duplicate body ranges in lookup
                rng=re.findall(r'^// (?:method|function|type|var|const|struct|interface)\s+(\S+)\s+(\S+:\d+-\d+)',r,re.M)
                cnt=collections.Counter(x[1] for x in rng)
                for k,vv in cnt.items():
                    if vv>1: dup.append((n,t,c['n'],k,vv))
print('PRE-EDIT segments',dict(pre)); print('POST-EDIT segments',dict(post))
print('op sizes (solo calls): op n mean max')
for k,v in sorted(opsz.items()): print('  ',k,len(v),sum(v)//len(v),max(v))
print('dup lookup bodies',dup)
print('repo searches: lines, test lines'); 
tl=sum(x[4] for x in repo_search); L=sum(x[3] for x in repo_search); print('  n',len(repo_search),'lines',L,'test-file lines',tl)
print('MISS/ERR lines',len(miss))
for m in miss: print('  ',m)
