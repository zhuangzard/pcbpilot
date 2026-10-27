"""install.ps1 contract tests against a localhost release; Windows only, never the network.

The fake CLI is a tiny Go program (go must be on PATH); the release base URL is
pointed at a local HTTP server in a temporary copy of the script.
"""
import functools
import hashlib
import http.server
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import threading
import unittest

REPO = Path(__file__).resolve().parents[2]
DEMO = 'pcbpilot-demo'  # test-only focused Skill
POWERSHELL = shutil.which('powershell') or shutil.which('pwsh')


class QuietHandler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass


def canonical_manifest(skills, retired=()):
    data = {'retired': list(retired), 'router': 'pcbpilot', 'schemaVersion': 1, 'skills': list(skills)}
    return (json.dumps(data, indent=2, sort_keys=True) + '\n').encode()


@unittest.skipUnless(os.name == 'nt' and POWERSHELL and shutil.which('go'), 'install.ps1 runs on Windows with PowerShell; fixture CLI needs Go')
class PowerShellInstallerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build = Path(tempfile.mkdtemp(prefix='pcbpilot-ps1-cli-'))
        (cls.build / 'go.mod').write_text('module fakecli\n\ngo 1.21\n')
        (cls.build / 'main.go').write_text('package main\n\nimport "fmt"\n\nfunc main() { fmt.Println("pcbpilot v1.4.2") }\n')
        subprocess.check_call(['go', 'build', '-o', str(cls.build / 'pcbpilot_windows_amd64.exe'), '.'], cwd=cls.build,
                              env={**os.environ, 'GOFLAGS': '-mod=mod'})

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.build, ignore_errors=True)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='pcbpilot ps1 ')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.assets = self.root / 'srv/download/v1.4.2'
        self.assets.mkdir(parents=True)
        shutil.copy2(self.build / 'pcbpilot_windows_amd64.exe', self.assets)
        handler = functools.partial(QuietHandler, directory=str(self.root / 'srv'))
        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        script = (REPO / 'install.ps1').read_text(encoding='ascii')
        old = '$BaseUrl = "https://github.com/$Repo/releases/download/$Version"'
        self.assertIn(old, script)
        self.script = self.root / 'install-test.ps1'
        self.script.write_text(script.replace(old, f'$BaseUrl = "http://127.0.0.1:{self.server.server_address[1]}/download/$Version"'),
                               encoding='ascii')
        self.home = self.root / 'home'
        self.codex, self.claude = self.home / 'codex', self.home / 'claude'
        self.package()

    def package(self, entries=None, skills=('pcbpilot', DEMO), retired=(), manifest=True, demo_version='1.4.2'):
        """entries: extra (name, kind, data) tar members appended after the Skills."""
        with tarfile.open(self.assets / 'skills.tar.gz', 'w:gz') as archive:
            def add(name, data=b'', kind='file'):
                item = tarfile.TarInfo(name)
                item.mode = 0o644
                if kind == 'symlink':
                    item.type, item.linkname = tarfile.SYMTYPE, '../../outside'
                    archive.addfile(item)
                elif kind == 'hardlink':
                    item.type, item.linkname = tarfile.LNKTYPE, 'pcbpilot/SKILL.md'
                    archive.addfile(item)
                else:
                    item.size = len(data)
                    archive.addfile(item, io.BytesIO(data))
            if manifest is True:
                add('pcbpilot/.public-skills.json', canonical_manifest(skills, retired))
            elif manifest:
                add('pcbpilot/.public-skills.json', manifest)
            for skill in skills:
                version = '1.4.2' if skill == 'pcbpilot' else demo_version
                add(f'{skill}/SKILL.md', f'---\nname: {skill}\nmetadata:\n  version: "{version}"\n---\nbody\n'.encode())
                add(f'{skill}/references/x.md', b'ref\n')
            for name, kind, data in entries or ():
                add(name, data, kind)
        names = ['pcbpilot_windows_amd64.exe', 'skills.tar.gz']
        (self.assets / 'checksums.txt').write_text(''.join(
            f'{hashlib.sha256((self.assets / n).read_bytes()).hexdigest()}  {n}\n' for n in names), newline='\n')

    def run_install(self, **env):
        base = {k: v for k, v in os.environ.items() if not k.startswith(('PCBPILOT_', '_PCBPILOT_'))}
        base.update(USERPROFILE=str(self.home), HOME=str(self.home), CODEX_HOME=str(self.codex), CLAUDE_CONFIG_DIR=str(self.claude),
                    PCBPILOT_VERSION='v1.4.2', PCBPILOT_INSTALL_DIR=str(self.root / 'bin'), PCBPILOT_INSTALL_SKILLS='codex,claude',
                    PCBPILOT_SIM_TOOLS='0', PCBPILOT_MCP='0', PCBPILOT_GITHUB_PROXY='off',
                    LOCALAPPDATA=str(self.home / 'AppData/Local'), **env)
        return subprocess.run([POWERSHELL, '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', str(self.script)],
                              env=base, cwd=self.root, capture_output=True, text=True, timeout=300)

    def old_install(self):
        for client in (self.codex, self.claude):
            for skill in ('pcbpilot', DEMO):
                path = client / 'skills' / skill
                path.mkdir(parents=True)
                (path / 'SKILL.md').write_text('old ' + skill)
                (path / '.version').write_text('1.4.1\n')

    def snapshot(self):
        """Every file under the client skill roots (the CLI and ~/.pcbpilot are not Skills)."""
        return {str(p.relative_to(self.home)): p.read_bytes()
                for client in (self.codex, self.claude) for p in client.rglob('*') if p.is_file()}

    def assert_failed_unchanged(self, result, before):
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.snapshot(), before)
        leftovers = [p for p in self.home.rglob('.easyeda-*')]
        self.assertEqual(leftovers, [], 'staging/backup dirs left behind')

    def test_every_skill_installed_and_retired_removed_only_when_owned(self):
        owned = self.codex / 'skills/pcbpilot-old'
        owned.mkdir(parents=True)
        (owned / '.version').write_text('1.4.1\n')
        user = self.claude / 'skills/pcbpilot-old'
        user.mkdir(parents=True)
        (user / 'SKILL.md').write_text('user content')
        self.package(retired=('pcbpilot-old',))
        result = self.run_install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        for client in (self.codex, self.claude):
            for skill in ('pcbpilot', DEMO):
                self.assertEqual((client / 'skills' / skill / '.version').read_text(), '1.4.2\n')
            self.assertTrue((client / 'skills/pcbpilot/.public-skills.json').is_file())
        self.assertFalse(owned.exists())
        self.assertEqual((user / 'SKILL.md').read_text(), 'user content')

    def test_legacy_single_skill_archive_installs_router_only(self):
        self.package(skills=('pcbpilot',), manifest=False)
        result = self.run_install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(sorted(p.name for p in (self.codex / 'skills').iterdir()), ['pcbpilot'])

    def test_adversarial_archives_rejected_before_any_write(self):
        self.old_install()
        before = self.snapshot()
        cases = {
            'parent path': dict(entries=[('pcbpilot/../../evil.txt', 'file', b'x')]),
            'absolute path': dict(entries=[('/evil.txt', 'file', b'x')]),
            'drive path': dict(entries=[('pcbpilot/C:/evil.txt', 'file', b'x')]),
            'symlink': dict(entries=[('pcbpilot/link', 'symlink', b'')]),
            'hardlink': dict(entries=[('pcbpilot/hard', 'hardlink', b'')]),
            'duplicate': dict(entries=[('pcbpilot/SKILL.md', 'file', b'again')]),
            'case duplicate': dict(entries=[('pcbpilot/skill.md', 'file', b'again')]),
            'unlisted root': dict(entries=[('pcbpilot-other/SKILL.md', 'file', b'x')]),
            'root file': dict(entries=[('stray.json', 'file', b'{}')]),
            'malformed manifest': dict(manifest=b'{"schemaVersion": 1, "router": "pcbpilot", "skills": ['),
            'wrong schema': dict(manifest=b'{"schemaVersion": 2, "router": "pcbpilot", "skills": ["pcbpilot"]}'),
            'duplicate names': dict(skills=('pcbpilot', DEMO), manifest=b'{"schemaVersion": 1, "router": "pcbpilot", "skills": ["pcbpilot", "pcbpilot-demo", "pcbpilot-demo"]}'),
            'bad name': dict(skills=('pcbpilot', 'notes')),
            'focused version': dict(demo_version='1.4.1'),
        }
        for name, kwargs in cases.items():
            with self.subTest(case=name):
                self.package(**kwargs)
                self.assert_failed_unchanged(self.run_install(), before)
                self.assertFalse((self.root / 'evil.txt').exists())

    def test_unowned_same_named_dir_is_not_replaced_without_adopt(self):
        self.old_install()
        user = self.claude / 'skills' / DEMO
        (user / '.version').unlink()
        (user / 'notes.md').write_text('mine')
        result = self.run_install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('pcbpilot did not install it', result.stdout + result.stderr)
        self.assertEqual((user / 'notes.md').read_text(), 'mine')
        self.assertEqual((self.claude / 'skills/pcbpilot/.version').read_text(), '1.4.2\n')
        result = self.run_install(PCBPILOT_SKILL_ADOPT='1')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((user / '.version').read_text(), '1.4.2\n')

    def test_a_failed_switch_at_any_position_restores_every_skill(self):
        self.old_install()
        retired = self.codex / 'skills/pcbpilot-old'
        retired.mkdir(parents=True)
        (retired / '.version').write_text('1.4.1\n')
        self.package(retired=('pcbpilot-old',))
        before = self.snapshot()
        # 2 clients x 2 Skills (backup + switch each) + 1 retired removal = 9 switches.
        for position in range(1, 10):
            with self.subTest(position=position):
                result = self.run_install(_PCBPILOT_TEST_FAIL_SWAP=str(position))
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn('every Skill was restored', result.stdout + result.stderr)
                self.assertEqual(self.snapshot(), before)
                self.assertEqual([p for p in self.home.rglob('.easyeda-*')], [])
        result = self.run_install(_PCBPILOT_TEST_FAIL_SWAP='10')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == '__main__':
    unittest.main()
