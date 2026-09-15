# E2E runbook

How to actually run the OSAC Deployment MCP PoC end to end: a real browser OAuth login against a
real Keycloak, driving the four demo MCP tools against a real `fulfillment-service`, with real
per-user attribution. Three paths, depending on what you already have.

All paths below are relative to the root of this `osac` clone — cd back to that root between steps
(none of the commands chain a `cd` from one step into the next).

**Status: chart rendering and the catalog seeder are contract-tested; an end-to-end Kind browser
OAuth run remains a manual verification.** `install-mcp-demo` builds the fulfillment-service image
from this checkout, loads it into Kind, and deploys its MCP-server command. Known residual risk, by option:

- **Option A**: Kind's self-signed CA must be explicitly trusted by the local MCP client; a native
  IDE client may need the CA installed in the operating system trust store.
- **Option C, steps 1-2 (shared RHBK path)**: the external-Keycloak Helm rendering and lint checks
  pass, and Phase 1 was installed successfully on a shared OpenShift/RHBK cluster. It deliberately
  creates an isolated OSAC realm and never adopts an existing Keycloak namespace, custom resource,
  route, or realm. Do not add Helm ownership metadata to shared infrastructure as a workaround for
  an ownership error. Phase 3 and the live MCP demo remain to be exercised.
- **Option C, step 4 (install-osac)**: `publishTemplates.enabled: true` being the chart default (vs.
  kind's explicit `false`) is confirmed by reading `charts/osac/values.yaml`, but the actual AAP
  job-template-sync timing/behavior on a fresh real cluster is unobserved.
- **Option C, step 6**: the `:443` port on `--grpc-server-address` is inferred from how OpenShift
  Routes terminate TLS by default, not confirmed against a live Route.

Ping back with the actual error if any of these don't match what you see.

## Option A: Fresh kind cluster (self-contained)

No AAP license or pull secret is needed — `PLATFORM=kind` disables AAP entirely. The
`install-mcp-demo` target installs the normal Kind `dev` control plane, builds and deploys an MCP
endpoint from this checkout's fulfillment-service image, and seeds the one catalog chain this
Cluster-focused PoC needs.

### macOS: use `PROFILE=dev`, not `PROFILE=dev-full`

For this Cluster-based MCP PoC, `PROFILE=dev` is the intended local Kind path.
`PROFILE=dev-full` adds KubeVirt, AWX, a VM catalog, and the UI so it can demo
VMaaS—not the Cluster catalog used by these MCP tools. More importantly, upstream
[`KubeVirt does not support Kind on macOS`](https://kubevirt.io/quickstart_kind/)
because it requires KVM on the host kernel.
The Apple Silicon `origin-cli` image workaround does not change that limitation.

Use Docker Desktop or a running Podman machine as Kind's container provider.
When both clients are installed, select the provider explicitly:

```bash
# Docker Desktop:
export KIND_EXPERIMENTAL_PROVIDER=docker
export CONTAINER_TOOL=docker       # required when running `make test`

# Podman on macOS:
podman machine start
export KIND_EXPERIMENTAL_PROVIDER=podman
export CONTAINER_TOOL=podman       # required when running `make test`
```

`PROFILE=dev` supplies the control plane the PoC needs. The dedicated target
builds and loads the branch's fulfillment-service image, then adds an
in-cluster MCP endpoint and a ClusterVersion → HostType → ClusterTemplate →
published ClusterCatalogItem fixture. The template includes an explicit
cluster-version default, so the demo does not depend on a system-wide default
version. It does not provide a real HostedCluster backend, so a created Cluster
proves catalog selection, API authentication, and attribution—not a usable
OpenShift cluster for application deployment.

### 1. Boot infra + OSAC

From this branch (`OSAC-4388-deployment-mcp-poc`) — the Keycloak `osac-mcp-client` registration only
exists here:

```bash
cd osac-installer
make install-mcp-demo PLATFORM=kind PROFILE=dev NS=osac
export KUBECONFIG="$HOME/.kube/osac-dev-kind.kubeconfig"
kubectl get pods -n osac
```

If you ran an earlier checkout of this branch before the MCP OAuth client was
added, rerun the infrastructure target once before starting the local client.
The Kind-only post-upgrade fixture Job creates or updates just the public
`osac-mcp-client` through the Keycloak Admin API; it does not overwrite the
existing realm.

```bash
DEPS_HELM_ARGS='' INFRA_HELM_ARGS='' \
  make install-infra PLATFORM=kind PROFILE=dev NS=osac
```

`install-mcp-demo` uses a short-lived `admin` ServiceAccount token, a temporary
local port-forward, and the `ca-bundle` ConfigMap to seed the fixture through
the private API. It verifies TLS and is safe to rerun: existing fixtures are
reused by name and the template's version default is reconciled. To add or
repair the fixture on an already-installed Kind `dev` control plane without
reinstalling it, run:

```bash
make seed-mcp-demo-catalog PLATFORM=kind PROFILE=dev NS=osac
```

This also creates the `tenant1` OSAC tenant, matching Keycloak organization,
and dev-user membership required for resource creation.

If this command adds the organization membership, close the prior browser
login and rerun the client so Keycloak issues a token containing the
`organization` claim.

Then point your host at the MCP endpoint and Keycloak through Kind's Envoy Gateway:

```bash
echo '127.0.0.1 mcp.osac.svc.cluster.local' | sudo tee -a /etc/hosts
echo '127.0.0.1 keycloak.keycloak.svc.cluster.local' | sudo tee -a /etc/hosts
```

(kind's `extraPortMappings` in `kind-config.yaml` map host port 8443 → Envoy Gateway's HTTPS NodePort;
Envoy Gateway then routes by Host header/SNI to the right in-cluster service.)

### 2. Trust the cluster's CA

The in-cluster MCP server reads the namespace CA bundle automatically. The local
`mcp-oauth-demo-client` needs the same cert-manager-issued CA (self-signed and aggregated by
`trust-manager` into a ConfigMap):

```bash
mkdir -p /tmp/osac-ca
kubectl get configmap ca-bundle -n osac -o jsonpath='{.data.bundle\.pem}' \
  > /tmp/osac-ca/ca-bundle.pem
```

### 3. Confirm the in-cluster MCP server is ready

The server calls the internal fulfillment API directly and forwards each browser user's bearer token
to preserve attribution:

```bash
kubectl rollout status deployment/fulfillment-mcp-server -n osac --timeout=5m
```

### 4. Build and run the reference OAuth demo client

New terminal:

```bash
cd tools/mcp-oauth-demo-client
GOWORK=off go run . \
  -server-url https://mcp.osac.svc.cluster.local:8443 \
  -issuer https://keycloak.keycloak.svc.cluster.local:8443/realms/osac \
  -ca-file /tmp/osac-ca/ca-bundle.pem
```

A browser tab opens to Keycloak's login page (expect a self-signed-cert warning — click through it).
Log in as **`tenant1_user` / `foobar`** (a regular, non-admin dev-fixture tenant user — `devFixtures.enabled`
in `kind-infra.yaml`). After login, the terminal drives `list_catalog_items` →
`describe_catalog_item` → `create_cluster_from_catalog_item` → `get_cluster_status` and prints each
result. The cluster it creates will likely sit in a pending/error state since AAP isn't running on
kind — that's expected; the point of this demo is the OAuth handshake and attribution, not a
successful provision.

### 5. (Optional) Point a real IDE at it directly

To test the "zero custom client code needed" claim, add
`https://mcp.osac.svc.cluster.local:8443` as a remote MCP
server in Cursor's or Claude Desktop's MCP settings and see whether it drives its own native login,
no demo client involved. This will likely hit the same self-signed-CA trust problem the demo client's
`-ca-file` flag works around — the IDE has no equivalent flag, so this only works cleanly if
`/tmp/osac-ca/ca-bundle.pem` is also installed into the OS-level trust store. Treat this as a stretch
goal, not required to prove the core claim.

## Option B: Existing cluster-tool VMaaS/CaaS cluster

If you already have a cluster-tool-booted dev cluster, this is simpler — AAP is real there, so catalog
items are already published and hostnames are real OpenShift Routes (no `/etc/hosts` hack needed).

The one thing that cluster's Keycloak realm won't have yet, if it was booted from a flavor snapshot
that predates this branch, is the `osac-mcp-client` entry. Registering just that one client is much
smaller than a full `refresh-after-snapshot.py` stack refresh — do it directly against Keycloak's
admin REST API, mirroring `osac-installer`'s own `set-passwords.sh` pattern:

```bash
KEYCLOAK_URL="https://keycloak-keycloak.<your-cluster-domain>"
ADMIN_TOKEN=$(curl -sf -X POST "$KEYCLOAK_URL/realms/master/protocol/openid-connect/token" \
  -d grant_type=password -d client_id=admin-cli \
  -d username=admin -d password=<realm-admin-password> \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['access_token'])")

curl -sf -X POST "$KEYCLOAK_URL/admin/realms/osac/clients" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d @<(python3 -c "
import json
print(json.dumps({
  'clientId': 'osac-mcp-client',
  'publicClient': True,
  'standardFlowEnabled': True,
  'redirectUris': ['http://localhost:8091/callback'],
  'description': 'OAuth demo client for the fulfillment-service MCP server',
}))
")
```

(Or just re-copy the exact `osac-mcp-client` block from this branch's
`osac-installer/charts/osac-infra/files/realm.json` if you'd rather import it through the Keycloak
admin console UI.)

Then follow Option C's local-server/client steps, but point them at your real cluster's Route
hostnames rather than the Kind names. Omit `--ca-file` / `-ca-file` when the ingress certificate is
already trusted by your host (for example, a Let's Encrypt certificate).

## Option C: Existing OpenShift cluster with shared RHBK (`PLATFORM=openshift`)

Use this path when the cluster already provides Red Hat build of Keycloak (RHBK), for example for
its own SSO. It uses `keycloak.mode=external` to create a dedicated OSAC realm. The default
`keycloak.mode=managed` remains the right choice for a clean, dedicated cluster; do not use the
external settings below for that case.

### 1. Preflight the target without changing it

Set these names to the existing RHBK namespace, `Keycloak` custom resource, Route, and a new realm
name. `OSAC_REALM` must not name an existing realm: RHBK realm imports create realms but do not
update them. The example values match a common workshop layout, but the discovery commands are the
source of truth.

```bash
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"
export OSAC_NAMESPACE=osac-demo
export KEYCLOAK_NAMESPACE=keycloak
export KEYCLOAK_NAME=keycloak
export KEYCLOAK_ROUTE=keycloak
export OSAC_REALM=osac-demo

oc whoami
oc whoami --show-server
oc api-resources --api-group=k8s.keycloak.org
oc get keycloaks.k8s.keycloak.org -n "$KEYCLOAK_NAMESPACE"
oc get route -n "$KEYCLOAK_NAMESPACE"
oc get certmanager.operator.openshift.io/cluster
```

Confirm that the chosen `Keycloak` is Ready and select its externally reachable Route. Do not
annotate or label the existing RHBK resources for Helm ownership, and do not reuse the cluster's
existing SSO realm. Retrieve the Route, then derive the issuer from its OpenID
discovery document. This fails before Helm runs if the route, realm, or issuer
is unavailable; it requires `curl` and `jq`.

```bash
KEYCLOAK_ROUTE_HOST="$(oc get route "$KEYCLOAK_ROUTE" -n "$KEYCLOAK_NAMESPACE" -o jsonpath='{.spec.host}')"
[ -n "$KEYCLOAK_ROUTE_HOST" ] || { echo "ERROR: Route $KEYCLOAK_ROUTE has no host"; exit 1; }
KEYCLOAK_ROUTE_URL="https://${KEYCLOAK_ROUTE_HOST}"
KEYCLOAK_ISSUER="$(curl --fail --silent --show-error "$KEYCLOAK_ROUTE_URL/realms/$OSAC_REALM/.well-known/openid-configuration" | jq --exit-status --raw-output '.issuer')"
case "$KEYCLOAK_ISSUER" in
  */realms/"$OSAC_REALM") ;;
  *) echo "ERROR: discovery returned an invalid issuer: $KEYCLOAK_ISSUER"; exit 1 ;;
esac
export KEYCLOAK_URL="${KEYCLOAK_ISSUER%/realms/$OSAC_REALM}"
printf 'External Keycloak route: %s\nOSAC issuer: %s\n' "$KEYCLOAK_ROUTE_URL" "$KEYCLOAK_ISSUER"
```

### 2. Install infrastructure without adopting cert-manager or Keycloak

`DEPS_HELM_ARGS` applies only to the operator-subscription release. `INFRA_HELM_ARGS` applies only
to the infrastructure release. This distinction is important: the first skips the cluster-owned
cert-manager subscription, while the second imports the OSAC realm into the existing RHBK instance.

```bash
cd osac-installer
INFRA_HELM_ARGS='--set keycloak.mode=external'
INFRA_HELM_ARGS+=' --set-string keycloak.external.namespace=$KEYCLOAK_NAMESPACE'
INFRA_HELM_ARGS+=' --set-string keycloak.external.instanceName=$KEYCLOAK_NAME'
INFRA_HELM_ARGS+=' --set-string keycloak.external.realmName=$OSAC_REALM'
export INFRA_HELM_ARGS

KUBECONFIG="$KUBECONFIG" \
DEPS_HELM_ARGS='--set certManager.enabled=false' \
make INFRA_VALUES=values/dev/external-rhbk-demo-infra.yaml \
  install-infra PLATFORM=openshift PROFILE=dev NS="$OSAC_NAMESPACE"
```

Keep each `INFRA_HELM_ARGS` assignment on its own physical shell line. Do not
insert a newline inside a quoted value: Make expands it into the Helm recipe,
which leaves Helm with an incomplete `--set-string` flag.

The demo values file enables an ephemeral PostgreSQL instance in `osac-infra`
without enabling the broader CI-profile operator set. Use this same
`INFRA_VALUES` value in Phase 3; it is intentionally unsuitable for durable
installations because its PostgreSQL data is lost on restart.

This creates a `KeycloakRealmImport` named `osac-<realm>-realm` and an OSAC-only client-secret
source in the existing Keycloak namespace. It does not change the pre-existing Keycloak instance,
Route, or realms. Verify completion without printing Secret data:

```bash
oc get keycloakrealmimports.k8s.keycloak.org -n "$KEYCLOAK_NAMESPACE"
oc get secret osac-keycloak-client-secrets -n "$KEYCLOAK_NAMESPACE"
oc get pods -n osac-infra
```

In external mode, OSAC's shared `ca-bundle` automatically includes the standard
system CA roots, allowing in-cluster components to verify a publicly trusted
RHBK Route. Never bypass TLS verification. If the Route uses a private CA, have
the Keycloak or cluster administrator add that CA to the OSAC trust bundle before
continuing with Phase 3.

Wait for the updated bundle before starting Phase 3:

```bash
oc wait --for=condition=Synced bundles.trust.cert-manager.io/ca-bundle --timeout=5m
oc get configmap ca-bundle -n "$OSAC_NAMESPACE"
```

### 3. Install OSAC against the imported realm

The demo profile installs OpenShift Virtualization and MultiCluster Engine because
the enabled VMaaS and CaaS controllers require the KubeVirt and HyperShift APIs.
Those are cluster-scoped operators; obtain cluster-administrator approval before
using this profile. OLM creates their APIs asynchronously, so wait for them before
running Phase 3:

```bash
for crd in virtualmachines.kubevirt.io hostedclusters.hypershift.openshift.io; do
  oc wait --for=create "crd/$crd" --timeout=15m
  oc wait --for=condition=Established "crd/$crd" --timeout=15m
done
```

Phase 1 configures the `HyperConverged` resource once. Subsequent Phase 1
upgrades detect an already Available resource and do not reapply that mutable
CNV setup.

The one thing kind does not need is a real AAP `license.zip`. Give all OSAC services the external
realm's issuer URL in Phase 3:

```bash
EXTRA_HELM_ARGS="--set-string service.auth.issuerUrl=$KEYCLOAK_ISSUER"
EXTRA_HELM_ARGS+=" --set-string service.idp.url=$KEYCLOAK_URL"
EXTRA_HELM_ARGS+=" --set-string service.vault.keycloakIssuerUrl=$KEYCLOAK_ISSUER"
export EXTRA_HELM_ARGS

KUBECONFIG="$KUBECONFIG" \
make INFRA_VALUES=values/dev/external-rhbk-demo-infra.yaml \
  install-osac PLATFORM=openshift PROFILE=dev NS="$OSAC_NAMESPACE" AAP_LICENSE_FILE=/path/to/license.zip
```

The controller derives its Keycloak administration realm from
`service.auth.issuerUrl`, so keep the discovery-derived
`/realms/$OSAC_REALM` suffix there. `service.idp.url` is deliberately the
Keycloak base URL, without that suffix.

`install-osac` derives OSAC's Route hostnames from `oc get ingresses.config/cluster`; unlike kind,
there is no `/etc/hosts` step.

### 4. Trust the cluster's CA

Same mechanism as Option A step 2 — `trust-manager` aggregates the CA into the same `ca-bundle`
ConfigMap regardless of platform:

```bash
mkdir -p /tmp/osac-ca
kubectl get configmap ca-bundle -n "$OSAC_NAMESPACE" -o json \
  | python3 -c "import json,sys; [print(v) for v in json.load(sys.stdin)['data'].values()]" \
  > /tmp/osac-ca/ca-bundle.pem
```

`--ca-file` and `-ca-file` add this bundle to the normal system trust store. If the external
Keycloak Route is not already trusted by the host OS, obtain its public CA from the Keycloak or
cluster administrator and append it to this PEM file before starting the local processes. Never use
an insecure TLS bypass for the demo.

### 5. Catalog items publish automatically — no manual seeding needed

Unlike kind, `publishTemplates.enabled: true` is the chart default (kind is the one platform that
turns it off) — real AAP syncs the `osac.templates` collection into published `ClusterCatalogItem`s
on its own. Give it a few minutes after `install-osac` finishes; confirm via the demo client's own
`list_catalog_items` tool in step 7 rather than a separate CLI check.

### 6. Build and run `mcp-server` locally

Same as Option A step 5, but pointed at the cluster's real Route hostnames instead of kind's
`*.svc.cluster.local` names:

```bash
cd fulfillment-service
go build -o /tmp/fulfillment-service ./cmd/fulfillment-service
DOMAIN=$(oc get ingresses.config/cluster -o jsonpath='{.spec.domain}')

/tmp/fulfillment-service start mcp-server \
  --grpc-server-address "fulfillment-internal-api-${OSAC_NAMESPACE}.${DOMAIN}:443" \
  --ca-file /tmp/osac-ca/ca-bundle.pem \
  --http-listener-address localhost:8001 \
  --grpc-authn-trusted-token-issuers "$KEYCLOAK_URL/realms/$OSAC_REALM" \
  --oauth-authorization-server "$KEYCLOAK_URL/realms/$OSAC_REALM" \
  --oauth-resource-url http://localhost:8001
```

The `:443` on `--grpc-server-address` is inferred from how OpenShift Routes terminate TLS by
default, matching Option B's own Route-based wiring — not independently confirmed against a live
Route in this session.

### 7. Build and run the reference OAuth demo client

Same as Option A step 6, pointed at the same real issuer:

```bash
cd tools/mcp-oauth-demo-client
GOWORK=off go run . \
  -server-url http://localhost:8001 \
  -issuer "$KEYCLOAK_URL/realms/$OSAC_REALM" \
  -ca-file /tmp/osac-ca/ca-bundle.pem
```

The realm import creates, by default, an `osac-admin` login with a generated password held in the
OSAC-only Secret above; obtain it through the normal Keycloak-administrator process and do not
place it in shell history, tickets, or terminal recordings. Since AAP is real here, the created cluster has an
actual chance of reaching `READY` — a good opportunity to also exercise `get_cluster_status`'s
poll-until-ready path, not just kind's immediate-`PROGRESSING` response.

## Cleanup

- kind: `make -C osac-installer uninstall PLATFORM=kind PROFILE=dev NS=osac` (also deletes the
  kind cluster itself, per the Makefile's `uninstall-infra` kind branch).
- Option C (`PLATFORM=openshift`): `make -C osac-installer uninstall PLATFORM=openshift PROFILE=vmaas-ci NS=osac` (`AAP_LICENSE_FILE` isn't checked by the uninstall targets, only `install-osac`).
- The demo cluster the demo client creates is **not** deleted automatically — clean it up via the
  `osac` CLI or console.
