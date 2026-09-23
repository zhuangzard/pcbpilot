"""Installed-skill runtime tests; fake CLI only, no daemon or EDA traffic."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

REPO = Path(__file__).resolve().parents[2]


class InstalledSkillTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.skill = self.root / "skills/pcbpilot"
        self.scripts = self.skill / "scripts"
        self.refs = self.skill / "references"
        self.scripts.mkdir(parents=True)
        self.refs.mkdir()
        for name in ["blocks-pin-audit.py", "lint.sh"]:
            shutil.copyfile(REPO / ".agents/skills/pcbpilot/scripts" / name, self.scripts / name)
        spec = importlib.util.spec_from_file_location("audit_fixture", self.scripts / "blocks-pin-audit.py")
        self.audit = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.audit)
        self.log = self.root / "calls.jsonl"
        body = '''import json, os, sys
a=sys.argv[1:]
with open(os.environ['FAKE_LOG'],'a',encoding='utf-8') as f: f.write(json.dumps(a)+'\\n')
if a[:3]==['blocks','ls','--json']:
 print(json.dumps([{'id':'block.demo'}]))
elif a[:2]==['blocks','show']:
 print(json.dumps({'id':'block.demo','parts':{'U':{'part':'test-chip'}},'internal_nets':[['U.VCC']]}))
elif a[:2]==['sch','clear'] and os.environ.get('FAKE_FAIL_CLEAR'):
 print(json.dumps({'ok':False,'error':'clear failed'})); sys.exit(1)
elif a[:2]==['sch','list']:
 print(json.dumps({'ok':True,'result':{'components':[{'componentType':'part','x':200,'y':200,'pins':[{'pinNumber':'1','pinName':'VCC'}]}]}}))
else:
 print(json.dumps({'ok':True,'result':{}}))
'''
        # Windows has no shebang and an extensionless file is not executable
        # there (shutil.which rejects it), so the fake CLI ships as a .cmd
        # launcher next to the same Python body. POSIX keeps the shebang.
        fake_bin = self.root / "fake-bin"
        fake_bin.mkdir()
        if os.name == "nt":
            (fake_bin / "easyeda-fake.py").write_text(body, encoding="utf-8")
            self.binary = fake_bin / "easyeda.cmd"
            self.binary.write_text(f'@"{sys.executable}" "%~dp0easyeda-fake.py" %*\n', encoding="utf-8")
        else:
            self.binary = fake_bin / "pcbpilot"
            self.binary.write_text(f"#!{sys.executable}\n" + body, encoding="utf-8")
            self.binary.chmod(0o755)
        (self.refs / "standard-parts.json").write_text(json.dumps({"libraryUuid": "official", "parts": {"test-chip": {"deviceUuid": "a" * 32}}}), encoding="utf-8")
        (self.refs / "symbol-pins.json").write_text('{"parts":{}}', encoding="utf-8")
        self.env = {"PCBPILOT_BIN": str(self.binary), "FAKE_LOG": str(self.log)}

    def calls(self):
        return [json.loads(line) for line in self.log.read_text(encoding="utf-8").splitlines()] if self.log.exists() else []

    def test_installed_audit_reads_full_blocks_from_cli(self):
        with mock.patch.dict(os.environ, self.env):
            refs, parts = self.audit.load_refs()
        self.assertEqual(parts, ["test-chip"])
        self.assertEqual(refs["block.demo"], [("U", "VCC", "test-chip")])
        self.assertEqual(self.calls(), [["blocks", "ls", "--json"], ["blocks", "show", "block.demo"]])

    def test_explicit_bad_cli_does_not_fall_back(self):
        with mock.patch.dict(os.environ, {**self.env, "PCBPILOT_BIN": str(self.root / "missing")}):
            with self.assertRaisesRegex(RuntimeError, "PCBPILOT_BIN is not an executable"):
                self.audit.load_refs()
        self.assertEqual(self.calls(), [])

    def test_probe_requires_all_three_explicit_scope_arguments(self):
        with mock.patch.dict(os.environ, self.env):
            for args in [(None, "page", True), ("scratch", None, True), ("scratch", "page", False)]:
                with self.assertRaisesRegex(RuntimeError, "requires explicit"):
                    self.audit.probe(*args)
        self.assertEqual(self.calls(), [])

    def test_nothing_to_probe_never_clears(self):
        path = self.refs / "symbol-pins.json"
        path.write_text('{"parts":{"test-chip":[{"n":"1","name":"VCC"}]}}', encoding="utf-8")
        before = path.read_bytes()
        with mock.patch.dict(os.environ, self.env), contextlib.redirect_stdout(io.StringIO()):
            self.audit.probe("scratch", "measurement", True)
        self.assertEqual(path.read_bytes(), before)
        self.assertTrue(all(call[0] == "blocks" for call in self.calls()))

    def test_failed_clear_stops_before_placement_and_preserves_cache(self):
        before = (self.refs / "symbol-pins.json").read_bytes()
        with mock.patch.dict(os.environ, {**self.env, "FAKE_FAIL_CLEAR": "1"}), contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(RuntimeError, "clear failed.*probe stopped"):
                self.audit.probe("scratch", "measurement", True)
        writes = [c for c in self.calls() if c[0] == "sch"]
        self.assertEqual(writes, [["sch", "clear", "--project", "scratch", "--doc", "measurement"]])
        self.assertEqual((self.refs / "symbol-pins.json").read_bytes(), before)

    def test_successful_probe_pins_every_action_and_updates_local_cache(self):
        with mock.patch.dict(os.environ, self.env), contextlib.redirect_stdout(io.StringIO()):
            self.audit.probe("scratch", "measurement", True)
        writes = [c for c in self.calls() if c[0] == "sch"]
        self.assertEqual([c[1] for c in writes], ["clear", "save", "place", "list", "clear", "save"])
        for call in writes:
            self.assertEqual(call[call.index("--project") + 1], "scratch")
            self.assertEqual(call[call.index("--doc") + 1], "measurement")
        table = json.loads((self.refs / "symbol-pins.json").read_text(encoding="utf-8"))["parts"]
        self.assertEqual(table["test-chip"], [{"n": "1", "name": "VCC"}])

    def run_lint(self, env):
        # Reversed port range executes no curl request. The diagnostic includes
        # the selected CLI, so resolution is tested without touching any daemon.
        return subprocess.run(["/bin/bash", str(self.scripts / "lint.sh"), "fixture", "127.0.0.1", "2", "1"],
                              env=env, text=True, capture_output=True)

    @unittest.skipIf(os.name == 'nt', 'lint.sh is a POSIX shell entry point; Windows uses the native CLI')
    def test_lint_cli_explicit_path_and_path_fallback(self):
        env = {**os.environ, **self.env, "PATH": f"{self.binary.parent}:/usr/bin:/bin"}
        result = self.run_lint(env)
        self.assertIn(f"run: {self.binary} daemon", result.stderr)
        env.pop("PCBPILOT_BIN")
        result = self.run_lint(env)
        self.assertIn(f"run: {self.binary} daemon", result.stderr)
        env["PCBPILOT_BIN"] = str(self.root / "missing")
        result = self.run_lint(env)
        self.assertIn("PCBPILOT_BIN is not an executable", result.stderr)
        self.assertEqual(self.calls(), [])

    @unittest.skipIf(os.name == 'nt', 'lint.sh is a POSIX shell entry point; Windows uses the native CLI')
    def test_lint_development_fallback_requires_a_repository(self):
        relocated = self.root / ".agents/skills/pcbpilot"
        relocated.parent.mkdir(parents=True)
        self.skill.rename(relocated)
        self.skill = relocated
        self.scripts = self.skill / "scripts"
        (self.root / "go.mod").write_text("module fixture\n", encoding="utf-8")
        (self.root / "cmd/pcbpilot").mkdir(parents=True)
        (self.root / "bin").mkdir()
        fallback = self.root / "bin/pcbpilot"
        shutil.copyfile(self.binary, fallback)
        fallback.chmod(0o755)
        env = {**os.environ, "PATH": "/usr/bin:/bin"}
        env.pop("PCBPILOT_BIN", None)
        result = self.run_lint(env)
        self.assertIn(f"run: {fallback.resolve()} daemon", result.stderr)
        (self.root / "go.mod").unlink()
        result = self.run_lint(env)
        self.assertIn("pcbpilot CLI not found", result.stderr)


if __name__ == "__main__":
    unittest.main()
