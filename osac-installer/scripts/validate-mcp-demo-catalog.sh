#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALLER_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
SEED_SCRIPT="${SCRIPT_DIR}/seed-mcp-demo-catalog.sh"
OSAC_CHART="${INSTALLER_DIR}/charts/osac"
KIND_VALUES="${INSTALLER_DIR}/values/dev/kind-instance.yaml"
KIND_INFRA_VALUES="${INSTALLER_DIR}/values/dev/kind-infra.yaml"

fail() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

[[ -x "${SEED_SCRIPT}" ]] || fail "missing executable MCP demo catalog seeder: ${SEED_SCRIPT}"
rg -F 'set_password "tenant1_user"' \
    "${INSTALLER_DIR}/charts/osac-infra/templates/keycloak/resources.yaml" >/dev/null || \
    fail "Kind dev fixtures must seed the tenant1_user password used by the MCP demo"
rg -F 'ensure_mcp_client()' \
    "${INSTALLER_DIR}/charts/osac-infra/templates/keycloak/resources.yaml" >/dev/null || \
    fail "Kind dev fixtures must reconcile the MCP OAuth client after a chart upgrade"
if rg -F 'set_password "user"' \
    "${INSTALLER_DIR}/charts/osac-infra/templates/keycloak/resources.yaml" >/dev/null; then
    fail "Kind dev fixtures must not reference the removed user fixture"
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
fake_bin="${tmp_dir}/bin"
request_log="${tmp_dir}/curl.log"
mkdir -p "${fake_bin}"

printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -euo pipefail' \
    'case "${1:-} ${2:-}" in' \
    '  "-n mcp-demo") shift 2 ;;' \
    'esac' \
    'case "${1:-} ${2:-}" in' \
    '  "wait --for=condition=Available") exit 0 ;;' \
    '  "get configmap") printf "test CA bundle\\n" ;;' \
    '  "create token") printf "fixture-admin-token\\n" ;;' \
    '  "port-forward service/fulfillment-internal-api") while true; do sleep 1; done ;;' \
    '  *) printf "unexpected kubectl command: %s\\n" "$*" >&2; exit 1 ;;' \
    'esac' >"${fake_bin}/kubectl"
chmod +x "${fake_bin}/kubectl"

printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -euo pipefail' \
    'printf "%q " "$@" >>"${CURL_LOG}"' \
    'printf "\\n" >>"${CURL_LOG}"' \
    'method=GET' \
    'for ((i = 1; i <= $#; i++)); do' \
    '  if [[ "${!i}" == "-X" ]]; then' \
    '    next=$((i + 1))' \
    '    method="${!next}"' \
    '  fi' \
    'done' \
    'url="${!#}"' \
    'case "${url}" in' \
    '  */host_types)' \
    '    if [[ "${method}" == "POST" ]]; then printf "{\\\"id\\\":\\\"host-type-id\\\"}\\n"; elif [[ "${CURL_MODE}" == "existing" ]]; then printf "{\\\"items\\\":[{\\\"id\\\":\\\"host-type-id\\\",\\\"metadata\\\":{\\\"name\\\":\\\"mcp-demo-host-type\\\"}}]}\\n"; else printf "{\\\"items\\\":[]}\\n"; fi ;;' \
    '  */cluster_templates)' \
    '    if [[ "${method}" == "POST" ]]; then printf "{\\\"id\\\":\\\"template-id\\\"}\\n"; elif [[ "${CURL_MODE}" == "existing" ]]; then printf "{\\\"items\\\":[{\\\"id\\\":\\\"template-id\\\",\\\"metadata\\\":{\\\"name\\\":\\\"mcp-demo-template\\\"}}]}\\n"; else printf "{\\\"items\\\":[]}\\n"; fi ;;' \
    '  */cluster_catalog_items)' \
    '    if [[ "${method}" == "POST" ]]; then printf "{\\\"id\\\":\\\"catalog-item-id\\\"}\\n"; elif [[ "${CURL_MODE}" == "existing" ]]; then printf "{\\\"items\\\":[{\\\"id\\\":\\\"catalog-item-id\\\",\\\"metadata\\\":{\\\"name\\\":\\\"mcp-demo-cluster\\\"}}]}\\n"; else printf "{\\\"items\\\":[]}\\n"; fi ;;' \
    '  *) printf "unexpected curl URL: %s\\n" "${url}" >&2; exit 1 ;;' \
    'esac' >"${fake_bin}/curl"
chmod +x "${fake_bin}/curl"

run_seed() {
    local mode="$1"
    : >"${request_log}"
    if ! PATH="${fake_bin}:${PATH}" CURL_LOG="${request_log}" CURL_MODE="${mode}" \
        LOCAL_PORT=18444 "${SEED_SCRIPT}" mcp-demo >"${tmp_dir}/${mode}.out" 2>&1; then
        cat "${tmp_dir}/${mode}.out" >&2
        fail "MCP demo catalog seeder failed in ${mode} mode"
    fi
}

run_seed missing

for resource in host_types cluster_templates cluster_catalog_items; do
    rg -F "/api/private/v1/${resource}" "${request_log}" >/dev/null || \
        fail "seeder did not call the ${resource} private API"
done

post_count="$(rg -c -- '-X POST' "${request_log}")"
[[ "${post_count}" == "3" ]] || fail "expected three create requests, got ${post_count}"
rg -F -- '--cacert ' "${request_log}" >/dev/null || fail "seeder must verify the service certificate with the CA bundle"
rg -F 'Content-Type:' "${request_log}" >/dev/null || fail "seeder must send catalog fixtures as JSON"
rg -F -- '--resolve fulfillment-internal-api.mcp-demo.svc.cluster.local:18444:127.0.0.1' "${request_log}" >/dev/null || \
    fail "seeder must preserve the internal API TLS hostname through the port-forward"
if rg -e '(^| )-k( |$)|--insecure' "${request_log}" >/dev/null; then
    fail "seeder must not disable TLS verification"
fi
rg -F 'Created catalog item: mcp-demo-cluster' "${tmp_dir}/missing.out" >/dev/null || \
    fail "seeder did not report the created catalog item"

run_seed existing
if rg -F -- '-X POST' "${request_log}" >/dev/null; then
    fail "seeder must reuse fixtures found by name instead of recreating them"
fi
rg -F 'Reusing catalog item: mcp-demo-cluster' "${tmp_dir}/existing.out" >/dev/null || \
    fail "seeder did not report reuse of the existing catalog item"

if ! output=$(DEPS_HELM_ARGS='--set unexpected.deps=true' \
    INFRA_HELM_ARGS='--set unexpected.infra=true' \
    EXTRA_HELM_ARGS='--set unexpected.osac=true' \
    make -C "${INSTALLER_DIR}" -n install-mcp-demo PLATFORM=kind PROFILE=dev NS=mcp-demo 2>&1); then
    fail "install-mcp-demo Kind dry run failed: ${output}"
fi
[[ "${output}" == *"./scripts/seed-mcp-demo-catalog.sh mcp-demo"* ]] || \
    fail "install-mcp-demo must seed the catalog after installing OSAC"
[[ "${output}" == *"build -t localhost/fulfillment-service:mcp-demo"* ]] || \
    fail "install-mcp-demo must build the fulfillment-service image from this checkout"
[[ "${output}" == *"service.mcp.enabled=true"* ]] || \
    fail "install-mcp-demo must enable the MCP server"
[[ "${output}" == *"service.images.service=localhost/fulfillment-service:mcp-demo"* ]] || \
    fail "install-mcp-demo must deploy the locally built fulfillment-service image"
if [[ "${output}" == *"unexpected.deps=true"* || "${output}" == *"unexpected.infra=true"* || \
    "${output}" == *"unexpected.osac=true"* ]]; then
    fail "install-mcp-demo must not inherit Helm overrides from another deployment"
fi

if ! mac_output=$(HOST_IS_MAC=true CONTAINER_TOOL=podman \
    make -C "${INSTALLER_DIR}" -n install-mcp-demo PLATFORM=kind PROFILE=dev NS=mcp-demo 2>&1); then
    fail "install-mcp-demo macOS dry run failed: ${mac_output}"
fi
[[ "${mac_output}" == *"machine ssh -- podman build"* ]] || \
    fail "install-mcp-demo must stream the source build context to Podman on macOS"
[[ "${mac_output}" == *"fulfillment-service/Containerfile"* ]] || \
    fail "macOS image build must use the fulfillment-service Containerfile"

if ! helm dependency build "${OSAC_CHART}" >/dev/null; then
    fail "failed to build chart dependencies for MCP demo rendering"
fi

if ! infra_rendered=$(helm template osac-infra "${INSTALLER_DIR}/charts/osac-infra" \
    --namespace osac-infra --values "${KIND_INFRA_VALUES}" --set osacNamespace=mcp-demo); then
    fail "failed to render the Kind MCP demo infrastructure chart"
fi
for expected in \
    'Ensuring Keycloak client ${client_id} is registered...' \
    'https://keycloak:443/admin/realms/osac/clients?clientId=${client_id}' \
    'mountPath: /realm' \
    'name: keycloak-realm'; do
    rg -F -- "${expected}" <<<"${infra_rendered}" >/dev/null || \
        fail "Kind MCP demo infrastructure render is missing: ${expected}"
done

if ! rendered="$(helm template osac "${OSAC_CHART}" --namespace mcp-demo --values "${KIND_VALUES}" \
    --set service.mcp.enabled=true \
    --set-string service.mcp.externalHostname=mcp.mcp-demo.svc.cluster.local \
    --set service.mcp.externalPort=8443)"; then
    fail "failed to render the Kind MCP demo chart"
fi

for expected in \
    'name: fulfillment-mcp-server' \
    'app: fulfillment-mcp-server' \
    '- mcp-server' \
    '--grpc-server-address=fulfillment-internal-api:8001' \
    '--grpc-authn-trusted-token-issuers=https://keycloak.keycloak.svc.cluster.local:8443/realms/osac' \
    '--oauth-authorization-server=https://keycloak.keycloak.svc.cluster.local:8443/realms/osac' \
    '--oauth-resource-url=https://mcp.mcp-demo.svc.cluster.local:8443' \
    'secretName: fulfillment-mcp-server-tls' \
    '- mcp.mcp-demo.svc.cluster.local'; do
    rg -F -- "${expected}" <<<"${rendered}" >/dev/null || \
        fail "Kind MCP demo render is missing: ${expected}"
done

if rendered="$(helm template osac "${OSAC_CHART}" --namespace mcp-demo \
    --values "${OSAC_CHART}/ci/default-values.yaml" \
    --set service.mcp.enabled=false)"; then
    if rg -F 'fulfillment-mcp-server' <<<"${rendered}" >/dev/null; then
        fail "MCP server must remain disabled outside the MCP demo profile"
    fi
else
    fail "failed to render an ordinary MCP-disabled chart"
fi

if output=$(make -C "${INSTALLER_DIR}" -n install-mcp-demo PLATFORM=kind PROFILE=dev-full NS=mcp-demo 2>&1); then
    fail "install-mcp-demo must reject PROFILE=dev-full"
fi
[[ "${output}" == *"PLATFORM=kind PROFILE=dev"* ]] || \
    fail "dev-full rejection did not explain the supported MCP demo profile: ${output}"

if output=$(make -C "${INSTALLER_DIR}" -n install-mcp-demo PLATFORM=openshift PROFILE=dev NS=mcp-demo 2>&1); then
    fail "install-mcp-demo must reject PLATFORM=openshift"
fi
[[ "${output}" == *"PLATFORM=kind PROFILE=dev"* ]] || \
    fail "OpenShift rejection did not explain the supported MCP demo profile: ${output}"

echo "MCP demo catalog checks passed."
