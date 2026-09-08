#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALLER_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
LIBRARY="${SCRIPT_DIR}/lib.sh"
DEV_VALUES="${INSTALLER_DIR}/values/dev/infra.yaml"
DEMO_VALUES="${INSTALLER_DIR}/values/dev/external-rhbk-demo-infra.yaml"

fail() {
    echo "ERROR: $*" >&2
    exit 1
}

fake_bin="$(mktemp -d)"
trap 'rm -rf "${fake_bin}"' EXIT
printf '%s\n' '#!/usr/bin/env bash' \
    'case "${1:-} ${2:-}" in' \
    '  "get clusterversion") printf "4.21.0\\n" ;;' \
    '  "get ingresses.config/cluster") printf "apps.example.invalid\\n" ;;' \
    '  *) exit 1 ;;' \
    'esac' > "${fake_bin}/oc"
chmod +x "${fake_bin}/oc"

expect_failure() {
    local values_file="$1"
    local expected="$2"
    local output

    if output=$(PATH="${fake_bin}:${PATH}" bash -c '
        set -euo pipefail
        source "$1"
        check_postgres_prerequisites test-namespace "$2"
    ' bash "${LIBRARY}" "${values_file}" 2>&1); then
        fail "expected PostgreSQL prerequisite check to fail for ${values_file}"
    fi
    [[ "${output}" == *"${expected}"* ]] || \
        fail "expected ${expected@Q} in output for ${values_file}, got: ${output}"
}

# A profile without bundled PostgreSQL must reach the external-database check
# under set -e rather than exiting after the status probe (OSAC-2113).
expect_failure "${DEV_VALUES}" "Secret osac-db-config not found in namespace test-namespace."
expect_failure "${fake_bin}/missing.yaml" "not found or unreadable"

if ! output=$(PATH="${fake_bin}:${PATH}" bash -c '
    set -euo pipefail
    source "$1"
    check_postgres_prerequisites test-namespace "$2"
' bash "${LIBRARY}" "${DEMO_VALUES}" 2>&1); then
    fail "expected bundled PostgreSQL profile to pass, got: ${output}"
fi
[[ "${output}" == *"bundledPostgres enabled"* ]] || \
    fail "expected bundled PostgreSQL success output, got: ${output}"

expect_make_values_override() {
    local target="$1"
    local expected="$2"
    local output

    if ! output=$(PATH="${fake_bin}:${PATH}" INFRA_VALUES="${DEMO_VALUES}" \
        make -C "${INSTALLER_DIR}" -n "${target}" PLATFORM=openshift PROFILE=dev \
        NS=test-namespace 2>&1); then
        fail "expected ${target} dry run to accept INFRA_VALUES from the environment, got: ${output}"
    fi
    [[ "${output}" == *"${expected}"* ]] || \
        fail "expected ${target} to contain ${expected@Q}, got: ${output}"
}

# Keep environment-style overrides working too: this is useful for CI and
# prevents Make's profile default from silently replacing a selected overlay.
expect_make_values_override install-infra "--values ${DEMO_VALUES}"
# Phase 3 uses instance values for Helm but passes infrastructure values to the
# PostgreSQL prerequisite check; verify that distinct consumption path.
expect_make_values_override install-osac "${DEMO_VALUES}"

if ! rendered_dependencies=$(helm template osac-deps "${INSTALLER_DIR}/charts/osac-deps" \
    --values "${DEMO_VALUES}" 2>&1); then
    fail "expected external RHBK demo dependencies to render, got: ${rendered_dependencies}"
fi
for subscription in kubevirt-hyperconverged multicluster-engine; do
    [[ "${rendered_dependencies}" == *"name: ${subscription}"* ]] || \
        fail "expected external RHBK demo to enable the ${subscription} subscription"
done

echo "PostgreSQL prerequisite checks passed."
