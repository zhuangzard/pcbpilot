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


if __name__ == "__main__":
    unittest.main()
