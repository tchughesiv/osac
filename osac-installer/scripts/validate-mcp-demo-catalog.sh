#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALLER_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
SEED_SCRIPT="${SCRIPT_DIR}/seed-mcp-demo-catalog.sh"
IMAGE_VALIDATOR="${SCRIPT_DIR}/validate-mcp-demo-image.sh"
OSAC_CHART="${INSTALLER_DIR}/charts/osac"
VM_VALUES="${INSTALLER_DIR}/values/vmaas-ci/instance.yaml"
EXTERNAL_VM_VALUES="${INSTALLER_DIR}/values/vmaas-external/instance.yaml"
EXTERNAL_INFRA_VALUES="${INSTALLER_DIR}/values/vmaas-external/infra.yaml"
EXTERNAL_VALIDATOR="${SCRIPT_DIR}/validate-external-vmaas-prerequisites.sh"
KIND_VALUES="${INSTALLER_DIR}/values/dev/kind-instance.yaml"
KIND_DEV_FULL_VALUES="${INSTALLER_DIR}/values/dev/kind-instance-devfull.yaml"
VIRT_NODE_SETUP="${SCRIPT_DIR}/dev-full/install-virt-node-setup.sh"

fail() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

[[ -x "${SEED_SCRIPT}" ]] || fail "missing executable MCP demo catalog seeder: ${SEED_SCRIPT}"
[[ -x "${IMAGE_VALIDATOR}" ]] || fail "missing executable MCP demo image validator: ${IMAGE_VALIDATOR}"
bash -n "${SEED_SCRIPT}"
bash -n "${IMAGE_VALIDATOR}"
bash -n "${EXTERNAL_VALIDATOR}"
bash -n "${VIRT_NODE_SETUP}"

"${IMAGE_VALIDATOR}" quay.io/example/fulfillment-service:mcp-demo linux/amd64
"${IMAGE_VALIDATOR}" registry.example.test:5000/example/fulfillment-service:v1.2.3 linux/arm64
for invalid_image in \
    '' \
    localhost/fulfillment-service:mcp-demo \
    quay.io/example/fulfillment-service \
    quay.io/example/fulfillment-service:'tag with-space' \
    'quay.io/example/fulfillment-service:tag;touch' \
    'quay.io/example/fulfillment-service:$(touch)' \
    'quay.io/example/fulfillment-service:tag"quote'; do
    if "${IMAGE_VALIDATOR}" "${invalid_image}" linux/amd64 >/dev/null 2>&1; then
        fail "MCP demo image validator accepted unsafe or incomplete image reference: ${invalid_image}"
    fi
done
for invalid_platform in '' darwin/amd64 linux/ppc64le 'linux/amd64;touch'; do
    if "${IMAGE_VALIDATOR}" quay.io/example/fulfillment-service:mcp-demo "${invalid_platform}" >/dev/null 2>&1; then
        fail "MCP demo image validator accepted unsupported platform: ${invalid_platform}"
    fi
done

for expected in \
    'virtualmachines.kubevirt.io' \
    'datavolumes.cdi.kubevirt.io' \
    'hyperconverged kubevirt-hyperconverged' \
    'get secret hub-access' \
    'compute_instance_templates' \
    'osac.templates.ocp_virt_vm' \
    'storage_tiers' \
    'mcp-demo-fedora' \
    'mcp-demo-small' \
    'compute_instance_catalog_items' \
    'mcp-demo-compute-instance' \
    'VIRTUAL_NETWORK_STATE_READY' \
    'SUBNET_STATE_READY' \
    'SECURITY_GROUP_STATE_READY' \
    'osac.openshift.io/default' \
    '--cacert'; do
    rg -F -- "${expected}" "${SEED_SCRIPT}" >/dev/null || \
        fail "MCP VMaaS catalog seeder is missing prerequisite or fixture: ${expected}"
done
if rg -e '(^| )-k( |$)|--insecure' "${SEED_SCRIPT}" >/dev/null; then
    fail "MCP VMaaS catalog seeder must not disable TLS verification"
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
fake_bin="${tmp_dir}/bin"
request_log="${tmp_dir}/curl.log"
state_dir="${tmp_dir}/state"
mkdir -p "${fake_bin}" "${state_dir}"

external_bin="${tmp_dir}/external-bin"
mkdir -p "${external_bin}"
printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -euo pipefail' \
    'case "$*" in' \
    '  "get crd "*) exit 0 ;;' \
    '  "get clusterissuer.cert-manager.io "*) exit 0 ;;' \
    '  "-n mcp-demo get configmap "*) printf "test CA bundle\\n" ;;' \
    '  "-n keycloak get keycloaks.k8s.keycloak.org "*) if [[ "$*" == *jsonpath* ]]; then printf "True"; fi ;;' \
    '  "-n keycloak get route "*) printf "sso.apps.example.test" ;;' \
    '  "auth can-i "*) printf "yes" ;;' \
    '  "-n openshift-cnv get hyperconverged kubevirt-hyperconverged") ;;' \
    '  "-n openshift-storage get lvmcluster -o name") printf "lvmcluster.lvm.topolvm.io/lvms-vg1\\n" ;;' \
    '  "get storageclass lvms-vg1") ;;' \
    '  "-n metallb-system get ipaddresspool -o name") printf "ipaddresspool.metallb.io/default\\n" ;;' \
    '  *) printf "unexpected external oc command: %s\\n" "$*" >&2; exit 1 ;;' \
    'esac' >"${external_bin}/oc"
chmod +x "${external_bin}/oc"

PATH="${external_bin}:${PATH}" bash "${EXTERNAL_VALIDATOR}" \
    mcp-demo keycloak keycloak keycloak osac default-ca ca-bundle lvms-vg1 >/dev/null
if PATH="${external_bin}:${PATH}" bash "${EXTERNAL_VALIDATOR}" \
    mcp-demo keycloak keycloak keycloak master default-ca ca-bundle lvms-vg1 >/dev/null 2>&1; then
    fail "external-prerequisites validation accepted the Keycloak master realm"
fi

mac_runtime_bin="${tmp_dir}/mac-runtime-bin"
sudo_trace="${tmp_dir}/sudo-called"
mkdir -p "${mac_runtime_bin}"
printf '%s\n' \
    '#!/usr/bin/env bash' \
    'case "$1" in' \
    '  -s) printf "Darwin\\n" ;;' \
    '  -m) printf "arm64\\n" ;;' \
    '  *) exit 1 ;;' \
    'esac' >"${mac_runtime_bin}/uname"
printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -euo pipefail' \
    'case "$1" in' \
    '  ps) printf "mcp-demo-control-plane\\n" ;;' \
    '  exec) exit 0 ;;' \
    '  *) exit 1 ;;' \
    'esac' >"${mac_runtime_bin}/podman"
printf '%s\n' \
    '#!/usr/bin/env bash' \
    'touch "${SUDO_TRACE}"' \
    'exit 1' >"${mac_runtime_bin}/sudo"
chmod +x "${mac_runtime_bin}/uname" "${mac_runtime_bin}/podman" "${mac_runtime_bin}/sudo"
if ! PATH="${mac_runtime_bin}:${PATH}" SUDO_TRACE="${sudo_trace}" \
    bash "${VIRT_NODE_SETUP}" mcp-demo >"${tmp_dir}/mac-runtime.out" 2>&1; then
    cat "${tmp_dir}/mac-runtime.out" >&2
    fail "macOS dev-full node setup failed with the user's Podman connection"
fi
[[ ! -e "${sudo_trace}" ]] || fail "macOS dev-full node setup invoked sudo"
rg -F 'Bridge CNI plugin installed successfully' "${tmp_dir}/mac-runtime.out" >/dev/null || \
    fail "macOS dev-full node setup did not use the rootless Podman connection"

fake_container="${fake_bin}/container"
container_log="${tmp_dir}/container.log"
printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -euo pipefail' \
    'printf "%q " "$@" >>"${CONTAINER_LOG}"' \
    'printf "\n" >>"${CONTAINER_LOG}"' \
    'case "${1:-}" in' \
    '  build) exit 0 ;;' \
    '  image) [[ "${2:-}" == "inspect" ]] || exit 1; printf "%s" "${IMAGE_PLATFORM}" ;;' \
    '  push) exit 0 ;;' \
    '  *) printf "unexpected container command: %s\n" "$*" >&2; exit 1 ;;' \
    'esac' >"${fake_container}"
chmod +x "${fake_container}"

CONTAINER_LOG="${container_log}" IMAGE_PLATFORM=linux/amd64 \
    make -C "${INSTALLER_DIR}" build-mcp-demo-image \
        CONTAINER_TOOL="${fake_container}" \
        MCP_DEMO_IMAGE=quay.io/example/fulfillment-service:mcp-demo \
        MCP_DEMO_PLATFORM=linux/amd64 >/dev/null
rg -F -- 'build --platform=linux/amd64' "${container_log}" >/dev/null || \
    fail "MCP demo image target did not request the configured platform"
rg -F -- 'push quay.io/example/fulfillment-service:mcp-demo' "${container_log}" >/dev/null || \
    fail "MCP demo image target did not push the configured image"

: >"${container_log}"
if CONTAINER_LOG="${container_log}" IMAGE_PLATFORM=linux/arm64 \
    make -C "${INSTALLER_DIR}" build-mcp-demo-image \
        CONTAINER_TOOL="${fake_container}" \
        MCP_DEMO_IMAGE=quay.io/example/fulfillment-service:mcp-demo \
        MCP_DEMO_PLATFORM=linux/amd64 >/dev/null 2>&1; then
    fail "MCP demo image target accepted a mismatched image platform"
fi
if rg -F -- 'push quay.io/example/fulfillment-service:mcp-demo' "${container_log}" >/dev/null; then
    fail "MCP demo image target pushed an image with the wrong platform"
fi

mac_build_bin="${tmp_dir}/mac-build-bin"
tar_log="${tmp_dir}/tar.log"
mkdir -p "${mac_build_bin}"
printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -euo pipefail' \
    'printf "%q " "$@" >>"${TAR_LOG}"' \
    'printf "\n" >>"${TAR_LOG}"' >"${mac_build_bin}/tar"
printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -euo pipefail' \
    'case "${1:-}" in' \
    '  machine) cat >/dev/null ;;' \
    '  image) [[ "${2:-}" == "inspect" ]] || exit 1; printf "linux/amd64" ;;' \
    '  push) ;;' \
    '  *) printf "unexpected Podman command: %s\n" "$*" >&2; exit 1 ;;' \
    'esac' >"${mac_build_bin}/podman"
chmod +x "${mac_build_bin}/tar" "${mac_build_bin}/podman"

PATH="${mac_build_bin}:${PATH}" TAR_LOG="${tar_log}" \
    make -C "${INSTALLER_DIR}" build-mcp-demo-image \
        HOST_IS_MAC=true \
        CONTAINER_TOOL="${mac_build_bin}/podman" \
        MCP_DEMO_IMAGE=quay.io/example/fulfillment-service:mcp-demo \
        MCP_DEMO_PLATFORM=linux/amd64 >/dev/null
for expected in osac-ui/proxy/go.mod osac-ui/proxy/go.sum; do
    rg -F -- "${expected}" "${tar_log}" >/dev/null || \
        fail "macOS Podman build context is missing ${expected}"
done

printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -euo pipefail' \
    'printf "%q " "$@" >>"${OC_LOG}"' \
    'printf "\n" >>"${OC_LOG}"' \
    'case "${1:-} ${2:-}" in' \
    '  "get crd") exit 0 ;;' \
    '  "-n openshift-cnv") shift 2; [[ "${1:-}" == "get" && "${2:-}" == "hyperconverged" ]] && exit 0 ;;' \
    '  "-n mcp-demo") shift 2 ;;' \
    'esac' \
    'case "${1:-} ${2:-}" in' \
    '  "get secret") [[ "${3:-}" == "hub-access" ]] && exit 0 ;;' \
    '  "get configmap") printf "test CA bundle\n"; exit 0 ;;' \
    '  "create token") printf "fixture-admin-token\n"; exit 0 ;;' \
    '  "port-forward service/fulfillment-internal-api") while true; do sleep 1; done ;;' \
    'esac' \
    'printf "unexpected oc command: %s\n" "$*" >&2; exit 1' >"${fake_bin}/oc"
chmod +x "${fake_bin}/oc"

printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -euo pipefail' \
    'printf "%q " "$@" >>"${CURL_LOG}"' \
    'printf "\n" >>"${CURL_LOG}"' \
    'method=GET' \
    'for ((i = 1; i <= $#; i++)); do' \
    '  if [[ "${!i}" == "-X" ]]; then next=$((i + 1)); method="${!next}"; fi' \
    'done' \
    'url="${!#}"' \
    'resource="${url##*/}"' \
    'present() { [[ -f "${STATE_DIR}/$1" ]]; }' \
    'case "${resource}" in' \
    '  compute_instance_templates) printf "{\"items\":[{\"id\":\"osac.templates.ocp_virt_vm\",\"metadata\":{\"name\":\"ocp-virt-vm\"}}]}\n" ;;' \
    '  storage_tiers) printf "{\"items\":[{\"id\":\"local-tier\",\"metadata\":{\"name\":\"local\"},\"spec\":{\"protocol\":\"STORAGE_PROTOCOL_BLOCK\",\"backends\":[{\"backend_id\":\"local-backend\"}]}}]}\n" ;;' \
    '  virtual_networks) if [[ "${NETWORK_READY}" == "true" ]]; then printf "{\"items\":[{\"id\":\"default-vn\",\"metadata\":{\"tenant\":\"osac-e2e-ci\",\"labels\":{\"osac.openshift.io/default\":\"true\"}},\"status\":{\"state\":\"VIRTUAL_NETWORK_STATE_READY\"}}]}\n"; else printf "{\"items\":[]}\n"; fi ;;' \
    '  subnets) printf "{\"items\":[{\"id\":\"default-subnet\",\"metadata\":{\"tenant\":\"osac-e2e-ci\",\"labels\":{\"osac.openshift.io/default\":\"true\"}},\"spec\":{\"virtual_network\":{\"id\":\"default-vn\"}},\"status\":{\"state\":\"SUBNET_STATE_READY\"}}]}\n" ;;' \
    '  security_groups) printf "{\"items\":[{\"id\":\"default-sg\",\"metadata\":{\"tenant\":\"osac-e2e-ci\",\"labels\":{\"osac.openshift.io/default\":\"true\"}},\"spec\":{\"virtual_network\":{\"id\":\"default-vn\"}},\"status\":{\"state\":\"SECURITY_GROUP_STATE_READY\"}}]}\n" ;;' \
    '  disk_images)' \
    '    if [[ "${method}" == "POST" ]]; then touch "${STATE_DIR}/disk_images"; printf "{\"id\":\"disk-image-id\"}\n"; elif present disk_images; then printf "{\"items\":[{\"id\":\"disk-image-id\",\"metadata\":{\"name\":\"mcp-demo-fedora\"},\"spec\":{\"source_ref\":\"quay.io/containerdisks/fedora:41\",\"lifecycle\":\"DISK_IMAGE_LIFECYCLE_AVAILABLE\"}}]}\n"; else printf "{\"items\":[]}\n"; fi ;;' \
    '  instance_types)' \
    '    if [[ "${method}" == "POST" ]]; then touch "${STATE_DIR}/instance_types"; printf "{\"id\":\"instance-type-id\"}\n"; elif present instance_types; then printf "{\"items\":[{\"id\":\"instance-type-id\",\"metadata\":{\"name\":\"mcp-demo-small\"},\"spec\":{\"state\":\"INSTANCE_TYPE_STATE_ACTIVE\"}}]}\n"; else printf "{\"items\":[]}\n"; fi ;;' \
    '  compute_instance_catalog_items)' \
    '    if [[ "${method}" == "POST" ]]; then touch "${STATE_DIR}/catalog_items"; printf "{\"id\":\"catalog-item-id\"}\n"; elif present catalog_items && [[ "${CATALOG_ITEM_MODE}" == "incompatible" ]]; then printf "{\"items\":[{\"id\":\"catalog-item-id\",\"metadata\":{\"name\":\"mcp-demo-compute-instance\"},\"template\":{\"id\":\"osac.templates.ocp_virt_vm\"},\"published\":true,\"field_definitions\":[{\"path\":\"instance_type\",\"editable\":false,\"default\":\"other\"},{\"path\":\"disk_image\",\"editable\":false,\"default\":\"mcp-demo-fedora\"},{\"path\":\"boot_disk.size_gib\",\"editable\":false,\"default\":20},{\"path\":\"boot_disk.storage_tier\",\"editable\":false,\"default\":{\"name\":\"local\"}},{\"path\":\"run_strategy\",\"editable\":false,\"default\":\"Always\"},{\"path\":\"network_attachments\",\"editable\":true}]}]}\n"; elif present catalog_items; then printf "{\"items\":[{\"id\":\"catalog-item-id\",\"metadata\":{\"name\":\"mcp-demo-compute-instance\"},\"template\":{\"id\":\"osac.templates.ocp_virt_vm\"},\"published\":true,\"field_definitions\":[{\"path\":\"instance_type\",\"editable\":false,\"default\":\"mcp-demo-small\"},{\"path\":\"disk_image\",\"editable\":false,\"default\":\"mcp-demo-fedora\"},{\"path\":\"boot_disk.size_gib\",\"editable\":false,\"default\":20},{\"path\":\"boot_disk.storage_tier\",\"editable\":false,\"default\":{\"name\":\"local\"}},{\"path\":\"run_strategy\",\"editable\":false,\"default\":\"Always\"},{\"path\":\"network_attachments\",\"editable\":true}]}]}\n"; else printf "{\"items\":[]}\n"; fi ;;' \
    '  *) printf "unexpected curl URL: %s\n" "${url}" >&2; exit 1 ;;' \
    'esac' >"${fake_bin}/curl"
chmod +x "${fake_bin}/curl"

run_seed() {
    local network_ready="${1:-true}"
    local catalog_item_mode="${2:-compatible}"

    : >"${request_log}"
    : >"${tmp_dir}/oc.log"
    if ! PATH="${fake_bin}:${PATH}" CURL_LOG="${request_log}" OC_LOG="${tmp_dir}/oc.log" STATE_DIR="${state_dir}" \
        NETWORK_READY="${network_ready}" CATALOG_ITEM_MODE="${catalog_item_mode}" LOCAL_PORT=18444 "${SEED_SCRIPT}" mcp-demo >"${tmp_dir}/seed.out" 2>&1; then
        cat "${tmp_dir}/seed.out" >&2
        fail "MCP VMaaS catalog seeder failed"
    fi
}

run_seed_failure() {
    local network_ready="$1"
    local catalog_item_mode="$2"

    : >"${request_log}"
    : >"${tmp_dir}/oc.log"
    if PATH="${fake_bin}:${PATH}" CURL_LOG="${request_log}" OC_LOG="${tmp_dir}/oc.log" STATE_DIR="${state_dir}" \
        NETWORK_READY="${network_ready}" CATALOG_ITEM_MODE="${catalog_item_mode}" LOCAL_PORT=18444 "${SEED_SCRIPT}" mcp-demo >"${tmp_dir}/seed.out" 2>&1; then
        fail "MCP VMaaS catalog seeder unexpectedly succeeded"
    fi
}

run_seed_failure false compatible
if rg -F -- '-X POST' "${request_log}" >/dev/null; then
    fail "seeder must validate tenant networking before creating demo resources"
fi

run_seed

for resource in compute_instance_templates storage_tiers disk_images instance_types compute_instance_catalog_items virtual_networks subnets security_groups; do
    rg -F "/api/private/v1/${resource}" "${request_log}" >/dev/null || \
        fail "seeder did not call the ${resource} private API"
done
[[ "$(rg -c -- '-X POST' "${request_log}")" == "3" ]] || fail "seeder must create exactly the DiskImage, InstanceType, and CatalogItem"
rg -F -- '--cacert ' "${request_log}" >/dev/null || fail "seeder must verify the service certificate with the CA bundle"
rg -F -- '--resolve fulfillment-internal-api.mcp-demo.svc.cluster.local:18444:127.0.0.1' "${request_log}" >/dev/null || \
    fail "seeder must preserve the internal API TLS hostname through the port-forward"
rg -F 'MCP VMaaS demo is ready (catalog item id: catalog-item-id, tenant: osac-e2e-ci).' "${tmp_dir}/seed.out" >/dev/null || \
    fail "seeder did not report the ready VMaaS catalog item"

run_seed
if rg -F -- '-X POST' "${request_log}" >/dev/null; then
    fail "seeder must reuse compatible VMaaS demo resources instead of recreating them"
fi
rg -F 'Reusing DiskImage: mcp-demo-fedora' "${tmp_dir}/seed.out" >/dev/null || fail "seeder did not report DiskImage reuse"
rg -F 'Reusing catalog item: mcp-demo-compute-instance' "${tmp_dir}/seed.out" >/dev/null || fail "seeder did not report CatalogItem reuse"

run_seed_failure true incompatible
rg -F 'recreate the demo environment instead of migrating it' "${tmp_dir}/seed.out" >/dev/null || \
    fail "seeder must reject an incompatible existing CatalogItem"
if rg -F -- '-X POST' "${request_log}" >/dev/null; then
    fail "seeder must not mutate an incompatible existing CatalogItem"
fi

if ! helm dependency build "${OSAC_CHART}" >/dev/null; then
    fail "failed to build chart dependencies for MCP demo rendering"
fi

if ! rendered="$(helm template osac "${OSAC_CHART}" --namespace mcp-demo --values "${VM_VALUES}" \
	--set-string service.externalHostname=fulfillment-api-mcp-demo.apps.example.test \
	--set-string service.internalHostname=fulfillment-internal-api-mcp-demo.apps.example.test \
    --set service.mcp.enabled=true \
    --set-string service.mcp.externalHostname=mcp-mcp-demo.apps.example.test \
    --set service.images.service=quay.io/example/fulfillment-service:mcp-demo \
    --set service.images.pullPolicy=Always)"; then
    fail "failed to render the OpenShift VMaaS MCP demo chart"
fi
for expected in \
    'name: fulfillment-mcp-server' \
    'name: mcp' \
    'host: mcp-mcp-demo.apps.example.test' \
    'image: quay.io/example/fulfillment-service:mcp-demo' \
    'imagePullPolicy: Always' \
    '- mcp-server' \
    '--oauth-resource-url=https://mcp-mcp-demo.apps.example.test'; do
    rg -F -- "${expected}" <<<"${rendered}" >/dev/null || \
        fail "OpenShift VMaaS MCP demo render is missing: ${expected}"
done

if ! external_rendered="$(helm template osac "${OSAC_CHART}" --namespace mcp-demo --values "${EXTERNAL_VM_VALUES}" \
    --set-string service.externalHostname=fulfillment-api-mcp-demo.apps.example.test \
    --set-string service.internalHostname=fulfillment-internal-api-mcp-demo.apps.example.test \
    --set service.mcp.enabled=true \
    --set-string service.mcp.externalHostname=mcp-mcp-demo.apps.example.test \
    --set service.images.service=quay.io/example/fulfillment-service:mcp-demo \
    --set service.images.pullPolicy=Always)"; then
    fail "failed to render the external-prerequisites OpenShift VMaaS MCP demo chart"
fi
for expected in \
    'name: fulfillment-mcp-server' \
    'host: mcp-mcp-demo.apps.example.test' \
    'image: quay.io/example/fulfillment-service:mcp-demo' \
    'imagePullPolicy: Always'; do
    rg -F -- "${expected}" <<<"${external_rendered}" >/dev/null || \
        fail "external-prerequisites MCP demo render is missing: ${expected}"
done

if ! external_infra_rendered="$(helm template osac-infra "${INSTALLER_DIR}/charts/osac-infra" \
    --namespace osac-infra --values "${EXTERNAL_INFRA_VALUES}")"; then
    fail "failed to render the external-prerequisites VMaaS infrastructure chart"
fi
external_infra_manifest="${tmp_dir}/external-infra.yaml"
printf '%s\n' "${external_infra_rendered}" >"${external_infra_manifest}"
if rg -n '^kind: (ClusterIssuer|Bundle)$' "${external_infra_manifest}" >/dev/null; then
    fail "external-prerequisites infrastructure unexpectedly renders a shared ClusterIssuer or Bundle"
fi

if ! kind_rendered="$(helm template osac "${OSAC_CHART}" --namespace osac \
    --values "${KIND_VALUES}" --values "${KIND_DEV_FULL_VALUES}" \
    --set service.mcp.enabled=true \
    --set-string service.mcp.externalHostname=mcp.osac.localhost \
    --set service.mcp.externalPort=8443 \
    --set service.images.service=localhost/fulfillment-service:mcp-demo \
    --set service.images.pullPolicy=Never)"; then
    fail "failed to render the Kind dev-full MCP demo chart"
fi
for expected in \
    'kind: TLSRoute' \
    'name: fulfillment-mcp-server' \
    '- mcp.osac.localhost' \
    'port: 8443' \
    'image: localhost/fulfillment-service:mcp-demo' \
    'imagePullPolicy: Never' \
    '--oauth-resource-url=https://mcp.osac.localhost:8443'; do
    rg -F -- "${expected}" <<<"${kind_rendered}" >/dev/null || \
        fail "Kind dev-full MCP demo render is missing: ${expected}"
done

makefile="${INSTALLER_DIR}/Makefile"
for expected in \
    'require PLATFORM=openshift PROFILE=vmaas-ci or PROFILE=vmaas-external' \
    'build-mcp-demo-image' \
    'MCP_DEMO_PLATFORM ?= linux/amd64' \
    'validate-mcp-demo-image.sh "$${MCP_DEMO_IMAGE}" "$${MCP_DEMO_PLATFORM}"' \
    '--platform="$(2)"' \
    'DEPS_HELM_ARGS="$(DEPS_HELM_ARGS)" INFRA_HELM_ARGS="$(INFRA_HELM_ARGS)"' \
    'validate-external-vmaas-prerequisites.sh' \
    'EXTERNAL_KEYCLOAK_ROUTE_HOST' \
    '$(MAKE) build-mcp-demo-image MCP_DEMO_IMAGE="$${MCP_DEMO_IMAGE}" MCP_DEMO_PLATFORM="$${MCP_DEMO_PLATFORM}"' \
    '$(CONTAINER_TOOL) push "$${MCP_DEMO_IMAGE}"' \
    'service.images.service=$${MCP_DEMO_IMAGE}' \
    'service.images.pullPolicy=Always' \
    'mcp-$(NS).$$domain' \
    './scripts/seed-mcp-demo-catalog.sh $(NS)'; do
    rg -F -- "${expected}" "${makefile}" >/dev/null || \
        fail "OpenShift MCP demo target is missing: ${expected}"
done

for expected in \
    'install-mcp-demo-kind' \
    'requires NS=osac because dev-full is a single local instance' \
    'MCP_DEMO_KIND_IMAGE ?= localhost/fulfillment-service:mcp-demo' \
    'MCP_DEMO_KIND_INFRA_HELM_ARGS ?=' \
    'MCP_DEMO_KIND_HELM_ARGS ?=' \
    '$(call build-fulfillment-service-image,$(MCP_DEMO_KIND_IMAGE))' \
    '$(call kind-load-image,$(MCP_DEMO_KIND_IMAGE))' \
    'DEPS_HELM_ARGS="" INFRA_HELM_ARGS="$(MCP_DEMO_KIND_INFRA_HELM_ARGS)"' \
    'service.mcp.externalHostname=mcp.osac.localhost' \
    'service.mcp.externalPort=8443' \
    'service.images.pullPolicy=Never' \
    '$(MAKE) install-devstack PLATFORM=$(PLATFORM) PROFILE=$(PROFILE) NS=$(NS)' \
    'kubectl rollout restart deployment/fulfillment-mcp-server -n $(NS)'; do
    rg -F -- "${expected}" "${makefile}" >/dev/null || \
        fail "Kind dev-full MCP demo target is missing: ${expected}"
done
if make -C "${INSTALLER_DIR}" --no-print-directory -n install-mcp-demo-kind \
    PLATFORM=kind PROFILE=dev NS=osac >"${tmp_dir}/kind-target.out" 2>&1; then
    fail "Kind MCP demo target accepted PROFILE=dev"
fi
rg -F 'requires PLATFORM=kind PROFILE=dev-full' "${tmp_dir}/kind-target.out" >/dev/null || \
    fail "Kind MCP demo target did not explain its profile requirement"
if make -C "${INSTALLER_DIR}" --no-print-directory -n install-mcp-demo-kind \
    PLATFORM=kind PROFILE=dev-full NS=other >"${tmp_dir}/kind-namespace.out" 2>&1; then
    fail "Kind MCP demo target accepted a non-osac namespace"
fi
rg -F 'requires NS=osac because dev-full is a single local instance' "${tmp_dir}/kind-namespace.out" >/dev/null || \
    fail "Kind MCP demo target did not explain its namespace requirement"

echo "MCP demo checks passed."
