# snapshot_min.py — MINIMAL KiCad board snapshot for `pcbpilot kicad place`.
# Replaced by kicad/core's bridge.py `snapshot` at merge; keep it small.
#
#   python3 snapshot_min.py BOARD.kicad_pcb  > snapshot.json
#
# Emits the pcb dump subset pcbauto.FromSnapshot reads, in mil and y-up
# (y = -KiCad y; KiCad's CCW orientation stays CCW), plus two optional
# component extras the placer uses when present: "footprint" (lib:name) and
# "courtyard" (front/back courtyard bbox, the placer's body).
import json
import sys

import pcbnew

mil = pcbnew.ToMils


def box(b):
    """KiCad BOX2I (y-down nm) -> {minX..maxY} in y-up mil."""
    return {"minX": mil(b.GetLeft()), "minY": -mil(b.GetBottom()),
            "maxX": mil(b.GetRight()), "maxY": -mil(b.GetTop())}


def pad_shape(p):
    s = p.GetShape(pcbnew.F_Cu) if hasattr(pcbnew, "PADSTACK") else p.GetShape()
    if s == pcbnew.PAD_SHAPE_CIRCLE:
        return "ELLIPSE"
    if s == pcbnew.PAD_SHAPE_OVAL:
        return "OVAL"
    return "RECT"


def comp(fp, holes):
    flipped = fp.IsFlipped()
    pads = []
    for p in fp.Pads():
        pos, bb = p.GetPosition(), p.GetBoundingBox()
        if p.GetAttribute() == pcbnew.PAD_ATTRIB_NPTH:
            # A hole, not copper (mounting holes, locating pegs).
            sz = p.GetDrillSize()
            holes.append({"owner": fp.GetReference(), "sourceId": "pad" + p.GetNumber(), "shape": "circle",
                          "x": mil(pos.x), "y": -mil(pos.y), "dia": mil(min(sz.x, sz.y))})
            continue
        if p.GetAttribute() in (pcbnew.PAD_ATTRIB_PTH,):
            layer = 12
        else:
            layer = 2 if p.IsOnLayer(pcbnew.B_Cu) and not p.IsOnLayer(pcbnew.F_Cu) else 1
        sz = p.GetSize()
        pads.append({"padNumber": p.GetNumber(), "net": p.GetNetname(), "layer": layer,
                     "x": mil(pos.x), "y": -mil(pos.y),
                     "width": mil(bb.GetWidth()), "height": mil(bb.GetHeight()),
                     "rotation": p.GetOrientationDegrees() % 360,
                     "shape": [pad_shape(p), mil(sz.x), mil(sz.y)]})
    pos = fp.GetPosition()
    c = {"primitiveId": fp.m_Uuid.AsString(), "designator": fp.GetReference(),
         "device": fp.GetValue(), "footprint": fp.GetFPIDAsString(),
         "layer": 2 if flipped else 1, "x": mil(pos.x), "y": -mil(pos.y),
         "rotation": fp.GetOrientationDegrees() % 360, "locked": fp.IsLocked(),
         "bbox": box(fp.GetBoundingBox(False)), "pads": pads}
    fp.BuildCourtyardCaches()
    cy = fp.GetCourtyard(pcbnew.B_CrtYd if flipped else pcbnew.F_CrtYd)
    if cy.OutlineCount() > 0:
        c["courtyard"] = box(cy.BBox())
    return c


def silk(fp):
    r = fp.Reference()
    layer = {pcbnew.F_SilkS: 3, pcbnew.B_SilkS: 4}.get(r.GetLayer(), 0)
    if not layer:
        return None
    return {"Kind": "attribute", "Key": "Designator", "Text": r.GetShownText(False), "Layer": layer,
            "Mirror": r.IsMirrored(), "Rotation": r.GetDrawRotation().AsDegrees() % 360,
            "FontSize": mil(r.GetTextHeight()), "LineWidth": mil(r.GetTextThickness()),
            "CompID": fp.m_Uuid.AsString(), "CompLayer": 2 if fp.IsFlipped() else 1,
            "X": mil(r.GetPosition().x), "Y": -mil(r.GetPosition().y),
            "BBox": {k[0].upper() + k[1:]: v for k, v in box(r.GetBoundingBox()).items()},
            "Hidden": not r.IsVisible()}


def outline(board, fills):
    ps = pcbnew.SHAPE_POLY_SET()
    board.GetBoardPolygonOutlines(ps, True)
    if ps.OutlineCount() == 0:
        return None
    o = ps.Outline(0)
    pts = [[mil(o.CPoint(i).x), -mil(o.CPoint(i).y)] for i in range(o.PointCount())]
    for h in range(ps.HoleCount(0)):  # inner cutouts -> MULTI fills (board holes)
        bb = ps.Hole(0, h).BBox()
        fills.append({"primitiveId": "cutout%d" % h, "layer": 12, "bbox": box(bb)})
    return {"bbox": box(ps.BBox()), "points": pts, "source": "polygon"}


def rule_areas(board):
    out = []
    for z in board.Zones():
        if not z.GetIsRuleArea():
            continue
        rt = []
        if z.GetDoNotAllowFootprints():
            rt.append(2)
        if z.GetDoNotAllowTracks() or z.GetDoNotAllowVias():
            rt.append(5)
        if not rt:
            continue
        o = z.Outline().Outline(0)
        src = []
        for i in range(o.PointCount()):
            if i:
                src.append("L")
            src += [mil(o.CPoint(i).x), -mil(o.CPoint(i).y)]
        layer = 0
        if z.GetLayerSet().CuStack().size() == 1:
            layer = {pcbnew.F_Cu: 1, pcbnew.B_Cu: 2}.get(z.GetLayerSet().CuStack()[0], 0)
        out.append({"primitiveId": z.m_Uuid.AsString(), "name": z.GetZoneName(), "ruleType": rt,
                    "layer": layer, "source": src, "bbox": box(z.GetBoundingBox())})
    return out


def rules(board):
    ds = board.GetDesignSettings()
    r = {"copperToEdgeMil": mil(ds.m_CopperEdgeClearance), "source": "kicad"}
    try:
        nc = ds.m_NetSettings.GetDefaultNetclass()
        r.update(clearanceMil=mil(nc.GetClearance()), trackWidthMil=mil(nc.GetTrackWidth()),
                 viaDrillMil=mil(nc.GetViaDrill()), viaDiameterMil=mil(nc.GetViaDiameter()))
    except Exception:  # older API: board minimums
        r.update(clearanceMil=mil(ds.m_MinClearance), trackWidthMil=mil(ds.m_TrackMinWidth))
    r["trackWidthMinMil"] = mil(ds.m_TrackMinWidth)
    try:
        r["holeToHoleMil"] = mil(ds.m_HoleToHoleMin)
    except Exception:
        pass
    return r


def main():
    board = pcbnew.LoadBoard(sys.argv[1])
    holes, fills, comps, texts = [], [], [], []
    for fp in board.GetFootprints():
        if not fp.GetReference():
            continue
        comps.append(comp(fp, holes))
        t = silk(fp)
        if t:
            texts.append(t)
    snap = {"components": comps, "outline": outline(board, fills), "silk": texts,
            "copperLayers": board.GetCopperLayerCount(), "rules": rules(board),
            "copper": {"regions": rule_areas(board), "fills": fills},
            "footprintHoles": holes, "source": "kicad-snapshot-min"}
    json.dump(snap, sys.stdout)


if __name__ == "__main__":
    main()
