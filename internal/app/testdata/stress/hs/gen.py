#!/usr/bin/env python3
"""High-speed stress cases for `make stress-hs` (internal/app/stress_hs_test.go).

Writes, per case directory: connectivity.json (sch connectivity 1.4 subset),
values.json, spec.json (intent product spec), board.json (pcb dump subset with
realistic footprints and a human first placement) and expect.json.

expect.json is derived here BY HAND from the design-guide numbers and the
stackup, with an implementation of the impedance formulas written
independently of pkg/pcbauto (Hammerstad-Jensen microstrip + the
edge-coupled approximation Zdiff = 2·Z0·(1 − 0.48·e^(−0.96·s/h)), IPC-2141
as a cross-check), so a regression in either the formula code or the
stackup constants shows up as a width mismatch.

Re-generate: python3 internal/app/testdata/stress/hs/gen.py
Deterministic; units mil, y up.
"""
import json
import math
import os

HERE = os.path.dirname(os.path.abspath(__file__))

# ---------------------------------------------------------------- stackups
# JLC stackups the engine builds (pcbauto.StackupReference): outer layer →
# nearest plane height (mil), εr. t = 1 oz outer copper = 1.378 mil.
STACK = {
    2: dict(h=62.99, er=4.5, name="JLC 2-layer 1.6mm FR4"),
    4: dict(h=8.28, er=4.4, name="JLC04161H-7628"),
    6: dict(h=4.4, er=4.2, name="JLC06161H-2116"),
}
T_OUTER = 1.378


def z0_hj(w, h, t, er):
    """Hammerstad-Jensen microstrip with the thickness correction."""
    we = w + t / math.pi * math.log(4 * math.e / math.sqrt((t / h) ** 2 + (t / (w * math.pi + 1.1 * t * math.pi)) ** 2)) * (1 + 1 / er) / 2
    u = we / h
    a = 1 + math.log((u ** 4 + (u / 52) ** 2) / (u ** 4 + 0.432)) / 49 + math.log(1 + (u / 18.1) ** 3) / 18.7
    b = 0.564 * ((er - 0.9) / (er + 3)) ** 0.053
    eeff = (er + 1) / 2 + (er - 1) / 2 * (1 + 10 / u) ** (-a * b)
    f = 6 + (2 * math.pi - 6) * math.exp(-((30.666 / u) ** 0.7528))
    return 60 / math.sqrt(eeff) * math.log(f / u + math.sqrt(1 + 4 / (u * u)))


def zdiff(w, s, h, t, er):
    return 2 * z0_hj(w, h, t, er) * (1 - 0.48 * math.exp(-0.96 * s / h))


def zdiff_ipc(w, s, h, t, er):
    z0 = 87 / math.sqrt(er + 1.41) * math.log(5.98 * h / (0.8 * w + t))
    return 2 * z0 * (1 - 0.48 * math.exp(-0.96 * s / h))


def solve_diff(target, h, er, gap, t=T_OUTER):
    lo, hi = 2.0, 100.0
    for _ in range(80):
        m = (lo + hi) / 2
        if zdiff(m, gap, h, t, er) > target:
            lo = m
        else:
            hi = m
    return (lo + hi) / 2


def solve_se(target, h, er, t=T_OUTER):
    lo, hi = 1.0, 200.0
    for _ in range(80):
        m = (lo + hi) / 2
        if z0_hj(m, h, t, er) > target:
            lo = m
        else:
            hi = m
    return (lo + hi) / 2


def ceil1(v):
    return math.ceil(v * 10 - 1e-9) / 10


# ------------------------------------------------------------- footprints
# A footprint is a list of (number, dx, dy, w, h, layer); layer 1 top,
# 2 bottom, 12 through-hole (all layers).

def chip(size):
    d = {"0201": (11.8, 11.8, 9.8), "0402": (19.7, 23.6, 21.7), "0603": (31.5, 31.5, 35.4),
         "0805": (37.4, 39.4, 51.2), "1206": (59.1, 47.2, 70.9), "1808": (90.6, 59.1, 90.6)}[size]
    off, w, h = d
    return [("1", -off, 0, w, h, 1), ("2", off, 0, w, h, 1)]


def sot23_6():
    out = []
    for i, y in enumerate([37.4, 0, -37.4]):
        out.append((str(i + 1), -44, y, 40, 23.6, 1))
    for i, y in enumerate([-37.4, 0, 37.4]):
        out.append((str(i + 4), 44, y, 40, 23.6, 1))
    return out


def sot223():
    p = 90.6
    return [("1", -p, -120, 47, 79, 1), ("2", 0, -120, 47, 79, 1), ("3", p, -120, 47, 79, 1), ("4", 0, 120, 130, 79, 1)]


def uson10_flow():
    """TPD4E05U06-style flow-through ESD array (USON-10, 0.5 mm pitch):
    channel k passes over pin k and its mirror pin (1↔10, 2↔9, 4↔7, 5↔6)."""
    out = []
    xs = [-39.4, -19.7, 0, 19.7, 39.4]
    for i, x in enumerate(xs):
        out.append((str(i + 1), x, -19.7, 9.8, 23.6, 1))
    for i, x in enumerate(reversed(xs)):
        out.append((str(i + 6), x, 19.7, 9.8, 23.6, 1))
    return out


def qfn(k, pitch=19.685, body=236.2, padl=31.5, padw=11.8, ep=160):
    """QFN with k pins per side, numbered counter-clockwise from the top of
    the left side; exposed pad = 4k+1."""
    out = []
    half = body / 2 - padl / 2 + 6
    span = (k - 1) / 2 * pitch
    n = 1
    for i in range(k):  # left, top → bottom
        out.append((str(n), -half, span - i * pitch, padl, padw, 1)); n += 1
    for i in range(k):  # bottom, left → right
        out.append((str(n), -span + i * pitch, -half, padw, padl, 1)); n += 1
    for i in range(k):  # right, bottom → top
        out.append((str(n), half, -span + i * pitch, padl, padw, 1)); n += 1
    for i in range(k):  # top, right → left
        out.append((str(n), span - i * pitch, half, padw, padl, 1)); n += 1
    if ep:
        out.append((str(n), 0, 0, ep, ep, 1))
    return out


ROWS = "ABCDEFGHJKLMNPRTUVWY"


def bga(rows, cols, pitch, pad, skip_cols=()):
    out = []
    for r in range(rows):
        for c in range(cols):
            if c + 1 in skip_cols:
                continue
            x = (c - (cols - 1) / 2) * pitch
            y = ((rows - 1) / 2 - r) * pitch
            out.append((f"{ROWS[r]}{c + 1}", x, y, pad, pad, 1))
    return out


def typec_straddle():
    """24-pin USB 3.x Type-C straddle-mount receptacle: row A on TOP, row B
    on BOTTOM (the board edge sits between them), 0.5 mm pitch, plug face
    toward −x. B(i) lies under A(13−i). Four shell tabs through-hole."""
    out = []
    for i in range(12):
        y = (5.5 - i) * 19.685
        out.append((f"A{i + 1}", 0, y, 40, 11.8, 1))
        out.append((f"B{12 - i}", 0, y, 40, 11.8, 2))
    for j, (x, y) in enumerate([(-30, 175), (-30, -175), (90, 175), (90, -175)]):
        out.append((f"S{j + 1}", x, y, 45, 70, 12))
    return out


def hdmi_a():
    """HDMI type A receptacle (19 pins, 0.5 mm pitch, odd row / even row
    staggered), plug face toward −x, 4 shell tabs."""
    out = []
    for p in range(1, 20):
        y = (10 - p) * 19.685
        x = 0 if p % 2 else 50
        out.append((str(p), x, y, 55, 11.8, 1))
    for j, (x, y) in enumerate([(-20, 290), (-20, -290), (160, 270), (160, -270)]):
        out.append((f"S{j + 1}", x, y, 60, 90, 12))
    return out


def rj45():
    """RJ45 jack without magnetics (8 TH contacts in two staggered rows,
    1.02 mm pitch) + 2 shield posts; plug face toward −x."""
    out = []
    for p in range(1, 9):
        y = (4.5 - p) * 40.2
        x = 0 if p % 2 else 70
        out.append((str(p), x + 150, y, 60, 60, 12))
    out.append(("S1", 20, 305, 90, 90, 12))
    out.append(("S2", 20, -305, 90, 90, 12))
    return out


def soic_w(n, pitch=50, row=400, padl=80, padw=24):
    """Wide SOIC (Ethernet magnetics): pins 1..n/2 on the left (top → bottom),
    n/2+1..n on the right (bottom → top)."""
    k = n // 2
    out = []
    for i in range(k):
        out.append((str(i + 1), -row / 2, ((k - 1) / 2 - i) * pitch, padl, padw, 1))
    for i in range(k):
        out.append((str(k + i + 1), row / 2, (-(k - 1) / 2 + i) * pitch, padl, padw, 1))
    return out


def m2_socket():
    """M.2 key-M socket (75 positions, 0.5 mm pitch; odd pins front row,
    even pins rear row; key positions 59–66 removed), card toward +y."""
    out = []
    for p in range(1, 76):
        if 59 <= p <= 66:
            continue
        x = (p - 38) * 9.84
        y = 0 if p % 2 else 60
        out.append((str(p), x, y, 11.8, 40, 1))
    out.append(("S1", -440, 30, 60, 100, 12))
    out.append(("S2", 440, 30, 60, 100, 12))
    return out


def terminal2():
    return [("1", -100, 0, 80, 80, 12), ("2", 100, 0, 80, 80, 12)]


def rot(dx, dy, deg):
    r = math.radians(deg)
    return dx * math.cos(r) - dy * math.sin(r), dx * math.sin(r) + dy * math.cos(r)


# -------------------------------------------------------------------- case
class Case:
    def __init__(self, name, title, w, h, layers, rules=None):
        self.name, self.title, self.w, self.h, self.layers = name, title, w, h, layers
        self.rules = rules or dict(clearanceMil=6, trackWidthMil=6, trackWidthMinMil=5, viaDrillMil=12, viaDiameterMil=24, copperToEdgeMil=12)
        self.parts = []
        self.spec = {}
        self.expect = {}

    def add(self, ref, mpn, fp, x, y, rot_deg=0, pins=None, names=None, value="", desc="", locked=False, body=None):
        """pins: {number: net}; names: {number: pin name} (default = number)."""
        pins = pins or {}
        names = names or {}
        self.parts.append(dict(ref=ref, mpn=mpn, fp=fp, x=x, y=y, rot=rot_deg, pins=pins, names=names,
                               value=value, desc=desc, locked=locked, body=body))

    # --- writers ---
    def board(self):
        comps = []
        for i, p in enumerate(self.parts):
            pads = []
            minx = miny = 1e9
            maxx = maxy = -1e9
            for j, (num, dx, dy, w, h, layer) in enumerate(p["fp"]):
                rx, ry = rot(dx, dy, p["rot"])
                pw, ph = (h, w) if p["rot"] % 180 == 90 else (w, h)
                x, y = round(p["x"] + rx, 3), round(p["y"] + ry, 3)
                pads.append(dict(primitiveId=f"e{i}p{j}", padNumber=num, net=p["pins"].get(num, ""), layer=layer,
                                 x=x, y=y, width=round(pw, 3), height=round(ph, 3)))
                minx, maxx = min(minx, x - pw / 2), max(maxx, x + pw / 2)
                miny, maxy = min(miny, y - ph / 2), max(maxy, y + ph / 2)
            m = 12
            bb = dict(minX=round(minx - m, 3), minY=round(miny - m, 3), maxX=round(maxx + m, 3), maxY=round(maxy + m, 3))
            if p["body"]:
                bx0, by0, bx1, by1 = p["body"]
                pts = [rot(bx0, by0, p["rot"]), rot(bx1, by1, p["rot"]), rot(bx0, by1, p["rot"]), rot(bx1, by0, p["rot"])]
                bb = dict(minX=round(min(bb["minX"], p["x"] + min(q[0] for q in pts)), 3), minY=round(min(bb["minY"], p["y"] + min(q[1] for q in pts)), 3),
                          maxX=round(max(bb["maxX"], p["x"] + max(q[0] for q in pts)), 3), maxY=round(max(bb["maxY"], p["y"] + max(q[1] for q in pts)), 3))
            comps.append(dict(primitiveId=f"e{i}", designator=p["ref"], device=p["mpn"], layer=1, x=p["x"], y=p["y"],
                              rotation=p["rot"], locked=p["locked"], bbox=bb, pads=pads))
        pts = [[0, 0], [self.w, 0], [self.w, self.h], [0, self.h]]
        return dict(components=comps, outline=dict(bbox=dict(minX=0, minY=0, maxX=self.w, maxY=self.h), points=pts, source="synthetic"),
                    copperLayers=self.layers, rules=self.rules, project="stress-hs/" + self.name,
                    _provenance="synthetic high-speed stress case (gen.py): " + self.title)

    def connectivity(self):
        comps, nets, conns = [], {}, []
        for p in self.parts:
            numbers = []
            for (num, *_rest) in p["fp"]:
                if num not in numbers:
                    numbers.append(num)
            comps.append(dict(id="cmp-" + p["ref"], ref=p["ref"], device=dict(name="={Value}"),
                              pins=[dict(number=n, name=p["names"].get(n, n)) for n in numbers]))
            for n in numbers:
                net = p["pins"].get(n, "")
                if not net:
                    continue
                nets.setdefault(net, dict(id="net-" + net, name=net, role=role_of(net)))
                conns.append(dict(componentId="cmp-" + p["ref"], pinNumber=n, netId="net-" + net, kind="netlist"))
        return dict(schemaVersion="1.4", projectId="stress-hs-" + self.name, documentId="page-1",
                    _provenance="synthetic high-speed stress case (gen.py): " + self.title,
                    components=comps, nets=sorted(nets.values(), key=lambda n: n["name"]), connections=conns)

    def values(self):
        parts = {}
        for p in self.parts:
            v = {"mpn": p["mpn"]}
            if p["value"]:
                v["value"] = p["value"]
            if p["desc"]:
                v["description"] = p["desc"]
            parts[p["ref"]] = v
        return {"_provenance": "synthetic values for connectivity.json (gen.py)", "parts": parts}

    def write(self):
        d = os.path.join(HERE, self.name)
        os.makedirs(d, exist_ok=True)
        for fn, obj in [("board.json", self.board()), ("connectivity.json", self.connectivity()), ("values.json", self.values()),
                        ("spec.json", self.spec), ("expect.json", self.expect)]:
            with open(os.path.join(d, fn), "w") as f:
                json.dump(obj, f, indent=1, ensure_ascii=False)
                f.write("\n")


def role_of(net):
    u = net.upper()
    if "GND" in u:
        return "ground"
    if u.startswith("+") or u.startswith("VBUS") or u.startswith("5V") or u.startswith("DDR_V") or u in ("VDD", "VCC"):
        return "power"
    return "signal"


def pair_expect(case, p, n, iface, ohm, skew, vias, group=""):
    st = STACK[case.layers]
    gap = case.rules["clearanceMil"]
    w = solve_diff(ohm, st["h"], st["er"], gap)
    out = dict(p=p, n=n, interface=iface, ohm=ohm, maxSkewMil=skew, maxVias=vias,
               widthMil=ceil1(w), gapMil=math.ceil(gap * 2) / 2,
               why=f"{ohm} Ω edge-coupled microstrip on {st['name']} (h={st['h']} mil, εr={st['er']}, t={T_OUTER} mil, s={gap} mil): "
                   f"Hammerstad-Jensen w={w:.2f} mil (IPC-2141 check {zdiff_ipc(w, gap, st['h'], T_OUTER, st['er']):.1f} Ω)")
    if group:
        out["group"] = group
    return out


def cap_decaps(case, refs_nets_xy):
    for ref, net, x, y in refs_nets_xy:
        case.add(ref, "CL05B104KO5NNNC", chip("0402"), x, y, 90, {"1": net, "2": "GND"}, value="100nF", desc="Capacitance:100nF Voltage Rating:16V")


def power_3v3(case, vin, x, y, refs=("U9", "C90", "C91")):
    u, cin, cout = refs
    case.add(u, "AMS1117-3.3", sot223(), x, y, 0, {"1": "GND", "2": "+3V3", "3": vin, "4": "+3V3"},
             names={"1": "GND", "2": "VOUT", "3": "VIN", "4": "TAB"})
    case.add(cin, "CL10A106KP8NNNC", chip("0603"), x + 90, y - 250, 0, {"1": vin, "2": "GND"}, value="10uF", desc="Capacitance:10uF Voltage Rating:10V")
    case.add(cout, "CL10A106KP8NNNC", chip("0603"), x - 90, y + 250, 0, {"1": "+3V3", "2": "GND"}, value="10uF", desc="Capacitance:10uF Voltage Rating:10V")


# ================================================================ (a) USB3
def case_usb3(name="usb3-typec", layers=4, negative=False):
    c = Case(name, "USB 3.0 device with an integrated Type-C flip mux (TX1/TX2/RX1/RX2) behind a straddle-mount Type-C: "
                   "100 nF TX AC caps at the controller, flow-through ESD at the connector; USB 2.0 D+/D- with USBLC6; "
                   "5.1 kΩ CC pull-downs; VBUS divider sense; AMS1117 3V3" +
             (" — NEGATIVE: ordered as a 2-layer board" if negative else ""), 1900, 1250, layers)
    y0 = 625
    j = {"A1": "GND", "A12": "GND", "B1": "GND", "B12": "GND", "A4": "VBUS", "A9": "VBUS", "B4": "VBUS", "B9": "VBUS",
         "A2": "USB3_SSTX1C_P", "A3": "USB3_SSTX1C_N", "B11": "USB3_SSRX1_P", "B10": "USB3_SSRX1_N",
         "B2": "USB3_SSTX2C_P", "B3": "USB3_SSTX2C_N", "A11": "USB3_SSRX2_P", "A10": "USB3_SSRX2_N",
         "A6": "USB_DP", "B6": "USB_DP", "A7": "USB_DM", "B7": "USB_DM", "A5": "CC1", "B5": "CC2",
         "S1": "GND", "S2": "GND", "S3": "GND", "S4": "GND"}
    jn = {"A1": "GND", "A12": "GND", "B1": "GND", "B12": "GND", "A4": "VBUS", "A9": "VBUS", "B4": "VBUS", "B9": "VBUS",
          "A2": "SSTXp1", "A3": "SSTXn1", "B11": "SSRXp1", "B10": "SSRXn1", "B2": "SSTXp2", "B3": "SSTXn2", "A11": "SSRXp2",
          "A10": "SSRXn2", "A6": "Dp1", "B6": "Dp2", "A7": "Dn1", "B7": "Dn2", "A5": "CC1", "B5": "CC2", "A8": "SBU1", "B8": "SBU2"}
    c.add("J1", "TYPE-C-24P-STRADDLE", typec_straddle(), 50, y0, 0, j, jn, locked=True, body=(-60, -200, 140, 200))
    # Flow-through ESD (rotated: lines run along x; pin 5/6 on top).
    esd_names = {"1": "D1+", "2": "D1-", "3": "GND", "4": "D2+", "5": "D2-", "6": "D2-", "7": "D2+", "8": "GND", "9": "D1-", "10": "D1+"}
    def esd(ref, y, top_p, top_n, bot_p, bot_n):
        c.add(ref, "TPD4E05U06DQAR", uson10_flow(), 330, y, 90,
              {"5": top_p, "6": top_p, "4": top_n, "7": top_n, "2": bot_p, "9": bot_p, "1": bot_n, "10": bot_n, "3": "GND", "8": "GND"}, esd_names)
    esd("U4", y0 + 79, "USB3_SSTX1C_P", "USB3_SSTX1C_N", "USB3_SSRX1_P", "USB3_SSRX1_N")
    esd("U5", y0 - 79, "USB3_SSRX2_N", "USB3_SSRX2_P", "USB3_SSTX2C_N", "USB3_SSTX2C_P")
    c.add("U6", "USBLC6-2SC6", sot23_6(), 560, y0, 0, {"1": "USB_DP", "6": "USB_DP", "3": "USB_DM", "4": "USB_DM", "2": "GND", "5": "VBUS"},
          {"1": "I/O1", "2": "GND", "3": "I/O2", "4": "I/O2", "5": "VBUS", "6": "I/O1"})
    # AC coupling caps on both TX pairs, at the controller.
    for ref, net, y in [("C1", "USB3_SSTX1", y0 + 118), ("C2", "USB3_SSTX1", y0 + 89), ("C3", "USB3_SSTX2", y0 - 89), ("C4", "USB3_SSTX2", y0 - 118)]:
        pol = "_P" if ref in ("C1", "C4") else "_N"
        c.add(ref, "GRM033R61A104KE15D", chip("0201"), 820, y, 0, {"1": net + "C" + pol, "2": net + pol}, value="100nF", desc="Capacitance:100nF Voltage Rating:10V")
    # U1 controller QFN-48 7x7: the left side mirrors the connector order.
    left = ["SSTX1P", "SSTX1N", "GND", "SSRX1P", "SSRX1N", "DP", "DM", "GND", "SSRX2N", "SSRX2P", "SSTX2N", "SSTX2P"]
    bottom = ["RESETN", "XI", "XO", "GND", "VDD33", None, None, None, None, None, None, "GND"]
    right = [None] * 10 + ["VBUS_DET", "GND"]
    top = ["VDD33", "GND"] + [None] * 9 + ["VDD33"]
    upins = left + bottom + right + top
    unet = {"SSTX1P": "USB3_SSTX1_P", "SSTX1N": "USB3_SSTX1_N", "SSRX1P": "USB3_SSRX1_P", "SSRX1N": "USB3_SSRX1_N",
            "SSTX2P": "USB3_SSTX2_P", "SSTX2N": "USB3_SSTX2_N", "SSRX2P": "USB3_SSRX2_P", "SSRX2N": "USB3_SSRX2_N",
            "DP": "USB_DP", "DM": "USB_DM", "GND": "GND", "VDD33": "+3V3", "VBUS_DET": "VBUS_DET", "RESETN": "RESETN"}
    pins = {str(i + 1): unet[n] for i, n in enumerate(upins) if n in unet}
    pins["49"] = "GND"
    names = {str(i + 1): n for i, n in enumerate(upins) if n}
    names["49"] = "EP"
    c.add("U1", "VL817-Q7", qfn(12, body=275.6, ep=200), 1150, y0, 0, pins, names)
    c.add("R1", "0402WGF5101TCE", chip("0402"), 220, y0 + 330, 90, {"1": "CC1", "2": "GND"}, value="5.1k")
    c.add("R2", "0402WGF5101TCE", chip("0402"), 220, y0 - 330, 90, {"1": "CC2", "2": "GND"}, value="5.1k")
    c.add("R3", "0402WGF1002TCE", chip("0402"), 1400, y0 + 330, 90, {"1": "VBUS", "2": "VBUS_DET"}, value="10k")
    c.add("R4", "0402WGF1002TCE", chip("0402"), 1100, y0 - 330, 0, {"1": "+3V3", "2": "RESETN"}, value="10k")
    cap_decaps(c, [("C5", "+3V3", 1060, y0 + 250), ("C6", "+3V3", 1240, y0 + 250), ("C7", "+3V3", 1240, y0 - 250)])
    power_3v3(c, "VBUS", 1600, 950)
    c.spec = {"layers": layers, "standard": {"name": "IPC-2221B"}, "usbBudgetA": 0.9}
    pairs = []
    for base in ["USB3_SSTX1", "USB3_SSTX1C", "USB3_SSRX1", "USB3_SSTX2", "USB3_SSTX2C", "USB3_SSRX2"]:
        pairs.append(pair_expect(c, base + "_P", base + "_N", "USB3", 90, 5, 2))
    pairs.append(pair_expect(c, "USB_DP", "USB_DM", "USB", 90, 100, 2))
    c.expect = dict(
        case=name, title=c.title, layers=layers, stackup=STACK[layers],
        pairs=pairs,
        acCaps=[["C1", "C2"], ["C3", "C4"]],
        findingsMust=["reference-plane-missing", "impedance-uncontrolled"] if negative else ["ac-coupling"],
        findingsMustNot=[] if negative else ["reference-plane-missing", "impedance-uncontrolled", "usb-esd", "ac-coupling-missing"],
        route=dict(minCompletion=100, maxDRC=0, maxSplitCrossings=0),
        negative=negative,
        siMust=["no-reference"] if negative else [],
        note="USB 3.2 Gen1 layout: 90 Ω ±10 %, intra-pair ≤ 5 mil, ≤ 2 vias per net; TX AC caps (75–265 nF, 100 nF) at the transmitter, "
             "symmetric; USB 2.0 pair 90 Ω, ≤ 100 mil. Negative variant: same netlist on a 2-layer board — impedance cannot be controlled "
             "(no adjacent plane at 62.99 mil) and must be flagged, never silently routed as 'pass'.",
    )
    if negative:
        for p in c.expect["pairs"]:
            p["widthMil"], p["gapMil"], p["uncontrolled"] = c.rules["trackWidthMil"], c.rules["clearanceMil"], True
            p["why"] = "2-layer: 90 Ω would need ≫ 25 mil over 62.99 mil — uncontrolled, routed as a coupled 6/6 mil pair"
    return c


# ================================================================ (b) HDMI
def case_hdmi():
    c = Case("hdmi-tx", "HDMI 1.4 source: TMDS D0-D2 + clock (100 Ω) through two flow-through ESD arrays to a type-A receptacle; "
                        "DDC with pull-ups, HPD, +5V with a PTC; lanes inter-pair matched (HDMI_LANES recognised from the names)", 1800, 1300, 4)
    y0 = 650
    jn = {"1": "HDMI_D2_P", "2": "GND", "3": "HDMI_D2_N", "4": "HDMI_D1_P", "5": "GND", "6": "HDMI_D1_N", "7": "HDMI_D0_P",
          "8": "GND", "9": "HDMI_D0_N", "10": "HDMI_CLK_P", "11": "GND", "12": "HDMI_CLK_N", "13": "HDMI_CEC", "15": "HDMI_SCL",
          "16": "HDMI_SDA", "17": "GND", "18": "HDMI_5V", "19": "HDMI_HPD", "S1": "GND", "S2": "GND", "S3": "GND", "S4": "GND"}
    names = {"1": "D2+", "2": "D2S", "3": "D2-", "4": "D1+", "5": "D1S", "6": "D1-", "7": "D0+", "8": "D0S", "9": "D0-", "10": "CK+",
             "11": "CKS", "12": "CK-", "13": "CEC", "14": "UTIL", "15": "SCL", "16": "SDA", "17": "DDCGND", "18": "+5V", "19": "HPD"}
    c.add("J1", "HDMI-A-19P-SMT", hdmi_a(), 50, y0, 0, jn, names, locked=True, body=(-40, -320, 220, 320))
    # Two flow-through ESD arrays: U4 D2/D1, U5 D0/CLK.
    esd_names = {"1": "D1+", "2": "D1-", "3": "GND", "4": "D2+", "5": "D2-", "6": "D2-", "7": "D2+", "8": "GND", "9": "D1-", "10": "D1+"}
    c.add("U4", "TPD4E05U06DQAR", uson10_flow(), 380, y0 + 110, 90,
          {"1": "HDMI_D2_P", "10": "HDMI_D2_P", "2": "HDMI_D2_N", "9": "HDMI_D2_N", "3": "GND", "8": "GND",
           "4": "HDMI_D1_P", "7": "HDMI_D1_P", "5": "HDMI_D1_N", "6": "HDMI_D1_N"}, esd_names)
    c.add("U5", "TPD4E05U06DQAR", uson10_flow(), 380, y0 - 110, 90,
          {"1": "HDMI_D0_P", "10": "HDMI_D0_P", "2": "HDMI_D0_N", "9": "HDMI_D0_N", "3": "GND", "8": "GND",
           "4": "HDMI_CLK_P", "7": "HDMI_CLK_P", "5": "HDMI_CLK_N", "6": "HDMI_CLK_N"}, esd_names)
    # U1 HDMI transmitter QFN-48 7x7 (0.5 mm): TMDS on the left side facing the connector.
    left = ["TX2P", "TX2N", "GND", "TX1P", "TX1N", "GND", "TX0P", "TX0N", "GND", "TXCP", "TXCN", "AVDD"]
    bottom = ["SCL", "SDA", "HPD", "CEC", "GND", "DVDD", None, None, None, None, None, "GND"]
    right = [None] * 12
    top = ["DVDD", "GND"] + [None] * 9 + ["AVDD"]
    pn = left + bottom + right + top
    net = {"TX2P": "HDMI_D2_P", "TX2N": "HDMI_D2_N", "TX1P": "HDMI_D1_P", "TX1N": "HDMI_D1_N", "TX0P": "HDMI_D0_P", "TX0N": "HDMI_D0_N",
           "TXCP": "HDMI_CLK_P", "TXCN": "HDMI_CLK_N", "GND": "GND", "AVDD": "+3V3", "DVDD": "+3V3", "SCL": "HDMI_SCL", "SDA": "HDMI_SDA",
           "HPD": "HDMI_HPD", "CEC": "HDMI_CEC"}
    pins = {str(i + 1): net[n] for i, n in enumerate(pn) if n}
    pins["49"] = "GND"
    nm = {str(i + 1): n for i, n in enumerate(pn) if n}
    c.add("U1", "IT66121FN", qfn(12, body=275.6, ep=200), 950, y0, 0, pins, nm)
    c.add("R1", "0402WGF4701TCE", chip("0402"), 700, y0 - 330, 90, {"1": "HDMI_5V", "2": "HDMI_SCL"}, value="4.7k")
    c.add("R2", "0402WGF4701TCE", chip("0402"), 760, y0 - 330, 90, {"1": "HDMI_5V", "2": "HDMI_SDA"}, value="4.7k")
    c.add("R3", "0402WGF1002TCE", chip("0402"), 820, y0 - 330, 90, {"1": "HDMI_HPD", "2": "GND"}, value="10k")
    c.add("F1", "SMD0805P050TF", chip("0805"), 400, y0 - 420, 0, {"1": "5V_IN", "2": "HDMI_5V"}, value="0.5A", desc="Current Rating:500mA")
    c.add("J2", "KF301-5.0-2P", terminal2(), 1500, 1150, 0, {"1": "5V_IN", "2": "GND"}, {"1": "1", "2": "2"}, locked=True)
    cap_decaps(c, [("C5", "+3V3", 870, y0 + 240), ("C6", "+3V3", 1030, y0 + 240), ("C7", "+3V3", 1180, y0 - 240)])
    power_3v3(c, "5V_IN", 1500, 500)
    c.spec = {"layers": 4, "standard": {"name": "IPC-2221B"}}
    pairs = [pair_expect(c, f"HDMI_{b}_P", f"HDMI_{b}_N", "HDMI", 100, 5, 2, group="HDMI_LANES") for b in ("D0", "D1", "D2", "CLK")]
    c.expect = dict(case=c.name, title=c.title, layers=4, stackup=STACK[4], pairs=pairs,
                    groups=[dict(name="HDMI_LANES", units=4, tolMil=100,
                                 why="HDMI TMDS inter-pair skew: ≤ 0.2 T_bit at the source (HDMI 1.4 §4.2.x) ≈ 59 ps at 3.4 Gb/s ≈ 400 mil; "
                                     "the engine's HDMI class keeps a conservative 100 mil design-guide default")],
                    findingsMustNot=["reference-plane-missing", "impedance-uncontrolled"],
                    route=dict(minCompletion=100, maxDRC=0, maxSplitCrossings=0), negative=False,
                    note="HDMI 1.4 TMDS: 100 Ω ±15 %, intra-pair ≤ 5 mil, lanes (incl. clock) inter-pair matched, ≤ 2 vias")
    return c


# ============================================================ (c) Ethernet
def case_eth():
    c = Case("gbe-rj45", "Gigabit Ethernet: RTL8211-class PHY → 4 MDI pairs (100 Ω) → discrete 1000BASE-T magnetics (SOIC-24W) → RJ45; "
                         "Bob-Smith termination (4× 75 Ω → 1 nF/2 kV → chassis); chassis ↔ GND 1 nF/2 kV; IEEE 802.3 1500 Vrms isolation "
                         "declared on the chassis/cable side", 2200, 1500, 4)
    y0 = 750
    rj = {"1": "ETH_TRD0_P", "2": "ETH_TRD0_N", "3": "ETH_TRD1_P", "6": "ETH_TRD1_N", "4": "ETH_TRD2_P", "5": "ETH_TRD2_N",
          "7": "ETH_TRD3_P", "8": "ETH_TRD3_N", "S1": "CHASSIS_GND", "S2": "CHASSIS_GND"}
    c.add("J1", "RJ45-8P8C-NOMAG", rj45(), 60, y0, 0, rj, locked=True, body=(-60, -330, 560, 330))
    # T1 magnetics: pins 1-12 PHY side, 13-24 cable side (mirror).
    phy = ["TCT0", "TD0+", "TD0-", "TCT1", "TD1+", "TD1-", "TCT2", "TD2+", "TD2-", "TCT3", "TD3+", "TD3-"]
    cab = ["MX3-", "MX3+", "MCT3", "MX2-", "MX2+", "MCT2", "MX1-", "MX1+", "MCT1", "MX0-", "MX0+", "MCT0"]
    tn = {}
    names = {}
    for i, n in enumerate(phy):
        names[str(i + 1)] = n
        k = n[-2] if n[-1] in "+-" else n[-1]
        tn[str(i + 1)] = (f"ETH_MDI{k}_P" if n.endswith("+") else f"ETH_MDI{k}_N") if n[0] == "T" and n[1] == "D" else f"ETH_TCT{k}"
    for i, n in enumerate(cab):
        names[str(13 + i)] = n
        k = n[-2] if n[-1] in "+-" else n[-1]
        tn[str(13 + i)] = (f"ETH_TRD{k}_P" if n.endswith("+") else f"ETH_TRD{k}_N") if n.startswith("MX") else f"ETH_MCT{k}"
    # SOIC-24W with the cable side (13-24) on the left, facing the jack.
    c.add("T1", "H5007NL", soic_w(24, row=-420), 850, y0, 0, tn, names, body=(-260, -330, 260, 330))
    # Bob-Smith: MCTk — 75 Ω — ETH_BS — 1 nF/2 kV — CHASSIS_GND.
    for k in range(4):
        c.add(f"R{10 + k}", "0603WAF750JT5E", chip("0603"), 700, y0 - 520 + k * 60, 0, {"1": f"ETH_MCT{k}", "2": "ETH_BS"}, value="75R")
    c.add("C20", "1808B102K202NT", chip("1808"), 450, y0 - 560, 0, {"1": "ETH_BS", "2": "CHASSIS_GND"}, value="1nF", desc="Capacitance:1nF Voltage Rating:2kV")
    c.add("C21", "1808B102K202NT", chip("1808"), 450, y0 + 560, 0, {"1": "CHASSIS_GND", "2": "GND"}, value="1nF", desc="Capacitance:1nF Voltage Rating:2kV")
    # PHY-side centre taps decoupled to GND.
    for k in range(4):
        c.add(f"C{10 + k}", "CL05B104KO5NNNC", chip("0402"), 1180, y0 - 330 + k * 220, 90, {"1": f"ETH_TCT{k}", "2": "GND"}, value="100nF")
    # U1 PHY QFN-48 7x7: MDI on the left side facing the magnetics.
    left = ["MDIP0", "MDIN0", "AVDD", "MDIP1", "MDIN1", "GND", "MDIP2", "MDIN2", "AVDD", "MDIP3", "MDIN3", "GND"]
    bottom = ["RSET", "GND", "DVDD", None, None, None, None, None, None, None, "GND", "DVDD"]
    right = [None] * 10 + ["GND", "DVDD"]
    top = ["DVDD", "GND"] + [None] * 10
    pn = left + bottom + right + top
    net = {"MDIP0": "ETH_MDI0_P", "MDIN0": "ETH_MDI0_N", "MDIP1": "ETH_MDI1_P", "MDIN1": "ETH_MDI1_N", "MDIP2": "ETH_MDI2_P",
           "MDIN2": "ETH_MDI2_N", "MDIP3": "ETH_MDI3_P", "MDIN3": "ETH_MDI3_N", "AVDD": "+3V3", "DVDD": "+3V3", "GND": "GND", "RSET": "PHY_RSET"}
    pins = {str(i + 1): net[n] for i, n in enumerate(pn) if n}
    pins["49"] = "GND"
    nm = {str(i + 1): n for i, n in enumerate(pn) if n}
    c.add("U1", "RTL8211F-CG", qfn(12, body=275.6, ep=200), 1500, y0, 0, pins, nm)
    c.add("R1", "0402WGF2491TCE", chip("0402"), 1500, y0 - 330, 0, {"1": "PHY_RSET", "2": "GND"}, value="2.49k")
    cap_decaps(c, [("C5", "+3V3", 1420, y0 + 250), ("C6", "+3V3", 1580, y0 + 250), ("C7", "+3V3", 1700, y0 - 250)])
    c.add("J2", "KF301-5.0-2P", terminal2(), 1950, 1350, 0, {"1": "5V_IN", "2": "GND"}, {"1": "1", "2": "2"}, locked=True)
    power_3v3(c, "5V_IN", 1950, 800)
    c.spec = {"layers": 4, "standard": {"name": "IEC62368-1", "insulation": "functional", "pollutionDegree": 2},
              "domains": [{"kind": "isolated-secondary", "nets": ["CHASSIS_GND"], "isolationVrms": 1500}]}
    pairs = [pair_expect(c, f"ETH_MDI{k}_P", f"ETH_MDI{k}_N", "ETH", 100, 50, 2) for k in range(4)]
    pairs += [pair_expect(c, f"ETH_TRD{k}_P", f"ETH_TRD{k}_N", "ETH", 100, 50, 2) for k in range(4)]
    wpk = 1500 * math.sqrt(2)
    c.expect = dict(case=c.name, title=c.title, layers=4, stackup=STACK[4], pairs=pairs,
                    isolation=dict(domainNet="CHASSIS_GND", insulation="basic", requiredWithstandV=round(wpk, 1), clearanceMm=1.5, creepageMm=1.5,
                                   bridges=["C21", "T1"],
                                   why=f"IEEE 802.3 §40.6.1.1: 1500 Vrms electric strength → {wpk:.0f} V peak; IEC 60664-1 Table F.2 case A, PD2, "
                                       "step table → 2.5 kV row = 1.5 mm clearance; working voltage ≤ 3.3 V → creepage (Table F.4) below it, "
                                       "raised to the clearance → 1.5 mm; the gap under the magnetics (pin rows 420 mil apart) holds it without a slot"),
                    findingsMustNot=["reference-plane-missing", "impedance-uncontrolled", "insulation-slot"],
                    route=dict(minCompletion=100, maxDRC=0, maxSplitCrossings=0, maxIsolationFindings=0), negative=False,
                    note="1000BASE-T MDI: 100 Ω ±10 %, intra-pair ≤ 50 mil, ≤ 2 vias; no plane under the cable side of the magnetics")
    return c


# ================================================================ (d) PCIe
def case_pcie():
    c = Case("pcie-m2", "PCIe Gen2 x1 root port → M.2 key-M socket: host TX with 100 nF AC caps, RX direct (caps on the module), "
                        "100 MHz HCSL REFCLK pair, PERST#/CLKREQ#/WAKE#", 1900, 1500, 4)
    m2 = {"41": "PCIE_TXC_N", "43": "PCIE_TXC_P", "47": "PCIE_RX_N", "49": "PCIE_RX_P", "53": "PCIE_REFCLK_N", "55": "PCIE_REFCLK_P",
          "50": "PCIE_PERST_N", "52": "PCIE_CLKREQ_N", "54": "PCIE_WAKE_N", "S1": "GND", "S2": "GND"}
    for p in (2, 4, 12, 14, 16, 18, 70, 72, 74):
        m2[str(p)] = "+3V3"
    for p in (3, 5, 11, 17, 23, 29, 35, 39, 45, 51, 57, 71, 73, 75):
        m2[str(p)] = "GND"
    names = {"41": "PERn0", "43": "PERp0", "47": "PETn0", "49": "PETp0", "53": "REFCLKn", "55": "REFCLKp", "50": "PERST#",
             "52": "CLKREQ#", "54": "PEWAKE#"}
    c.add("J1", "M.2-KEY-M-SOCKET", m2_socket(), 950, 1330, 0, m2, names, locked=True, body=(-450, -30, 450, 120))
    c.add("C1", "GRM033R61A104KE15D", chip("0201"), 920, 700, 90, {"1": "PCIE_TX_P", "2": "PCIE_TXC_P"}, value="100nF", desc="Capacitance:100nF Voltage Rating:10V")
    c.add("C2", "GRM033R61A104KE15D", chip("0201"), 890, 700, 90, {"1": "PCIE_TX_N", "2": "PCIE_TXC_N"}, value="100nF", desc="Capacitance:100nF Voltage Rating:10V")
    # U1 root port QFN-48 7x7: PCIe on the top side facing the socket.
    left = [None] * 12
    bottom = ["GND"] + [None] * 10 + ["VDD"]
    right = ["VDD", "GND"] + [None] * 10
    top = ["PERST", "CLKREQ", "WAKE", "GND", "REFCLKP", "REFCLKN", "GND", "RXP", "RXN", "GND", "TXP", "TXN"]  # right → left
    pn = left + bottom + right + top
    net = {"PERST": "PCIE_PERST_N", "CLKREQ": "PCIE_CLKREQ_N", "WAKE": "PCIE_WAKE_N", "REFCLKP": "PCIE_REFCLK_P", "REFCLKN": "PCIE_REFCLK_N",
           "RXP": "PCIE_RX_P", "RXN": "PCIE_RX_N", "TXP": "PCIE_TX_P", "TXN": "PCIE_TX_N", "GND": "GND", "VDD": "+3V3"}
    pins = {str(i + 1): net[n] for i, n in enumerate(pn) if n}
    pins["49"] = "GND"
    nm = {str(i + 1): n for i, n in enumerate(pn) if n}
    c.add("U1", "ASM-PCIE-RC", qfn(12, body=275.6, ep=200), 950, 420, 0, pins, nm)
    c.add("R1", "0402WGF1002TCE", chip("0402"), 1250, 700, 90, {"1": "+3V3", "2": "PCIE_CLKREQ_N"}, value="10k")
    c.add("R2", "0402WGF1002TCE", chip("0402"), 1310, 700, 90, {"1": "+3V3", "2": "PCIE_WAKE_N"}, value="10k")
    cap_decaps(c, [("C5", "+3V3", 700, 420), ("C6", "+3V3", 1200, 420), ("C7", "+3V3", 400, 1250), ("C8", "+3V3", 1500, 1250)])
    c.add("J2", "KF301-5.0-2P", terminal2(), 250, 150, 0, {"1": "5V_IN", "2": "GND"}, {"1": "1", "2": "2"}, locked=True)
    power_3v3(c, "5V_IN", 1600, 300)
    c.spec = {"layers": 4, "standard": {"name": "IPC-2221B"}}
    pairs = [pair_expect(c, f"PCIE_{b}_P", f"PCIE_{b}_N", "PCIE", 85, 5, 2) for b in ("TX", "TXC", "RX", "REFCLK")]
    c.expect = dict(case=c.name, title=c.title, layers=4, stackup=STACK[4], pairs=pairs, acCaps=[["C1", "C2"]],
                    findingsMustNot=["reference-plane-missing", "impedance-uncontrolled"],
                    route=dict(minCompletion=100, maxDRC=0, maxSplitCrossings=0), negative=False,
                    note="PCIe CEM / M.2: 85 Ω ±15 % (100 Ω also allowed; the engine uses 85), intra-pair ≤ 5 mil, ≤ 2 vias per net, "
                         "TX AC caps 75–265 nF at the transmitter, placed as a symmetric pair")
    return c


# ================================================================= (e) DDR
def case_ddr():
    rules = dict(clearanceMil=4, trackWidthMil=4, trackWidthMinMil=3.5, viaDrillMil=8, viaDiameterMil=16, copperToEdgeMil=12)
    c = Case("ddr3-x16", "DDR3 16-bit: controller BGA-196 (1.0 mm) → two x8 DDR3 FBGA-78 (0.8 mm); byte lanes point-to-point with "
                        "differential DQS; address/command/control + CK fly-by U1 → U2 → U3 → VTT terminations", 2600, 1600, 6, rules)
    addr = [f"DDR_A{i}" for i in range(14)] + ["DDR_BA0", "DDR_BA1", "DDR_BA2", "DDR_RAS_N", "DDR_CAS_N", "DDR_WE_N", "DDR_CS_N", "DDR_CKE", "DDR_ODT"]
    lane = [[f"DDR_DQ{i}" for i in range(8)] + ["DDR_DM0"], [f"DDR_DQ{i}" for i in range(8, 16)] + ["DDR_DM1"]]
    dqs = [("DDR_DQS0_P", "DDR_DQS0_N"), ("DDR_DQS1_P", "DDR_DQS1_N")]
    ck = ("DDR_CK_P", "DDR_CK_N")
    # Controller: 14x14 balls 1.0 mm. DDR balls on the three right-most columns.
    ctl = {}
    names = {}
    right_cols = [14, 13, 12]
    sig_order = []
    for i in range(2):
        sig_order += lane[i][:4] + [dqs[i][0], dqs[i][1]] + lane[i][4:]
    sig_order += [ck[0], ck[1]] + addr
    slots = [(r, col) for col in right_cols for r in range(14)]
    for (r, col), n in zip(slots, sig_order):
        ctl[f"{ROWS[r]}{col}"] = n
        names[f"{ROWS[r]}{col}"] = n.replace("DDR_", "")
    for r in range(14):
        for col in range(1, 12):
            b = f"{ROWS[r]}{col}"
            if (r + col) % 3 == 0:
                ctl[b], names[b] = "GND", "VSS"
            elif (r + col) % 7 == 1:
                ctl[b], names[b] = "DDR_VDDQ", "VDDQ"
    c.add("U1", "DDR-CTRL-BGA196", bga(14, 14, 39.37, 18), 700, 800, 0, ctl, names)
    # Two x8 DDR3 FBGA-78 (13 rows x 9 cols, cols 4-6 depopulated).
    for k, (ref, y) in enumerate([("U2", 1150), ("U3", 450)]):
        mem = {}
        mn = {}
        balls = [(r, col) for r in range(13) for col in (1, 2, 3)]
        sig = lane[k][:4] + [dqs[k][0], dqs[k][1]] + lane[k][4:] + [ck[0], ck[1]] + addr
        for (r, col), n in zip(balls, sig):
            b = f"{ROWS[r]}{col}"
            mem[b], mn[b] = n, n.replace("DDR_", "")
        for r in range(13):
            for col in (7, 8, 9):
                b = f"{ROWS[r]}{col}"
                mem[b], mn[b] = ("GND", "VSS") if (r + col) % 2 else ("DDR_VDDQ", "VDDQ")
        c.add(ref, "MT41K256M8DA-125", bga(13, 9, 31.5, 15, skip_cols=(4, 5, 6)), 1500, y, 0, mem, mn)
    # Fly-by end: VTT terminations (39 Ω) beyond U3, CK: 2× 36 Ω to a cap node.
    for i, n in enumerate(addr):
        x = 1900 + (i % 6) * 60
        y = 250 + (i // 6) * 60
        c.add(f"R{20 + i}", "0402WGF390JTCE", chip("0402"), x, y, 90, {"1": n, "2": "DDR_VTT"}, value="39R")
    c.add("R50", "0402WGF360JTCE", chip("0402"), 1900, 560, 0, {"1": ck[0], "2": "DDR_CK_TERM"}, value="36R")
    c.add("R51", "0402WGF360JTCE", chip("0402"), 1900, 620, 0, {"1": ck[1], "2": "DDR_CK_TERM"}, value="36R")
    c.add("C50", "CL05B104KO5NNNC", chip("0402"), 2000, 590, 90, {"1": "DDR_CK_TERM", "2": "DDR_VDDQ"}, value="100nF")
    # VTT regulator (TPS51200 MSOP-10 ≈ SOIC-like) and VDDQ LDO.
    vtt = {"1": "DDR_VDDQ", "2": "DDR_VTT", "3": "GND", "4": "DDR_VTT", "5": "DDR_VREF", "6": "GND", "7": "GND", "8": "+3V3", "9": "GND", "10": "DDR_VDDQ"}
    c.add("U4", "TPS51200DRCR", soic_w(10, pitch=19.7, row=120, padl=30, padw=10), 2350, 400, 0, vtt,
          {"1": "REFIN", "2": "VLDOIN", "3": "PGND", "4": "VO", "5": "REFOUT", "6": "GND", "7": "EN", "8": "VIN", "9": "GND", "10": "VOSNS"})
    c.add("U5", "AMS1117-1.5", sot223(), 2350, 1200, 0, {"1": "GND", "2": "DDR_VDDQ", "3": "+3V3", "4": "DDR_VDDQ"},
          {"1": "GND", "2": "VOUT", "3": "VIN", "4": "TAB"})
    cap_decaps(c, [("C5", "DDR_VDDQ", 1350, 1150), ("C6", "DDR_VDDQ", 1350, 450), ("C7", "DDR_VTT", 2250, 250), ("C8", "DDR_VDDQ", 400, 800)])
    c.add("J2", "KF301-5.0-2P", terminal2(), 250, 1450, 0, {"1": "5V_IN", "2": "GND"}, {"1": "1", "2": "2"}, locked=True)
    power_3v3(c, "5V_IN", 250, 400)
    st = STACK[6]
    c.spec = {"layers": 6, "standard": {"name": "IPC-2221B"}, "rules": {"clearanceMil": 4, "trackMil": 4, "viaDrillMil": 8, "viaDiaMil": 16},
              "hsInterfaces": [
                  {"name": "DDR", "pairs": [list(dqs[0])], "nets": lane[0], "diffOhm": 100, "singleOhm": 50, "lengthGroup": "DDR_BYTE0", "lengthTolMil": 25, "maxVias": 2},
                  {"name": "DDR", "pairs": [list(dqs[1])], "nets": lane[1], "diffOhm": 100, "singleOhm": 50, "lengthGroup": "DDR_BYTE1", "lengthTolMil": 25, "maxVias": 2},
                  {"name": "DDR", "pairs": [list(ck)], "nets": addr, "diffOhm": 100, "singleOhm": 50, "lengthGroup": "DDR_ADDR", "lengthTolMil": 100, "maxVias": 4},
              ]}
    se = solve_se(50, st["h"], st["er"])
    pairs = [pair_expect(c, dqs[0][0], dqs[0][1], "DDR", 100, 5, 2, group="DDR_BYTE0"),
             pair_expect(c, dqs[1][0], dqs[1][1], "DDR", 100, 5, 2, group="DDR_BYTE1"),
             pair_expect(c, ck[0], ck[1], "DDR", 100, 5, 4, group="DDR_ADDR")]
    c.expect = dict(case=c.name, title=c.title, layers=6, stackup=st, pairs=pairs,
                    singleEnded=dict(ohm=50, widthMil=ceil1(se), nets=lane[0] + lane[1] + addr,
                                     why=f"50 Ω microstrip on {st['name']}: Hammerstad-Jensen w={se:.2f} mil"),
                    groups=[dict(name="DDR_BYTE0", units=10, tolMil=25), dict(name="DDR_BYTE1", units=10, tolMil=25),
                            dict(name="DDR_ADDR", units=24, tolMil=100)],
                    viaBudget={"DDR_BYTE0": 2, "DDR_BYTE1": 2, "DDR_ADDR": 4},
                    findingsMustNot=["reference-plane-missing", "impedance-uncontrolled"],
                    route=dict(minCompletion=95, maxDRC=0, maxSplitCrossings=0), negative=False,
                    note="DDR3 layout guide values: DQ/DM/DQS of a byte lane within ±25 mil of DQS, DQS/CK pairs ≤ 5 mil intra-pair, "
                         "address/command/control within ±100 mil of CK along the fly-by; ≤ 2 vias per data net, ≤ 4 per fly-by net")
    return c


def main():
    for c in [case_usb3(), case_hdmi(), case_eth(), case_pcie(), case_ddr(), case_usb3("usb3-2layer-negative", 2, True)]:
        c.write()
        print("wrote", c.name, len(c.parts), "parts")


if __name__ == "__main__":
    main()
