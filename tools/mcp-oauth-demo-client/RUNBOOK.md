# OSAC Deployment MCP VMaaS PoC runbook

This runbook demonstrates browser OAuth and a tenant-facing ComputeInstance
lifecycle through the OSAC Deployment MCP server. The target is OpenShift
Virtualization through `PLATFORM=openshift PROFILE=vmaas-ci`; it does not use
Kind or create a CaaS cluster.

The resulting VM is infrastructure only. This phase does not deploy an
application, inject cloud-init content, or create the VM's platform and network
dependencies dynamically.

## 1. Install the VMaaS MCP demo

Use a dedicated OpenShift demo cluster. The installer manages VMaaS
prerequisites and Keycloak, so do not use this target to adopt shared
cluster-scoped infrastructure or an existing shared Keycloak installation.

The cluster must be able to pull `MCP_DEMO_IMAGE`; a local `localhost/...`
image reference cannot work. Sign in to the target registry first and use an
explicit image tag.

```bash
export NS=osac
export AAP_LICENSE_FILE=/absolute/path/to/license.zip
export REGISTRY_USER=your-registry-user
export MCP_DEMO_IMAGE="quay.io/${REGISTRY_USER}/fulfillment-service:osac-4388"

podman login quay.io
make -C osac-installer install-mcp-demo \
  PLATFORM=openshift PROFILE=vmaas-ci NS="$NS" \
  AAP_LICENSE_FILE="$AAP_LICENSE_FILE" \
  MCP_DEMO_IMAGE="$MCP_DEMO_IMAGE"
```

On macOS with Podman, the target streams the Linux build context into the
Podman machine, then pushes the image. It deploys the same image with
`imagePullPolicy: Always`, so rerunning the command after changing MCP code
causes the new image to be fetched even if the tag is unchanged.

The catalog seeder uses a short-lived `admin` service-account token, a
temporary port-forward, and the namespace `ca-bundle` to make TLS-verified
private API calls. It requires:

- OpenShift Virtualization/KubeVirt and CDI.
- A ready `kubevirt-hyperconverged` resource, hub access, a published
  `ocp-virt-vm` template, and a block StorageTier.
- One ready default VirtualNetwork, Subnet, and SecurityGroup for the demo
  tenant. The default tenant is `osac-e2e-ci`; select an existing prepared
  tenant with `MCP_DEMO_TENANT=<tenant>` if needed.

It creates or reuses `mcp-demo-fedora`, `mcp-demo-small`, and
`mcp-demo-compute-instance`. It refuses to mutate an incompatible existing
catalog item. Recreate the demo environment instead of attempting a migration.

## 2. Discover and verify the endpoints

Derive all hostnames from OpenShift. Do not copy a hostname from another
cluster or use a `.svc.cluster.local` name on the Mac.

```bash
DOMAIN="$(oc get ingresses.config/cluster -o jsonpath='{.spec.domain}')"
MCP_URL="https://mcp-${NS}.${DOMAIN}"
KEYCLOAK_HOST="$(oc get route keycloak-keycloak -n keycloak \
  -o jsonpath='{.spec.host}')"
KEYCLOAK_URL="https://${KEYCLOAK_HOST}"
KEYCLOAK_DISCOVERY="${KEYCLOAK_URL}/realms/osac/.well-known/openid-configuration"
KEYCLOAK_ISSUER="$(curl --fail --silent --show-error "$KEYCLOAK_DISCOVERY" \
  | jq --raw-output '.issuer')"

oc rollout status deployment/fulfillment-mcp-server -n "$NS" --timeout=5m
printf 'MCP endpoint: %s\nIssuer: %s\n' "$MCP_URL" "$KEYCLOAK_ISSUER"
```

If the ingress certificate is not trusted by the workstation, write the
namespace CA bundle to a local file and pass it to the reference client or
Inspector:

```bash
mkdir -p /tmp/osac-ca
oc get configmap ca-bundle -n "$NS" -o jsonpath='{.data.bundle\.pem}' \
  > /tmp/osac-ca/ca-bundle.pem
```

## 3. Run the OAuth reference client

Use an OSAC user that belongs to the selected `MCP_DEMO_TENANT` Keycloak
organization. The installer registers the public `osac-mcp-client` with the
loopback callback used below; it has no client secret. Get or create the user's
credentials through the cluster's Keycloak administrator—do not place them in
this runbook or shell history.

```bash
GOWORK=off go run ./tools/mcp-oauth-demo-client \
  -server-url "$MCP_URL" \
  -issuer "$KEYCLOAK_ISSUER" \
  -delete
```

The browser login returns to `http://localhost:8091/callback`. The client lists
ComputeInstance catalog items, inspects the selected item, creates a VM, polls
it through MCP, then removes it because `-delete` was supplied. Omit `-delete`
to retain the VM for inspection; clean it up later with
`delete_compute_instance` or the OSAC CLI/console.

The MCP server exposes exactly four tools:

- `list_resources`: list the allowlisted `compute_instance_catalog_item` or
  `compute_instance` resource type. Page size defaults to 50 and cannot exceed
  100.
- `get_resource`: fetch one allowlisted resource by ID.
- `create_compute_instance_from_catalog_item`: create a VM from a published
  ComputeInstance catalog item, applying only catalog-authorized field overrides
  and existing default networking.
- `delete_compute_instance`: delete one VM by ID.

`list_resources` and `get_resource` are deliberately not generic API
forwarders. The service validates the caller token and forwards it to the
fulfillment API, so normal tenant authorization and attribution still apply.

## 4. Explore with MCP Inspector

The checked-in Inspector configuration supplies the static public client ID but
uses a placeholder URL. Create a temporary configuration with the discovered
endpoint:

```bash
INSPECTOR_CONFIG="$(mktemp /tmp/osac-mcp-inspector-XXXXXX.json)"
jq --arg url "$MCP_URL" \
  '.mcpServers.osac.url = $url' \
  tools/mcp-oauth-demo-client/inspector-osac.config.json > "$INSPECTOR_CONFIG"

NODE_EXTRA_CA_CERTS=/tmp/osac-ca/ca-bundle.pem \
  npx --yes @modelcontextprotocol/inspector@2.6.0 \
    --config "$INSPECTOR_CONFIG" \
    --server osac
```

When the inspector opens, connect to `osac` and complete browser login with a
user in the demo tenant. Leave the **OAuth Client Metadata Document** field
blank: the configuration supplies the pre-registered public
`osac-mcp-client`. It is not necessary to use dynamic client registration.

If the ingress certificate is publicly trusted, omit `NODE_EXTRA_CA_CERTS`.
When using an internal CA, keep it; a successful `curl` does not automatically
make Node trust that CA.

## 5. Connect a model host

Use the same `MCP_URL` as a remote HTTP MCP server in a model host that supports
MCP OAuth discovery and loopback browser callbacks. The server advertises its
authorization server through protected-resource metadata. The model host must
trust the OpenShift ingress certificate and its callback URI must be allowed by
the Keycloak client; otherwise validate first with the reference client above.

Use the narrow tools as building blocks: inspect a catalog item before creating
a VM, report asynchronous state honestly, and delete test resources. VM Ready
does not mean an application has been deployed.
