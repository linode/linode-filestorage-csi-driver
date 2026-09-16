# 📸 Snapshots and restore

A `VolumeSnapshot` maps to a Linode NFS snapshot, taken on the filesystem behind the source PVC. Restoring means cloning that snapshot into a brand new filesystem; snapshots are never restored in place.

## 📜 Table of Contents

1. [Prerequisites](#-prerequisites)
2. [Create a VolumeSnapshotClass](#1-create-a-volumesnapshotclass)
3. [Take a snapshot](#2-take-a-snapshot)
4. [Restore into a new PVC](#3-restore-into-a-new-pvc)
5. [Deleting snapshots](#-deleting-snapshots)
6. [Snapshot identity](#-snapshot-identity)
7. [Behavior details](#-behavior-details)
8. [Limitations](#-limitations)

## 🔧 Prerequisites

Two things have to be in place, and only one of them is inside this chart:

1. **The snapshot CRDs.** `volumesnapshots`, `volumesnapshotcontents`, and `volumesnapshotclasses` from `snapshot.storage.k8s.io`. Kubernetes does not ship these; install them from [external-snapshotter](https://github.com/kubernetes-csi/external-snapshotter).
2. **The `csi-snapshotter` sidecar.** Shipped in the chart but **disabled by default**:

   ```yaml
   sidecars:
     snapshotter:
       enabled: true
   ```

   ```sh
   helm upgrade --install linode-nfs-csi-driver \
     --namespace kube-system \
     --set sidecars.snapshotter.enabled=true \
     --set apiToken="${LINODE_API_TOKEN}" \
     charts/linode-nfs-csi-driver
   ```

Verify the CRDs and the sidecar:

```sh
kubectl get crd | grep snapshot.storage.k8s.io
kubectl -n kube-system get deploy csi-linode-nfs-controller \
  -o jsonpath='{.spec.template.spec.containers[*].name}'
```

The container list should now include `csi-snapshotter`. `CREATE_DELETE_SNAPSHOT` is advertised whether or not the sidecar is enabled, so a CSI sanity run will exercise the RPCs even when Kubernetes cannot.

## 1. Create a VolumeSnapshotClass

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: linode-nfs-snapshots
driver: linodenfs.csi.linode.com
deletionPolicy: Delete
```

The driver reads no snapshot-class parameters. `parameters` on this object is ignored, so tags and squash settings cannot be set per snapshot.

`deletionPolicy` is the one field that matters:

| `deletionPolicy` | On `VolumeSnapshot` deletion |
| --- | --- |
| `Delete` | `DeleteSnapshot` is called and the Linode NFS snapshot is removed |
| `Retain` | The `VolumeSnapshotContent` and the Linode snapshot both survive |

## 2. Take a snapshot

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: nfs-pvc-snap-1
spec:
  volumeSnapshotClassName: linode-nfs-snapshots
  source:
    persistentVolumeClaimName: nfs-pvc
```

```sh
kubectl apply -f snapshot.yaml
kubectl get volumesnapshot nfs-pvc-snap-1
```

```text
NAME             READYTOUSE   SOURCEPVC   RESTORESIZE   SNAPSHOTCLASS          AGE
nfs-pvc-snap-1   true         nfs-pvc     4194304       linode-nfs-snapshots   1m
```

`READYTOUSE` goes `true` when the Linode snapshot reaches `active`. `RESTORESIZE` is the snapshot's real reported size in bytes, which is one of the few genuinely accurate size numbers this driver produces (unlike the PVC capacity, which is advisory).

The snapshot is not quiesced. Nothing pauses the writers, so it is a crash-consistent point-in-time copy of the export. If your workload needs application consistency, flush or freeze it yourself before creating the object.

## 3. Restore into a new PVC

Restoring creates a **new filesystem** cloned from the snapshot. The original PVC is untouched.

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: nfs-pvc-restored
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: linode-nfs
  dataSource:
    name: nfs-pvc-snap-1
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  resources:
    requests:
      storage: 10Gi
```

Two things to know about where the clone lands:

- **The Storage Space comes from the restore PVC's `StorageClass`, not from the source.** The clone is created in the space that class names, which means you can restore into a different space by pointing the class elsewhere.
- **The region is always the cluster's region.** The driver does not validate the source snapshot's region against it, so a cross-region restore is not blocked and not defined either. Restore within one region.

`tags` from the restore `StorageClass` are applied to the clone. `filesystem-root-squash` is applied after the clone goes active, the same as for a fresh volume.

## 🗑 Deleting snapshots

```sh
kubectl delete volumesnapshot nfs-pvc-snap-1
```

With `deletionPolicy: Delete`, this calls `DeleteSnapshot`, which is idempotent: a snapshot already gone returns success rather than an error, so a retried delete does not wedge the finalizer.

Two ordering points:

- Kubernetes protects an in-use snapshot. A `VolumeSnapshot` that is the `dataSource` of a PVC still being provisioned will not finish deleting until that finishes.
- Deleting the **source PVC** while snapshots of it exist is not blocked by the driver. `DeleteVolume` deletes the filesystem, and what happens to the snapshots underneath it is decided by the Linode backend, not by this driver. If the snapshots matter, delete them first or use `Retain` on the source PV.

## 🔗 Snapshot identity

A CSI snapshot handle is three segments:

```text
{space_id}/{filesystem_id}/{snapshot_id}
```

For example `42/1337/91`: snapshot 91, on filesystem 1337, in space 42. Compare with a volume handle, which is the first two segments only (`42/1337`).

```sh
kubectl get volumesnapshotcontent -o custom-columns=\
NAME:.metadata.name,\
HANDLE:.status.snapshotHandle,\
SOURCE:.spec.source.volumeHandle,\
READY:.status.readyToUse
```

A snapshot handle is parseable on its own, which is what makes `DeleteSnapshot` and `ListSnapshots` by ID work without a lookup table.

### Pre-provisioned snapshots

Because the handle is self-describing, you can adopt an existing Linode snapshot:

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotContent
metadata:
  name: adopted-snap
spec:
  deletionPolicy: Retain
  driver: linodenfs.csi.linode.com
  source:
    snapshotHandle: "42/1337/91"
  volumeSnapshotRef:
    name: adopted-snap
    namespace: default
---
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: adopted-snap
  namespace: default
spec:
  source:
    volumeSnapshotContentName: adopted-snap
```

Use `deletionPolicy: Retain` for anything you adopted, so deleting the Kubernetes object does not delete a snapshot Kubernetes never created.

## ⚙ Behavior details

### Snapshot labels and idempotency

The snapshot label is the CSI snapshot name (which `csi-snapshotter` derives from the `VolumeSnapshotContent` name), lowercased and truncated to 63 bytes.

`CreateSnapshot` must be idempotent, and the driver gets there in two passes:

1. Before creating, it lists the source filesystem's snapshots and returns the existing one if a label matches.
2. If creation still comes back `409 Conflict`, it re-lists and returns the match. Only if that second lookup finds nothing does the conflict surface as an error.

The label is the identity key, so two snapshots of the same filesystem cannot share a name.

`CreateSnapshot` takes the volume lock on the source volume ID. A snapshot in flight blocks `NodeStageVolume` and `NodeUnstageVolume` for that volume with `Aborted`, and the sidecars retry. Snapshots of different volumes proceed in parallel.

### Waiting

After creating, the driver waits up to 5 minutes for the snapshot to reach `active` before responding. On timeout it returns `DeadlineExceeded` and `csi-snapshotter` retries, at which point the idempotency lookup finds the snapshot that is still settling and returns it with `ReadyToUse: false`. The snapshot controller then polls until it is ready.

### ListSnapshots

`ListSnapshots` requires a filter. An unfiltered call returns `InvalidArgument: snapshot id or source volume id is required`, because satisfying it would mean an account-wide traversal of every space and filesystem.

| Request | Behavior |
| --- | --- |
| `snapshot_id` set | Parsed, fetched directly. Also filtered by `source_volume_id` when both are given |
| `snapshot_id` for a missing snapshot | Empty response, not an error, as the spec requires |
| `source_volume_id` only | Lists that filesystem's snapshots, paginated in memory |
| Neither | `InvalidArgument` |
| `max_entries` negative | `InvalidArgument` |
| Invalid or out-of-range `starting_token` | `Aborted`, which tells the caller to restart the listing |

The `starting_token` is an absolute offset rather than a backend page cursor, so a caller that changes `max_entries` between calls still gets a coherent sequence. In practice this path rarely runs: `external-snapshotter` only ever calls `ListSnapshots` by `snapshot_id`.

## 🚫 Limitations

| Limitation | Detail |
| --- | --- |
| **No group snapshots** | `GET_VOLUME_GROUP_SNAPSHOT` is not advertised; `VolumeGroupSnapshot` will not work |
| **No in-place restore** | Restoring always produces a new filesystem and a new PV. To swap it in, repoint your workload at the restored PVC |
| **No snapshot-class parameters** | Tags and squash policy on a restored volume come from the restore `StorageClass` |

## 📚 Related pages

- [Usage](./usage.md)
- [StorageClass parameters](./storage-class-parameters.md)
- [Troubleshooting](./troubleshooting.md)
