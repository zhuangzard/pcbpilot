#!/usr/bin/env python3
"""Measure each part's Designator box at every rotation on a scratch page.

EasyEDA Pro V4 re-lays a part's Designator when the part is rotated; it does
not turn rigidly with the body (live 2026-09-24: a resistor at 180 keeps its
0-degree offset, a capacitor at 90/270 moves its label to the right). The
zone solver therefore needs measured boxes per pose, supplied as
measurement.textBboxesByRotation (relative to the anchor x/y).

Only typed pcbpilot commands are used: sch place (rotation 0, like Apply),
sch modify --rotation (absolute, like Apply's orient step), and
sch designator-geometry. The scratch page must be empty and dedicated; delete
it afterwards with sch page-delete. Output JSON: {componentId: {"0": [box],
"90": [box], ...}} with boxes relative to the anchor.

  measure-designator-rotations.py --parts parts.json --project <uuid> \
      --page <scratch-page-uuid> --out rotations.json

parts.json rows: {id, libraryUuid, uuid|deviceUuid, designator, measureAt:[x,y]}
"""
import argparse
import json
import subprocess
import sys
import time


def cli(*args, soft=False):
    out = subprocess.run(["pcbpilot", *args], capture_output=True, text=True)
    try:
        doc = json.loads(out.stdout)
    except json.JSONDecodeError:
        doc = {"ok": False, "raw": out.stdout[:300]}
    if doc.get("ok") is False or out.returncode != 0:
        if soft:
            return None
        sys.exit(f"pcbpilot {' '.join(args[:2])} failed: {json.dumps(doc)[:600]} {out.stderr[:300]}")
    return doc


def page_parts(project, page):
    listed = cli("sch", "list", "--project", project, "--page", page, "--stay")
    return [c for c in listed["result"]["components"] if c.get("componentType") != "sheet"]


def designators(project, page):
    doc = cli("sch", "designator-geometry", "--project", project, "--doc", page)
    return {d["parentId"]: d for d in doc["designators"]}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--parts", required=True)
    ap.add_argument("--project", required=True)
    ap.add_argument("--page", required=True, help="dedicated empty scratch schematic page")
    ap.add_argument("--rotations", default="90,180,270")
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    parts = json.load(open(a.parts))
    base = ["--project", a.project, "--doc", a.page]
    # A resumed run may reuse parts this script already placed at rotation 0
    # (matched by designator AND anchor); anything else on the page refuses.
    existing = {(c.get("designator"), c.get("x"), c.get("y")): c for c in page_parts(a.project, a.page)}
    wanted = {(p["designator"], p["measureAt"][0], p["measureAt"][1]) for p in parts}
    if any(k not in wanted or (c.get("rotation") or 0) != 0 for k, c in existing.items()):
        sys.exit("scratch page holds foreign or rotated parts; refusing to measure on a design page")
    placed = {}
    for p in parts:
        x, y = p["measureAt"]
        key = (p["designator"], x, y)
        if key not in existing:
            doc = cli("sch", "place", *base, "--lib", p["libraryUuid"], "--uuid", p.get("deviceUuid") or p["uuid"],
                      "--x", str(x), "--y", str(y), "--designator", p["designator"], soft=True)
            if doc is None:
                # V4 Web sometimes acks late or drops a place. Read back before
                # any retry so a late landing is never duplicated.
                time.sleep(5)
                existing = {(c.get("designator"), c.get("x"), c.get("y")): c for c in page_parts(a.project, a.page)}
                if key not in existing:
                    cli("sch", "place", *base, "--lib", p["libraryUuid"], "--uuid", p.get("deviceUuid") or p["uuid"],
                        "--x", str(x), "--y", str(y), "--designator", p["designator"])
            existing = {(c.get("designator"), c.get("x"), c.get("y")): c for c in page_parts(a.project, a.page)}
        placed[p["id"]] = (existing[key]["primitiveId"], x, y)
    out = {pid: {} for pid in placed}

    def record(angle):
        boxes = designators(a.project, a.page)
        for pid, (prim, x, y) in placed.items():
            d = boxes.get(prim)
            if d is None:
                sys.exit(f"{pid}: no measured Designator at rotation {angle}")
            b = d["bbox"]
            out[pid][str(angle)] = [{"minX": b["minX"] - x, "minY": b["minY"] - y, "maxX": b["maxX"] - x, "maxY": b["maxY"] - y}]

    record(0)
    for angle in [int(r) for r in a.rotations.split(",") if r]:
        for pid, (prim, x, y) in placed.items():
            cli("sch", "modify", *base, "--id", prim, "--rotation", str(angle))
        after = cli("sch", "list", "--project", a.project, "--page", a.page, "--stay")
        anchors = {c["primitiveId"]: (c.get("x"), c.get("y")) for c in after["result"]["components"]}
        for pid, (prim, x, y) in placed.items():
            if anchors.get(prim) != (x, y):
                sys.exit(f"{pid}: anchor moved on rotation {angle}: {anchors.get(prim)} != {(x, y)}")
        record(angle)
    json.dump(out, open(a.out, "w"), indent=2)
    print(f"measured {len(out)} parts at rotations 0,{a.rotations} -> {a.out}")


if __name__ == "__main__":
    main()
