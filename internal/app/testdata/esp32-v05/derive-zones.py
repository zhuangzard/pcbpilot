import json,sys,re
import os
T=os.path.dirname(os.path.abspath(__file__))+'/'
page,groups,out=sys.argv[1],sys.argv[2],sys.argv[3]
d=json.load(open(T+page)); g=json.load(open(T+groups))
nets={n['id']:n['name'] for n in d['nets']}
pn={}
for c in d['connections']: pn[(c['componentId'],c['pinNumber'])]=nets[c['netId']]
byref={c['ref']:c for c in d['components']}
def ispow(n): return bool(re.match(r'^(\+|VCC|VDD|VBUS|\d+V\d*|3V3|5V)',n.upper())) 
def isgnd(n): return 'GND' in n.upper()
comps=[];zones=[]
zone_of={}
for m in g['modules']:
    refs=[p['designator'] for p in m['placements'] if p['designator'] in byref]
    if not refs: continue
    core=max(refs,key=lambda r:len(byref[r]['pins']))
    zones.append({'id':m['id'],'title':m['id'],'coreComponentId':byref[core]['id'],'componentIds':[byref[r]['id'] for r in refs]})
    for r in refs: zone_of[r]=m['id']
netzones={}
for r,c in byref.items():
    if r not in zone_of: continue
    pins=[];states={}
    for q in c['pins']:
        n=pn.get((c['id'],q['number']),'')
        if n: netzones.setdefault(n,set()).add(zone_of[r])
        else: states[q['number']]='nc'
        pins.append({'number':q['number'],'name':q.get('name',q['number']),'net':n,'x':q['x'],'y':q['y']})
    pl=c['placement']
    comps.append({'id':c['id'],'measurement':{'designator':r,'x':pl['x'],'y':pl['y'],'rotation':pl.get('rotation',0),'mirror':pl.get('mirror',False),'bbox':pl['bbox'],'pins':pins},'pinStates':states})
pol={}
for n,zs in netzones.items():
    pol[n]='local_ground' if isgnd(n) else 'local_power' if ispow(n) else ('direct' if len(zs)==1 else 'module_port')
json.dump({'schemaVersion':1,'components':comps,'netPolicies':pol,'zones':zones,'maxCandidates':200000},open(out,'w'),indent=1)
print(pol)
