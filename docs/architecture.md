# Architecture

This repository is the initial scaffold for a Linode-managed NFS CSI driver.

## Directory Structure

```text
.
├── .github/workflows/
├── .ko.yaml
├── charts/linode-nfs-csi-driver/
├── deploy/kubernetes/
├── docs/
├── internal/driver/
├── pkg/linode-client/
├── Makefile
├── README.md
├── go.mod
└── main.go
```

## Runtime Shape

- `main.go` loads environment-based configuration, initializes `klog`, creates the Linode client stub, wires the driver, and starts gRPC server on the configured CSI endpoint.
- `pkg/linode-client` is the external boundary for future Linode-managed NFS API calls. It currently validates configuration and returns explicit not-implemented errors for operations. We should create the mock provider API and implementation here as well, so the controller and node server implementations can move ahead before the production API and service are ready.
- `internal/driver` owns the CSI-facing surfaces:
  - shared driver state
  - identity, controller, and node servers
  - capability wiring
  - gRPC server startup, lightweight unary logging, and Unix socket handling
  - lightweight metadata and helper files

## Image Build Shape

Container images are built with `ko`, not a Dockerfile.

- `.ko.yaml` defines the default distroless base image and target platforms.
- `make ko-build` builds the root Go package into the local container runtime.
- `make ko-publish` publishes a multi-platform image to `KO_DOCKER_REPO`.

This follows the same basic direction as the Linode Karpenter provider: keep image construction centered on Go build output and a small `.ko.yaml` instead of maintaining a separate Dockerfile path. 'ko' is a good fit and also a lot more efficient than traditional Dockerfiles for multi-platform builds, so this shape should be efficient and maintainable for our needs.

## CSI Surface In This Scaffold

Identity methods are wired and return real metadata from the configured driver instance. We will need to adjust the returned capabilities as we implement controller and node behavior.

Controller and node methods are present as skeletons and include brief comments describing the future responsibility of each method. Most currently return gRPC `Unimplemented` so the structure is explicit without implying finished behavior.

Advertised capabilities are intentionally conservative:

- Plugin:
  - `CONTROLLER_SERVICE`
- Controller:
  - `CREATE_DELETE_VOLUME`
- Node:
  - `STAGE_UNSTAGE_VOLUME`
  - `GET_VOLUME_STATS`

Expansion and snapshot RPCs exist as placeholders. The controller manifests already include `csi-resizer` and `csi-snapshotter` for initial setup, but the driver does not yet advertise those capabilities, so those sidecars are preparatory rather than active feature signals. Snapshot support might not be availble in the initial release, but we can iterate on capabilities and sidecar wiring as development progresses and the production API shape becomes clearer.

## Socket And Sidecar Wiring

The default CSI endpoint is `unix:///csi/csi.sock`.

Deployment manifests and the Helm chart share that socket path across:

- the main plugin container
- `csi-provisioner`, `csi-resizer`, `csi-snapshotter`, and `livenessprobe` on the controller side
- `node-driver-registrar` and `livenessprobe` on the node side

The `CSIDriver` object is configured with:

- `attachRequired: false`
- `podInfoOnMount: false`
- `fsGroupPolicy: File`

## Mock Provider Development Plan

Early driver development will use a lightweight mock provider so controller and node server implementation can move ahead before the production managed file storage API and service are available.

The mock provider plan is documented in
[Mock Provider Development Plan](./mock-provider.md). The short version is:

- keep the mock API shaped like the planned production API boundary
- run a lightweight NFS server that exports a single root directory
- model each CSI volume as a directory under that exported root
- return mount coordinates through `VolumeContext`
- use real NFS mount and bind-mount behavior in the node server

This is development infrastructure only. It should unblock CSI behavior, idempotency, validation, and Kubernetes mount testing without becoming a production storage service.
