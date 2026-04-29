# Mock Provider Development Plan

This document describes the temporary mock provider used to build the managed NFS CSI driver before the production file storage API and backing service are available.

The goal is to unblock CSI development with real Kubernetes node-side mount behavior while keeping the backend simple enough to replace later.

## Goals

- Let the CSI controller call an API that looks like the future production API.
- Let the CSI node server perform real NFS mounts, bind mounts, unmounts, and volume stats operations.
- Keep the mock backend intentionally small and development-only.
- Avoid production-specific assumptions until the real API contract is known.

## Mock Provider Shape

The mock provider should run as Kubernetes test infrastructure and expose two surfaces:

- a small HTTP API used by the CSI controller server
- a lightweight NFS export used by the CSI node server

The first version should export one root directory from the NFS server, for example `/exports`. Each created volume is represented as a subdirectory under that root:

```text
/exports/
  volumes/
    <volume-id>/
```

The mock API owns the metadata and directory lifecycle. The NFS server only needs to make the export reachable from Kubernetes nodes.

## Mock API Responsibilities

The mock API should be built around storage concepts rather than final route names. The exact HTTP paths can change later, but the behavior should cover:

- create a volume directory and metadata record
- return a stable volume ID
- return NFS mount coordinates for the volume
- delete a volume directory or mark it deleted
- return idempotent success for repeated create and delete requests
- optionally clone a volume by copying directory contents
- optionally update capacity metadata for expansion testing

The API response used by `CreateVolume` should contain enough information for the CSI driver to populate `VolumeContext`, such as:

```json
{
  "id": "vol-123",
  "server": "10.0.0.10",
  "exportPath": "/volumes/vol-123",
  "capacityBytes": 10737418240
}
```

The mock should also return realistic errors for invalid requests, duplicate names with incompatible parameters, missing volumes, and backend-unavailable cases.

## Lightweight NFS Server Plan

Use a lightweight containerized NFS server for the first implementation. The server should export a single root path and avoid per-volume export reloads.

This keeps the test backend focused on what the CSI driver owns:

- receiving mount coordinates from the controller response
- mounting an NFS path in `NodeStageVolume`
- bind-mounting the staged path to pod target path in `NodePublishVolume`
- unmounting idempotently during unpublish and unstage
- reporting stats from the mounted filesystem

The NFS server may need elevated privileges. 

## CSI Integration Plan

The CSI controller server will call the mock API for lifecycle operations:

- `CreateVolume` creates or finds a mock volume and returns CSI volume metadata.
- `DeleteVolume` deletes or confirms deletion of the mock volume.
- `ValidateVolumeCapabilities` checks that the request is compatible with NFS filesystem semantics.
- `ControllerExpandVolume`, if enabled, updates mock capacity metadata and returns `NodeExpansionRequired=false`.

The CSI node server will use the returned `VolumeContext` from controller calls to perform real mounts:

- `NodeStageVolume` mounts `server:exportPath` to the kubelet staging target.
- `NodePublishVolume` bind-mounts the staging target to the pod target path.
- `NodeUnpublishVolume` unmounts the pod target path.
- `NodeUnstageVolume` unmounts the staging target path.
- `NodeGetVolumeStats` reports usage from the mounted path.

The driver should keep the API client behind a small interface so the mock API can be swapped for the production client without rewriting CSI RPC handlers.

## Networking Notes

The NFS endpoint must be reachable from the Kubernetes node environment where the CSI node plugin performs mounts. A regular pod IP or service DNS name may not be sufficient in every local cluster.

The initial deployment should choose one explicit development strategy:

- run the mock NFS server with `hostNetwork: true`
- expose it through a node-reachable service
- use a local-cluster-specific address that is known to work from the node
  mount namespace

This should be validated with a direct NFS mount from the CSI node plugin environment before relying on PVC tests.

## Validation Plan

Use both CSI-level and Kubernetes-level tests:

- run `csi-sanity` against the driver socket for CSI idempotency and validation behavior
- create `chainsaw` tests:
  - deploy a PVC and pod that writes, reads, restarts, and verifies data
  - test repeated create, delete, stage, publish, unpublish, and unstage calls
  - test invalid volume IDs and unreachable NFS endpoint behavior
