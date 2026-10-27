# place.py — apply footprint poses to a KiCad board (pcbpilot kicad place).
#
#   python3 place.py place BOARD.kicad_pcb POSES.json OUT.kicad_pcb
#
# POSES.json: {"U1": {"xMil": 1000, "yMil": 2000, "rotationDeg": 90, "side": "top"}}
# in KiCad's own frame (mil, y down; rotationDeg = footprint orientation,
# CCW as drawn). A key is a designator, or "uuid:<footprint uuid>" for a
# footprint whose designator is shared (several "REF**" logos, two "H1").
# A side change flips the footprint first (KiCad mirrors it left-right about
# its anchor; the orientation is then set to the one asked for). Placement
# invalidates the copper: tracks, arcs and vias are removed and copper zones
# unfilled.
#
# All or nothing: a key that names no footprint, or a designator on more
# than one footprint, rejects the whole run and OUT is not written. Prints
# one JSON line after RESULT: the summary, or {"ok": false, "error",
# "missing", "ambiguous": {designator: [uuid, ...]}} with exit status 1.
import json
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


def resolve(fps, poses):
    """[(footprint, key)] for every pose key, or (None, missing, ambiguous)."""
    by_ref, by_uuid = {}, {}
    for fp in fps:
        by_ref.setdefault(fp.GetReference(), []).append(fp)
        by_uuid.setdefault(fp.m_Uuid.AsString(), []).append(fp)
    hits, missing, ambiguous = [], [], {}
    for key in poses:
        if key.startswith("uuid:"):
            cand = by_uuid.get(key[5:], [])
        else:
            cand = by_ref.get(key, [])
        if not cand:
            missing.append(key)
        elif len(cand) > 1:
            ambiguous[key] = [fp.m_Uuid.AsString() for fp in cand]
        else:
            hits.append((cand[0], key))
    # Two keys for one footprint (its designator and its uuid): which pose?
    # (The wrappers come from one footprint list: identity is the footprint;
    # converted boards can repeat a uuid across different footprints.)
    owner = {}
    for fp, key in hits:
        owner.setdefault(id(fp), (fp, []))[1].append(key)
    for fp, keys in owner.values():
        if len(keys) > 1:
            ambiguous["+".join(sorted(keys))] = [fp.m_Uuid.AsString()]
    if missing or ambiguous:
        return None, sorted(missing), ambiguous
    return hits, [], {}


def place(src, poses_path, out):
    board = pcbnew.LoadBoard(src)
    with open(poses_path) as f:
        poses = json.load(f)
    hits, missing, ambiguous = resolve(list(board.GetFootprints()), poses)
    if hits is None:
        why = []
        if missing:
            why.append("no footprint for %s" % ", ".join(missing))
        for k in sorted(ambiguous):
            if len(ambiguous[k]) == 1:
                why.append("%s address the same footprint (uuid:%s) — pose it once" % (k.replace("+", " and "), ambiguous[k][0]))
                continue
            why.append("%s is on %d footprints (uuid:%s) — address one as uuid:<uuid>" % (k, len(ambiguous[k]), ", uuid:".join(ambiguous[k])))
        result({"ok": False, "error": "place rejected, board not written: " + "; ".join(why),
                "missing": missing, "ambiguous": ambiguous}, 1)
    moved, flipped, locked = 0, 0, []
    for fp, key in hits:
        if fp.IsLocked():
            locked.append(key)
            continue
        p = poses[key]
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
