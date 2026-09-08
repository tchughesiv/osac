# E2E runbook

How to actually run the OSAC Deployment MCP PoC end to end: a real browser OAuth login against a
real Keycloak, driving the four demo MCP tools against a real `fulfillment-service`, with real
per-user attribution. Three paths, depending on what you already have.

All paths below are relative to the root of this `osac` clone — cd back to that root between steps
(none of the commands chain a `cd` from one step into the next).

**Status: derived from reading the source/Helm charts/CI workflow, not personally exercised against
a live cluster in this session** — this sandbox's local podman/kind networking (`gvproxy`) is broken
(see the chat for the diagnosis), so none of this was run live here. The `mcp-server` flags, hosts
file entries, and CA-bundle extraction are all confirmed by cross-referencing existing code
(`it_tool.go`, `integration-tests.yml`, the Helm values files) rather than guessed. Known residual
risk, by option:

- **Option A, step 4**: the catalog-item-seeding `grpcurl` payloads — field names read straight off
  the `.proto` files, but no live server has validated them.
- **Option C, step 2 (install-infra)**: this *was* attempted live, against a cluster that turned out
  to have pre-existing `cert-manager`/`openshift-storage`/`keycloak` infra from an earlier manual
  install. Working around Helm's "exists and cannot be imported" errors by labeling/annotating those
  pre-existing objects with Helm ownership metadata — the obvious-looking fix — is a real footgun: it
  let a later `helm upgrade --install osac-infra` overwrite a pre-existing RHBK `Keycloak` custom
  resource that was *also* backing that cluster's own OpenShift console SSO, breaking it, with no
  Helm revision to roll back to (it was that release's first successful install). **Do not do this**
  — see the preflight check in step 1 below. On a genuinely clean cluster (no prior manual
  cert-manager/Keycloak install), `install-infra` itself is expected to work cleanly; that part
  remains unexercised live.
- **Option C, step 4 (install-osac)**: `publishTemplates.enabled: true` being the chart default (vs.
  kind's explicit `false`) is confirmed by reading `charts/osac/values.yaml`, but the actual AAP
  job-template-sync timing/behavior on a fresh real cluster is unobserved.
- **Option C, step 6**: the `:443` port on `--grpc-server-address` is inferred from how OpenShift
  Routes terminate TLS by default, not confirmed against a live Route.

Ping back with the actual error if any of these don't match what you see.

## Option A: Fresh kind cluster (self-contained)

No AAP license or pull secret needed — `PLATFORM=kind` disables AAP entirely, so this is the fastest
self-serve path, at the cost of one extra manual step (4) that a real AAP-backed cluster (Option B
or C) doesn't need.

### 1. Boot infra + OSAC

From this branch (`OSAC-4388-deployment-mcp-poc`) — the Keycloak `osac-mcp-client` registration only
exists here:

```bash
cd osac-installer
make install-infra PLATFORM=kind PROFILE=dev NS=osac
```

Then point your host at the cluster's internal service names (mirrors exactly what
`.github/workflows/integration-tests.yml` does for its own kind-based IT runs):

```bash
echo '127.0.0.1 fulfillment-api.osac.svc.cluster.local' | sudo tee -a /etc/hosts
echo '127.0.0.1 fulfillment-internal-api.osac.svc.cluster.local' | sudo tee -a /etc/hosts
echo '127.0.0.1 keycloak.keycloak.svc.cluster.local' | sudo tee -a /etc/hosts
```

(kind's `extraPortMappings` in `kind-config.yaml` map host port 8443 → Envoy Gateway's HTTPS NodePort;
Envoy Gateway then routes by Host header/SNI to the right in-cluster service.)

```bash
make install-osac PLATFORM=kind PROFILE=dev NS=osac
export KUBECONFIG="$HOME/.kube/osac-dev-kind.kubeconfig"
kubectl get pods -n osac   # wait for everything Running before continuing
```

### 2. Trust the cluster's CA

Both `mcp-server` and `mcp-oauth-demo-client` need to trust the cluster's cert-manager-issued CA
(self-signed, aggregated by `trust-manager` into a ConfigMap):

```bash
mkdir -p /tmp/osac-ca
kubectl get configmap ca-bundle -n osac -o json \
  | python3 -c "import json,sys; [print(v) for v in json.load(sys.stdin)['data'].values()]" \
  > /tmp/osac-ca/ca-bundle.pem
```

### 3. Get an admin token

Used only for seeding fixtures (step 5) — the `fulfillment-service` Helm subchart creates an `admin`
ServiceAccount that's on the server's `emergencyServiceAccounts` trust list (Kubernetes-issued tokens
validated directly, no Keycloak round-trip):

```bash
TOKEN=$(kubectl create token admin -n osac --duration=1h)
```

### 4. (Skip if you already have a published `ClusterCatalogItem`)

`PLATFORM=kind` disables AAP and the `osac-publish-templates` hook, so a fresh kind cluster has no
catalog items to demo against. Seed one minimal `HostType` → `ClusterTemplate` → published
`ClusterCatalogItem` via the private API (gRPC reflection is on, so `grpcurl` needs no `.proto`
files):

```bash
HT_ID=$(uuidgen); TMPL_ID=$(uuidgen); CI_ID=$(uuidgen)

grpcurl -cacert /tmp/osac-ca/ca-bundle.pem -H "Authorization: Bearer $TOKEN" \
  -d "{\"object\":{\"id\":\"$HT_ID\",\"metadata\":{\"name\":\"mcp-demo-host-type\"},\"title\":\"MCP demo host type\"}}" \
  fulfillment-internal-api.osac.svc.cluster.local:8443 osac.private.v1.HostTypes/Create

grpcurl -cacert /tmp/osac-ca/ca-bundle.pem -H "Authorization: Bearer $TOKEN" \
  -d "{\"object\":{\"id\":\"$TMPL_ID\",\"metadata\":{\"name\":\"mcp-demo-template\"},\"title\":\"MCP demo template\",\"nodeSets\":{\"workers\":{\"hostType\":{\"id\":\"$HT_ID\"},\"size\":3}}}}" \
  fulfillment-internal-api.osac.svc.cluster.local:8443 osac.private.v1.ClusterTemplates/Create

grpcurl -cacert /tmp/osac-ca/ca-bundle.pem -H "Authorization: Bearer $TOKEN" \
  -d "{\"object\":{\"id\":\"$CI_ID\",\"metadata\":{\"name\":\"mcp-demo-catalog-item\"},\"title\":\"MCP demo catalog item\",\"description\":\"Seeded for the OSAC Deployment MCP PoC demo.\",\"template\":{\"id\":\"$TMPL_ID\"},\"published\":true}}" \
  fulfillment-internal-api.osac.svc.cluster.local:8443 osac.private.v1.ClusterCatalogItems/Create
```

### 5. Build and run `mcp-server` locally

It runs as a plain local process — no in-cluster deployment needed, since it just talks to the
already-deployed public gRPC API like any external client would:

```bash
cd fulfillment-service
go build -o /tmp/fulfillment-service ./cmd/fulfillment-service

/tmp/fulfillment-service start mcp-server \
  --grpc-server-address fulfillment-api.osac.svc.cluster.local:8443 \
  --ca-file /tmp/osac-ca/ca-bundle.pem \
  --http-listener-address localhost:8001 \
  --grpc-authn-trusted-token-issuers https://keycloak.keycloak.svc.cluster.local:8443/realms/osac \
  --oauth-authorization-server https://keycloak.keycloak.svc.cluster.local:8443/realms/osac \
  --oauth-resource-url http://localhost:8001
```

Leave this running in its own terminal.

### 6. Build and run the reference OAuth demo client

New terminal:

```bash
cd tools/mcp-oauth-demo-client
GOWORK=off go run . \
  -server-url http://localhost:8001 \
  -issuer https://keycloak.keycloak.svc.cluster.local:8443/realms/osac \
  -ca-file /tmp/osac-ca/ca-bundle.pem
```

A browser tab opens to Keycloak's login page (expect a self-signed-cert warning — click through it).
Log in as **`user` / `foobar`** (a regular, non-admin dev-fixture tenant user — `devFixtures.enabled`
in `kind-infra.yaml`). After login, the terminal drives `list_catalog_items` →
`describe_catalog_item` → `create_cluster_from_catalog_item` → `get_cluster_status` and prints each
result. The cluster it creates will likely sit in a pending/error state since AAP isn't running on
kind — that's expected; the point of this demo is the OAuth handshake and attribution, not a
successful provision.

### 7. (Optional) Point a real IDE at it directly

To test the "zero custom client code needed" claim, add `http://localhost:8001` as a remote MCP
server in Cursor's or Claude Desktop's MCP settings and see whether it drives its own native login,
no demo client involved. This will likely hit the same self-signed-CA trust problem the demo client's
`-ca-file` flag works around — the IDE has no equivalent flag, so this only works cleanly if
`/tmp/osac-ca/ca-bundle.pem` is also installed into the OS-level trust store. Treat this as a stretch
goal, not required to prove the core claim.

## Option B: Existing cluster-tool VMaaS/CaaS cluster

If you already have a cluster-tool-booted dev cluster, this is simpler — AAP is real there, so catalog
items are already published (skip step 4/5 above entirely), and hostnames are real OpenShift Routes
(no `/etc/hosts` hack needed).

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

Then run `mcp-server` and the demo client exactly as in Option A steps 5-6, but pointed at your real
cluster's Route hostnames instead of the `*.svc.cluster.local` kind names, and without `--ca-file` /
`-ca-file` at all if that cluster's ingress cert is issued by a CA your host already trusts (e.g. a
real Let's Encrypt cert, unlike kind's self-signed one).

## Option C: Existing OpenShift cluster (`PLATFORM=openshift`, no cluster-tool)

If you already have `oc` cluster-admin access to a real OpenShift 4.x cluster — not a fresh kind
cluster, not booted via cluster-tool — `osac-installer` supports installing OSAC directly onto it.
**This only works cleanly on a cluster with no pre-existing OSAC/cert-manager/Keycloak install** —
see step 1.

### 1. Preflight: confirm this is a clean cluster

`osac-infra`'s chart hardcodes a fixed set of namespaces and resource names (`cert-manager`,
`cert-manager-operator`, `openshift-storage`, and a `keycloak` namespace with a `keycloak-tls`
Certificate, a `keycloak-database` StatefulSet, etc.) — it does not parameterize any of them.
If objects with those exact names already exist (from any earlier manual install, a different
Keycloak/cert-manager setup, or a workshop/demo catalog's own bootstrap), Helm will refuse to
install with an "exists and cannot be imported" error.

**Do not work around that error by labeling/annotating the pre-existing objects with Helm ownership
metadata.** This was tried live and it breaks things: adopting a pre-existing `keycloak` namespace
let a later `helm upgrade --install osac-infra` overwrite a pre-existing `Keycloak` custom resource
that also happened to back that cluster's own OpenShift console SSO — taking it down, with no prior
Helm revision to roll back to. Check first:

```bash
oc get namespace cert-manager-operator cert-manager openshift-storage keycloak 2>&1
oc get oauth cluster -o jsonpath='{.spec.identityProviders[*].name}{"\n"}'
```

If any of those namespaces already exist, or the second command prints any identity provider
names, **stop** — this cluster already has infra `osac-infra`'s chart doesn't expect to share, and
proceeding risks breaking it exactly as above. Use a different, genuinely clean OpenShift cluster
for this option instead (one where nobody — including a workshop/demo catalog's own bootstrap — has
already installed cert-manager or Keycloak by hand).

### 2. Install infra

`PROFILE=vmaas-ci` bundles everything (Postgres, cert-manager, Keycloak, AAP via OLM) the same way
kind/cluster-tool do, despite the CI-sounding name — it's the exact profile `cluster-tool`'s own
`refresh-after-snapshot.py` uses to install OSAC onto its real OpenShift VMs, so it's expected to
behave identically against any other clean real OpenShift cluster.

```bash
cd osac-installer
oc login ...   # however you normally authenticate to this cluster
make install-infra PLATFORM=openshift PROFILE=vmaas-ci NS=osac
```

Wait for it to finish and verify before moving on — don't proceed to step 3 if anything here is
still `Pending`/`ContainerCreating`/degraded:

```bash
oc get pods -n keycloak
oc get pods -n cert-manager
oc get keycloak -n keycloak   # status.conditions should show Ready: True
```

### 3. Install OSAC

The one thing kind doesn't need that this does: a real AAP `license.zip`.

```bash
make install-osac PLATFORM=openshift PROFILE=vmaas-ci NS=osac AAP_LICENSE_FILE=/path/to/license.zip
```

`install-osac` derives the Route hostnames itself from `oc get ingresses.config/cluster` — no
manual `/etc/hosts` step needed, unlike kind.

### 4. Trust the cluster's CA

Same mechanism as Option A step 2 — `trust-manager` aggregates the CA into the same `ca-bundle`
ConfigMap regardless of platform:

```bash
mkdir -p /tmp/osac-ca
kubectl get configmap ca-bundle -n osac -o json \
  | python3 -c "import json,sys; [print(v) for v in json.load(sys.stdin)['data'].values()]" \
  > /tmp/osac-ca/ca-bundle.pem
```

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
  --grpc-server-address "fulfillment-internal-api-osac.${DOMAIN}:443" \
  --ca-file /tmp/osac-ca/ca-bundle.pem \
  --http-listener-address localhost:8001 \
  --grpc-authn-trusted-token-issuers "https://keycloak-keycloak.${DOMAIN}/realms/osac" \
  --oauth-authorization-server "https://keycloak-keycloak.${DOMAIN}/realms/osac" \
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
  -issuer "https://keycloak-keycloak.${DOMAIN}/realms/osac" \
  -ca-file /tmp/osac-ca/ca-bundle.pem
```

Log in as `user`/`foobar` (the same `devFixtures` fixture `vmaas-ci` shares with kind). Since AAP is
real here, the created cluster has an actual chance of reaching `READY` — a good opportunity to
also exercise `get_cluster_status`'s poll-until-ready path, not just kind's immediate-`PROGRESSING`
response.

## Cleanup

- kind: `make -C osac-installer uninstall PLATFORM=kind PROFILE=dev NS=osac` (also deletes the
  kind cluster itself, per the Makefile's `uninstall-infra` kind branch).
- Option C (`PLATFORM=openshift`): `make -C osac-installer uninstall PLATFORM=openshift PROFILE=vmaas-ci NS=osac` (`AAP_LICENSE_FILE` isn't checked by the uninstall targets, only `install-osac`).
- The demo cluster the demo client creates is **not** deleted automatically — clean it up via the
  `osac` CLI or console.
