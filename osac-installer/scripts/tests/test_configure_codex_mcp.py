"""Offline tests for the opt-in local Codex MCP setup."""

from __future__ import annotations

import importlib.util
import os
import stat
import subprocess
import tempfile
import tomllib
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "configure_codex_mcp_config", SCRIPTS / "configure_codex_mcp_config.py"
)
assert SPEC and SPEC.loader
CONFIG = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CONFIG)
URL = "https://mcp.osac.localhost:8443"


class CodexConfigTests(unittest.TestCase):
    def test_new_config_has_registered_callback_and_writes_policy(self) -> None:
        text = CONFIG.merge_config("", URL)
        osac = tomllib.loads(text)["mcp_servers"]["osac"]
        self.assertEqual(osac["url"], URL)
        self.assertEqual(osac["default_tools_approval_mode"], "writes")
        self.assertEqual(osac["oauth"]["client_id"], "osac-mcp-client")
        self.assertEqual(osac["oauth"]["callback_url"], CONFIG.CALLBACK_URL)
        self.assertEqual(osac["oauth"]["callback_port"], 6274)

    def test_preserves_unrelated_config_and_existing_approval_preference(self) -> None:
        original = (
            '# user note\nmodel = "gpt-6-sol"\n\n'
            '[mcp_servers.other]\nurl = "https://example.test/mcp"\n\n'
            '[mcp_servers.osac]\nurl = "https://mcp.osac.localhost:8443"\n'
            'default_tools_approval_mode = "prompt"\n'
            '[mcp_servers.osac.oauth]\nclient_id = "old-client"\n'
            'callback_url = "http://127.0.0.1/callback"\n'
        )
        updated = CONFIG.merge_config(original, URL)
        parsed = tomllib.loads(updated)
        self.assertIn('# user note\nmodel = "gpt-6-sol"', updated)
        self.assertEqual(parsed["mcp_servers"]["other"]["url"], "https://example.test/mcp")
        self.assertEqual(parsed["mcp_servers"]["osac"]["default_tools_approval_mode"], "prompt")
        self.assertEqual(parsed["mcp_servers"]["osac"]["oauth"]["callback_port"], 6274)
        self.assertEqual(CONFIG.merge_config(updated, URL), updated)

    def test_refuses_to_replace_different_osac_server(self) -> None:
        with self.assertRaisesRegex(ValueError, "different MCP server"):
            CONFIG.merge_config('[mcp_servers.osac]\nurl = "https://other.example/mcp"\n', URL)

    def test_refuses_invalid_or_unsupported_toml(self) -> None:
        with self.assertRaises(tomllib.TOMLDecodeError):
            CONFIG.merge_config("[broken\n", URL)
        with self.assertRaisesRegex(ValueError, "unsupported osac table syntax"):
            CONFIG.merge_config('[mcp_servers."osac"]\nurl = "https://mcp.osac.localhost:8443"\n', URL)

    def test_update_is_atomic_idempotent_and_backs_up_existing_config(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "config.toml"
            path.write_text('model = "gpt-6-sol"\n', encoding="utf-8")
            backup = CONFIG.update_config(path, URL)
            self.assertIsNotNone(backup)
            self.assertEqual(backup.read_text(encoding="utf-8"), 'model = "gpt-6-sol"\n')
            self.assertEqual(stat.S_IMODE(backup.stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertIsNone(CONFIG.update_config(path, URL))
            self.assertEqual(len(list(Path(directory).glob("*.osac-backup-*"))), 1)

    def test_refuses_to_replace_symlink(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "target.toml"
            target.write_text("", encoding="utf-8")
            link = Path(directory) / "config.toml"
            link.symlink_to(target)
            with self.assertRaisesRegex(ValueError, "symlink"):
                CONFIG.update_config(link, URL)


class SetupScriptTests(unittest.TestCase):
    def run_setup(self, curl_succeeds: bool) -> tuple[subprocess.CompletedProcess[str], Path, Path, Path]:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        fake_bin = root / "bin"
        fake_bin.mkdir()
        commands = {
            "kubectl": '#!/bin/sh\ntest "$1" = "-n" && test "$2" = "osac" || exit 2\nprintf "%s\\n" "-----BEGIN CERTIFICATE-----" "FAKE" "-----END CERTIFICATE-----"\n',
            "openssl": "#!/bin/sh\nexit 0\n",
            "curl": f"#!/bin/sh\nexit {0 if curl_succeeds else 22}\n",
            "codex": '#!/bin/sh\ntest "$1" = "mcp" && test "$2" = "login" && test "$3" = "osac" || exit 2\nprintf "%s" "$CODEX_CA_CERTIFICATE" > "$OSAC_TEST_CODEX_MARKER"\n',
            "launchctl": "#!/bin/sh\nexit 0\n",
        }
        for name, contents in commands.items():
            command = fake_bin / name
            command.write_text(contents, encoding="utf-8")
            command.chmod(0o700)
        config = root / "codex" / "config.toml"
        ca_path = root / "certs" / "kind-ca.pem"
        marker = root / "login-ca-path"
        env = os.environ.copy()
        env.update(
            PATH=f"{fake_bin}{os.pathsep}{env['PATH']}",
            OSAC_CODEX_CONFIG_PATH=str(config),
            OSAC_MCP_CA_DIR=str(ca_path.parent),
            OSAC_TEST_CODEX_MARKER=str(marker),
        )
        result = subprocess.run(
            [
                "make",
                "-C",
                str(SCRIPTS.parent),
                "setup-mcp-demo-codex",
                "PLATFORM=kind",
                "PROFILE=dev-full",
                "NS=osac",
            ],
            env=env,
            text=True,
            capture_output=True,
            check=False,
        )
        return result, config, ca_path, marker

    def test_setup_configures_ca_and_starts_login(self) -> None:
        result, config, ca_path, marker = self.run_setup(curl_succeeds=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(ca_path.exists())
        self.assertEqual(marker.read_text(encoding="utf-8"), str(ca_path))
        self.assertEqual(tomllib.loads(config.read_text())["mcp_servers"]["osac"]["oauth"]["callback_port"], 6274)

    def test_failed_metadata_check_does_not_change_config_or_ca(self) -> None:
        result, config, ca_path, marker = self.run_setup(curl_succeeds=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(config.exists())
        self.assertFalse(ca_path.exists())
        self.assertFalse(marker.exists())


if __name__ == "__main__":
    unittest.main()
