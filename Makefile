# Copyright 2026 Dicer Authors
# SPDX-License-Identifier: MIT

SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

# dicerd and its embedded guest binaries target Linux on this architecture
# unless told otherwise, e.g. `make build GOARCH=arm64`.
GOARCH ?= $(shell go env GOARCH)
ARCHES := amd64 arm64

BIN_DIR ?= $(CURDIR)/bin

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/dicer-sh/dicer.Version=$(VERSION) \
	-X github.com/dicer-sh/dicer.Commit=$(COMMIT) \
	-X github.com/dicer-sh/dicer.BuildDate=$(DATE)

# Tools, pinned so every machine generates and lints identically. They are
# built with at least the toolchain go.mod selects -- `go install tool@version`
# would otherwise use the tool's own, and a linter built with an older Go than
# this module's refuses to load it -- and with a newer one where the tool
# itself asks for that.
GO_TOOLCHAIN := $(shell go env GOVERSION)
BUF_VERSION           := v1.73.0
OAPI_CODEGEN_VERSION  := v2.5.1
ADDLICENSE_VERSION    := v1.2.0
GOLANGCI_LINT_VERSION := v2.8.0

BUF           := $(BIN_DIR)/buf-$(BUF_VERSION)
OAPI_CODEGEN  := $(BIN_DIR)/oapi-codegen-$(OAPI_CODEGEN_VERSION)
ADDLICENSE    := $(BIN_DIR)/addlicense-$(ADDLICENSE_VERSION)
GOLANGCI_LINT := $(BIN_DIR)/golangci-lint-$(GOLANGCI_LINT_VERSION)

# Embedded binaries. Each dicerd embeds only its own architecture's; see
# internal/initrd/embed.go and internal/hypervisor/cloudhypervisor/embed.go.
GUEST_BIN := internal/initrd/bin
CH_BIN    := internal/hypervisor/cloudhypervisor/bin
FC_BIN    := internal/hypervisor/firecracker/bin
CH_SPEC   := specs/cloud-hypervisor/v0.3.0/spec.yaml
# Cloud Hypervisor directory names are the full semver; its release tags
# drop the patch, e.g. v49.0.0 is released as v49.0.
CH_VERSIONS       := v48.0.0 v49.0.0
CH_ASSET_amd64    := cloud-hypervisor-static
CH_ASSET_arm64    := cloud-hypervisor-static-aarch64
ch_release_tag     = $(patsubst %.0,%,$(1))
# Firecracker ships a tarball per architecture, named for the machine
# rather than the Go architecture.
FC_VERSIONS       := v1.17.0
FC_MACHINE_amd64  := x86_64
FC_MACHINE_arm64  := aarch64

LICENSE_IGNORE := -ignore 'bin/**' -ignore '**/bin/**' -ignore 'specs/**'

##@ Building

.PHONY: build
build: embedded hypervisor-binaries ## Build dicer and dicerd into bin/
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/dicer ./cmd/dicer
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -trimpath -tags containers_image_openpgp \
		-ldflags '$(LDFLAGS)' -o $(BIN_DIR)/dicerd ./cmd/dicerd

.PHONY: embedded
embedded: $(GUEST_BIN)/$(GOARCH)/dicer-init $(GUEST_BIN)/$(GOARCH)/dicer-agent ## Build the guest binaries dicerd embeds

.PHONY: embedded-all
embedded-all: $(foreach a,$(ARCHES),$(GUEST_BIN)/$(a)/dicer-init $(GUEST_BIN)/$(a)/dicer-agent)

# Always rebuilt: the Go build cache makes that cheap, and it is simpler and
# more reliable than restating every package the guest binaries import.
$(GUEST_BIN)/%/dicer-init: FORCE
	CGO_ENABLED=0 GOOS=linux GOARCH=$* go build -trimpath -ldflags '$(LDFLAGS)' -o $@ ./cmd/dicer-init

$(GUEST_BIN)/%/dicer-agent: FORCE
	CGO_ENABLED=0 GOOS=linux GOARCH=$* go build -trimpath -ldflags '$(LDFLAGS)' -o $@ ./cmd/dicer-agent

hypervisor_binaries = $(foreach v,$(CH_VERSIONS),$(CH_BIN)/$(1)/$(v)/cloud-hypervisor) \
	$(foreach v,$(FC_VERSIONS),$(FC_BIN)/$(1)/$(v)/firecracker)

.PHONY: hypervisor-binaries
hypervisor-binaries: $(call hypervisor_binaries,$(GOARCH)) ## Download the hypervisor binaries dicerd embeds

.PHONY: hypervisor-binaries-all
hypervisor-binaries-all: $(foreach a,$(ARCHES),$(call hypervisor_binaries,$(a)))

define ch_download
$(CH_BIN)/$(1)/$(2)/cloud-hypervisor:
	@mkdir -p $$(@D)
	curl -fsSL -o $$@ \
		https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/$(call ch_release_tag,$(2))/$(CH_ASSET_$(1))
	chmod +x $$@
endef
$(foreach a,$(ARCHES),$(foreach v,$(CH_VERSIONS),$(eval $(call ch_download,$(a),$(v)))))

# Firecracker's release asset is a tarball of every tool it builds; only the
# VMM itself is wanted.
define fc_download
$(FC_BIN)/$(1)/$(2)/firecracker:
	@mkdir -p $$(@D)
	curl -fsSL https://github.com/firecracker-microvm/firecracker/releases/download/$(2)/firecracker-$(2)-$(FC_MACHINE_$(1)).tgz \
		| tar -xzO release-$(2)-$(FC_MACHINE_$(1))/firecracker-$(2)-$(FC_MACHINE_$(1)) > $$@
	chmod +x $$@
endef
$(foreach a,$(ARCHES),$(foreach v,$(FC_VERSIONS),$(eval $(call fc_download,$(a),$(v)))))

.PHONY: release-prep
release-prep: embedded-all hypervisor-binaries-all ## Prepare every architecture's embedded binaries (run by GoReleaser)

##@ Testing

.PHONY: test
test: ## Run the unit tests
	go test -race -short ./...

.PHONY: test-integration
test-integration: ## Run all tests, including those that reach a registry
	go test -race ./...

.PHONY: test-e2e
test-e2e: ## Boot real VMs on a remote host (needs DICER_E2E_HOST; see DEVELOPMENT.md)
	@test -n "$(DICER_E2E_HOST)" || { echo "set DICER_E2E_HOST=<addr> to run the end-to-end tests"; exit 1; }
	DICER_E2E_HOST='$(DICER_E2E_HOST)' \
	DICER_E2E_SSH_USER='$(DICER_E2E_SSH_USER)' \
	DICER_E2E_SSH_KEY='$(DICER_E2E_SSH_KEY)' \
	DICER_E2E_KERNEL_URL='$(DICER_E2E_KERNEL_URL)' \
	DICER_E2E_KEEP='$(DICER_E2E_KEEP)' \
	go test -tags e2e -count 1 -v -timeout 30m ./test/e2e/...

.PHONY: lint
lint: $(GOLANGCI_LINT) $(BUF) ## Run the linters
	$(GOLANGCI_LINT) run ./...
	# The end-to-end tests are behind a build tag, so the default run does
	# not see them at all. Linted separately rather than left to rot.
	$(GOLANGCI_LINT) run --build-tags e2e ./test/...
	$(BUF) lint

.PHONY: fmt
fmt: $(GOLANGCI_LINT) $(BUF) ## Format the code
	$(GOLANGCI_LINT) fmt ./...
	$(BUF) format -w

.PHONY: license-check
license-check: $(ADDLICENSE) ## Check every source file carries a licence header
	$(ADDLICENSE) -check -c "Dicer Authors" -l mit -s=only $(LICENSE_IGNORE) .

.PHONY: license
license: $(ADDLICENSE) ## Add missing licence headers
	$(ADDLICENSE) -c "Dicer Authors" -l mit -s=only $(LICENSE_IGNORE) .

##@ Code generation

.PHONY: generate
generate: generate-proto generate-hypervisor-client ## Regenerate all generated code

.PHONY: generate-proto
generate-proto: $(BUF) ## Regenerate the gRPC code from proto/
	$(BUF) generate

.PHONY: generate-hypervisor-client
generate-hypervisor-client: $(OAPI_CODEGEN) ## Regenerate the Cloud Hypervisor API client
	$(OAPI_CODEGEN) -config internal/hypervisor/cloudhypervisor/oapi-codegen.yaml $(CH_SPEC)

.PHONY: update-hypervisor-spec
update-hypervisor-spec: ## Download the Cloud Hypervisor OpenAPI spec
	curl -fsSL -o $(CH_SPEC) \
		https://raw.githubusercontent.com/cloud-hypervisor/cloud-hypervisor/refs/tags/v48.0/vmm/src/api/openapi/cloud-hypervisor.yaml

##@ Tools

$(BUF): | $(BIN_DIR)
	GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto GOBIN=$(BIN_DIR) go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	mv $(BIN_DIR)/buf $@

$(OAPI_CODEGEN): | $(BIN_DIR)
	GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto GOBIN=$(BIN_DIR) go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)
	mv $(BIN_DIR)/oapi-codegen $@

$(ADDLICENSE): | $(BIN_DIR)
	GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto GOBIN=$(BIN_DIR) go install github.com/google/addlicense@$(ADDLICENSE_VERSION)
	mv $(BIN_DIR)/addlicense $@

$(GOLANGCI_LINT): | $(BIN_DIR)
	GOTOOLCHAIN=$(GO_TOOLCHAIN)+auto GOBIN=$(BIN_DIR) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv $(BIN_DIR)/golangci-lint $@

$(BIN_DIR):
	mkdir -p $@

.PHONY: tools
tools: $(BUF) $(OAPI_CODEGEN) $(ADDLICENSE) $(GOLANGCI_LINT) ## Install the pinned development tools into bin/

##@ Housekeeping

.PHONY: clean
clean: ## Remove build output and downloaded binaries
	rm -rf $(BIN_DIR) $(GUEST_BIN) $(CH_BIN) $(FC_BIN)

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n"} \
		/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-28s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

.PHONY: FORCE
FORCE:
