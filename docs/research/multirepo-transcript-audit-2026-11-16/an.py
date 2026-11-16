import json,re,collections,sys
S=sys.argv[1]
PR=re.compile(r'(?:^|[\s;&|(])prism\s+(search|lookup|query|read|change-impact|verify|status|test-plan|index)\b([^;\n]*?)(?=(?:\s*;|\s*&&|\s*\|\||\n|$))')
EDIT=re.compile(r"(sed -i|s\.replace|\.write\(|cat >+ |>> )")
for n in ('c15','c16'):
    d=json.load(open(f'{S}/{n}.json'))
    print('==========',n)
    for arm in ('prism_cli','native'):
        cells=collections.defaultdict(list)
        for c in d:
            if c['arm']==arm: cells[c['task']].append(c)
        # skip empty cells
        cells={t:v for t,v in cells.items() if len(v)>3}
        tot=collections.Counter(); modcache=set(); 
        for t,v in cells.items():
            first_edit=None
            for c in v:
                cmd=c['input'].get('command','') if c['tool']=='Bash' else ''
                if first_edit is None and (c['tool'] in('Edit','Write') or EDIT.search(cmd)): first_edit=c['n']
            for c in v:
                cmd=c['input'].get('command','') if c['tool']=='Bash' else ''
                r=c['res'] or ''
                tot['calls']+=1; tot['chars']+=len(r)
                post = first_edit is not None and c['n']>first_edit
                if c['tool']=='Read': tot['read_calls']+=1; tot['read_chars']+=len(r)
                if c['tool']=='Bash':
                    ps=PR.findall(cmd)
                    if ps:
                        tot['prism_bash']+=1; tot['prism_chars']+=len(r)
                        tot['prism_post_edit' if post else 'prism_pre_edit']+=1
                        if re.search(r'\|\s*head',cmd): tot['prism_bash_with_head']+=1
                        if re.search(r'\|\s*grep',cmd): tot['prism_bash_with_grep']+=1
                        for op,args in ps:
                            tot['op_'+op]+=1
                            if '--repo' in args: tot['op_'+op+'_repo']+=1
                    else:
                        if re.search(r'\b(grep|rg)\b',cmd): tot['grep_bash']+=1; tot['grep_post_edit' if post else 'grep_pre_edit']+=1
                        if re.search(r'sed -n|\bcat [^>]',cmd): tot['sedcat_bash']+=1
                    if 'go/pkg/mod' in cmd or 'go list -m' in cmd: modcache.add(t); tot['modcache_calls']+=1
        print(arm,len(cells),'cells'); 
        for k in sorted(tot): print('   ',k,tot[k])
        print('    modcache cells',len(modcache),sorted(modcache))
