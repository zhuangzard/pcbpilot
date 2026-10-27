import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "gen-command-index.py"
spec = importlib.util.spec_from_file_location("gen_command_index", SCRIPT)
gen = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gen)

ROOT_HELP = """pcbpilot — test

Usage:
  pcbpilot [command]

Available Commands:
  completion  Generate the autocompletion script
  help        Help about any command
  kicad       Design in KiCad
  sch         Schematic operations

Flags:
  -h, --help   help for pcbpilot
"""
KICAD_HELP = """Available Commands:
  fab         Export manufacturing files
  sch-check   Schematic hard gate

Flags:
  -h, --help   help for kicad
"""
# The longest name gets a single space before its summary; a missing Short
# leaves the name alone on the line.
SCH_HELP = """Additional Commands:
  gate        Run the gate
  post-layout Post-layout check
  bare
"""


class ParseTest(unittest.TestCase):
    def test_parse_subcommands_reads_every_block_and_tight_padding(self):
        self.assertEqual(gen.parse_subcommands(SCH_HELP),
                         [("gate", "Run the gate"), ("post-layout", "Post-layout check"), ("bare", "")])
        self.assertEqual([n for n, _ in gen.parse_subcommands(ROOT_HELP)], ["completion", "help", "kicad", "sch"])

    def test_walk_skips_shell_plumbing_and_recurses(self):
        tree = {(): ROOT_HELP, ("kicad",): KICAD_HELP, ("sch",): SCH_HELP}
        rows = gen.walk(lambda path: tree.get(path, "Usage:\n  leaf\n"))
        paths = [" ".join(p) for p, _ in rows]
        self.assertEqual(paths, ["kicad", "kicad fab", "kicad sch-check", "sch", "sch bare", "sch gate", "sch post-layout"])

    def test_doc_pattern_ignores_kicad_cli_and_bare_words(self):
        drc = gen.doc_pattern(("pcb", "drc"))
        self.assertIsNone(drc.search("kicad-cli pcb drc --format json"))
        self.assertIsNotNone(drc.search("run `pcbpilot pcb drc --json`"))
        self.assertIsNone(gen.doc_pattern(("pcb", "check")).search("pcb check-list"))
        top = gen.doc_pattern(("apply",))
        self.assertIsNone(top.search("apply the fix"))
        self.assertIsNotNone(top.search("pcbpilot apply steps.json"))

    def test_render_links_docs_and_counts_undocumented(self):
        rows = [(("kicad", "fab"), "Export | files"), (("kicad", "place"), "Place")]
        text = gen.render(rows, {"kicad-jlc.md": "pcbpilot kicad fab --out x", "../SKILL.md": "kicad fab"})
        self.assertIn("| `kicad fab` | Export \\| files | [kicad-jlc.md](kicad-jlc.md), [SKILL.md](../SKILL.md) |", text)
        self.assertIn("| `kicad place` | Place | — |", text)
        self.assertIn("其中 1 条尚无参考提到", text)


@unittest.skipUnless(os.environ.get("PCBPILOT_BIN") or shutil.which("go"), "needs PCBPILOT_BIN or a Go toolchain")
class CommittedIndexTest(unittest.TestCase):
    def test_index_matches_cli(self):
        # The index is generated from this checkout's CLI; editing the CLI or a
        # reference without regenerating it must fail here.
        res = subprocess.run([sys.executable, str(SCRIPT), "--check"], cwd=ROOT, capture_output=True,
                             text=True, encoding="utf-8", errors="replace", timeout=1200)
        self.assertEqual(res.returncode, 0, res.stderr)


if __name__ == "__main__":
    unittest.main()
