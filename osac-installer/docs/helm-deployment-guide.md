# OSAC Helm Deployment Guide

Deploy OSAC on a clean connected OpenShift cluster using the three-phase
Helm install.

## Requirements

| Requirement | Details |
|-------------|---------|
| OpenShift | 4.17+ with cluster-admin access |
| CLI tools | `oc`, `helm`, `git`, `make` |
| Network | Outbound access to github.com, ghcr.io, quay.io, registry.redhat.io |
| AAP license | Subscription manifest (`license.zip`) from [Red Hat Customer Portal](https://access.redhat.com/) |

## Quick Start

```bash
git clone https://github.com/osac-project/osac.git
cd osac/osac-installer
make helm-deps

# Place your AAP license file
cp /path/to/license.zip values/vmaas-ci/

# Install (infra + osac)
make install PLATFORM=openshift PROFILE=vmaas-ci NS=osac
```

## How It Works

OSAC installs in three phases. Each phase's outputs are the next phase's
inputs. This is required because Helm validates all templates before applying
any — a template that references a CRD must have that CRD already registered
on the cluster.

### `make install-infra`

Installs infrastructure in two Helm releases:

1. **osac-deps** — OLM operator Subscriptions (cert-manager, AAP, LVMS, MetalLB,
   CNV, MCE). Post-install hooks wait for each operator's CSV to succeed and CRDs
   to register. Each operator is gated by a values toggle.

2. **osac-infra** — CRD instances: CertManager CR, ClusterIssuer, CA certificates,
   trust-manager Bundle, Keycloak, LVMCluster, HyperConverged, MetalLB IPAddressPool,
   controller credentials, and bundled PostgreSQL (dev/CI only).

### `make install-osac`

Installs OSAC: operator, fulfillment-service, AAP bootstrap, UI. All
prerequisites are ready - certificates issued, secrets created, CRDs
registered.

### Post-Install Hooks

After Phase 3, Helm runs post-install (and post-upgrade) hooks that
finalize the deployment:

| Hook | Weight | What it does |
|------|--------|-------------|
| `osac-publish-templates` | 20 | Publishes cluster templates to the fulfillment service catalog |

**Cluster template publishing** (`osac-publish-templates`): An init
container waits for the fulfillment REST gateway to be healthy (up to
600s), then launches the `osac-publish-templates` AAP job template and
polls until it completes. Helm blocks until this hook succeeds, so
`helm install` and `helm upgrade` will not report success until cluster
templates are available. The underlying Ansible role uses a PATCH/POST
pattern, making re-publish on upgrade safe and idempotent.

This hook is enabled by default (`aap.instanceGroups.publishTemplates.enabled: true`).
To disable it (e.g., in environments without CaaS):

```yaml
aap:
  instanceGroups:
    publishTemplates:
      enabled: false
```

## Values Files

Each profile has two files: `infra.yaml` (infrastructure config) and `instance.yaml` (OSAC instance config).

| Profile | Use case |
|---------|----------|
| `values/vmaas-ci/` | VMaaS CI (compute instances) |
| `values/caas-ci/` | CaaS CI (cluster provisioning) |
| `values/bmaas-ci/` | BMaaS CI (bare metal) |
| `values/dev/` | Local dev (Kind) |

Copy and customize for your environment:

```bash
mkdir -p values/my-env
cp values/dev/infra.yaml values/my-env/
cp values/dev/instance.yaml values/my-env/
# Edit to match your cluster
```

Key settings:

| Setting | Description |
|---------|-------------|
| `service.externalHostname` | Required. Set automatically by `make install-osac`. |
| `service.internalHostname` | Required. Set automatically by `make install-osac`. |
| `service.auth.issuerUrl` | Keycloak realm URL (default works for in-cluster Keycloak) |
| `operator.controllers.*` | Enable/disable individual controllers |

## CI/Dev-Only Features

These are top-level values, disabled by default. Enable only in CI/dev:

| Value | What it does |
|-------|-------------|
| `hubAccess.enabled` | Creates hub-access SA/RBAC and registers local cluster as a hub. Only for environments where fulfillment-service and hub are the same cluster. |
| `bundledPostgres.enabled` | Deploys a single-pod ephemeral PostgreSQL. Uses `fsync=off` and `emptyDir` — data lost on restart. Not for production. |
| `bundledVault.enabled` | Deploys a single-pod ephemeral OpenBao (Vault-compatible) secret store for testing. Dev mode — data is lost on restart. Not for production. |

## Infrastructure Configuration

### Keycloak Route with Public Ingress Certificates

For clusters with publicly-trusted wildcard ingress certificates (e.g., production OpenShift clusters with Let's Encrypt), you can configure Keycloak's Route to use `reencrypt` termination instead of `passthrough`:

```yaml
# values/<profile>/infra.yaml
keycloak:
  route:
    publicIngress: true  # Switches Route from passthrough to reencrypt
    hostname: keycloak.apps.example.com  # Optional: custom hostname
```

**How it works:**
- `passthrough` (default): Browser connects directly to Keycloak's internal TLS cert (self-signed CA)
- `reencrypt`: Router presents the cluster's public ingress cert to browsers, then re-encrypts traffic to Keycloak's internal TLS endpoint using the CA cert

**Requirements:**
- `caIssuer.enabled: true` (default) — The hook depends on cert-manager's CA bundle
- The `osac-infra-patch-keycloak-route` hook Job automatically sets `destinationCACertificate` at install/upgrade time

**When to use:**
- Production clusters where browser cert warnings are unacceptable
- Environments with corporate CA or Let's Encrypt ingress certs

### External Red Hat build of Keycloak

Set `keycloak.mode=external` when an existing RHBK instance provides identity
services. The `osac-infra` chart creates a new OSAC realm using a
`KeycloakRealmImport` in the configured provider namespace, along with a
dedicated Secret for OSAC client credentials. It does not create or take
ownership of the provider's namespace, Keycloak custom resource, route, or
other realms.

External mode requires `keycloak.external.namespace` and
`keycloak.external.instanceName`. The target Keycloak instance must be Ready and
the RHBK operator must provide `k8s.keycloak.org/v2alpha1`. The post-install hook
waits for the realm import to reach its `Done` condition before it creates the
credentials consumed by OSAC services.

When passing the external settings through `INFRA_HELM_ARGS`, construct that
variable with short shell assignments rather than wrapping one long quoted value.
An embedded newline becomes part of the Make recipe and leaves Helm with an
incomplete `--set-string` flag. The installer README has the copy-safe command.

For a short-lived external-RHBK demo, use
`values/dev/external-rhbk-demo-infra.yaml` as `INFRA_VALUES` in both installation
phases. It enables only ephemeral bundled PostgreSQL on top of the `dev` profile;
it does not enable the additional operators in the CI profiles. Durable installs
instead require external PostgreSQL connection and client-certificate Secrets.

Set `service.auth.issuerUrl`, `service.idp.url`, and
`service.vault.keycloakIssuerUrl` on the OSAC application release to the external
route and imported realm. The RHBK operator creates realms only: later chart
upgrades do not alter the imported realm, and uninstalling OSAC does not delete
it. The client-secret source is retained to allow a reinstall to use the same
realm; coordinate its eventual cleanup with the Keycloak administrator.

## Makefile Targets

All targets require `PLATFORM=kind|openshift PROFILE=dev|vmaas-ci|... NS=<namespace>`.

| Target | Description |
|--------|-------------|
| `make install` | Full install (infra + osac) |
| `make install-infra` | Infrastructure only (osac-deps + osac-infra) |
| `make install-osac` | OSAC instance only |
| `make uninstall` | Full uninstall (reverse order) |
| `make test` | Run integration tests (SUITE= required) |
| `make helm-lint` | Lint all charts |

## Uninstall

```bash
make uninstall PLATFORM=openshift PROFILE=vmaas-ci NS=osac
```

## Troubleshooting

### AAP Bootstrap Failing

```bash
oc logs -f job/osac-aap-bootstrap -n ${NAMESPACE}
oc get secret config-as-code-manifest-ig -n ${NAMESPACE}  # license exists?
```

### Fulfillment Pods CrashLooping

```bash
oc logs deployment/fulfillment-grpc-server -n ${NAMESPACE}
```

Common causes: missing `fulfillment-db` secret, cert-manager certificates
not issued (`oc get certificate -n ${NAMESPACE}`), missing controller
credentials.

### Helm Install Timeout

The AAP bootstrap hook can take 10-40 minutes. Monitor with:

```bash
oc logs -f job/osac-aap-bootstrap -n ${NAMESPACE}
```

### Template Publish Hook Failing

The `osac-publish-templates` post-install hook must complete for Helm to
report success. If it fails, cluster templates may be missing or incomplete
(on upgrade, previously published templates may still exist).

**Check hook pod status and logs:**

```bash
oc get pods -n ${NAMESPACE} | grep publish-templates
oc logs job/osac-publish-templates -n ${NAMESPACE} -c wait-for-fulfillment  # init container
oc logs job/osac-publish-templates -n ${NAMESPACE} -c publish-templates     # main container
```

**Common causes:**

- **Fulfillment service not ready** - The init container polls
  `https://fulfillment-rest-gateway:8000/healthz` for up to 600s. If it
  times out, check that the fulfillment service pods are running and the
  `fulfillment-rest-gateway` Service exists.
- **AAP token missing or empty** - The main container reads the `osac-aap-api-token`
  Secret and fails if the token is absent or empty. Verify the secret exists
  and contains a valid token:
  `oc get secret osac-aap-api-token -n ${NAMESPACE} -o jsonpath='{.data.token}' | base64 -d`.
- **AAP job template not found** - The `osac-publish-templates` job template
  must exist in AAP. Verify via AAP UI or API after the bootstrap job
  completes.
- **AAP job failure** - The hook logs include the AAP job stdout on failure.
  Check AAP for the job run details.

The hook has `backoffLimit: 6` and `activeDeadlineSeconds: 1300`. After
6 retries or 1300s, the Job fails and Helm reports the install as failed.

### Hook Job Failed

Failed hook pods are preserved for debugging (`hook-succeeded` delete
policy). Check logs:

```bash
oc get pods -n ${NAMESPACE} | grep -v Running | grep -v Completed
oc logs <failed-pod> -n ${NAMESPACE}
```
