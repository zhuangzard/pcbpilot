#!/usr/bin/env python3
"""Regenerates sch-frontend.json / values.json (the analog front-end fixture).

The circuit is written here as a pin → net table so it stays reviewable; the
output uses the `pcbpilot sch connectivity` 1.4 IR (components / nets /
connections) that `sim analog --connectivity` reads.
"""
import hashlib, json

parts = [
    # ref, value, mpn, lcsc, description, [(pin number, pin name, net)]
    ("J1", "", "KF301-5.0-2P", "C474881", "sensor input 0–24 V", [("1", "1", "SENS_IN"), ("2", "2", "GND")]),
    ("J2", "", "KF301-5.0-2P", "C474881", "5 V supply input", [("1", "1", "+5V"), ("2", "2", "GND")]),
    ("J3", "", "HDR-1x4", "", "ADC SPI header", [("1", "1", "ADC_CS"), ("2", "2", "ADC_DOUT"), ("3", "3", "ADC_CLK"), ("4", "4", "GND")]),
    ("R1", "100kΩ", "0402WGF1003TCE", "C25741", "Resistance:100kΩ Tolerance:±1%", [("1", "1", "SENS_IN"), ("2", "2", "DIV")]),
    ("R2", "20kΩ", "0402WGF2002TCE", "C25765", "Resistance:20kΩ Tolerance:±1%", [("1", "1", "DIV"), ("2", "2", "GND")]),
    ("R3", "1kΩ", "0402WGF1001TCE", "C11702", "Resistance:1kΩ Tolerance:±1%", [("1", "1", "DIV"), ("2", "2", "AAF")]),
    ("C1", "100nF", "CL05B104KO5NNNC", "C1525", "Capacitance:100nF Tolerance:±10% X7R", [("1", "1", "AAF"), ("2", "2", "GND")]),
    ("U1", "MCP6002", "MCP6002T-I/SN", "C7377", "dual RRIO op-amp 1 MHz", [
        ("1", "OUTA", "BUF_OUT"), ("2", "INA-", "BUF_OUT"), ("3", "INA+", "AAF"), ("4", "VSS", "GND"),
        ("5", "INB+", "SK_P"), ("6", "INB-", "FILT_OUT"), ("7", "OUTB", "FILT_OUT"), ("8", "VDD", "+5V")]),
    ("R4", "10kΩ", "0402WGF1002TCE", "C25744", "Resistance:10kΩ Tolerance:±1%", [("1", "1", "BUF_OUT"), ("2", "2", "SK_X")]),
    ("R5", "10kΩ", "0402WGF1002TCE", "C25744", "Resistance:10kΩ Tolerance:±1%", [("1", "1", "SK_X"), ("2", "2", "SK_P")]),
    ("C2", "10nF", "CL05B103KB5NNNC", "C15195", "Capacitance:10nF Tolerance:±10% X7R", [("1", "1", "SK_X"), ("2", "2", "FILT_OUT")]),
    ("C3", "10nF", "CL05B103KB5NNNC", "C15195", "Capacitance:10nF Tolerance:±10% X7R", [("1", "1", "SK_P"), ("2", "2", "GND")]),
    ("R6", "100Ω", "0402WGF1000TCE", "C25076", "Resistance:100Ω Tolerance:±1%", [("1", "1", "FILT_OUT"), ("2", "2", "ADC_IN")]),
    ("C4", "1nF", "CL05B102KB5NNNC", "C1523", "Capacitance:1nF Tolerance:±10% X7R", [("1", "1", "ADC_IN"), ("2", "2", "GND")]),
    ("U2", "MCP3201", "MCP3201-CI/SN", "C79488", "12-bit SAR ADC SPI", [
        ("1", "VREF", "VREF"), ("2", "IN+", "ADC_IN"), ("3", "IN-", "GND"), ("4", "VSS", "GND"),
        ("5", "CS/SHDN", "ADC_CS"), ("6", "DOUT", "ADC_DOUT"), ("7", "CLK", "ADC_CLK"), ("8", "VDD", "+5V")]),
    ("U3", "LM4040DIM3-4.1", "LM4040DIM3-4.1/NOPB", "C33193", "4.096 V shunt reference", [("1", "K", "VREF"), ("2", "A", "GND"), ("3", "NC", "")]),
    ("R7", "1kΩ", "0402WGF1001TCE", "C11702", "Resistance:1kΩ Tolerance:±1%", [("1", "1", "+5V"), ("2", "2", "VREF")]),
    ("C5", "1uF", "CL05A105KA5NQNC", "C52923", "Capacitance:1uF Tolerance:±10% X5R", [("1", "1", "VREF"), ("2", "2", "GND")]),
    ("C6", "100nF", "CL05B104KO5NNNC", "C1525", "Capacitance:100nF Tolerance:±10% X7R", [("1", "1", "+5V"), ("2", "2", "GND")]),
    ("C7", "100nF", "CL05B104KO5NNNC", "C1525", "Capacitance:100nF Tolerance:±10% X7R", [("1", "1", "+5V"), ("2", "2", "GND")]),
]

roles = {"GND": "ground", "+5V": "power", "VREF": "power"}
nets = sorted({n for p in parts for (_, _, n) in p[5] if n})
nid = {n: "net-" + hashlib.sha256(n.encode()).hexdigest()[:16] for n in nets}
doc = {
    "schemaVersion": "1.4",
    "projectId": "analog-frontend-fixture",
    "documentId": "sch-frontend",
    "_provenance": "synthetic ADC front-end fixture for pcbpilot sim analog (testdata/analog/frontend/gen.py): 0–24 V sensor → 100k/20k divider → 1k/100nF anti-alias RC → MCP6002 follower → unity-gain Sallen-Key low-pass (deliberately Q = 0.5) → 100 Ω/1 nF kickback filter → MCP3201 12-bit ADC with an LM4040 4.096 V reference",
    "components": [{"id": "cmp-" + p[0], "ref": p[0], "device": {"name": "={Value}", "supplierId": p[3]},
                    "pins": [{"number": n, "name": nm} for (n, nm, _) in p[5]]} for p in parts],
    "nets": [{"id": nid[n], "name": n, "scope": "global" if n in roles else "local", "role": roles.get(n, "signal")} for n in nets],
    "connections": [{"componentId": "cmp-" + p[0], "pinNumber": num, "netId": nid[n], "kind": "netlist"} for p in parts for (num, _, n) in p[5] if n],
}
vals = {"_provenance": "values of the analog front-end fixture (gen.py)", "parts": {}}
for ref, value, mpn, lcsc, desc, _ in parts:
    e = {"mpn": mpn}
    if value:
        e["value"] = value
    if lcsc:
        e["lcsc"] = lcsc
    if desc:
        e["description"] = desc
    vals["parts"][ref] = e
spec = {"_doc": "analog targets of the front-end fixture: Butterworth 1 kHz anti-alias filter; 0–24 V input range on the buffer",
        "blocks": [{"core": "U1:B", "targets": {"fcHz": 1000, "q": 0.707}, "tolPct": {"fcHz": 5, "q": 5}},
                   {"core": "U1:A", "inputRange": [0, 24]}],
        "adc": {"sampleRateHz": 100000}}
json.dump(doc, open("sch-frontend.json", "w"), indent=1, ensure_ascii=False)
json.dump(vals, open("values.json", "w"), indent=1, ensure_ascii=False)
json.dump(spec, open("spec.json", "w"), indent=1, ensure_ascii=False)
