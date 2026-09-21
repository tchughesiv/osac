# OSAC Installer -- single deployment API.
#
# Entities:  Infra (osac-deps + osac-infra), Osac (osac chart), Test
# Parameters are REQUIRED -- missing params produce hard errors.
#
# Usage:
# Every deployment target requires PLATFORM, PROFILE, and NS. No exceptions.
#   make install         PLATFORM=kind|openshift  PROFILE=dev|dev-full|vmaas-ci|vmaas-external|bmaas-ci|caas-ci|full-ci  NS=osac
#   make uninstall       PLATFORM=kind|openshift  PROFILE=dev|dev-full|vmaas-ci|vmaas-external|bmaas-ci|caas-ci|full-ci  NS=osac
#   make install-infra   PLATFORM=kind|openshift  PROFILE=dev|dev-full|vmaas-ci|vmaas-external|bmaas-ci|caas-ci|full-ci  NS=osac
#   make install-osac    PLATFORM=kind|openshift  PROFILE=dev|dev-full|vmaas-ci|vmaas-external|bmaas-ci|caas-ci|full-ci  NS=osac
#   make uninstall-osac  PLATFORM=kind|openshift  PROFILE=dev|dev-full|vmaas-ci|vmaas-external|bmaas-ci|caas-ci|full-ci  NS=osac
#   make uninstall-infra PLATFORM=kind|openshift  PROFILE=dev|dev-full|vmaas-ci|vmaas-external|bmaas-ci|caas-ci|full-ci  NS=osac
#   make install-mcp-demo PLATFORM=openshift PROFILE=vmaas-ci|vmaas-external NS=osac MCP_DEMO_IMAGE=<registry>/<repository>:<tag>
#   make install-mcp-demo-kind PLATFORM=kind PROFILE=dev-full NS=osac [MCP_DEMO_KIND_IMAGE=localhost/fulfillment-service:mcp-demo]
#   make build-mcp-demo-image MCP_DEMO_IMAGE=<registry>/<repository>:<tag> MCP_DEMO_PLATFORM=linux/amd64
#   make seed-mcp-demo-catalog PLATFORM=openshift PROFILE=vmaas-ci|vmaas-external NS=osac
#   make test            PLATFORM=kind|openshift  PROFILE=dev|dev-full|vmaas-ci|bmaas-ci|caas-ci|full-ci  NS=osac  SUITE=all|fulfillment|operator|bmf
#
# PROFILE=dev-full (kind only) is a superset of dev: it runs the same chart-based
# control plane, then layers on the full local dev stack (KubeVirt/CDI/Multus, AWX,
# UI, seeded catalog) via scripts/dev-full/. Linux needs rootful Podman (or Docker)
# and /dev/kvm for KubeVirt; macOS uses the invoking user's Docker Desktop or Podman
# Desktop connection without sudo. See scripts/dev-full/kind-runtime.sh and README.md.
#
# Helm chart management (no parameters required):
#   make helm-lint
#   make helm-validate

SHELL := /usr/bin/env bash -o pipefail
.SHELLFLAGS := -ec

# Extra Helm --set/--set-string args for each installation phase.
DEPS_HELM_ARGS ?=
INFRA_HELM_ARGS ?=
EXTRA_HELM_ARGS ?=
FULFILLMENT_GINKGO_ARGS ?=

# -- Fail-fast parameter validation ------------------------------------
# Guards fire at parse time, gated by MAKECMDGOALS so `make help` works.

ALL_TARGETS := install uninstall install-infra uninstall-infra install-osac uninstall-osac install-devstack install-mcp-demo install-mcp-demo-kind seed-mcp-demo-catalog test

ifneq ($(filter $(ALL_TARGETS),$(MAKECMDGOALS)),)
ifndef PLATFORM
$(error PLATFORM is required (kind|openshift))
endif

MCP_DEMO_TARGETS := install-mcp-demo seed-mcp-demo-catalog
ifneq ($(filter $(MCP_DEMO_TARGETS),$(MAKECMDGOALS)),)
ifeq ($(PLATFORM),openshift)
ifeq ($(filter $(PROFILE),vmaas-ci vmaas-external),)
$(error $(MCP_DEMO_TARGETS) require PLATFORM=openshift PROFILE=vmaas-ci or PROFILE=vmaas-external)
endif
else
$(error $(MCP_DEMO_TARGETS) require PLATFORM=openshift PROFILE=vmaas-ci or PROFILE=vmaas-external)
endif
endif

MCP_DEMO_KIND_TARGETS := install-mcp-demo-kind
ifneq ($(filter $(MCP_DEMO_KIND_TARGETS),$(MAKECMDGOALS)),)
ifneq ($(PLATFORM)/$(PROFILE),kind/dev-full)
$(error $(MCP_DEMO_KIND_TARGETS) requires PLATFORM=kind PROFILE=dev-full)
endif
ifneq ($(NS),osac)
$(error $(MCP_DEMO_KIND_TARGETS) requires NS=osac because dev-full is a single local instance)
endif
endif
ifeq ($(filter $(PLATFORM),kind openshift),)
$(error PLATFORM=$(PLATFORM) is invalid; must be kind or openshift)
endif
ifndef PROFILE
$(error PROFILE is required (dev|dev-full|vmaas-ci|vmaas-external|bmaas-ci|caas-ci|full-ci))
endif
ifeq ($(filter $(PROFILE),dev dev-full vmaas-ci vmaas-external bmaas-ci caas-ci full-ci),)
$(error PROFILE=$(PROFILE) is invalid; must be dev, dev-full, vmaas-ci, vmaas-external, bmaas-ci, caas-ci, or full-ci)
endif
ifndef NS
$(error NS is required (namespace for OSAC instance, e.g. NS=osac))
endif
ifeq ($(PLATFORM),kind)
ifeq ($(filter $(PROFILE),dev dev-full),)
$(error PLATFORM=kind only supports PROFILE=dev or PROFILE=dev-full)
endif
endif
endif

SUITE_TARGETS := test
ifneq ($(filter $(SUITE_TARGETS),$(MAKECMDGOALS)),)
ifndef SUITE
$(error SUITE is required (all|fulfillment|operator|bmf))
endif
ifeq ($(filter $(SUITE),all fulfillment operator bmf),)
$(error SUITE=$(SUITE) is invalid; must be all, fulfillment, operator, or bmf)
endif
endif

# -- Paths and constants -----------------------------------------------

CHARTS         := charts
DEPS_CHART     := $(CHARTS)/osac-deps
INFRA_CHART    := $(CHARTS)/osac-infra
OSAC_CHART     := $(CHARTS)/osac
DEVSTACK_CHART := $(CHARTS)/osac-devstack
DEVSTACK_AWX_REPO := awx-operator
DEVSTACK_AWX_REPO_URL := https://ansible-community.github.io/awx-operator-helm/

# Values file selection: kind uses kind-specific files, OpenShift uses profile files.
# dev-full reuses the identical dev control-plane values, plus a small overlay that
# disables AAP-based networking provisioning (no fabric on kind).
INSTANCE_VALUES_EXTRA :=
INFRA_VALUES_EXTRA :=
ifeq ($(PLATFORM),kind)
INFRA_VALUES    ?= values/dev/kind-infra.yaml
INSTANCE_VALUES := values/dev/kind-instance.yaml
ifeq ($(PROFILE),dev-full)
INSTANCE_VALUES_EXTRA := -f values/dev/kind-instance-devfull.yaml
INFRA_VALUES_EXTRA := -f values/dev/kind-infra-devfull.yaml
endif
else ifdef PROFILE
INFRA_VALUES    ?= values/$(PROFILE)/infra.yaml
INSTANCE_VALUES := values/$(PROFILE)/instance.yaml
endif

# Kind cluster create/delete routing. dev-full needs a rootful runtime (KubeVirt
# chowns /dev/kvm), so it goes through the cross-platform wrapper. Apple Silicon
# also uses the wrapper for both Kind profiles, keeping image build/load and Kind
# provider selection aligned.
KIND_RUNTIME := scripts/dev-full/kind-runtime.sh
KIND_ARM64_CLI_IMAGE := quay.io/openshift/origin-cli:4.20.0
# The OpenShift MCP demo deliberately requires the caller to supply a pullable
# registry image. Its cluster cannot pull a developer's local Podman image.
MCP_DEMO_IMAGE ?=
MCP_DEMO_PLATFORM ?= linux/amd64
MCP_DEMO_KIND_IMAGE ?= localhost/fulfillment-service:mcp-demo
MCP_DEMO_KIND_INFRA_HELM_ARGS ?=
MCP_DEMO_KIND_HELM_ARGS ?=
# dev-full hooks use grpcurl as well as kubectl, curl, jq, and Python. Build a
# native helper image locally and load it into Kind rather than relying on a
# developer to publish or pre-pull one.
DEVSTACK_CLI_TOOLS_IMAGE ?= localhost/osac-devstack-cli-tools:dev
DEVSTACK_CLI_TOOLS_ARCH ?= $(shell arch="$$(uname -m)"; if [ "$$arch" = x86_64 ]; then echo amd64; elif [ "$$arch" = aarch64 ] || [ "$$arch" = arm64 ]; then echo arm64; else echo "$$arch"; fi)
ifeq ($(PROFILE),vmaas-external)
MCP_DEMO_TENANT ?= osac-demo
else
MCP_DEMO_TENANT ?= osac-e2e-ci
endif
MCP_DEMO_STORAGE_TIER ?= local
export MCP_DEMO_IMAGE MCP_DEMO_PLATFORM MCP_DEMO_TENANT MCP_DEMO_STORAGE_TIER

# The vmaas-external profile uses existing, administrator-owned platform
# services. These values identify the isolated OSAC realm that the installer
# may create within the existing Keycloak instance; no Keycloak namespace,
# instance, route, or existing realm is adopted.
EXTERNAL_KEYCLOAK_NAMESPACE ?= keycloak
EXTERNAL_KEYCLOAK_INSTANCE_NAME ?= keycloak
EXTERNAL_KEYCLOAK_ROUTE_NAME ?= keycloak
EXTERNAL_KEYCLOAK_REALM ?= osac
EXTERNAL_CERT_ISSUER_NAME ?= default-ca
EXTERNAL_CA_BUNDLE_CONFIGMAP ?= ca-bundle
EXTERNAL_LVMS_STORAGE_CLASS ?= lvms-vg1

ifeq ($(PROFILE),vmaas-external)
EXTERNAL_KEYCLOAK_ROUTE_HOST = $$(oc -n "$(EXTERNAL_KEYCLOAK_NAMESPACE)" get route "$(EXTERNAL_KEYCLOAK_ROUTE_NAME)" -o jsonpath='{.spec.host}')
EXTERNAL_INFRA_HELM_ARGS = \
	--set keycloak.mode=external \
	--set-string keycloak.external.namespace=$(EXTERNAL_KEYCLOAK_NAMESPACE) \
	--set-string keycloak.external.instanceName=$(EXTERNAL_KEYCLOAK_INSTANCE_NAME) \
	--set-string keycloak.external.realmName=$(EXTERNAL_KEYCLOAK_REALM) \
	--set-string bundledVault.keycloakIssuerUrl="https://$(EXTERNAL_KEYCLOAK_ROUTE_HOST)/realms/$(EXTERNAL_KEYCLOAK_REALM)" \
	--set-string certs.issuerRef.kind=ClusterIssuer \
	--set-string certs.issuerRef.name=$(EXTERNAL_CERT_ISSUER_NAME)
EXTERNAL_OSAC_HELM_ARGS = \
	--set-string service.auth.issuerUrl="https://$(EXTERNAL_KEYCLOAK_ROUTE_HOST)/realms/$(EXTERNAL_KEYCLOAK_REALM)" \
	--set-string service.idp.url="https://$(EXTERNAL_KEYCLOAK_ROUTE_HOST)" \
	--set-string service.vault.keycloakIssuerUrl="https://$(EXTERNAL_KEYCLOAK_ROUTE_HOST)/realms/$(EXTERNAL_KEYCLOAK_REALM)" \
	--set-string service.certs.issuerRef.kind=ClusterIssuer \
	--set-string service.certs.issuerRef.name=$(EXTERNAL_CERT_ISSUER_NAME) \
	--set-string service.certs.caBundle.configMap=$(EXTERNAL_CA_BUNDLE_CONFIGMAP) \
	--set-string service.vault.caBundle.configMap=$(EXTERNAL_CA_BUNDLE_CONFIGMAP) \
	--set-string ui.api.fulfillment.certs.caBundle.configMap=$(EXTERNAL_CA_BUNDLE_CONFIGMAP) \
	--set-string ui.auth.certs.caBundle.configMap=$(EXTERNAL_CA_BUNDLE_CONFIGMAP) \
	--set-string operator.certs.issuerRef.kind=ClusterIssuer \
	--set-string operator.certs.issuerRef.name=$(EXTERNAL_CERT_ISSUER_NAME)
endif

# quay.io/openshift/origin-cli:4.20.0 has no arm64 manifest. On Apple Silicon,
# build a compatible local image with the same tag so every chart can continue
# using the shared cliImage value without platform-specific values files.
HOST_IS_MAC := $(shell if [ "$$(uname -s)" = "Darwin" ]; then echo true; fi)
HOST_IS_MAC_ARM := $(shell if [ "$$(uname -s)" = "Darwin" ] && { [ "$$(uname -m)" = "arm64" ] || [ "$$(sysctl -in hw.optional.arm64 2>/dev/null)" = "1" ]; }; then echo true; fi)
KIND_ARM64_CLI_IMAGE_SCRIPT := scripts/dev-full/build-arm64-cli-image.sh

# Container tool and Kind cluster config.
CONTAINER_TOOL ?= $(or $(shell command -v podman 2>/dev/null),$(shell command -v docker 2>/dev/null),$(error neither podman nor docker found in PATH))
KIND_CLUSTER_NAME ?= osac-dev
KIND_CONFIG := kind-config.yaml
# dev-full runs its cluster in a separate (rootful) podman daemon; give it a
# distinct kubeconfig so it never collides with the rootless PROFILE=dev cluster
# of the same name (both would otherwise share this one file and clobber it).
ifeq ($(PROFILE),dev-full)
KIND_KUBECONFIG := $(HOME)/.kube/$(KIND_CLUSTER_NAME)-kind-root.kubeconfig
else
KIND_KUBECONFIG := $(HOME)/.kube/$(KIND_CLUSTER_NAME)-kind.kubeconfig
endif
ifeq ($(PLATFORM),kind)
# The Kind runtime writes this file when it creates or reuses the local cluster.
# Always select it: a workstation may still export KUBECONFIG for a remote
# OpenShift cluster, and Helm otherwise upgrades that unrelated cluster.
# Set KIND_KUBECONFIG explicitly when a non-default local path is needed.
export KUBECONFIG := $(KIND_KUBECONFIG)
endif

define kind-load-image
	@if [ "$$(basename $(CONTAINER_TOOL))" = "podman" ]; then \
		tmpfile=$$(mktemp /tmp/kind-image-XXXXXX.tar); \
		$(CONTAINER_TOOL) save $(1) -o "$$tmpfile"; \
		$(KIND_RUNTIME) load image-archive "$$tmpfile" --name $(KIND_CLUSTER_NAME); \
		rm -f "$$tmpfile"; \
	else \
		$(KIND_RUNTIME) load docker-image $(1) --name $(KIND_CLUSTER_NAME); \
	fi
endef

# Podman on macOS runs inside a VM. Stream exactly the files copied by the
# fulfillment-service Containerfile into that VM so buildah sees a Linux build
# context rather than a host path. This is shared by the MCP image push and the
# Kind integration-test image load paths.
define build-fulfillment-service-image
	@if [ "$(HOST_IS_MAC)" = "true" ] && [ "$$(basename $(CONTAINER_TOOL))" = "podman" ]; then \
		tar -C .. -cf - \
			go.work \
			proto \
			fulfillment-service \
			bare-metal-fulfillment-operator \
			osac-operator/go.mod osac-operator/go.sum osac-operator/api \
			osac-csi-driver/go.mod osac-csi-driver/go.sum \
			osac-metering/schema/go.mod osac-metering/schema/go.sum \
			osac-metering/metering-service/go.mod osac-metering/metering-service/go.sum \
			osac-metering/adapters/go.mod osac-metering/adapters/go.sum \
			osac-ui/proxy/go.mod osac-ui/proxy/go.sum | \
		$(CONTAINER_TOOL) machine ssh -- podman build $(if $(strip $(2)),--platform="$(2)") -t $(1) -f fulfillment-service/Containerfile -; \
	else \
		$(CONTAINER_TOOL) build $(if $(strip $(2)),--platform="$(2)") -t $(1) -f ../fulfillment-service/Containerfile ..; \
	fi
endef

# Build the dev-full hook image for the Kind node's native architecture. The
# macOS Podman machine needs a streamed context for the same reason as the
# fulfillment-service image build above.
define build-devstack-cli-tools-image
	@if [ "$(HOST_IS_MAC)" = "true" ] && [ "$$(basename $(CONTAINER_TOOL))" = "podman" ]; then \
		tar -C $(DEVSTACK_CHART) -cf - Containerfile.cli-tools | \
		$(CONTAINER_TOOL) machine ssh -- podman build --build-arg TARGETARCH="$(2)" -t $(1) -f Containerfile.cli-tools -; \
	else \
		$(CONTAINER_TOOL) build --build-arg TARGETARCH="$(2)" -t $(1) -f $(DEVSTACK_CHART)/Containerfile.cli-tools $(DEVSTACK_CHART); \
	fi
endef

# -- Help --------------------------------------------------------------

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Entity: Infra

.PHONY: install-infra
install-infra: ## Install infrastructure (PLATFORM= PROFILE= NS= required)
ifeq ($(PLATFORM),kind)
ifeq ($(HOST_IS_MAC_ARM),true)
	@$(KIND_RUNTIME) check
	@$(KIND_RUNTIME) create-cluster $(KIND_CLUSTER_NAME) $(KIND_CONFIG) $(KIND_KUBECONFIG)
else ifeq ($(PROFILE),dev-full)
	@$(KIND_RUNTIME) check
	@$(KIND_RUNTIME) create-cluster $(KIND_CLUSTER_NAME) $(KIND_CONFIG) $(KIND_KUBECONFIG)
else
	@if kind get clusters | grep -q "^$(KIND_CLUSTER_NAME)$$"; then \
		echo "Kind cluster '$(KIND_CLUSTER_NAME)' already exists"; \
	else \
		echo "Creating Kind cluster '$(KIND_CLUSTER_NAME)'..."; \
		kind create cluster --name $(KIND_CLUSTER_NAME) --config $(KIND_CONFIG) --wait 60s; \
	fi
	@kind export kubeconfig --name $(KIND_CLUSTER_NAME) --kubeconfig $(KIND_KUBECONFIG)
endif
ifeq ($(HOST_IS_MAC_ARM),true)
	@$(KIND_ARM64_CLI_IMAGE_SCRIPT) $(KIND_CLUSTER_NAME) $(KIND_ARM64_CLI_IMAGE)
endif
	helm upgrade --install cert-manager oci://quay.io/jetstack/charts/cert-manager \
		--version v1.20.0 --namespace cert-manager --create-namespace \
		--set crds.enabled=true --wait --timeout 5m
	helm upgrade --install trust-manager oci://quay.io/jetstack/charts/trust-manager \
		--version v0.22.0 --namespace cert-manager \
		--set defaultPackage.enabled=false --wait --timeout 5m
	helm upgrade --install envoy-gateway oci://docker.io/envoyproxy/gateway-helm \
		--version v1.6.5 --namespace envoy-gateway --create-namespace \
		--wait --timeout 5m
	helm upgrade --install strimzi-cluster-operator oci://quay.io/strimzi-helm/strimzi-kafka-operator \
		--version 1.1.0 --namespace osac-kafka --create-namespace \
		--wait --timeout 5m
	helm upgrade --install osac-infra $(INFRA_CHART) \
		--namespace osac-infra --create-namespace \
		-f $(INFRA_VALUES) $(INFRA_VALUES_EXTRA) \
		--set osacNamespace=$(NS) \
		$(INFRA_HELM_ARGS) \
		--wait-for-jobs --timeout 30m \
	|| { echo "=== osac-infra hook jobs ==="; kubectl get jobs,pods,cm -n osac-infra || true; kubectl logs -n osac-infra -l job-name=osac-infra-apply-ca-bundle --tail=200 || true; kubectl get deploy,endpoints,certificate -n cert-manager || true; exit 1; }
	@for f in ../osac-operator/config/crd/fakes/*.yaml; do \
		case "$$(basename "$$f")" in \
			*osac.openshift.io*) continue ;; \
		esac; \
		kubectl apply --server-side --force-conflicts -f "$$f"; \
	done
else
	$(eval OCP_VERSION := $(shell oc get clusterversion version -o jsonpath='{.status.desired.version}' | cut -d. -f1,2))
ifeq ($(PROFILE),vmaas-external)
	oc create namespace $(NS) --dry-run=client -o yaml | oc apply -f -
	bash ./scripts/validate-external-vmaas-prerequisites.sh "$(NS)" "$(EXTERNAL_KEYCLOAK_NAMESPACE)" "$(EXTERNAL_KEYCLOAK_INSTANCE_NAME)" "$(EXTERNAL_KEYCLOAK_ROUTE_NAME)" "$(EXTERNAL_KEYCLOAK_REALM)" "$(EXTERNAL_CERT_ISSUER_NAME)" "$(EXTERNAL_CA_BUNDLE_CONFIGMAP)" "$(EXTERNAL_LVMS_STORAGE_CLASS)"
endif
	@bash -c 'source scripts/lib.sh && \
		for ns in ansible-aap cert-manager-operator cert-manager openshift-storage metallb-system multicluster-engine openshift-cnv osac-kafka; do \
			wait_for_namespace_cleanup "$$ns"; \
		done'
	helm upgrade --install osac-deps $(DEPS_CHART) \
		--namespace osac-deps --create-namespace \
		--values $(INFRA_VALUES) \
		--set lvms.channel=stable-$(OCP_VERSION) \
		$(DEPS_HELM_ARGS) \
		--timeout 30m --wait
	@bash -c 'source scripts/lib.sh && wait_for_namespace_cleanup keycloak'
	$(eval DOMAIN := $(shell oc get ingresses.config/cluster -o jsonpath='{.spec.domain}'))
	@[ -n "$(DOMAIN)" ] || { echo "ERROR: could not determine cluster domain" >&2; exit 1; }
	helm upgrade --install osac-infra $(INFRA_CHART) \
		--namespace osac-infra --create-namespace \
		--values $(INFRA_VALUES) \
		--set osacNamespace=$(NS) \
		--set lvms.channel=stable-$(OCP_VERSION) \
		--set keycloak.hostname=https://keycloak-keycloak.$(DOMAIN) \
		--set keycloak.route.hostname=keycloak-keycloak.$(DOMAIN) \
		--set "bundledVault.keycloakIssuerUrl=https://keycloak-keycloak.$(DOMAIN)/realms/osac" \
		$(INFRA_HELM_ARGS) \
		$(EXTERNAL_INFRA_HELM_ARGS) \
		--timeout 30m --wait-for-jobs \
	|| { echo "=== osac-infra hook jobs ==="; oc get jobs,pods,cm -n osac-infra || true; oc logs -n osac-infra -l job-name=osac-infra-apply-ca-bundle --tail=200 || true; oc get deploy,endpoints,certificate -n cert-manager || true; exit 1; }
endif

.PHONY: uninstall-infra
uninstall-infra: ## Uninstall infrastructure (PLATFORM= PROFILE= NS= required)
ifeq ($(PLATFORM),kind)
ifeq ($(HOST_IS_MAC_ARM),true)
	@$(KIND_RUNTIME) delete-cluster $(KIND_CLUSTER_NAME)
else ifeq ($(PROFILE),dev-full)
	@$(KIND_RUNTIME) delete-cluster $(KIND_CLUSTER_NAME)
else
	helm uninstall osac-infra --namespace osac-infra --ignore-not-found
	helm uninstall strimzi-cluster-operator --namespace osac-kafka --ignore-not-found
	helm uninstall envoy-gateway --namespace envoy-gateway --ignore-not-found
	helm uninstall trust-manager --namespace cert-manager --ignore-not-found
	helm uninstall cert-manager --namespace cert-manager --ignore-not-found
	kind delete cluster --name $(KIND_CLUSTER_NAME)
endif
else
	helm uninstall osac-infra --namespace osac-infra --ignore-not-found --wait --timeout 10m
	helm uninstall osac-deps --namespace osac-deps --ignore-not-found
endif

##@ Entity: Osac

AAP_LICENSE_FILE ?= values/$(PROFILE)/license.zip

.PHONY: install-osac
install-osac: ## Install OSAC instance (PLATFORM= PROFILE= NS= required)
	helm dependency build $(OSAC_CHART)
ifneq ($(PLATFORM),kind)
	@[ -n "$(AAP_LICENSE_FILE)" ] || { echo "ERROR: AAP_LICENSE_FILE is required for PLATFORM=openshift" >&2; exit 1; }
	@[ -f "$(AAP_LICENSE_FILE)" ] || { echo "ERROR: AAP_LICENSE_FILE not found at $(AAP_LICENSE_FILE)" >&2; exit 1; }
	oc create namespace $(NS) --dry-run=client -o yaml | oc apply -f -
ifeq ($(PROFILE),vmaas-external)
	bash ./scripts/validate-external-vmaas-prerequisites.sh "$(NS)" "$(EXTERNAL_KEYCLOAK_NAMESPACE)" "$(EXTERNAL_KEYCLOAK_INSTANCE_NAME)" "$(EXTERNAL_KEYCLOAK_ROUTE_NAME)" "$(EXTERNAL_KEYCLOAK_REALM)" "$(EXTERNAL_CERT_ISSUER_NAME)" "$(EXTERNAL_CA_BUNDLE_CONFIGMAP)" "$(EXTERNAL_LVMS_STORAGE_CLASS)"
endif
	oc create secret generic config-as-code-manifest-ig \
		--from-file=license.zip="$(AAP_LICENSE_FILE)" \
		-n $(NS) --dry-run=client -o yaml | oc apply --server-side -f -
	oc label secret config-as-code-manifest-ig \
		osac.openshift.io/project=osac-aap \
		-n $(NS) --overwrite
	@bash -c 'source scripts/lib.sh && check_postgres_prerequisites "$(NS)" "$(INFRA_VALUES)"'
	$(eval DOMAIN := $(shell oc get ingresses.config/cluster -o jsonpath='{.spec.domain}'))
	@[ -n "$(DOMAIN)" ] || { echo "ERROR: Could not determine cluster domain. Is oc logged in?" >&2; exit 1; }
endif
	helm upgrade --install osac $(OSAC_CHART) \
		-f $(INSTANCE_VALUES) $(INSTANCE_VALUES_EXTRA) \
		--namespace $(NS) --create-namespace \
		--set-string global.osacDeploymentId=$(NS)/osac \
		$(if $(DOMAIN),--set service.externalHostname=fulfillment-api-$(NS).$(DOMAIN) --set service.internalHostname=fulfillment-internal-api-$(NS).$(DOMAIN) --set global.clusterDomain=$(DOMAIN)) \
		$(EXTRA_HELM_ARGS) \
		$(EXTERNAL_OSAC_HELM_ARGS) \
		--wait --timeout 40m

.PHONY: uninstall-osac
uninstall-osac: ## Uninstall OSAC instance (PLATFORM= PROFILE= NS= required)
	helm uninstall osac --namespace $(NS) --ignore-not-found

##@ Entity: Dev Stack (kind PROFILE=dev-full only)

.PHONY: install-devstack
install-devstack: ## Install dev-full extras: KubeVirt/CDI/Multus + AWX + UI + seeded catalog
ifneq ($(PLATFORM)/$(PROFILE),kind/dev-full)
	@echo "ERROR: install-devstack requires PLATFORM=kind PROFILE=dev-full" >&2; exit 1
endif
	# Pre-install: node-level CNI setup (requires host container runtime access)
	scripts/dev-full/install-virt-node-setup.sh $(KIND_CLUSTER_NAME)
	# Build and load the native helper used by catalog, tenant, and AWX hook Jobs.
	$(call build-devstack-cli-tools-image,$(DEVSTACK_CLI_TOOLS_IMAGE),$(DEVSTACK_CLI_TOOLS_ARCH))
	$(call kind-load-image,$(DEVSTACK_CLI_TOOLS_IMAGE))
	# Helm install: operators + AWX + UI + seeding
	helm repo add $(DEVSTACK_AWX_REPO) $(DEVSTACK_AWX_REPO_URL) --force-update
	helm dependency build $(DEVSTACK_CHART)
	helm upgrade --install osac-devstack $(DEVSTACK_CHART) \
		--namespace $(NS) --create-namespace \
		--set osacNamespace=$(NS) \
		--set kindClusterName=$(KIND_CLUSTER_NAME) \
		--set-string cliImage=$(DEVSTACK_CLI_TOOLS_IMAGE) \
		--wait --timeout 30m

##@ Composite

.PHONY: install
ifeq ($(PROFILE),dev-full)
install: install-infra install-osac install-devstack ## Full install: infra + osac (+ dev stack for dev-full)
else
install: install-infra install-osac ## Full install: infra + osac (PLATFORM= PROFILE= required)
endif

.PHONY: build-mcp-demo-image
build-mcp-demo-image: ## Build, verify, and push the MCP demo image (MCP_DEMO_IMAGE= required)
	@./scripts/validate-mcp-demo-image.sh "$${MCP_DEMO_IMAGE}" "$${MCP_DEMO_PLATFORM}"
	$(call build-fulfillment-service-image,$${MCP_DEMO_IMAGE},$${MCP_DEMO_PLATFORM})
	@actual_platform="$$( $(CONTAINER_TOOL) image inspect --format '{{.Os}}/{{.Architecture}}' "$${MCP_DEMO_IMAGE}")"; \
		[ "$$actual_platform" = "$${MCP_DEMO_PLATFORM}" ] || { \
			echo "ERROR: built MCP demo image platform $$actual_platform does not match $${MCP_DEMO_PLATFORM}" >&2; \
			exit 1; \
		}
	$(CONTAINER_TOOL) push "$${MCP_DEMO_IMAGE}"

.PHONY: install-mcp-demo
install-mcp-demo: ## Install OpenShift VMaaS OSAC, push and deploy its MCP server, then seed the VM catalog
	@./scripts/validate-mcp-demo-image.sh "$${MCP_DEMO_IMAGE}" "$${MCP_DEMO_PLATFORM}"
	$(MAKE) install-infra PLATFORM=$(PLATFORM) PROFILE=$(PROFILE) NS=$(NS) \
		DEPS_HELM_ARGS="$(DEPS_HELM_ARGS)" INFRA_HELM_ARGS="$(INFRA_HELM_ARGS)"
	$(MAKE) build-mcp-demo-image MCP_DEMO_IMAGE="$${MCP_DEMO_IMAGE}" MCP_DEMO_PLATFORM="$${MCP_DEMO_PLATFORM}"
	@domain="$$(oc get ingresses.config/cluster -o jsonpath='{.spec.domain}')"; \
		[ -n "$$domain" ] || { echo "ERROR: could not determine cluster domain" >&2; exit 1; }; \
		$(MAKE) install-osac PLATFORM=$(PLATFORM) PROFILE=$(PROFILE) NS=$(NS) \
			EXTRA_HELM_ARGS="$(EXTRA_HELM_ARGS) --set service.mcp.enabled=true --set-string service.mcp.externalHostname=mcp-$(NS).$$domain --set service.images.service=$${MCP_DEMO_IMAGE} --set service.images.pullPolicy=Always"
	$(MAKE) seed-mcp-demo-catalog PLATFORM=$(PLATFORM) PROFILE=$(PROFILE) NS=$(NS)

.PHONY: install-mcp-demo-kind
install-mcp-demo-kind: ## Install the local Kind dev-full MCP demo using a freshly built, loaded image
	$(MAKE) install-infra PLATFORM=$(PLATFORM) PROFILE=$(PROFILE) NS=$(NS) \
		DEPS_HELM_ARGS="" INFRA_HELM_ARGS="$(MCP_DEMO_KIND_INFRA_HELM_ARGS)"
	$(call build-fulfillment-service-image,$(MCP_DEMO_KIND_IMAGE))
	$(call kind-load-image,$(MCP_DEMO_KIND_IMAGE))
	$(MAKE) install-osac PLATFORM=$(PLATFORM) PROFILE=$(PROFILE) NS=$(NS) \
		EXTRA_HELM_ARGS="$(MCP_DEMO_KIND_HELM_ARGS) --set service.mcp.enabled=true --set-string service.mcp.externalHostname=mcp.osac.localhost --set service.mcp.externalPort=8443 --set service.images.service=$(MCP_DEMO_KIND_IMAGE) --set service.images.pullPolicy=Never"
	$(MAKE) install-devstack PLATFORM=$(PLATFORM) PROFILE=$(PROFILE) NS=$(NS)
	kubectl rollout restart deployment/fulfillment-mcp-server -n $(NS)
	kubectl rollout status deployment/fulfillment-mcp-server -n $(NS) --timeout=5m
	@printf 'MCP demo is ready at https://mcp.osac.localhost:8443\n'

.PHONY: seed-mcp-demo-catalog
seed-mcp-demo-catalog: ## Seed and validate the OpenShift VMaaS catalog and tenant prerequisites for the MCP PoC
	./scripts/seed-mcp-demo-catalog.sh $(NS)

.PHONY: uninstall
uninstall: uninstall-osac uninstall-infra ## Full uninstall: osac + infra (PLATFORM= PROFILE= required)

##@ Entity: Test

.PHONY: test
test: ## Run integration tests (SUITE= required: all|fulfillment|operator|bmf)
ifeq ($(SUITE),all)
	$(MAKE) test SUITE=fulfillment
	$(MAKE) test SUITE=operator
	$(MAKE) test SUITE=bmf
endif
ifeq ($(SUITE),fulfillment)
	$(call build-fulfillment-service-image,localhost/fulfillment-service:it)
	$(call kind-load-image,localhost/fulfillment-service:it)
	$(MAKE) install-osac PLATFORM=$(PLATFORM) PROFILE=$(PROFILE) NS=$(NS) \
		EXTRA_HELM_ARGS="--set service.images.service=localhost/fulfillment-service:it --set operator.enabled=false --set networkClass.enabled=false"
	@for f in ../fulfillment-service/it/crds/*.yaml; do kubectl apply --server-side --field-manager=helm --force-conflicts -f "$$f"; done
	cd ../fulfillment-service && ginkgo run --timeout 1h -v $(FULFILLMENT_GINKGO_ARGS) it
	$(MAKE) install-osac PLATFORM=$(PLATFORM) PROFILE=$(PROFILE) NS=$(NS) \
		EXTRA_HELM_ARGS="--set service.images.service=localhost/fulfillment-service:it --set global.services.bmaas.enabled=false --set operator.enabled=false --set networkClass.enabled=false"
	cd ../fulfillment-service && IT_TEST_SUITE=bmaas-disabled ginkgo run --timeout 1h -v --focus 'Service enablement' it
endif
ifeq ($(SUITE),operator)
	$(CONTAINER_TOOL) build -t localhost/osac-operator:it -f ../osac-operator/Containerfile ..
	$(call kind-load-image,localhost/osac-operator:it)
	$(MAKE) install-osac PLATFORM=$(PLATFORM) PROFILE=$(PROFILE) NS=$(NS) \
		EXTRA_HELM_ARGS="--set operator.image.repository=localhost/osac-operator --set operator.image.tag=it --set operator.image.pullPolicy=Never --set service.enabled=false --set hubAccess.enabled=false --set validation.enabled=false --set networkClass.enabled=false"
	cd ../osac-operator && ginkgo run --timeout 30m -v test/integration
endif
ifeq ($(SUITE),bmf)
	$(CONTAINER_TOOL) build -t localhost/bmf-operator:it -f ../bare-metal-fulfillment-operator/Containerfile ..
	$(call kind-load-image,localhost/bmf-operator:it)
	kubectl apply --server-side --force-conflicts -f ../bare-metal-fulfillment-operator/test/crds/
	$(MAKE) install-osac PLATFORM=$(PLATFORM) PROFILE=$(PROFILE) NS=$(NS) \
		EXTRA_HELM_ARGS="--set global.services.bmaas.enabled=true --set bmf.replicaCount=1 --set bmf.image.repository=localhost/bmf-operator --set bmf.image.tag=it --set bmf.image.pullPolicy=Never --set operator.enabled=false --set service.enabled=false --set hubAccess.enabled=false --set validation.enabled=false --set networkClass.enabled=false"
	cd ../bare-metal-fulfillment-operator && ginkgo run --timeout 30m -v test/integration
endif

##@ Charts

.PHONY: sync-charts
sync-charts: helm-deps ## Rebuild chart dependencies (alias for helm-deps)

.PHONY: helm-deps
helm-deps: ## Build all local Helm chart dependencies
	helm dependency build $(OSAC_CHART)
	helm repo add $(DEVSTACK_AWX_REPO) $(DEVSTACK_AWX_REPO_URL) --force-update
	helm dependency build $(DEVSTACK_CHART)

.PHONY: helm-lint
helm-lint: helm-deps ## Lint all charts
	helm lint $(DEPS_CHART)
	helm lint $(INFRA_CHART)
	helm lint $(OSAC_CHART)
	helm lint $(DEVSTACK_CHART)

.PHONY: helm-template
helm-template: helm-deps ## Dry-run render all templates
	helm template osac-deps $(DEPS_CHART)
	helm template osac-infra $(INFRA_CHART)
	helm template osac $(OSAC_CHART)
	helm template osac-devstack $(DEVSTACK_CHART)

.PHONY: helm-validate
helm-validate: helm-lint ## Validate all charts (lint + template)
	@for d in values/*/; do \
		infra="$${d}infra.yaml"; \
		[ -f "$$infra" ] && echo "Validating $$infra..." && \
			helm template osac-deps $(DEPS_CHART) --values "$$infra" && \
			helm template osac-infra $(INFRA_CHART) --values "$$infra"; \
	done
	@echo "Validating values/dev/kind-infra.yaml..."
	helm template osac-infra $(INFRA_CHART) --values values/dev/kind-infra.yaml
	./scripts/validate-postgres-prerequisites.sh
	./scripts/validate-keycloak-credentials.sh
	./scripts/validate-external-keycloak.sh
	./scripts/validate-cnv-hook.sh
	./scripts/validate-mcp-demo-catalog.sh
	@echo "Validation passed."
