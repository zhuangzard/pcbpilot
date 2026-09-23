"""Installer contract tests with local release fixtures; never use the network."""
import hashlib
import io
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[2]


@unittest.skipIf(os.name == 'nt', 'Bash installer is for macOS/Linux; Windows uses the native CLI')
class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='pcbpilot installer ')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.assets = self.root / 'assets'
        self.shims = self.root / 'shims'
        self.assets.mkdir()
        self.shims.mkdir()
        arch = {'x86_64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}[platform.machine().lower()]
        self.binary_name = f'pcbpilot_{platform.system().lower()}_{arch}'
        (self.assets / self.binary_name).write_text('#!/bin/sh\nprintf "pcbpilot v1.4.2\\n"\n')
        self.package()
        self.sums()
        curl = self.shims / 'curl'
        curl.write_text(f'#!{sys.executable}\n' + '''import os, pathlib, shutil, sys
args = sys.argv[1:]
url = next(a for a in args if a.startswith('https://'))
name = url.rsplit('/', 1)[-1]
if os.environ.get('FAIL_ASSET') == name: sys.exit(22)
if os.environ.get('FAIL_PRIMARY_ASSET') == name and url.startswith('https://github.com/'): sys.exit(18)
output = pathlib.Path(args[args.index('-o')+1])
source = pathlib.Path(os.environ['FIXTURE_ASSETS']) / name
if source.exists():
 shutil.copyfile(source, output)
 if '-w' in args: print('200', end='')
else:
 output.write_text('not found')
 if '-w' in args: print(os.environ.get('MISSING_STATUS', '404'), end='')
 else: sys.exit(22)
''')
        curl.chmod(0o755)
        self.env = {**os.environ, 'PATH': f'{self.shims}:/usr/bin:/bin',
                    'HOME': str(self.root),
                    'PCBPILOT_VERSION': 'v1.4.2', 'PCBPILOT_INSTALL_DIR': str(self.root / 'bin'),
                    'CODEX_HOME': str(self.root / 'codex config'),
                    'CLAUDE_CONFIG_DIR': str(self.root / 'claude config'),
                    'PCBPILOT_INSTALL_SKILLS': 'codex,claude', 'PCBPILOT_SKILL_PRESERVE': '0',
                    'FIXTURE_ASSETS': str(self.assets)}
        self.cli = self.root / 'bin/pcbpilot'
        self.skill = self.root / 'codex config/skills/pcbpilot'

    def package(self, version='1.4.2'):
        with tarfile.open(self.assets / 'skills.tar.gz', 'w:gz') as archive:
            for name, content in {'SKILL.md': f'---\nname: pcbpilot\nmetadata:\n  version: "{version}"\n---\n[Guide](references/guide.md)\n',
                                  'references/guide.md': 'New guide\n'}.items():
                data = content.encode()
                item = tarfile.TarInfo('pcbpilot/' + name)
                item.size = len(data)
                item.mode = 0o644
                archive.addfile(item, io.BytesIO(data))

    def sums(self):
        names = [self.binary_name, 'skills.tar.gz']
        (self.assets / 'checksums.txt').write_text(''.join(
            f'{hashlib.sha256((self.assets / n).read_bytes()).hexdigest()}  {n}\n' for n in names))

    def old_install(self):
        self.cli.parent.mkdir()
        self.cli.write_text('old binary')
        self.skill.mkdir(parents=True)
        (self.skill / 'SKILL.md').write_text('local old skill')
        (self.skill / '.version').write_text('1.4.1\n')
        (self.skill / 'retired.md').write_text('must disappear on normal upgrade')

    def run_install(self, **env):
        return subprocess.run(['bash', str(REPO / 'install.sh')], env={**self.env, **env},
                              text=True, capture_output=True, timeout=20, cwd=self.root)

    def assert_failed_unchanged(self, result):
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.cli.read_text(), 'old binary')
        self.assertEqual((self.skill / '.version').read_text(), '1.4.1\n')
        self.assertEqual((self.skill / 'SKILL.md').read_text(), 'local old skill')

    def test_fresh_clients_and_new_shell(self):
        result = self.run_install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        for client in ('codex', 'claude'):
            path = self.root / f'{client} config/skills/pcbpilot'
            self.assertEqual((path / '.version').read_text(), '1.4.2\n')
            self.assertTrue((path / 'references/guide.md').is_file())
        result = subprocess.run(['bash', '--noprofile', '--norc', '-c', 'command -v pcbpilot && pcbpilot --version'],
                                env={**self.env, 'PATH': f'{self.cli.parent}:/usr/bin:/bin'}, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), [str(self.cli), 'pcbpilot v1.4.2'])

    def test_shared_agents_skill_root(self):
        result = self.run_install(PCBPILOT_INSTALL_SKILLS='agents')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        path = self.root / '.agents/skills/pcbpilot'
        self.assertEqual((path / '.version').read_text(), '1.4.2\n')
        self.assertTrue((path / 'references/guide.md').is_file())

    def test_large_asset_falls_back_to_checksum_verified_proxy(self):
        result = self.run_install(FAIL_PRIMARY_ASSET=self.binary_name,
                                  PCBPILOT_GITHUB_PROXY='https://mirror.example/{url}')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('checksum-verified mirror', result.stdout)
        self.assertEqual(self.cli.read_text(), (self.assets / self.binary_name).read_text())

    def test_normal_upgrade_removes_retired_files(self):
        self.old_install()
        result = self.run_install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse((self.skill / 'retired.md').exists())
        self.assertEqual((self.skill / '.version').read_text(), '1.4.2\n')

    def test_preserve_keeps_content_and_old_marker(self):
        self.old_install()
        result = self.run_install(PCBPILOT_SKILL_PRESERVE='1')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.skill / '.version').read_text(), '1.4.1\n')
        self.assertEqual((self.skill / 'SKILL.md').read_text(), 'local old skill')
        self.assertTrue((self.skill / 'references/guide.md').exists())
        self.assertIn('mixed', result.stdout)

    def test_preserve_without_marker_stays_unknown(self):
        self.old_install()
        (self.skill / '.version').unlink()
        result = self.run_install(PCBPILOT_SKILL_PRESERVE='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.skill / '.version').exists())

    def test_skip_skill_does_not_download_skill(self):
        result = self.run_install(PCBPILOT_INSTALL_SKILLS='none', FAIL_ASSET='skills.tar.gz')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(self.skill.exists())

    def test_unknown_client_rejected_before_changes(self):
        self.old_install()
        self.assert_failed_unchanged(self.run_install(PCBPILOT_INSTALL_SKILLS='codex,typo'))

    def test_bad_download_or_checksum_keeps_old_install(self):
        self.old_install()
        for asset in (self.binary_name, 'skills.tar.gz'):
            with self.subTest(asset=asset, failure='download'):
                self.assert_failed_unchanged(self.run_install(FAIL_ASSET=asset))
            original = (self.assets / asset).read_bytes()
            (self.assets / asset).write_bytes(b'corrupt')
            with self.subTest(asset=asset, failure='checksum'):
                self.assert_failed_unchanged(self.run_install())
            (self.assets / asset).write_bytes(original)

    def test_missing_or_duplicate_checksum_rejected(self):
        self.old_install()
        sums = self.assets / 'checksums.txt'
        original = sums.read_text()
        sums.write_text(original.splitlines()[0] + '\n')
        self.assert_failed_unchanged(self.run_install())
        sums.write_text(original + original)
        self.assert_failed_unchanged(self.run_install())

    def test_old_release_404_fallback_and_server_error(self):
        (self.assets / 'checksums.txt').unlink()
        result = self.run_install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Old release', result.stdout)
        before = self.cli.read_bytes()
        result = self.run_install(MISSING_STATUS='503')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(before, self.cli.read_bytes())

    def test_wrong_binary_or_skill_version_rejected(self):
        self.old_install()
        path = self.assets / self.binary_name
        original = path.read_text()
        path.write_text(original.replace('1.4.2', '1.4.1'))
        self.sums()
        self.assert_failed_unchanged(self.run_install())
        path.write_text(original)
        self.package('1.4.1')
        self.sums()
        self.assert_failed_unchanged(self.run_install())

    def test_auto_empty_custom_configs_installs_both(self):
        result = self.run_install(PCBPILOT_INSTALL_SKILLS='auto')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(self.skill.exists())
        self.assertTrue((self.root / 'claude config/skills/pcbpilot/SKILL.md').exists())

    def test_relative_install_dir_rejected(self):
        result = self.run_install(PCBPILOT_INSTALL_DIR='relative')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.cli.exists())

    def test_shared_skill_symlink_survives_update(self):
        self.old_install()
        shared = self.root / 'shared Skill'
        self.skill.rename(shared)
        self.skill.symlink_to(shared, target_is_directory=True)
        result = self.run_install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(self.skill.is_symlink())
        self.assertEqual((shared / '.version').read_text(), '1.4.2\n')
        self.assertFalse((shared / 'retired.md').exists())

    def test_dangling_skill_symlink_refused_before_binary_change(self):
        self.old_install()
        shutil.rmtree(self.skill)
        self.skill.symlink_to(self.root / 'missing Skill', target_is_directory=True)
        result = self.run_install()
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(self.skill.is_symlink())
        self.assertEqual(self.cli.read_text(), 'old binary')


if __name__ == '__main__':
    unittest.main()
