"""Check release guards without building or publishing Docker images."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class PublishTest(unittest.TestCase):
    def run_case(self, **variables):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            version = root / 'golang-backend/internal/app/version/version.go'
            version.parent.mkdir(parents=True)
            version.write_text('var serverVersion = "2.0.0.30"\n')
            script = root / 'publish.sh'
            script.write_bytes((ROOT / 'publish.sh').read_bytes())
            tools = root / 'tools'
            tools.mkdir()
            git = tools / 'git'
            git.write_text('''#!/bin/bash
case "$1" in
  status) printf '%s' "${DIRTY:-}" ;;
  rev-parse)
    case "$2" in
      HEAD) echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ;;
      --short=12) echo aaaaaaaaaaaa ;;
      *) echo "${TAG_SHA:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" ;;
    esac ;;
  show) printf 'var versionBase = "%s"\\n' "${AGENT_VERSION:-2.0.0.30}" ;;
  ls-remote) printf '%s\\trefs/tags/v2.0.0.30^{}\\n' "${REMOTE_SHA:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" ;;
  archive) printf 'ONLY_TRACKED_ARCHIVE' ;;
  *) exit 1 ;;
esac
''')
            docker = tools / 'docker'
            docker.write_text('''#!/bin/bash
printf '%s\\n' "$*" >> "$CALLS"
if [[ "$2" == build ]]; then
  input=$(cat)
  [[ "$input" == ONLY_TRACKED_ARCHIVE ]] || exit 20
  exit "${BUILD_EXIT:-0}"
fi
''')
            git.chmod(0o700)
            docker.chmod(0o700)
            calls = root / 'calls'
            result = subprocess.run(['bash', str(script)], text=True, capture_output=True,
                env={**os.environ, 'PATH': str(tools) + ':' + os.environ['PATH'], 'CALLS': str(calls), **variables})
            return result, calls.read_text() if calls.exists() else ''

    def test_only_tagged_archive_is_built_and_latest_is_not_default(self):
        result, calls = self.run_case()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('--target final', calls)
        self.assertIn('--push -\n', calls)
        self.assertNotIn('latest', calls)
        self.assertIn('imagetools inspect', calls)

    def test_rejects_dirty_or_wrong_tag_or_wrong_agent_before_docker(self):
        for variables in ({'DIRTY': ' M install.sh'}, {'TAG_SHA': 'wrong'},
                          {'REMOTE_SHA': 'wrong'}, {'AGENT_VERSION': '2.0.0.29'}):
            with self.subTest(variables=variables):
                result, calls = self.run_case(**variables)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(calls, '')

    def test_build_failure_never_promotes_latest(self):
        result, calls = self.run_case(BUILD_EXIT='1', PROMOTE_LATEST='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('imagetools', calls)
        self.assertNotIn('latest', calls)


if __name__ == '__main__':
    unittest.main()
