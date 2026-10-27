# board_test.py — unit tests of board.py's pure helpers (the s-expression
# scanner and the stackup splice) with a fake pcbnew (plain python, no
# KiCad). Run by TestBoardPyUnit (go test) or: python -B board_test.py
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


if __name__ == "__main__":
    unittest.main()
