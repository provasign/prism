import json,re,collections,sys
S=sys.argv[1]
EDIT=re.compile(r"(sed -i|s\.replace|\.write\(|cat >+ |>> )")
allpre=[]
for n in ('c15','c16'):
    d=json.load(open(f'{S}/{n}.json'))
    cells=collections.defaultdict(list)
    for c in d:
        if c['arm']=='prism_cli': cells[c['task']].append(c)
    st=collections.Counter()
    for t,v in cells.items():
        if len(v)<=3: continue
        fe=None
        for c in v:
            cmd=c['input'].get('command','') if c['tool']=='Bash' else ''
            if fe is None and (c['tool'] in('Edit','Write') or EDIT.search(cmd)): fe=c['n']
        seen=[]
        for c in v:
            if c['tool']!='Bash': continue
            cmd=c['input']['command']; r=c['res'] or ''
            segs=re.split(r'\s*(?:;|&&|\|\|)\s*|\n',cmd)
            psegs=[s for s in segs if re.match(r'\s*\(?\s*prism\s+(search|lookup|query|read|change-impact)',s)]
            # single prism seg + head only command => can test truncation
            if len(psegs)==1 and len([s for s in segs if s.strip() and not s.strip().startswith('cd ')])==1:
                m=re.search(r'\|\s*head\s+-(?:n\s*)?(\d+)',psegs[0])
                st['solo_prism']+=1
                if m:
                    st['solo_head']+=1
                    N=int(m.group(1)); L=r.rstrip('\n').count('\n')+1
                    if L>=N:
                        st['solo_head_truncated']+=1
                        if 'linked repositories' not in r: st['trunc_no_linked_tail']+=1
                    else:
                        st['solo_head_not_trunc']+=1
                    st['head_N_%d'%N]+=1
            for s in psegs:
                m=re.match(r'\s*\(?\s*prism\s+(\S+)\s+(.*)',s)
                op=m.group(1); rest=re.sub(r'\s*(2>&1)?\s*\|.*','',m.group(2))
                repo=re.search(r'--repo[ =](\S+)',rest)
                path=re.search(r'--path[ =](\S+)',rest)
                terms=tuple(sorted(x.strip('\'"') for x in re.findall(r"'[^']*'|\"[^\"]*\"|\S+",re.sub(r'--(repo|path|scope|format|terms|file)[ =]\S+','',rest)) if not x.startswith('--')))
                st['seg_'+op]+=1
                for (op2,terms2,repo2,n2) in seen:
                    if op2==op and set(terms)&set(terms2) and (repo2 is None)!=(repo is None) :
                        st['rerun_same_term_other_repo_'+op]+=1; break
                seen.append((op,terms,repo.group(1) if repo else None,c['n']))
                st['nterms_%s_%d'%(op,min(len(terms),6))]+=1
            if not psegs and re.search(r'\b(grep|rg)\b',cmd) and (fe is None or c['n']<=fe):
                allpre.append((n,t,c['n'],cmd))
    print(n); [print('   ',k,st[k]) for k in sorted(st)]
print(len(allpre))
for x in allpre: print(x[0],x[1],x[2],'|',x[3].replace('\n',' ⏎ ')[:420])
