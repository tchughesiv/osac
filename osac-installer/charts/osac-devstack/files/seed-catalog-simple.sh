#!/usr/bin/env bash
# Seed the OSAC catalog via the private gRPC API
#
# Creates disk images, instance types, templates, and catalog items for dev-full.
#
# Usage: seed-catalog-simple.sh <internal-api-service> <internal-api-port>

set -euo pipefail

SERVICE="${1:-${INTERNAL_SVC:-fulfillment-internal-api}}"
PORT="${2:-${INTERNAL_PORT:-8001}}"
NS="${NS:-osac}"
CA_FILE="${CA_FILE:-/etc/fulfillment-api-tls/ca.crt}"
ADMIN_SERVICE_ACCOUNT="${ADMIN_SERVICE_ACCOUNT:-admin}"

log() { echo "[+] $*"; }

# Helper: gRPC call via grpcurl
grpc_call() {
  local method="$1"
  local data="$2"
  grpcurl -cacert "${CA_FILE}" \
    -H "authorization: Bearer ${admin_token}" \
    -d "${data}" \
    "${SERVICE}.${NS}.svc.cluster.local:${PORT}" "${method}"
}

create_or_reuse() {
  local label="$1"
  local method="$2"
  local data="$3"
  local output

  if output="$(grpc_call "${method}" "${data}" 2>&1)"; then
    log "  created ${label}"
    return
  fi
  if [[ "${output}" == *"AlreadyExists"* || "${output}" == *"already exists"* ]]; then
    log "  reusing ${label}"
    return
  fi

  printf 'ERROR: failed to create %s with %s:\n%s\n' "${label}" "${method}" "${output}" >&2
  return 1
}

find_named_item_id() {
  local name="$1"

  python3 -c '
import json
import sys

name = sys.argv[1]
items = json.load(sys.stdin).get("items", [])
item = next((item for item in items if item.get("metadata", {}).get("name") == name), None)
print(item.get("id", "") if item else "")
' "${name}"
}

created_object_id() {
  python3 -c 'import json, sys; print(json.load(sys.stdin).get("object", {}).get("id", ""))'
}

valid_block_storage_tier() {
  local name="$1"

  python3 -c '
import json
import sys

name = sys.argv[1]
items = json.load(sys.stdin).get("items", [])
tier = next((item for item in items if item.get("metadata", {}).get("name") == name), None)
backends = tier.get("spec", {}).get("backends", []) if tier else []
is_block = tier and tier.get("spec", {}).get("protocol") == "STORAGE_PROTOCOL_BLOCK"
has_backend = any(backend.get("backendId") for backend in backends)
sys.exit(0 if is_block and has_backend else 1)
' "${name}"
}

ensure_kind_local_storage() {
  local backend_name="kind-local-path"
  local tier_name="local"
  local backend_id
  local output
  local tier_data

  # The Kind cluster's standard StorageClass is the physical implementation of
  # this logical tier. It lets the normal catalog contract require a storage
  # tier without pretending that LVMS or an external storage array is present.
  if ! output="$(grpc_call "osac.private.v1.StorageTiers/List" '{}' 2>&1)"; then
    printf 'ERROR: failed to list StorageTiers before creating %s:\n%s\n' "${tier_name}" "${output}" >&2
    return 1
  fi
  if [[ -n "$(find_named_item_id "${tier_name}" <<<"${output}")" ]]; then
    if ! valid_block_storage_tier "${tier_name}" <<<"${output}"; then
      printf 'ERROR: existing StorageTier %s is not a usable block tier\n' "${tier_name}" >&2
      return 1
    fi
    log "  reusing StorageTier: ${tier_name}"
    return
  fi

  if output="$(grpc_call "osac.private.v1.StorageBackends/Create" \
    '{"object":{"metadata":{"name":"kind-local-path"},"spec":{"provider":"kind-local-path","endpoint":"standard","credentials":{"username":"not-applicable","password":"not-applicable"}}}}' 2>&1)"; then
    backend_id="$(created_object_id <<<"${output}")"
    log "  created StorageBackend: ${backend_name}"
  elif [[ "${output}" == *"AlreadyExists"* || "${output}" == *"already exists"* ]]; then
    if ! output="$(grpc_call "osac.private.v1.StorageBackends/List" '{}' 2>&1)"; then
      printf 'ERROR: failed to list StorageBackends while reusing %s:\n%s\n' "${backend_name}" "${output}" >&2
      return 1
    fi
    backend_id="$(find_named_item_id "${backend_name}" <<<"${output}")"
    if [[ -z "${backend_id}" ]]; then
      printf 'ERROR: StorageBackend %s already exists but could not be resolved\n' "${backend_name}" >&2
      return 1
    fi
    log "  reusing StorageBackend: ${backend_name}"
  else
    printf 'ERROR: failed to create StorageBackend %s:\n%s\n' "${backend_name}" "${output}" >&2
    return 1
  fi

  if [[ -z "${backend_id}" ]]; then
    printf 'ERROR: StorageBackend %s was created without an identifier\n' "${backend_name}" >&2
    return 1
  fi

  tier_data="{\"object\":{\"metadata\":{\"name\":\"${tier_name}\"},\"spec\":{\"description\":\"Kind local-path storage tier.\",\"protocol\":\"STORAGE_PROTOCOL_BLOCK\",\"backends\":[{\"backendId\":\"${backend_id}\"}]}}}"
  if output="$(grpc_call "osac.private.v1.StorageTiers/Create" "${tier_data}" 2>&1)"; then
    log "  created StorageTier: ${tier_name}"
    return
  fi
  if [[ "${output}" == *"AlreadyExists"* || "${output}" == *"already exists"* ]]; then
    if ! output="$(grpc_call "osac.private.v1.StorageTiers/List" '{}' 2>&1)" || \
      ! valid_block_storage_tier "${tier_name}" <<<"${output}"; then
      printf 'ERROR: existing StorageTier %s is not a usable block tier\n' "${tier_name}" >&2
      return 1
    fi
    log "  reusing StorageTier: ${tier_name}"
    return
  fi

  printf 'ERROR: failed to create StorageTier %s:\n%s\n' "${tier_name}" "${output}" >&2
  return 1
}

create_or_update_template() {
  local create_data="$1"
  local update_data="$2"
  local output

  if output="$(grpc_call "osac.private.v1.ComputeInstanceTemplates/Create" "${create_data}" 2>&1)"; then
    log "  created template: osac.templates.ocp_virt_vm"
    return
  fi
  if [[ "${output}" != *"AlreadyExists"* && "${output}" != *"already exists"* ]]; then
    printf 'ERROR: failed to create template with osac.private.v1.ComputeInstanceTemplates/Create:\n%s\n' "${output}" >&2
    return 1
  fi

  if output="$(grpc_call "osac.private.v1.ComputeInstanceTemplates/Update" "${update_data}" 2>&1)"; then
    log "  updated template: osac.templates.ocp_virt_vm"
    return
  fi

  printf 'ERROR: failed to update template with osac.private.v1.ComputeInstanceTemplates/Update:\n%s\n' "${output}" >&2
  return 1
}

log "Seeding catalog into '${NS}'..."

if [[ ! -r "${CA_FILE}" ]]; then
  printf 'ERROR: fulfillment API CA certificate is not readable: %s\n' "${CA_FILE}" >&2
  exit 1
fi

# The devstack service account has permission to mint a short-lived token for
# the local `admin` service account. The fulfillment service recognizes that
# identity as an emergency administrator for private API setup operations.
admin_token="$(kubectl -n "${NS}" create token "${ADMIN_SERVICE_ACCOUNT}")"
if [[ -z "${admin_token}" ]]; then
  printf 'ERROR: failed to mint a token for service account: %s\n' "${ADMIN_SERVICE_ACCOUNT}" >&2
  exit 1
fi

log "Creating storage catalog..."
ensure_kind_local_storage

# Disk images
log "Creating disk images..."
create_or_reuse "disk-image: fedora" "osac.private.v1.DiskImages/Create" \
  '{"object":{"metadata":{"name":"fedora"},"spec":{"sourceType":"SOURCE_TYPE_REGISTRY","sourceRef":"quay.io/containerdisks/fedora:latest","guestOsFamily":"GUEST_OS_FAMILY_LINUX","architecture":["ARCHITECTURE_AMD64","ARCHITECTURE_ARM64"],"lifecycle":"DISK_IMAGE_LIFECYCLE_AVAILABLE"}}}'

# Instance types
log "Creating instance types..."
for it in \
  "u1-small:2:4:2 cores, 4 GiB RAM" \
  "u1-medium:4:8:4 cores, 8 GiB RAM" \
  "u1-large:8:16:8 cores, 16 GiB RAM"; do
  IFS=: read -r name cores memGib desc <<<"$it"
  create_or_reuse "instance-type: ${name}" "osac.private.v1.InstanceTypes/Create" \
    "{\"object\":{\"metadata\":{\"name\":\"${name}\"},\"spec\":{\"vcpus\":${cores},\"memoryGib\":${memGib},\"description\":\"${desc}\",\"state\":\"INSTANCE_TYPE_STATE_ACTIVE\"}}}"
done

# Templates
log "Creating templates..."
create_or_update_template \
  '{"object":{"id":"osac.templates.ocp_virt_vm","metadata":{"name":"ocp-virt-vm"},"title":"Virtual Machine Template (Linux and Windows)","description":"KubeVirt VM template for local development.","specDefaults":{"instanceType":{"name":"u1-small","shared":true},"diskImage":{"name":"fedora"},"bootDisk":{"sizeGib":20,"storageTier":{"name":"local"}},"runStrategy":"COMPUTE_INSTANCE_RUN_STRATEGY_ALWAYS"}}}' \
  '{"object":{"id":"osac.templates.ocp_virt_vm","specDefaults":{"instanceType":{"name":"u1-small","shared":true},"diskImage":{"name":"fedora"},"bootDisk":{"sizeGib":20,"storageTier":{"name":"local"}},"runStrategy":"COMPUTE_INSTANCE_RUN_STRATEGY_ALWAYS"}},"updateMask":{"paths":["spec_defaults"]}}'

# Catalog items
log "Creating catalog items..."
create_or_reuse "catalog-item: linux-vm" "osac.private.v1.ComputeInstanceCatalogItems/Create" \
  '{"object":{"metadata":{"name":"linux-vm"},"title":"Linux Virtual Machine","description":"Fedora-based VM with KVM acceleration","template":{"id":"osac.templates.ocp_virt_vm"},"published":true}}'

log "Catalog seeded — ready to create compute instances"
