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
        self.assertEqual(bridge.resolve_footprint(es, "ref", "U1")[0]["key"], "u-1")
        self.assertEqual(bridge.resolve_footprint(es, "uuid", "h-2")[0]["key"], "h-2")
        self.assertEqual(bridge.resolve_footprint(es, "id", "fp:r-5:2")[0]["fp"], es[4]["fp"])
        self.assertIn("ambiguous: 2 footprints (h-1, h-2)", bridge.resolve_footprint(es, "ref", "H1")[1])
        self.assertIn("ambiguous", bridge.resolve_footprint(es, "uuid", "r-5")[1])
        self.assertEqual(bridge.resolve_footprint(es, "ref", "Q9"), (None, "missing"))
        # Namespaces never mix: a primitiveId is not a designator and back.
        self.assertEqual(bridge.resolve_footprint(es, "ref", "u-1"), (None, "missing"))
        self.assertEqual(bridge.resolve_footprint(es, "id", "U1"), (None, "missing"))
        self.assertEqual(bridge.resolve_footprint(es, "ref", "uuid:h-2"), (None, "missing"))
        dup = bridge.duplicate_refs(es)
        self.assertEqual(sorted(dup), ["H1", "R5"])

    def test_ref_spelled_like_an_id(self):
        # Two footprints share uuid "d": ids ref:C1 / ref:C2; a third
        # footprint's real designator is "ref:C1".
        b = Board(fps=[FP("d", "C1"), FP("d", "C2"), FP("z", "ref:C1", pads=("7",))])
        es = bridge.footprint_index(b)
        self.assertEqual([e["key"] for e in es], ["ref:C1", "ref:C2", "z"])
        self.assertIs(bridge.resolve_footprint(es, "ref", "ref:C1")[0]["fp"], b.fps[2])
        self.assertIs(bridge.resolve_footprint(es, "id", "ref:C1")[0]["fp"], b.fps[0])
        bad, plan = bridge.validate_edit(b, {"padZoneNone": [{"ref": "ref:C1", "pad": "7"}]})
        self.assertEqual(bad, [])
        self.assertIs(plan["zonePads"][0], b.fps[2].pads[0])
        bad = bridge.validate_edit(b, {"padZoneNone": [{"ref": "ref:C1", "id": "z", "pad": "7"}, {"pad": "7"}]})[0]
        self.assertTrue(has(bad, "exactly one of ref / id / uuid (got ref, id)"), bad)
        self.assertTrue(has(bad, "(got none)"), bad)

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
                    "padZoneNone[2]: id 3 is not a non-empty string", "pad None", "padZoneNone[4]: not an object"):
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


# ── netclass sizes: integer nm, board floors, partial classes ─────────────

FIELDS = ("TrackWidth", "Clearance", "ViaDiameter", "ViaDrill")


class NC:
    """A KiCad 9+ NETCLASS: optional fields, priority (lower wins)."""

    def __init__(self, name, init=True, **vals):
        self.name, self.prio, self.v = name, 0, dict(vals)
        if init:  # KiCad's built-in defaults
            for k, d in zip(FIELDS, (200000, 200000, 600000, 300000)):
                self.v.setdefault(k, d)

    def GetName(self):
        return self.name

    def SetPriority(self, p):
        self.prio = p

    def GetPriority(self):
        return self.prio

    def SetDescription(self, d):
        pass

    def __getattr__(self, attr):
        for pre in ("Has", "Get", "Set"):
            if attr.startswith(pre) and attr[len(pre):] in FIELDS:
                f = attr[len(pre):]
                if pre == "Has":
                    return lambda: f in self.v
                if pre == "Get":
                    return lambda: self.v.get(f, 0)
                return lambda x: self.v.__setitem__(f, x)
        raise AttributeError(attr)


class NS:
    """NET_SETTINGS: exact-name patterns; the effective class is the
    composite of every matching class by priority, Default filling the rest."""

    def __init__(self, default, classes=(), assign=None):
        self.default, self.classes, self.assign = default, {c.name: c for c in classes}, dict(assign or {})

    def GetDefaultNetclass(self):
        return self.default

    def GetNetclasses(self):
        return dict(self.classes)

    def SetNetclass(self, name, nc):
        self.classes[name] = nc

    def SetNetclassPatternAssignment(self, net, name):
        self.assign.setdefault(net, []).append(name)

    def ClearAllCaches(self):
        pass

    def GetEffectiveNetClass(self, net):
        cls = sorted((self.classes[n] for n in self.assign.get(net, [])), key=lambda c: c.prio)
        out = NC(",".join(c.name for c in cls) or "Default", False)
        for c in cls + [self.default]:
            for f in FIELDS:
                if f in c.v and f not in out.v:
                    out.v[f] = c.v[f]
        return out


class DS:
    def __init__(self, track=0, clr=0, hole=0, via=0, ring=0):
        self.m_TrackMinWidth, self.m_MinClearance = track, clr
        self.m_MinThroughDrill, self.m_ViasMinSize, self.m_ViasMinAnnularWidth = hole, via, ring


class NetclassPlan(unittest.TestCase):
    def setUp(self):
        self.saved = getattr(fake, "NETCLASS", None)
        fake.NETCLASS = NC

    def tearDown(self):
        fake.NETCLASS = self.saved

    def plan(self, ds, ns, names, reqs):
        missing, raised = [], []
        groups, floored = bridge.class_plan(ds, ns, set(names), reqs, missing, raised)
        return groups, floored, missing

    def test_floors_survive_mil_rounding(self):
        # 0.2 mm track / 0.127 mm clearance / 0.5 mm hole are not whole
        # hundredths of a mil: 7.87 mil would be 199898 nm < 200000.
        ds = DS(track=200000, clr=127000, hole=500000, via=900000, ring=50000)
        ns = NS(NC("Default", True, TrackWidth=150000, Clearance=100000, ViaDiameter=800000, ViaDrill=400000))
        groups, _, _ = self.plan(ds, ns, ["A"], {"A": {"outerMil": 7.87, "innerMil": 7.87, "minMil": 7.0, "clearanceMil": 4.99,
                                                      "viaDiaMil": 35.43, "viaDrillMil": 19.68}})
        (key, nets), = groups.items()
        w, inner, mn, c, outer, vdia, vdrill = key
        self.assertEqual(nets, ["A"])
        self.assertEqual((w, inner, mn, c), (200000, 200000, 200000, 127000))
        self.assertEqual((vdia, vdrill), (900000, 500000))
        self.assertTrue(all(isinstance(v, int) for v in key), key)
        self.assertEqual(bridge.mm_of_nm(w), "0.2")

    def test_inner_width_floored(self):
        ds = DS(track=200000)
        ns = NS(NC("Default", True, TrackWidth=100000))
        groups, _, _ = self.plan(ds, ns, ["A"], {"A": {"outerMil": 10, "innerMil": 5}})
        (key,) = groups
        self.assertEqual(key[1], 200000)  # inner 5 mil = 127000 nm < 0.2 mm

    def test_partial_and_multiple_classes(self):
        ds = DS(hole=500000, via=900000, ring=50000)
        dn = NC("Default", True, ViaDiameter=600000, ViaDrill=300000)
        dia_only = NC("DiaOnly", False, ViaDiameter=1000000)
        drill_only = NC("DrillOnly", False, ViaDrill=600000)
        thin = NC("Thin", False, ViaDiameter=950000, ViaDrill=900000)
        dia_only.prio, drill_only.prio = 1, 2
        ns = NS(dn, [dia_only, drill_only, thin],
                {"A": ["DiaOnly"], "B": ["DrillOnly"], "C": ["DiaOnly", "DrillOnly"], "D": ["Thin"]})
        names = ["", "A", "B", "C", "D", "E", "I"]
        _, floored, _ = self.plan(ds, ns, names, {"I": {"outerMil": 10}})
        eff = {n: bridge.effective_sizes(ns, n)[3:] for n in names if n}
        # A: 1.0 / Default 0.3 -> hole floor. B: Default 0.6 / 0.6 -> min via
        # 0.9 (ring 0.6 + 0.1 = 0.7 is lower). C: both classes, 1.0 / 0.6, is
        # fine. D: ring 0.025 -> 1.0. E: plain Default -> 0.9 / 0.5.
        self.assertEqual(eff["A"], (1000000, 500000))
        self.assertEqual(eff["B"], (900000, 600000))
        self.assertEqual(eff["C"], (1000000, 600000))
        self.assertEqual(eff["D"], (1000000, 900000))
        self.assertEqual(eff["E"], (900000, 500000))
        self.assertFalse(any(n.startswith("PPF_") for n in ns.assign.get("C", [])))
        # Default is floored in place (the DSN declares its padstack whatever
        # uses it); only D needs a via-only override class.
        self.assertEqual((dn.GetViaDiameter(), dn.GetViaDrill()), (900000, 500000))
        self.assertTrue(floored[0].startswith("Default: track/clearance/via"), floored)
        self.assertEqual([n for n in ns.assign if any(c.startswith("PPF_") for c in ns.assign[n])], ["D"])
        # The intent net is left to its PP class (class_plan's groups).
        self.assertFalse(any(n.startswith("PPF_") for n in ns.assign.get("I", [])))

    def test_non_intent_and_domain_nets_get_track_and_clearance_floors(self):
        # Default 0.1 / 0.1 mm and a user class "Thin" with a 0.12 mm track on
        # a domain-only net: neither is an intent net, both must still meet
        # the board's 0.2 mm track / 0.15 mm clearance.
        ds = DS(track=200000, clr=150000)
        dn = NC("Default", True, TrackWidth=100000, Clearance=100000)
        thin = NC("Thin", False, TrackWidth=120000)
        ns = NS(dn, [thin, NC("PPD_hv", False)], {"HV1": ["Thin", "PPD_hv"], "WIDE": ["Wide"]})
        ns.classes["Wide"] = NC("Wide", False, TrackWidth=300000, Clearance=250000)
        names = ["GND", "HV1", "WIDE"]
        self.plan(ds, ns, names, {})
        for n in names:
            _, w, c, _, _ = bridge.effective_sizes(ns, n)
            self.assertGreaterEqual(w, 200000, n)
            self.assertGreaterEqual(c, 150000, n)
        self.assertEqual(bridge.effective_sizes(ns, "WIDE")[1:3], (300000, 250000))  # never lowered
        self.assertEqual(bridge.check_effective(ds, ns, names, []), [])

    def test_floor_class_names_are_exact(self):
        # 1.0 mm / 0.5001 mm and 1.0009 mm / 0.5 mm are distinct pairs; µm
        # names would give both ..._1000_500.
        ds = DS(hole=500000, via=1000000, ring=50000)
        dn = NC("Default", True, ViaDiameter=1000000, ViaDrill=500000)
        a = NC("A", False, ViaDiameter=599000, ViaDrill=500100)  # -> 1.0 mm / 0.5001 mm
        b = NC("B", False, ViaDiameter=1000900, ViaDrill=300000)  # -> 1.0009 mm / 0.5 mm
        ns = NS(dn, [a, b], {"N1": ["A"], "N2": ["B"]})
        self.plan(ds, ns, ["N1", "N2"], {})
        e1, e2 = bridge.effective_sizes(ns, "N1"), bridge.effective_sizes(ns, "N2")
        self.assertEqual(e1[3:], (1000000, 500100))
        self.assertEqual(e2[3:], (1000900, 500000))
        ppf = sorted(n for n in ns.classes if n.startswith("PPF_"))
        self.assertEqual(len(ppf), 2, ppf)
        self.assertTrue(any(n.endswith("_1000000_500100") for n in ppf) and any(n.endswith("_1000900_500000") for n in ppf), ppf)

    def test_generated_classes_outrank_every_project_class(self):
        # A project class at priority -2000000 with a small via and clearance
        # must not override PP / PPF; check_effective catches one that would.
        ds = DS(track=200000, clr=150000, hole=500000, via=900000, ring=50000)
        dn = NC("Default", True, TrackWidth=250000, Clearance=200000, ViaDiameter=1000000, ViaDrill=500000)
        boss = NC("Boss", True, TrackWidth=100000, Clearance=100000, ViaDiameter=600000, ViaDrill=300000)
        boss.prio = -2000000
        ns = NS(dn, [boss], {"I": ["Boss"], "O": ["Boss"]})
        prio = bridge.class_priorities(ns)
        self.assertLess(max(prio.values()), -2000000)
        missing, raised = [], []
        groups, _ = bridge.class_plan(ds, ns, {"I", "O"}, {"I": {"outerMil": 10, "clearanceMil": 8}}, missing, raised, prio)
        (key, nets), = groups.items()
        pp = NC("PP1_W10", False, TrackWidth=key[0], Clearance=key[3], ViaDiameter=key[5], ViaDrill=key[6])
        pp.prio = prio["PP"]
        ns.SetNetclass(pp.name, pp)
        ns.SetNetclassPatternAssignment("I", pp.name)
        q = {"track": key[0], "clearance": key[3], "viaDiameter": key[5], "viaDrill": key[6]}
        classes = [{"name": pp.name, "nets": ["I"], "nm": q}]
        self.assertEqual(bridge.check_effective(ds, ns, {"I", "O"}, classes), [])
        self.assertEqual(bridge.effective_sizes(ns, "O")[1:], (200000, 150000, 900000, 500000))
        # The same PP class below the project class: caught, not shipped.
        pp.prio = -1000000
        bad = bridge.check_effective(ds, ns, {"I", "O"}, classes)
        self.assertTrue(bad and bad[0].startswith("I: effective class Boss"), bad)

    def test_no_valueless_netclass_is_refused(self):
        def old_netclass(name, *init):
            if init:
                raise TypeError("NETCLASS() takes 1 argument")
            return NC(name)
        fake.NETCLASS = old_netclass
        with self.assertRaises(bridge.UnsupportedKiCad):
            bridge.empty_netclass("PPD_x")
        import tempfile
        d = tempfile.mkdtemp()
        reqs = os.path.join(d, "reqs.json")
        with open(reqs, "w") as f:
            f.write('{"GND": {"outerMil": 10}}')
        loaded = []
        fake.LoadBoard = lambda p: loaded.append(p)
        res = bridge.netclasses(os.path.join(d, "b.kicad_pcb"), reqs, os.path.join(d, "out.kicad_pcb"))
        self.assertFalse(res["ok"])
        self.assertIn("KiCad 9", res["error"])
        self.assertEqual(loaded, [])  # refused before the board is loaded or anything written
        self.assertFalse(os.path.exists(os.path.join(d, "out.kicad_pcb")))


import place  # noqa: E402  (same fake pcbnew)


class PlaceRecords(unittest.TestCase):
    pose = {"xMil": 1, "yMil": 2, "rotationDeg": 0}

    def test_envelope(self):
        recs, bad = place.records({"pcbpilotPoses": 2, "poses": [dict(self.pose, ref="U1"), dict(self.pose, uuid="u-9")]})
        self.assertEqual(bad, [])
        self.assertEqual([r[0] for r in recs], [{"ref": "U1"}, {"uuid": "u-9"}])
        for doc in ({"pcbpilotPoses": 3, "poses": []}, {"pcbpilotPoses": 2, "poses": None}, {"pcbpilotPoses": 2, "poses": [], "x": 1}):
            self.assertTrue(place.records(doc)[1], doc)

    def test_legacy_designator_named_like_the_envelope(self):
        # A footprint literally called "pcbpilotPoses": its value is a pose
        # object, so this is a legacy map, keys as designators.
        recs, bad = place.records({"pcbpilotPoses": dict(self.pose), "uuid:abc": dict(self.pose)})
        self.assertEqual(bad, [])
        self.assertEqual(sorted(r[0]["ref"] for r in recs), ["pcbpilotPoses", "uuid:abc"])
        recs, bad = place.records({"pcbpilotPoses": True})  # not a version number either
        self.assertEqual((bad, [r[0] for r in recs]), ([], [{"ref": "pcbpilotPoses"}]))


if __name__ == "__main__":
    unittest.main()
