IMAGE_VERSION := env("IMAGE_VERSION", "dev")
IMAGE_TAGS := env("IMAGE_TAGS", IMAGE_VERSION)
IMAGE_REPO := env("IMAGE_REPO", "docker.io/linode/linode-filestorage-csi-driver")
PLATFORM := env("PLATFORM", "linux/amd64")
RELEASE_IMAGE_REPO := env("RELEASE_IMAGE_REPO", "docker.io/linode/linode-filestorage-csi-driver")
RELEASE_DIR := env("RELEASE_DIR", "release")

LINODE_REGION := env('LINODE_REGION', 'us-ord')
CLUSTER_NAME := env('CLUSTER_NAME', "nfs-csi-driver-dev")
KUBECONFIG := env('KUBECONFIG', CLUSTER_NAME + "-kubeconfig")
LINODE_CLI_API_VERSION := env('LINODE_CLI_API_VERSION', "v4beta")
LINODE_CLI_API_HOST := env('LINODE_CLI_API_HOST', "api.linode.com")
LINODE_TYPE := env('LINODE_TYPE', 'g6-standard-2')
STACK_TYPE := env('STACK_TYPE', 'ipv4-ipv6')
NODEPOOL_SIZE := env('NODEPOOL_SIZE', '3')
# TILT_MODE := env('TILT_MODE', 'ci')
# CHAINSAW_FLAGS := env('CHAINSAW_FLAGS', '--config .chainsaw.yaml')
# CHAINSAW_SELECTOR := env('CHAINSAW_SELECTOR', 'all')
CLUSTER_ID := env("CLUSTER_ID", "")
CLUSTER_TIER := env("CLUSTER_TIER", "enterprise")
CLUSTER_ACL_FLAGS := env("CLUSTER_ACL_FLAGS", '--acl.enabled true --acl.addresses.ipv4=$(curl --fail --silent --show-error https://ipv4.icanhazip.com)')
K8S_VERSION := env("K8S_VERSION", "v1.33.6+lke7")

helm_version := trim_start_match(IMAGE_VERSION, "v")

# Format the Go code
fmt:
    go fmt ./...

# Tidy Go modules
tidy:
    go mod tidy

# Run go vet
vet: fmt
    go vet ./...

# Run golangci-lint
lint:
    golangci-lint run --fix ./...

# Run unit tests
test:
    go test ./... -cover -coverprofile=coverage.out -outputdir=. -coverpkg=./...


# Run the embedded CSI sanity suite
sanity:
    go test -race -count=1 -v ./tests/sanity \
      -run '^TestCSISanity$' \
      -timeout 15m
cover:
    go tool cover -html=coverage.out

# Build the binary
build:
    go build -ldflags "-X main.vendorVersion={{ IMAGE_VERSION }}" ./...

# Build a local image. Default linux/amd64 (Akamai/LKE).
docker-build:
    docker build --platform={{ PLATFORM }} --build-arg REV={{ IMAGE_VERSION }} -t {{ IMAGE_REPO }}:{{ IMAGE_TAGS }} .

# Publish the PLATFORM image (default linux/amd64).
docker-publish:
    docker buildx build --platform={{ PLATFORM }} --build-arg REV={{ IMAGE_VERSION }} -t {{ IMAGE_REPO }}:{{ IMAGE_TAGS }} --push .

# Lint the Helm chart
helm-lint:
    helm lint charts/linode-nfs-csi-driver

# Render deploy/kubernetes/base from the Helm chart
update-kustomize:
    ./hack/update-kustomize.sh

# Fail if Helm chart and kustomize base are out of sync
verify-kustomize:
    ./hack/verify-kustomize.sh

# Package a versioned Helm chart and Kustomize manifests for release
release:
    rm -rf {{ RELEASE_DIR }}
    mkdir -p "{{ RELEASE_DIR }}/charts"
    cp -R charts/linode-nfs-csi-driver "{{ RELEASE_DIR }}/charts/"
    yq -i '.version = "{{ helm_version }}" | .appVersion = "{{ IMAGE_VERSION }}"' "{{ RELEASE_DIR }}/charts/linode-nfs-csi-driver/Chart.yaml"
    helm package "{{ RELEASE_DIR }}/charts/linode-nfs-csi-driver" \
        --destination "{{ RELEASE_DIR }}" >/dev/null
    mv "{{ RELEASE_DIR }}/linode-nfs-csi-driver-{{ helm_version }}.tgz" "{{ RELEASE_DIR }}/helm-chart-{{ IMAGE_VERSION }}.tgz"
    kustomize build deploy/kubernetes/base \
        | sed "s|{{ RELEASE_IMAGE_REPO }}:dev|{{ RELEASE_IMAGE_REPO }}:{{ IMAGE_VERSION }}|g" > "{{ RELEASE_DIR }}/linode-filestorage-csi-driver-{{ IMAGE_VERSION }}.yaml"

gen-mock:
    go run go.uber.org/mock/mockgen@v0.6.0 -source=pkg/linode-client/client.go -destination=mocks/mock_linodeclient.go -package=mocks
    go run go.uber.org/mock/mockgen@v0.6.0 -source=pkg/mount-manager/safe_mounter.go -destination=mocks/mock_safe-mounter.go -package=mocks
    go run go.uber.org/mock/mockgen@v0.6.0 -source=pkg/filesystem/filesystem.go -destination=mocks/mock_filesystem.go -package=mocks

# Run all CI steps
ci: fmt gen-mock vet lint test build

# Install the Helm chart
helm-install:
    @if [ -z "$LINODE_TOKEN" ]; then echo "Error: LINODE_TOKEN environment variable is not set."; exit 1; fi
    helm upgrade --install --namespace kube-system --create-namespace linode-nfs-csi-driver charts/linode-nfs-csi-driver \
        --set image.repository={{ IMAGE_REPO }} \
        --set image.tag={{ IMAGE_VERSION }} \
        --set apiToken=$LINODE_TOKEN

# Create an LKE test cluster
create-lke-cluster:
    #!/usr/bin/env bash
    set -euo pipefail
    export LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }}
    export LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }}
    existing_id=$(just get-lke-cluster-id)
    if [ -n "$existing_id" ]; then
    	echo "LKE cluster '{{ CLUSTER_NAME }}' already exists (id: $existing_id); skipping create"
    	exit 0
    fi
    linode-cli lke cluster-create \
    	--label '{{ CLUSTER_NAME }}' \
    	--region '{{ LINODE_REGION }}' \
    	--k8s_version {{ K8S_VERSION }} \
    	--node_pools.type {{ LINODE_TYPE }} \
    	--node_pools.count {{ NODEPOOL_SIZE }} \
    	--tier {{ CLUSTER_TIER }} \
        --stack_type {{ STACK_TYPE }} \
    	--no-defaults

# Retrying logic to wait for LKE cluster kubeconfig to be ready
wait-for-lke-cluster-readiness cluster_id:
    #!/usr/bin/env bash
    set -euo pipefail
    export LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }}
    export LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }}
    until OUTPUT=$(linode-cli lke kubeconfig-view "{{ cluster_id }}" --text 2>&1) && ! echo "$OUTPUT" | grep -q 503; do
    	echo "Kubeconfig is not ready yet, retrying in 10s..."
    	sleep 10
    done
    echo "Kubeconfig is ready!"

# Get the kubeconfig for your LKE cluster
get-lke-kubeconfig cluster_id: (wait-for-lke-cluster-readiness cluster_id)
    #!/usr/bin/env bash
    set -euo pipefail
    export LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }}
    export LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }}
    linode-cli lke kubeconfig-view {{ cluster_id }} --text | sed '1d' | base64 -d > {{ KUBECONFIG }}
    chmod 0600 {{ KUBECONFIG }}

# Wait for the Kubernetes API to accept requests after ACL/kubeconfig changes
wait-for-lke-kube-api:
    #!/usr/bin/env bash
    set -euo pipefail
    export KUBECONFIG={{ KUBECONFIG }}
    for _ in $(seq 1 12); do
    	if kubectl get --raw=/version >/dev/null 2>&1; then
            echo "Kubernetes API is ready!"
    		exit 0
    	fi
    	echo "Kubernetes API is not reachable yet, retrying in 5s..."
    	sleep 5
    done
    echo "Timed out waiting for Kubernetes API reachability"
    exit 1

# Get the ID of your LKE development cluster
get-lke-cluster-id:
    #!/usr/bin/env bash
    set -euo pipefail
    export LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }}
    export LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }}
    linode-cli lke clusters-list --label '{{ CLUSTER_NAME }}' --format id --text | sed '1d'

init-lke-cluster:
    #!/usr/bin/env bash
    set -euo pipefail
    export LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }}
    export LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }}
    cluster_id=$(just get-lke-cluster-id)
    if [ -z "$cluster_id" ]; then
    	echo "Unable to determine LKE cluster ID for '{{ CLUSTER_NAME }}'"
    	exit 1
    fi
    linode-cli lke cluster-acl-update "$cluster_id" {{ CLUSTER_ACL_FLAGS }}
    just get-lke-kubeconfig $cluster_id
    just wait-for-lke-kube-api

# Destroy your LKE test cluster
destroy-lke-cluster cluster_id:
    #!/usr/bin/env bash
    set -euo pipefail
    export KUBECONFIG={{ KUBECONFIG }}
    export LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }}
    export LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }}
    if [ "{{ CLUSTER_TIER }}" = "standard" ] && [ -f "{{ KUBECONFIG }}" ]; then
    	if kubectl get crd/cloudfirewalls.networking.linode.com >/dev/null 2>&1; then
    		kubectl -n kube-system delete \
    			cloudfirewall.networking.linode.com/primary \
    			--ignore-not-found=true
    		kubectl -n kube-system wait \
    			--for=delete cloudfirewall.networking.linode.com/primary \
    			--timeout=5m || true
    	fi
    fi
    linode-cli lke cluster-delete '{{ cluster_id }}'
    rm -f {{ KUBECONFIG }}
