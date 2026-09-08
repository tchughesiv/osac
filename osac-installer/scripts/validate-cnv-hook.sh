#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOOK="${SCRIPT_DIR}/../charts/osac-infra/files/hooks/configure-cnv.sh"

# An available HyperConverged instance must make a post-upgrade hook exit
# without reapplying mutable CNV setup. Fail any unexpected oc invocation so
# this check cannot pass by continuing through the setup path.
if ! output=$(bash -c '
    oc() {
        case "$1 $2" in
            "get hyperconverged") printf "True" ;;
            "get csv")
                if [[ "${3:-}" == "kubevirt-hyperconverged-operator" ]]; then
                    printf "Succeeded"
                else
                    printf "kubevirt-hyperconverged-operator Succeeded\\n"
                fi
                ;;
            "get cdi"|"get kubevirt"|"get ssp") return 0 ;;
            "apply -f") exit 42 ;;
            *)
                printf "unexpected oc command: %s\\n" "$*" >&2
                exit 42
                ;;
        esac
    }
    source "$1"
' bash "${HOOK}" 2>&1); then
    printf 'ERROR: configure-cnv hook did not exit for an available HyperConverged instance:\n%s\n' "${output}" >&2
    exit 1
fi

[[ "${output}" == *"already available"* ]] || {
    printf 'ERROR: configure-cnv hook did not report the available fast path:\n%s\n' "${output}" >&2
    exit 1
}

echo "CNV hook idempotency check passed."
