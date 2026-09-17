#!/usr/bin/env bash
set -euo pipefail

image="${1:-}"

if [[ -z "${image}" ]]; then
    printf 'ERROR: MCP_DEMO_IMAGE is required\n' >&2
    exit 1
fi

case "${image}" in
    localhost/*|127.0.0.1/*|localhost:*|127.0.0.1:*)
        printf 'ERROR: MCP_DEMO_IMAGE must be a pullable registry reference, not %s\n' "${image}" >&2
        exit 1
        ;;
esac

if [[ ! "${image}" =~ ^[a-z0-9][a-z0-9.-]*(:[0-9]+)?/[a-z0-9][a-z0-9._/-]*:[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
    printf 'ERROR: MCP_DEMO_IMAGE must be a pullable registry reference with an explicit tag\n' >&2
    exit 1
fi
