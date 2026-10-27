# board_test.py — unit tests of board.py's pure helpers (the s-expression
# scanner and the stackup splice) with a fake pcbnew (plain python, no
# KiCad). Run by TestBoardPyUnit (go test) or: python -B board_test.py
import math
import os
import sys
import types
import unittest

sys.modules["pcbnew"] = types.ModuleType("pcbnew")
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import board  # noqa: E402

BOARD = '''(kicad_pcb
\t(version 20260206)
\t(general
\t\t(thickness 1.6)
\t)
\t(title_block (comment 1 "a (tricky) \\"setup\\" string"))
\t(setup
\t\t(pad_to_mask_clearance 0)
\t\t(pcbplotparams
\t\t\t(outputdirectory "")
\t\t)
\t)
\t(embedded_fonts no)
)
'''

BLOCK = '(stackup\n\t(layer "F.Cu"\n\t\t(type "copper")\n\t)\n\t(copper_finish "ENIG")\n)'


class SexprTest(unittest.TestCase):
    def test_top_level_children(self):
        heads = [c[2] for c in board.sexpr_children(BOARD, BOARD.index("("))]
        self.assertEqual(heads, ["version", "general", "title_block", "setup", "embedded_fonts"])

    def test_strings_with_parens_are_skipped(self):
        tb = next(c for c in board.sexpr_children(BOARD, BOARD.index("(")) if c[2] == "title_block")
        self.assertEqual([c[2] for c in board.sexpr_children(BOARD, tb[0])], ["comment"])
        self.assertTrue(BOARD[tb[0]:tb[1]].endswith('string"))'))


class SpliceTest(unittest.TestCase):
    def test_insert_then_replace(self):
        once = board.splice_stackup(BOARD, BLOCK)
        self.assertEqual(board.stackup_text(once), BLOCK)
        setup = next(c for c in board.sexpr_children(once, once.index("(")) if c[2] == "setup")
        self.assertEqual([c[2] for c in board.sexpr_children(once, setup[0])], ["stackup", "pad_to_mask_clearance", "pcbplotparams"])
        other = BLOCK.replace("ENIG", "OSP")
        twice = board.splice_stackup(once, other)
        self.assertEqual(board.stackup_text(twice), other)
        self.assertEqual(twice.count("(stackup"), 1)
        self.assertEqual(twice.count("("), twice.count(")") - 0)
        # Everything outside the setup section is untouched.
        self.assertEqual(twice.replace(other + "\n", ""), BOARD)

    def test_no_setup(self):
        with self.assertRaises(ValueError):
            board.splice_stackup("(kicad_pcb (version 1))", BLOCK)
        self.assertEqual(board.stackup_text("(kicad_pcb (version 1))"), "")


class ProblemsTest(unittest.TestCase):
    def test_add(self):
        p = board.Problems()
        self.assertFalse(p)
        p.add("E_BOARD_NET", "net", "x", "fix")
        self.assertEqual(p, [{"code": "E_BOARD_NET", "field": "net", "detail": "x", "fix": "fix"}])


def square(x0, y0, x1, y1):
    return [(x0, y0), (x1, y0), (x1, y1), (x0, y1)]


def circle_item(x, y, d):
    return {"edges": [{"arc": [x, y, d / 2, 0, 2 * math.pi]}], "shape": {"circle": {"x": x, "y": y, "diameter": d}}}


def rect_item(x, y, w, h):
    pts = square(x, y, x + w, y + h)
    return {"edges": [{"seg": [pts[i][0], pts[i][1], pts[(i + 1) % 4][0], pts[(i + 1) % 4][1]]} for i in range(4)],
            "shape": {"rect": {"x": x, "y": y, "width": w, "height": h}}}


def board_of(contours):
    """(contours, contains) of a board: first contour the outline, the rest cutouts."""
    def contains(p):
        return board.point_in_poly(contours[0], p) and not any(board.point_in_poly(c, p) for c in contours[1:])
    return contours, contains


class GeometryTest(unittest.TestCase):
    def test_hole_enclosing_a_cutout(self):
        cs, inside = board_of([square(0, 0, 50, 50), square(20, 20, 22, 22)])
        why = board.fits(cs, inside, circle_item(21, 21, 10))
        self.assertIn("encloses", why)
        self.assertIsNone(board.fits(cs, inside, circle_item(40, 40, 6)))

    def test_concave_crossing(self):
        u = [(0, 0), (60, 0), (60, 40), (35, 40), (35, 10), (25, 10), (25, 40), (0, 40)]
        cs, inside = board_of([u])
        self.assertIn("crosses", board.fits(cs, inside, rect_item(20, 30, 20, 4)))
        self.assertIsNone(board.fits(cs, inside, rect_item(5, 30, 15, 4)))

    def test_tangency(self):
        cs, inside = board_of([square(0, 0, 50, 50)])
        self.assertIn("crosses", board.fits(cs, inside, circle_item(25, 5.0005, 10)))   # 0.0005 mm from the edge
        self.assertIsNone(board.fits(cs, inside, circle_item(25, 5.01, 10)))
        self.assertIn("outside", board.fits(cs, inside, circle_item(80, 80, 2)))

    def test_distances(self):
        self.assertAlmostEqual(board.seg_arc((-5, 1.0005), (5, 1.0005), [0, 0, 1, 0, 2 * math.pi]), 0.0005, places=9)
        self.assertEqual(board.seg_arc((-5, 0), (5, 0), [0, 0, 1, 0, 2 * math.pi]), 0.0)
        # The half arc on the far side (y < 0): the segment below it is nearest its end points.
        self.assertAlmostEqual(board.seg_arc((-0.5, 0.5), (0.5, 0.5), [0, 0, 1, math.pi, math.pi]), math.hypot(0.5, 0.5), places=9)
        self.assertLess(board.sdf({"rect": {"x": 0, "y": 0, "width": 10, "height": 10, "cornerRadius": 2}}, (5, 5)), 0)
        self.assertGreater(board.sdf({"rect": {"x": 0, "y": 0, "width": 10, "height": 10, "cornerRadius": 2}}, (0.3, 0.3)), 0)
        self.assertAlmostEqual(board.sdf({"slot": {"x1": 0, "y1": 0, "x2": 10, "y2": 0, "width": 2}}, (12, 0)), 1)


HOLE = {"x": 4, "y": 5, "drill": 3.2, "pad": 6.4, "type": "pth", "net": "GND"}
ZONE = {"net": "GND", "clearance": 0.3, "minWidth": 0.25, "thermalGap": 0.5, "spokeWidth": 0.4, "connection": "thermal",
        "priority": 2, "name": "gnd", "fill": True}
TEXT = {"text": "rev A", "x": 1, "y": 2, "size": 1.2, "thickness": 0.18, "rotation": 90, "justify": "left"}
KEEP = {"shape": {"samples": [[0, 0], [5, 0], [5, 5]]}, "forbid": ["tracks", "vias"], "name": "k"}


def perturbed(v):
    if isinstance(v, bool):
        return not v
    if isinstance(v, (int, float)):
        return v + 0.01
    if isinstance(v, str):
        return v + "x"
    if isinstance(v, list):
        return perturbed(v[0]) if not v else [perturbed(v[0])] + v[1:]
    raise TypeError(v)


class ReadbackTest(unittest.TestCase):
    def check_every_field(self, want):
        self.assertEqual(board.compare("x", want, dict(want)), [])
        for k in want:
            got = dict(want)
            got[k] = perturbed(want[k])
            self.assertTrue(board.compare("x", want, got), "altered %s not caught" % k)
            del got[k]
            self.assertTrue(board.compare("x", want, got), "missing %s not caught" % k)

    def test_hole(self):
        self.check_every_field(board.expect_hole(HOLE, "H1", [0, 1, 2, 3, 4]))
        self.assertEqual(board.expect_hole(dict(HOLE, type="npth"), "H1", [0])["number"], "")

    def test_zone(self):
        self.check_every_field(board.expect_zone(ZONE, ["B.Cu", "F.Cu"], [[0, 0], [10, 0], [10, 10]]))

    def test_keepout(self):
        self.check_every_field(board.expect_keepout(KEEP, ["F.Cu"]))

    def test_text(self):
        self.check_every_field(board.expect_text(TEXT, "F.Silkscreen", False))

    def test_edge(self):
        self.check_every_field(board.expect_edge({"kind": "arc", "start": [0, 1], "mid": [1, 1], "end": [1, 0], "lineWidth": 9}))

    def test_stackup_rows(self):
        want = [{"name": "F.Cu", "type": "copper", "thickness": 0.035},
                {"name": "dielectric 1", "type": "core", "thickness": 1.51, "material": "FR4", "epsilonR": 4.5, "lossTangent": 0.02},
                {"name": "B.Cu", "type": "copper", "thickness": 0.035}]
        block = ('(stackup\n\t(layer "F.Cu" (type "copper") (thickness 0.035))\n'
                 '\t(layer "dielectric 1" (type "core") (thickness 1.51) (material "FR4") (epsilon_r 4.5) (loss_tangent 0.02))\n'
                 '\t(layer "B.Cu" (type "copper") (thickness 0.035))\n\t(copper_finish "ENIG")\n)')
        rows, finish = board.read_stackup(block)
        self.assertEqual(board.check_stackup_rows(want, rows, "ENIG", finish), [])
        for i, k, v in [(1, "thickness", 1.5), (1, "material", "FR408"), (1, "epsilonR", 4.2), (1, "lossTangent", 0.03),
                        (1, "type", "prepreg"), (0, "thickness", 0.07)]:
            bad = [dict(r) for r in rows]
            bad[i][k] = v
            self.assertTrue(board.check_stackup_rows(want, bad, "ENIG", finish), "altered %s.%s not caught" % (i, k))
        self.assertTrue(board.check_stackup_rows(want, rows, "OSP", finish))
        self.assertTrue(board.check_stackup_rows(want, rows[:2], "ENIG", finish))

    def test_stackup_colors(self):
        want = [{"name": "F.SilkS", "type": "Top Silk Screen", "thickness": 0, "color": "White"},
                {"name": "F.Mask", "type": "Top Solder Mask", "thickness": 0.01, "color": "Green"}]
        block = ('(stackup\n\t(layer "F.SilkS"\n\t\t(type "Top Silk Screen")\n\t\t(color "White")\n\t)\n'
                 '\t(layer "F.Mask" (type "Top Solder Mask") (color "Green") (thickness 0.01))\n\t(copper_finish "None")\n)')
        rows, finish = board.read_stackup(block)
        self.assertEqual(board.check_stackup_rows(want, rows, "None", finish), [])
        for i in range(2):
            bad = [dict(r) for r in rows]
            bad[i]["color"] = "Black"
            self.assertTrue(board.check_stackup_rows(want, bad, "None", finish), "altered colour %d not caught" % i)
            del bad[i]["color"]
            self.assertTrue(board.check_stackup_rows(want, bad, "None", finish), "missing colour %d not caught" % i)

    def test_bool_is_not_a_number(self):
        self.assertTrue(board.compare("", {"locked": True}, {"locked": 1}))


if __name__ == "__main__":
    unittest.main()
