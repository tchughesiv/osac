---
marp: true
theme: redhat
paginate: true
title: MCP PoC — Compute Instance Provisioning
description: An authenticated, catalog-governed MCP interface for provisioning OSAC ComputeInstances from model hosts.
---

<!-- markdownlint-disable MD001 MD013 MD025 MD033 -->

<style>
section.conclusion { font-size: 25px; }
section.conclusion li { margin-bottom: 8px; }
section.tools table { font-size: 0.59em; }
section.tools table td { padding-top: 7px; padding-bottom: 7px; }
section.demo blockquote { font-size: 0.92em; border-left: 8px solid #ee0000; padding-left: 20px; margin: 16px 0; }
section footer a { text-shadow: none; box-shadow: none; }
</style>

<!-- _class: title -->
<!-- _paginate: false -->

# OSAC Deployment MCP PoC

### Natural-language ComputeInstance provisioning

OSAC-4388

<!--
Speaker notes — 0:00–0:15
Spoken: The question was not whether a model can call an API. It was whether a model can provision through OSAC without bypassing the catalog, tenant identity, or existing reconciliation path.
-->

---

## What we set out to prove

Can a model request a VM through MCP while OSAC enforces policy?

The PoC tested whether it could:

- discover a published offering, its defaults, and editable fields;
- act with the caller's tenant permissions;
- submit a request and report asynchronous state accurately; and
- invoke named, schema-defined deployment actions.

> **Why MCP for model hosts?** A remote, OAuth-protected set of discoverable,
> typed actions gives models structured results with less CLI-specific guidance.
> The host needs no local CLI or shell access; Fulfillment still enforces policy.

<!-- _footer: "Scope: [OSAC-4388](https://redhat.atlassian.net/browse/OSAC-4388)" -->

<!--
Speaker notes — 0:15–0:55
Spoken: We started with cluster provisioning, then chose a focused VMaaS journey that still exercises catalog, identity, networking, storage, and reconciliation. MCP gives a model host named, typed actions over OAuth without requiring a local OSAC CLI or shell. Fulfillment still applies the caller's tenant permissions and catalog policy; MCP adds no new provisioning authority.

Backup — if asked:

The OSAC CLI can reach the same backend and remains useful for people and scripts. MCP's advantage for a remote or restricted model host is discoverable schemas and structured results with less CLI-specific guidance. This is the provisioning lane, distinct from the proposed Observability MCP.

Catalog field policies define locked and editable values plus defaults. Users supply permitted configuration; they do not override catalog policy. Fulfillment checks the caller's allowed operations and tenant, verifies the catalog item is visible and published, applies defaults and locked values, and validates selected references. The model's request for human confirmation is part of the demo workflow, not a server-enforced approval gate.
-->

---

## Existing OSAC boundaries stay in charge

![h:500](assets/osac-mcp-architecture.svg)

<!--
Speaker notes — 0:55–1:45
Spoken: This is a Go protocol adapter, not another control plane. It runs in a separate pod from the same Fulfillment image and translates MCP tool calls into generated public Fulfillment API calls, forwarding the caller's token. Fulfillment remains authoritative for identity, tenant access, catalog policy, and lifecycle state. The existing reconciliation and provider path is unchanged. This MCP path does not grant an admin identity or direct Kubernetes or AAP access.

Backup — if asked:

The MCP process is the `fulfillment-service start mcp-server` subcommand, with its own Service and serving certificate. Using the public Fulfillment API over a cluster-local gRPC connection makes MCP behave like another tenant client rather than a privileged internal integration. The separate Deployment allows an independent rollout while keeping its image version matched to Fulfillment.

The MCP validates the incoming bearer token and forwards it on tool calls. Fulfillment remains the system of record and enforcement point. After the API, the existing database, reconciliation, CR, operator, AAP, KubeVirt, and feedback path takes over. The model can poll current state with `get_resource`. The MCP pod uses cluster DNS and cluster-managed CA and serving-certificate material; an external model host still needs to trust the MCP serving CA.
-->

---

<!-- _class: tools -->

## Four tools, one governed workflow

| Tool | Purpose | Key behavior |
| --- | --- | --- |
| `list_resources` | Discover catalog, sizing, storage, networks, and VMs | Read-only; allowlisted; paginated |
| `get_resource` | Inspect references, policy, and current state | Read-only; resource type + ID |
| `create_compute_instance` | Submit a catalog-mediated VM request | Catalog item required; typed inputs; asynchronous |
| `delete_compute_instance` | Explicitly clean up one VM | ID-scoped; destructive; asynchronous |

**Why this shape?** Compact generic discovery keeps model context small;
purpose-specific mutations preserve precise schemas and accurate risk hints.

<!--
Speaker notes — 1:45–2:25
Spoken: Two generic read tools cover nine allowlisted deployment resource types, including catalog items, instance types, storage tiers, subnets, and security groups. Create and delete remain named, typed actions. The model can select existing networking, but these tools cannot create a network or invoke arbitrary Fulfillment methods.

Backup — if asked: Deletion is asynchronous. Repeating it after the resource is gone can return `NotFound`, even though it cannot delete that resource twice.
-->

---

<!-- _class: demo -->

## Recorded demo

> <a href="https://drive.google.com/file/d/1boI9cFBGApCeNfvQYT4Tojr163KseNhC/view?usp=sharing" target="_blank" rel="noopener noreferrer">Watch the OSAC Deployment MCP PoC →</a>

<!--
Speaker notes — 2:25–5:55
Spoken: Open the 3:20 recording, which opens in a new tab, then return to the deck. The recording shows explicit deletion as well as the VM request.

Backup — if asked: The link is accessible to company employees. The slide avoids a fixed prompt checklist so narration follows the recording. If creation is accepted but the VM is not Ready, distinguish those states.
-->

---

<!-- _class: conclusion -->

## Recommendation: graduate the PoC into a Feature

**Demonstrated:** a model can discover catalog choices, submit a
ComputeInstance request, observe its status, and delete the resource through
the caller's identity and Fulfillment policy.

**Productize next:**

1. **Broaden workflows:** add more catalog-backed offerings and design
   a separate network-creation workflow.
2. **Govern mutations:** audit and correlate MCP actions; define confirmation
   and dry-run behavior.
3. **Harden delivery:** make setup and login repeatable across supported
   model hosts; make failures actionable and test hosting and HA.

**Boundary:** this demo selects pre-seeded network and storage prerequisites;
it does not create networks.

<!--
Speaker notes — 5:55–7:10
Spoken: These are three distinct tracks. Broaden what a model can request: additional catalog-backed offerings, plus a separately designed network-creation workflow. Govern writes with real confirmation and audit behavior, not just tool hints or a conversational request for approval. Then make setup, login, failure reporting, and hosting reliable beyond this prepared demo environment. The current VM can already be tracked by ID; a new status mechanism is not the immediate next feature.

Backup — if asked:

Capability scope: ComputeInstance alone does not prove the tool shape generalizes. OSAC also has catalog items for clusters and bare metal, but their provisioning paths differ. Prioritize each by user demand and platform readiness. For each journey, verify that the public Fulfillment API supports discovery, selectable inputs, create, status, and cleanup; then expose a small set of typed MCP actions. Keep generic discovery where useful, but do not mirror every Fulfillment RPC as a tool.

Network creation needs its own workflow, not a hidden side effect of VM creation. A VM selects existing, ready networking; creating a network makes separate tenant-visible choices about address space and optional firewall rules, and spans several asynchronous writes. The public Fulfillment API already supports those resources and validates CIDR format, network-class compatibility, subnet containment and overlap, and attachment readiness. MCP should use those rules rather than duplicate them, and show the user each proposed create and its outcome. Today's MCP can inspect existing networking but cannot discover NetworkClasses or create network resources.

The create order is: discover a platform-provided NetworkClass; create a VirtualNetwork with a CIDR and wait for READY; create a Subnet inside that CIDR and, if needed, a SecurityGroup on the same VirtualNetwork. Subnet and SecurityGroup are siblings, so neither has to be created before the other, but the selected Subnet and any selected SecurityGroup must be READY before a VM can use them. Then create the VM with those references. A future MCP flow must handle partial failure and distinguish newly created resources from shared ones. For cleanup, delete this VM first, then any owned SecurityGroup, Subnet, and VirtualNetwork in that order, checking for other references before each deletion; backend guards prevent deleting a Subnet while a SecurityGroup remains on its network or deleting a VirtualNetwork while children remain. Never remove shared networking merely because one VM was deleted.

Mutation governance: Tool descriptions and risk annotations are hints to model hosts, not approval or authorization boundaries. The server does not enforce a separate human approval step. Define which mutations require confirmation and where it is enforced. A preview or dry-run should reuse Fulfillment validation to show resolved defaults, rejected inputs, and likely effects without persisting a resource. Test tenant isolation, and correlate MCP and Fulfillment audit records by caller, tenant, tool, catalog item, resource, and outcome without logging tokens or secrets.

Delivery readiness: Create returns a resource ID and initial state, and the host polls `get_resource`; that is sufficient for this VM demo. Make terminal failure reasons and retry guidance clear without assuming every workflow needs a separate operation ID. For a fresh supported model host, verify CA trust, OAuth metadata discovery, the configured client and callback, login through Keycloak, an authorized tool call, and token refresh. Test bad callbacks, invalid scopes, and expired tokens. For the server, test certificate rotation, health checks, restarts, multiple replicas, and MCP SDK compatibility.
-->
