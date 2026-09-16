# 💡 Usage

## 📜 Table of Contents

1. [Create a StorageClass](#1-create-a-storageclass)
2. [Create a PersistentVolumeClaim](#2-create-a-persistentvolumeclaim)
3. [Consume it from a pod](#3-consume-it-from-a-pod)
4. [Share one volume between pods](#4-share-one-volume-between-pods)
5. [Access modes](#-access-modes)
6. [About capacity](#-about-capacity)
7. [Mount options](#-mount-options)
8. [Reclaim policy](#-reclaim-policy)
9. [Inspecting a volume](#-inspecting-a-volume)
10. [What is not supported](#-what-is-not-supported)

## 1. Create a StorageClass

**The driver does not ship a StorageClass.** There is no default class and no template in the Helm chart, because every class has to name a Storage Space that only you know about. Create one before you create a PVC.

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: linode-nfs
provisioner: linodenfs.csi.linode.com
parameters:
  # Exactly one of space-id or space-label is required.
  linodenfs.csi.linode.com/space-id: "42"
reclaimPolicy: Delete
allowVolumeExpansion: false
volumeBindingMode: Immediate
```

Or reference the space by label instead of ID:

```yaml
parameters:
  linodenfs.csi.linode.com/space-label: "my-cluster-space"
```

Referencing by label costs one extra API call per `CreateVolume` (an exact-match list) and fails with `FailedPrecondition` if two spaces share the label. Referencing by ID is the more deterministic choice.

To make it the cluster default:

```yaml
metadata:
  name: linode-nfs
  annotations:
    storageclass.kubernetes.io/is-default-class: "true"
```

See [StorageClass parameters](./storage-class-parameters.md) for tags, root squash, and the exact validation rules.

## 2. Create a PersistentVolumeClaim

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: nfs-pvc
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: linode-nfs
  resources:
    requests:
      storage: 10Gi
```

```sh
kubectl apply -f nfs-pvc.yaml
kubectl get pvc nfs-pvc
```

```text
NAME      STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   AGE
nfs-pvc   Bound    pvc-a1b2c3d4-5e6f-7890-abcd-ef1234567890   10Gi       RWX            linode-nfs     35s
```

Behind the bound PVC, the controller has created an NFS filesystem in your Storage Space labelled after the PV name (lowercased, truncated to 63 bytes), attached your cluster VPC to the space access policy, and waited for the filesystem to reach `active`.

Provisioning waits on the Linode API, so first-time creation is not instant. The driver allows up to 5 minutes per wait before returning `DeadlineExceeded` and letting `csi-provisioner` retry.

## 3. Consume it from a pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: nfs-app
spec:
  containers:
    - name: app
      image: busybox
      command: ["/bin/sh", "-c", "while true; do date >> /data/out.txt; sleep 5; done"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: nfs-pvc
```

```sh
kubectl apply -f nfs-app.yaml
kubectl exec nfs-app -- tail -n 3 /data/out.txt
```

Three things happen between scheduling and a running pod:

1. **Attach.** `csi-attacher` calls `ControllerPublishVolume`, which adds that node's Linode to the filesystem's Linode ACL and waits for the access policy to become active. There is no block device involved; "attach" here means "authorize".
2. **Stage.** The node plugin mounts the filesystem's `mount_target_fqdn` over NFSv4 at the kubelet staging path, once per node.
3. **Publish.** The node plugin bind-mounts the staging path into the pod's target path.

## 4. Share one volume between pods

This is the whole point of file storage. One PVC, many pods, on many nodes, all writing:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nfs-writers
spec:
  replicas: 3
  selector:
    matchLabels:
      app: nfs-writers
  template:
    metadata:
      labels:
        app: nfs-writers
    spec:
      containers:
        - name: writer
          image: busybox
          command:
            - /bin/sh
            - -c
            - while true; do echo "$(hostname) $(date)" >> /data/shared.log; sleep 5; done
          volumeMounts:
            - name: data
              mountPath: /data
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: nfs-pvc
```

```sh
kubectl exec deploy/nfs-writers -- tail -n 10 /data/shared.log
```

Every replica writes to the same filesystem, and each node the replicas land on gets its Linode added to the filesystem's ACL on first attach.

Pods sharing a volume on one node do not serialize behind each other: `NodePublishVolume` locks the pod target path, not the volume ID, so concurrent pod starts proceed in parallel. The one-per-node NFS mount is shared and staged only once.

## 🔓 Access modes

The driver accepts every mount access mode the CSI spec defines:

| Access mode | Short | Accepted |
| --- | --- | --- |
| `ReadWriteMany` | RWX | ✅ |
| `ReadOnlyMany` | ROX | ✅ |
| `ReadWriteOnce` | RWO | ✅ |
| `ReadWriteOncePod` | RWOP | ✅ |
| Block volumes | n/a | ❌ `only mount volume capabilities are supported` |
| Unset / unknown mode | n/a | ❌ `volume access mode is required` |

`ReadWriteMany` is the mode that reflects what the backend actually does. The narrower modes are accepted and honored by Kubernetes' own scheduling and binding rules, but nothing in the driver or the NFS backend enforces single-node exclusivity for them, so do not rely on `ReadWriteOnce` as a data-safety mechanism here.

Requesting a raw block volume (`volumeMode: Block`) fails with `InvalidArgument`. This is a filesystem, not a device.

## 📏 About capacity

`spec.resources.requests.storage` is **not enforced**. The Linode NFS API does not accept a size when creating a filesystem, so:

- The driver echoes the requested capacity back in the CSI `Volume` so Kubernetes can bind the PVC and report a size.
- Nothing stops the workload writing more than the request.
- `kubectl get pvc` shows the number you asked for, not a real quota.

For actual consumption, read the mounted filesystem instead:

```sh
kubectl exec nfs-app -- df -h /data
```

Or, if your metrics pipeline collects CSI volume stats, `NodeGetVolumeStats` reports both bytes and inodes read from `statfs(2)` on the mount.

When both `required_bytes` and `limit_bytes` are set, the driver reports `required_bytes`; with only a limit set, it reports the limit.

## ⚓ Mount options

Mount flags on the `StorageClass` are passed through to both the NFS mount and the pod bind-mount:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: linode-nfs-tuned
provisioner: linodenfs.csi.linode.com
parameters:
  linodenfs.csi.linode.com/space-id: "42"
mountOptions:
  - hard
  - noatime
  - rsize=1048576
  - wsize=1048576
  - timeo=600
  - retrans=2
reclaimPolicy: Delete
```

Notes:

- The filesystem type is always `nfs4`. There is no `fsType` parameter, and setting `csi.storage.k8s.io/fstype` has no effect.
- `NodeStageVolume` passes these flags to the NFS mount. `NodePublishVolume` passes them again alongside `bind`, and adds `ro` when the pod mounts the PVC read-only.
- Do not add `xprtsec=mtls` by hand. The driver adds it based on the Storage Space's mTLS mode, which arrives in the volume context. See [mTLS](./access-and-networking.md#-mtls).

## 🔄 Reclaim policy

Standard Kubernetes semantics apply, and the choice matters more than usual because the driver cannot list volumes for you.

| `reclaimPolicy` | On PVC deletion |
| --- | --- |
| `Delete` | The PV is removed and `DeleteVolume` deletes the NFS filesystem. Snapshots of it are affected by backend rules, not by the driver |
| `Retain` | The PV stays `Released` and the NFS filesystem stays in your Storage Space |

With `Retain`, cleanup is manual. Since `ListVolumes` is not implemented, the driver will not help you find orphans later; the filesystem label carries the old PV name, which is your only breadcrumb. Delete retained filesystems through the Cloud Manager or the API when you are done with them.

## 🔍 Inspecting a volume

The PV's `volumeHandle` and `volumeAttributes` tell you exactly which backend object a claim maps to:

```sh
kubectl get pv -o custom-columns=\
NAME:.metadata.name,\
HANDLE:.spec.csi.volumeHandle,\
TARGET:.spec.csi.volumeAttributes.mount-target,\
REGION:.spec.csi.volumeAttributes.region,\
MTLS:.spec.csi.volumeAttributes.mtls-mode
```

```text
NAME                                       HANDLE    TARGET                              REGION    MTLS
pvc-a1b2c3d4-5e6f-7890-abcd-ef1234567890   42/1337   fs-1337.us-ord.nfs.linode.com      us-ord    disabled
```

The handle is `{space_id}/{filesystem_id}`. To see which nodes are currently authorized, read the filesystem's access policy:

```sh
curl -sS \
  -H "Authorization: Bearer ${LINODE_API_TOKEN}" \
  https://api.linode.com/v4beta/nfs/spaces/42/filesystems/1337/access-policy
```

The `linode_acl` array is what the driver maintains through `ControllerPublishVolume` and `ControllerUnpublishVolume`.

## 🚫 What is not supported

| Operation | Status |
| --- | --- |
| **Volume expansion** | Not implemented. Set `allowVolumeExpansion: false`; editing a PVC's size does nothing |
| **Volume cloning** (`dataSource` of kind `PersistentVolumeClaim`) | Rejected with `unsupported volume content source`. Snapshot the source and restore from the snapshot instead |
| **Raw block volumes** | Rejected. Mount capabilities only |
| **`GetCapacity`** | Not implemented, so storage-capacity-aware scheduling has nothing to consume |
| **Listing volumes** | Not implemented and not advertised. See [the reasoning](./csi-rpc-reference.md#listvolumes-is-intentionally-unimplemented) |

## 📚 Related pages

- [StorageClass parameters](./storage-class-parameters.md)
- [Snapshots and restore](./snapshots.md)
- [Access and Networking](./access-and-networking.md)
- [Troubleshooting](./troubleshooting.md)
