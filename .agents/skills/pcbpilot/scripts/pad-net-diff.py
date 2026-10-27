#!/usr/bin/env python3
"""Reconcile schematic pin->net against PCB pad->net, pad by pad.

Inputs:
  --sch  output of `pcbpilot sch connectivity` (connectivity IR, optionally
         wrapped in {"result": ...}). Repeat --sch once per page when a project
         holds several schematics: read only the pages whose
         parentSchematicUuid is the one bound to this PCB (`pcbpilot board list`,
         `pcbpilot sch pages`); `--all-pages` mixes every schematic.
  --pcb  output of `pcbpilot pcb dump --out board.json`

  --pcb  may also be `pcbpilot kicad snapshot board.kicad_pcb` (KiCad); --sch
         then `pcbpilot kicad netlist`.

Nets are compared by identity (pkg/netname, the rule every pcbpilot matcher
uses): a schematic net's "fullName" (KiCad's full hierarchical name, escaped
as on the board: "/pic_sockets/VCC_PIC", "/VPP{slash}MCLR") when present,
else its "name"; a name without fullName (older documents) that is not a
board net resolves to the one board net whose display name it is (the sheet
path dropped, "{slash}" read as '/'). Never cut at '/' inside a label. A KiCad
"unconnected-(…)" pad net is no net.

Reports parts missing on the PCB, extra PCB parts, pins without a pad, pads
without a pin, and net mismatches. A schematic no-connect matches an empty
PCB net. Nets that differ only in letter case (EasyEDA may upper-case net
names on the PCB side) are listed as warnings, not errors - on the
EasyEDA path only: KiCad names are case-sensitive (a net with fullName, a
kicad netlist document, a kicad snapshot board, or --strict-case). Distinct schematic
nets (by page + net id; equal names on different pages are one net) that
reach one PCB net are a short: netCollision.
Exit 0 = identical, 2 = only case warnings (report them, not "passed"),
1 = real differences.

  pad-net-diff.py --sch p1.json [--sch p2.json ...] --pcb board.json [--json] [--ignore-refs H1,H2]
"""
import argparse
import json
import sys

NC = "<NC>"
UNCONNECTED = "unconnected-("


def local_name(net):
    """Display name of a net identity: the sheet path dropped, {slash} → '/'."""
    if net.startswith("/"):
        net = net[net.rfind("/") + 1:]
    return net.replace("{slash}", "/")


def resolver(board_nets):
    """name -> board identity: exact, else the single board net whose display
    name (or unescaped full name) it is; ambiguous names are not guessed."""
    ids = {n for n in board_nets if n and not n.startswith(UNCONNECTED)}
    alias = {}
    for i in ids:
        for a in {local_name(i), i.replace("{slash}", "/")} - {i}:
            alias.setdefault(a, set()).add(i)

    def resolve(name):
        if name in ids:
            return name
        hit = alias.get(name, set())
        return next(iter(hit)) if len(hit) == 1 else name
    return resolve


def unwrap(doc):
    if isinstance(doc, dict) and "components" not in doc:
        for key in ("result", "connectivity", "document"):
            if isinstance(doc.get(key), dict):
                return unwrap(doc[key])
    return doc


def schematic_map(doc, page=0):
    """pin -> (name, has fullName, (page, net id), KiCad doc) | NC | None.
    fullName is the identity; a bare name may still need resolving against
    the board. The (page, net id) source keeps distinct schematic nets
    apart. A KiCad document (kicad netlist: documentId "kicad-sch", even
    from before fullName existed) has case-sensitive names."""
    doc = unwrap(doc)
    kicad = doc.get("documentId") == "kicad-sch"
    net_name = {n["id"]: ((n.get("fullName") or n.get("name") or n["id"]), bool(n.get("fullName")), (page, n["id"]), kicad)
                for n in doc.get("nets") or []}
    comp_ref = {c["id"]: c.get("ref", "") for c in doc.get("components") or []}
    pins = {}
    for c in doc.get("components") or []:
        ref = c.get("ref", "")
        for p in c.get("pins") or []:
            key = (ref, str(p.get("number", "")))
            pins[key] = NC if p.get("noConnected") else None
    for conn in doc.get("connections") or []:
        ref = comp_ref.get(conn.get("componentId"), "")
        key = (ref, str(conn.get("pinNumber", "")))
        nid = conn.get("netId")
        pins[key] = net_name.get(nid, (nid, False, (page, nid), kicad))
    return pins


def unwrap_board(doc):
    if isinstance(doc, dict) and "components" not in doc:
        for key in ("result", "board", "snapshot"):
            if isinstance(doc.get(key), dict):
                return unwrap_board(doc[key])
    return doc


def pcb_map(board):
    pads = {}
    for c in board.get("components") or []:
        ref = c.get("designator", "")
        for p in c.get("pads") or []:
            key = (ref, str(p.get("padNumber", p.get("number", ""))))
            net = p.get("net") or ""
            if key[1] == "" and not net:
                continue  # unnumbered, netless pad: a mechanical hole (KiCad NPTH)
            pads[key] = "" if net.startswith(UNCONNECTED) else net
    return pads


def to_identities(sch, pcb):
    """Schematic pin -> net identity (NC / None kept), resolved per
    schematic net (page + net id), never per pin or per name.

    Pages are merged by net name (the rule of sim power / intent derive):
    equal names on different pages are one net - unless one page has that
    name on several nets, which then all stay distinct. Distinct schematic
    nets that would become one identity (two "VCC" records, or "VCC" and
    "/pwr/VCC") keep distinct labels, so their pads mismatch, and are
    returned as collisions (a short on the board). Also returns the pins
    whose net is KiCad's (fullName or a KiCad document: case-sensitive)."""
    resolve = resolver(pcb.values())
    nets = {v for v in sch.values() if isinstance(v, tuple)}
    ids_by_name = {}
    for name, _, (page, nid), _ in nets:
        ids_by_name.setdefault(name, {}).setdefault(page, set()).add(nid)

    def source(v):
        name, _, (page, nid), _ = v
        if all(len(ids) == 1 for ids in ids_by_name[name].values()):
            return ("name", name)
        return ("net", page, nid, name)

    def label(src):
        return src[1] if src[0] == "name" else f"{src[3]} (page {src[1] + 1} net {src[2]})"

    ident = {v: (v[0] if v[1] else resolve(v[0])) for v in nets}
    sources = {}
    for v, i in ident.items():
        sources.setdefault(i, set()).add(source(v))
    collisions = [{"net": i, "schematic": sorted(label(x) for x in src)} for i, src in sorted(sources.items()) if len(src) > 1]
    clash = {c["net"] for c in collisions}
    out, strict = {}, set()
    for key, v in sch.items():
        if isinstance(v, tuple):
            out[key] = label(source(v)) if ident[v] in clash else ident[v]
            if v[1] or v[3]:
                strict.add(key)
        else:
            out[key] = v
    return out, collisions, strict


def board_is_kicad(board):
    """A `kicad snapshot` board: its names are case-sensitive."""
    rules = board.get("rules") if isinstance(board, dict) else None
    return isinstance(board, dict) and (board.get("source") == "kicad" or (isinstance(rules, dict) and rules.get("source") == "kicad"))


def diff(sch, pcb, ignore=(), strict_case=False):
    """strict_case: no case folding at all (a KiCad board, --strict-case)."""
    sch, collisions, strict = to_identities(sch, pcb)
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
        "netCollision": collisions,
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
        # Case folding only for EasyEDA names: KiCad names (fullName) are
        # case-sensitive, a case-only difference is a different net.
        if pad_net != want and not strict_case and (ref, pin) not in strict and pad_net.lower() == want.lower():
            out["netCaseOnly"].append({"pad": f"{ref}.{pin}", "schematic": want, "pcb": pad_net})
        elif pad_net != want:
            out["netMismatch"].append({"pad": f"{ref}.{pin}", "schematic": net or "", "pcb": pad_net})
    for (ref, pin) in sorted(pcb):
        if ref in ignore or ref not in sch_refs:
            continue
        if (ref, pin) not in sch:
            out["padWithoutPin"].append(f"{ref}.{pin}")
    out["ok"] = not any(out[k] for k in ("missingOnPcb", "extraOnPcb", "pinWithoutPad", "padWithoutPin", "netMismatch", "netCollision"))
    out["counts"] = {k: len(v) for k, v in out.items() if isinstance(v, list)}
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--sch", required=True, action="append", help="connectivity JSON; repeat per page")
    ap.add_argument("--pcb", required=True)
    ap.add_argument("--ignore-refs", default="", help="comma list of designators to skip (mounting holes, logos)")
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--strict-case", action="store_true",
                    help="never treat case-only differences as warnings (automatic for KiCad documents and kicad snapshot boards)")
    a = ap.parse_args()
    sch = {}
    for page, path in enumerate(a.sch):
        with open(path, encoding="utf-8") as f:
            for key, net in schematic_map(json.load(f), page).items():
                # A pin seen with a net on one page wins over "unconnected".
                if sch.get(key) is None:
                    sch[key] = net
    with open(a.pcb, encoding="utf-8") as f:
        board = unwrap_board(json.load(f))
    pcb = pcb_map(board)
    ignore = {r.strip() for r in a.ignore_refs.split(",") if r.strip()}
    rep = diff(sch, pcb, ignore, strict_case=a.strict_case or board_is_kicad(board))
    if a.json:
        json.dump(rep, sys.stdout, ensure_ascii=False, indent=2)
        print()
    else:
        status = "MISMATCH" if not rep["ok"] else ("WARN" if rep["netCaseOnly"] else "OK")
        print(status, rep["counts"])
        for k in ("missingOnPcb", "extraOnPcb", "pinWithoutPad", "padWithoutPin"):
            if rep[k]:
                print(f"  {k}: {', '.join(rep[k][:40])}")
        for c in rep["netCollision"]:
            print(f"  short: schematic nets {', '.join(c['schematic'])} all resolve to pcb net {c['net']}")
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
