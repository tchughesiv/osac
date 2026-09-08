# OSAC Deployment MCP PoC recommendation

## Bottom line

Continue toward a product Feature, but treat this as a constrained VMaaS proof
of concept—not a general OSAC API gateway or an application-deployment product.

The PoC demonstrates a useful interaction shape for a human-led agent session:
the MCP server validates the human's bearer token and forwards it to the
fulfillment API on every call. Existing fulfillment authorization, tenant
isolation, and attribution remain authoritative; MCP does not use a shared
administrator identity.

## Current VMaaS Phase 1 surface

- `list_resources`: list ComputeInstance catalog items or ComputeInstances
  from an explicit allowlist.
- `get_resource`: get one allowlisted resource by ID.
- `create_compute_instance_from_catalog_item`: create a VM from a published,
  policy-controlled offering.
- `delete_compute_instance`: delete a VM by ID.

The read tools are intentionally narrow. A raw generic `GET` proxy would make
every present and future fulfillment object callable by a model without a
product-level review of its authorization semantics, result size, or safety.
A skill can provide higher-level workflow guidance while the MCP server keeps
the executable surface explicit and enforceable.

The server uses generated fulfillment clients over one downstream gRPC
connection. A future production implementation should keep those clients behind
a purpose-built deployment API interface rather than letting each MCP handler
depend on a growing list of generated service clients. That is a code-structure
boundary, not an additional network connection.

## What the implementation establishes

- A browser OAuth client can discover the authorization server and invoke MCP
  tools without manually pasting a bearer token.
- Catalog policy and existing default-network behavior apply to an MCP-created
  ComputeInstance just as they do for other public API callers.
- The calling user, rather than a shared MCP service account, remains the
  fulfillment API's principal.
- An OpenShift VMaaS demo target builds, pushes, deploys, and pulls an explicit
  branch image, then seeds a virtual-machine offering from AAP-published
  template data. Validate its live provisioning flow on the target cluster.

## Remaining product work

- Model- or agent-native delegated identity, token lifecycle, and MCP-specific
  audit records remain unaddressed.
- No confirmation, dry-run, quota aggregation, rate limit, or policy layer has
  been added for model-initiated writes.
- Creation is asynchronous. A successful create response means OSAC accepted
  the request; it does not mean that the virtual machine is ready.
- The demo relies on pre-existing platform and tenant prerequisites: OpenShift
  Virtualization, CDI, storage, AAP publication, hub access, and a ready
  default VirtualNetwork, Subnet, and SecurityGroup. A ComputeInstance does not
  invent them dynamically.
- VM creation is not application deployment. Workload image selection,
  configuration, day-two access, and application health are later-phase design
  work.

## Recommended next step

Use the VMaaS demo to validate one real model host's OAuth behavior and the
human experience of its four tools. If that is successful, create a Feature
with explicit decisions on delegated agent identity, auditability, confirmation
policy, the supported resource catalog, and composite workflow ownership.
