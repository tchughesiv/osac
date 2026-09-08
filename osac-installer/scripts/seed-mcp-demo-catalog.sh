#!/usr/bin/env bash
set -euo pipefail

# Seeds the published ComputeInstance catalog item used by the MCP demo, after
# checking the OpenShift Virtualization and tenant prerequisites it relies on.
#
# The AAP template itself is intentionally not created here. AAP publishes the
# osac.templates.ocp_virt_vm template and owns its lifecycle; this script only
# creates the demo-specific DiskImage, InstanceType, and CatalogItem around it.
#
# Usage: seed-mcp-demo-catalog.sh [osac-namespace]

NS="${1:-${NS:-osac}}"
MCP_DEMO_TENANT="${MCP_DEMO_TENANT:-osac-e2e-ci}"
LOCAL_PORT="${LOCAL_PORT:-8444}"
API_HOST="fulfillment-internal-api.${NS}.svc.cluster.local"
API_URL="https://${API_HOST}:${LOCAL_PORT}/api/private/v1"
DEFAULT_LABEL="osac.openshift.io/default"

DISK_IMAGE_NAME="mcp-demo-fedora"
DISK_IMAGE_SOURCE="quay.io/containerdisks/fedora:41"
INSTANCE_TYPE_NAME="mcp-demo-small"
STORAGE_TIER_NAME="${MCP_DEMO_STORAGE_TIER:-local}"
TEMPLATE_NAME="ocp-virt-vm"
TEMPLATE_ID="osac.templates.ocp_virt_vm"
CATALOG_ITEM_NAME="mcp-demo-compute-instance"

fail() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

log() {
    printf '%s\n' "$*" >&2
}

for command in oc curl jq; do
    command -v "${command}" >/dev/null || fail "${command} is required"
done

[[ "${LOCAL_PORT}" =~ ^[1-9][0-9]{0,4}$ ]] || fail "LOCAL_PORT must be a TCP port number"
((LOCAL_PORT <= 65535)) || fail "LOCAL_PORT must be a TCP port number"
[[ -n "${MCP_DEMO_TENANT}" ]] || fail "MCP_DEMO_TENANT must not be empty"

tmp_dir="$(mktemp -d)"
ca_file="${tmp_dir}/bundle.pem"
port_forward_log="${tmp_dir}/port-forward.log"
port_forward_pid=""

cleanup() {
    if [[ -n "${port_forward_pid}" ]]; then
        kill "${port_forward_pid}" 2>/dev/null || true
        wait "${port_forward_pid}" 2>/dev/null || true
    fi
    rm -rf "${tmp_dir}"
}
trap cleanup EXIT

require_crd() {
    local crd="$1"
    local prerequisite="$2"

    oc get crd "${crd}" >/dev/null 2>&1 || \
        fail "${prerequisite} is not available: required CRD ${crd} was not found"
}

preflight_platform() {
    # HCO owns the KubeVirt deployment on OpenShift. Checking it as well as the
    # CRDs yields a useful error when the operator is subscribed but unfinished.
    require_crd virtualmachines.kubevirt.io "OpenShift Virtualization/KubeVirt"
    require_crd datavolumes.cdi.kubevirt.io "Containerized Data Importer (CDI)"
    oc -n openshift-cnv get hyperconverged kubevirt-hyperconverged >/dev/null 2>&1 || \
        fail "OpenShift Virtualization is not ready: HyperConverged kubevirt-hyperconverged is missing"
    oc -n "${NS}" get secret hub-access >/dev/null 2>&1 || \
        fail "hub access is not ready: expected Secret ${NS}/hub-access"
}

api_list() {
    local resource="$1"
    curl --fail --silent --show-error \
        --cacert "${ca_file}" \
        --resolve "${API_HOST}:${LOCAL_PORT}:127.0.0.1" \
        --header "Authorization: Bearer ${admin_token}" \
        "${API_URL}/${resource}"
}

api_list_with_retry() {
    local resource="$1"
    local response
    local attempt

    for attempt in $(seq 1 30); do
        if response="$(api_list "${resource}" 2>/dev/null)"; then
            printf '%s\n' "${response}"
            return 0
        fi
        sleep 1
    done

    printf 'ERROR: timed out waiting for the private fulfillment API; port-forward log follows:\n' >&2
    cat "${port_forward_log}" >&2
    return 1
}

api_create() {
    local resource="$1"
    local body="$2"

    curl --fail --silent --show-error \
        --cacert "${ca_file}" \
        --resolve "${API_HOST}:${LOCAL_PORT}:127.0.0.1" \
        --header "Authorization: Bearer ${admin_token}" \
        --header "Content-Type: application/json" \
        -X POST \
        --data "${body}" \
        "${API_URL}/${resource}"
}

lookup_object() {
    local resource="$1"
    local name="$2"
    local response
    local matches
    local count

    response="$(api_list_with_retry "${resource}")" || return 2
    matches="$(jq --arg name "${name}" '[.items[]? | select(.metadata.name == $name)]' <<<"${response}")" || return 2
    count="$(jq -r 'length' <<<"${matches}")" || return 2

    case "${count}" in
        0) return 1 ;;
        1) jq -ec '.[0]' <<<"${matches}" ;;
        *) printf 'ERROR: multiple %s fixtures named %s exist\n' "${resource}" "${name}" >&2; return 2 ;;
    esac
}

ensure_resource() {
    local resource="$1"
    local label="$2"
    local name="$3"
    local body="$4"
    local object
    local response
    local id
    local lookup_status

    if object="$(lookup_object "${resource}" "${name}")"; then
        id="$(jq -er '.id' <<<"${object}")" || fail "${label} ${name} has no ID"
        log "Reusing ${label}: ${name}"
        printf '%s\n' "${id}"
        return 0
    else
        lookup_status=$?
    fi
    if ((lookup_status != 1)); then
        fail "could not look up ${label} ${name}"
    fi

    response="$(api_create "${resource}" "${body}")" || fail "could not create ${label} ${name}"
    id="$(jq -er '.id' <<<"${response}")" || fail "private API did not return an ID for ${label} ${name}"
    log "Created ${label}: ${name}"
    printf '%s\n' "${id}"
}

require_resource() {
    local resource="$1"
    local label="$2"
    local name="$3"
    local object

    if ! object="$(lookup_object "${resource}" "${name}")"; then
        fail "${label} ${name} is missing; wait for its controller or configure the VMaaS environment"
    fi
    printf '%s\n' "${object}"
}

require_template() {
    local template

    template="$(require_resource compute_instance_templates 'AAP-published ComputeInstance template' "${TEMPLATE_NAME}")"
    [[ "$(jq -r '.id' <<<"${template}")" == "${TEMPLATE_ID}" ]] || \
        fail "ComputeInstance template ${TEMPLATE_NAME} has ID $(jq -r '.id' <<<"${template}"); expected ${TEMPLATE_ID}"
}

require_storage_tier() {
    local tier

    tier="$(require_resource storage_tiers 'StorageTier' "${STORAGE_TIER_NAME}")"
    jq -e '.spec.protocol == "STORAGE_PROTOCOL_BLOCK" and (.spec.backends | length > 0)' <<<"${tier}" >/dev/null || \
        fail "StorageTier ${STORAGE_TIER_NAME} must be a block tier with at least one backend"
}

require_default_networking() {
    local virtual_networks
    local subnets
    local security_groups
    local virtual_network
    local subnet
    local virtual_network_id

    virtual_networks="$(api_list_with_retry virtual_networks)" || fail "could not list VirtualNetworks"
    virtual_network="$(jq -ec --arg tenant "${MCP_DEMO_TENANT}" --arg label "${DEFAULT_LABEL}" '
        [.items[]? | select(.metadata.tenant == $tenant and .metadata.labels[$label] == "true" and .status.state == "VIRTUAL_NETWORK_STATE_READY")]
        | if length == 1 then .[0] else error("expected exactly one ready default VirtualNetwork") end
    ' <<<"${virtual_networks}")" || fail "tenant ${MCP_DEMO_TENANT} needs exactly one ready default VirtualNetwork"
    virtual_network_id="$(jq -er '.id' <<<"${virtual_network}")"

    subnets="$(api_list_with_retry subnets)" || fail "could not list Subnets"
    subnet="$(jq -ec --arg tenant "${MCP_DEMO_TENANT}" --arg label "${DEFAULT_LABEL}" --arg virtual_network_id "${virtual_network_id}" '
        [.items[]? | select(
            .metadata.tenant == $tenant and
            .metadata.labels[$label] == "true" and
            .status.state == "SUBNET_STATE_READY" and
            .spec.virtual_network.id == $virtual_network_id
        )]
        | if length == 1 then .[0] else error("expected exactly one ready default Subnet") end
    ' <<<"${subnets}")" || fail "tenant ${MCP_DEMO_TENANT} needs exactly one ready default Subnet"

    security_groups="$(api_list_with_retry security_groups)" || fail "could not list SecurityGroups"
    jq -e --arg tenant "${MCP_DEMO_TENANT}" --arg label "${DEFAULT_LABEL}" --arg virtual_network_id "${virtual_network_id}" '
        [.items[]? | select(
            .metadata.tenant == $tenant and
            .metadata.labels[$label] == "true" and
            .status.state == "SECURITY_GROUP_STATE_READY" and
            .spec.virtual_network.id == $virtual_network_id
        )]
        | length == 1
    ' <<<"${security_groups}" >/dev/null || \
        fail "tenant ${MCP_DEMO_TENANT} needs exactly one ready default SecurityGroup"
}

verify_catalog_item() {
    local item

    item="$(require_resource compute_instance_catalog_items 'MCP demo catalog item' "${CATALOG_ITEM_NAME}")"
    [[ "$(jq -r '.template.id' <<<"${item}")" == "${TEMPLATE_ID}" ]] || \
        fail "MCP demo catalog item ${CATALOG_ITEM_NAME} does not reference ${TEMPLATE_ID}; recreate the demo environment instead of migrating it"
    [[ "$(jq -r '.published' <<<"${item}")" == "true" ]] || \
        fail "MCP demo catalog item ${CATALOG_ITEM_NAME} is not published"
    jq -e \
        --arg instance_type "${INSTANCE_TYPE_NAME}" \
        --arg disk_image "${DISK_IMAGE_NAME}" \
        --arg storage_tier "${STORAGE_TIER_NAME}" '
        def has_policy($path; $editable; $default):
            [.field_definitions[]? | select(
                .path == $path and .editable == $editable and .default == $default
            )] | length == 1;

        has_policy("instance_type"; false; $instance_type) and
        has_policy("disk_image"; false; $disk_image) and
        has_policy("boot_disk.size_gib"; false; 20) and
        has_policy("boot_disk.storage_tier"; false; {name: $storage_tier}) and
        has_policy("run_strategy"; false; "Always") and
        has_policy("network_attachments"; true; null)
    ' <<<"${item}" >/dev/null || \
        fail "MCP demo catalog item ${CATALOG_ITEM_NAME} has incompatible VM field policies; recreate the demo environment instead of migrating it"
}

log "Checking VMaaS prerequisites for the MCP demo in namespace ${NS}, tenant ${MCP_DEMO_TENANT}..."
preflight_platform

oc -n "${NS}" get configmap ca-bundle -o jsonpath='{.data.bundle\.pem}' >"${ca_file}"
[[ -s "${ca_file}" ]] || fail "ca-bundle in namespace ${NS} has no bundle.pem"

admin_token="$(oc -n "${NS}" create token admin --duration=10m)"
[[ -n "${admin_token}" ]] || fail "could not create a token for service account ${NS}/admin"

oc -n "${NS}" port-forward service/fulfillment-internal-api "${LOCAL_PORT}:8001" --address=127.0.0.1 \
    >"${port_forward_log}" 2>&1 &
port_forward_pid=$!

require_template
require_storage_tier
require_default_networking

disk_image_body="$(jq -n --arg name "${DISK_IMAGE_NAME}" --arg source "${DISK_IMAGE_SOURCE}" '
    {
        metadata: {name: $name},
        spec: {
            source_type: "SOURCE_TYPE_REGISTRY",
            source_ref: $source,
            guest_os_family: "GUEST_OS_FAMILY_LINUX",
            architecture: ["ARCHITECTURE_AMD64"],
            lifecycle: "DISK_IMAGE_LIFECYCLE_AVAILABLE"
        }
    }
')"
ensure_resource disk_images 'DiskImage' "${DISK_IMAGE_NAME}" "${disk_image_body}" >/dev/null

instance_type_body="$(jq -n --arg name "${INSTANCE_TYPE_NAME}" '
    {
        metadata: {name: $name},
        spec: {
            cores: 2,
            memory_gib: 4,
            description: "MCP demo VM size",
            state: "INSTANCE_TYPE_STATE_ACTIVE"
        }
    }
')"
ensure_resource instance_types 'InstanceType' "${INSTANCE_TYPE_NAME}" "${instance_type_body}" >/dev/null

disk_image="$(require_resource disk_images DiskImage "${DISK_IMAGE_NAME}")"
jq -e --arg source "${DISK_IMAGE_SOURCE}" '
    .spec.lifecycle == "DISK_IMAGE_LIFECYCLE_AVAILABLE" and .spec.source_ref == $source
' <<<"${disk_image}" >/dev/null || \
    fail "DiskImage ${DISK_IMAGE_NAME} must be available and use ${DISK_IMAGE_SOURCE}"

instance_type="$(require_resource instance_types InstanceType "${INSTANCE_TYPE_NAME}")"
jq -e '.spec.state == "INSTANCE_TYPE_STATE_ACTIVE"' <<<"${instance_type}" >/dev/null || \
    fail "InstanceType ${INSTANCE_TYPE_NAME} must be active"

catalog_item_body="$(jq -n \
    --arg name "${CATALOG_ITEM_NAME}" \
    --arg template_id "${TEMPLATE_ID}" \
    --arg instance_type "${INSTANCE_TYPE_NAME}" \
    --arg disk_image "${DISK_IMAGE_NAME}" \
    --arg storage_tier "${STORAGE_TIER_NAME}" '
    {
        metadata: {name: $name},
        title: "MCP demo virtual machine",
        description: "Fedora virtual machine offering for the OSAC Deployment MCP PoC.",
        template: {id: $template_id},
        published: true,
        tenant: "",
        field_definitions: [
            {path: "instance_type", display_name: "Instance Type", editable: false, default: $instance_type},
            {path: "disk_image", display_name: "Disk Image", editable: false, default: $disk_image},
            {path: "boot_disk.size_gib", display_name: "Boot Disk Size", editable: false, default: 20},
            {path: "boot_disk.storage_tier", display_name: "Boot Disk Storage Tier", editable: false, default: {name: $storage_tier}},
            {path: "run_strategy", display_name: "Run Strategy", editable: false, default: "Always"},
            {path: "network_attachments", display_name: "Network Attachments", editable: true}
        ]
    }
')"
ensure_resource compute_instance_catalog_items 'catalog item' "${CATALOG_ITEM_NAME}" "${catalog_item_body}" >/dev/null
verify_catalog_item

catalog_item="$(require_resource compute_instance_catalog_items 'MCP demo catalog item' "${CATALOG_ITEM_NAME}")"
catalog_item_id="$(jq -er '.id' <<<"${catalog_item}")"
log "MCP VMaaS demo is ready (catalog item id: ${catalog_item_id}, tenant: ${MCP_DEMO_TENANT})."
