"""Offline tests for scripts/agent_clients.py (no real client configs touched)."""
import importlib.util
import json
import os
import shutil
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("agent_clients", os.path.join(HERE, "..", "agent_clients.py"))
ac = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ac)

CODEX = """model = "x"

[mcp_servers.node_repl]
command = "n"

[mcp_servers.easyeda-agent]
command = "/node"
args = ["/up/server.mjs"]

[mcp_servers.easyeda-agent.env]
EASYEDA_BIN = "/bin/easyeda"

[mcp_servers.easyeda-agentic]
command = "keep-me"

[tui]
x = 1
"""


class AgentClientsTest(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.mkdtemp()
        os.makedirs(os.path.join(self.home, ".codex"))
        os.makedirs(os.path.join(self.home, ".zcode", "cli"))
        with open(os.path.join(self.home, ".codex", "config.toml"), "w") as f:
            f.write(CODEX)
        with open(os.path.join(self.home, ".zcode", "cli", "config.json"), "w") as f:
            json.dump({"plugins": {"enabledPlugins": {}}, "mcp": {"servers": {"easyeda-agent": {"command": "n"}, "other": {"command": "o"}}}}, f)
        with open(os.path.join(self.home, ".claude.json"), "w") as f:
            json.dump({"mcpServers": {"easyeda-agent": {"command": "n"}, "keep": {}}, "projects": {"x": 1}}, f)
        ac.CLAUDE_JSON = os.path.join(self.home, ".claude.json")
        ac.CODEX_TOML = os.path.join(self.home, ".codex", "config.toml")
        ac.ZCODE_JSON = os.path.join(self.home, ".zcode", "cli", "config.json")
        ac.AGENTS_JSON = os.path.join(self.home, ".agents", "mcp.json")
        ac.have = lambda cmd: False  # exercise the file-editing paths, never the real `claude` CLI

    def tearDown(self):
        shutil.rmtree(self.home)

    def test_clean_then_register(self):
        bk = os.path.join(self.home, "bk")
        removed = ac.clean_upstream(bk, dry=False)
        self.assertEqual(sorted(removed), ["claude:easyeda-agent", "codex:easyeda-agent", "zcode:easyeda-agent"])
        toml = open(ac.CODEX_TOML).read()
        self.assertNotIn("[mcp_servers.easyeda-agent]", toml)
        self.assertNotIn("EASYEDA_BIN", toml)
        self.assertIn("[mcp_servers.easyeda-agentic]", toml)  # same prefix, different server: kept
        self.assertIn("[mcp_servers.node_repl]", toml)
        self.assertIn("[tui]", toml)
        self.assertEqual(list(json.load(open(ac.ZCODE_JSON))["mcp"]["servers"]), ["other"])
        claude = json.load(open(ac.CLAUDE_JSON))
        self.assertEqual(list(claude["mcpServers"]), ["keep"])
        self.assertEqual(claude["projects"], {"x": 1})
        self.assertTrue(os.path.exists(os.path.join(bk, "config.toml.orig")))

        ac.register("/bin/pcbpilot", "/node", "/repo/mcp/src/server.mjs", dry=False)
        ac.register("/bin/pcbpilot", "/node", "/repo/mcp/src/server.mjs", dry=False)  # idempotent
        toml = open(ac.CODEX_TOML).read()
        self.assertEqual(toml.count("[mcp_servers.pcbpilot]"), 1)
        self.assertIn('PCBPILOT_BIN = "/bin/pcbpilot"', toml)
        z = json.load(open(ac.ZCODE_JSON))["mcp"]["servers"]["pcbpilot"]
        self.assertEqual(z["args"], ["/repo/mcp/src/server.mjs"])
        self.assertEqual(json.load(open(ac.CLAUDE_JSON))["mcpServers"]["pcbpilot"]["env"]["PCBPILOT_BIN"], "/bin/pcbpilot")

    def test_dry_run_changes_nothing(self):
        before = open(ac.CODEX_TOML).read()
        ac.clean_upstream(os.path.join(self.home, "bk"), dry=True)
        ac.register("/b", "/n", "/s", dry=True)
        self.assertEqual(open(ac.CODEX_TOML).read(), before)
        self.assertIn("easyeda-agent", json.load(open(ac.ZCODE_JSON))["mcp"]["servers"])


if __name__ == "__main__":
    unittest.main()


class ServiceEntryTest(unittest.TestCase):
    def test_launchd_and_systemd_entries(self):
        import sys as _sys
        with tempfile.TemporaryDirectory() as home:
            self.assertEqual(ac.service_entry(home), (None, None))
            if _sys.platform == "darwin":
                p = os.path.join(home, "Library", "LaunchAgents", "com.pcbpilot.daemon.plist")
                os.makedirs(os.path.dirname(p))
                open(p, "w").write("<array><string>/opt/a &amp; b/pcbpilot</string><string>daemon</string>")
                self.assertEqual(ac.service_entry(home), (p, "/opt/a & b/pcbpilot"))
            elif _sys.platform.startswith("linux"):
                p = os.path.join(home, ".config", "systemd", "user", "pcbpilot-daemon.service")
                os.makedirs(os.path.dirname(p))
                open(p, "w").write('[Service]\nExecStart="/opt/my dir/pcbpilot" daemon start\n')
                self.assertEqual(ac.service_entry(home), (p, "/opt/my dir/pcbpilot"))
