#!/usr/bin/env bash
set -euo pipefail

# Validate the platform services consumed by values/vmaas-external. This script
# is deliberately read-only: a shared cluster retains ownership of its
# Keycloak, operators, subscriptions, ClusterIssuer, CA bundle, and networking
# resources. The installer creates only the requested OSAC namespace and its
# own releases after these checks pass.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

if [[ "$#" -ne 8 ]]; then
    echo "usage: $0 <osac-namespace> <keycloak-namespace> <keycloak-instance> <keycloak-route> <osac-realm> <cluster-issuer> <ca-bundle-configmap> <lvms-storageclass>" >&2
    exit 2
fi

osac_namespace="$1"
keycloak_namespace="$2"
keycloak_instance="$3"
keycloak_route="$4"
osac_realm="$5"
cluster_issuer="$6"
ca_bundle_configmap="$7"
lvms_storageclass="$8"

fail() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

require_dns_name() {
    local label="$1"
    local value="$2"
    [[ "${value}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || \
        fail "${label} must be a DNS label: ${value}"
}

require_crd() {
    local crd="$1"
    local dependency="$2"
    oc get crd "${crd}" >/dev/null 2>&1 || \
        fail "${dependency} is not available: required CRD ${crd} was not found"
}

require_command oc

for pair in \
    "OSAC namespace:${osac_namespace}" \
    "Keycloak namespace:${keycloak_namespace}" \
    "Keycloak instance:${keycloak_instance}" \
    "Keycloak Route:${keycloak_route}" \
    "ClusterIssuer:${cluster_issuer}" \
    "CA bundle ConfigMap:${ca_bundle_configmap}" \
    "LVMS StorageClass:${lvms_storageclass}"; do
    require_dns_name "${pair%%:*}" "${pair#*:}"
done

[[ "${osac_realm}" =~ ^[A-Za-z0-9._-]+$ ]] || \
    fail "OSAC Keycloak realm may contain only letters, numbers, '.', '_', and '-': ${osac_realm}"
[[ "${osac_realm}" != "master" ]] || \
    fail "OSAC Keycloak realm must not be the Keycloak master realm"

require_crd certificates.cert-manager.io "cert-manager"
require_crd keycloaks.k8s.keycloak.org "Red Hat build of Keycloak"
require_crd keycloakrealmimports.k8s.keycloak.org "Red Hat build of Keycloak"
require_crd automationcontrollers.automationcontroller.ansible.com "Ansible Automation Platform"
require_crd virtualmachines.kubevirt.io "OpenShift Virtualization/KubeVirt"
require_crd datavolumes.cdi.kubevirt.io "Containerized Data Importer (CDI)"
require_crd lvmclusters.lvm.topolvm.io "Logical Volume Manager Storage"
require_crd ipaddresspools.metallb.io "MetalLB"

oc get clusterissuer.cert-manager.io "${cluster_issuer}" >/dev/null 2>&1 || \
    fail "required ClusterIssuer ${cluster_issuer} was not found; ask the cluster administrator to provide it rather than enabling caIssuer in this profile"

ca_bundle="$(oc -n "${osac_namespace}" get configmap "${ca_bundle_configmap}" -o jsonpath='{.data.bundle\.pem}' 2>/dev/null || true)"
[[ -n "${ca_bundle}" ]] || \
    fail "required ConfigMap ${osac_namespace}/${ca_bundle_configmap} with data.bundle.pem was not found; ask the cluster administrator to provide the shared CA bundle"

oc -n "${keycloak_namespace}" get keycloaks.k8s.keycloak.org "${keycloak_instance}" >/dev/null 2>&1 || \
    fail "existing Keycloak ${keycloak_namespace}/${keycloak_instance} was not found"
keycloak_ready="$(oc -n "${keycloak_namespace}" get keycloaks.k8s.keycloak.org "${keycloak_instance}" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
[[ "${keycloak_ready}" == "True" ]] || \
    fail "existing Keycloak ${keycloak_namespace}/${keycloak_instance} is not Ready"
keycloak_host="$(oc -n "${keycloak_namespace}" get route "${keycloak_route}" -o jsonpath='{.spec.host}' 2>/dev/null || true)"
[[ -n "${keycloak_host}" ]] || \
    fail "existing Keycloak Route ${keycloak_namespace}/${keycloak_route} has no host"

[[ "$(oc auth can-i create keycloakrealmimports.k8s.keycloak.org -n "${keycloak_namespace}")" == "yes" ]] || \
    fail "current user may not create KeycloakRealmImports in ${keycloak_namespace}"
[[ "$(oc auth can-i create secrets -n "${keycloak_namespace}")" == "yes" ]] || \
    fail "current user may not create the OSAC client Secret in ${keycloak_namespace}"

oc -n openshift-cnv get hyperconverged kubevirt-hyperconverged >/dev/null 2>&1 || \
    fail "OpenShift Virtualization is not ready: HyperConverged openshift-cnv/kubevirt-hyperconverged is missing"
oc -n openshift-storage get lvmcluster -o name 2>/dev/null | grep -q . || \
    fail "Logical Volume Manager Storage has no LVMCluster in openshift-storage"
oc get storageclass "${lvms_storageclass}" >/dev/null 2>&1 || \
    fail "expected LVMS StorageClass ${lvms_storageclass} was not found"
oc -n metallb-system get ipaddresspool -o name 2>/dev/null | grep -q . || \
    fail "MetalLB has no IPAddressPool in metallb-system"

printf 'External VMaaS prerequisites are ready. Reusing Keycloak Route https://%s and isolated realm %s.\n' \
    "${keycloak_host}" "${osac_realm}"
