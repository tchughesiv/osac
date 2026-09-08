#!/usr/bin/env bash

set -euo pipefail
umask 077

namespace="${1:?namespace required}"
mcp_url="${2:?MCP URL required}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ca_dir="${OSAC_MCP_CA_DIR:-${HOME}/.config/osac/certs}"
config_path="${OSAC_CODEX_CONFIG_PATH:-${HOME}/.codex/config.toml}"
ca_path="${ca_dir}/kind-ca.pem"

for tool in kubectl openssl curl python3 codex; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
        echo "ERROR: ${tool} is required for Codex MCP setup" >&2
        exit 1
    fi
done
if ! python3 -c 'import tomllib' >/dev/null 2>&1; then
    echo "ERROR: Python 3.11 or newer is required for Codex TOML configuration" >&2
    exit 1
fi

temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/osac-mcp-codex.XXXXXX")"
temp_ca="${temp_dir}/kind-ca.pem"
trap 'rm -f -- "${temp_ca}"; rmdir -- "${temp_dir}"' EXIT

if ! kubectl -n "${namespace}" get configmap ca-bundle \
    --request-timeout=10s -o 'jsonpath={.data.bundle\.pem}' > "${temp_ca}"; then
    echo "ERROR: could not read ca-bundle from namespace ${namespace}" >&2
    exit 1
fi
if [[ ! -s "${temp_ca}" ]] || ! openssl x509 -in "${temp_ca}" -noout >/dev/null; then
    echo "ERROR: ca-bundle in namespace ${namespace} did not contain a CA certificate" >&2
    exit 1
fi
curl --fail --silent --show-error --connect-timeout 5 --max-time 20 \
    --cacert "${temp_ca}" \
    --output /dev/null "${mcp_url}/.well-known/oauth-protected-resource"

mkdir -p "${ca_dir}"
install -m 600 "${temp_ca}" "${ca_path}"
python3 "${script_dir}/configure_codex_mcp_config.py" \
    --config "${config_path}" --url "${mcp_url}"

if [[ "$(uname -s)" == Darwin ]] && command -v launchctl >/dev/null 2>&1; then
    if launchctl setenv CODEX_CA_CERTIFICATE "${ca_path}"; then
        echo "Codex Desktop CA trust set for this macOS login session; restart Codex Desktop."
    else
        echo "WARNING: launchctl could not set Codex Desktop CA trust; see the runbook." >&2
    fi
fi

echo "Starting Codex browser login for the OSAC MCP server..."
CODEX_CA_CERTIFICATE="${ca_path}" codex mcp login osac
printf '\nCodex MCP setup complete. For a new terminal session, run:\n'
printf '  export CODEX_CA_CERTIFICATE=%q\n' "${ca_path}"
printf '  codex\n'
printf 'Then use /mcp to confirm osac is connected.\n'
