import importlib.util
import pathlib
import unittest

_SPEC = importlib.util.spec_from_file_location(
    "pad_net_diff", pathlib.Path(__file__).resolve().parent.parent / "pad-net-diff.py")
pnd = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(pnd)

SCH = {"result": {
    "components": [
        {"id": "c1", "ref": "U1", "pins": [{"number": "1"}, {"number": "2"}, {"number": "3", "noConnected": True}]},
        {"id": "c2", "ref": "R1", "pins": [{"number": "1"}, {"number": "2"}]},
    ],
    "nets": [{"id": "n1", "name": "VCC"}, {"id": "n2", "name": "SDA"}],
    "connections": [
        {"componentId": "c1", "pinNumber": "1", "netId": "n1"},
        {"componentId": "c1", "pinNumber": "2", "netId": "n2"},
        {"componentId": "c2", "pinNumber": "1", "netId": "n1"},
        {"componentId": "c2", "pinNumber": "2", "netId": "n2"},
    ],
}}


def board(r1_pad2="SDA", extra=False):
    comps = [
        {"designator": "U1", "pads": [{"padNumber": "1", "net": "VCC"}, {"padNumber": "2", "net": "SDA"}, {"padNumber": "3", "net": ""}]},
        {"designator": "R1", "pads": [{"padNumber": "1", "net": "VCC"}, {"padNumber": "2", "net": r1_pad2}]},
    ]
    if extra:
        comps.append({"designator": "H1", "pads": [{"padNumber": "1", "net": ""}]})
    return {"components": comps}


class PadNetDiffTest(unittest.TestCase):
    def test_match_with_nc(self):
        rep = pnd.diff(pnd.schematic_map(SCH), pnd.pcb_map(board()))
        self.assertTrue(rep["ok"], rep)

    def test_net_mismatch(self):
        rep = pnd.diff(pnd.schematic_map(SCH), pnd.pcb_map(board(r1_pad2="SCL")))
        self.assertFalse(rep["ok"])
        self.assertEqual(rep["netMismatch"][0]["pad"], "R1.2")

    def test_case_only_is_warning(self):
        rep = pnd.diff(pnd.schematic_map(SCH), pnd.pcb_map(board(r1_pad2="sda")))
        self.assertTrue(rep["ok"])
        self.assertEqual(rep["netCaseOnly"][0]["pad"], "R1.2")

    def test_extra_part_and_ignore(self):
        rep = pnd.diff(pnd.schematic_map(SCH), pnd.pcb_map(board(extra=True)))
        self.assertEqual(rep["extraOnPcb"], ["H1"])
        rep = pnd.diff(pnd.schematic_map(SCH), pnd.pcb_map(board(extra=True)), ignore={"H1"})
        self.assertTrue(rep["ok"])


# kicad netlist of a hierarchical design vs kicad snapshot of its board.
HIER_SCH = {
    "components": [
        {"id": "c1", "ref": "U1", "pins": [{"number": "1"}, {"number": "2"}, {"number": "4", "connectionState": "unconnected"}]},
        {"id": "c2", "ref": "R1", "pins": [{"number": "1"}, {"number": "2"}]},
        {"id": "c3", "ref": "H1", "pins": None},  # older kicad netlist: pinless mounting hole
    ],
    "nets": [
        {"id": "n1", "name": "VCC_PIC", "fullName": "/pic_sockets/VCC_PIC"},
        {"id": "n2", "name": "VPP/MCLR", "fullName": "/VPP{slash}MCLR"},
    ],
    "connections": [
        {"componentId": "c1", "pinNumber": "1", "netId": "n2"},
        {"componentId": "c1", "pinNumber": "2", "netId": "n1"},
        {"componentId": "c2", "pinNumber": "1", "netId": "n2"},
        {"componentId": "c2", "pinNumber": "2", "netId": "n1"},
    ],
}
HIER_PCB = {"components": [
    {"designator": "U1", "pads": [{"padNumber": "1", "net": "/VPP{slash}MCLR"}, {"padNumber": "2", "net": "/pic_sockets/VCC_PIC"},
                                  {"padNumber": "4", "net": "unconnected-(U1-S{slash}S-Pad4)"}]},
    {"designator": "R1", "pads": [{"padNumber": "1", "net": "/VPP{slash}MCLR"}, {"padNumber": "2", "net": "/pic_sockets/VCC_PIC"}]},
    {"designator": "H1", "pads": [{"padNumber": "", "net": ""}]},
]}


class HierarchicalNetIdentityTest(unittest.TestCase):
    def test_full_names_match_snapshot(self):
        rep = pnd.diff(pnd.schematic_map(HIER_SCH), pnd.pcb_map(HIER_PCB))
        self.assertTrue(rep["ok"], rep)
        self.assertEqual(rep["netMismatch"], [])

    def test_display_names_resolve_without_full_name(self):
        old = {**HIER_SCH, "nets": [{"id": n["id"], "name": n["name"]} for n in HIER_SCH["nets"]]}
        rep = pnd.diff(pnd.schematic_map(old), pnd.pcb_map(HIER_PCB))
        self.assertTrue(rep["ok"], rep)

    def test_never_cut_at_label_slash(self):
        cut = {**HIER_SCH, "nets": [{"id": "n1", "name": "VCC_PIC"}, {"id": "n2", "name": "MCLR"}]}
        rep = pnd.diff(pnd.schematic_map(cut), pnd.pcb_map(HIER_PCB))
        self.assertEqual(sorted(m["pad"] for m in rep["netMismatch"]), ["R1.1", "U1.1"])

    def test_ambiguous_display_name_is_not_guessed(self):
        pcb = {"components": [{"designator": "R1", "pads": [{"padNumber": "1", "net": "/a/SDA"}, {"padNumber": "2", "net": "/b/SDA"}]}]}
        sch = {"components": [{"id": "c", "ref": "R1", "pins": [{"number": "1"}, {"number": "2"}]}],
               "nets": [{"id": "n", "name": "SDA"}],
               "connections": [{"componentId": "c", "pinNumber": "1", "netId": "n"}, {"componentId": "c", "pinNumber": "2", "netId": "n"}]}
        rep = pnd.diff(pnd.schematic_map(sch), pnd.pcb_map(pcb))
        self.assertEqual(len(rep["netMismatch"]), 2)


if __name__ == "__main__":
    unittest.main()
