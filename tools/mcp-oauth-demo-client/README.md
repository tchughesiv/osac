# mcp-oauth-demo-client

Reference OAuth client for the OSAC Deployment MCP PoC ([OSAC-4388](https://issues.redhat.com/browse/OSAC-4388)).

Proves that the full RFC 9728 discovery + OAuth 2.0 Authorization Code + PKCE handshake against
`fulfillment-service`'s `start mcp-server` command works end to end — real interactive browser login,
no manually copy-pasted bearer token — without requiring a full AI IDE (Cursor, Claude Desktop) to be
configured just to exercise it. After connecting, it drives the demo tool sequence
(`list_catalog_items` -> `describe_catalog_item` -> `create_cluster_from_catalog_item` ->
`get_cluster_status`) and prints what happened.

It's modeled directly on the MCP Go SDK's own official example
(`examples/auth/client/main.go` in [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)),
specialized to a fixed `/callback` redirect path matching the `osac-mcp-client` Keycloak client
registered in `osac-installer`'s bootstrap realm (`charts/osac-infra/files/realm.json`).

This is a standalone Go module with no dependency on any `fulfillment-service` internal package — it
only speaks the MCP wire protocol, so it isn't forced to live inside that module. It's under `tools/`
(not part of `go.work`, not a shipped component) because it's proof-of-concept scaffolding for the
handshake, not a maintained OSAC deliverable — same category as `tools/bootstrap.sh`, just Go instead
of shell.

See [`RUNBOOK.md`](RUNBOOK.md) for a full end-to-end walkthrough (booting a cluster, seeding a
catalog item, running `mcp-server`, then this client) if you don't already have all of the
prerequisites below in place.

## Prerequisites

- `fulfillment-service`'s `start mcp-server` running with OAuth discovery configured
  (`--oauth-authorization-server`/`--oauth-resource-url` both set to the Keycloak realm issuer and this
  server's own externally-reachable URL, respectively).
- The `osac-mcp-client` Keycloak client registered (already the case for any environment using
  `osac-installer`'s bootstrap realm from this commit onward).
- At least one published `ClusterCatalogItem` to demo against.

## Usage

Not listed in `osac`'s root `go.work` (deliberately — it isn't a shipped component), so plain `go`
commands run from this directory need `GOWORK=off` to avoid an "outside the workspace" error:

```bash
GOWORK=off go run . -server-url http://localhost:8001
```

Run `go run . -h` for the full flag list (callback port, client ID, issuer pinning, which catalog item
to use, field overrides via repeated `-set key=value`, status-poll count/interval).

Against a dev cluster whose Keycloak/`mcp-server` TLS certs are signed by a cluster-local CA (e.g. a
kind cluster via `osac-installer`), pass `-ca-file` pointing at that CA's PEM bundle (see
`osac-installer`'s `ca-bundle` ConfigMap in the OSAC instance namespace) — otherwise both the OAuth
discovery requests and the MCP connection itself fail TLS verification.

The demo does not delete the cluster it creates — clean it up afterward via the `osac` CLI or console
if this was just a demo run.
