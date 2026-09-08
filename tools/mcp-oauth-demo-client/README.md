# mcp-oauth-demo-client

Reference OAuth client for the OSAC Deployment MCP proof of concept
([OSAC-4388](https://issues.redhat.com/browse/OSAC-4388)). It proves the RFC
9728 discovery plus OAuth 2.0 Authorization Code with PKCE flow against
`fulfillment-service start mcp-server` without configuring a full AI IDE.

After browser login, it lists and inspects ComputeInstance catalog items,
creates a ComputeInstance, polls it, and can delete it through MCP. It uses
only the MCP wire protocol and does not import fulfillment-service internals.

The server exposes four tools:

- `list_resources` and `get_resource` for the deployment-focused allowlist:
  ComputeInstance catalog items, templates, instance types, disk images,
  storage tiers, virtual networks, subnets, security groups, and
  ComputeInstances.
- `create_compute_instance` and `delete_compute_instance` for the VM
  lifecycle. Creation accepts catalog-authorized instance-type, boot-disk, and
  network-attachment overrides using IDs returned by the read tools.

This standalone Go module is intentionally outside the root `go.work`. Run it
from the repository root with `GOWORK=off`:

```bash
GOWORK=off go run ./tools/mcp-oauth-demo-client -server-url "$MCP_URL"
```

Pass `-issuer "$KEYCLOAK_ISSUER"` to pin the authorization server and
`-ca-file /path/to/ca-bundle.pem` when the local Kind CA is not trusted by the
workstation. `-delete` removes the VM after polling; otherwise
the client leaves it for inspection.

See [RUNBOOK.md](RUNBOOK.md) for installation, local endpoint setup, Inspector
setup, and cleanup.
