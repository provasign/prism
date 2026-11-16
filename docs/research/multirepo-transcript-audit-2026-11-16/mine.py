import json,os,sys,re,glob,collections
C=os.path.expanduser(sys.argv[1])
out=[]
for f in sorted(glob.glob(C+'/transcripts/*.prism_cli')+glob.glob(C+'/transcripts/*.native')):
    task,arm=os.path.basename(f).rsplit('.',1)
    calls={};order=[]
    for l in open(f):
        try:o=json.loads(l)
        except: continue
        if o.get('type')=='assistant':
            for b in o['message'].get('content',[]):
                if b.get('type')=='tool_use':
                    calls[b['id']]={'task':task,'arm':arm,'tool':b['name'],'input':b['input'],'res':None,'err':False,'n':len(order)}
                    order.append(b['id'])
        elif o.get('type')=='user':
            c=o['message'].get('content')
            if isinstance(c,list):
                for b in c:
                    if b.get('type')=='tool_result' and b['tool_use_id'] in calls:
                        r=b.get('content')
                        if isinstance(r,list): r='\n'.join(x.get('text','') for x in r if isinstance(x,dict))
                        calls[b['tool_use_id']]['res']=r or ''
                        calls[b['tool_use_id']]['err']=bool(b.get('is_error'))
    out+= [calls[i] for i in order]
json.dump(out,open(sys.argv[2],'w'))
print(len(out))
