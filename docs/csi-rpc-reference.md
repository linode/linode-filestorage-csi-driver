# 🔌 CSI RPC Reference

What the driver advertises, what each RPC actually does, and the exact errors it returns. Useful when reading sidecar logs, writing a CSI sanity test, or deciding whether a failure is yours or the driver's.

## 📜 Table of Contents

1. [Capabilities](#-capabilities)
2. [Identity service](#-identity-service)
3. [Controller service](#-controller-service)
4. [Node service](#-node-service)
5. [ListVolumes is intentionally unimplemented](#listvolumes-is-intentionally-unimplemented)
6. [Error code mapping](#-error-code-mapping)
7. [Concurrency and locking](#-concurrency-and-locking)

## 🎯 Capabilities

### Plugin capabilities

| Capability | Advertised | Role |
| --- | --- | --- |
| `CONTROLLER_SERVICE` | ✅ | controller only |
| `VOLUME_ACCESSIBILITY_CONSTRAINTS` | ❌ | The driver publishes no accessible topology |
| `VolumeExpansion` | ❌ | Neither `ONLINE` nor `OFFLINE` |

The node role advertises **no** plugin capabilities, which is correct: `CONTROLLER_SERVICE` is a property of the controller binary, and the same image serves both roles based on `DRIVER_ROLE`.

### Controller service capabilities

| Capability | Advertised |
| --- | --- |
| `CREATE_DELETE_VOLUME` | ✅ |
| `PUBLISH_UNPUBLISH_VOLUME` | ✅ |
| `GET_VOLUME` | ✅ |
| `CREATE_DELETE_SNAPSHOT` | ✅ |
| `LIST_VOLUMES` | ❌ intentionally, [see below](#listvolumes-is-intentionally-unimplemented) |
| `LIST_SNAPSHOTS` | ❌ although the RPC is implemented |
| `EXPAND_VOLUME` | ❌ |
| `CLONE_VOLUME` | ❌ |
| `GET_CAPACITY` | ❌ |
| `PUBLISH_READONLY` | ❌ |
| `VOLUME_CONDITION` | ❌ |

`LIST_SNAPSHOTS` is the interesting gap: `ListSnapshots` is implemented and works, but the capability is not advertised. Advertising it would promise the unfiltered account-wide listing the spec expects, which the driver deliberately refuses. Callers that reach the RPC directly, including `external-snapshotter`'s by-ID lookups, get a working implementation anyway.

### Node service capabilities

| Capability | Advertised |
| --- | --- |
| `STAGE_UNSTAGE_VOLUME` | ✅ |
| `GET_VOLUME_STATS` | ✅ |
| `EXPAND_VOLUME` | ❌ |
| `VOLUME_CONDITION` | ❌ |
| `SINGLE_NODE_MULTI_WRITER` | ❌ |

## 🪪 Identity service

| RPC | Behavior |
| --- | --- |
| `GetPluginInfo` | Returns `linodenfs.csi.linode.com` and the build's vendor version (`dev` for an unstamped build). Returns `Unavailable: driver name not configured` if called before setup |
| `GetPluginCapabilities` | Returns the role's plugin capabilities |
| `Probe` | Returns `Ready: true` once `Run` has been entered, otherwise `false`. Never returns an error, so `livenessprobe` restarts the container only on a socket that has stopped answering |

`Probe` reflects process readiness, not Linode API reachability. A controller with a revoked token still probes healthy while failing every provisioning call.

## 🎛 Controller service

### CreateVolume

Creates an NFS filesystem inside a pre-created Storage Space, or restores a snapshot clone into one.

**Validation, in order:**

| Condition | Error |
| --- | --- |
| Empty name | `InvalidArgument: volume name is required` |
| No volume capabilities | `InvalidArgument: volume capabilities are required` |
| A block capability | `InvalidArgument: only mount volume capabilities are supported` |
| No access mode set | `InvalidArgument: volume access mode is required` |
| `volume_content_source` of a kind other than snapshot | `InvalidArgument: unsupported volume content source` |
| Bad `space-id`, both space params, or neither | `InvalidArgument`, see [StorageClass parameters](./storage-class-parameters.md) |
| Bad `filesystem-root-squash` | `InvalidArgument: unsupported ... value` |
| No cluster VPC | `FailedPrecondition: this driver requires VPC-backed IPv6 connectivity; cluster VPC not found` |
| Mixed-region cluster | `FailedPrecondition: resolve cluster metadata: cluster has multiple regions: ...` |
| Space not found | `NotFound` |
| Existing filesystem with different tags or squash | `AlreadyExists` |
| Filesystem never reaches `active` within 5 minutes | `DeadlineExceeded` |
| Filesystem active but no mount target | `FailedPrecondition: NFS filesystem %d does not have a mount target` |

**Response:** `volume_id` of `{space_id}/{filesystem_id}`, `capacity_bytes` echoing the request, and a five-key volume context. No `accessible_topology`.

Full step-by-step in [Architecture](./architecture.md#-provisioning-lifecycle).

### DeleteVolume

| Condition | Result |
| --- | --- |
| Empty `volume_id` | `InvalidArgument: volume ID is not set` |
| Malformed non-empty handle | **Success.** A handle the driver cannot parse is a volume it does not know about, and the spec requires deleting an unknown volume to succeed |
| Filesystem already gone (`404`) | Success |
| Any other API error | Mapped, [see below](#-error-code-mapping) |

There is no dependency check. Deleting a volume with snapshots, or one still in some node's ACL, is not blocked by the driver.

### ControllerPublishVolume

Adds the node's Linode to the filesystem's Linode ACL and enables the policy. No block device, no attachment; publish means authorize.

| Condition | Result |
| --- | --- |
| Empty `volume_id` or `node_id` | `InvalidArgument` |
| Unsupported volume capability | `InvalidArgument` |
| Linode already in the ACL and policy enabled | Success, no API write |
| Otherwise | Append the Linode, set `enabled: true`, wait for the policy to go active |
| Policy not active within 5 minutes | `DeadlineExceeded` |

Returns an empty `publish_context`; the node plugin gets everything it needs from the volume context instead.

### ControllerUnpublishVolume

| Condition | Result |
| --- | --- |
| Empty `volume_id` | `InvalidArgument` |
| Empty `node_id` | Success (nothing to revoke) |
| Filesystem gone (`404`) | Success, so detach never blocks PV cleanup |
| Linode not in the ACL | Success, no API write |
| Otherwise | Remove the Linode, preserve `enabled`, wait for active |

### ValidateVolumeCapabilities

| Condition | Result |
| --- | --- |
| Empty `volume_id` | `InvalidArgument` |
| Unparseable handle | `NotFound: volume not found` |
| Empty capability list | `InvalidArgument: volume capabilities are required` |
| Any unsupported capability | Success with a `message` and **no** `confirmed` block, per the spec |
| All supported | `confirmed` with the volume context and the echoed capabilities |

The confirmed context omits `mtls-mode`, since this RPC does not read the space policy.

### ControllerGetVolume

| Condition | Result |
| --- | --- |
| Empty `volume_id` | `InvalidArgument` |
| Filesystem gone | `NotFound` |
| Success | The volume with `capacity_bytes: 0` and no `mtls-mode`, plus a status listing the Linode IDs currently in the ACL as `published_node_ids` |

`capacity_bytes` is zero because the driver has no size to report; the requested capacity lives only in the PV.

### CreateSnapshot, DeleteSnapshot, ListSnapshots

See [Snapshots](./snapshots.md#-behavior-details) for the detail. In brief:

| RPC | Notes |
| --- | --- |
| `CreateSnapshot` | Idempotent by label, with a second lookup on `409 Conflict`. Waits for `active`. Takes the volume lock on the source volume |
| `DeleteSnapshot` | Idempotent; a missing snapshot returns success |
| `ListSnapshots` | Requires `snapshot_id` or `source_volume_id`. Unfiltered calls return `InvalidArgument`. Offset-based `starting_token`; bad or out-of-range tokens return `Aborted` |

### Unimplemented controller RPCs

| RPC | Result | Why |
| --- | --- | --- |
| `ControllerExpandVolume` | `Unimplemented` | Backend quota semantics for resize are not defined yet |
| `GetCapacity` | `Unimplemented` | The NFS API exposes no capacity figure to report |
| `ListVolumes` | `Unimplemented` | Deliberate, [see below](#listvolumes-is-intentionally-unimplemented) |

## 🖥 Node service

### NodeGetInfo

Returns the node's **Linode ID as a string**, which is the `node_id` the controller later receives on publish. Resolution tries the Linode instance metadata service first, then falls back to a Kubernetes node lookup by `NODE_NAME`.

| Condition | Error |
| --- | --- |
| Metadata service unconfigured for the node role | `Internal: metadata service is not configured` |
| Resolution fails through both paths | `Internal: resolve node metadata: ...` |

No `accessible_topology` and no `max_volumes_per_node`, so there is no driver-imposed cap on volumes per node.

### NodeStageVolume

Mounts the export once per node at the kubelet's staging path.

| Condition | Result |
| --- | --- |
| Missing `volume_id`, `staging_target_path`, or capability | `InvalidArgument` |
| Non-mount capability | `InvalidArgument: no mount volume capability set` |
| Missing `mount-target` in the volume context | `InvalidArgument: mount-target is required in volume context` |
| `mtls-mode` set to something invalid | `InvalidArgument: invalid mtls-mode value, must be one of: required, optional, disabled` |
| Another operation holds the volume lock | `Aborted` |
| Path already a mount point | Success, no remount |
| The staging path cannot be created | `Internal: Failed to create directory ...` |
| The path cannot be inspected for any other reason | `Internal: Unknown error when checking mount point ...` |
| Mount fails | `Internal: NodeStageVolume failed to stage volume ...` |

Mounts as `nfs4` with the capability's mount flags, plus `xprtsec=mtls` per the [mTLS mode](./access-and-networking.md#-mtls). The staging path is created if missing.

### NodePublishVolume

Bind-mounts the staged path into the pod's target path.

| Condition | Result |
| --- | --- |
| Missing `volume_id`, `staging_target_path`, `target_path`, or capability | `InvalidArgument` |
| Another operation holds the **target path** lock | `Aborted` |
| Target already a mount point | Success, no remount |
| Mount fails | `Internal`, and the driver cleans up the staging mount point before returning |

Flags are the capability's mount flags plus `bind`, plus `ro` when the request is read-only.

### NodeUnstageVolume and NodeUnpublishVolume

Both call the standard `CleanupMountPoint`, which unmounts if mounted and removes the directory, and treats an already-clean path as success. `NodeUnstageVolume` locks the volume ID; `NodeUnpublishVolume` locks the target path. Missing `volume_id`, `staging_target_path`, or `target_path` is `InvalidArgument`; a failed unmount is `Internal`.

### NodeGetVolumeStats

Runs `statfs(2)` on `volume_path` and reports two `VolumeUsage` entries, bytes and inodes, each with available, total, and used.

| Condition | Result |
| --- | --- |
| Missing `volume_id` or `volume_path` | `InvalidArgument` |
| Path does not exist (`ENOENT`) | `NotFound: volume path not found` |
| Any other `statfs` error, including `EIO` from a broken mount | `Internal: failed to get stats` |

No `volume_condition` is reported, since `VOLUME_CONDITION` is not advertised.

### NodeExpandVolume

`Unimplemented`. Expansion, if it ever lands, is expected to stay controller-only for quota-backed NFS.

## ListVolumes is intentionally unimplemented

`ListVolumes` returns `Unimplemented` and `LIST_VOLUMES` is not advertised. This is a decision, not a gap.

**The spec allows it.** `LIST_VOLUMES` is optional even for a plugin that supports `CREATE_DELETE_VOLUME`. Nothing in the CSI contract requires the two together.

**Kubernetes does not need it.** Provisioning, attaching, mounting, and deleting never call `ListVolumes`. Only the volume-health monitoring controller uses it, and only when a driver opts into that, which this one does not.

**A correct implementation is not possible with the current API.** The Linode NFS API lists filesystems per space. There is no account-wide query and no ownership or cluster-scope marker on a filesystem. Implementing `ListVolumes` would mean:

- enumerating every Storage Space on the account, then every filesystem in each, and
- returning filesystems belonging to other clusters, or to no cluster at all, since nothing distinguishes them.

That is an expensive account-wide scan whose results are ambiguous. Worse, a partial answer is dangerous: a caller that trusts the list to be authoritative would draw wrong conclusions about what exists.

**Recovery does not depend on it.** The usual argument for `ListVolumes` is reconciling orphans after a failure. Here, `CreateVolume` idempotency covers that: the label-and-region lookup finds an existing filesystem and returns it rather than creating a duplicate, so a crash mid-provisioning heals on retry without any listing.

**What to use instead:**

- `ControllerGetVolume` for a single volume, when you have its handle.
- The [tags parameter](./storage-class-parameters.md#tags) to mark filesystems belonging to a cluster, then list that space's filesystems through the API or the Cloud Manager.
- `kubectl get pv` for what Kubernetes believes exists, which is the answer you usually actually want.

The decision is recorded in the code alongside the RPC and in commit `f9c65fb`.

## 🚨 Error code mapping

Linode API HTTP statuses map to gRPC codes uniformly, so the sidecars can decide what to retry:

| HTTP | gRPC | Sidecar behavior |
| --- | --- | --- |
| 404 | `NotFound` | Not retried for deletes; treated as success by the idempotent paths first |
| 400, 422 | `InvalidArgument` | Not retried. Fix the request |
| 401, 403 | `PermissionDenied` | Not retried. Fix the token |
| 409 | `FailedPrecondition` | Retried with backoff |
| 429, 502, 503, 504 | `Unavailable` | Retried with backoff |
| anything else | `Internal` | Retried with backoff |

Waits have their own mapping, applied before the table above:

| Wait outcome | gRPC |
| --- | --- |
| Context canceled | `Canceled` |
| 5-minute deadline hit | `DeadlineExceeded` |
| An API error during the wait | Mapped by the table above |

A `DeadlineExceeded` from a wait is not a failure of the operation; the backend resource is usually still settling. The sidecar retries, and the retry finds the resource through the idempotency path.

## 🔐 Concurrency and locking

Both servers hold an in-process lock set. A contended key returns `Aborted: An operation with the given volume key <key> already exists.`, which every CSI sidecar knows to retry.

| RPC | Lock key |
| --- | --- |
| `CreateSnapshot` | volume ID |
| `NodeStageVolume` | volume ID |
| `NodeUnstageVolume` | volume ID |
| `NodePublishVolume` | **target path** |
| `NodeUnpublishVolume` | **target path** |

`CreateVolume`, `DeleteVolume`, and the controller publish RPCs take no lock; their idempotency comes from the backend lookups instead.

Publish and unpublish lock the target path rather than the volume ID on purpose. Many pods on one node share a volume, and locking the volume would serialize their startup behind each other for no reason. Locking the path keeps each pod's mount independent while still preventing a publish and an unpublish from racing on the same path.

The locks are per-process, so multiple controller replicas do not coordinate through them. Leader election among the sidecars is what makes a multi-replica controller safe; see the [replicaCount note](./configuration-reference.md#controller).

## 📚 Related pages

- [Architecture](./architecture.md)
- [Snapshots](./snapshots.md)
- [Configuration Reference](./configuration-reference.md)
- [Troubleshooting](./troubleshooting.md)
