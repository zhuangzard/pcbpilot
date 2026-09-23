#!/usr/bin/env python3
"""Reconcile schematic pin->net against PCB pad->net, pad by pad.

Inputs:
  --sch  output of `pcbpilot sch connectivity` (connectivity IR, optionally
         wrapped in {"result": ...}). Repeat --sch once per page when a project
         holds several schematics: read only the pages whose
         parentSchematicUuid is the one bound to this PCB (`pcbpilot board list`,
         `pcbpilot sch pages`); `--all-pages` mixes every schematic.
  --pcb  output of `pcbpilot pcb dump --out board.json`

Reports parts missing on the PCB, extra PCB parts, pins without a pad, pads
without a pin, and net mismatches. A schematic no-connect matches an empty
PCB net. Nets that differ only in letter case (EasyEDA may upper-case net
names on the PCB side) are listed as warnings, not errors.
Exit 0 = identical, 2 = only case warnings (report them, not "passed"),
1 = real differences.

  pad-net-diff.py --sch p1.json [--sch p2.json ...] --pcb board.json [--json] [--ignore-refs H1,H2]
"""
import argparse
import json
import sys

NC = "<NC>"


def unwrap(doc):
    if isinstance(doc, dict) and "components" not in doc:
        for key in ("result", "connectivity", "document"):
            if isinstance(doc.get(key), dict):
                return unwrap(doc[key])
    return doc


def schematic_map(doc):
    doc = unwrap(doc)
    net_name = {n["id"]: (n.get("name") or n["id"]) for n in doc.get("nets", [])}
    comp_ref = {c["id"]: c.get("ref", "") for c in doc.get("components", [])}
    pins = {}
    for c in doc.get("components", []):
        ref = c.get("ref", "")
        for p in c.get("pins", []):
            key = (ref, str(p.get("number", "")))
            pins[key] = NC if p.get("noConnected") else None
    for conn in doc.get("connections", []):
        ref = comp_ref.get(conn.get("componentId"), "")
        key = (ref, str(conn.get("pinNumber", "")))
        pins[key] = net_name.get(conn.get("netId"), conn.get("netId"))
    return pins


def pcb_map(board):
    pads = {}
    for c in board.get("components", []):
        ref = c.get("designator", "")
        for p in c.get("pads") or []:
            key = (ref, str(p.get("padNumber", "")))
            pads[key] = p.get("net") or ""
    return pads


def diff(sch, pcb, ignore=()):
    sch_refs = {r for r, _ in sch if r not in ignore}
    pcb_refs = {r for r, _ in pcb if r not in ignore}
    out = {
        "missingOnPcb": sorted(sch_refs - pcb_refs),
        "extraOnPcb": sorted(pcb_refs - sch_refs),
        "pinWithoutPad": [],
        "padWithoutPin": [],
        "netMismatch": [],
        "netCaseOnly": [],
        "unconnectedPins": [],
    }
    for (ref, pin), net in sorted(sch.items()):
        if ref in ignore or ref not in pcb_refs:
            continue
        if (ref, pin) not in pcb:
            out["pinWithoutPad"].append(f"{ref}.{pin}")
            continue
        pad_net = pcb[(ref, pin)]
        if net is None:
            out["unconnectedPins"].append(f"{ref}.{pin}")
            want = ""
        else:
            want = "" if net == NC else net
        if pad_net != want and pad_net.lower() == want.lower():
            out["netCaseOnly"].append({"pad": f"{ref}.{pin}", "schematic": want, "pcb": pad_net})
        elif pad_net != want:
            out["netMismatch"].append({"pad": f"{ref}.{pin}", "schematic": net or "", "pcb": pad_net})
    for (ref, pin) in sorted(pcb):
        if ref in ignore or ref not in sch_refs:
            continue
        if (ref, pin) not in sch:
            out["padWithoutPin"].append(f"{ref}.{pin}")
    out["ok"] = not any(out[k] for k in ("missingOnPcb", "extraOnPcb", "pinWithoutPad", "padWithoutPin", "netMismatch"))
    out["counts"] = {k: len(v) for k, v in out.items() if isinstance(v, list)}
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--sch", required=True, action="append", help="connectivity JSON; repeat per page")
    ap.add_argument("--pcb", required=True)
    ap.add_argument("--ignore-refs", default="", help="comma list of designators to skip (mounting holes, logos)")
    ap.add_argument("--json", action="store_true")
    a = ap.parse_args()
    sch = {}
    for path in a.sch:
        with open(path, encoding="utf-8") as f:
            for key, net in schematic_map(json.load(f)).items():
                # A pin seen with a net on one page wins over "unconnected".
                if sch.get(key) is None:
                    sch[key] = net
    with open(a.pcb, encoding="utf-8") as f:
        pcb = pcb_map(json.load(f))
    ignore = {r.strip() for r in a.ignore_refs.split(",") if r.strip()}
    rep = diff(sch, pcb, ignore)
    if a.json:
        json.dump(rep, sys.stdout, ensure_ascii=False, indent=2)
        print()
    else:
        status = "MISMATCH" if not rep["ok"] else ("WARN" if rep["netCaseOnly"] else "OK")
        print(status, rep["counts"])
        for k in ("missingOnPcb", "extraOnPcb", "pinWithoutPad", "padWithoutPin"):
            if rep[k]:
                print(f"  {k}: {', '.join(rep[k][:40])}")
        for m in rep["netMismatch"][:60]:
            print(f"  net {m['pad']}: schematic={m['schematic'] or '(none)'} pcb={m['pcb'] or '(none)'}")
        for m in rep["netCaseOnly"][:60]:
            print(f"  WARN case-only {m['pad']}: schematic={m['schematic']} pcb={m['pcb']}")
        if rep["unconnectedPins"]:
            print(f"  note: {len(rep['unconnectedPins'])} schematic pins have neither a net nor an explicit NC")
    if not rep["ok"]:
        return 1
    return 2 if rep["netCaseOnly"] else 0


if __name__ == "__main__":
    sys.exit(main())
