#!/usr/bin/env bash
set -euo pipefail

# Seeds the minimal catalog chain needed by the Cluster-focused MCP demo.
#
# Usage: seed-mcp-demo-catalog.sh [osac-namespace]

NS="${1:-${NS:-osac}}"
LOCAL_PORT="${LOCAL_PORT:-8444}"
API_HOST="fulfillment-internal-api.${NS}.svc.cluster.local"
API_URL="https://${API_HOST}:${LOCAL_PORT}/api/private/v1"
HOST_TYPE_NAME="mcp-demo-host-type"
CLUSTER_VERSION_NAME="mcp-demo-cluster-version"
CLUSTER_VERSION="4.20.0"
CLUSTER_RELEASE_IMAGE="quay.io/openshift-release-dev/ocp-release:4.20.0-multi"
TEMPLATE_NAME="mcp-demo-template"
# ClusterTemplate IDs are forwarded unchanged to ClusterOrder.spec.templateID.
# They must therefore use the AAP role-name format enforced by that CRD.
TEMPLATE_ID="osac.templates.mcp_demo_cluster"
CATALOG_ITEM_NAME="mcp-demo-cluster"

fail() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

log() {
    printf '%s\n' "$*" >&2
}

for command in kubectl curl jq; do
    command -v "${command}" >/dev/null || fail "${command} is required"
done

[[ "${LOCAL_PORT}" =~ ^[1-9][0-9]{0,4}$ ]] || fail "LOCAL_PORT must be a TCP port number"
((LOCAL_PORT <= 65535)) || fail "LOCAL_PORT must be a TCP port number"

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

kubectl -n "${NS}" get configmap ca-bundle -o jsonpath='{.data.bundle\.pem}' >"${ca_file}"
[[ -s "${ca_file}" ]] || fail "ca-bundle in namespace ${NS} has no bundle.pem"

admin_token="$(kubectl -n "${NS}" create token admin --duration=10m)"
[[ -n "${admin_token}" ]] || fail "could not create a token for service account ${NS}/admin"

kubectl -n "${NS}" port-forward service/fulfillment-internal-api "${LOCAL_PORT}:8001" --address=127.0.0.1 \
    >"${port_forward_log}" 2>&1 &
port_forward_pid=$!

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

api_update() {
    local resource="$1"
    local id="$2"
    local body="$3"

    curl --fail --silent --show-error \
        --cacert "${ca_file}" \
        --resolve "${API_HOST}:${LOCAL_PORT}:127.0.0.1" \
        --header "Authorization: Bearer ${admin_token}" \
        --header "Content-Type: application/json" \
        -X PATCH \
        --data "${body}" \
        "${API_URL}/${resource}/${id}"
}

lookup_id() {
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
        1) jq -er '.[0].id' <<<"${matches}" ;;
        *) printf 'ERROR: multiple %s fixtures named %s exist\n' "${resource}" "${name}" >&2; return 2 ;;
    esac
}

ensure_resource() {
    local resource="$1"
    local label="$2"
    local name="$3"
    local body="$4"
    local existing_id
    local response
    local id
    local lookup_status

    if existing_id="$(lookup_id "${resource}" "${name}")"; then
        log "Reusing ${label}: ${name}"
        printf '%s\n' "${existing_id}"
        return 0
    else
        lookup_status=$?
    fi
    if ((lookup_status != 1)); then
        fail "could not look up ${label} ${name}"
    fi

    response="$(api_create "${resource}" "${body}")" || fail "could not create ${label} ${name}"
    id="$(jq -er '.id' <<<"${response}")" || fail "private API did not return an id for ${label} ${name}"
    log "Created ${label}: ${name}"
    printf '%s\n' "${id}"
}

ensure_cluster_template() {
    local body="$1"
    local existing_id
    local response
    local id
    local lookup_status

    if existing_id="$(lookup_id cluster_templates "${TEMPLATE_NAME}")"; then
        [[ "${existing_id}" == "${TEMPLATE_ID}" ]] || fail \
            "MCP demo template ${TEMPLATE_NAME} has immutable ID ${existing_id}; recreate the Kind dev cluster before reseeding"
        api_update cluster_templates "${existing_id}" "${body}" >/dev/null || \
            fail "could not update cluster template ${TEMPLATE_NAME}"
        log "Reconciled cluster template: ${TEMPLATE_NAME}"
        printf '%s\n' "${existing_id}"
        return 0
    else
        lookup_status=$?
    fi
    if ((lookup_status != 1)); then
        fail "could not look up cluster template ${TEMPLATE_NAME}"
    fi

    response="$(api_create cluster_templates "${body}")" || \
        fail "could not create cluster template ${TEMPLATE_NAME}"
    id="$(jq -er '.id' <<<"${response}")" || \
        fail "private API did not return an id for cluster template ${TEMPLATE_NAME}"
    [[ "${id}" == "${TEMPLATE_ID}" ]] || \
        fail "private API created MCP demo template with ID ${id}, expected ${TEMPLATE_ID}"
    log "Created cluster template: ${TEMPLATE_NAME}"
    printf '%s\n' "${id}"
}

log "Seeding the MCP demo catalog in namespace ${NS}..."

host_type_body="$(jq -n --arg name "${HOST_TYPE_NAME}" '{metadata: {name: $name}, title: "MCP demo host type"}')"
host_type_id="$(ensure_resource host_types 'host type' "${HOST_TYPE_NAME}" "${host_type_body}")"

cluster_version_body="$(jq -n \
    --arg name "${CLUSTER_VERSION_NAME}" \
    --arg version "${CLUSTER_VERSION}" \
    --arg image "${CLUSTER_RELEASE_IMAGE}" \
    '{metadata: {name: $name}, spec: {version: $version, image: $image, enabled: true, state: "CLUSTER_VERSION_STATE_ACTIVE"}}')"
ensure_resource cluster_versions 'cluster version' "${CLUSTER_VERSION_NAME}" "${cluster_version_body}" >/dev/null

template_body="$(jq -n \
    --arg id "${TEMPLATE_ID}" \
    --arg name "${TEMPLATE_NAME}" \
    --arg host_type_id "${host_type_id}" \
    --arg cluster_version_name "${CLUSTER_VERSION_NAME}" \
    '{id: $id, metadata: {name: $name}, title: "MCP demo cluster template", node_sets: {workers: {host_type: {id: $host_type_id}, size: 3}}, spec_defaults: {version: {name: $cluster_version_name}}}')"
template_id="$(ensure_cluster_template "${template_body}")"

catalog_item_body="$(jq -n \
    --arg name "${CATALOG_ITEM_NAME}" \
    --arg template_id "${template_id}" \
    '{metadata: {name: $name}, title: "MCP demo cluster", description: "Seeded cluster offering for the OSAC Deployment MCP PoC.", template: {id: $template_id}, published: true}')"
catalog_item_id="$(ensure_resource cluster_catalog_items 'catalog item' "${CATALOG_ITEM_NAME}" "${catalog_item_body}")"

log "MCP demo catalog is ready (catalog item id: ${catalog_item_id})."
