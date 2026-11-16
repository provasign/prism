import json,re,collections,sys,random
S=sys.argv[1]
EDIT=re.compile(r"(sed -i|s\.replace|\.write\(|cat >+ |>> )")
random.seed(1)
postgrep=[];postprism=[];queries=[]
for n in ('c15','c16'):
    d=json.load(open(f'{S}/{n}.json'))
    for arm in ('native','prism_cli'):
        cells=collections.defaultdict(list)
        for c in d:
            if c['arm']==arm: cells[c['task']].append(c)
        pre=collections.Counter()
        for t,v in cells.items():
            if len(v)<=3 or (n=='c15' and t=='webrtc-sctp-startcontext'): continue
            fe=None
            for c in v:
                cmd=c['input'].get('command','') if c['tool']=='Bash' else ''
                if fe is None and (c['tool'] in('Edit','Write') or EDIT.search(cmd)): fe=c['n']
            pre['cells']+=1
            for c in v:
                r=c['res'] or ''
                cmd=c['input'].get('command','') if c['tool']=='Bash' else ''
                if fe is None or c['n']<fe:
                    pre['pre_calls']+=1; pre['pre_chars']+=len(r)
                else:
                    pre['post_calls']+=1; pre['post_chars']+=len(r)
                if arm=='prism_cli' and c['tool']=='Bash':
                    isp=re.search(r'(^|[\s;&|(])prism\s+(search|lookup|query|read|change-impact)',cmd)
                    if fe is not None and c['n']>fe:
                        if isp: postprism.append((n,t,c['n'],cmd,r))
                        else:
                            for s in re.split(r'\s*(?:;|&&|\|\|)\s*|\n',cmd):
                                s=s.strip()
                                if s.startswith(('grep','rg ')) : postgrep.append((n,t,s))
                    if isp and 'prism query' in cmd: queries.append((n,t,c['n'],cmd,r))
                    if isp:
                        # repo-search blocks
                        for blk in re.split(r'(?m)^(?=// repo \S+  )',r)[1:]:
                            ls=[l for l in blk.split('\n')[1:] if re.match(r'\.\./\S+:\d+:',l)]
                            pre['repo_blocks']+=1; pre['repo_lines']+=len(ls); pre['repo_test_lines']+=sum('_test.go:' in l for l in ls)
                        pre['linked_fallthrough_lookup']+=len(re.findall(r'not defined in this repository; shown from linked',r))
                        pre['local_test_omitted_notes']+=len(re.findall(r'// test files: \d+ hit',r))
        print(n,arm,dict(pre))
print('\nPOST-EDIT grep segments in prism arm:',len(postgrep))
for x in random.sample(postgrep,40): print('  ',x[1][:18],'|',x[2][:170])
print('\nPOST-EDIT prism calls:',len(postprism))
for x in postprism: print('  ',x[0],x[1],x[2],'|',x[3].replace('\n',' ')[:230],'| out',len(x[4]),'|',x[4][:160].replace('\n',' ⏎ '))
print('\nQUERY calls')
for x in queries: print('  ',x[0],x[1],x[2],'|',x[3].replace('\n',' ')[:300],'| out',len(x[4]))
