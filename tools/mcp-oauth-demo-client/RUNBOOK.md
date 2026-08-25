# E2E runbook

How to actually run the OSAC Deployment MCP PoC end to end: a real browser OAuth login against a
real Keycloak, driving the four demo MCP tools against a real `fulfillment-service`, with real
per-user attribution. Two paths, depending on what you already have.

All paths below are relative to the root of this `osac` clone — cd back to that root between steps
(none of the commands chain a `cd` from one step into the next).

**Status: derived from reading the source/Helm charts/CI workflow, not personally exercised against
a live cluster in this session** — this sandbox's local podman/kind networking (`gvproxy`) is broken
(see the chat for the diagnosis), so none of this was run live here. The `mcp-server` flags, hosts
file entries, and CA-bundle extraction are all confirmed by cross-referencing existing code
(`it_tool.go`, `integration-tests.yml`, the Helm values files) rather than guessed. The one place with
real residual risk is the catalog-item-seeding `grpcurl` payloads in Option A step 5 — the field names
are read straight off the `.proto` files, but no live server has validated them. Ping back with the
actual error if one of those doesn't match.

## Option A: Fresh kind cluster (self-contained)

No AAP license or pull secret needed — `PLATFORM=kind` disables AAP entirely, so this is the fastest
self-serve path, at the cost of one extra manual step (5) that a real AAP-backed cluster (Option B)
doesn't need.

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

## Cleanup

- kind: `make -C osac-installer uninstall PLATFORM=kind PROFILE=dev NS=osac` (also deletes the
  kind cluster itself, per the Makefile's `uninstall-infra` kind branch).
- The demo cluster the demo client creates is **not** deleted automatically — clean it up via the
  `osac` CLI or console.
