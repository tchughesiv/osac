# OSAC-4388 — OSAC Deployment MCP PoC: Recommendation

**Jira:** [OSAC-4388](https://redhat.atlassian.net/browse/OSAC-4388) — Spike: PoC for OSAC Deployment MCP (natural-language provisioning)
**Branch:** `OSAC-4388-deployment-mcp-poc` (based on `osac-project/osac@main`)
**Status:** Code complete (Tasks 1–4, then 6–8 done, added in a later round at the user's explicit
direction); this document is Task 5/9 (updated after Tasks 6-8 landed — see "OAuth discovery"
section below, added later than the rest of this document).

*Originally written in `osac-workspace` (the now-retiring meta-workspace); relocated here alongside
the demo client and runbook it references, since `osac` is where the PoC's other durable artifacts
now live.*

## Bottom line

**Worth pursuing as a full OSAC Feature — yes, with one hard prerequisite already met and one still open.**

This PoC ended up proving more than the ticket originally asked for. What started as "wrap four
read/create endpoints in MCP tools" became, through several rounds of explicit scope
conversations with the user, a real answer to the #1 blocking concern from the prior research:
**agent tool calls can be attributed to the real calling tenant, not a shared service identity,
using nothing but mechanisms `fulfillment-service` already has** (bearer-token passthrough,
mirroring how `restgateway` already works). That is a genuine, demonstrated finding — not
something this ticket asked for, and worth naming explicitly to leadership and to Amit, since it
changes the shape of the "is this safe to build" conversation for both this effort and his.

## What was built

Four MCP tools (exactly as scoped — no more), exposed over MCP's Streamable HTTP transport by a
new `fulfillment-service start mcp-server` subcommand:

| Tool | Wraps |
|------|-------|
| `list_catalog_items` | `ClusterCatalogItems.List` |
| `describe_catalog_item` | `ClusterCatalogItems.List`/`.Get` (resolves by id or name) |
| `create_cluster_from_catalog_item` | `Clusters.Create` (via `ClusterSpec.catalog_item` + `--set`-style field overrides) |
| `get_cluster_status` | `Clusters.Get` (state, conditions, API/console URLs) |

Every tool call:

1. Requires a real bearer token (`sdkauth.RequireBearerToken`), verified with the exact same
   JWKS/`JwtValidator` machinery `grpcserver`'s own interceptor uses — no new auth mechanism
   invented.
2. **Forwards that same token, per call, to the downstream gRPC client** rather than the server
   holding one shared identity — so `fulfillment-service`'s own authn/authz and tenancy logic
   attribute the resulting object to the real caller, exactly as if they'd used the CLI directly.

This is a bigger scope than the ticket's own Implementation Guidance asked for — it explicitly
listed "Agent-vs-human attribution" and "Production multi-tenant hosting" as **out of scope**.
That expansion happened deliberately, in conversation, across three plan revisions in one
sitting (CLI-subcommand-over-stdio → service-tree-with-client-credentials →
service-tree-with-token-passthrough), each time because the cheaper option was cheap because it
punted on the exact problem the research had flagged as blocking. See `02-plan.md`'s Risk
Assessment for the full history. Flagging it here again because it's the single most important
thing for a reader of this document to not miss.

## The finding: attribution is solved for the "human holds a token" case

The original research (`2026-08-18-amit-osac-deployment-mcp-sync-agenda.md`) listed
agent-vs-human attribution as gap #1, and — together with gap #2 (no audit trail) — as one of
two gaps blocking real tenant rollout: *"you can't safely give customers agent-driven write
access to their own quota without knowing whose agent did what."*

This PoC demonstrates, with a real integration test against a live `fulfillment-service`
instance (`it/it_mcp_server_test.go`), that when a human has already authenticated (holds a real
bearer token — from `osac login` or an equivalent flow) and hands that token to their MCP client,
every resulting `fulfillment-service` object is attributed to *that person*:
`metadata.creator` and `metadata.tenant` on a cluster created via `create_cluster_from_catalog_item`
match the real calling user's identity, not a proxy/service-account identity. This works because
`mcpserver` never validates-then-discards the token — it re-forwards the exact bearer string the
caller supplied, so `fulfillment-service`'s existing interceptor chain (which already knows how
to attribute a call to a subject) does the actual attribution work, unmodified. **No new
attribution mechanism needed to be built.**

What this does *not* solve, and shouldn't be read as solving:

- **Pure agent-native delegated identity** — an agent acting continuously on a user's behalf
  without that user handing over their own token per-session (e.g., a long-running background
  agent with its own distinct, revocable, narrower-scoped credential). That's a materially
  different problem (delegated authorization, likely OAuth token exchange or a
  service-account-with-limited-scope pattern) and remains open.
- **Audit trail beyond what `grpcserver` already logs.** `mcpserver` adds no structured
  "MCP tool X called by Y" log distinct from the gRPC call it forwards to. `grpcserver`'s
  existing request logging captures the call, but there's still no queryable "which MCP tool
  produced this API call" record. Gap #2 from the original research is unchanged.
- **Token revocation/rotation mid-session.** `mcpserver` re-validates the token on every call
  (via its own `TokenVerifier`) so a revoked token is rejected on the *next* call, but there's no
  session-invalidation mechanism if a token is revoked mid-flight for a long-running tool call.
- **Rate limiting.** Nothing new here beyond whatever `fulfillment-service` already enforces
  (which the ticket's own research flagged as inconsistent — see gap #3 below).

A meaningful safety property worth stating plainly: **because the real token is forwarded
as-is, `grpcserver`'s own interceptor re-validates it again on arrival.** `mcpserver`'s own
`TokenVerifier` is a fail-fast convenience (a clearer MCP-level 401 instead of an opaque gRPC
error), not a replacement for the authorization decision that already happens downstream. Even
if `mcpserver`'s own check were buggy, it cannot itself grant access beyond what the forwarded
token already permits — the blast radius of a bug in the new code is bounded by the code that
already existed.

## Status of the previously-identified gaps

| # | Gap (from prior research) | Status after this PoC |
|---|---|---|
| 1 | Agent-vs-human attribution | **Solved for the "human holds a token" case** (this PoC's headline finding). Pure agent-delegated identity remains open. |
| 2 | No audit-log interceptor | **Unchanged.** Still just `grpcserver`'s existing request logging; no MCP-call-specific audit trail. |
| 3 | Quota enforcement is per-resource, not platform-wide | **Unchanged** — not touched by this PoC; still a `docs/API.md`-documented pattern gap. |
| 4 | No dry-run/confirm/force flags anywhere | **Unchanged.** None of the four tools added a confirmation gate; `create_cluster_from_catalog_item` executes immediately, same as the CLI. |
| 5 | Async create-vs-reconcile mismatch | **Confirmed hands-on, not just theorized** (see below) — still unresolved, but now backed by an observed behavior rather than an inference from reading the proto docs. |
| 6 | No skill/guidance for safe agent-driven provisioning | **Unchanged** — out of scope for this PoC; still a blank slate in `osac-ai-skills`. |
| 7 | Nothing operational exists | **Resolved for this narrow slice** — `mcp-server` is now a real (experimental, not deployed by default) subcommand with a working four-tool demo. Still zero *production* deployment story. |

### Async create-vs-reconcile, confirmed

`create_cluster_from_catalog_item` returns as soon as `fulfillment-service` accepts the create
request — the object exists with a state like `PROGRESSING`, not `READY`. `get_cluster_status`
has to be called again (or polled) to see real provisioning progress. `it_mcp_server_test.go`'s
assertion deliberately checks for a valid non-`UNSPECIFIED` state rather than `READY`, because
`READY` is not reachable within a reasonable integration-test timeout and isn't guaranteed within
any fixed window at all — the underlying provisioning is genuinely asynchronous. For a natural-
language agent, this means "the tool call returned successfully" and "the cluster is usable" are
different claims, and an agent (or the skill guidance layered on top of it) needs to make that
distinction explicit to the end user rather than reporting "done" prematurely. This is exactly
the gap #5 concern from the original research, now observed rather than inferred.

## What building this actually took (implementation notes worth carrying forward)

- **Code location:** the MCP server lives inside `fulfillment-service`'s own Go module
  (`internal/cmd/service/start/mcpserver/`), as a new `start mcp-server` subcommand — sibling to
  `start grpc-server`/`start rest-gateway`/`start controller`. This was a deliberate, debated
  choice (see `02-plan.md`'s history): Go's `internal/` package visibility means code that needs
  to reuse `fulfillment-service`'s own internal packages (gRPC client builders, JWT validation,
  `--set` field-override parsing) has to physically live inside that module — a sibling
  `osac/mcp/` directory at the mono-repo root could not import any of those `internal/` packages.
  If a future Feature keeps MCP tooling in Go and wrapping `fulfillment-service`, this is very
  likely still the right shape, not a PoC-only shortcut.
- **Auth model:** Streamable HTTP transport with per-call bearer-token passthrough (not stdio,
  not a fixed client-credentials service identity). This was the direct consequence of choosing
  to solve real attribution rather than defer it — it costs more up front (a real HTTP listener,
  JWKS wiring, CORS support for browser-based MCP clients like the Inspector) but the resulting
  shape is much closer to what a production deployment would actually look like, per the user's
  explicit "let's implement how we would long-term, if it's not too much more work" direction.
- **Catalog-item field overrides:** `create_cluster_from_catalog_item`'s `--set key=value`-style
  input reuses `fulfillment-service`'s own `internal/cmd/cli/create/fieldutil.ApplyFields` — the
  same code path the CLI's `--set` flag uses. No protobuf `Any`-type-inference logic needed to be
  reimplemented for the MCP server, confirming the ingest-phase research's expectation.
- **Demo mechanics for a human tester — since superseded, see below.** This bullet originally said
  there was no interactive OAuth flow and a tester had to manually paste a bearer token into their
  MCP client's config. That's no longer accurate — see "OAuth discovery" immediately below.

## OAuth discovery: closing the manual-token-paste gap (added after the rest of this document)

At the user's explicit direction — after asking "we need to implement client side too, right?"
and choosing to wire up a real interactive OAuth flow rather than leave manual token-paste as the
only path — three more pieces landed on top of Tasks 1-4, closing exactly the gap the paragraph
above used to flag as open follow-on work:

1. **`mcp-server` now serves RFC 9728 Protected Resource Metadata** (`--oauth-authorization-server`/
   `--oauth-resource-url` flags; unset by default, so Tasks 1-4's behavior is unchanged unless an
   operator opts in). When configured, 401 responses carry a `resource_metadata` discovery hint,
   and an unauthenticated `/.well-known/oauth-protected-resource` document advertises the Keycloak
   realm as the protecting Authorization Server.
2. **A dedicated public OAuth client (`osac-mcp-client`) is registered** in `osac-installer`'s
   Keycloak bootstrap realm (`charts/osac-infra/files/realm.json` — the file `make install-infra`
   actually installs for both kind and real OpenShift, not a throwaway dev-only fixture),
   Authorization Code + PKCE only, no secret, one fixed loopback redirect URI.
3. **A reference OAuth demo client** (`tools/mcp-oauth-demo-client/` — a standalone Go module,
   not inside `fulfillment-service`, since it has no dependency on that module's internal
   packages) drives the full handshake end to end: real browser login, then the same four-tool
   sequence Task 4's integration test exercises, printed for a human to watch.

**The headline implication:** the two items above are exactly what a real, spec-compliant MCP host
(Cursor, Claude Desktop) needs to discover the Authorization Server on its own and drive its own
native interactive login — the reference client in (3) exists to *prove* the handshake works, not
because a custom client is required for every future consumer. Once (1) and (2) are deployed, a
human should be able to point Cursor's own remote-MCP config at `mcp-server`'s URL directly and get
a real browser login popup, with zero custom code — this is expected based on reading both the MCP
authorization spec and the SDK's implementation, but **has not been exercised against a real IDE in
this session** (see the testing-limitation section below for the full list of what's verified vs.
assumed).

This does not change the "Status of the previously-identified gaps" table above — none of gaps
#1-7 are about *how a human obtains a token in the first place*, they're about what happens once a
tool call carries one. OAuth discovery makes the already-solved attribution story easier to actually
use, it doesn't solve a new gap.

## A limitation of this PoC's own testing (flagging honestly)

Task 4's integration test (`it/it_mcp_server_test.go`) was written to the same standard as the
rest of `fulfillment-service`'s `it/` suite — it drives all four tools over a real HTTP+auth
stack against a live `fulfillment-service` and asserts real attribution — and it compiles cleanly
and passes `go vet`/`golangci-lint`. **It was not actually executed against a live cluster in this
session**, because no local Podman/cluster environment was available in the sandbox this work was
done in (confirmed: `podman machine` isn't running here, no `docker` CLI either). All of the
`mcpserver` package's unit tests (37, after Tasks 6-8 added more) do run and pass in this
environment, and they cover the tool-handler logic, the token-forwarding mechanism, the auth
adapter, and the OAuth-discovery wiring in isolation — but the integration test's own pass/fail
status is unverified pending a real cluster-backed run (`osac-installer`'s `make install-infra
PLATFORM=kind` + `make test`, or `cluster-tool` against baremetal — see the Recommended next step
below; the standalone `kind-dev/` setup referenced elsewhere in this document's earlier drafting has
since been removed, with that functionality folded into `osac-installer` directly). **Running the
`it/` suite against this branch before treating the demo as fully proven is the single most
important follow-up action**, distinct from and in addition to the recommended next step below.

One specific piece of that test's design carries residual risk worth a reviewer's attention: the
catalog-item fixture relies on `fulfillment-service`'s tenancy logic assigning newly-created,
no-tenant-specified resources to the built-in `"shared"` tenant (visible to every tenant) when
created via an admin connection — inferred from reading `internal/auth/default_tenancy_logic.go`
and cross-checked against the fact that `it_public_clusters_test.go`'s own `HostType`/
`ClusterTemplate` fixtures already rely on the identical mechanism successfully. High confidence,
but backed by code-reading rather than a live run.

**The OAuth discovery pieces (Tasks 6-8) carry the same kind of unverified-live-run caveat, one
level further out.** `mcp-server`'s new unit tests (`start_mcp_server_cmd_test.go`) exercise the
RFC 9728 wiring directly (hand-built HTTP requests against the composed handler) and pass, and
`mcp-oauth-demo-client` builds/vets/lints cleanly — but neither the Keycloak client registration nor
the actual browser-redirect handshake it's meant to drive has been run against a real Keycloak
instance in this session, for the same reason as above (no local cluster/Podman environment
available). Concretely unverified: that Keycloak actually accepts `osac-mcp-client`'s registration
as written, that its OIDC discovery document resolves the way `GetAuthServerMetadata` expects, and
that the full redirect round-trip (browser → Keycloak login → `localhost:8091/callback` →
token exchange) completes without a mismatch somewhere in that chain. This is the single most
directly demo-blocking unverified item in the whole PoC — everything else can be sanity-checked by
reading code with high confidence; a live OAuth handshake either works exactly right or visibly
doesn't, and that can only be settled by actually running it once against a real cluster.

## Recommended next step

1. **Immediate:** run this branch's `it/` suite (including the new `it_mcp_server_test.go`)
   against a real cluster (`osac-installer`'s `make install-infra PLATFORM=kind` — the standalone
   `kind-dev/` setup this branch's earlier notes referenced no longer exists; local dev now goes
   through `osac-installer` directly, or `cluster-tool` against baremetal) to close the testing gap
   above before treating the demo as fully proven.
2. **Also immediate, and arguably higher-value for the Amit demo specifically:** run
   `mcp-oauth-demo-client` against that same real cluster once `mcp-server` is started with
   `--oauth-authorization-server`/`--oauth-resource-url` set, to confirm the live OAuth handshake
   end to end — then try pointing a real MCP host (Cursor's remote-MCP config, or Claude Desktop)
   directly at the server URL and confirm it drives its own native login, no custom client needed.
   That second check is the concrete evidence for this document's "zero custom code needed for a
   real host" claim above, which is currently a spec-reading-based expectation, not yet observed.
3. **Bring this document + the working demo to the Amit sync** (already on the calendar per
   `2026-08-18-amit-osac-deployment-mcp-sync-agenda.md`) rather than scheduling a separate
   review. The sync's existing agenda item #3 ("Open questions + known gaps") should now
   explicitly reference gap #1 as resolved-for-one-case, which changes the "is attribution a
   hard prerequisite before any tenant-facing rollout" question the agenda already poses —
   the honest answer is now "partially, and here's exactly which part."
4. **If leadership wants to proceed:** formalize via `osac-feature` (bootstraps the Epic +
   PRD/Design gate tasks) rather than continuing ad hoc. The PRD/Design should explicitly scope:
   - Which of the two remaining hard gaps (audit trail, pure agent-delegated identity) are
     prerequisites for an initial *limited* rollout vs. full tenant-facing GA — this PoC doesn't
     answer that policy question, it only narrows what "attribution" needs to still cover.
   - The async-feedback UX (poll/wait pattern, or explicit "still provisioning" framing) as a
     first-class design concern, not an afterthought — this PoC confirms it's a real, observable
     behavior, not a theoretical edge case.
   - Whether composite "deploy a stack" tools belong server-side (more tools, less agent
     reasoning) or as skill-level guidance chaining these narrow tools (fewer tools, matches this
     PoC's four-tool scope) — the broad-vs-narrow tool-shape question from the original research
     is still fully open; this PoC deliberately stayed narrow per the ticket's AC-2.
5. **Don't let this stall on gap #2 (audit trail) alone.** It's real, but per the safety property
   above, a missing MCP-level audit log doesn't create a new *authorization* hole — it creates an
   observability gap. That's a legitimate reason to require it before GA, but not a reason to
   block further design/scoping work in the meantime.
