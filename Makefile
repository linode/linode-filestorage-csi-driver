IMAGE_VERSION ?= dev
IMAGE_TAGS ?= $(IMAGE_VERSION)
KO_DOCKER_REPO ?= docker.io/linode/linode-filestorage-csi-driver
RELEASE_IMAGE_REPO ?= docker.io/linode/linode-filestorage-csi-driver
RELEASE_DIR ?= release

#####################################################################
# OS / ARCH
#####################################################################
OS=$(shell uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(shell uname -m)
ARCH_SHORT=$(ARCH)
ifeq ($(ARCH_SHORT),x86_64)
ARCH_SHORT := amd64
else ifeq ($(ARCH_SHORT),aarch64)
ARCH_SHORT := arm64
endif

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet: fmt
	go vet ./...

.PHONY: lint
lint: golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: test
test:
	go test ./...

.PHONY: build
build:
	go build -ldflags "-X main.vendorVersion=$(IMAGE_VERSION)" ./...

.PHONY: ko-build
ko-build: ko
	$(KO) build --local --bare --tags $(IMAGE_TAGS) .

.PHONY: ko-publish
ko-publish: ko
	KO_DOCKER_REPO=$(KO_DOCKER_REPO) $(KO) build --bare --tags $(IMAGE_TAGS) .

.PHONY: helm-lint
helm-lint: helm
	$(HELM) lint charts/linode-nfs-csi-driver

.PHONY: release
release: helm kustomize
	rm -rf $(RELEASE_DIR)
	mkdir -p $(RELEASE_DIR)
	$(HELM) package charts/linode-nfs-csi-driver \
		--version "$(patsubst v%,%,$(IMAGE_VERSION))" \
		--app-version "$(IMAGE_VERSION)" \
		--destination "$(RELEASE_DIR)" >/dev/null
	mv "$(RELEASE_DIR)/linode-nfs-csi-driver-$(patsubst v%,%,$(IMAGE_VERSION)).tgz" "$(RELEASE_DIR)/helm-chart-$(IMAGE_VERSION).tgz"
	$(KUSTOMIZE) build deploy/kubernetes/base \
		| sed "s|$(RELEASE_IMAGE_REPO):dev|$(RELEASE_IMAGE_REPO):$(IMAGE_VERSION)|g" > "$(RELEASE_DIR)/linode-filestorage-csi-driver-$(IMAGE_VERSION).yaml"

.PHONY: ci
ci: vet lint test build

## --------------------------------------
## Tooling Binaries
## --------------------------------------

##@ Tooling Binaries:

LOCALBIN ?= $(CURDIR)/bin
CACHE_BIN ?= $(LOCALBIN)

export PATH := $(CACHE_BIN):$(PATH)

$(LOCALBIN):
	mkdir -p $(LOCALBIN)


ifneq ($(CACHE_BIN),$(LOCALBIN))
$(CACHE_BIN):
	mkdir -p $(CACHE_BIN)
endif

GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint
HELM ?= $(LOCALBIN)/helm
KO ?= $(LOCALBIN)/ko
KUSTOMIZE ?= $(LOCALBIN)/kustomize

## Tool Versions
# renovate: datasource=go depName=github.com/golangci/golangci-lint/v2
GOLANGCI_LINT_VERSION ?= v2.11.4

# renovate: datasource=github-releases depName=helm/helm packageName=helm/helm
HELM_VERSION ?= v3.19.0

# renovate: datasource=github-tags depName=ko-build/ko packageName=github.com/google/ko
KO_VERSION ?= v0.18.1

# renovate: datasource=go depName=sigs.k8s.io/kustomize/kustomize/v5 packageName=sigs.k8s.io/kustomize/kustomize/v5
KUSTOMIZE_VERSION ?= v5.7.1

.PHONY: tools
tools: $(GOLANGCI_LINT) $(HELM) $(KO) $(KUSTOMIZE)

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: ko
ko: $(KO) ## Download ko locally if necessary.
$(KO): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install github.com/google/ko@$(KO_VERSION)

.PHONY: helm
helm: $(HELM) ## Download Helm locally if necessary.
$(HELM): $(LOCALBIN)
	TMPDIR=$$(mktemp -d); ARCHIVE="helm-$(HELM_VERSION)-$(OS)-$(ARCH_SHORT).tar.gz"; curl -fsSL "https://get.helm.sh/$$ARCHIVE" -o "$$TMPDIR/$$ARCHIVE" && curl -fsSL "https://get.helm.sh/$$ARCHIVE.sha256" -o "$$TMPDIR/$$ARCHIVE.sha256" && EXPECTED=$$(tr -d '\n' < "$$TMPDIR/$$ARCHIVE.sha256"); ACTUAL=$$(shasum -a 256 "$$TMPDIR/$$ARCHIVE" | awk '{print $$1}'); test "$$ACTUAL" = "$$EXPECTED" && tar -xz -C "$$TMPDIR" -f "$$TMPDIR/$$ARCHIVE" && mv "$$TMPDIR/$(OS)-$(ARCH_SHORT)/helm" "$(HELM)" && chmod +x "$(HELM)" && rm -rf "$$TMPDIR"

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)
