# board.py — headless board setup for `pcbpilot kicad board` (outline,
# stackup, mounting holes, rule areas, copper zones, board text). Run with
# KiCad's bundled Python (the one that can `import pcbnew`); embedded into the
# pcbpilot binary (internal/kicad/board.go, go:embed) and written to the user
# cache before each run. Kept apart from bridge.py on purpose: the two evolve
# independently.
#
#   python board.py OP IN.kicad_pcb SPEC.json OUT.kicad_pcb
#   python board.py new - SPEC.json OUT.kicad_pcb
#
# OP: new | outline | stackup | holes | keepout | zone | text. SPEC is what
# board.go sends: every length in mm in KiCad's frame (y down); every shape
# as primitives to draw ("prims"), its exact boundary ("edges": segments and
# arcs) and parameters ("shape") for the containment checks, and a polygon
# ("samples") for zones and rule areas. Go has checked the numbers and the
# geometry; this script checks what needs the board (enabled layers, nets,
# the outline, items on layers a stackup removes, pads, vias and holes)
# BEFORE changing anything, then applies the op, saves OUT, loads OUT again
# and compares every requested property with what was read back.
#
# One result line on stdout: PCBPILOT_JSON:{...}. A refusal is
# {"ok": false, "problems": [{"code", "field", "detail", "fix"}]} with exit
# status 1 and OUT not written; a failed fill or read-back is code
# E_BOARD_VERIFY (OUT may exist then: the Go side never commits it).
#
# Only KiCad's public Python API (pcbnew) is used, except for the stackup:
# 10.0's Python bindings do not expose the stackup, so it is spliced into
# the saved file's (setup ...) section as KiCad writes it and the board is
# then loaded and saved again by pcbnew (which re-reads and normalises it).

import json
import math
import os
import re
import sys

import pcbnew

PREFIX = "PCBPILOT_JSON:"

# Two boundaries closer than this (mm) touch: board_geom.go BoardTol.
TOL = 0.001
# Read-back tolerance (mm): values are stored in nm.
EXACT = 1.5e-6
# Arc mid points are re-derived by KiCad on load.
ARC_TOL = 0.002

# KiCad 6-9 short layer names that 10 still writes in the file but no
# longer resolves by name.
LEGACY_LAYERS = {
    "F.SilkS": "F.Silkscreen", "B.SilkS": "B.Silkscreen", "F.Adhes": "F.Adhesive", "B.Adhes": "B.Adhesive",
    "F.CrtYd": "F.Courtyard", "B.CrtYd": "B.Courtyard", "Dwgs.User": "User.Drawings", "Cmts.User": "User.Comments",
    "Eco1.User": "User.Eco1", "Eco2.User": "User.Eco2",
}

TEXT_LAYERS = ("F.Silkscreen", "B.Silkscreen", "F.Fab", "B.Fab")

FIX_LAYER = "use a layer the board has enabled (listed in the detail); add copper layers with `pcbpilot kicad board stackup --layers N`"
FIX_VERIFY = "nothing was committed; report this with the JSON report (do not edit the file by hand)"


def emit(obj, code=0):
    sys.stdout.write("\n" + PREFIX + json.dumps(obj, separators=(",", ":")) + "\n")
    sys.stdout.flush()
    sys.exit(code)


class Problems(list):
    def add(self, code, field, detail, fix=""):
        self.append({"code": code, "field": field, "detail": detail, "fix": fix})


class VerifyError(Exception):
    """A fill or read-back that failed: reported as E_BOARD_VERIFY."""


def nm(v):
    return int(round(v * 1e6))


def mm(v):
    return round(v / 1e6, 6)


def vec(p):
    return pcbnew.VECTOR2I(nm(p[0]), nm(p[1]))


def xy_mm(v):
    return [mm(v.x), mm(v.y)]


def uuid_of(item):
    return item.m_Uuid.AsString()


def fmt_num(v):
    return ("%.3f" % v).rstrip("0").rstrip(".")


# ── exact geometry (the formulas of board_geom.go) ──────────────────────────

def dist(a, b):
    return math.hypot(a[0] - b[0], a[1] - b[1])


def pt_seg(p, a, b):
    dx, dy = b[0] - a[0], b[1] - a[1]
    l2 = dx * dx + dy * dy
    if l2 == 0:
        return dist(p, a)
    t = max(0.0, min(1.0, ((p[0] - a[0]) * dx + (p[1] - a[1]) * dy) / l2))
    return dist(p, (a[0] + t * dx, a[1] + t * dy))


def _cross(o, a, b):
    return (a[0] - o[0]) * (b[1] - o[1]) - (a[1] - o[1]) * (b[0] - o[0])


def _on_seg(a, b, p):
    return (min(a[0], b[0]) - 1e-9 <= p[0] <= max(a[0], b[0]) + 1e-9 and
            min(a[1], b[1]) - 1e-9 <= p[1] <= max(a[1], b[1]) + 1e-9)


def segs_touch(a, b, c, d):
    d1, d2, d3, d4 = _cross(c, d, a), _cross(c, d, b), _cross(a, b, c), _cross(a, b, d)
    if ((d1 > 0 > d2) or (d1 < 0 < d2)) and ((d3 > 0 > d4) or (d3 < 0 < d4)):
        return True
    e = 1e-12
    return ((abs(d1) < e and _on_seg(c, d, a)) or (abs(d2) < e and _on_seg(c, d, b)) or
            (abs(d3) < e and _on_seg(a, b, c)) or (abs(d4) < e and _on_seg(a, b, d)))


def seg_seg(a, b, c, d):
    if segs_touch(a, b, c, d):
        return 0.0
    return min(pt_seg(a, c, d), pt_seg(b, c, d), pt_seg(c, a, b), pt_seg(d, a, b))


def _full(arc):
    return abs(arc[4]) >= 2 * math.pi - 1e-12


def in_span(arc, t):
    if _full(arc):
        return True
    d = t - arc[3]
    if arc[4] < 0:
        d = -d
    d = math.fmod(d, 2 * math.pi)
    if d < 0:
        d += 2 * math.pi
    return d <= abs(arc[4]) + 1e-12 or d >= 2 * math.pi - 1e-12


def arc_ends(arc):
    if _full(arc):
        return []
    cx, cy, r, a0, sw = arc
    return [(cx + r * math.cos(a0), cy + r * math.sin(a0)), (cx + r * math.cos(a0 + sw), cy + r * math.sin(a0 + sw))]


def pt_arc(p, arc):
    c = (arc[0], arc[1])
    d = dist(p, c)
    if d < 1e-12:
        return arc[2]
    if in_span(arc, math.atan2(p[1] - c[1], p[0] - c[0])):
        return abs(d - arc[2])
    return min([dist(p, q) for q in arc_ends(arc)] or [math.inf])


def seg_arc(a, b, arc):
    cx, cy, r = arc[0], arc[1], arc[2]
    dx, dy = b[0] - a[0], b[1] - a[1]
    fx, fy = a[0] - cx, a[1] - cy
    A, B, C = dx * dx + dy * dy, 2 * (fx * dx + fy * dy), fx * fx + fy * fy - r * r
    if A > 0:
        disc = B * B - 4 * A * C
        if disc >= 0:
            sq = math.sqrt(disc)
            for t in ((-B - sq) / (2 * A), (-B + sq) / (2 * A)):
                if -1e-12 <= t <= 1 + 1e-12:
                    x, y = a[0] + t * dx, a[1] + t * dy
                    if in_span(arc, math.atan2(y - cy, x - cx)):
                        return 0.0
    best = min(pt_arc(a, arc), pt_arc(b, arc))
    for q in arc_ends(arc):
        best = min(best, pt_seg(q, a, b))
    t = max(0.0, min(1.0, -(fx * dx + fy * dy) / A)) if A > 0 else 0.0
    q = (a[0] + t * dx, a[1] + t * dy)
    d = dist(q, (cx, cy))
    if d > 1e-12 and in_span(arc, math.atan2(q[1] - cy, q[0] - cx)):
        best = min(best, abs(d - r))
    return best


def seg_edge(a, b, e):
    """Distance from segment a→b to an edge {"seg": [...]} or {"arc": [...]}."""
    if "seg" in e:
        s = e["seg"]
        return seg_seg(a, b, (s[0], s[1]), (s[2], s[3]))
    return seg_arc(a, b, e["arc"])


def edge_start(e):
    if "seg" in e:
        return (e["seg"][0], e["seg"][1])
    cx, cy, r, a0, _ = e["arc"]
    return (cx + r * math.cos(a0), cy + r * math.sin(a0))


def point_in_poly(pts, p):
    inside, j = False, len(pts) - 1
    for i in range(len(pts)):
        a, b = pts[i], pts[j]
        if (a[1] > p[1]) != (b[1] > p[1]) and p[0] < (b[0] - a[0]) * (p[1] - a[1]) / (b[1] - a[1]) + a[0]:
            inside = not inside
        j = i
    return inside


def sdf(shape, p):
    """Exact signed distance from p to a shape {"rect"|"circle"|"slot"|"polygon": ...} (< 0 inside)."""
    if shape.get("circle"):
        c = shape["circle"]
        return dist(p, (c["x"], c["y"])) - c["diameter"] / 2
    if shape.get("slot"):
        s = shape["slot"]
        return pt_seg(p, (s["x1"], s["y1"]), (s["x2"], s["y2"])) - s["width"] / 2
    if shape.get("rect"):
        r = shape["rect"]
        rad = r.get("cornerRadius", 0) or 0
        qx = abs(p[0] - (r["x"] + r["width"] / 2)) - (r["width"] / 2 - rad)
        qy = abs(p[1] - (r["y"] + r["height"] / 2)) - (r["height"] / 2 - rad)
        return math.hypot(max(qx, 0), max(qy, 0)) + min(max(qx, qy), 0) - rad
    pts = shape["polygon"]
    d = min(pt_seg(p, pts[i], pts[(i + 1) % len(pts)]) for i in range(len(pts)))
    return -d if point_in_poly(pts, p) else d


def fits(contours, contains, item):
    """None when the shape item ({"edges", "shape"}) lies on the board — its
    boundary more than TOL from every contour (outline and cutouts), a point
    of it on the board, no contour inside it — else why not. contains(p) is
    the board's own point test."""
    for e in item["edges"]:
        for c in contours:
            for i in range(len(c)):
                if seg_edge(c[i], c[(i + 1) % len(c)], e) <= TOL:
                    return "its edge crosses or touches the board outline or a cutout (keep more than %g mm away)" % TOL
    if not contains(edge_start(item["edges"][0])):
        return "it lies outside the board outline or inside a cutout"
    for c in contours:
        if c and sdf(item["shape"], c[0]) < 0:
            return "it encloses an existing cutout or the whole outline"
    return None


# ── layers and nets ─────────────────────────────────────────────────────────

def layer_id(board, name):
    """KiCad layer id of a name (canonical, user name or legacy short name), or -1."""
    lid = board.GetLayerID(name)
    if lid < 0 and name in LEGACY_LAYERS:
        lid = board.GetLayerID(LEGACY_LAYERS[name])
    return lid


def copper_names(board):
    return [board.GetLayerName(l) for l in board.GetEnabledLayers().CuStack()]


def resolve_copper(board, names, field, P):
    """Enabled copper layer ids of names ("*.Cu" = every copper layer)."""
    ids = []
    enabled = list(board.GetEnabledLayers().CuStack())
    for i, n in enumerate(names):
        f = "%s[%d]" % (field, i)
        if n == "*.Cu":
            ids += [l for l in enabled if l not in ids]
            continue
        lid = layer_id(board, n)
        if lid < 0:
            P.add("E_BOARD_LAYER", f, "no layer named %r (copper layers on this board: %s)" % (n, ", ".join(copper_names(board))), FIX_LAYER)
        elif not pcbnew.IsCopperLayer(lid):
            P.add("E_BOARD_LAYER", f, "%s is not a copper layer" % n, "name copper layers: " + ", ".join(copper_names(board)))
        elif lid not in enabled:
            P.add("E_BOARD_LAYER", f, "%s is not enabled on this %d-layer board (copper: %s)" % (n, board.GetCopperLayerCount(), ", ".join(copper_names(board))), FIX_LAYER)
        elif lid not in ids:
            ids.append(lid)
    return ids


def resolve_text_layer(board, name, P):
    lid = layer_id(board, name)
    canon = board.GetLayerName(lid) if lid >= 0 else ""
    std = pcbnew.LayerName(lid) if lid >= 0 else ""
    if lid < 0:
        P.add("E_BOARD_LAYER", "layer", "no layer named %r" % name, "use one of " + ", ".join(TEXT_LAYERS))
    elif std not in TEXT_LAYERS and canon not in TEXT_LAYERS:
        P.add("E_BOARD_LAYER", "layer", "%s is not a silkscreen or fab layer" % name, "use one of " + ", ".join(TEXT_LAYERS))
    elif not board.IsLayerEnabled(lid):
        P.add("E_BOARD_LAYER", "layer", "%s is not enabled on this board" % name, "enable it in Board Setup > Board Layers, or use another of " + ", ".join(TEXT_LAYERS))
    return lid


def find_net(board, name, field, P):
    nets = {str(k): v for k, v in board.GetNetsByName().items() if str(k)}
    if name in nets:
        return nets[name]
    near = sorted(n for n in nets if n.lower() == name.lower())
    hint = (" (did you mean %s?)" % ", ".join(near)) if near else ""
    P.add("E_BOARD_NET", field, "net %r is not on the board%s; the board has %d net(s)" % (name, hint, len(nets)),
          "use a net from `pcbpilot kicad snapshot` (netClassOf), or sync the schematic first (Update PCB from Schematic)")
    return None


# ── read-back comparison ────────────────────────────────────────────────────

def same(want, got, tol):
    if isinstance(want, bool) or isinstance(got, bool):
        return type(want) is type(got) and want == got
    if isinstance(want, (int, float)) and isinstance(got, (int, float)):
        return abs(want - got) <= tol
    if isinstance(want, (list, tuple)) and isinstance(got, (list, tuple)):
        return len(want) == len(got) and all(same(a, b, tol) for a, b in zip(want, got))
    if isinstance(want, dict) and isinstance(got, dict):
        return not compare("", want, got, tol)
    return want == got


def compare(label, want, got, tol=EXACT):
    """Every key of want that got does not match (numbers within tol)."""
    out = []
    for k, w in want.items():
        g = got.get(k, "<missing>")
        if not same(w, g, tol):
            out.append("%s%s: read back %s, expected %s" % (label + " " if label else "", k, json.dumps(g), json.dumps(w)))
    return out


def rnd(p):
    return [round(p[0], 6), round(p[1], 6)]


# ── board outline ───────────────────────────────────────────────────────────

def edge_drawings(board):
    """Board-level drawings on Edge.Cuts (footprint edge cuts are the footprint's own)."""
    return [d for d in board.GetDrawings() if d.GetLayer() == pcbnew.Edge_Cuts]


def board_polys(board):
    ps = pcbnew.SHAPE_POLY_SET()
    try:
        ok = board.GetBoardPolygonOutlines(ps, False)
    except TypeError:
        ok = board.GetBoardPolygonOutlines(ps)
    if not ok or ps.OutlineCount() == 0:
        return None
    return ps


def largest(ps):
    return max(range(ps.OutlineCount()), key=lambda i: abs(ps.Outline(i).Area()))


def chain_mm(ch):
    return [(mm(ch.CPoint(i).x), mm(ch.CPoint(i).y)) for i in range(ch.PointCount())]


def board_contours(ps):
    """Every outline and cutout of the board as a point list (mm). Arcs in
    Edge.Cuts arrive as KiCad's own polyline (its max-error approximation)."""
    out = []
    for i in range(ps.OutlineCount()):
        out.append(chain_mm(ps.Outline(i)))
        for j in range(ps.HoleCount(i)):
            out.append(chain_mm(ps.Hole(i, j)))
    return out


def fits_board(ps, item):
    return fits(board_contours(ps), lambda p: ps.Contains(vec(p)), item)


def add_prims(board, prims, layer, width):
    made = []
    for p in prims:
        s = pcbnew.PCB_SHAPE(board)
        k = p["kind"]
        if k == "segment":
            s.SetShape(pcbnew.SHAPE_T_SEGMENT)
            s.SetStart(vec(p["start"]))
            s.SetEnd(vec(p["end"]))
        elif k == "arc":
            s.SetShape(pcbnew.SHAPE_T_ARC)
            s.SetArcGeometry(vec(p["start"]), vec(p["mid"]), vec(p["end"]))
        elif k == "circle":
            s.SetShape(pcbnew.SHAPE_T_CIRCLE)
            c = p["center"]
            s.SetCenter(vec(c))
            s.SetEnd(vec((c[0] + p["radius"], c[1])))
        else:
            raise ValueError("unknown primitive %r" % k)
        s.SetLayer(layer)
        s.SetWidth(nm(width))
        board.Add(s)
        made.append({"kind": "edge-" + k, "uuid": uuid_of(s)})
    return made


def read_edge(d):
    k = d.GetShape()
    if k == pcbnew.SHAPE_T_SEGMENT:
        return {"kind": "segment", "start": xy_mm(d.GetStart()), "end": xy_mm(d.GetEnd())}
    if k == pcbnew.SHAPE_T_ARC:
        return {"kind": "arc", "start": xy_mm(d.GetStart()), "mid": xy_mm(d.GetArcMid()), "end": xy_mm(d.GetEnd())}
    if k == pcbnew.SHAPE_T_CIRCLE:
        return {"kind": "circle", "center": xy_mm(d.GetCenter()), "radius": mm(d.GetRadius())}
    return {"kind": "shape %d" % k}


def canon_edge(e):
    """e with its end points in a fixed order: KiCad stores arcs counter-
    clockwise (it swaps start and end of a clockwise one) and the direction
    of an outline piece does not matter."""
    e = dict(e)
    if "start" in e and "end" in e and list(e["end"]) < list(e["start"]):
        e["start"], e["end"] = e["end"], e["start"]
    return e


def expect_edge(p):
    return canon_edge({k: v for k, v in p.items() if k in ("kind", "start", "mid", "end", "center", "radius")})


def fill_zones(board):
    """Regenerate every copper zone fill: unfill first (so a fill that is
    only still present cannot pass), fill with pcbnew's filler, and fail
    unless it reports success and every zone is filled again."""
    zones = board.Zones()
    copper = [z for z in zones if not z.GetIsRuleArea()]
    for z in copper:
        z.UnFill()
    try:
        ok = pcbnew.ZONE_FILLER(board).Fill(zones)
    except Exception as e:
        raise VerifyError("the zone filler failed: %s: %s" % (type(e).__name__, e))
    if not ok:
        raise VerifyError("the zone filler reported failure")
    stale = [z.GetZoneName() or uuid_of(z) for z in copper if not z.IsFilled()]
    if stale:
        raise VerifyError("zones not filled after the fill: %s" % ", ".join(stale))
    return len(copper)


def refill_filled(board, res):
    """Refill the board's zones when any is filled: a new edge, hole or
    keepout changes what the fills may cover, and a stale fill is a DRC
    error (zone in a keepout, unconnected new PTH pad)."""
    if not any(z.IsFilled() for z in board.Zones() if not z.GetIsRuleArea()):
        return res
    n = fill_zones(board)
    res.setdefault("warnings", []).append("refilled the board's %d zone(s): the change affects their fills" % n)
    res.setdefault("summary", {})["zonesRefilled"] = n
    res["filled"] = True
    return res


def op_outline(board, spec, P):
    existing = edge_drawings(board)
    outer = spec.get("outer")
    cutouts = spec.get("cutouts") or []
    holes_before = 0
    if outer:
        if existing and not spec.get("replace"):
            P.add("E_BOARD_OUTLINE_EXISTS", "outline", "Edge.Cuts already has %d drawing(s) on this board" % len(existing),
                  "add --replace to remove them and draw the new outline, or give cutouts only to add to the existing outline")
    else:
        ps = board_polys(board)
        if ps is None:
            P.add("E_BOARD_NO_OUTLINE", "cutouts", "the board has no closed outline to cut into",
                  "draw the outline first (`kicad board outline --rect W,H`) or give it together with the cutouts")
        else:
            holes_before = ps.HoleCount(largest(ps))
            for i, c in enumerate(cutouts):
                why = fits_board(ps, c)
                if why:
                    P.add("E_BOARD_GEOMETRY", "cutouts[%d]" % i, "the %s cutout does not fit: %s" % (c["kind"], why),
                          "move it inside the outline, away from the edge and other cutouts")
    if P:
        return None
    removed = []
    if outer and spec.get("replace"):
        for d in existing:
            removed.append({"kind": "edge", "uuid": uuid_of(d)})
            board.Remove(d)
    created, want = [], {}
    w = spec.get("lineWidth") or 0.05
    for shp in ([outer] if outer else []) + cutouts:
        made = add_prims(board, shp["prims"], pcbnew.Edge_Cuts, w)
        for m, p in zip(made, shp["prims"]):
            want[m["uuid"]] = expect_edge(p)
        created += made
    expect = {"holes": holes_before + len(cutouts), "edges": want, "lineWidth": w}
    if outer:
        expect["bbox"] = outer["bbox"]
    return refill_filled(board, {"created": created, "removed": removed, "expect": expect})


def verify_outline(board, spec, res, V):
    ps = board_polys(board)
    if ps is None:
        V.append("no closed board outline after the write")
        return
    i = largest(ps)
    exp = res["expect"]
    if ps.HoleCount(i) != exp["holes"]:
        V.append("the outline has %d cutout(s), expected %d" % (ps.HoleCount(i), exp["holes"]))
    if "bbox" in exp:
        bb = ps.Outline(i).BBox()
        got = [mm(bb.GetX()), mm(bb.GetY()), mm(bb.GetX() + bb.GetWidth()), mm(bb.GetY() + bb.GetHeight())]
        if not same(exp["bbox"], got, 0.01):
            V.append("outline bbox %s, expected %s (mm)" % (got, exp["bbox"]))
    drawings = {uuid_of(d): d for d in board.GetDrawings()}
    for u, want in exp["edges"].items():
        d = drawings.get(u)
        if d is None or d.GetLayer() != pcbnew.Edge_Cuts:
            V.append("Edge.Cuts drawing %s missing after the write" % u)
            continue
        V += compare("edge " + u, want, canon_edge(read_edge(d)), ARC_TOL if want["kind"] == "arc" else EXACT)
        V += compare("edge " + u, {"width": exp["lineWidth"]}, {"width": mm(d.GetWidth())})
    for r in res.get("removed", []):
        if r["uuid"] in drawings:
            V.append("replaced Edge.Cuts drawing %s is still on the board" % r["uuid"])
    res.setdefault("summary", {}).update({"outlines": ps.OutlineCount(), "cutouts": ps.HoleCount(i)})


# ── stackup ─────────────────────────────────────────────────────────────────

def layers_in_use(board, drop):
    """{kind: count} of items on any of the copper layers in drop."""
    use = {}

    def hit(kind):
        use[kind] = use.get(kind, 0) + 1
    for t in board.GetTracks():
        if t.Type() == pcbnew.PCB_VIA_T:
            if t.GetViaType() != pcbnew.VIATYPE_THROUGH and (t.TopLayer() in drop or t.BottomLayer() in drop):
                hit("blind/buried vias")
        elif t.GetLayer() in drop:
            hit("tracks")
    for z in board.Zones():
        if any(l in drop for l in z.GetLayerSet().CuStack()):
            hit("rule areas" if z.GetIsRuleArea() else "zones")
    for d in board.GetDrawings():
        if d.GetLayer() in drop:
            hit("drawings")
    enabled = set(board.GetEnabledLayers().CuStack())
    for fp in board.GetFootprints():
        for p in fp.Pads():
            cu = set(p.GetLayerSet().CuStack())
            # A plated or unplated hole through every copper layer ("*.Cu")
            # simply loses the dropped layers; any other pad that names a
            # dropped layer (SMD, connector, a hole on chosen layers) uses it.
            if p.GetAttribute() in (pcbnew.PAD_ATTRIB_PTH, pcbnew.PAD_ATTRIB_NPTH) and cu >= enabled:
                continue
            if cu & set(drop):
                hit("pads")
        for g in fp.GraphicalItems():
            if g.GetLayer() in drop:
                hit("footprint graphics")
    return use


def splice_stackup(text, block):
    """text with the (stackup ...) of its (setup ...) replaced by block (or
    inserted as the first child of setup)."""
    setup = next((c for c in sexpr_children(text, text.index("(")) if c[2] == "setup"), None)
    if setup is None:
        raise ValueError("the saved board has no (setup ...) section")
    st = next((c for c in sexpr_children(text, setup[0]) if c[2] == "stackup"), None)
    if st is not None:
        return text[:st[0]] + block + text[st[1]:]
    head = setup[0] + len("(setup")
    return text[:head] + "\n" + block + text[head:]


def sexpr_children(text, at):
    """[(open, end, head)] of the lists directly inside the list that opens
    at text[at] ("("): end is one past the closing paren, head the list's
    first atom. Quoted strings (with backslash escapes) are skipped."""
    out, depth, start, i, n = [], 0, None, at, len(text)
    while i < n:
        c = text[i]
        if c == '"':
            i += 1
            while i < n and text[i] != '"':
                i += 2 if text[i] == "\\" else 1
        elif c == "(":
            depth += 1
            if depth == 2:
                start = i
        elif c == ")":
            if depth == 2 and start is not None:
                k = start + 1
                while k < n and text[k] not in " \t\r\n()\"":
                    k += 1
                out.append((start, i + 1, text[start + 1:k]))
                start = None
            depth -= 1
            if depth == 0:
                break
        i += 1
    return out


def stackup_text(text):
    """The (stackup ...) block of a board file, or ""."""
    setup = next((c for c in sexpr_children(text, text.index("(")) if c[2] == "setup"), None)
    if setup is None:
        return ""
    st = next((c for c in sexpr_children(text, setup[0]) if c[2] == "stackup"), None)
    return text[st[0]:st[1]] if st else ""


STACKUP_FIELDS = {"type": "type", "thickness": "thickness", "material": "material", "epsilon_r": "epsilonR", "loss_tangent": "lossTangent",
                  "color": "color"}


def read_stackup(block):
    """(rows, finish) of a (stackup ...) block: each layer's name, type,
    thickness (0 when absent) and, where written, material / epsilonR /
    lossTangent."""
    rows = []
    for s, e, head in sexpr_children(block, 0):
        if head != "layer":
            continue
        chunk = block[s:e]
        m = re.match(r'\(layer\s+"([^"]*)"', chunk)
        row = {"name": m.group(1) if m else "", "thickness": 0}
        for fs, fe, fh in sexpr_children(chunk, 0):
            if fh in STACKUP_FIELDS:
                v = chunk[fs + 1 + len(fh):fe - 1].strip()
                if v.startswith('"'):
                    v = v[1:-1]
                else:
                    try:
                        v = float(v)
                    except ValueError:
                        pass
                row[STACKUP_FIELDS[fh]] = v
        rows.append(row)
    f = re.search(r'\(copper_finish\s+"([^"]*)"\)', block)
    return rows, f.group(1) if f else None


def check_stackup_rows(want_rows, got_rows, want_finish, got_finish):
    V = []
    if [r["name"] for r in got_rows] != [r["name"] for r in want_rows]:
        V.append("stackup layers %s, expected %s" % ([r["name"] for r in got_rows], [r["name"] for r in want_rows]))
        return V
    for w, g in zip(want_rows, got_rows):
        V += compare("stackup " + w["name"], w, g)
    if got_finish != want_finish:
        V.append("stackup copper_finish %r, expected %r" % (got_finish, want_finish))
    return V


def op_stackup(board, spec, P):
    n = spec["layers"]
    cur = board.GetCopperLayerCount()
    if n < cur:
        drop = [getattr(pcbnew, "In%d_Cu" % k) for k in range(n - 1, cur - 1)]
        use = layers_in_use(board, drop)
        if use:
            P.add("E_BOARD_LAYER_IN_USE", "layers", "going from %d to %d copper layers removes %s, which still carry %s" % (
                cur, n, ", ".join(board.GetLayerName(l) for l in drop), ", ".join("%d %s" % (v, k) for k, v in sorted(use.items()))),
                "move or delete those items in KiCad first (or keep %d layers)" % cur)
    if P:
        return None
    board.SetCopperLayerCount(n)
    board.GetDesignSettings().SetBoardThickness(nm(spec["thickness"]))
    return {"created": [], "removed": [], "summary": {"copperLayersBefore": cur, "copperLayers": n, "thickness": spec["thickness"], "finish": spec["finish"]}}


def write_stackup(out, block):
    with open(out, encoding="utf-8", newline="") as f:
        text = f.read()
    text = splice_stackup(text, block)
    with open(out, "w", encoding="utf-8", newline="") as f:
        f.write(text)
    # pcbnew re-reads the stackup and writes it in its own layout.
    b = pcbnew.LoadBoard(out)
    pcbnew.SaveBoard(out, b)


def verify_stackup(board, spec, out, res, V):
    if board.GetCopperLayerCount() != spec["layers"]:
        V.append("%d copper layers after the write, expected %d" % (board.GetCopperLayerCount(), spec["layers"]))
    ds = board.GetDesignSettings()
    if not ds.m_HasStackup:
        V.append("the saved board has no stackup")
    if abs(mm(ds.GetBoardThickness()) - spec["thickness"]) > EXACT:
        V.append("board thickness %.6f mm, expected %.6f mm" % (mm(ds.GetBoardThickness()), spec["thickness"]))
    with open(out, encoding="utf-8") as f:
        rows, finish = read_stackup(stackup_text(f.read()))
    V += check_stackup_rows(spec["rows"], rows, spec["finish"], finish)
    res["summary"]["stackupLayers"] = len(rows)


# ── holes ───────────────────────────────────────────────────────────────────

def via_width(v):
    try:
        return v.GetWidth(pcbnew.F_Cu)
    except TypeError:
        return v.GetWidth()


def hole_conflicts(board, h):
    """Why the new hole h cannot go where it is — only definite physical
    conflicts, never a clearance: KiCad's DRC (netclasses, custom rules,
    hole-to-hole, hole clearance) runs on the result and decides those. A
    conflict is a drill overlapping another drill or any copper, or a PTH
    ring overlapping copper of another net (or of no net)."""
    x, y, rd, rp = h["x"], h["y"], h["drill"] / 2, h["pad"] / 2
    pth, net = h["type"] == "pth", h.get("net") or ""
    # A PTH ring may touch same-net copper (a stitched mounting hole).
    ring = lambda other: pth and not (net and other == net)
    out = []
    for t in board.GetTracks():
        if t.Type() != pcbnew.PCB_VIA_T:
            d = track_dist(t, (x, y)) - mm(t.GetWidth()) / 2
            at = "the %s on %s at (%s, %s)-(%s, %s)" % ("arc" if t.Type() == pcbnew.PCB_ARC_T else "track", pcbnew.LayerName(t.GetLayer()),
                                                          fmt_num(mm(t.GetStart().x)), fmt_num(mm(t.GetStart().y)), fmt_num(mm(t.GetEnd().x)), fmt_num(mm(t.GetEnd().y)))
            if d < rd:
                out.append("the drill cuts the copper of %s" % at)
            elif ring(t.GetNetname()) and d < rp:
                out.append("the pad overlaps %s (net %s)" % (at, t.GetNetname() or "none"))
            continue
        vx, vy = mm(t.GetPosition().x), mm(t.GetPosition().y)
        d = math.hypot(vx - x, vy - y)
        vcu, vdr = mm(via_width(t)) / 2, mm(t.GetDrillValue()) / 2
        at = "the via at (%s, %s)" % (fmt_num(vx), fmt_num(vy))
        if d < rd + vdr:
            out.append("the drill overlaps the drill of %s" % at)
        elif d < rd + vcu:
            out.append("the drill cuts the copper of %s" % at)
        elif ring(t.GetNetname()) and d < rp + vcu:
            out.append("the pad overlaps the copper of %s (net %s)" % (at, t.GetNetname() or "none"))
    enabled = set(board.GetEnabledLayers().CuStack())
    for fp in board.GetFootprints():
        for p in fp.Pads():
            px, py = mm(p.GetPosition().x), mm(p.GetPosition().y)
            pd = mm(max(p.GetDrillSizeX(), p.GetDrillSizeY())) / 2
            if pd > 0 and math.hypot(px - x, py - y) < rd + pd:
                out.append("the drill overlaps the drill of %s pad %s" % (fp.GetReference(), p.GetNumber()))
                break
            r = rp if ring(p.GetNetname()) else rd
            if pth and not ring(p.GetNetname()):
                continue  # same net: copper may touch; DRC judges the drill
            if any(pad_copper_hits(p, l, x, y, r) for l in p.GetLayerSet().CuStack() if l in enabled):
                what = "pad" if pth else "drill"
                out.append("the %s overlaps the copper of %s pad %s (net %s)" % (what, fp.GetReference(), p.GetNumber(), p.GetNetname() or "none"))
                break
    return out


def pad_copper_hits(p, layer, x, y, r):
    """The disk (x, y, r) meets the pad's real copper on layer. KiCad's
    effective polygon (custom shapes, rings, rounded corners; error inside
    the true shape, so never larger) is tested exactly; a pad without one
    is left to DRC."""
    try:
        ps = p.GetEffectivePolygon(layer, pcbnew.ERROR_INSIDE)
    except Exception:
        return False
    if ps is None or ps.OutlineCount() == 0:
        return False
    if ps.Contains(vec((x, y))):
        return True
    for i in range(ps.OutlineCount()):
        for c in [chain_mm(ps.Outline(i))] + [chain_mm(ps.Hole(i, j)) for j in range(ps.HoleCount(i))]:
            for k in range(len(c)):
                if pt_seg((x, y), c[k], c[(k + 1) % len(c)]) < r:
                    return True
    return False


def track_dist(t, p):
    """Distance (mm) from p to the centre line of a track segment or arc."""
    a, b = xy_mm(t.GetStart()), xy_mm(t.GetEnd())
    if t.Type() == pcbnew.PCB_ARC_T:
        c, m = xy_mm(t.GetCenter()), xy_mm(t.GetMid())
        ang = lambda q: math.atan2(q[1] - c[1], q[0] - c[0])
        a0, a1, am = ang(a), ang(b), ang(m)
        sw = math.fmod(a1 - a0, 2 * math.pi)
        if sw < 0:
            sw += 2 * math.pi
        arc = [c[0], c[1], dist(a, c), a0, sw]
        if not in_span(arc, am):
            arc[4] = sw - 2 * math.pi  # the arc runs the other way round
        return pt_arc(p, arc)
    return pt_seg(p, a, b)


def next_refs(board, count, taken):
    used = {fp.GetReference() for fp in board.GetFootprints()} | set(taken)
    out, k = [], 1
    while len(out) < count:
        r = "H%d" % k
        if r not in used:
            out.append(r)
            used.add(r)
        k += 1
    return out


def op_holes(board, spec, P):
    holes = spec["holes"]
    ps = board_polys(board)
    if ps is None:
        what = "Edge.Cuts is not one closed outline" if edge_drawings(board) else "the board has no outline"
        P.add("E_BOARD_NO_OUTLINE", "holes", "%s: hole positions cannot be checked" % what,
              "draw a closed outline first (`kicad board outline`)")
        return None
    used = {fp.GetReference() for fp in board.GetFootprints()}
    nets = []
    for i, h in enumerate(holes):
        f = "holes[%d]" % i
        at = "the hole at (%s, %s)" % (fmt_num(h["x"]), fmt_num(h["y"]))
        net = None
        if h.get("net"):
            net = find_net(board, h["net"], f + ".net", P)
        nets.append(net)
        if h.get("ref") and h["ref"] in used:
            P.add("E_BOARD_REF_EXISTS", f + ".ref", "%s is already on the board" % h["ref"], "give another designator or omit it (the next free H<n> is used)")
        why = fits_board(ps, h)
        if why:
            P.add("E_BOARD_GEOMETRY", f, "%s does not fit on the board: %s" % (at, why), "move it inside the outline, away from the edge and cutouts")
        for why in hole_conflicts(board, h):
            P.add("E_BOARD_OVERLAP", f, "%s: %s" % (at, why), "move the hole clear of existing pads, vias, tracks and holes (clearances are then checked by DRC)")
    if P:
        return None
    auto = next_refs(board, sum(1 for h in holes if not h.get("ref")), [h["ref"] for h in holes if h.get("ref")])
    created = []
    for h, net in zip(holes, nets):
        ref = h.get("ref") or auto.pop(0)
        fp = pcbnew.FOOTPRINT(board)
        pth = h["type"] == "pth"
        name = "MountingHole_%smm%s" % (fmt_num(h["drill"]), ("_Pad%smm" % fmt_num(h["pad"])) if pth else "")
        fp.SetFPIDAsString("pcbpilot:" + name)
        fp.SetReference(ref)
        fp.SetValue("MountingHole")
        at = vec((h["x"], h["y"]))
        fp.SetPosition(at)
        pad = pcbnew.PAD(fp)
        pad.SetShape(pcbnew.PAD_SHAPE_CIRCLE)
        pad.SetDrillSize(pcbnew.VECTOR2I(nm(h["drill"]), nm(h["drill"])))
        pad.SetSize(pcbnew.VECTOR2I(nm(h["pad"]), nm(h["pad"])))
        if pth:
            pad.SetAttribute(pcbnew.PAD_ATTRIB_PTH)
            pad.SetLayerSet(pcbnew.PAD.PTHMask())
            pad.SetNumber("1")
        else:
            pad.SetAttribute(pcbnew.PAD_ATTRIB_NPTH)
            # "*.Cu *.Mask" like KiCad's own MountingHole NPTHs (pcbnew
            # writes UnplatedHoleMask() back as that anyway).
            pad.SetLayerSet(pcbnew.PAD.PTHMask())
            pad.SetNumber("")
        pad.SetPosition(at)
        if net is not None:
            pad.SetNetCode(net.GetNetCode())
        fp.Add(pad)
        cy = pcbnew.PCB_SHAPE(fp)
        cy.SetShape(pcbnew.SHAPE_T_CIRCLE)
        cy.SetLayer(pcbnew.F_CrtYd)
        cy.SetCenter(at)
        cy.SetEnd(vec((h["x"] + h["pad"] / 2 + 0.25, h["y"])))
        cy.SetWidth(nm(0.05))
        fp.Add(cy)
        ref_t, val_t = fp.Reference(), fp.Value()
        ref_t.SetLayer(pcbnew.F_Fab)
        ref_t.SetPosition(vec((h["x"], h["y"] - h["pad"] / 2 - 1.0)))
        val_t.SetLayer(pcbnew.F_Fab)
        val_t.SetPosition(vec((h["x"], h["y"] + h["pad"] / 2 + 1.0)))
        val_t.SetVisible(False)
        fp.SetAttributes(pcbnew.FP_BOARD_ONLY | pcbnew.FP_EXCLUDE_FROM_BOM | pcbnew.FP_EXCLUDE_FROM_POS_FILES)
        fp.SetLocked(True)
        board.Add(fp)
        created.append({"kind": "mounting-hole", "uuid": uuid_of(fp), "name": ref, "parts": [uuid_of(pad)]})
    return refill_filled(board, {"created": created, "removed": []})


def hole_layers(pth):
    """Layer ids a mounting hole pad is on (sorted)."""
    return sorted(pcbnew.PAD.PTHMask().Seq())


def expect_hole(h, ref, layers):
    pth = h["type"] == "pth"
    return {"ref": ref, "x": h["x"], "y": h["y"], "padPos": [h["x"], h["y"]], "drill": [h["drill"], h["drill"]],
            "pad": [h["pad"], h["pad"]], "type": h["type"], "net": h.get("net") or "", "locked": True, "pads": 1,
            "boardOnly": True, "excludeFromBOM": True, "excludeFromPos": True, "shape": "circle",
            "number": "1" if pth else "", "layers": layers}


def pad_shape(p):
    try:
        k = p.GetShape()
    except TypeError:  # KiCad 10 padstacks: the front layer's shape
        k = p.GetFrontShape()
    return {pcbnew.PAD_SHAPE_CIRCLE: "circle", pcbnew.PAD_SHAPE_RECTANGLE: "rect", pcbnew.PAD_SHAPE_OVAL: "oval",
            pcbnew.PAD_SHAPE_ROUNDRECT: "roundrect"}.get(k, "shape %d" % k)


def read_hole(fp):
    pads = list(fp.Pads())
    a = fp.GetAttributes()
    got = {"ref": fp.GetReference(), "x": mm(fp.GetPosition().x), "y": mm(fp.GetPosition().y), "locked": bool(fp.IsLocked()), "pads": len(pads),
           "boardOnly": bool(a & pcbnew.FP_BOARD_ONLY), "excludeFromBOM": bool(a & pcbnew.FP_EXCLUDE_FROM_BOM),
           "excludeFromPos": bool(a & pcbnew.FP_EXCLUDE_FROM_POS_FILES)}
    if pads:
        p = pads[0]
        attr = p.GetAttribute()
        got.update({"padPos": xy_mm(p.GetPosition()), "drill": [mm(p.GetDrillSizeX()), mm(p.GetDrillSizeY())],
                    "pad": [mm(p.GetSizeX()), mm(p.GetSizeY())], "net": p.GetNetname(),
                    "type": "pth" if attr == pcbnew.PAD_ATTRIB_PTH else "npth" if attr == pcbnew.PAD_ATTRIB_NPTH else "attr %d" % attr,
                    "shape": pad_shape(p), "number": p.GetNumber(), "layers": sorted(p.GetLayerSet().Seq())})
    return got


def verify_holes(board, spec, res, V):
    fps = {uuid_of(fp): fp for fp in board.GetFootprints()}
    for c, h in zip(res["created"], spec["holes"]):
        fp = fps.get(c["uuid"])
        if fp is None:
            V.append("hole %s missing after the write" % c["name"])
            continue
        V += compare("hole " + c["name"], expect_hole(h, c["name"], hole_layers(h["type"] == "pth")), read_hole(fp))


# ── keepout and zone ────────────────────────────────────────────────────────

RULES = {"tracks": "SetDoNotAllowTracks", "vias": "SetDoNotAllowVias", "pads": "SetDoNotAllowPads",
         "pours": "SetDoNotAllowZoneFills", "footprints": "SetDoNotAllowFootprints"}
GETTERS = {"tracks": "GetDoNotAllowTracks", "vias": "GetDoNotAllowVias", "pads": "GetDoNotAllowPads",
           "pours": "GetDoNotAllowZoneFills", "footprints": "GetDoNotAllowFootprints"}
CONNECTIONS = {"thermal": "ZONE_CONNECTION_THERMAL", "solid": "ZONE_CONNECTION_FULL", "none": "ZONE_CONNECTION_NONE",
               "tht-thermal": "ZONE_CONNECTION_THT_THERMAL"}


def layer_set(ids):
    ls = pcbnew.LSET()
    for l in ids:
        ls.AddLayer(l)
    return ls


def set_outline(z, pts):
    o = z.Outline()
    o.NewOutline()
    for p in pts:
        o.Append(nm(p[0]), nm(p[1]))


def zone_polygon(z):
    o = z.Outline()
    if o.OutlineCount() != 1:
        return "%d outlines" % o.OutlineCount()
    if o.HoleCount(0):
        return "%d holes" % o.HoleCount(0)
    return [rnd(p) for p in chain_mm(o.Outline(0))]


def op_keepout(board, spec, P):
    ids = resolve_copper(board, spec["layers"], "layers", P)
    if P:
        return None
    z = pcbnew.ZONE(board)
    z.SetIsRuleArea(True)
    z.SetLayerSet(layer_set(ids))
    for r, setter in RULES.items():
        getattr(z, setter)(r in spec["forbid"])
    set_outline(z, spec["shape"]["samples"])
    z.SetZoneName(spec.get("name") or "")
    board.Add(z)
    names = sorted(board.GetLayerName(l) for l in ids)
    return refill_filled(board, {"created": [{"kind": "rule-area", "uuid": uuid_of(z), "name": spec.get("name", "")}], "removed": [],
                                 "summary": {"layers": names, "forbid": spec["forbid"]}})


def expect_keepout(spec, layers):
    w = {"ruleArea": True, "layers": layers, "polygon": [rnd(p) for p in spec["shape"]["samples"]], "name": spec.get("name") or ""}
    for r in RULES:
        w["no " + r] = r in spec["forbid"]
    return w


def read_keepout(board, z):
    g = {"ruleArea": bool(z.GetIsRuleArea()), "layers": sorted(board.GetLayerName(l) for l in z.GetLayerSet().CuStack()),
         "polygon": zone_polygon(z), "name": z.GetZoneName()}
    for r, getter in GETTERS.items():
        g["no " + r] = bool(getattr(z, getter)())
    return g


def verify_keepout(board, spec, res, V):
    z = next((z for z in board.Zones() if uuid_of(z) == res["created"][0]["uuid"]), None)
    if z is None:
        V.append("rule area missing after the write")
        return
    V += compare("rule area", expect_keepout(spec, res["summary"]["layers"]), read_keepout(board, z))


def op_zone(board, spec, P):
    net = find_net(board, spec["net"], "net", P)
    ids = resolve_copper(board, spec["layers"], "layers", P)
    pts = None
    if spec.get("boardOutline"):
        ps = board_polys(board)
        if ps is None:
            P.add("E_BOARD_NO_OUTLINE", "boardOutline", "the board has no closed outline to pour into",
                  "draw it first (`kicad board outline`) or give the zone a shape")
        else:
            pts = chain_mm(ps.Outline(largest(ps)))
    else:
        pts = spec["shape"]["samples"]
    if P:
        return None
    z = pcbnew.ZONE(board)
    z.SetLayerSet(layer_set(ids))
    z.SetNetCode(net.GetNetCode())
    set_outline(z, pts)
    z.SetLocalClearance(nm(spec["clearance"]))
    z.SetMinThickness(nm(spec["minWidth"]))
    z.SetThermalReliefGap(nm(spec["thermalGap"]))
    z.SetThermalReliefSpokeWidth(nm(spec["spokeWidth"]))
    z.SetPadConnection(getattr(pcbnew, CONNECTIONS[spec["connection"]]))
    z.SetAssignedPriority(int(spec["priority"]))
    z.SetZoneName(spec.get("name") or "")
    board.Add(z)
    filled, warnings = 0, []
    if spec["fill"]:
        filled = fill_zones(board)
        if filled > 1:
            warnings.append("all %d zones of the board were refilled (KiCad fills by priority across zones)" % filled)
    return {"created": [{"kind": "zone", "uuid": uuid_of(z), "name": spec.get("name", "")}], "removed": [], "warnings": warnings,
            "filled": bool(spec["fill"]), "expectPolygon": [rnd(p) for p in pts],
            "summary": {"net": spec["net"], "layers": sorted(board.GetLayerName(l) for l in ids), "zonesFilled": filled}}


def expect_zone(spec, layers, polygon):
    return {"ruleArea": False, "net": spec["net"], "layers": layers, "polygon": polygon, "clearance": spec["clearance"],
            "minWidth": spec["minWidth"], "thermalGap": spec["thermalGap"], "spokeWidth": spec["spokeWidth"],
            "connection": spec["connection"], "priority": int(spec["priority"]), "name": spec.get("name") or "",
            "filled": bool(spec["fill"])}


def read_zone(board, z):
    conn = {getattr(pcbnew, v): k for k, v in CONNECTIONS.items()}
    clr = z.GetLocalClearance()  # std::optional<int> in 10.0: an int or None
    return {"ruleArea": bool(z.GetIsRuleArea()), "net": z.GetNetname(),
            "layers": sorted(board.GetLayerName(l) for l in z.GetLayerSet().CuStack()), "polygon": zone_polygon(z),
            "clearance": None if clr is None else mm(int(clr)), "minWidth": mm(z.GetMinThickness()),
            "thermalGap": mm(z.GetThermalReliefGap()), "spokeWidth": mm(z.GetThermalReliefSpokeWidth()),
            "connection": conn.get(z.GetPadConnection(), "connection %d" % z.GetPadConnection()),
            "priority": z.GetAssignedPriority(), "name": z.GetZoneName(), "filled": bool(z.IsFilled())}


def verify_zone(board, spec, res, V):
    z = next((z for z in board.Zones() if uuid_of(z) == res["created"][0]["uuid"]), None)
    if z is None:
        V.append("zone missing after the write")
        return
    V += compare("zone", expect_zone(spec, res["summary"]["layers"], res["expectPolygon"]), read_zone(board, z))
    if spec["fill"]:
        per = {}
        for l in z.GetLayerSet().CuStack():
            per[board.GetLayerName(l)] = round(abs(z.GetFilledPolysList(l).Area()) / 1e12, 3) if z.HasFilledPolysForLayer(l) else 0.0
        res["summary"]["filledAreaMm2"] = round(sum(per.values()), 3)
        res["summary"]["filledAreaByLayerMm2"] = per
        for name in res["summary"]["layers"]:
            if per.get(name, 0) <= 0:
                V.append("the zone filled to nothing on %s (outside the board, or covered there by a keepout that forbids pours)" % name)


# ── text ────────────────────────────────────────────────────────────────────

JUSTIFY = {"left": "GR_TEXT_H_ALIGN_LEFT", "center": "GR_TEXT_H_ALIGN_CENTER", "right": "GR_TEXT_H_ALIGN_RIGHT"}


def op_text(board, spec, P):
    lid = resolve_text_layer(board, spec["layer"], P)
    if P:
        return None
    t = pcbnew.PCB_TEXT(board)
    t.SetText(spec["text"])
    t.SetLayer(lid)
    t.SetTextSize(pcbnew.VECTOR2I(nm(spec["size"]), nm(spec["size"])))
    t.SetTextThickness(nm(spec["thickness"]))
    t.SetPosition(vec((spec["x"], spec["y"])))
    t.SetTextAngleDegrees(float(spec["rotation"]))
    t.SetHorizJustify(getattr(pcbnew, JUSTIFY[spec["justify"]]))
    mirror = spec.get("mirror")
    if mirror is None:
        mirror = pcbnew.IsBackLayer(lid)
    t.SetMirrored(bool(mirror))
    board.Add(t)
    warnings = []
    ds = board.GetDesignSettings()
    if pcbnew.LayerName(lid).endswith("Silkscreen") and (nm(spec["size"]) < ds.m_MinSilkTextHeight or nm(spec["thickness"]) < ds.m_MinSilkTextThickness):
        warnings.append("text %.3g mm / stroke %.3g mm is below the board's minimum silk text %.3g mm / %.3g mm (KiCad DRC text_height / text_thickness)" % (
            spec["size"], spec["thickness"], mm(ds.m_MinSilkTextHeight), mm(ds.m_MinSilkTextThickness)))
    ps = board_polys(board)
    if ps is not None and not ps.Contains(vec((spec["x"], spec["y"]))):
        warnings.append("the text anchor is outside the board outline")
    return {"created": [{"kind": "text", "uuid": uuid_of(t), "name": spec["text"][:40]}], "removed": [], "warnings": warnings,
            "summary": {"layer": board.GetLayerName(lid), "mirrored": bool(mirror)}}


def expect_text(spec, layer, mirrored):
    return {"text": spec["text"], "layer": layer, "x": spec["x"], "y": spec["y"], "size": [spec["size"], spec["size"]],
            "thickness": spec["thickness"], "rotation": round(float(spec["rotation"]) % 360, 6), "justify": spec["justify"],
            "mirrored": mirrored}


def read_text(board, t):
    just = {getattr(pcbnew, v): k for k, v in JUSTIFY.items()}
    return {"text": t.GetText(), "layer": board.GetLayerName(t.GetLayer()), "x": mm(t.GetPosition().x), "y": mm(t.GetPosition().y),
            "size": [mm(t.GetTextSize().x), mm(t.GetTextSize().y)], "thickness": mm(t.GetTextThickness()),
            "rotation": round(t.GetTextAngleDegrees() % 360, 6), "justify": just.get(t.GetHorizJustify(), "?"),
            "mirrored": bool(t.IsMirrored())}


def verify_text(board, spec, res, V):
    t = next((d for d in board.GetDrawings() if uuid_of(d) == res["created"][0]["uuid"]), None)
    if t is None or t.GetClass() != "PCB_TEXT":
        V.append("text missing after the write")
        return
    V += compare("text", expect_text(spec, res["summary"]["layer"], res["summary"]["mirrored"]), read_text(board, t))


# ── main ────────────────────────────────────────────────────────────────────

OPS = {"outline": op_outline, "stackup": op_stackup, "holes": op_holes, "keepout": op_keepout, "zone": op_zone, "text": op_text}
VERIFY = {"outline": verify_outline, "holes": verify_holes, "keepout": verify_keepout, "zone": verify_zone, "text": verify_text}


def verify_problems(details):
    return [{"code": "E_BOARD_VERIFY", "field": "", "detail": d, "fix": FIX_VERIFY} for d in details]


def run(op, src, spec_path, out):
    with open(spec_path, encoding="utf-8") as f:
        spec = json.load(f)
    P = Problems()
    if op == "new":
        if os.path.exists(out):
            P.add("E_BOARD_OUT_EXISTS", "out", "%s exists" % out, "choose a new path")
            return {"ok": False, "op": op, "problems": P}
        board = pcbnew.CreateEmptyBoard()
        board.SetCopperLayerCount(spec["layers"])
        res = {"created": [], "removed": [], "summary": {"copperLayers": spec["layers"]}}
    else:
        board = pcbnew.LoadBoard(src)
        try:
            res = OPS[op](board, spec, P)
        except VerifyError as e:
            return {"ok": False, "op": op, "problems": verify_problems([str(e)]), "verify": {"status": "fail", "detail": [str(e)]}}
        if P:
            return {"ok": False, "op": op, "problems": P}
    pcbnew.SaveBoard(out, board)
    if op == "stackup":
        write_stackup(out, spec["block"])
    # Verify what a fresh load of the file says, not the in-memory board.
    back = pcbnew.LoadBoard(out)
    V = []
    if op == "new":
        if back.GetCopperLayerCount() != spec["layers"]:
            V.append("%d copper layers after the write" % back.GetCopperLayerCount())
    elif op == "stackup":
        verify_stackup(back, spec, out, res, V)
    else:
        VERIFY[op](back, spec, res, V)
    if res.get("filled"):
        stale = [z.GetZoneName() or uuid_of(z) for z in back.Zones() if not z.GetIsRuleArea() and not z.IsFilled()]
        if stale:
            V.append("zones saved unfilled: %s" % ", ".join(stale))
    result = {"ok": not V, "op": op, "created": res["created"], "removed": res["removed"], "warnings": res.get("warnings", []),
              "summary": res.get("summary", {}), "verify": {"status": "fail" if V else "pass", "detail": V},
              "kicadVersion": pcbnew.Version()}
    if V:
        result["problems"] = verify_problems(V)
    return result


def main(argv):
    usage = "usage: board.py new|outline|stackup|holes|keepout|zone|text IN.kicad_pcb|- SPEC.json OUT.kicad_pcb"
    if len(argv) != 5 or argv[1] not in ("new",) + tuple(OPS):
        emit({"ok": False, "error": usage}, 2)
    try:
        res = run(*argv[1:])
    except Exception as e:  # report, never a bare traceback on stdout
        import traceback
        sys.stderr.write(traceback.format_exc())
        emit({"ok": False, "error": "%s: %s" % (type(e).__name__, e)}, 1)
    emit(res, 0 if res.get("ok") else 1)


if __name__ == "__main__":
    main(sys.argv)
