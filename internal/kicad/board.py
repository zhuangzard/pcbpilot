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
# board.go sends: every length in mm in KiCad's frame (y down), every shape
# already turned into primitives ("prims": segment / arc / circle) and a
# sampled polygon ("samples") for the containment checks. Go has checked the
# numbers and the geometry; this script checks what needs the board (enabled
# layers, nets, the existing outline, items on layers a stackup removes,
# overlaps with pads) BEFORE changing anything, then applies the op, saves
# OUT, loads OUT again and verifies it.
#
# One result line on stdout: PCBPILOT_JSON:{...}. A refusal is
# {"ok": false, "problems": [{"code", "field", "detail", "fix"}]} with exit
# status 1 and OUT not written; a verification failure is code
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

# KiCad 6-9 short layer names that 10 still writes in the file but no
# longer resolves by name.
LEGACY_LAYERS = {
    "F.SilkS": "F.Silkscreen", "B.SilkS": "B.Silkscreen", "F.Adhes": "F.Adhesive", "B.Adhes": "B.Adhesive",
    "F.CrtYd": "F.Courtyard", "B.CrtYd": "B.Courtyard", "Dwgs.User": "User.Drawings", "Cmts.User": "User.Comments",
    "Eco1.User": "User.Eco1", "Eco2.User": "User.Eco2",
}

TEXT_LAYERS = ("F.Silkscreen", "B.Silkscreen", "F.Fab", "B.Fab")

FIX_LAYER = "use a layer the board has enabled (listed in the detail); add copper layers with `pcbpilot kicad board stackup --layers N`"


def emit(obj, code=0):
    sys.stdout.write("\n" + PREFIX + json.dumps(obj, separators=(",", ":")) + "\n")
    sys.stdout.flush()
    sys.exit(code)


class Problems(list):
    def add(self, code, field, detail, fix=""):
        self.append({"code": code, "field": field, "detail": detail, "fix": fix})


def nm(v):
    return int(round(v * 1e6))


def mm(v):
    return round(v / 1e6, 6)


def vec(p):
    return pcbnew.VECTOR2I(nm(p[0]), nm(p[1]))


def uuid_of(item):
    return item.m_Uuid.AsString()


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


# ── outline ─────────────────────────────────────────────────────────────────

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


def inside_board(ps, samples):
    """Every sample point is on the board (inside an outline, outside its cutouts)."""
    return all(ps.Contains(vec(p)) for p in samples)


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


def refill_filled(board, res):
    """Refill the board's zones when any is filled: a new edge, hole or
    keepout changes what the fills may cover, and a stale fill is a DRC
    error (zone in a keepout, unconnected new PTH pad)."""
    zones = board.Zones()
    if not any(z.IsFilled() for z in zones if not z.GetIsRuleArea()):
        return res
    pcbnew.ZONE_FILLER(board).Fill(zones)
    res.setdefault("warnings", []).append("refilled the board's %d zone(s): the change affects their fills" % len(zones))
    res.setdefault("summary", {})["zonesRefilled"] = len(zones)
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
                if not inside_board(ps, c["samples"]):
                    P.add("E_BOARD_GEOMETRY", "cutouts[%d]" % i, "the %s cutout is not wholly inside the board (or overlaps an existing cutout)" % c["kind"],
                          "move it inside the outline, away from the edge and other cutouts")
    if P:
        return None
    removed = []
    if outer and spec.get("replace"):
        for d in existing:
            removed.append({"kind": "edge", "uuid": uuid_of(d)})
            board.Remove(d)
    created = []
    w = spec.get("lineWidth") or 0.05
    if outer:
        created += add_prims(board, outer["prims"], pcbnew.Edge_Cuts, w)
    for c in cutouts:
        created += add_prims(board, c["prims"], pcbnew.Edge_Cuts, w)
    want = {"holes": holes_before + len(cutouts)}
    if outer:
        want["bbox"] = outer["bbox"]
    return refill_filled(board, {"created": created, "removed": removed, "expect": want})


def verify_outline(board, res, V):
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
        if any(abs(a - b) > 0.01 for a, b in zip(got, exp["bbox"])):
            V.append("outline bbox %s, expected %s (mm)" % (got, exp["bbox"]))
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
    for fp in board.GetFootprints():
        for p in fp.Pads():
            if p.GetAttribute() == pcbnew.PAD_ATTRIB_SMD and p.GetLayer() in drop:
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


def op_stackup(board, spec, P, out):
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
    ds = board.GetDesignSettings()
    ds.SetBoardThickness(nm(spec["thickness"]))
    return {"created": [], "removed": [], "summary": {"copperLayersBefore": cur, "copperLayers": n, "thickness": spec["thickness"], "finish": spec["finish"]},
            "post": "stackup"}


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
    if abs(mm(ds.GetBoardThickness()) - spec["thickness"]) > 0.001:
        V.append("board thickness %.4f mm, expected %.4f mm" % (mm(ds.GetBoardThickness()), spec["thickness"]))
    with open(out, encoding="utf-8") as f:
        block = stackup_text(f.read())
    if '(copper_finish "%s")' % spec["finish"] not in block:
        V.append("copper finish %s not in the saved stackup" % spec["finish"])
    copper = re.findall(r'\(layer\s+"([^"]+)"\s*\(type\s+"copper"\)', block)
    want = ["F.Cu"] + ["In%d.Cu" % k for k in range(1, spec["layers"] - 1)] + ["B.Cu"]
    if copper != want:
        V.append("stackup copper layers %s, expected %s" % (copper, want))
    res["summary"]["stackupLayers"] = len(re.findall(r'\(layer\s+"', block))


# ── holes ───────────────────────────────────────────────────────────────────

def pad_hits(board, x, y, r):
    """Designators of footprints with a pad whose bbox comes within r of (x, y)."""
    hits = []
    for fp in board.GetFootprints():
        for p in fp.Pads():
            bb = p.GetBoundingBox()
            x0, y0 = mm(bb.GetX()), mm(bb.GetY())
            x1, y1 = x0 + mm(bb.GetWidth()), y0 + mm(bb.GetHeight())
            dx = max(x0 - x, 0, x - x1)
            dy = max(y0 - y, 0, y - y1)
            if math.hypot(dx, dy) < r:
                hits.append(fp.GetReference())
                break
    return hits


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


def fmt_num(v):
    return ("%.3f" % v).rstrip("0").rstrip(".")


def op_holes(board, spec, P):
    holes = spec["holes"]
    ps = board_polys(board)
    used = {fp.GetReference() for fp in board.GetFootprints()}
    nets = []
    for i, h in enumerate(holes):
        f = "holes[%d]" % i
        net = None
        if h.get("net"):
            net = find_net(board, h["net"], f + ".net", P)
        nets.append(net)
        if h.get("ref") and h["ref"] in used:
            P.add("E_BOARD_REF_EXISTS", f + ".ref", "%s is already on the board" % h["ref"], "give another designator or omit it (the next free H<n> is used)")
        if ps is not None and not inside_board(ps, h["samples"]):
            P.add("E_BOARD_GEOMETRY", f, "the hole at (%s, %s) is not wholly inside the board outline" % (fmt_num(h["x"]), fmt_num(h["y"])),
                  "move it inside the outline (away from the edge and cutouts)")
        hit = pad_hits(board, h["x"], h["y"], h["pad"] / 2)
        if hit:
            P.add("E_BOARD_OVERLAP", f, "the hole at (%s, %s) lands on pads of %s" % (fmt_num(h["x"]), fmt_num(h["y"]), ", ".join(sorted(set(hit)))),
                  "move the hole clear of existing pads")
    if P:
        return None
    warnings = []
    if ps is None:
        warnings.append("the board has no closed outline: hole positions were not checked against it")
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
            pad.SetLayerSet(pcbnew.PAD.UnplatedHoleMask())
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
        created.append({"kind": "mounting-hole", "uuid": uuid_of(fp), "name": ref})
    return refill_filled(board, {"created": created, "removed": [], "warnings": warnings})


def verify_holes(board, spec, res, V):
    fps = {uuid_of(fp): fp for fp in board.GetFootprints()}
    for c, h in zip(res["created"], spec["holes"]):
        fp = fps.get(c["uuid"])
        if fp is None:
            V.append("hole %s missing after the write" % c["name"])
            continue
        pads = list(fp.Pads())
        if len(pads) != 1:
            V.append("hole %s has %d pads" % (c["name"], len(pads)))
            continue
        p = pads[0]
        want = pcbnew.PAD_ATTRIB_PTH if h["type"] == "pth" else pcbnew.PAD_ATTRIB_NPTH
        if p.GetAttribute() != want or abs(mm(p.GetDrillSizeX()) - h["drill"]) > 1e-4:
            V.append("hole %s reads back as attribute %d drill %.4f mm" % (c["name"], p.GetAttribute(), mm(p.GetDrillSizeX())))
        if (h.get("net") or "") != p.GetNetname():
            V.append("hole %s is on net %r, expected %r" % (c["name"], p.GetNetname(), h.get("net") or ""))
        if not fp.IsLocked():
            V.append("hole %s is not locked" % c["name"])


# ── keepout and zone ────────────────────────────────────────────────────────

RULES = {"tracks": "SetDoNotAllowTracks", "vias": "SetDoNotAllowVias", "pads": "SetDoNotAllowPads",
         "pours": "SetDoNotAllowZoneFills", "footprints": "SetDoNotAllowFootprints"}
GETTERS = {"tracks": "GetDoNotAllowTracks", "vias": "GetDoNotAllowVias", "pads": "GetDoNotAllowPads",
           "pours": "GetDoNotAllowZoneFills", "footprints": "GetDoNotAllowFootprints"}


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
    if spec.get("name"):
        z.SetZoneName(spec["name"])
    board.Add(z)
    return refill_filled(board, {"created": [{"kind": "rule-area", "uuid": uuid_of(z), "name": spec.get("name", "")}], "removed": [],
                                 "summary": {"layers": [board.GetLayerName(l) for l in ids], "forbid": spec["forbid"]}})


def verify_keepout(board, spec, res, V):
    z = next((z for z in board.Zones() if uuid_of(z) == res["created"][0]["uuid"]), None)
    if z is None or not z.GetIsRuleArea():
        V.append("rule area missing after the write")
        return
    for r, g in GETTERS.items():
        if getattr(z, g)() != (r in spec["forbid"]):
            V.append("rule area: %s %s" % (r, "allowed" if getattr(z, g)() is False else "forbidden"))
    if sorted(board.GetLayerName(l) for l in z.GetLayerSet().CuStack()) != sorted(res["summary"]["layers"]):
        V.append("rule area layers differ after the write")


CONNECTIONS = {"thermal": "ZONE_CONNECTION_THERMAL", "solid": "ZONE_CONNECTION_FULL", "none": "ZONE_CONNECTION_NONE",
               "tht-thermal": "ZONE_CONNECTION_THT_THERMAL"}


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
            ch = ps.Outline(largest(ps))
            pts = [(mm(ch.CPoint(i).x), mm(ch.CPoint(i).y)) for i in range(ch.PointCount())]
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
    filled = 0
    if spec["fill"]:
        zones = board.Zones()
        pcbnew.ZONE_FILLER(board).Fill(zones)
        filled = len(zones)
    warnings = []
    if spec["fill"] and filled > 1:
        warnings.append("all %d zones of the board were refilled (KiCad fills by priority across zones)" % filled)
    return {"created": [{"kind": "zone", "uuid": uuid_of(z), "name": spec.get("name", "")}], "removed": [], "warnings": warnings,
            "summary": {"net": spec["net"], "layers": [board.GetLayerName(l) for l in ids], "zonesFilled": filled}}


def verify_zone(board, spec, res, V):
    z = next((z for z in board.Zones() if uuid_of(z) == res["created"][0]["uuid"]), None)
    if z is None or z.GetIsRuleArea():
        V.append("zone missing after the write")
        return
    if z.GetNetname() != spec["net"]:
        V.append("zone on net %r, expected %r" % (z.GetNetname(), spec["net"]))
    if z.GetAssignedPriority() != int(spec["priority"]):
        V.append("zone priority %d, expected %d" % (z.GetAssignedPriority(), spec["priority"]))
    clr = z.GetLocalClearance()  # std::optional<int> in 10.0: an int or None
    if clr is None or abs(mm(int(clr)) - spec["clearance"]) > 1e-4:
        V.append("zone clearance %r, expected %.4f mm" % (clr, spec["clearance"]))
    if spec["fill"]:
        area = 0.0
        for l in z.GetLayerSet().CuStack():
            if z.HasFilledPolysForLayer(l):
                area += abs(z.GetFilledPolysList(l).Area())
        res["summary"]["filledAreaMm2"] = round(area / 1e12, 3)
        if area <= 0:
            res.setdefault("warnings", []).append("the zone filled to nothing: check its net, clearance and that it overlaps the board")


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


def verify_text(board, spec, res, V):
    t = next((d for d in board.GetDrawings() if uuid_of(d) == res["created"][0]["uuid"]), None)
    if t is None or t.GetClass() != "PCB_TEXT":
        V.append("text missing after the write")
        return
    if t.GetText() != spec["text"]:
        V.append("text reads back as %r" % t.GetText())


# ── main ────────────────────────────────────────────────────────────────────

OPS = {"outline": op_outline, "holes": op_holes, "keepout": op_keepout, "zone": op_zone, "text": op_text}


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
        if op == "stackup":
            res = op_stackup(board, spec, P, out)
        else:
            res = OPS[op](board, spec, P)
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
    elif op == "outline":
        verify_outline(back, res, V)
    elif op == "stackup":
        verify_stackup(back, spec, out, res, V)
    elif op == "holes":
        verify_holes(back, spec, res, V)
    elif op == "keepout":
        verify_keepout(back, spec, res, V)
    elif op == "zone":
        verify_zone(back, spec, res, V)
    elif op == "text":
        verify_text(back, spec, res, V)
    uuids = {c["uuid"] for c in res["created"]}
    if op in ("outline",):
        have = {uuid_of(d) for d in back.GetDrawings()}
        missing = uuids - have
        if missing:
            V.append("%d created drawing(s) missing after the write" % len(missing))
    result = {"ok": not V, "op": op, "created": res["created"], "removed": res["removed"], "warnings": res.get("warnings", []),
              "summary": res.get("summary", {}), "verify": {"status": "fail" if V else "pass", "detail": V},
              "kicadVersion": pcbnew.Version()}
    if V:
        result["problems"] = [{"code": "E_BOARD_VERIFY", "field": "", "detail": d,
                               "fix": "report this: the board did not read back as written (nothing was committed)"} for d in V]
    return result


def main(argv):
    usage = "usage: board.py new|outline|stackup|holes|keepout|zone|text IN.kicad_pcb|- SPEC.json OUT.kicad_pcb"
    if len(argv) != 5 or argv[1] not in ("new", "stackup") + tuple(OPS):
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
