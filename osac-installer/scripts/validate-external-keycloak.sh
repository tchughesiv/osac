#!/usr/bin/env bash
set -euo pipefail

python3 -c "import yaml" 2>/dev/null || {
    echo "ERROR: PyYAML is required to validate external Keycloak rendering." >&2
    exit 1
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="${SCRIPT_DIR}/../charts/osac-infra"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

helm template osac-infra "${CHART_DIR}" >"${TMP_DIR}/managed.yaml"
helm template osac-infra "${CHART_DIR}" \
    --set keycloak.mode=external \
    --set keycloak.external.namespace=external-keycloak \
    --set keycloak.external.instanceName=workshop-keycloak \
    --set keycloak.external.realmName=osac-demo \
    --set keycloak.external.clientSecretName=osac-demo-client-secrets >"${TMP_DIR}/external.yaml"

python3 - "${TMP_DIR}/managed.yaml" "${TMP_DIR}/external.yaml" <<'PY'
import sys
import yaml


def documents(path):
    with open(path) as stream:
        return [document for document in yaml.safe_load_all(stream) if document]


def resource(items, kind, name, namespace=None):
    for item in items:
        metadata = item.get("metadata", {})
        if item.get("kind") == kind and metadata.get("name") == name:
            if namespace is None or metadata.get("namespace") == namespace:
                return item
    return None


def environment(item, container):
    containers = item["spec"]["template"]["spec"].get("containers", [])
    for candidate in containers:
        if candidate.get("name") == container:
            return {entry["name"]: entry.get("value") for entry in candidate.get("env", [])}
    return {}


managed = documents(sys.argv[1])
external = documents(sys.argv[2])
errors = []

if resource(managed, "Namespace", "keycloak") is None:
    errors.append("managed mode must create the installer-owned keycloak namespace")
if resource(managed, "KeycloakRealmImport", "osac-osac-realm", "keycloak") is not None:
    errors.append("managed mode must not create an external Keycloak realm import")

if resource(external, "Namespace", "keycloak") is not None:
    errors.append("external mode must not create or adopt the keycloak namespace")
if resource(external, "Deployment", "keycloak-service", "keycloak") is not None:
    errors.append("external mode must not deploy the bundled Keycloak service")
if resource(external, "StatefulSet", "keycloak-database", "keycloak") is not None:
    errors.append("external mode must not deploy the bundled Keycloak database")

bundle = resource(external, "Bundle", "ca-bundle")
if bundle is None:
    errors.append("external mode must render the shared CA bundle")
elif {"useDefaultCAs": True} not in bundle.get("spec", {}).get("sources", []):
    errors.append("external mode must add system CA roots for the external Keycloak Route")

secret = resource(external, "Secret", "osac-demo-client-secrets", "external-keycloak")
if secret is None:
    errors.append("external mode must create the OSAC client-secret source")
elif secret.get("metadata", {}).get("annotations", {}).get("helm.sh/resource-policy") != "keep":
    errors.append("external client-secret source must survive Helm uninstall")

realm_import = resource(external, "KeycloakRealmImport", "osac-osac-demo-realm", "external-keycloak")
if realm_import is None:
    errors.append("external mode must create a KeycloakRealmImport in the provider namespace")
else:
    spec = realm_import.get("spec", {})
    if spec.get("keycloakCRName") != "workshop-keycloak":
        errors.append("realm import must target the configured Keycloak CR")
    if spec.get("realm", {}).get("realm") != "osac-demo":
        errors.append("realm import must use the configured OSAC realm name")
    controller_source = spec.get("placeholders", {}).get("OSAC_CONTROLLER_CLIENT_SECRET", {}).get("secret", {})
    if controller_source != {"name": "osac-demo-client-secrets", "key": "osac-controller"}:
        errors.append("realm import must source controller credentials from the OSAC secret")

wait_job = resource(external, "Job", "osac-infra-wait-keycloak", "default")
wait_environment = environment(wait_job, "wait-keycloak") if wait_job else {}
expected_wait = {
    "KEYCLOAK_MODE": "external",
    "KEYCLOAK_NAMESPACE": "external-keycloak",
    "KEYCLOAK_REALM_IMPORT_NAME": "osac-osac-demo-realm",
}
if {name: wait_environment.get(name) for name in expected_wait} != expected_wait:
    errors.append("external readiness hook must wait for the configured realm import")

credentials_job = resource(external, "Job", "osac-infra-create-creds", "default")
credentials_environment = environment(credentials_job, "create-creds") if credentials_job else {}
if credentials_environment.get("KEYCLOAK_NAMESPACE") != "external-keycloak" or credentials_environment.get("KEYCLOAK_CLIENT_SECRET_NAME") != "osac-demo-client-secrets":
    errors.append("controller credential hook must use the external Keycloak secret")

if errors:
    raise SystemExit("\n".join(errors))
PY

if helm template osac-infra "${CHART_DIR}" --set keycloak.mode=unsupported >/dev/null 2>&1; then
    echo "ERROR: unsupported keycloak.mode rendered successfully" >&2
    exit 1
fi

if helm template osac-infra "${CHART_DIR}" --set keycloak.mode=external >/dev/null 2>&1; then
    echo "ERROR: external Keycloak rendered without a namespace and Keycloak CR name" >&2
    exit 1
fi

echo "External Keycloak rendering checks passed."
