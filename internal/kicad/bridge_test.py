# bridge_test.py — unit tests of bridge.py's validation and indexing with a
# fake pcbnew (plain python, no KiCad). Run by TestBridgePyUnit (go test) or
# directly: python -B bridge_test.py
import os
import sys
import types
import unittest

# ── fake pcbnew: only what the pure helpers touch ──────────────────────────
fake = types.ModuleType("pcbnew")
fake.F_Cu, fake.B_Cu = 0, 2
for _k in range(1, 31):
    setattr(fake, "In%d_Cu" % _k, 2 + 2 * _k)
fake.F_SilkS, fake.B_SilkS, fake.F_Fab = 100, 101, 102
fake.PCB_TRACE_T, fake.PCB_VIA_T = 1, 2
sys.modules["pcbnew"] = fake
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bridge  # noqa: E402


class UUID:
    def __init__(self, u):
        self.u = u

    def AsString(self):
        return self.u


class Text:
    def __init__(self, uuid, layer=fake.F_SilkS, cls="PCB_TEXT"):
        self.m_Uuid, self.layer, self.cls = UUID(uuid), layer, cls

    def GetClass(self):
        return self.cls

    def GetLayer(self):
        return self.layer


class Pad:
    def __init__(self, num):
        self.num = num

    def GetNumber(self):
        return self.num


class FP:
    def __init__(self, uuid, ref, texts=(), pads=("1", "2")):
        self.m_Uuid, self.ref = UUID(uuid), ref
        self.ref_field, self.value_field = Text(uuid + "-ref"), Text(uuid + "-val", fake.F_Fab)
        self.texts, self.pads = list(texts), [Pad(p) for p in pads]

    def GetReference(self):
        return self.ref

    def Reference(self):
        return self.ref_field

    def Value(self):
        return self.value_field

    def GraphicalItems(self):
        return self.texts

    def Pads(self):
        return self.pads


class Track:
    def __init__(self, uuid, kind=fake.PCB_TRACE_T):
        self.m_Uuid, self.kind = UUID(uuid), kind

    def Type(self):
        return self.kind


class Layers:
    def __init__(self, ls):
        self.ls = ls

    def CuStack(self):
        return self.ls


class Board:
    def __init__(self, fps=(), tracks=(), drawings=(), nets=("GND", "VCC"), layers=(fake.F_Cu, fake.B_Cu)):
        self.fps, self.tracks, self.drawings = list(fps), list(tracks), list(drawings)
        self.nets, self.layers = {n: object() for n in ("",) + tuple(nets)}, list(layers)

    def GetFootprints(self):
        return self.fps

    def GetTracks(self):
        return self.tracks

    def GetDrawings(self):
        return self.drawings

    def GetNetsByName(self):
        return self.nets

    def GetEnabledLayers(self):
        return Layers(self.layers)

    def GetCopperLayerCount(self):
        return len(self.layers)


def board():
    """U1, two H1 (own uuids), two R5 sharing one uuid (designator and uuid
    repeat), a board text, a footprint graphic text, one track and one via."""
    return Board(
        fps=[FP("u-1", "U1", texts=[Text("fptext-1"), Text("fab-text", fake.F_Fab)]),
             FP("h-1", "H1"), FP("h-2", "H1"), FP("r-5", "R5"), FP("r-5", "R5"), FP("x-1", "")],
        tracks=[Track("t-1"), Track("v-1", fake.PCB_VIA_T)],
        drawings=[Text("board-text"), Text("board-fab", fake.F_Fab)])


class FootprintIndex(unittest.TestCase):
    def test_keys_are_unique_and_follow_the_footprint(self):
        b = board()
        keys = [e["key"] for e in bridge.footprint_index(b)]
        self.assertEqual(keys, ["u-1", "h-1", "h-2", "fp:r-5:1", "fp:r-5:2", "x-1"])
        # uuid repeated, designator unique → ref:<designator>
        b2 = Board(fps=[FP("d", "C1"), FP("d", "C2")])
        self.assertEqual([e["key"] for e in bridge.footprint_index(b2)], ["ref:C1", "ref:C2"])

    def test_resolve(self):
        es = bridge.footprint_index(board())
        self.assertEqual(bridge.resolve_footprint(es, "U1")[0]["key"], "u-1")
        self.assertEqual(bridge.resolve_footprint(es, "uuid:h-2")[0]["key"], "h-2")
        self.assertEqual(bridge.resolve_footprint(es, "fp:r-5:2")[0]["fp"], es[4]["fp"])
        self.assertIn("ambiguous: 2 footprints (h-1, h-2)", bridge.resolve_footprint(es, "H1")[1])
        self.assertIn("ambiguous", bridge.resolve_footprint(es, "uuid:r-5")[1])
        self.assertEqual(bridge.resolve_footprint(es, "Q9"), (None, "missing"))
        dup = bridge.duplicate_refs(es)
        self.assertEqual(sorted(dup), ["H1", "R5"])

    def test_placeholders(self):
        for r in ("", "REF**", "G***", "R?", "LOGO**"):
            self.assertTrue(bridge.is_placeholder(r), r)
        for r in ("R1", "H1", "U@kiid:1", "*"):
            self.assertEqual(bridge.is_placeholder(r), r == "*", r)


class TextIndex(unittest.TestCase):
    def test_every_reported_text_has_an_id(self):
        b = board()
        ids = [it[0] for it in bridge.text_index(b, bridge.footprint_index(b))]
        self.assertIn("u-1#ref", ids)
        self.assertIn("fp:r-5:2#value", ids)
        self.assertIn("fptext-1", ids)       # footprint graphic text on silk
        self.assertIn("board-text", ids)     # board text on silk
        self.assertNotIn("fab-text", ids)    # not silk: not reported, not editable
        self.assertNotIn("board-fab", ids)
        self.assertEqual(len(ids), len(set(ids)))

    def test_repeated_graphic_text_uuid(self):
        t = "logo-text"
        b = Board(fps=[FP("a", "H1", texts=[Text(t)]), FP("b", "H1", texts=[Text(t)])], drawings=[Text(t)])
        ids = [it[0] for it in bridge.text_index(b, bridge.footprint_index(b))]
        self.assertEqual([i for i in ids if "#text:" in i], ["a#text:logo-text:1", "b#text:logo-text:1", "board#text:logo-text:1"])


class Scalars(unittest.TestCase):
    def test_is_int_is_num(self):
        self.assertTrue(bridge.is_int(1))
        for v in (True, 1.0, 1.9, "1", None):
            self.assertFalse(bridge.is_int(v), v)
        self.assertTrue(bridge.is_num(1.5))
        for v in (True, "1", float("nan"), float("inf"), None, [1]):
            self.assertFalse(bridge.is_num(v), v)

    def test_distinct_keeps_types_apart_and_takes_unhashables(self):
        self.assertEqual(bridge.distinct([1, True, 1.0, 1, [1], [1], {"a": 1}]), [1, True, 1.0, [1], {"a": 1}])


class OpLists(unittest.TestCase):
    def test_containers(self):
        L, bad = bridge.op_lists("edit", {"setWidth": {}, "hideTexts": "", "addVias": None, "addTracks": [], "bogus": []}, bridge.EDIT_OPS)
        self.assertEqual(L["setWidth"], [])
        self.assertTrue(any("setWidth: a dict, not a list" in p for p in bad), bad)
        self.assertTrue(any("hideTexts: a str, not a list" in p for p in bad), bad)
        self.assertTrue(any("unknown field 'bogus'" in p for p in bad), bad)
        self.assertFalse(any("addVias" in p or "addTracks" in p for p in bad), bad)
        for v in (7, True, "x", [1]):
            self.assertTrue(bridge.op_lists("edit", v, bridge.EDIT_OPS)[1])


def problems(ops, b=None):
    return bridge.validate_edit(b or board(), ops)[0]


def has(bad, sub):
    return any(sub in p for p in bad)


class ValidateEdit(unittest.TestCase):
    track = {"net": "GND", "layer": 1, "startX": 0, "startY": 0, "endX": 10, "endY": 0, "lineWidth": 8}

    def test_valid_batch(self):
        ops = {"addTracks": [self.track], "addVias": [{"net": "VCC", "x": 1, "y": 2}],
               "setWidth": [{"id": "t-1", "width": 9}],
               "setText": [{"id": "board-text", "x": 1, "y": 1}, {"id": "fptext-1", "x": 2, "y": 2}, {"id": "u-1#ref", "x": 3, "y": 3}],
               "hideTexts": ["fp:r-5:1#ref"], "addTexts": [{"text": "A", "x": 0, "y": 0}, {"text": "B", "x": 0, "y": 0, "layer": 4}],
               "padZoneNone": [{"id": "fp:r-5:2", "pad": "1"}, {"ref": "U1", "pad": 2}]}
        bad, plan = bridge.validate_edit(board(), ops)
        self.assertEqual(bad, [])
        self.assertEqual(len(plan["zonePads"]), 2)

    def test_mixed_batch_lists_every_problem(self):
        t = dict(self.track)
        ops = {"addTracks": [self.track, dict(t, net="NOPE"), dict(t, net=["GND"]), dict(t, layer=1.9), dict(t, layer=True), dict(t, layer=15)],
               "addVias": [{"net": {"a": 1}, "x": 0, "y": 0}, {"net": "gnd", "x": 0}],
               "setWidth": [{"id": "v-1", "width": 9}, {"id": ["t-1"], "width": 9}, {"id": "t-9", "width": 9}, {"id": "t-1", "width": 0}],
               "setText": [{"id": "fab-text", "x": 0, "y": 0}, {"id": {"x": 1}, "x": 0, "y": 0}],
               "hideTexts": ["", 5, "nope"],
               "addTexts": [{"text": "A", "x": 0, "y": 0, "layer": 1}, {"text": "", "x": 0, "y": 0}],
               "padZoneNone": [{"ref": "H1", "pad": "1"}, {"ref": "U1", "pad": "9"}, {"id": 3, "pad": "1"}, {"ref": "U1", "pad": None}, "x"]}
        bad = problems(ops)
        for sub in ("net NOPE: not on the board", "net ['GND']: not a net name string", "net {'a': 1}", "net gnd: not on the board (did you mean GND?)",
                    "layer 1.9: not an integer", "layer True: not an integer", "layer 15: not enabled",
                    "addVias[1].y", "setWidth[0]: v-1 is a via", "setWidth[1]: id ['t-1']", "no such track t-9", "setWidth[3].width",
                    "setText[0]: no such text fab-text", "setText[1]: text id {'x': 1}", "hideTexts[0]", "hideTexts[1]", "no such text nope",
                    "text layer 1", "addTexts[1].text", "padZoneNone H1.1: footprint ambiguous", "padZoneNone U1.9: no such pad",
                    "padZoneNone[2]: id/ref 3", "pad None", "padZoneNone[4]: not an object"):
            self.assertTrue(has(bad, sub), "%r not in %s" % (sub, bad))

    def test_containers_and_types(self):
        self.assertTrue(has(problems({"setWidth": {}}), "setWidth: a dict"))
        self.assertTrue(has(problems({"hideTexts": ""}), "hideTexts: a str"))
        self.assertTrue(has(problems({"hideTexts": ["fptext-1"]}), "graphic text; KiCad keeps no hidden state"))
        self.assertTrue(has(problems({"hideTexts": ["board-text"]}), "graphic text"))
        self.assertTrue(has(problems([]), "not an object"))
        self.assertTrue(has(problems({"addTracks": [5]}), "addTracks[0]: not an object"))
        self.assertEqual(problems({"addVias": None, "addTracks": None}), [])


class DuplicateCopperIds(unittest.TestCase):
    def test_shared_track_uuid_is_refused(self):
        b = board()
        b.tracks += [Track("t-dup"), Track("t-dup"), Track("t-dup", fake.PCB_VIA_T)]
        bad, plan = bridge.validate_edit(b, {"setWidth": [{"id": "t-1", "width": 9}, {"id": "t-dup", "width": 9}]})
        self.assertTrue(has(bad, "id t-dup is ambiguous: 3 tracks/vias share that uuid"), bad)
        self.assertNotIn("t-dup", plan["tracks"])
        self.assertEqual(len(bridge.copper_by_uuid(b)["t-dup"]), 3)


class ValidatePoursAndNetclasses(unittest.TestCase):
    def test_pours(self):
        b = board()
        self.assertEqual(bridge.validate_pours(b, {"zones": [{"net": "GND", "layer": 2}]}), [])
        for spec, sub in (({"zones": {}}, "zones: a dict"), ({"zones": "GND"}, "zones: a str"), ([], "not an object"),
                          ({"zones": [{"net": ["GND"], "layer": 1}]}, "net ['GND']"), ({"zones": [{"net": "GND", "layer": 1.9}]}, "layer 1.9"),
                          ({"zones": [{"net": "GND", "layer": True}]}, "layer True"), ({"zones": [{"net": "GND", "layer": 1, "clearanceMil": "8"}]}, "clearanceMil"),
                          ({"zones": [{"net": "X", "layer": 1}], "extra": 1}, "unknown field 'extra'")):
            self.assertTrue(has(bridge.validate_pours(b, spec), sub), (spec, sub))

    def test_netclasses(self):
        ok = {"pcbpilotReqs": 2, "nets": {"GND": {"outerMil": 10}}, "isolation": [{"a": "HV", "b": "LV", "aNets": ["GND"], "bNets": [], "clearanceMm": 2}],
              "edges": [{"domain": "HV", "nets": ["GND"], "mil": 30}]}
        self.assertEqual(bridge.validate_netclasses(ok)[0], [])
        self.assertEqual(bridge.validate_netclasses({"GND": {"outerMil": 10}})[0], [])
        for req, sub in (({"pcbpilotReqs": 2, "nets": {}, "isolation": {}}, "isolation: a dict"),
                         ({"pcbpilotReqs": 2, "nets": {}, "edges": "x"}, "edges: a str"),
                         ({"pcbpilotReqs": 2, "nets": []}, "nets: must map"),
                         ({"GND": {"outerMil": "wide"}}, "outerMil"),
                         ({"pcbpilotReqs": 2, "nets": {}, "isolation": [{"aNets": [["GND"]]}]}, "aNets: not a list of net names"),
                         ({"pcbpilotReqs": 2, "nets": {}, "edges": [{"nets": "GND"}]}, "nets: not a list of net names")):
            self.assertTrue(has(bridge.validate_netclasses(req)[0], sub), (req, sub))


if __name__ == "__main__":
    unittest.main()
