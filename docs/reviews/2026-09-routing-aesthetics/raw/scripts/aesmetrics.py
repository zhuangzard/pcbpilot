#!/usr/bin/env python3
"""Throwaway routing-aesthetic metrics on pcbpilot routed dumps (read-only).

Usage: aesmetrics.py <routed.json> [...]
Metrics:
  M1 bend density           bends / inch, bends / connection, 90deg/acute residue
  M2 non-octilinear residue length share of segments whose angle is not k*45deg
  M3 short jogs / zigzags   S-jog (parallel in/out, short middle), staircases, tiny segs
  M4 layer-direction        per-layer H/V/D share vs stackup preferred dir
  M5 pad entry              axial / diagonal / skew, off-centre, corner exit
  M6 parallel spacing CV    edge-gap CV within parallel bundles
"""
import json, math, sys, collections

TOL = 0.05          # mil, endpoint merge
ANG_TOL = 0.5       # deg
PREF = {1: 'h', 2: 'v'}  # 4L stackup: TOP h, BOTTOM v (stackup.go)


def key(p):
    return (round(p[0] / TOL), round(p[1] / TOL))


def ang(a, b):
    return math.degrees(math.atan2(b[1] - a[1], b[0] - a[0])) % 360


def octi_dev(deg):
    r = deg % 45
    return min(r, 45 - r)


def axis_class(deg):
    d = deg % 180
    if min(d, 180 - d) < ANG_TOL:
        return 'H'
    if abs(d - 90) < ANG_TOL:
        return 'V'
    if abs(d - 45) < ANG_TOL or abs(d - 135) < ANG_TOL:
        return 'D'
    return 'X'


def load(path):
    d = json.load(open(path))
    segs = []
    for l in d['copper']['lines']:
        a, b = (l['startX'], l['startY']), (l['endX'], l['endY'])
        if math.dist(a, b) < 1e-6:
            continue
        segs.append(dict(net=l['net'], layer=l['layer'], a=a, b=b, w=l['lineWidth'],
                         len=math.dist(a, b), id=l['primitiveId']))
    pads = []
    for c in d['components']:
        for p in c['pads']:
            pads.append(dict(ref=c['designator'], num=p['padNumber'], net=p.get('net', ''),
                             x=p['x'], y=p['y'], w=p['width'], h=p['height'],
                             rot=p.get('rotation', 0) or 0, layer=p['layer'], shape=p['shape'][0]))
    vias = [(v['x'], v['y'], v['net']) for v in d['copper']['vias']]
    return d, segs, pads, vias


def pad_local(p, pt):
    t = math.radians(p['rot'])
    dx, dy = pt[0] - p['x'], pt[1] - p['y']
    return dx * math.cos(t) + dy * math.sin(t), -dx * math.sin(t) + dy * math.cos(t)


def in_pad(p, pt, slack=0.5):
    u, v = pad_local(p, pt)
    return abs(u) <= p['w'] / 2 + slack and abs(v) <= p['h'] / 2 + slack


def pads_at(pads, pt, layer, net):
    out = []
    for p in pads:
        if p['layer'] not in (layer, 12):
            continue
        if net and p['net'] and p['net'] != net:
            continue
        if in_pad(p, pt):
            out.append(p)
    return out


def analyse(path):
    d, segs, pads, vias = load(path)
    viak = {key((x, y)) for x, y, _ in vias}
    rep = {'file': path.split('/')[-1]}
    total_len = sum(s['len'] for s in segs)
    rep['segments'] = len(segs)
    rep['routed_len_in'] = round(total_len / 1000, 2)

    # endpoint classification
    for s in segs:
        s['padA'] = pads_at(pads, s['a'], s['layer'], s['net'])
        s['padB'] = pads_at(pads, s['b'], s['layer'], s['net'])

    # ---- adjacency per (net, layer)
    adj = collections.defaultdict(list)
    for i, s in enumerate(segs):
        adj[(s['net'], s['layer'], key(s['a']))].append((i, 'a'))
        adj[(s['net'], s['layer'], key(s['b']))].append((i, 'b'))

    def other(s, end):
        return s['b'] if end == 'a' else s['a']

    def this(s, end):
        return s['a'] if end == 'a' else s['b']

    # ---- M1 bends
    per_net = collections.defaultdict(lambda: collections.Counter())
    net_len = collections.Counter()
    for s in segs:
        net_len[s['net']] += s['len']
    bends = []
    for (net, layer, k), lst in adj.items():
        if len(lst) != 2:
            continue
        (i, ei), (j, ej) = lst
        si, sj = segs[i], segs[j]
        pt = this(si, ei)
        if k in viak:
            continue
        # vertex inside a pad = pad junction, not a routing bend
        if pads_at(pads, pt, layer, net):
            continue
        din = ang(other(si, ei), pt)
        dout = ang(pt, other(sj, ej))
        turn = abs((dout - din + 180) % 360 - 180)
        cls = ('collinear' if turn < ANG_TOL else '45' if abs(turn - 45) < ANG_TOL else
               '90' if abs(turn - 90) < ANG_TOL else 'acute' if turn > 90 + ANG_TOL else 'odd')
        per_net[net][cls] += 1
        bends.append(dict(net=net, layer=layer, pt=pt, turn=turn, cls=cls, i=i, j=j,
                          wchange=si['w'] != sj['w']))
    tot = collections.Counter()
    for c in per_net.values():
        tot.update(c)
    nb = sum(v for k, v in tot.items() if k != 'collinear')
    # connections per net = pads-1 (routed nets only)
    netpads = collections.Counter(p['net'] for p in pads if p['net'])
    routed_nets = set(net_len)
    conns = sum(max(netpads[n] - 1, 1) for n in routed_nets)
    rep['M1_bends'] = dict(total=nb, by_class=dict(tot), per_inch=round(nb / (total_len / 1000), 2),
                           per_connection=round(nb / conns, 2), connections=conns,
                           collinear_split_vertices=tot['collinear'],
                           collinear_from_width_change=sum(1 for b in bends if b['cls'] == 'collinear' and b['wchange']))
    worst = []
    for n, c in per_net.items():
        b = sum(v for k, v in c.items() if k != 'collinear')
        worst.append((round(b / (net_len[n] / 1000), 1), b, round(net_len[n]), max(netpads[n] - 1, 1), n, dict(c)))
    worst.sort(key=lambda t: (-t[1] / t[3], -t[0]))
    rep['M1_worst_by_bends_per_conn'] = [dict(net=w[4], bends=w[1], conns=w[3], per_conn=round(w[1] / w[3], 1),
                                              per_inch=w[0], len_mil=w[2], classes=w[5]) for w in worst[:8]]

    # ---- M2 non-octilinear
    nonoct = [s for s in segs if octi_dev(ang(s['a'], s['b'])) > ANG_TOL]
    stub = [s for s in nonoct if s['padA'] or s['padB']]
    mid = [s for s in nonoct if not (s['padA'] or s['padB'])]
    rep['M2_nonoctilinear'] = dict(count=len(nonoct), len_share=round(sum(s['len'] for s in nonoct) / total_len, 4),
                                   pad_stub=len(stub), pad_stub_len=round(sum(s['len'] for s in stub)),
                                   mid_route=len(mid), mid_len=round(sum(s['len'] for s in mid)),
                                   stub_len_p50=round(sorted(s['len'] for s in stub)[len(stub) // 2], 1) if stub else 0,
                                   stub_len_max=round(max((s['len'] for s in stub), default=0), 1))
    c = collections.Counter(s['net'] for s in nonoct)
    rep['M2_worst'] = [dict(net=n, count=k, len=round(sum(s['len'] for s in nonoct if s['net'] == n)),
                            devs=sorted({round(octi_dev(ang(s['a'], s['b'])), 1) for s in nonoct if s['net'] == n})[:5])
                       for n, k in c.most_common(8)]

    # ---- M3 short jogs: walk chains of degree-2 non-pad non-via vertices
    bend_at = {}
    for b in bends:
        bend_at[(b['i'], b['j'])] = b
        bend_at[(b['j'], b['i'])] = b
    seg_bends = collections.defaultdict(list)
    for b in bends:
        seg_bends[b['i']].append(b)
        seg_bends[b['j']].append(b)
    sjogs, tiny, stair = [], [], []
    for m, s in enumerate(segs):
        bs = [b for b in seg_bends[m] if b['cls'] != 'collinear']
        if len(bs) != 2:
            continue
        n1 = bs[0]['j'] if bs[0]['i'] == m else bs[0]['i']
        n2 = bs[1]['j'] if bs[1]['i'] == m else bs[1]['i']
        s1, s2 = segs[n1], segs[n2]

        def dirv(x, towards_pt):
            # direction of segment x oriented towards the shared point
            a, b = x['a'], x['b']
            if math.dist(b, towards_pt) > math.dist(a, towards_pt):
                a, b = b, a
            return ang(a, b)
        pA = bs[0]['pt']
        pB = bs[1]['pt']
        d1 = dirv(s1, pA)
        d3 = (dirv(s2, pB) + 180) % 360  # oriented away from middle
        net_turn = abs((d3 - d1 + 180) % 360 - 180)
        L = s['len']
        w = s['w']
        if net_turn < ANG_TOL:
            # lateral offset = perpendicular displacement between in/out lines
            ux, uy = math.cos(math.radians(d1)), math.sin(math.radians(d1))
            off = abs((pB[0] - pA[0]) * -uy + (pB[1] - pA[1]) * ux)
            if off < 4 * max(w, s1['w']) or L < 25:
                sjogs.append(dict(net=s['net'], layer=s['layer'], at=(round(pA[0]), round(pA[1])),
                                  offset=round(off, 1), mid_len=round(L, 1), w=w))
        if L < max(1.5 * w, 6) and net_turn > ANG_TOL:
            tiny.append(dict(net=s['net'], at=(round(pA[0]), round(pA[1])), len=round(L, 1), w=w,
                             turns=(round(bs[0]['turn']), round(bs[1]['turn']))))
    # staircase: >=3 consecutive bends where consecutive middle segments all < 30mil
    rep['M3_jogs'] = dict(s_jogs=len(sjogs), tiny_segments=len(tiny),
                          s_jog_offset_p50=sorted(j['offset'] for j in sjogs)[len(sjogs) // 2] if sjogs else 0)
    cj = collections.Counter(j['net'] for j in sjogs)
    rep['M3_worst_sjog_nets'] = cj.most_common(8)
    rep['M3_sjog_examples'] = sorted(sjogs, key=lambda j: j['offset'])[:6]
    rep['M3_tiny_examples'] = tiny[:6]

    # ---- M4 layer direction
    lay = collections.defaultdict(collections.Counter)
    wrong_long = []
    for s in segs:
        cl = axis_class(ang(s['a'], s['b']))
        lay[s['layer']][cl] += s['len']
        pref = PREF.get(s['layer'])
        if pref and s['len'] >= 150 and ((pref == 'h' and cl == 'V') or (pref == 'v' and cl == 'H')):
            wrong_long.append((s['net'], s['layer'], round(s['len'])))
    rep['M4_layer_dir'] = {}
    for L_, c in sorted(lay.items()):
        t = sum(c.values())
        pref = PREF.get(L_)
        prefshare = c['H' if pref == 'h' else 'V'] / t if pref else None
        rep['M4_layer_dir'][L_] = dict(len_mil=round(t), pref=pref,
                                       share={k: round(v / t, 3) for k, v in c.items()},
                                       pref_share=round(prefshare, 3) if prefshare is not None else None)
    wrong_long.sort(key=lambda t: -t[2])
    rep['M4_wrong_dir_long_segments'] = len(wrong_long)
    rep['M4_worst'] = wrong_long[:8]

    # ---- M5 pad entry
    entries = []
    for s in segs:
        for end, pl in (('a', s['padA']), ('b', s['padB'])):
            if not pl:
                continue
            p = pl[0]
            pt = this(s, end)
            far = other(s, end)
            if in_pad(p, far, slack=-0.01):
                continue  # whole segment inside pad
            u, v = pad_local(p, pt)
            half = min(p['w'], p['h']) / 2
            off = math.hypot(u, v) / half if half else 0
            rel = (ang(pt, far) - p['rot']) % 90
            dev = min(rel, 90 - rel)
            square = abs(p['w'] - p['h']) < 0.5 and p['shape'] in ('ELLIPSE',)
            cls = 'axial' if dev < ANG_TOL else ('diag' if abs(dev - 45) < ANG_TOL else 'skew')
            # exit point on pad boundary (local frame), corner proximity
            fu, fv = pad_local(p, far)
            du, dv = fu - u, fv - v
            tmax = 1e9
            for comp, dc, hw in ((u, du, p['w'] / 2), (v, dv, p['h'] / 2)):
                if abs(dc) > 1e-9:
                    for bnd in (hw, -hw):
                        t = (bnd - comp) / dc
                        if t > 0:
                            tmax = min(tmax, t)
            eu, ev = u + du * tmax, v + dv * tmax
            corner = min(math.hypot(eu - cu, ev - cv) for cu in (p['w'] / 2, -p['w'] / 2) for cv in (p['h'] / 2, -p['h'] / 2))
            fat = s['w'] > min(p['w'], p['h']) + 0.01
            entries.append(dict(net=s['net'], pad=f"{p['ref']}.{p['num']}", cls=cls if not square else 'round',
                                dev=round(dev, 1), off=round(off, 2), corner=corner < s['w'] / 2 and cls != 'axial',
                                fat=fat, w=s['w'], len=round(s['len'], 1), pw=p['w'], ph=p['h']))
    ce = collections.Counter(e['cls'] for e in entries)
    offc = [e for e in entries if e['off'] > 0.05]
    rep['M5_pad_entry'] = dict(entries=len(entries), by_class=dict(ce),
                               axial_share=round(ce['axial'] / max(len(entries), 1), 3),
                               off_centre=len(offc), corner_exit=sum(e['corner'] for e in entries),
                               wider_than_pad=sum(e['fat'] for e in entries))
    bad = [e for e in entries if e['cls'] == 'skew']
    cb = collections.Counter(e['net'] for e in bad)
    rep['M5_worst_skew_nets'] = cb.most_common(8)
    rep['M5_skew_examples'] = sorted(bad, key=lambda e: -e['dev'])[:6]
    rep['M5_offcentre_examples'] = sorted(offc, key=lambda e: -e['off'])[:5]

    # ---- M6 parallel spacing CV
    cand = []
    for i, s in enumerate(segs):
        cl = axis_class(ang(s['a'], s['b']))
        if cl in ('H', 'V', 'D') and s['len'] >= 30:
            cand.append((i, cl))
    pairs = []
    for x in range(len(cand)):
        i, ci = cand[x]
        si = segs[i]
        th = math.radians(ang(si['a'], si['b']))
        ux, uy = math.cos(th), math.sin(th)
        for y in range(x + 1, len(cand)):
            j, cj_ = cand[y]
            sj = segs[j]
            if cj_ != ci or sj['layer'] != si['layer'] or sj['net'] == si['net']:
                continue
            if ci == 'D' and abs(((ang(sj['a'], sj['b']) - ang(si['a'], si['b'])) % 180)) > 1:
                continue
            # perpendicular distance + projection overlap
            perp = (sj['a'][0] - si['a'][0]) * -uy + (sj['a'][1] - si['a'][1]) * ux
            p0 = 0
            p1 = si['len']
            q = sorted(((sj['a'][0] - si['a'][0]) * ux + (sj['a'][1] - si['a'][1]) * uy,
                        (sj['b'][0] - si['a'][0]) * ux + (sj['b'][1] - si['a'][1]) * uy))
            ov = min(p1, q[1]) - max(p0, q[0])
            if ov < 30:
                continue
            gap = abs(perp) - (si['w'] + sj['w']) / 2
            if gap < 0 or gap > 25:
                continue
            pairs.append((i, j, gap, ov))
    # union-find bundles
    par = {}

    def f(a):
        while par.setdefault(a, a) != a:
            par[a] = par[par[a]]
            a = par[a]
        return a
    for i, j, g, ov in pairs:
        par[f(i)] = f(j)
    bund = collections.defaultdict(list)
    for i, j, g, ov in pairs:
        bund[f(i)].append((g, ov, segs[i]['net'], segs[j]['net']))
    cvs = []
    for r_, lst in bund.items():
        if len(lst) < 2:
            continue
        gs = [g for g, _, _, _ in lst]
        ws = [ov for _, ov, _, _ in lst]
        mu = sum(g * w for g, w in zip(gs, ws)) / sum(ws)
        var = sum(w * (g - mu) ** 2 for g, w in zip(gs, ws)) / sum(ws)
        nets = sorted({n for _, _, a, b in lst for n in (a, b)})
        cvs.append((math.sqrt(var) / mu if mu > 0 else 0, len(lst), round(mu, 1), round(min(gs), 1), round(max(gs), 1), nets[:6]))
    allg = [g for _, _, g, _ in pairs]
    mu = sum(allg) / len(allg) if allg else 0
    sd = math.sqrt(sum((g - mu) ** 2 for g in allg) / len(allg)) if allg else 0
    cvs.sort(key=lambda t: -t[0])
    rep['M6_parallel'] = dict(pairs=len(pairs), bundles_ge2=len(cvs),
                              gap_mean=round(mu, 2), gap_cv_all=round(sd / mu, 3) if mu else None,
                              bundle_cv_median=round(sorted(c[0] for c in cvs)[len(cvs) // 2], 3) if cvs else None,
                              hugging_share=round(sum(1 for g in allg if g < 7) / max(len(allg), 1), 3))
    rep['M6_worst_bundles'] = [dict(cv=round(c[0], 2), pairs=c[1], gap_mean=c[2], gap_min=c[3], gap_max=c[4], nets=c[5]) for c in cvs[:6]]

    # ---- extra: via grid / alignment
    on5 = sum(1 for x, y, _ in vias if abs(x / 5 - round(x / 5)) < 0.01 and abs(y / 5 - round(y / 5)) < 0.01)
    rep['X_vias'] = dict(count=len(vias), on_5mil_grid=on5)
    return rep


if __name__ == '__main__':
    for p in sys.argv[1:]:
        print(json.dumps(analyse(p), indent=1, ensure_ascii=False, default=str))
