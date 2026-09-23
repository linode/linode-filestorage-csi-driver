# 👩‍💻 Development Setup

## 📜 Table of Contents

1. [Prerequisites](#-prerequisites)
2. [Setting up the local development environment](#-setting-up-the-local-development-environment)
3. [Task reference](#-task-reference)
4. [Building](#-building)
5. [Creating a development cluster](#-creating-a-development-cluster)
6. [Deploying your build](#-deploying-your-build)
7. [The Helm chart is the source of truth](#-the-helm-chart-is-the-source-of-truth)
8. [Code layout](#-code-layout)
9. [Working on the docs](#-working-on-the-docs)
10. [Releasing](#-releasing)

## 🔧 Prerequisites

| Tool | Why |
| --- | --- |
| [mise](https://mise.jdx.dev/) | Pins and installs every other tool. The only thing you install by hand |
| Docker (or another builder) | Building and pushing the driver image |
| A Linode account and API token | Anything beyond unit tests |
| `git` | |

Everything else comes from the `[tools]` block in `mise.toml`: Go itself, plus `kubectl`, `helm`, `golangci-lint`, `kustomize`, `just`, `yq`, `uv`, and `linode-cli`. That block is the source of truth for versions, so read it there rather than trusting a copy here; `mise ls` shows what you actually have installed.

`minimum_release_age = "7d"` in the settings block means mise refuses tool versions published less than a week ago, which keeps a freshly-cut upstream release from landing in the toolchain before anyone has looked at it.

## 🧰 Setting up the local development environment

```sh
git clone https://github.com/linode/linode-filestorage-csi-driver.git
cd linode-filestorage-csi-driver

# Install every pinned tool
mise install

# Confirm
mise ls
go version
```

If tools are not on your `PATH` afterwards, mise's shell activation is not set up. Either add it to your shell profile per the [mise docs](https://mise.jdx.dev/getting-started.html), or prefix commands with `mise exec --`.

Then run the full check suite once, to confirm a clean baseline before you change anything:

```sh
mise run ci
```

That runs formatting, mock generation, `go vet`, lint, tests, and a build. It is exactly what CI runs, so a green local `ci` means a green pipeline.

## 📋 Task reference

There are two entry points for every task, and they are the same task. `mise.toml` declares the tasks and each one shells out to a `just` recipe. Use `mise run <task>` for consistency with CI, or call `just` directly if you prefer.

| `mise run` | `just` | What it does |
| --- | --- | --- |
| `fmt` | `fmt` | `go fmt ./...` |
| `tidy` | `tidy` | `go mod tidy` |
| `vet` | `vet` | `go fmt` then `go vet ./...` |
| `lint` | `lint` | `golangci-lint run --fix ./...` |
| `test` | `test` | Unit tests with coverage into `coverage.out` |
| `cover` | `cover` | Opens the HTML coverage report |
| `gen-mock` | `gen-mock` | Regenerates the three mock packages |
| `build` | `build` | `go build` with the version stamped in |
| `image-build` | `docker-build` | Builds the container image |
| `image-push` | `docker-publish` | `docker buildx build --push` |
| `helm-lint` | `helm-lint` | `helm lint charts/linode-nfs-csi-driver` |
| `update-kustomize` | `update-kustomize` | Renders `deploy/kubernetes/base` from the chart |
| `verify-kustomize` | `verify-kustomize` | Fails if the rendered base is stale |
| `helm-install` | `helm-install` | Installs the chart into the current cluster |
| `serve-docs` | `serve-docs` | Serves this documentation site locally with live reload |
| `build-docs` | `build-docs` | Builds the documentation site the way GitHub Pages does |
| `release` | `release` | Builds the release artifacts |
| `ci` | `ci` | `fmt`, `gen-mock`, `vet`, `lint`, `test`, `build` |

The cluster recipes are `just`-only, since they take arguments:

| Recipe | What it does |
| --- | --- |
| `just create-lke-cluster` | Creates the LKE dev cluster, or no-ops if it exists |
| `just get-lke-cluster-id` | Prints the cluster ID for `CLUSTER_NAME` |
| `just init-lke-cluster` | Sets the API ACL to your public IP, writes the kubeconfig, waits for the API |
| `just get-lke-kubeconfig <id>` | Writes the kubeconfig for a cluster ID |
| `just wait-for-lke-cluster-readiness <id>` | Polls until the kubeconfig is available |
| `just wait-for-lke-kube-api` | Polls until the API answers |
| `just destroy-lke-cluster <id>` | Deletes the cluster and removes the kubeconfig |

`just --list` prints everything with its doc comment.

## 🏗 Building

### The binary

```sh
mise run build
```

Stamps `main.vendorVersion` from `IMAGE_VERSION`, which defaults to `dev`. That string is what `GetPluginInfo` reports, so a build with no version reports `dev`.

### The image

```sh
export IMAGE_REPO=docker.io/yourname/linode-filestorage-csi-driver
export IMAGE_VERSION=my-feature

mise run image-build
mise run image-push
```

The build is two-stage: a `golang-alpine` builder producing a `CGO_ENABLED=0` static binary with `-trimpath -w -s`, then an `alpine` runtime carrying `ca-certificates` and `nfs-utils`. Both base images are pinned in the `Dockerfile`. `nfs-utils` is the reason the image is not `scratch`: the node plugin shells out to `mount.nfs4`.

`PLATFORM` defaults to `linux/amd64`, which is what Akamai and LKE run. Override it if you need something else, but note that the published images are amd64-only.

| Variable | Default |
| --- | --- |
| `IMAGE_REPO` | `docker.io/linode/linode-filestorage-csi-driver` |
| `IMAGE_VERSION` | `dev` |
| `IMAGE_TAGS` | `$IMAGE_VERSION` |
| `PLATFORM` | `linux/amd64` |

## ⛅ Creating a development cluster

The driver cannot be meaningfully exercised locally: it needs real Linode NFS, a real VPC, and real nodes. The justfile automates an LKE cluster for that.

```sh
export LINODE_TOKEN="...your token..."

just create-lke-cluster
just init-lke-cluster

export KUBECONFIG="$(pwd)/nfs-csi-driver-dev-kubeconfig"
kubectl get nodes
```

`init-lke-cluster` does three things worth knowing about: it sets the cluster's API ACL to your current public IP (fetched from `ipv4.icanhazip.com`), writes the kubeconfig to `${CLUSTER_NAME}-kubeconfig` with mode `0600`, and polls the API until it answers. Re-run it whenever your public IP changes, or the API will start refusing you.

Defaults, all overridable by environment variable:

| Variable | Default | Note |
| --- | --- | --- |
| `CLUSTER_NAME` | `nfs-csi-driver-dev` | Also the kubeconfig filename prefix |
| `LINODE_REGION` | `us-ord` | Must be a region with managed NFS |
| `K8S_VERSION` | `v1.33.6+lke7` | |
| `LINODE_TYPE` | `g6-standard-2` | |
| `NODEPOOL_SIZE` | `3` | |
| `CLUSTER_TIER` | `enterprise` | |
| `STACK_TYPE` | `ipv4-ipv6` | Dual stack; the driver needs VPC-backed IPv6 |
| `LINODE_CLI_API_VERSION` | `v4beta` | |
| `CLUSTER_ACL_FLAGS` | ACL enabled, your IPv4 | |

Then create a Storage Space for the cluster, since the driver never does. See [Create an NFS Storage Space](./installation.md#-create-an-nfs-storage-space).

Tear down when you are done, because this is not free:

```sh
just destroy-lke-cluster "$(just get-lke-cluster-id)"
```

## 🚀 Deploying your build

```sh
export LINODE_TOKEN="...your token..."
export IMAGE_REPO=docker.io/yourname/linode-filestorage-csi-driver
export IMAGE_VERSION=my-feature

mise run image-push
mise run helm-install
```

`helm-install` refuses to run without `LINODE_TOKEN` and otherwise runs `helm upgrade --install` into `kube-system` with `image.repository` and `image.tag` set from those variables.

Iterating on a change:

```sh
mise run image-push
kubectl -n kube-system rollout restart deploy/csi-linode-nfs-controller
kubectl -n kube-system rollout restart ds/csi-linode-nfs-node
```

Use a unique tag per build. With `imagePullPolicy: IfNotPresent`, pushing a new image under an existing tag leaves nodes running the old one, and you will spend a while debugging code that is not deployed.

Restarting the node DaemonSet does not disturb existing mounts; they live in the host mount namespace.

To iterate on the controller only:

```sh
kubectl -n kube-system logs -f deploy/csi-linode-nfs-controller -c plugin
```

## 🔗 The Helm chart is the source of truth

`charts/linode-nfs-csi-driver` is authoritative. `deploy/kubernetes/base` is **generated** from it by `hack/update-kustomize.sh`, and the `Helm` workflow runs `hack/verify-kustomize.sh` on any PR touching `charts/`, `deploy/kubernetes/`, the hack scripts, the justfile, or `mise.toml`. It fails when the two have drifted.

So after any chart change:

```sh
mise run update-kustomize
git add deploy/kubernetes/base
```

Never hand-edit `deploy/kubernetes/base`. Your edit will be silently reverted the next time someone regenerates it, and `verify-kustomize` will fail in the meantime.

`deploy/kubernetes/overlays/ci` and `deploy/kubernetes/overlays/dev` both currently just include the base; they exist as attachment points.

## 📁 Code layout

```text
main.go                    Entry point: reads the environment, picks a role, starts gRPC
internal/driver/
  driver.go                LinodeDriver, role constants, the driver name
  capabilities.go          Every advertised capability. Start here to change what is supported
  identityserver.go        GetPluginInfo, GetPluginCapabilities, Probe
  controllerserver.go      Controller RPCs
  controller_helpers.go    Parameter parsing, handles, labels, idempotency, error mapping
  nodeserver.go            Node RPCs
  nodeserver_helpers.go    Mount logic
  metadata.go              Node and cluster metadata, VPC resolution
  server.go                Non-blocking gRPC server
  errors.go                Shared gRPC errors
pkg/
  linode-client/           Linode API client interface and construction
  mount-manager/           Mounter abstraction
  filesystem/              Filesystem abstraction, for testability
  util/                    Volume locks, timestamps
mocks/                     Generated. Do not edit by hand
charts/                    The Helm chart, source of truth
deploy/kubernetes/         Generated Kustomize manifests
hack/                      update-kustomize.sh, verify-kustomize.sh
```

Two orientation notes:

- **The same binary is both plugins.** `DRIVER_ROLE` decides which server gets constructed. The controller gets a Linode client and no mounter; the node gets a mounter and no client. That is why the node plugin cannot call the API even in principle.
- **The three `pkg/` abstractions exist for tests.** `mocks/` is generated from their interfaces by `mise run gen-mock`, which `mise run ci` runs first, so a changed interface with stale mocks fails at the vet step rather than mysteriously later.

## 📝 Working on the docs

This site is Jekyll with the [just-the-docs](https://just-the-docs.com/) remote theme. Every page is a plain Markdown file in `docs/` with no front matter at all. Sidebar order comes from `nav_order`, which lives in the `defaults` block of `_config.yml`, one entry per page, so adding a page means adding an entry there and renumbering the ones after it.

The reason `nav_order` is not in the files is that GitHub's Markdown viewer renders a front matter block as a table at the top of the page. Jekyll treats a `defaults` value exactly as if it were in the file, so the nav is unaffected and the files stay clean in the repository. The page title comes from the first heading, via the `jekyll-titles-from-headings` plugin.

GitHub Pages builds from the `gh-pages` branch, not from `main`. The [Auto Update GH-Pages workflow](https://github.com/linode/linode-filestorage-csi-driver/blob/main/.github/workflows/autoupdate-gh-pages.yml) merges `main` into `gh-pages` on every push to `main`, so a merged docs change publishes itself. `gh-pages` is also where chart-releaser writes the Helm repository index, which is why the two share a branch. If a docs change is merged and the site does not update, check that workflow's run before looking at anything else.

Preview your changes the way Pages will build them:

```sh
mise run serve-docs   # http://localhost:4000, with live reload
mise run build-docs   # one-shot build into _site/
```

Both run the `jekyll/jekyll:pages` image against the repository root, so no local Ruby is needed. `_site/` is gitignored.

Two things to know before you add a heading:

- **Heading anchors are generated, and emoji affect them.** Pages strips the emoji but keeps the space it left behind, so `## 🔧 Requirements` becomes `#-requirements` with a leading hyphen. That is why in-page links look the way they do.
- **Avoid emoji that carry a variation selector (U+FE0F).** The selector survives slug generation and produces an invisible character at the front of the anchor, so `#-your-heading` silently fails to resolve. Prefer an emoji that renders in color on its own, such as `⛅` over `☁️`.

The theme overrides live in `_includes/head_custom.html` and `_includes/header_custom.html`, and site-wide settings in `_config.yml`.

## 📦 Releasing

```sh
export IMAGE_VERSION=v0.1.0
mise run release
```

That produces, in `release/`:

- `helm-chart-v0.1.0.tgz`, a packaged chart with `version` set to the tag without its leading `v` and `appVersion` set to the tag.
- `linode-filestorage-csi-driver-v0.1.0.yaml`, the Kustomize base with the `:dev` image tag rewritten to the release tag.

Both are attached to the GitHub release. Chart-releaser also publishes the versioned chart package and Helm repository index to the `gh-pages` branch, which is the same branch that serves this documentation site.

## 📚 Related pages

- [Testing](./testing.md)
- [Contributing](../.github/CONTRIBUTING.md)
- [Architecture](./architecture.md)
- [Configuration Reference](./configuration-reference.md)
