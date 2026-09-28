#!/usr/bin/env python3
"""Generate the high-voltage stress fixtures (testdata/stress/hv/<case>/).

Each case is one hand-designed circuit with realistic footprints (pad sizes
and positions from the package land patterns, mil, y-up) and a hand
placement a designer would start from. The generator writes, per case:

  connectivity.json  `pcbpilot sch connectivity` shape (components/nets/connections)
  values.json        ref → value / mpn / lcsc / description
  board.json         `pcbpilot pcb dump` shape (components + pads, outline, rules)

spec.json, models.json and expect.json are hand-written next to them (the
engineering answer is derived by hand, not generated).

Run:  python3 testdata/stress/hv/gen.py      (idempotent; commit the output)
"""
import json
import math
import os

HERE = os.path.dirname(os.path.abspath(__file__))
MM = 1 / 0.0254  # mil per mm


# ---------------------------------------------------------------- footprints
# A footprint is (pads, body): pads = [(number, x, y, w, h, round, tht)],
# body = (w, h) courtyard centred on the origin.

def two(pitch, w, h, body=None):
    return ([("1", -pitch / 2, 0, w, h, False, False), ("2", pitch / 2, 0, w, h, False, False)],
            body or (pitch + w + 10, h + 10))


def radial(pitch, d, body_d):
    return ([("1", -pitch / 2, 0, d, d, True, True), ("2", pitch / 2, 0, d, d, True, True)], (max(body_d, pitch + d), body_d))


def term(n, pitch, d, body_w):
    """Screw terminal, pins along y, pin 1 on top."""
    top = (n - 1) / 2 * pitch
    return ([(str(i + 1), 0, top - i * pitch, d, d, True, True) for i in range(n)], (body_w, n * pitch + 40))


def header(n, pitch=100, d=66):
    top = (n - 1) / 2 * pitch
    return ([(str(i + 1), 0, top - i * pitch, d, d, i != 0, True) for i in range(n)], (100, n * pitch + 10))


def dual_row(n, pitch, rowc, pw, ph, body):
    """SOIC/TSSOP: pins 1..n/2 on the bottom row (y = -rowc) left→right,
    n/2+1..n on the top row right→left."""
    k = n // 2
    x0 = -(k - 1) / 2 * pitch
    pads = [(str(i + 1), x0 + i * pitch, -rowc, pw, ph, False, False) for i in range(k)]
    pads += [(str(k + i + 1), -x0 - i * pitch, rowc, pw, ph, False, False) for i in range(k)]
    return pads, body


SOIC8 = dual_row(8, 50, 106, 24, 60, (200, 160))            # 150 mil body, 5.4 mm row centres
SOIC16W = dual_row(16, 50, 183, 24, 79, (420, 300))         # DW 7.5 mm body, rows 9.3 mm, pads 0.6x2.0 mm
SOIC16WW = dual_row(16, 50, 325, 24, 79, (420, 560))        # DWW 14 mm body, 14.5 mm row face gap
TSSOP16 = dual_row(16, 25.6, 114, 16, 57, (205, 180))       # 0.65 mm pitch, 5.8 mm row centres
SOT23 = ([("1", -37, -43, 32, 40, False, False), ("2", 37, -43, 32, 40, False, False), ("3", 0, 43, 32, 40, False, False)], (120, 130))
SOT23_5 = ([("1", -37, -45, 24, 42, False, False), ("2", 0, -45, 24, 42, False, False), ("3", 37, -45, 24, 42, False, False),
            ("4", 37, 45, 24, 42, False, False), ("5", -37, 45, 24, 42, False, False)], (120, 140))
SOT23_6 = ([("1", -37, -45, 24, 42, False, False), ("2", 0, -45, 24, 42, False, False), ("3", 37, -45, 24, 42, False, False),
            ("4", 37, 45, 24, 42, False, False), ("5", 0, 45, 24, 42, False, False), ("6", -37, 45, 24, 42, False, False)], (120, 140))
R0805 = two(75, 40, 50)
R1206 = two(116, 45, 71)       # gap 71 mil = 1.8 mm
R2512 = two(244, 55, 130)      # gap 189 mil = 4.8 mm
SMA = two(160, 60, 80)         # pin 1 = cathode
SMB = two(170, 85, 95)
SOD123 = two(140, 36, 48)
IND5040 = two(150, 60, 170, (220, 220))
# DIP-4 optocoupler (PC817): 300 mil rows, 1.52 mm pads → 6.1 mm row face gap.
DIP4 = ([("1", -150, 50, 60, 60, True, True), ("2", -150, -50, 60, 60, True, True),
         ("3", 150, -50, 60, 60, True, True), ("4", 150, 50, 60, 60, True, True)], (380, 200))
# MB10S (TO-269AA) bridge: AC pins left, DC pins right.
MB10S = ([("1", 100, 50, 60, 40, False, False), ("2", 100, -50, 60, 40, False, False),
          ("3", -100, 50, 60, 40, False, False), ("4", -100, -50, 60, 40, False, False)], (260, 200))
# TO-252 (DPAK): tab = drain (pad 2), gate 1 and source 3 leads 4.57 mm apart.
DPAK = ([("1", -90, -170, 40, 90, False, False), ("2", 0, 90, 250, 230, False, False), ("3", 90, -170, 40, 90, False, False)], (280, 460))
# TO-247-4 (Kelvin source): 1 D, 2 S, 3 KS, 4 G at 2.54 mm pitch, 1.83 mm pads.
TO247_4 = ([(str(i + 1), -150 + i * 100, 0, 72, 72, True, True) for i in range(4)], (640, 220))
# EE25 flyback transformer: two rows of 5 pins, 5.0 mm pitch, rows 20 mm apart.
EE25 = ([(str(i + 1), -394, 394 - i * 197, 90, 90, True, True) for i in range(5)] +
        [(str(6 + i), 394, -394 + i * 197, 90, 90, True, True) for i in range(5)], (1100, 1100))
# EP10/EP13 isolation transformer (reinforced bobbin): 2 x 3 pins, 2.5 mm pitch, rows 15 mm apart.
EP10 = ([(str(i + 1), -295, 98 - i * 98, 62, 62, True, True) for i in range(3)] +
        [(str(4 + i), 295, -98 + i * 98, 62, 62, True, True) for i in range(3)], (700, 420))
EP13 = ([(str(i + 1), -335, 98 - i * 98, 62, 62, True, True) for i in range(3)] +
        [(str(4 + i), 335, -98 + i * 98, 62, 62, True, True) for i in range(3)], (780, 480))
# Tiny EE10 transformer for the negative case: rows 7.5 mm apart.
EE10 = ([(str(i + 1), -148, 98 - i * 98, 55, 55, True, True) for i in range(3)] +
        [(str(4 + i), 148, -98 + i * 98, 55, 55, True, True) for i in range(3)], (400, 380))
# SIP-7 isolated DC/DC (2.54 mm pitch, positions 3/4 unpopulated).
SIP7_ISO = ([("1", -300, 0, 66, 66, True, True), ("2", -200, 0, 66, 66, True, True),
             ("5", 100, 0, 66, 66, True, True), ("6", 200, 0, 66, 66, True, True), ("7", 300, 0, 66, 66, True, True)], (780, 160))
BANANA = ([("1", -375, 0, 200, 200, True, True), ("2", 375, 0, 200, 200, True, True)], (1000, 260))

FOOTPRINTS = {
    "0805": R0805, "1206": R1206, "2512": R2512, "SMA": SMA, "SMB": SMB, "SOD123": SOD123, "IND5040": IND5040,
    "SOT23": SOT23, "SOT23-5": SOT23_5, "SOT23-6": SOT23_6, "SOIC8": SOIC8, "SOIC16W": SOIC16W, "SOIC16WW": SOIC16WW,
    "TSSOP16": TSSOP16, "DIP4": DIP4, "MB10S": MB10S, "DPAK": DPAK, "TO247-4": TO247_4, "EE25": EE25, "EP10": EP10,
    "EP13": EP13, "EE10": EE10, "SIP7-ISO": SIP7_ISO, "BANANA": BANANA,
    "TERM2-7.5": term(2, 295, 100, 330), "TERM2-5.0": term(2, 197, 90, 300), "TERM2-10.16": term(2, 400, 200, 500),
    "RAD5.0": radial(197, 80, 400), "RAD5.08": radial(200, 80, 240), "RAD7.5": radial(295, 80, 420),
    "RAD10": radial(394, 70, 480), "RAD15": radial(591, 75, 720), "RAD3.5": radial(138, 60, 320), "RAD2.5": radial(98, 55, 250),
    "SNAP10": radial(394, 130, 1000),
    "FILM27.5": ([("1", -541, 0, 100, 100, True, True), ("2", 541, 0, 100, 100, True, True)], (1260, 550)),
    "HDR4": header(4), "HDR6": header(6),
}


def rot(x, y, deg):
    a = math.radians(deg)
    return x * math.cos(a) - y * math.sin(a), x * math.sin(a) + y * math.cos(a)


# ------------------------------------------------------------------- writer

def build(case, parts, nets_role, outline_mm, layers, rules, provenance):
    """parts: list of dict(ref, fp, mpn, value?, lcsc?, desc?, pins={num: (name, net)}, at=(x,y) mil, rot=0, locked=False)."""
    d = os.path.join(HERE, case)
    os.makedirs(d, exist_ok=True)
    comps, conns, values, dump_comps = [], [], {}, []
    netset = {}
    for p in parts:
        pads, body = FOOTPRINTS[p["fp"]]
        pinmap = p["pins"]
        numbers = [pd[0] for pd in pads]
        for num in pinmap:
            assert num in numbers, f"{case} {p['ref']}: pin {num} not in footprint {p['fp']}"
        comps.append({"id": "cmp-" + p["ref"], "ref": p["ref"], "device": {"name": p.get("device", p["mpn"])},
                      "pins": [{"number": n, "name": pinmap[n][0] if n in pinmap else n} for n in numbers]})
        for n in numbers:
            if n in pinmap and pinmap[n][1]:
                net = pinmap[n][1]
                netset.setdefault(net, True)
                conns.append({"componentId": "cmp-" + p["ref"], "pinNumber": n, "netId": "net-" + net, "kind": "netlist"})
        v = {"mpn": p["mpn"]}
        for k in ("value", "lcsc", "description"):
            if p.get(k):
                v[k] = p[k]
        values[p["ref"]] = v
        x0, y0 = p["at"]
        r = p.get("rot", 0)
        dpads = []
        bb = [1e9, 1e9, -1e9, -1e9]
        for (num, px, py, w, h, rnd, tht) in pads:
            dx, dy = rot(px, py, r)
            q = int(round(r / 90)) % 2
            aw, ah = (h, w) if q else (w, h)
            pad = {"padNumber": num, "net": pinmap.get(num, ("", ""))[1] if num in pinmap else "", "layer": 12 if tht else 1,
                   "x": round(x0 + dx, 2), "y": round(y0 + dy, 2), "width": aw, "height": ah, "rotation": r % 360,
                   "shape": ["ELLIPSE" if rnd else "RECT", w, h]}
            dpads.append(pad)
            bb = [min(bb[0], pad["x"] - aw / 2), min(bb[1], pad["y"] - ah / 2), max(bb[2], pad["x"] + aw / 2), max(bb[3], pad["y"] + ah / 2)]
        for cx, cy in ((-body[0] / 2, -body[1] / 2), (body[0] / 2, body[1] / 2), (-body[0] / 2, body[1] / 2), (body[0] / 2, -body[1] / 2)):
            dx, dy = rot(cx, cy, r)
            bb = [min(bb[0], x0 + dx), min(bb[1], y0 + dy), max(bb[2], x0 + dx), max(bb[3], y0 + dy)]
        dump_comps.append({"primitiveId": "c-" + p["ref"], "designator": p["ref"], "device": p.get("device", p["mpn"]), "layer": 1,
                           "x": x0, "y": y0, "rotation": r % 360, "locked": bool(p.get("locked")),
                           "bbox": {"minX": round(bb[0], 2), "minY": round(bb[1], 2), "maxX": round(bb[2], 2), "maxY": round(bb[3], 2)},
                           "pads": dpads})
    nets = []
    for n in sorted(netset):
        nets.append({"id": "net-" + n, "name": n, "role": nets_role.get(n, "signal")})
    for n in nets_role:
        assert n in netset, f"{case}: role for unknown net {n}"
    conn = {"schemaVersion": "1.4", "projectId": "stress-hv-" + case, "documentId": "page-1", "_provenance": provenance,
            "components": comps, "nets": nets, "connections": conns}
    W, H = outline_mm[0] * MM, outline_mm[1] * MM
    pts = [[0, 0], [round(W, 2), 0], [round(W, 2), round(H, 2)], [0, round(H, 2)]]
    board = {"_provenance": provenance + " — hand placement (mil, y-up)", "components": dump_comps,
             "outline": {"bbox": {"minX": 0, "minY": 0, "maxX": round(W, 2), "maxY": round(H, 2)}, "points": pts, "source": "polygon"},
             "copperLayers": layers, "rules": rules}
    for x0, y0 in [(c["bbox"]["minX"], c["bbox"]["minY"]) for c in dump_comps] + [(c["bbox"]["maxX"], c["bbox"]["maxY"]) for c in dump_comps]:
        assert 0 <= x0 <= W and 0 <= y0 <= H, f"{case}: part outside the board at {x0},{y0}"
    with open(os.path.join(d, "connectivity.json"), "w") as f:
        json.dump(conn, f, indent=1, ensure_ascii=False)
        f.write("\n")
    with open(os.path.join(d, "values.json"), "w") as f:
        json.dump({"_provenance": "values for connectivity.json (" + case + ")", "parts": values}, f, indent=1, ensure_ascii=False)
        f.write("\n")
    with open(os.path.join(d, "board.json"), "w") as f:
        json.dump(board, f, indent=1, ensure_ascii=False)
        f.write("\n")
    print(f"{case}: {len(parts)} parts, {len(nets)} nets")


RULES_2L = {"clearanceMil": 6, "trackWidthMil": 10, "trackWidthMinMil": 6, "viaDrillMil": 12, "viaDiameterMil": 24, "copperToEdgeMil": 12}
RULES_4L = {"clearanceMil": 6, "trackWidthMil": 6, "trackWidthMinMil": 5, "viaDrillMil": 12, "viaDiameterMil": 24, "copperToEdgeMil": 12}


def P(ref, fp, mpn, at, pins, rot=0, **kw):
    d = {"ref": ref, "fp": fp, "mpn": mpn, "at": at, "rot": rot, "pins": pins}
    d.update(kw)
    return d


def two_pins(a, b, n1="1", n2="2"):
    return {"1": (n1, a), "2": (n2, b)}


# ============================================================ case 1: flyback
def flyback(case, outline=(110, 65)):
    """230 Vac → 12 V / 2 A isolated flyback, UC3843 + 650 V DPAK MOSFET,
    EE25 transformer, TL431 + PC817 feedback, Y1 cap across the barrier."""
    parts = [
        # ---- mains input (hazardous, line-connected)
        P("J1", "TERM2-7.5", "DG142R-7.5-02P", (170, 1280), {"1": ("L", "L"), "2": ("N", "N")}, locked=True,
          description="Screw terminal 2P 7.5 mm pitch, 300 V 15 A"),
        P("F1", "RAD5.08", "37211000411", (450, 1750), two_pins("L", "L_F"), value="1A",
          description="TR5 time-lag fuse 1 A 250 Vac Current Rating:1A"),
        P("RV1", "RAD5.0", "07D471K", (1000, 1300), two_pins("L_F", "N"), rot=270, description="Varistor 470 V 7 mm disc (300 Vac)"),
        P("C1", "RAD15", "MKP-X2-104K275", (750, 650), two_pins("L_F", "N"), value="100nF",
          description="X2 film capacitor Capacitance:100nF Voltage Rating:275Vac"),
        P("BR1", "MB10S", "MB10S", (1330, 1300), {"3": ("~1", "L_F"), "4": ("~2", "N"), "1": ("+", "HV_BULK"), "2": ("-", "PGND")},
          description="Bridge rectifier 1000 V 0.5 A"),
        P("C2", "RAD5.0", "400BXW22MEFR10X16", (1680, 1250), {"1": ("+", "HV_BULK"), "2": ("-", "PGND")}, rot=270, value="22uF",
          description="Aluminium electrolytic Capacitance:22uF Voltage Rating:400V"),
        # ---- primary control (hazardous, referenced to PGND)
        P("R1", "1206", "RC1206FR-07330KL", (1350, 2200), two_pins("HV_BULK", "RST_MID"), value="330kΩ",
          description="Resistance:330kΩ Power(Watts):250mW Overload Voltage (Max):400V"),
        P("R2", "1206", "RC1206FR-07330KL", (1600, 2200), two_pins("RST_MID", "VCC_P"), value="330kΩ",
          description="Resistance:330kΩ Power(Watts):250mW Overload Voltage (Max):400V"),
        P("D1", "SMA", "US1M", (1900, 2350), {"1": ("K", "CLAMP"), "2": ("A", "DRAIN")}, description="Ultrafast 1000 V 1 A"),
        P("R3", "2512", "RC2512FK-07100KL", (1950, 2100), two_pins("HV_BULK", "CLAMP"), rot=180, value="100kΩ",
          description="Resistance:100kΩ Power(Watts):1W"),
        P("C3", "1206", "1206B102K102NT", (1450, 1980), two_pins("HV_BULK", "CLAMP"), value="1nF",
          description="Capacitance:1nF Voltage Rating:1kV"),
        P("Q1", "DPAK", "STD7N65M2", (1950, 1700), {"1": ("G", "GATE_Q"), "2": ("D", "DRAIN"), "3": ("S", "CS")},
          description="N-channel MOSFET 650 V 5 A DPAK"),
        P("R4", "0805", "0805W8F100JT5E", (1450, 1000), two_pins("GATE", "GATE_Q"), value="10Ω"),
        P("R5", "1206", "1206W4F470LT5E", (2000, 1050), two_pins("CS", "PGND"), value="0.47Ω",
          description="Resistance:470mΩ Power(Watts):250mW"),
        P("R6", "0805", "0805W8F1001T5E", (1800, 850), two_pins("CS", "CS_F"), value="1kΩ"),
        P("C4", "0805", "CL21B471KBANNNC", (2000, 700), two_pins("CS_F", "PGND"), value="470pF"),
        P("U1", "SOIC8", "UC3843BD1R2G", (1500, 620), {"1": ("COMP", "COMP"), "2": ("FB", "PGND"), "3": ("CS", "CS_F"), "4": ("RT/CT", "RTCT"),
                                                        "5": ("GND", "PGND"), "6": ("OUT", "GATE"), "7": ("VCC", "VCC_P"), "8": ("VREF", "VREF")},
          description="Current-mode PWM controller"),
        P("R7", "0805", "0805W8F1002T5E", (1200, 380), two_pins("VREF", "RTCT"), value="10kΩ"),
        P("C5", "0805", "CL21B472KBANNNC", (1500, 330), two_pins("RTCT", "PGND"), value="4.7nF"),
        P("C6", "1206", "CL31A226KBHNNNE", (1300, 820), two_pins("VCC_P", "PGND"), value="22uF",
          description="Capacitance:22uF Voltage Rating:50V"),
        P("C7", "0805", "CL21B102KBANNNC", (1300, 980), two_pins("COMP", "PGND"), value="1nF"),
        P("D2", "SOD123", "1N4148W", (1100, 1850), {"1": ("K", "VCC_P"), "2": ("A", "AUX")}, description="Switching diode 100 V"),
        # ---- barrier parts
        P("T1", "EE25", "EE25-FLY-12V2A", (2650, 1280), {"1": ("P+", "HV_BULK"), "2": ("P-", "DRAIN"), "3": ("AUX", "AUX"), "4": ("AUXR", "PGND"),
                                                          "6": ("S+", "SEC"), "8": ("S-", "GND_S")},
          description="Flyback transformer EE25, reinforced (triple-insulated secondary), Lp 800 uH, Np:Ns:Na 60:10:13"),
        P("U2", "DIP4", "PC817C", (2650, 450), {"1": ("A", "OPTO_A"), "2": ("K", "OPTO_K"), "3": ("E", "PGND"), "4": ("C", "COMP")}, rot=180,
          description="Optocoupler CTR 200-400%, 5000 Vrms"),
        P("C8", "RAD10", "CD45-E2GA102M-NKA", (2650, 2250), two_pins("PGND", "GND_S"), value="1nF",
          description="Y1 safety ceramic capacitor Capacitance:1nF Voltage Rating:400Vac"),
        # ---- secondary (SELV)
        P("D3", "SMB", "SS310", (3400, 1900), {"1": ("K", "VOUT_RAW"), "2": ("A", "SEC")}, rot=180, description="Schottky 100 V 3 A"),
        P("C9", "RAD3.5", "25ZLH680MEFC8X20", (3450, 1350), {"1": ("+", "VOUT_RAW"), "2": ("-", "GND_S")}, rot=90, value="680uF",
          description="Capacitance:680uF Voltage Rating:25V"),
        P("C10", "RAD3.5", "25ZLH680MEFC8X20", (3800, 1350), {"1": ("+", "VOUT_RAW"), "2": ("-", "GND_S")}, rot=90, value="680uF",
          description="Capacitance:680uF Voltage Rating:25V"),
        P("L1", "IND5040", "SWPA5040S2R2MT", (3750, 1900), two_pins("VOUT_RAW", "VOUT"), value="2.2uH",
          description="Power inductor 2.2 uH 4.5 A DCR 20 mΩ"),
        P("C11", "RAD2.5", "25ZLH100MEFC5X11", (4050, 1900), {"1": ("+", "VOUT"), "2": ("-", "GND_S")}, rot=90, value="100uF",
          description="Capacitance:100uF Voltage Rating:25V"),
        P("J2", "TERM2-5.0", "DG128-5.0-02P", (4150, 1280), {"1": ("+", "VOUT"), "2": ("-", "GND_S")}, locked=True,
          description="Output terminal 2P 5.0 mm, 12 V 2 A load"),
        P("R8", "0805", "0805W8F1001T5E", (3300, 650), two_pins("VOUT", "OPTO_A"), value="1kΩ"),
        P("U3", "SOT23", "TL431AIDBZR", (3100, 400), {"1": ("REF", "TL_REF"), "2": ("K", "OPTO_K"), "3": ("A", "GND_S")},
          description="Programmable shunt reference 2.495 V"),
        P("R9", "0805", "0805W8F3832T5E", (3550, 700), two_pins("VOUT", "TL_REF"), value="38.3kΩ"),
        P("R10", "0805", "0805W8F1002T5E", (3550, 400), two_pins("TL_REF", "GND_S"), value="10kΩ"),
        P("C12", "0805", "CL21B104KBCNNNC", (3300, 900), two_pins("OPTO_K", "TL_REF"), value="100nF"),
    ]
    roles = {"HV_BULK": "power", "VCC_P": "power", "PGND": "ground", "VOUT": "power", "VOUT_RAW": "power", "GND_S": "ground"}
    build(case, parts, roles, outline, 2, RULES_2L,
          "hand-designed stress fixture: 230 Vac → 12 V / 2 A flyback (UC3843, STD7N65M2, EE25, PC817+TL431, Y1 cap)")


# ============================================================ case 2: medical
def medical(case):
    """Medical patient front-end: SELV host side ↔ patient-applied ADS1220
    through an ISO7741DW digital isolator and an SN6505B-driven EP13
    2×MOPP transformer."""
    parts = [
        # ---- SELV host side
        P("J1", "HDR6", "PZ254V-11-06P", (150, 900), {"1": ("+5V", "+5V"), "2": ("GND", "GND"), "3": ("SCLK", "SCLK"), "4": ("MOSI", "MOSI"),
                                                        "5": ("MISO", "MISO"), "6": ("CS", "CS_N")}, locked=True,
          description="Host header 6P 2.54 mm"),
        P("C1", "0805", "CL21A106KAYNNNE", (500, 1350), two_pins("+5V", "GND"), value="10uF", description="Capacitance:10uF Voltage Rating:25V"),
        P("U2", "SOT23-6", "SN6505BDBVR", (700, 1500), {"1": ("D2", "D2_T"), "2": ("VCC", "+5V"), "3": ("D1", "D1_T"), "4": ("GND", "GND"),
                                                          "5": ("EN", "+5V"), "6": ("CLK", "GND")},
          description="Low-noise 1 A transformer driver for isolated supplies"),
        P("C2", "0805", "CL21B104KBCNNNC", (500, 1600), two_pins("+5V", "GND"), value="100nF"),
        P("C3", "0805", "CL21B104KBCNNNC", (700, 700), two_pins("+5V", "GND"), value="100nF"),
        # ---- barrier
        P("T1", "EP13", "EP13-MED-2MOPP", (1350, 1500), {"1": ("D1", "D1_T"), "2": ("CT", "+5V"), "3": ("D2", "D2_T"),
                                                          "4": ("S1", "S1"), "5": ("SCT", "GND_PAT"), "6": ("S2", "S2")},
          description="Custom isolation transformer EP13, 1:1.3 centre-tapped, 2×MOPP 4 kVrms, 17 mm pin-row spacing"),
        P("U3", "SOIC16W", "ISO7741DWR", (1350, 650), {"1": ("VCC1", "+5V"), "2": ("GND1", "GND"), "3": ("INA", "SCLK"), "4": ("INB", "MOSI"),
                                                        "5": ("INC", "CS_N"), "6": ("OUTD", "MISO"), "7": ("EN1", "+5V"), "8": ("GND1", "GND"),
                                                        "9": ("GND2", "GND_PAT"), "10": ("EN2", "3V3_PAT"), "11": ("IND", "MISO_P"), "12": ("OUTC", "CS_P"),
                                                        "13": ("OUTB", "MOSI_P"), "14": ("OUTA", "SCLK_P"), "15": ("GND2", "GND_PAT"), "16": ("VCC2", "3V3_PAT")},
          rot=270, description="Quad-channel digital isolator 5000 Vrms, reinforced, SOIC-16 DW (8 mm creepage)"),
        # ---- patient-applied side
        P("D1", "SOT23", "BAT54C", (1900, 1500), {"1": ("A1", "S1"), "2": ("A2", "S2"), "3": ("K", "VRECT")}, rot=90,
          description="Dual Schottky common cathode 30 V"),
        P("C4", "0805", "CL21A106KAYNNNE", (2100, 1750), two_pins("VRECT", "GND_PAT"), value="10uF"),
        P("U4", "SOT23-5", "TLV75533PDBVR", (2150, 1350), {"1": ("IN", "VRECT"), "2": ("GND", "GND_PAT"), "3": ("EN", "VRECT"), "5": ("OUT", "3V3_PAT")},
          description="500 mA LDO 3.3 V"),
        P("C5", "0805", "CL21A106KAYNNNE", (2400, 1600), two_pins("3V3_PAT", "GND_PAT"), value="10uF"),
        P("U5", "TSSOP16", "ADS1220IPWR", (2150, 650), {"1": ("SCLK", "SCLK_P"), "2": ("CS", "CS_P"), "3": ("CLK", "GND_PAT"), "4": ("DGND", "GND_PAT"),
                                                         "5": ("AVSS", "GND_PAT"), "6": ("AIN3", ""), "7": ("AIN2", ""), "8": ("REFN0", "GND_PAT"),
                                                         "9": ("REFP0", "3V3_PAT"), "10": ("AIN1", "AIN1"), "11": ("AIN0", "AIN0"), "12": ("AVDD", "3V3_PAT"),
                                                         "13": ("DVDD", "3V3_PAT"), "14": ("DRDY", ""), "15": ("DOUT", "MISO_P"), "16": ("DIN", "MOSI_P")},
          rot=90, description="24-bit delta-sigma ADC, SPI"),
        P("C6", "0805", "CL21B104KBCNNNC", (2450, 900), two_pins("3V3_PAT", "GND_PAT"), value="100nF"),
        P("R1", "1206", "RC1206FR-0747KL", (2500, 450), two_pins("E1", "AIN0"), value="47kΩ", description="Patient lead protection resistor"),
        P("R2", "1206", "RC1206FR-0747KL", (2500, 250), two_pins("E2", "AIN1"), value="47kΩ", description="Patient lead protection resistor"),
        P("R3", "1206", "RC1206FR-0710KL", (2500, 1150), two_pins("E3", "GND_PAT"), value="10kΩ", description="Right-leg reference resistor"),
        P("C7", "0805", "CL21B472KBANNNC", (2200, 250), two_pins("AIN0", "GND_PAT"), value="4.7nF"),
        P("C8", "0805", "CL21B472KBANNNC", (2200, 1000), two_pins("AIN1", "GND_PAT"), value="4.7nF"),
        P("J2", "HDR4", "PATIENT-3P-DIN", (2650, 800), {"1": ("E1", "E1"), "2": ("E2", "E2"), "3": ("E3", "E3"), "4": ("SHIELD", "GND_PAT")},
          locked=True, description="Patient electrode connector (applied part, type BF)"),
    ]
    roles = {"+5V": "power", "GND": "ground", "3V3_PAT": "power", "VRECT": "power", "GND_PAT": "ground"}
    build(case, parts, roles, (72, 50), 4, RULES_4L,
          "hand-designed stress fixture: medical patient front-end (ISO7741DW + SN6505B/EP13 2×MOPP transformer + ADS1220)")


# ============================================================ case 3: CAT III
def catiii(case):
    """IEC 61010-1 CAT III 600 V voltage input: PTC + MOV protection, a
    6 × 1 MΩ 1206 divider chain, floating ADS1220, ISO7741DWW isolator and an
    SN6505B/EP10 reinforced bias transformer."""
    chain = ["VIN_P", "N1", "N2", "N3", "N4", "N5", "ADC_IN"]
    parts = [
        P("J1", "BANANA", "CAT3-600V-JACKS", (550, 1850), {"1": ("INPUT", "VIN_HV"), "2": ("COM", "GND_M")}, locked=True,
          description="4 mm safety jacks INPUT / COM, CAT III 600 V"),
        P("RT1", "RAD7.5", "PTC-600V-1K", (300, 1250), two_pins("VIN_HV", "VIN_P"), rot=270, value="1kΩ",
          description="PTC thermistor 1 kΩ 600 V (input protection)"),
        P("RV1", "RAD7.5", "14D112K", (800, 1250), two_pins("VIN_P", "GND_M"), rot=90, description="Varistor 1100 V 14 mm (680 Vac)"),
    ]
    # Divider chain along y (each 1206 on its own row, 1 MΩ, 200 V working).
    for i in range(6):
        parts.append(P(f"R{i + 1}", "1206", "RC1206FR-071ML", (1300, 1700 - i * 230), two_pins(chain[i], chain[i + 1]), rot=180 * (i % 2), value="1MΩ",
                       description="Resistance:1MΩ Power(Watts):250mW Max working voltage:200V"))
    parts += [
        P("R7", "1206", "RC1206FR-074K99L", (1300, 320), two_pins("ADC_IN", "GND_M"), value="4.99kΩ"),
        P("C1", "0805", "CL21B103KBANNNC", (1600, 180), two_pins("ADC_IN", "GND_M"), value="10nF"),
        P("D1", "SOT23", "BAV99", (1600, 450), {"1": ("A1", "GND_M"), "2": ("K2", "3V3_M"), "3": ("KA", "ADC_IN")}, description="Dual switching diode (clamp)"),
        P("U1", "TSSOP16", "ADS1220IPWR", (1850, 900), {"1": ("SCLK", "SCLK_M"), "2": ("CS", "CS_M"), "3": ("CLK", "GND_M"), "4": ("DGND", "GND_M"),
                                                        "5": ("AVSS", "GND_M"), "6": ("AIN3", ""), "7": ("AIN2", ""), "8": ("REFN0", "GND_M"),
                                                        "9": ("REFP0", "3V3_M"), "10": ("AIN1", "GND_M"), "11": ("AIN0", "ADC_IN"), "12": ("AVDD", "3V3_M"),
                                                        "13": ("DVDD", "3V3_M"), "14": ("DRDY", ""), "15": ("DOUT", "MISO_M"), "16": ("DIN", "MOSI_M")},
          rot=90, description="24-bit delta-sigma ADC (floating on COM)"),
        P("C2", "0805", "CL21B104KBCNNNC", (2100, 1150), two_pins("3V3_M", "GND_M"), value="100nF"),
        P("U2", "SOT23-5", "TLV75533PDBVR", (1850, 1450), {"1": ("IN", "VRECT_M"), "2": ("GND", "GND_M"), "3": ("EN", "VRECT_M"), "5": ("OUT", "3V3_M")},
          description="500 mA LDO 3.3 V"),
        P("C3", "0805", "CL21A106KAYNNNE", (2100, 1650), two_pins("3V3_M", "GND_M"), value="10uF"),
        P("C4", "0805", "CL21A106KAYNNNE", (1600, 1750), two_pins("VRECT_M", "GND_M"), value="10uF"),
        P("D2", "SOT23", "BAT54C", (1850, 1900), {"1": ("A1", "S1_M"), "2": ("A2", "S2_M"), "3": ("K", "VRECT_M")}, rot=270,
          description="Dual Schottky common cathode 30 V"),
        # ---- barrier
        P("T1", "EP10", "EP10-REINF-CAT3", (2600, 1650), {"1": ("S1", "S1_M"), "2": ("SCT", "GND_M"), "3": ("S2", "S2_M"),
                                                           "4": ("D1", "D1_T"), "5": ("CT", "+5V"), "6": ("D2", "D2_T")},
          description="Custom isolation transformer EP10 1:1.1, reinforced for CAT III 600 V (≥ 13 mm pin-row spacing, triple-insulated wire)"),
        P("U3", "SOIC16WW", "ISO7741DWWR", (2600, 750), {"1": ("VCC1", "+5V"), "2": ("GND1", "GND"), "3": ("INA", "SCLK"), "4": ("INB", "MOSI"),
                                                          "5": ("INC", "CS"), "6": ("OUTD", "MISO"), "7": ("EN1", "+5V"), "8": ("GND1", "GND"),
                                                          "9": ("GND2", "GND_M"), "10": ("EN2", "3V3_M"), "11": ("IND", "MISO_M"), "12": ("OUTC", "CS_M"),
                                                          "13": ("OUTB", "MOSI_M"), "14": ("OUTA", "SCLK_M"), "15": ("GND2", "GND_M"), "16": ("VCC2", "3V3_M")},
          rot=90, description="Quad digital isolator, SOIC-16 DWW extra-wide (14.5 mm creepage), reinforced 5.7 kVrms"),
        # ---- SELV host side
        P("U4", "SOT23-6", "SN6505BDBVR", (3200, 1700), {"1": ("D2", "D2_T"), "2": ("VCC", "+5V"), "3": ("D1", "D1_T"), "4": ("GND", "GND"),
                                                          "5": ("EN", "+5V"), "6": ("CLK", "GND")},
          description="Low-noise 1 A transformer driver for isolated supplies"),
        P("C5", "0805", "CL21A106KAYNNNE", (3200, 1350), two_pins("+5V", "GND"), value="10uF"),
        P("C6", "0805", "CL21B104KBCNNNC", (3200, 450), two_pins("+5V", "GND"), value="100nF"),
        P("J2", "HDR6", "PZ254V-11-06P", (3750, 1000), {"1": ("+5V", "+5V"), "2": ("GND", "GND"), "3": ("SCLK", "SCLK"), "4": ("MOSI", "MOSI"),
                                                         "5": ("MISO", "MISO"), "6": ("CS", "CS")}, locked=True, description="Host header 6P 2.54 mm"),
    ]
    roles = {"+5V": "power", "GND": "ground", "3V3_M": "power", "VRECT_M": "power", "GND_M": "ground"}
    build(case, parts, roles, (100, 55), 4, RULES_4L,
          "hand-designed stress fixture: IEC 61010-1 CAT III 600 V input — PTC/MOV, 6 × 1 MΩ 1206 divider, floating ADS1220, ISO7741DWW, SN6505B/EP10 bias")


# ============================================================ case 4: inverter
def inverter(case):
    """400 VDC battery → half-bridge leg with two TO-247-4 SiC MOSFETs (Kelvin
    source), a UCC21520DW dual isolated gate driver and two MGJ2D isolated
    bias supplies; 30 A DC bus and phase copper."""
    parts = [
        P("J1", "TERM2-10.16", "MKDSP10N-2-10.16", (3280, 600), {"1": ("HV+", "HV+"), "2": ("HV-", "HV_GND")}, locked=True,
          description="Power terminal 2P 10.16 mm, 1000 V 41 A"),
        P("C1", "FILM27.5", "DCLINK-MKP-20UF-800V", (2900, 2600), {"1": ("+", "HV+"), "2": ("-", "HV_GND")}, value="20uF",
          description="MKP DC-link film capacitor 27.5 mm Capacitance:20uF Voltage Rating:800V"),
        P("C2", "FILM27.5", "DCLINK-MKP-20UF-800V", (2900, 1500), {"1": ("+", "HV+"), "2": ("-", "HV_GND")}, value="20uF",
          description="MKP DC-link film capacitor 27.5 mm Capacitance:20uF Voltage Rating:800V"),
        P("Q1", "TO247-4", "C3M0060065K", (1950, 2650), {"1": ("D", "HV+"), "2": ("S", "PHASE"), "3": ("KS", "KS_H"), "4": ("G", "G_H")},
          description="SiC MOSFET 650 V 37 A TO-247-4 (Kelvin source)"),
        P("Q2", "TO247-4", "C3M0060065K", (1950, 1500), {"1": ("D", "PHASE"), "2": ("S", "HV_GND"), "3": ("KS", "KS_L"), "4": ("G", "G_L")},
          description="SiC MOSFET 650 V 37 A TO-247-4 (Kelvin source)"),
        P("J2", "TERM2-10.16", "TB-PHASE-2P-10.16", (2100, 600), {"1": ("PH", "PHASE"), "2": ("PH2", "PHASE")}, locked=True,
          description="Phase output terminal 2P 10.16 mm (both poles paralleled), 1000 V 41 A"),
        # ---- gate drive (hazardous, floating on the power stage)
        P("R1", "1206", "RC1206FR-074R7L", (1750, 2300), two_pins("OUTA", "G_H"), value="4.7Ω", description="Gate resistor"),
        P("R2", "1206", "RC1206FR-074R7L", (1750, 1200), two_pins("OUTB", "G_L"), value="4.7Ω", description="Gate resistor"),
        P("C3", "1206", "CL31B105KBHNNNE", (1500, 2200), two_pins("VDDA", "KS_H"), value="1uF", description="Capacitance:1uF Voltage Rating:50V"),
        P("C4", "1206", "CL31B105KBHNNNE", (1500, 1250), two_pins("VDDB", "KS_L"), value="1uF", description="Capacitance:1uF Voltage Rating:50V"),
        P("U2", "SIP7-ISO", "MGJ2D051505SC", (1150, 2600), {"1": ("+VIN", "+5V"), "2": ("-VIN", "GND"), "5": ("+VO", "VDDA"), "6": ("0V", "KS_H")},
          description="Isolated DC/DC 2 W 5 V → +15/−5 V, 5.2 kVDC, for SiC gate drivers"),
        P("U3", "SIP7-ISO", "MGJ2D051505SC", (1150, 1550), {"1": ("+VIN", "+5V"), "2": ("-VIN", "GND"), "5": ("+VO", "VDDB"), "6": ("0V", "KS_L")},
          description="Isolated DC/DC 2 W 5 V → +15/−5 V, 5.2 kVDC, for SiC gate drivers"),
        P("U1", "SOIC16W", "UCC21520DWR", (1150, 900), {"1": ("INA", "PWM_H"), "2": ("INB", "PWM_L"), "3": ("VCCI", "+5V"), "4": ("GND", "GND"),
                                                          "5": ("DIS", "DIS"), "6": ("DT", "DT"), "8": ("VCCI", "+5V"),
                                                          "9": ("VSSB", "KS_L"), "10": ("OUTB", "OUTB"), "11": ("VDDB", "VDDB"),
                                                          "14": ("VSSA", "KS_H"), "15": ("OUTA", "OUTA"), "16": ("VDDA", "VDDA")},
          rot=270, description="Dual-channel isolated gate driver 5.7 kVrms reinforced, SOIC-16 DW"),
        # ---- LV control (SELV)
        P("J3", "HDR4", "PZ254V-11-04P", (150, 600), {"1": ("+5V", "+5V"), "2": ("GND", "GND"), "3": ("PWM_H", "PWM_H"), "4": ("PWM_L", "PWM_L")},
          locked=True, description="Control header 4P 2.54 mm"),
        P("C5", "0805", "CL21A106KAYNNNE", (450, 450), two_pins("+5V", "GND"), value="10uF"),
        P("C6", "0805", "CL21B104KBCNNNC", (450, 750), two_pins("+5V", "GND"), value="100nF"),
        P("R3", "0805", "0805W8F1002T5E", (450, 1050), two_pins("DIS", "GND"), value="10kΩ"),
        P("R4", "0805", "0805W8F2002T5E", (450, 1250), two_pins("DT", "GND"), value="20kΩ"),
        P("R5", "0805", "0805W8F5101T5E", (700, 400), two_pins("PWM_H", "GND"), value="5.1kΩ"),
        P("R6", "0805", "0805W8F5101T5E", (700, 600), two_pins("PWM_L", "GND"), value="5.1kΩ"),
    ]
    roles = {"HV+": "power", "HV_GND": "ground", "+5V": "power", "GND": "ground", "VDDA": "power", "VDDB": "power"}
    build(case, parts, roles, (90, 80), 4, RULES_4L,
          "hand-designed stress fixture: 400 VDC half-bridge leg — 2 × C3M0060065K (TO-247-4 Kelvin), UCC21520DW, 2 × MGJ2D051505SC, 30 A bus")


# ============================================================ case 5: negative
def negative(case):
    """230 Vac → isolated 5 V on a 40 × 25 mm board with the classic mistake:
    a 1206 'Y capacitor' across the reinforced barrier (1.8 mm pad gap < 3 mm
    clearance) — no slot can lengthen an air path."""
    parts = [
        P("J1", "TERM2-5.0", "DG128-5.0-02P-AC", (150, 500), {"1": ("L", "L"), "2": ("N", "N")}, locked=True, description="Screw terminal 2P 5.0 mm"),
        P("F1", "1206", "0466001.NR", (400, 800), two_pins("L", "L_F"), value="1A", description="SMD fuse 1206 1 A Current Rating:1A"),
        P("BR1", "MB10S", "MB10S", (550, 450), {"3": ("~1", "L_F"), "4": ("~2", "N"), "1": ("+", "HV_BULK"), "2": ("-", "PGND")}),
        P("C1", "RAD2.5", "400BXW4R7MEFC8X11", (800, 700), {"1": ("+", "HV_BULK"), "2": ("-", "PGND")}, rot=90, value="4.7uF",
          description="Capacitance:4.7uF Voltage Rating:400V"),
        P("T1", "EE10", "EE10-5V", (1050, 300), {"1": ("P+", "HV_BULK"), "2": ("P-", "PGND"), "4": ("S+", "VOUT"), "6": ("S-", "GND_S")},
          description="EE10 transformer (functional model)"),
        P("C2", "1206", "1206Y-2KV-102", (1050, 800), two_pins("PGND", "GND_S"), value="1nF",
          description="1206 MLCC 1 nF 2 kV used as 'Y capacitor' (not Y-rated)"),
        P("C3", "0805", "CL21A106KAYNNNE", (1350, 600), two_pins("VOUT", "GND_S"), value="10uF"),
        P("J2", "TERM2-5.0", "DG128-5.0-02P", (1420, 300), {"1": ("+", "VOUT"), "2": ("-", "GND_S")}, locked=True,
          description="Output terminal 2P 5.0 mm, 5 V 0.2 A load"),
    ]
    roles = {"HV_BULK": "power", "PGND": "ground", "VOUT": "power", "GND_S": "ground"}
    build(case, parts, roles, (40, 25), 2, RULES_2L,
          "hand-designed negative stress fixture: 230 Vac isolated 5 V with a 1206 MLCC across the reinforced barrier on a 40 × 25 mm board")


if __name__ == "__main__":
    flyback("flyback")
    medical("medical")
    catiii("cat3")
    inverter("inverter")
    negative("negative")
