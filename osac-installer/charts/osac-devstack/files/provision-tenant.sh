#!/usr/bin/env bash
# dev-full: provision a ready-to-use tenant so the browser "create a VM" flow works.
#
# Why this exists (the chain of facts that makes it necessary):
#   - Networking resources (VirtualNetwork/Subnet/SecurityGroup) cannot live in the
#     reserved 'shared'/'system' tenants, so a real tenant is required.
#   - A regular user's tenant is derived from the Keycloak *organization* claim
#     (groups map to tenants only for service accounts). With no organization the
#     claim is empty and the API returns "failed to determine assignable tenants".
#   - JIT provisioning creates the *user record* on first authenticated call, but it
#     requires the DB tenant to already exist ("tenant '<t>' doesn't exist" otherwise).
#   - The Tenants API is gRPC-only (not exposed over REST), so the DB tenant is
#     created with a throwaway in-cluster grpcurl pod running as the 'admin'
#     ServiceAccount (same admin identity seed-catalog-simple.sh uses). This keeps the host
#     dependency-free: only kubectl is needed, no host grpcurl.
#
# What this does (all idempotent -- safe to re-run):
#   1. Create the DB tenant via the private gRPC Tenants API. Tenant onboarding
#      auto-provisions the default VirtualNetwork + Subnet + SecurityGroup.
#   2. Seed a second ready VirtualNetwork, Subnet, and SecurityGroup so the MCP
#      demo can select a tenant-visible network rather than only accepting the
#      onboarding defaults.
#   3. Bind Kind's built-in `standard` StorageClass to the single local tenant
#      as the logical `local` storage tier.
#   4. Create a matching, enabled Keycloak organization.
#   5. Add the dev users (default tenant1_user, tenant1_admin) as organization
#      members so their tokens carry the organization claim and they can log in to
#      the UI and manage resources in the tenant.
#
# Requires KUBECONFIG to point at the kind cluster (set by the Makefile).
#
# Usage: provision-tenant.sh [osac-namespace]
#   Env overrides: TENANT, TENANT_USERS (comma-separated), KC_NS, KC_REALM,
#                  INTERNAL_SVC, INTERNAL_PORT, GRPCURL_IMAGE

set -euo pipefail

NS="${1:-${NS:-osac}}"
TENANT="${TENANT:-tenant1}"
TENANT_USERS="${TENANT_USERS:-tenant1_user,tenant1_admin}"
KC_NS="${KC_NS:-keycloak}"
KC_REALM="${KC_REALM:-osac}"
INTERNAL_SVC="${INTERNAL_SVC:-fulfillment-internal-api}"
INTERNAL_PORT="${INTERNAL_PORT:-8001}"
GRPCURL_IMAGE="${GRPCURL_IMAGE:-docker.io/fullstorydev/grpcurl:latest}"

log()  { echo "[+] $*"; }
warn() { echo "[!] $*" >&2; }

log "Provisioning tenant '${TENANT}' in namespace '${NS}'..."

# ── 1. Create the DB tenant via the private gRPC Tenants API ────────────────────
# Tenants is gRPC-only. Run grpcurl in-cluster as the 'admin' SA (an emergency
# service account) so no host grpcurl is required. The admin token is minted
# host-side and passed as a request header argument; -insecure skips TLS verify
# against the internal CA. AlreadyExists is treated as success (re-run friendly).
admin_token=$(kubectl -n "${NS}" create token admin)
pod="osac-provision-tenant-$$"
kubectl -n "${NS}" delete pod "${pod}" --ignore-not-found >/dev/null 2>&1 || true
kubectl -n "${NS}" run "${pod}" --restart=Never --image="${GRPCURL_IMAGE}" \
  --command -- grpcurl -insecure \
    -H "authorization: Bearer ${admin_token}" \
    -d "{\"object\":{\"metadata\":{\"name\":\"${TENANT}\"}}}" \
    "${INTERNAL_SVC}:${INTERNAL_PORT}" osac.private.v1.Tenants/Create >/dev/null

# Wait for the one-shot pod to finish (Succeeded or Failed), then read its output.
phase=""
for _ in $(seq 1 60); do
  phase=$(kubectl -n "${NS}" get pod "${pod}" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  [[ "${phase}" == "Succeeded" || "${phase}" == "Failed" ]] && break
  sleep 2
done
out=$(kubectl -n "${NS}" logs "${pod}" 2>/dev/null || true)
kubectl -n "${NS}" delete pod "${pod}" --ignore-not-found >/dev/null 2>&1 || true

if [[ "${phase}" == "Succeeded" ]]; then
  log "  tenant '${TENANT}' created (default network auto-provisioned)"
elif echo "${out}" | grep -qi "AlreadyExists"; then
  log "  tenant '${TENANT}' already exists"
else
  warn "  tenant create did not succeed (phase=${phase:-unknown}):"
  echo "${out}" | sed 's/^/      /' >&2
  exit 1
fi

grpc_admin_call() {
  local method="$1"
  local data="$2"

  grpcurl -insecure \
    -H "authorization: Bearer ${admin_token}" \
    -d "${data}" \
    "${INTERNAL_SVC}:${INTERNAL_PORT}" "${method}"
}

find_tenant_resource_id() {
  local service="$1"
  local name="$2"

  grpc_admin_call "osac.private.v1.${service}/List" '{}' | python3 -c '
import json
import sys

name, tenant = sys.argv[1:]
item = next((item for item in json.load(sys.stdin).get("items", [])
             if item.get("metadata", {}).get("name") == name
             and item.get("metadata", {}).get("tenant") == tenant), None)
print(item.get("id", "") if item else "")
' "${name}" "${TENANT}"
}

wait_for_tenant_resource_ready() {
  local service="$1"
  local id="$2"
  local ready_state="$3"
  local output
  local state

  for _ in $(seq 1 60); do
    if output="$(grpc_admin_call "osac.private.v1.${service}/Get" "$(jq -cn --arg id "${id}" '{id: $id}')" 2>&1)"; then
      state="$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("object", {}).get("status", {}).get("state", ""))' <<<"${output}")"
      if [[ "${state}" == "${ready_state}" ]]; then
        return
      fi
    fi
    sleep 2
  done

  warn "  ${service} '${id}' did not reach ${ready_state}"
  return 1
}

ensure_tenant_resource() {
  local service="$1"
  local name="$2"
  local ready_state="$3"
  local create_data="$4"
  local output

  ENSURED_RESOURCE_ID="$(find_tenant_resource_id "${service}" "${name}")"
  if [[ -z "${ENSURED_RESOURCE_ID}" ]]; then
    if output="$(grpc_admin_call "osac.private.v1.${service}/Create" "${create_data}" 2>&1)"; then
      log "  created ${service}: ${name}"
    elif [[ "${output}" == *"AlreadyExists"* || "${output}" == *"already exists"* ]]; then
      log "  ${service} already exists: ${name}"
    else
      warn "  failed to create ${service} '${name}': ${output}"
      return 1
    fi
    ENSURED_RESOURCE_ID="$(find_tenant_resource_id "${service}" "${name}")"
  else
    log "  reusing ${service}: ${name}"
  fi

  if [[ -z "${ENSURED_RESOURCE_ID}" ]]; then
    warn "  ${service} '${name}' could not be resolved"
    return 1
  fi
  wait_for_tenant_resource_ready "${service}" "${ENSURED_RESOURCE_ID}" "${ready_state}"
}

default_network_class_id="$(grpc_admin_call "osac.private.v1.NetworkClasses/List" '{}' | python3 -c '
import json
import sys

item = next((item for item in json.load(sys.stdin).get("items", []) if item.get("isDefault")), None)
print(item.get("id", "") if item else "")
')"
if [[ -z "${default_network_class_id}" ]]; then
  warn "  default NetworkClass was not found"
  exit 1
fi

# ── 1b. Seed an alternate ready network for MCP selection ───────────────────────
# This is a deterministic local-demo fixture, not a model-created network. It
# proves that create_compute_instance can attach a VM to tenant-visible network
# resources chosen through MCP discovery while retaining the onboarding default.
alternate_network_data="$(jq -cn \
  --arg name mcp-demo-isolated \
  --arg tenant "${TENANT}" \
  --arg network_class_id "${default_network_class_id}" \
  '{object: {metadata: {name: $name, tenant: $tenant}, spec: {networkClass: {id: $network_class_id}, region: "local", ipv4Cidr: "10.201.0.0/16"}}}')"
ensure_tenant_resource "VirtualNetworks" "mcp-demo-isolated" "VIRTUAL_NETWORK_STATE_READY" "${alternate_network_data}"
alternate_network_id="${ENSURED_RESOURCE_ID}"

alternate_subnet_data="$(jq -cn \
  --arg name mcp-demo-app-subnet \
  --arg tenant "${TENANT}" \
  --arg virtual_network_id "${alternate_network_id}" \
  '{object: {metadata: {name: $name, tenant: $tenant}, spec: {virtualNetwork: {id: $virtual_network_id}, ipv4Cidr: "10.201.1.0/24"}}}')"
ensure_tenant_resource "Subnets" "mcp-demo-app-subnet" "SUBNET_STATE_READY" "${alternate_subnet_data}"

alternate_security_group_data="$(jq -cn \
  --arg name mcp-demo-app-sg \
  --arg tenant "${TENANT}" \
  --arg virtual_network_id "${alternate_network_id}" \
  '{object: {metadata: {name: $name, tenant: $tenant}, spec: {virtualNetwork: {id: $virtual_network_id}}}}')"
ensure_tenant_resource "SecurityGroups" "mcp-demo-app-sg" "SECURITY_GROUP_STATE_READY" "${alternate_security_group_data}"

# ── 1c. Bind the Kind StorageClass to the single local tenant ────────────────────
# The Kind profile has no external storage backend. The ComputeInstance
# controller resolves a StorageTier by finding a StorageClass labelled for the
# tenant; that resolved class is passed in the AWX job's osac_job_vars and skips
# the external-storage JIT path. This cluster-scoped binding is intentionally
# limited to dev-full's one seeded local tenant.
kind_storage_class="standard"
if ! kubectl get storageclass "${kind_storage_class}" >/dev/null 2>&1; then
  warn "  Kind StorageClass '${kind_storage_class}' was not found"
  exit 1
fi
kubectl label storageclass "${kind_storage_class}" \
  "osac.openshift.io/tenant=${TENANT}" \
  'osac.openshift.io/storage-tier=local' \
  --overwrite >/dev/null
log "  StorageClass '${kind_storage_class}' bound to tenant '${TENANT}' as tier 'local'"

# ── 1d. Create the subnet target namespace(s) ───────────────────────────────────
# Each Subnet gets its own k8s namespace named after the Subnet CR: the osac-operator
# ComputeInstance controller creates/looks for the KubeVirt VM in that
# subnet-target namespace (osac.openshift.io/subnet-target-namespace annotation),
# NOT in the tenant namespace. In a real environment that namespace is created by
# subnet provisioning (the cudn_net role, which also builds the OVN CUDN). On kind
# networkingProvisioning=false makes subnets reconcile to Ready without any AAP
# dispatch, so nothing creates the namespace and the VM would have nowhere to live
# (the CI would hang at Provisioned=False/WaitingForVM). kind VMs use the default
# pod network + cluster-wide l2bridge binding — no per-namespace CUDN/NAD — so a
# plain namespace is all that's needed. Create one per onboarded subnet (idempotent).
#
# Subnets are scoped to a tenant by the osac.openshift.io/tenant annotation (not a
# label), and onboarding creates them asynchronously, so poll for the tenant's
# subnet(s) to appear before creating their namespaces.
subnets_for_tenant() {
  kubectl -n "${NS}" get subnets -o json 2>/dev/null | python3 -c "
import json,sys
for s in json.load(sys.stdin).get('items', []):
    if s.get('metadata', {}).get('annotations', {}).get('osac.openshift.io/tenant') == '${TENANT}':
        print(s['metadata']['name'])
" 2>/dev/null || true
}
onboarded_subnets=""
for _ in $(seq 1 30); do
  onboarded_subnets=$(subnets_for_tenant)
  [[ -n "${onboarded_subnets}" ]] && break
  sleep 2
done
if [[ -z "${onboarded_subnets}" ]]; then
  warn "  no subnet found for tenant '${TENANT}' yet — VM provisioning will hang until its subnet namespace exists"
else
  for sn in ${onboarded_subnets}; do
    kubectl create namespace "${sn}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    log "  subnet target namespace ready: ${sn}"
  done
fi

# ── 2 & 3. Keycloak organization + membership ───────────────────────────────────
# Port-forward Keycloak and drive its admin API to create an enabled organization
# matching the tenant and add the dev users as members.
admin_pw=$(kubectl -n "${KC_NS}" get secret keycloak-admin-credentials \
  -o jsonpath='{.data.admin-password}' | base64 -d)

kubectl -n "${KC_NS}" port-forward svc/keycloak 18443:443 >/dev/null 2>&1 &
pf_pid=$!
trap 'kill "${pf_pid}" 2>/dev/null || true; wait "${pf_pid}" 2>/dev/null || true' EXIT
sleep 3

KC="https://localhost:18443"
kc_token=$(curl -sk -X POST "${KC}/realms/master/protocol/openid-connect/token" \
  -d client_id=admin-cli -d username=admin -d "password=${admin_pw}" \
  -d grant_type=password \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['access_token'])")

KCURL=(curl -skS -H "Authorization: Bearer ${kc_token}" -H "Content-Type: application/json")

# find_org_id <name> — prints the organization id, or '' if absent.
find_org_id() {
  "${KCURL[@]}" "${KC}/admin/realms/${KC_REALM}/organizations?exact=true&search=$1" \
    | python3 -c "import json,sys; print(next((o['id'] for o in json.load(sys.stdin) if o.get('name')=='$1'), ''))" 2>/dev/null || true
}

# Create the organization (idempotent: 201 created, 409 already exists both OK).
code=$("${KCURL[@]}" -o /dev/null -w "%{http_code}" \
  -X POST "${KC}/admin/realms/${KC_REALM}/organizations" \
  -d "{\"name\":\"${TENANT}\",\"alias\":\"${TENANT}\",\"enabled\":true,\"domains\":[{\"name\":\"${TENANT}.localhost\",\"verified\":true}]}")
case "${code}" in
  201) log "  organization '${TENANT}' created" ;;
  409) log "  organization '${TENANT}' already exists" ;;
  *)   warn "  organization create returned HTTP ${code}" ;;
esac

org_id=$(find_org_id "${TENANT}")
if [[ -z "${org_id}" ]]; then
  warn "  could not resolve organization id for '${TENANT}' — skipping membership"
  exit 1
fi

# Add each dev user as an organization member (idempotent).
IFS=',' read -ra users <<< "${TENANT_USERS}"
for u in "${users[@]}"; do
  u="${u// /}"
  [[ -z "${u}" ]] && continue
  uid=$("${KCURL[@]}" "${KC}/admin/realms/${KC_REALM}/users?username=${u}&exact=true" \
    | python3 -c "import json,sys; d=json.load(sys.stdin); print(d[0]['id'] if d else '')" 2>/dev/null || true)
  if [[ -z "${uid}" ]]; then
    warn "    user '${u}' not found — skipping"
    continue
  fi
  code=$("${KCURL[@]}" -o /dev/null -w "%{http_code}" \
    -X POST "${KC}/admin/realms/${KC_REALM}/organizations/${org_id}/members" -d "\"${uid}\"")
  case "${code}" in
    201) log "    member added: ${u}" ;;
    409) log "    member already present: ${u}" ;;
    *)   warn "    adding member '${u}' returned HTTP ${code}" ;;
  esac
done

log "Tenant '${TENANT}' ready — log in as one of: ${TENANT_USERS} (password: keycloak default-user-password)"
