"""Exercise installer arguments and downloads without installing services."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = (ROOT / "install.sh").read_text()
# Keep all real startup/argument handling, but never invoke the installer menu.
assert SCRIPT.endswith("# 执行主函数\nmain\n")
SCRIPT = SCRIPT.removesuffix("main\n")
DEFAULT = "https://proxy.yjkj02.com/"
IPV6 = "http://[240b:4000:93:de01:ffff:c725:3c65:47ff]:5000/"
GITHUB = "https://github.com/NiuStar/network-panel/releases/latest/download/flux-agent-linux-amd64"
PROBE = '''
printf 'STATE|%s|%s|%s|%s|%s\\n' "$PROXY_PREFIX" "$FORCE_PROXY_GITHUB" "$SOURCE_MODE" "$SERVER_ADDR" "$SECRET"
expand_github_urls "$TEST_GITHUB"
while read -r candidate; do
  expand_github_urls "$candidate"
done < <(build_candidate_urls flux-agent flux-agent-linux-amd64)
'''


def run_script(*args, probe=PROBE, extra_env=None):
    return subprocess.run(
        ["bash", "-c", 'source /dev/stdin "$@"', "install.sh", *args],
        input=SCRIPT + probe,
        text=True,
        capture_output=True,
        timeout=15,
        env={**os.environ, "TEST_GITHUB": GITHUB, **(extra_env or {})},
        cwd=ROOT,
    )


class InstallProxyTest(unittest.TestCase):
    def state(self, *args):
        result = run_script(*args)
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        state = next(line for line in result.stdout.splitlines() if line.startswith("STATE|"))
        return state.split("|")[1:], result.stdout.splitlines()

    def test_default_keeps_direct_downloads(self):
        state, lines = self.state()
        self.assertEqual(state[:3], ["", "0", "global"])
        self.assertIn(GITHUB, lines)
        self.assertNotIn(DEFAULT + GITHUB, lines)

    def test_ipv4_default_and_alias(self):
        for args in (("-p", "4"), ("-p4",), ("-4",)):
            with self.subTest(args=args):
                state, lines = self.state(*args)
                self.assertEqual(state[:2], [DEFAULT, "1"])
                self.assertIn(DEFAULT + GITHUB, lines)
                self.assertNotIn(GITHUB, lines)

    def test_cn_default(self):
        state, lines = self.state("-m", "cn")
        self.assertEqual(state[:3], [DEFAULT, "0", "cn"])
        self.assertIn(DEFAULT + GITHUB, lines)
        self.assertIn(GITHUB, lines)

    def test_ipv6_compatibility(self):
        for args in (("-p", "6"), ("-6",)):
            with self.subTest(args=args):
                state, lines = self.state(*args)
                self.assertEqual(state[:2], [IPV6, "1"])
                self.assertIn(IPV6 + GITHUB, lines)
                self.assertNotIn(GITHUB, lines)

    def test_custom_proxy_overrides_defaults_in_any_order(self):
        for args in (
            ("--proxy", "https://mirror.example"),
            ("--proxy=https://mirror.example/",),
            ("-p", "4", "--proxy", "https://mirror.example"),
            ("--proxy", "https://mirror.example", "-p4"),
            ("--proxy", "https://mirror.example", "-p6", "-mcn"),
        ):
            with self.subTest(args=args):
                state, lines = self.state(*args)
                self.assertEqual(state[:2], ["https://mirror.example/", "1"])
                self.assertIn("https://mirror.example/" + GITHUB, lines)
                self.assertNotIn(GITHUB, lines)
                self.assertNotIn(DEFAULT + GITHUB, lines)
                self.assertNotIn(IPV6 + GITHUB, lines)

    def test_path_prefix_http_and_ipv6(self):
        for url in ("http://localhost:8080/cache", "https://[::1]:8443/cache/"):
            with self.subTest(url=url):
                state, lines = self.state("--proxy", url)
                prefix = url.rstrip("/") + "/"
                self.assertEqual(state[:2], [prefix, "1"])
                self.assertIn(prefix + GITHUB, lines)

    def test_existing_short_options(self):
        state, _ = self.state("-a", "panel.example:8080", "-ssecret", "-mgithub", "--proxy", "https://mirror.example")
        self.assertEqual(state[2:], ["github", "panel.example:8080", "secret"])

    def test_rejects_bad_arguments_before_installation(self):
        for args in (
            ("--proxy",), ("--proxy=",), ("--proxy", "-4"),
            ("--proxy", "mirror.example"), ("--proxy", "ftp://mirror.example"),
            ("--proxy", "https://"), ("--proxy", "https:///cache"),
            ("--proxy", "https://mirror.example/with space"),
            ("--proxy", "https://mirror.example/?q=1"),
            ("--proxy", "https://mirror.example/#fragment"),
            ("--unknown",), ("-p", "5"), ("-p",), ("-a",),
            ("unexpected",),
        ):
            with self.subTest(args=args):
                result = run_script(*args)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("STATE|", result.stdout)

    def test_help_does_not_enter_menu(self):
        for flag in ("--help", "-h"):
            result = run_script(flag)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("--proxy", result.stdout)
            self.assertIn(DEFAULT, result.stdout)
            self.assertNotIn("STATE|", result.stdout)

    def test_only_github_urls_are_prefixed(self):
        url = "https://panel.example/flux-agent/flux-agent-linux-amd64"
        result = run_script("--proxy", "https://mirror.example", probe=f'expand_github_urls "{url}"\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines()[-1], url)

    def test_real_curl_uses_custom_prefix_for_api_and_binary(self):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                requests.append(self.path)
                self.send_response(200)
                self.end_headers()
                if self.path.endswith("/releases/latest"):
                    self.wfile.write(b'{"tag_name":"v9.9.9-test"}')
                else:
                    self.wfile.write(b"installer-proxy-test\n")

            def log_message(self, *_args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as directory:
                target = Path(directory) / "download"
                result = run_script(
                    "--proxy", f"http://127.0.0.1:{server.server_port}",
                    probe='while read -r api; do curl_text "$api"; done < <(expand_github_urls "$BASE_GOST_REPO_API")\ndownload_from_urls "$TEST_TARGET" "$TEST_GITHUB"\n',
                    extra_env={"TEST_TARGET": str(target), "NO_PROXY": "127.0.0.1", "no_proxy": "127.0.0.1"},
                )
                self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
                self.assertIn("v9.9.9-test", result.stdout)
                self.assertEqual(target.read_bytes(), b"installer-proxy-test\n")
                self.assertEqual(requests, [
                    "/https://api.github.com/repos/go-gost/gost/releases/latest",
                    "/" + GITHUB,
                ])
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    unittest.main()
