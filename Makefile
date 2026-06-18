IMAGE_VERSION ?= dev
IMAGE_TAGS ?= $(IMAGE_VERSION)
KO_DOCKER_REPO ?= docker.io/linode/linode-filestorage-csi-driver
RELEASE_IMAGE_REPO ?= docker.io/linode/linode-filestorage-csi-driver
RELEASE_DIR ?= release
LINODE_TOKEN ?= $(LINODE_TOKEN) # Required for helm-install target, but not used in other targets, so we don't want to error out if it's not set

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: vet
vet: fmt
	go vet ./...

.PHONY: lint
lint:
	golangci-lint run --fix ./...

.PHONY: test
test:
	go test ./...

.PHONY: gen-mock
gen-mock:
	go run go.uber.org/mock/mockgen@v0.6.0 -source=pkg/linode-client/client.go -destination=mocks/mock_linodeclient.go -package=mocks
	go run go.uber.org/mock/mockgen@v0.6.0 -source=pkg/mount-manager/safe_mounter.go -destination=mocks/mock_safe-mounter.go -package=mocks

.PHONY: build
build:
	go build -ldflags "-X main.vendorVersion=$(IMAGE_VERSION)" ./...

.PHONY: ko-build
ko-build:
	ko build --local --bare --tags $(IMAGE_TAGS) .

.PHONY: ko-publish
ko-publish:
	KO_DOCKER_REPO=$(KO_DOCKER_REPO) ko build --bare --tags $(IMAGE_TAGS) .

.PHONY: helm-lint
helm-lint:
	helm lint charts/linode-nfs-csi-driver

.PHONY: release
release:
	rm -rf $(RELEASE_DIR)
	mkdir -p $(RELEASE_DIR)
	helm package charts/linode-nfs-csi-driver \
		--version "$(patsubst v%,%,$(IMAGE_VERSION))" \
		--app-version "$(IMAGE_VERSION)" \
		--destination "$(RELEASE_DIR)" >/dev/null
	mv "$(RELEASE_DIR)/linode-nfs-csi-driver-$(patsubst v%,%,$(IMAGE_VERSION)).tgz" "$(RELEASE_DIR)/helm-chart-$(IMAGE_VERSION).tgz"
	kustomize build deploy/kubernetes/base \
		| sed "s|$(RELEASE_IMAGE_REPO):dev|$(RELEASE_IMAGE_REPO):$(IMAGE_VERSION)|g" > "$(RELEASE_DIR)/linode-filestorage-csi-driver-$(IMAGE_VERSION).yaml"

.PHONY: ci
ci: fmt vet lint gen-mock test build

.PHONY: helm-install
helm-install:
	helm upgrade --install --namespace kube-system --create-namespace linode-nfs-csi-driver charts/linode-nfs-csi-driver \
		--set controller.image.repository=$(KO_DOCKER_REPO) \
		--set apiToken=${LINODE_TOKEN}
