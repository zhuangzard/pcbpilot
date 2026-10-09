# place.py — apply footprint poses to a KiCad board (pcbpilot kicad place).
#
#   python3 place.py place BOARD.kicad_pcb POSES.json OUT.kicad_pcb
#
# POSES.json: {"U1": {"xMil": 1000, "yMil": 2000, "rotationDeg": 90, "side": "top"}}
# in KiCad's own frame (mil, y down; rotationDeg = footprint orientation,
# CCW as drawn). A side change flips the footprint first (KiCad mirrors it
# left-right about its anchor; the orientation is then set to the one asked
# for). Placement invalidates the copper: tracks, arcs and vias are removed
# and copper zones unfilled. Prints a JSON summary on stdout after RESULT.
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


def place(src, poses_path, out):
    board = pcbnew.LoadBoard(src)
    with open(poses_path) as f:
        poses = json.load(f)
    seen, moved, flipped = set(), 0, 0
    for fp in board.GetFootprints():
        ref = fp.GetReference()
        p = poses.get(ref)
        if p is None:
            continue
        seen.add(ref)
        if fp.IsLocked():
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
    missing = sorted(set(poses) - seen)
    locked = sorted(r for r in seen if board.FindFootprintByReference(r).IsLocked())
    # One marked line: SWIG prints leak notices on stdout too.
    sys.stdout.write("\n" + RESULT + json.dumps({"moved": moved, "flipped": flipped, "removedTracks": len(tracks),
                               "missing": missing, "locked": locked}) + "\n")
    sys.stdout.flush()


def main():
    if len(sys.argv) != 5 or sys.argv[1] != "place":
        sys.exit("usage: place.py place BOARD.kicad_pcb POSES.json OUT.kicad_pcb")
    place(*sys.argv[2:])


if __name__ == "__main__":
    main()
