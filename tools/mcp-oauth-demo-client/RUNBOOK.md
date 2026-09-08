# OSAC Deployment MCP PoC runbook

This runbook demonstrates browser OAuth and a tenant-facing ComputeInstance
lifecycle through the OSAC Deployment MCP server. The supported demo target is
the local Kind `dev-full` environment; it does not create a CaaS cluster.

The resulting VM is infrastructure only. This phase does not deploy an
application, inject cloud-init content, or create the VM's platform and network
dependencies dynamically.

## 1. Install the local MCP demo

```bash
make -C osac-installer install-mcp-demo \
  PLATFORM=kind PROFILE=dev-full NS=osac
```

AWX syncs its playbooks from upstream `main` by default, separately from the
locally built fulfillment image. Until this branch's ComputeInstance storage
fix is merged, push the branch and set `DEVSTACK_AWX_PROJECT_URL` and
`DEVSTACK_AWX_PROJECT_BRANCH` to that fork and branch when running the command
above. Otherwise the MCP endpoint may be ready while VM provisioning fails.
See the [installer guidance](../../osac-installer/README.md) for the override
example. Uncommitted AAP changes are not picked up by AWX.

The target creates or reuses the Kind cluster, builds the current checkout's
fulfillment-service image, loads it into Kind, and deploys it with
`imagePullPolicy: Never`. Rerun it after MCP code changes; the target reloads
the image and restarts the MCP deployment. No registry push or AAP license is
required.

The `dev-full` stack provides KubeVirt/CDI, AWX, a `linux-vm` catalog item,
the `tenant1` organization, ready default networking, and a ready alternate
network: `mcp-demo-isolated` with `mcp-demo-app-subnet` and
`mcp-demo-app-sg`. Its `linux-vm` catalog item explicitly permits instance-type,
boot-disk, and network-attachment overrides for this demo. On macOS, Podman
uses the normal user-level machine connection. A macOS Podman machine normally
lacks nested KVM, so OSAC request and reconciliation flows work but a guest VM
may remain unschedulable; use a Linux host with KVM exposure to demonstrate a
running guest.

## 2. Discover and verify the endpoints

```bash
MCP_URL=https://mcp.osac.localhost:8443
KEYCLOAK_ISSUER=https://keycloak.osac.localhost:8443/realms/osac

kubectl rollout status deployment/fulfillment-mcp-server -n osac --timeout=5m
printf 'MCP endpoint: %s\nIssuer: %s\n' "$MCP_URL" "$KEYCLOAK_ISSUER"
```

Write the local CA bundle to a file for the reference client or Inspector:

```bash
mkdir -p /tmp/osac-ca
kubectl get configmap ca-bundle -n osac -o jsonpath='{.data.bundle\.pem}' \
  > /tmp/osac-ca/ca-bundle.pem
```

## 3. Run the OAuth reference client

Use `tenant1_user` or `tenant1_admin` from the local Keycloak fixtures. The
password is the `default-user-password` in the `keycloak-admin-credentials`
Secret. The installer registers the public `osac-mcp-client` with the loopback
callback used below; it has no client secret. Do not place the password in this
runbook or shell history.

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

- `list_resources`: list one deployment-focused resource type:
  `compute_instance_catalog_item`, `compute_instance_template`,
  `instance_type`, `disk_image`, `storage_tier`, `virtual_network`, `subnet`,
  `security_group`, or `compute_instance`. Page size defaults to 50 and cannot
  exceed 100.
- `get_resource`: fetch one of those resources by ID. Inspecting the template
  reveals the instance type, disk image, and boot-disk storage tier selected by
  the catalog offering.
- `create_compute_instance`: create a VM from a published
  ComputeInstance catalog item. Optional `instance_type_id` is an ID string;
  `boot_disk` uses `{"size_gib":20,"storage_tier_id":"<storage-tier-id>"}`
  (either field may be supplied; copy the tier ID from `list_resources`); and
  `network_attachments` is an array of objects with
  `subnet_id` and optional `security_group_ids`. Select reference IDs with the
  read tools. The catalog item must permit each override; omit these fields to
  use catalog and tenant-network defaults. `get_resource` returns the
  Fulfillment API's camelCase representation, not a create-tool argument
  template—follow the create tool's snake_case input schema.
- `delete_compute_instance`: delete one VM by ID.

`list_resources` and `get_resource` are deliberately deployment-focused rather
than generic API forwarders. The service validates the caller token and forwards
it to fulfillment, so normal tenant authorization and attribution still apply.

For a deterministic network-selection rehearsal, ask the model to list the
tenant's subnets and security groups, select `mcp-demo-app-subnet` and
`mcp-demo-app-sg`, then create a VM with one `network_attachments` entry using
their returned IDs. The model must not invent the IDs or create networking
resources in this PoC.

## 4. Explore with MCP Inspector

The preferred launcher extracts a temporary local CA bundle and supplies the
checked-in public-client configuration with the local MCP endpoint:

```bash
make -C osac-installer mcp-demo-inspector \
  PLATFORM=kind PROFILE=dev-full NS=osac
```

For a manual launch, the checked-in Inspector configuration supplies the
static public client ID but uses a placeholder URL. Create a temporary
configuration with the discovered endpoint:

```bash
INSPECTOR_DIR="$(mktemp -d "${TMPDIR:-/tmp}/osac-mcp-inspector.XXXXXX")"
INSPECTOR_CONFIG="$INSPECTOR_DIR/inspector-osac.config.json"
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
The client grants the `organization` scope for OSAC tenant attribution and the
`offline_access` scope so Inspector can refresh its browser-login session.

Keep `NODE_EXTRA_CA_CERTS`: a successful `curl` with `--cacert` does not
automatically make Node trust the local Kind CA.

## 5. Connect a model host

Use the same `MCP_URL` as a remote HTTP MCP server in a model host that supports
MCP OAuth discovery and loopback browser callbacks. The server advertises its
authorization server through protected-resource metadata. The model host must
trust the local Kind CA and its callback URI must be allowed by the Keycloak
client; otherwise validate first with the reference client above.

Use the narrow tools as building blocks: inspect a catalog item before creating
a VM, report asynchronous state honestly, and delete test resources. VM Ready
does not mean an application has been deployed.

The server advertises this MCP-first workflow to compatible model hosts. For
operations covered by these four tools, the host should use MCP rather than the
local `osac` CLI, direct API calls, or Kubernetes commands. The repository's
`AGENTS.md` supplies the corresponding Codex routing policy when Codex starts
from this checkout.

### Codex CLI on a local Kind deployment

For the local Kind demo, the MCP endpoint is
`https://mcp.osac.localhost:8443`. The pre-registered public
`osac-mcp-client` uses the loopback callback
`http://127.0.0.1:6274/oauth/callback`. After installing the demo, the
recommended opt-in setup is:

```bash
make -C osac-installer setup-mcp-demo-codex \
  PLATFORM=kind PROFILE=dev-full NS=osac
```

It verifies and saves the Kind CA, merges the OSAC MCP configuration without
replacing other Codex settings, keeps an existing tool-approval preference,
and runs `codex mcp login osac` with CA trust for the browser login. Existing
configurations are backed up with private permissions before changes. If the
`osac` entry points at a different server, setup stops rather than overwriting
it. On macOS, restart Codex Desktop after setup so it inherits the CA setting.
For a new CLI session, use the `export CODEX_CA_CERTIFICATE=...` command
printed by the target; Make cannot set an environment variable in its parent
shell. The target requires Python 3.11 or newer.

To configure manually instead, add the following to
`~/.codex/config.toml` once, merging it with rather than replacing other Codex
settings:

```toml
[mcp_servers.osac]
url = "https://mcp.osac.localhost:8443"
startup_timeout_sec = 20
tool_timeout_sec = 120
default_tools_approval_mode = "writes"

[mcp_servers.osac.oauth]
client_id = "osac-mcp-client"
callback_url = "http://127.0.0.1:6274/oauth/callback"
callback_port = 6274
```

The fixed callback URL and listener port are both needed for this
pre-registered Keycloak client. `codex mcp add --oauth-client-id` can save the
server URL and client ID, but its default generated callback is not this demo's
registered `/oauth/callback` URI. Codex documents the [callback selection and
port rules](https://developers.openai.com/codex/mcp).

Codex must trust the local Kind CA. Extract it into a persistent local path and
verify the protected-resource metadata before authenticating:

```bash
ca_dir="$HOME/.config/osac/certs"
mkdir -p "$ca_dir"
kubectl -n osac get configmap ca-bundle \
  -o jsonpath='{.data.bundle\.pem}' \
  > "$ca_dir/kind-ca.pem"

curl --cacert "$ca_dir/kind-ca.pem" \
  https://mcp.osac.localhost:8443/.well-known/oauth-protected-resource
```

Set `CODEX_CA_CERTIFICATE` for this local demo and complete the browser login
with an OSAC user in the demo tenant:

```bash
export CODEX_CA_CERTIFICATE="$ca_dir/kind-ca.pem"
codex mcp login osac
```

The client grants the `organization` scope by default, so it does not need to
be requested explicitly. Add `--scopes offline_access` only when a refresh
token is useful for a long-lived local demo session. For Codex Desktop on
macOS, make the CA path available to GUI applications, then fully quit and
reopen Codex:

```bash
launchctl setenv CODEX_CA_CERTIFICATE "$HOME/.config/osac/certs/kind-ca.pem"
```

Start a new Codex session from this repository so it loads the MCP routing
instructions in `AGENTS.md`, then use `/mcp` to confirm that `osac` is
connected:

```bash
cd /path/to/osac
codex
```

An ordinary deployment request should then select the connected tools without
mentioning MCP. For example:

> Show me the VM offerings and sizes available to me, recommend the smallest
> option, and wait for my confirmation before creating anything.

If Codex must start outside this checkout, add equivalent OSAC MCP routing
guidance to `~/.codex/AGENTS.md`; connection settings in `config.toml` do not
control tool-selection policy. Do not disable TLS verification or store a user
password, bearer token, or Keycloak admin secret in `config.toml`.
