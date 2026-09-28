import sys, math, collections, json
sys.path.insert(0,'.')
import aesmetrics as A
d, segs, pads, vias = A.load(sys.argv[1])
for s in segs:
    s['padA']=A.pads_at(pads,s['a'],s['layer'],s['net']); s['padB']=A.pads_at(pads,s['b'],s['layer'],s['net'])
non=[s for s in segs if A.octi_dev(A.ang(s['a'],s['b']))>A.ANG_TOL]
bins=collections.OrderedDict([('0.5-3',[]),('3-10',[]),('10-22.5',[])])
for s in non:
    dv=A.octi_dev(A.ang(s['a'],s['b']))
    k='0.5-3' if dv<3 else '3-10' if dv<10 else '10-22.5'
    bins[k].append(s)
print('nonocti dev bins:',{k:(len(v),round(sum(s['len'] for s in v))) for k,v in bins.items()})
# long visibly-crooked stubs: len>=20 & dev in 0.5..10
vis=[s for s in non if s['len']>=20]
print('stubs >=20mil:',len(vis), round(sum(s['len'] for s in vis)))
c=collections.Counter()
for s in vis:
    p=(s['padA'] or s['padB'])[0]; c[(s['net'],p['ref'])]+=1
print('worst crooked stubs by net/ref:',c.most_common(10))
for s in sorted(vis,key=lambda s:-s['len'])[:8]:
    p=(s['padA'] or s['padB'])[0]
    print('  ',s['net'],p['ref']+'.'+p['num'],s['id'],'len',round(s['len'],1),'dev',round(A.octi_dev(A.ang(s['a'],s['b'])),1),'w',s['w'])
# kind of segment: are those fanout (pad->via) stubs?
vk={A.key((x,y)) for x,y,_ in vias}
fan=[s for s in vis if A.key(s['a']) in vk or A.key(s['b']) in vk]
print('crooked stubs ending on a via (fanout):',len(fan), round(sum(s['len'] for s in fan)))
# parallel with loose thresholds
cnt=0; gaps=[]
H=[s for s in segs if A.axis_class(A.ang(s['a'],s['b'])) in 'HVD' and s['len']>=20]
for i in range(len(H)):
  for j in range(i+1,len(H)):
    a,b=H[i],H[j]
    if a['layer']!=b['layer'] or a['net']==b['net']: continue
    ta=A.ang(a['a'],a['b'])%180; tb=A.ang(b['a'],b['b'])%180
    if abs(ta-tb)>0.5: continue
    th=math.radians(ta); ux,uy=math.cos(th),math.sin(th)
    perp=(b['a'][0]-a['a'][0])*-uy+(b['a'][1]-a['a'][1])*ux
    pa=sorted(((a['a'][0])*ux+a['a'][1]*uy,(a['b'][0])*ux+a['b'][1]*uy))
    pb=sorted(((b['a'][0])*ux+b['a'][1]*uy,(b['b'][0])*ux+b['b'][1]*uy))
    ov=min(pa[1],pb[1])-max(pa[0],pb[0])
    if ov<20: continue
    g=abs(perp)-(a['w']+b['w'])/2
    if 0<=g<=60: gaps.append((round(g,2),a['net'],b['net'],round(ov),a['layer']))
gaps.sort()
print('parallel neighbour pairs gap<=60 overlap>=20:',len(gaps))
for g in gaps: print('  ',g)
# via positions near each other: aligned rows?
vx=[(round(x,2),round(y,2),n) for x,y,n in vias]
print('via mod-5 residues sample', [(round(x%5,2),round(y%5,2)) for x,y,_ in vx[:6]])
# track vertex grid: fraction of non-pad vertices on 5-mil grid
pts=[p for s in segs for p in (s['a'],s['b'])]
on=sum(1 for p in pts if abs(p[0]/5-round(p[0]/5))<0.01 and abs(p[1]/5-round(p[1]/5))<0.01)
print('track vertices on 5mil grid', on, '/', len(pts))
# pad centres on 5mil grid
onp=sum(1 for p in pads if abs(p['x']/5-round(p['x']/5))<0.01 and abs(p['y']/5-round(p['y']/5))<0.01)
print('pad centres on 5mil grid', onp,'/',len(pads))
