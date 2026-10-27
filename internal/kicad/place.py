# place.py — apply footprint poses to a KiCad board (pcbpilot kicad place).
#
#   python3 place.py place BOARD.kicad_pcb POSES.json OUT.kicad_pcb
#
# POSES.json (mil, y down, in KiCad's own frame; rotationDeg = footprint
# orientation, CCW as drawn; side top | bottom):
#
#   {"pcbpilotPoses": 2, "poses": [
#       {"ref": "U1", "xMil": 1000, "yMil": 2000, "rotationDeg": 90, "side": "top"},
#       {"uuid": "<footprint uuid>", "xMil": ..., ...}]}
#
# Each record names its footprint by exactly one typed selector: "ref"
# matches designators only, "uuid" footprint uuids only (designators are
# free text: "uuid:..." is a designator like any other). The older
# {"U1": {pose}, ...} form is still read, its keys as designators only.
# A side change flips the footprint first (KiCad mirrors it left-right about
# its anchor; the orientation is then set to the one asked for). Placement
# invalidates the copper: tracks, arcs and vias are removed and copper zones
# unfilled.
#
# All or nothing: a malformed record, a selector that names no footprint or
# several, or two records for one footprint reject the whole run and OUT is
# not written. Prints one JSON line after RESULT: the summary, or {"ok":
# false, "error", "invalid": [...], "missing": [selector], "ambiguous":
# [{selector + "candidates": [uuid]}], "posedTwice": [{"uuid", "selectors"}]}
# with exit status 1.
import json
import math
import sys

import pcbnew

RESULT = "PCBPILOT_RESULT "


def flip(fp):
    c = fp.GetPosition()
    try:
        fp.Flip(c, pcbnew.FLIP_DIRECTION_LEFT_RIGHT)
    except (AttributeError, TypeError):  # KiCad < 9: bool aFlipLeftRight
        fp.Flip(c, True)


def result(obj, code=0):
    # One marked line: SWIG prints leak notices on stdout too.
    sys.stdout.write("\n" + RESULT + json.dumps(obj) + "\n")
    sys.stdout.flush()
    sys.exit(code)


def is_num(v):
    return isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v)


def records(doc):
    """([(selector, pose)], problems): selector = {"ref": r} or {"uuid": u}."""
    # The envelope carries a version number; a legacy map may well have a
    # footprint named "pcbpilotPoses" (its value is then a pose object).
    if isinstance(doc, dict) and is_num(doc.get("pcbpilotPoses")):
        if doc.get("pcbpilotPoses") != 2 or not isinstance(doc.get("poses"), list) or set(doc) - {"pcbpilotPoses", "poses"}:
            return [], ['poses file: want {"pcbpilotPoses": 2, "poses": [records]}']
        out, bad = [], []
        for i, r in enumerate(doc["poses"]):
            if not isinstance(r, dict):
                bad.append("poses[%d]: not an object" % i)
                continue
            given = [k for k in ("ref", "uuid") if k in r]
            if len(given) != 1 or not isinstance(r[given[0]], str) or not r[given[0]]:
                bad.append("poses[%d]: give exactly one non-empty string ref or uuid" % i)
                continue
            out.append(({given[0]: r[given[0]]}, r))
        return out, bad
    if not isinstance(doc, dict):
        return [], ["poses file: not an object"]
    return [({"ref": k}, v) for k, v in doc.items()], []  # legacy: keys are designators


def label(sel):
    return ("ref %s" if "ref" in sel else "uuid %s") % next(iter(sel.values()))


def check_pose(sel, p):
    if not isinstance(p, dict):
        return ["%s: pose is not an object" % label(sel)]
    bad = ["%s: %s %r is not a number" % (label(sel), k, p.get(k)) for k in ("xMil", "yMil", "rotationDeg") if not is_num(p.get(k))]
    if p.get("side", "top") not in ("top", "bottom"):
        bad.append("%s: side %r is not top or bottom" % (label(sel), p.get("side")))
    return bad


def resolve(fps, recs):
    """([(footprint, pose)], problems dict) for every record."""
    by = {"ref": {}, "uuid": {}}
    for fp in fps:
        by["ref"].setdefault(fp.GetReference(), []).append(fp)
        by["uuid"].setdefault(fp.m_Uuid.AsString(), []).append(fp)
    hits, prob = [], {"invalid": [], "missing": [], "ambiguous": [], "posedTwice": []}
    for sel, p in recs:
        prob["invalid"] += check_pose(sel, p)
        (kind, want), = sel.items()
        cand = by[kind].get(want, [])
        if not cand:
            prob["missing"].append(sel)
        elif len(cand) > 1:
            prob["ambiguous"].append(dict(sel, candidates=[fp.m_Uuid.AsString() for fp in cand]))
        else:
            hits.append((cand[0], p, sel))
    # Two records for one footprint (its designator and its uuid): which
    # pose? (The wrappers come from one footprint list: identity is the
    # footprint; converted boards can repeat a uuid across footprints.)
    owner = {}
    for fp, _, sel in hits:
        owner.setdefault(id(fp), (fp, []))[1].append(sel)
    for fp, sels in owner.values():
        if len(sels) > 1:
            prob["posedTwice"].append({"uuid": fp.m_Uuid.AsString(), "selectors": sels})
    return hits, prob


def place(src, poses_path, out):
    board = pcbnew.LoadBoard(src)
    with open(poses_path) as f:
        doc = json.load(f)
    recs, invalid = records(doc)
    hits, prob = resolve(list(board.GetFootprints()), recs)
    prob["invalid"] = invalid + prob["invalid"]
    if any(prob.values()):
        why = list(prob["invalid"])
        if prob["missing"]:
            why.append("no footprint for %s" % ", ".join(label(m) for m in prob["missing"]))
        for a in prob["ambiguous"]:
            why.append("%s is on %d footprints (uuids %s) — pose one by its uuid" % (label(a), len(a["candidates"]), ", ".join(a["candidates"])))
        for d in prob["posedTwice"]:
            why.append("%s address the same footprint (uuid %s) — pose it once" % (" and ".join(label(x) for x in d["selectors"]), d["uuid"]))
        result(dict(prob, ok=False, error="place rejected, board not written: " + "; ".join(why)), 1)
    moved, flipped, locked = 0, 0, []
    for fp, p, _ in hits:
        if fp.IsLocked():
            locked.append(fp.GetReference())
            continue
        want_bottom = p.get("side", "top") == "bottom"
        if want_bottom != fp.IsFlipped():
            flip(fp)
            flipped += 1
        fp.SetOrientationDegrees(float(p["rotationDeg"]))
        fp.SetPosition(pcbnew.VECTOR2I(pcbnew.FromMils(p["xMil"]), pcbnew.FromMils(p["yMil"])))
        moved += 1
    tracks = list(board.GetTracks())
    for t in tracks:
        board.Remove(t)
    for z in board.Zones():
        if not z.GetIsRuleArea():
            z.UnFill()
    board.Save(out)
    result({"ok": True, "moved": moved, "flipped": flipped, "removedTracks": len(tracks), "missing": [], "locked": sorted(locked)})


def main():
    if len(sys.argv) != 5 or sys.argv[1] != "place":
        sys.exit("usage: place.py place BOARD.kicad_pcb POSES.json OUT.kicad_pcb")
    place(*sys.argv[2:])


if __name__ == "__main__":
    main()
