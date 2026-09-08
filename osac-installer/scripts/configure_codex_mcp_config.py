#!/usr/bin/env python3
"""Merge the local OSAC MCP demo into Codex's TOML config without rewriting other settings."""

from __future__ import annotations

import argparse
import json
import os
import re
import tempfile
import tomllib
from datetime import datetime, timezone
from pathlib import Path

SERVER_SECTION = "mcp_servers.osac"
OAUTH_SECTION = "mcp_servers.osac.oauth"
CALLBACK_URL = "http://127.0.0.1:6274/oauth/callback"


def find_section(lines: list[str], name: str) -> tuple[int, int] | None:
    header = re.compile(rf"^\s*\[{re.escape(name)}\]\s*(?:#.*)?$")
    for start, line in enumerate(lines):
        if header.fullmatch(line.rstrip("\r\n")):
            end = start + 1
            while end < len(lines) and not re.match(r"^\s*\[", lines[end]):
                end += 1
            return start, end
    return None


def upsert_section(
    lines: list[str], name: str, values: dict[str, str | int], preserve: set[str]
) -> None:
    section = find_section(lines, name)
    if section is None:
        if lines and not lines[-1].endswith("\n"):
            lines[-1] += "\n"
        if lines and lines[-1].strip():
            lines.append("\n")
        lines.append(f"[{name}]\n")
        lines.extend(f"{key} = {json.dumps(value)}\n" for key, value in values.items())
        return

    start, end = section
    for key, value in values.items():
        assignment = re.compile(rf"^(\s*){re.escape(key)}\s*=")
        found = next((index for index in range(start + 1, end) if assignment.match(lines[index])), None)
        if found is not None:
            if key not in preserve:
                indent = assignment.match(lines[found]).group(1)
                lines[found] = f"{indent}{key} = {json.dumps(value)}\n"
            continue
        if end and not lines[end - 1].endswith("\n"):
            lines[end - 1] += "\n"
        lines.insert(end, f"{key} = {json.dumps(value)}\n")
        end += 1


def merge_config(original: str, url: str) -> str:
    parsed = tomllib.loads(original)
    servers = parsed.get("mcp_servers", {})
    if not isinstance(servers, dict):
        raise ValueError("mcp_servers must be a TOML table")
    server = servers.get("osac", {})
    if not isinstance(server, dict):
        raise ValueError("mcp_servers.osac must be a TOML table")
    if server.get("url") not in (None, url) or "command" in server:
        raise ValueError("osac already points to a different MCP server; refusing to replace it")

    lines = original.splitlines(keepends=True)
    if server and find_section(lines, SERVER_SECTION) is None:
        raise ValueError("unsupported osac table syntax; edit that table manually")
    if "oauth" in server and find_section(lines, OAUTH_SECTION) is None:
        raise ValueError("unsupported osac OAuth table syntax; edit that table manually")

    server_values = {
        "url": url,
        "startup_timeout_sec": 20,
        "tool_timeout_sec": 120,
        "default_tools_approval_mode": "writes",
    }
    oauth_values = {
        "client_id": "osac-mcp-client",
        "callback_url": CALLBACK_URL,
        "callback_port": 6274,
    }
    upsert_section(lines, SERVER_SECTION, server_values, preserve=set(server_values))
    existing_oauth = server.get("oauth", {})
    preserve_oauth = {
        key for key, value in oauth_values.items() if existing_oauth.get(key) == value
    }
    upsert_section(lines, OAUTH_SECTION, oauth_values, preserve=preserve_oauth)
    updated = "".join(lines)
    result = tomllib.loads(updated)["mcp_servers"]["osac"]
    if result["url"] != url or any(result["oauth"][key] != value for key, value in oauth_values.items()):
        raise ValueError("merged Codex configuration did not contain the expected OSAC settings")
    return updated


def update_config(path: Path, url: str) -> Path | None:
    if path.is_symlink():
        raise ValueError(f"refusing to replace symlink: {path}")
    if path.exists() and not path.is_file():
        raise ValueError(f"Codex configuration is not a regular file: {path}")
    original = path.read_text(encoding="utf-8") if path.exists() else ""
    updated = merge_config(original, url)
    if updated == original:
        return None

    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    backup = None
    if path.exists():
        stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        backup = path.with_name(f"{path.name}.osac-backup-{stamp}-{os.getpid()}")
        descriptor = os.open(backup, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "wb") as target:
            target.write(original.encode("utf-8"))

    descriptor, temporary = tempfile.mkstemp(prefix=".config.toml.osac.", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as target:
            target.write(updated)
            target.flush()
            os.fsync(target.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    return backup


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True, type=Path)
    parser.add_argument("--url", required=True)
    args = parser.parse_args()
    try:
        backup = update_config(args.config, args.url)
    except (OSError, UnicodeError, tomllib.TOMLDecodeError, ValueError) as error:
        parser.exit(1, f"ERROR: unable to configure Codex MCP: {error}\n")
    print(f"Codex MCP configured in {args.config}")
    if backup is not None:
        print(f"Previous configuration saved in {backup}")


if __name__ == "__main__":
    main()
