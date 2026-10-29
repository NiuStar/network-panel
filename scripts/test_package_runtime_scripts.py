"""Exercise runtime ZIP packaging against real temporary Git repositories."""
import importlib.util
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest
from unittest.mock import patch
import zipfile

ROOT = Path(__file__).resolve().parents[1]
PACKAGER = ROOT / 'scripts/package_runtime_scripts.py'


class RuntimeScriptsTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.git('init', '-q')
        self.write('install.sh', b'#!/bin/sh\necho install\n')
        self.write('easytier/install.sh', b'#!/bin/sh\necho easytier\n')
        self.write('easytier/nested/helper.sh', b'#!/bin/sh\ntrue\n')
        self.write('easytier/default.conf', b'[network]\n')
        self.write('easytier/docker-compose.yaml', b'services: {}\n')
        self.git('add', 'install.sh', 'easytier')
        self.output = self.root / 'assets/panel-runtime-scripts.zip'

    def git(self, *args):
        return subprocess.run(['git', '-C', str(self.root), *args],
                              check=True, capture_output=True)

    def write(self, name, content):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        return path

    def package(self):
        return subprocess.run([sys.executable, str(PACKAGER), '--root', str(self.root),
                               '--output', str(self.output)],
                              capture_output=True, text=True)

    def test_packages_only_tracked_runtime_files_with_fixed_metadata(self):
        self.write('.env', b'SECRET=outside\n')
        self.write('easytier/.env', b'SECRET=inside\n')
        self.write('easytier/state.db', b'private database')
        self.write('easytier/untracked.sh', b'private script')
        self.write('unrelated.txt', b'not runtime')
        self.git('add', '.env', 'unrelated.txt')
        result = self.package()
        self.assertEqual(result.returncode, 0, result.stderr)
        expected = sorted(['install.sh', 'easytier/install.sh',
                           'easytier/nested/helper.sh', 'easytier/default.conf',
                           'easytier/docker-compose.yaml'])
        with zipfile.ZipFile(self.output) as archive:
            self.assertEqual(archive.namelist(), expected)
            for info in archive.infolist():
                self.assertEqual(archive.read(info), (self.root / info.filename).read_bytes())
                self.assertEqual(info.date_time, (1980, 1, 1, 0, 0, 0))
                self.assertEqual(info.create_system, 3)
                self.assertTrue(stat.S_ISREG(info.external_attr >> 16))
                self.assertEqual(stat.S_IMODE(info.external_attr >> 16),
                                 0o755 if info.filename.endswith('.sh') else 0o644)
                self.assertEqual(info.compress_type, zipfile.ZIP_STORED)
                self.assertFalse(info.is_dir())
                self.assertEqual(info.extra, b'')
                self.assertEqual(info.comment, b'')

    def test_rejects_tracked_secrets_and_databases_without_publishing_archive(self):
        for name in ('easytier/.env', 'easytier/.env.production',
                     'easytier/private.env', 'easytier/panel.db',
                     'easytier/panel.db-wal', 'easytier/panel.sqlite3',
                     'easytier/panel.sqlite-journal'):
            with self.subTest(name=name):
                path = self.write(name, b'private state')
                self.git('add', name)
                result = self.package()
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn('forbidden runtime file', result.stderr)
                self.assertFalse(self.output.exists())
                self.git('rm', '--cached', '--', name)
                path.unlink()

    def test_rejects_symlink_files_and_parent_directories(self):
        target = self.write('outside.txt', b'not a runtime file')
        path = self.root / 'easytier/linked.sh'
        path.symlink_to(target)
        self.git('add', 'easytier/linked.sh')
        result = self.package()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.output.exists())
        # A staged symlink must remain forbidden even if replaced by a regular file.
        path.unlink()
        path.write_bytes(b'replacement')
        result = self.package()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.output.exists())
        self.git('rm', '--cached', '-f', '--', 'easytier/linked.sh')
        path.unlink()
        # Also reject worktree symlinks for entries staged as regular files.
        (self.root / 'easytier/default.conf').unlink()
        (self.root / 'easytier/default.conf').symlink_to(target)
        result = self.package()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.output.exists())
        (self.root / 'easytier/default.conf').unlink()
        self.write('easytier/default.conf', b'[network]\n')
        outside = self.root / 'outside'
        (self.root / 'easytier/nested').rename(outside)
        (self.root / 'easytier/nested').symlink_to(outside, target_is_directory=True)
        result = self.package()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.output.exists())

    def test_rejects_unsafe_archive_names(self):
        for name in ('easytier/back\\slash.sh', 'easytier/line\nbreak.sh'):
            with self.subTest(name=name):
                path = self.write(name, b'unsafe name')
                self.git('add', '--', name)
                result = self.package()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('unsafe runtime path', result.stderr)
                self.assertFalse(self.output.exists())
                self.git('rm', '--cached', '--', name)
                path.unlink()

    def test_requires_tracked_install_script(self):
        self.git('rm', '--cached', '--', 'install.sh')
        result = self.package()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('tracked install.sh is required', result.stderr)
        self.assertFalse(self.output.exists())

    def test_reproducible_across_worktree_metadata_and_untracked_changes(self):
        first = self.package()
        self.assertEqual(first.returncode, 0, first.stderr)
        original = self.output.read_bytes()
        for path in [self.root / 'install.sh', *(self.root / 'easytier').rglob('*')]:
            if path.is_file():
                path.chmod(0o777)
                os.utime(path, (1800000000, 1800000000))
        self.write('easytier/local-only.conf', b'not tracked')
        second = self.package()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(self.output.read_bytes(), original)

    def test_missing_or_nonregular_files_never_replace_existing_asset(self):
        self.output.parent.mkdir()
        self.output.write_bytes(b'existing verified asset')
        path = self.root / 'easytier/default.conf'
        path.unlink()
        for kind in ('missing', 'directory', 'fifo'):
            with self.subTest(kind=kind):
                if kind == 'directory':
                    path.mkdir()
                elif kind == 'fifo':
                    path.rmdir()
                    os.mkfifo(path)
                result = self.package()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.output.read_bytes(), b'existing verified asset')

    def test_archive_write_failure_preserves_existing_asset(self):
        spec = importlib.util.spec_from_file_location('runtime_packager', PACKAGER)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.output.parent.mkdir()
        self.output.write_bytes(b'existing verified asset')
        with patch.object(zipfile.ZipFile, 'writestr', side_effect=OSError('disk full')):
            with self.assertRaisesRegex(OSError, 'disk full'):
                module.package(self.root, self.output)
        self.assertEqual(self.output.read_bytes(), b'existing verified asset')
        self.assertEqual(list(self.output.parent.iterdir()), [self.output])

    def test_requires_tracked_easytier_files(self):
        self.git('rm', '-r', '--cached', '--', 'easytier')
        result = self.package()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('tracked easytier files are required', result.stderr)
        self.assertFalse(self.output.exists())

    def test_output_cannot_overwrite_a_runtime_source(self):
        self.output = self.root / 'install.sh'
        original = self.output.read_bytes()
        result = self.package()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('output overlaps runtime sources', result.stderr)
        self.assertEqual(self.output.read_bytes(), original)


class ReleaseWorkflowTest(unittest.TestCase):
    def test_actual_packaging_commands_keep_compat_install_identical(self):
        workflow = (ROOT / '.github/workflows/release.yml').read_text()
        commands = textwrap.dedent(workflow.split(
            '      - name: Package scripts, provenance and checksums', 1
        )[1].split('        run: |\n', 1)[1].split('          git archive', 1)[0])
        # Execute the actual local packaging prefix, never the release/publish steps.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'scripts').mkdir()
            (root / 'release-assets').mkdir()
            subprocess.run(['git', 'init', '-q', str(root)], check=True, capture_output=True)
            for name in ('install.sh', 'panel_install.sh', 'scripts/install_server.sh',
                         'scripts/package_runtime_scripts.py'):
                shutil.copyfile(ROOT / name, root / name)
            tracked = subprocess.run(['git', '-C', str(ROOT), 'ls-files', '-z', '--', 'easytier'],
                                     check=True, capture_output=True).stdout
            for raw_name in tracked.split(b'\0'):
                if raw_name:
                    name = raw_name.decode('utf-8')
                    (root / name).parent.mkdir(parents=True, exist_ok=True)
                    shutil.copyfile(ROOT / name, root / name)
            subprocess.run(['git', '-C', str(root), 'add', 'install.sh', 'easytier'],
                           check=True, capture_output=True)
            result = subprocess.run(['bash', '-c', commands], cwd=root,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            with zipfile.ZipFile(root / 'release-assets/panel-runtime-scripts.zip') as archive:
                self.assertEqual(archive.read('install.sh'),
                                 (root / 'release-assets/install.sh').read_bytes())
            checksum_commands = '\n'.join(line.strip() for line in workflow.splitlines()
                                          if 'sha256sum *' in line or line.strip().startswith('mv SHA256SUMS'))
            result = subprocess.run(['bash', '-ec', checksum_commands], cwd=root,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            sums = (root / 'release-assets/SHA256SUMS').read_text()
            self.assertIn('  panel-runtime-scripts.zip\n', sums)
            self.assertIn('  install.sh\n', sums)
            result = subprocess.run(['sha256sum', '-c', 'SHA256SUMS'],
                                    cwd=root / 'release-assets', capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_platform_inventory_and_tag_only_trigger_are_preserved(self):
        workflow = (ROOT / '.github/workflows/release.yml').read_text()
        triggers = workflow.split('permissions:', 1)[0]
        self.assertIn("tags:\n      - 'v2.*'", triggers)
        self.assertNotIn('workflow_dispatch', triggers)
        self.assertNotIn('branches:', triggers)
        matrix = workflow.split('        include:', 1)[1].split('    steps:', 1)[0]
        agents = re.findall(r'^            agents: (.*)$', matrix, re.MULTILINE)
        servers = re.findall(r'^            servers: (.*)$', matrix, re.MULTILINE)
        agents = [target for row in agents for target in row.split()]
        servers = [target for row in servers if row != "''" for target in row.split()]
        expected_servers = {'linux_amd64', 'linux_amd64v3', 'linux_arm64', 'linux_armv7',
                            'linux_386', 'linux_riscv64', 'linux_s390x', 'linux_loong64'}
        expected_agents = expected_servers | {
            'darwin_arm64', 'darwin_amd64', 'freebsd_386', 'freebsd_amd64',
            'linux_armv5', 'linux_armv6', 'linux_mips64le_hardfloat',
            'linux_mips64_hardfloat', 'linux_mipsle_hardfloat', 'linux_mipsle_softfloat',
            'linux_mips_hardfloat', 'linux_mips_softfloat', 'windows_386',
            'windows_amd64', 'windows_amd64v3', 'windows_arm64'}
        self.assertEqual(set(servers), expected_servers)
        self.assertEqual(len(servers), len(expected_servers))
        self.assertEqual(set(agents), expected_agents)
        self.assertEqual(len(agents), len(expected_agents))
        self.assertEqual(len(servers) + 2 * len(agents), 56)

    def test_release_target_is_the_build_info_commit(self):
        workflow = (ROOT / '.github/workflows/release.yml').read_text()
        publish = workflow.split('      - name: Create draft, upload and publish', 1)[1]
        self.assertIn('RELEASE_SHA: ${{ github.sha }}', publish)
        self.assertIn('gh release create "$RELEASE_TAG" --verify-tag --target "$RELEASE_SHA"', publish)

    def test_runtime_zip_is_verified_and_packaged_before_checksums(self):
        workflow = (ROOT / '.github/workflows/release.yml').read_text()
        verify = workflow.split('  verify:', 1)[1].split('  frontend:', 1)[0]
        self.assertIn('python3 scripts/test_package_runtime_scripts.py', verify)
        publish = workflow.split('  publish:', 1)[1]
        package = 'python3 scripts/package_runtime_scripts.py --output release-assets/panel-runtime-scripts.zip'
        self.assertIn(package, publish)
        self.assertLess(publish.index(package), publish.index('sha256sum *'))
        self.assertIn("assert len(binaries) == 56", publish)
        self.assertIn("{'tag': os.environ['RELEASE_TAG'], 'commit': os.environ['RELEASE_SHA']}", publish)


if __name__ == '__main__':
    unittest.main()
