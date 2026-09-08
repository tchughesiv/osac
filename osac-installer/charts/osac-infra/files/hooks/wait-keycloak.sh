#!/usr/bin/env bash
set -euo pipefail

KEYCLOAK_MODE="${KEYCLOAK_MODE:-managed}"
KEYCLOAK_NAMESPACE="${KEYCLOAK_NAMESPACE:-keycloak}"

if [[ "${KEYCLOAK_MODE}" == "external" ]]; then
    KEYCLOAK_REALM_IMPORT_NAME="${KEYCLOAK_REALM_IMPORT_NAME:?KEYCLOAK_REALM_IMPORT_NAME is required for external Keycloak}"
    echo "Waiting for external Keycloak realm import ${KEYCLOAK_REALM_IMPORT_NAME}..."
    oc wait --for=condition=Done "keycloakrealmimports/${KEYCLOAK_REALM_IMPORT_NAME}" \
        -n "${KEYCLOAK_NAMESPACE}" --timeout=600s
else
    echo "Waiting for keycloak-service deployment..."
    oc wait --for=condition=Available deploy/keycloak-service -n "${KEYCLOAK_NAMESPACE}" --timeout=600s
fi

echo "Keycloak is ready."
