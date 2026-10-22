# 🛠 Troubleshooting

Start by finding out which half of the driver is unhappy. Provisioning problems are the controller; mount problems are the node plugin.

## 📜 Table of Contents

1. [Where to look first](#-where-to-look-first)
2. [Increase log verbosity](#-increase-log-verbosity)
3. [A PVC stays Pending](#-a-pvc-stays-pending)
4. [A pod stays ContainerCreating](#-a-pod-stays-containercreating)
5. [The node plugin never registers](#-the-node-plugin-never-registers)
6. [A PVC or PV will not delete](#-a-pvc-or-pv-will-not-delete)
7. [Permission denied inside the pod](#-permission-denied-inside-the-pod)
8. [Unsupported requests](#-unsupported-requests)
9. [Error message index](#-error-message-index)
10. [Collecting a bug report](#-collecting-a-bug-report)

## 🔎 Where to look first

```sh
# Is everything running?
kubectl -n kube-system get pods -l role=csi-linode

# Provisioning problems: the controller and its sidecars
kubectl -n kube-system logs deploy/csi-linode-nfs-controller -c plugin --tail=100
kubectl -n kube-system logs deploy/csi-linode-nfs-controller -c csi-provisioner --tail=100
kubectl -n kube-system logs deploy/csi-linode-nfs-controller -c csi-attacher --tail=100

# Mount problems: the node plugin on the node the pod landed on
kubectl -n kube-system logs -l app=csi-linode-nfs-node -c plugin --tail=100

# What Kubernetes itself thinks
kubectl describe pvc <name>
kubectl describe pod <name>
kubectl get events --sort-by=.lastTimestamp | tail -30
```

The sidecars log the gRPC error the driver returned, verbatim. That message is almost always the fastest route to the cause, so read the sidecar log before the plugin log.

Which container to blame:

| Symptom | Container |
| --- | --- |
| PVC `Pending`, no PV | `csi-provisioner`, then `plugin` in the controller |
| PV bound, pod stuck at `ContainerCreating` with an attach error | `csi-attacher`, then `plugin` in the controller |
| Pod stuck at `ContainerCreating` with a mount error | `plugin` in the node pod on that node |
| Node missing from `csinodes` | `node-driver-registrar` in the node pod |

## 📢 Increase log verbosity

The driver is quiet at the default level. `-v=4` logs every RPC and every mount decision:

```sh
kubectl -n kube-system patch deploy csi-linode-nfs-controller --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args","value":["-v=4"]}]'

kubectl -n kube-system patch ds csi-linode-nfs-node --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args","value":["-v=4"]}]'
```

Both patches restart the workload. Restarting the node plugin does **not** disturb existing mounts, because they live in the host mount namespace rather than in the container. Remove the args when you are done:

```sh
kubectl -n kube-system patch deploy csi-linode-nfs-controller --type=json \
  -p='[{"op":"remove","path":"/spec/template/spec/containers/0/args"}]'
```

## ⏳ A PVC stays Pending

```sh
kubectl describe pvc <name> | tail -20
```

The event from `csi-provisioner` carries the driver's error. Match it below.

### No cluster VPC found

```text
FailedPrecondition: this driver requires VPC-backed IPv6 connectivity; cluster VPC not found
```

The Linode behind the cluster's first node has no VPC interface. This is the single most common installation failure.

```sh
# Get a Linode ID
kubectl get nodes -o jsonpath='{.items[0].spec.providerID}'

# Check its interfaces
curl -sS -H "Authorization: Bearer ${LINODE_API_TOKEN}" \
  https://api.linode.com/v4/linode/instances/<id>/configs
```

Look for an active interface with `"purpose": "vpc"` and a non-null `vpc_id`. If there is none, the node pool has to be recreated inside a VPC; a running Linode's interfaces cannot be reconfigured into a VPC in place. See [VPC requirement](./access-and-networking.md#-vpc-requirement).

If the token lacks read access to Linodes, this same error appears even though the VPC exists, because the interface lookup fails. Check for a `PermissionDenied` earlier in the controller log.

### Nodes in multiple regions

```text
FailedPrecondition: resolve cluster metadata: cluster has multiple regions: "us-ord" and "us-east"
```

Nodes report different `topology.kubernetes.io/region` values, and the driver refuses to guess which region a filesystem belongs in. Split the cluster, or remove the out-of-region nodes.

### Node has no Linode provider ID

```text
FailedPrecondition: resolve cluster metadata: node "...": invalid provider ID
```

A node's `spec.providerID` is empty or is not `linode://<id>`. Either the Linode CCM is not installed, or it has not processed that node yet.

```sh
kubectl get nodes -o custom-columns=NAME:.metadata.name,PROVIDER:.spec.providerID
kubectl -n kube-system get pods | grep ccm
```

This clears itself once the CCM catches up.

### Node missing a region label or an address

```text
node region label not found
node allowlist IP not found
```

Also a CCM problem. The driver needs `topology.kubernetes.io/region` (or the beta label) and at least one `InternalIP` or `ExternalIP`. Check `.metadata.labels` and `.status.addresses`:

```sh
kubectl get node <name> -o yaml | head -40
```

### No Storage Space in the StorageClass

```text
InvalidArgument: StorageClass must set either linodenfs.csi.linode.com/space-id or
linodenfs.csi.linode.com/space-label for a pre-created NFS Storage Space
```

Both parameters must carry the full driver-name prefix. A bare `space-id` is silently ignored, which looks exactly like not setting it.

```sh
kubectl get sc <name> -o jsonpath='{.parameters}' | python3 -m json.tool
```

The sibling errors are `... are mutually exclusive` (both set) and `... must be a positive integer` (a bad `space-id`).

### Storage Space label not found

```text
NotFound: NFS space with label "..." was not found
```

Labels are matched exactly, including case. List what exists:

```sh
curl -sS -H "Authorization: Bearer ${LINODE_API_TOKEN}" \
  https://api.linode.com/v4beta/nfs/spaces
```

### Duplicate Storage Space labels

```text
FailedPrecondition: multiple NFS spaces match label "..."
```

Two spaces share the label. Switch the `StorageClass` to `space-id`, which cannot be ambiguous.

### Token rejected

```text
PermissionDenied: create NFS filesystem: ...
```

The token is wrong, expired, or too narrowly scoped. Test it:

```sh
curl -sS -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer ${LINODE_API_TOKEN}" \
  https://api.linode.com/v4beta/nfs/spaces
```

Anything other than `200` is a token problem. Rotating the Secret requires a controller restart, since `LINODE_TOKEN` is read once at startup:

```sh
kubectl -n kube-system rollout restart deploy/csi-linode-nfs-controller
```

### Timed out waiting for the filesystem

```text
DeadlineExceeded: wait for NFS filesystem active: ...
```

The filesystem was created but did not reach `active` within 5 minutes. That timeout is not configurable. `csi-provisioner` retries, and the retry finds the still-settling filesystem through the idempotency lookup rather than creating a second one, so this usually resolves itself. If it repeats, check the filesystem's status directly and open a support ticket; the driver is only reporting what the backend told it.

### Existing filesystem does not match the request

```text
AlreadyExists: NFS filesystem "..." already exists with incompatible tags
AlreadyExists: NFS filesystem "..." already exists with incompatible root squash policy
```

The idempotency lookup found a filesystem with this volume's label already in the space, but its tags or squash policy do not match what the `StorageClass` asked for, so the driver refused to reuse it. `StorageClass` parameters cannot be edited in place (see [Parameters are immutable](./storage-class-parameters.md#-parameters-are-immutable)), so the usual causes are a class that was deleted and recreated with different parameters while a volume was still being provisioned, or a filesystem created outside the driver whose label collides.

Delete the PVC, then either provision against a class whose parameters match the existing filesystem or remove the conflicting filesystem from the space.

### No provisioning events at all

`csi-provisioner` never saw the PVC. Check that the provisioner name is exactly right and that the controller is running:

```sh
kubectl get sc <name> -o jsonpath='{.provisioner}'   # linodenfs.csi.linode.com
kubectl -n kube-system get deploy csi-linode-nfs-controller
```

## 🐣 A pod stays ContainerCreating

```sh
kubectl describe pod <name> | tail -30
```

### Attachment never completes

This applies only when `accessPolicy.mode=node`:

```text
Unable to attach or mount volumes: ... volume attachment is being created
```

`ControllerPublishVolume` has not finished. It adds the node's Linode to the filesystem ACL and waits for the access policy to go active, which is not instant.

```sh
kubectl get volumeattachment | grep <pv-name>
kubectl -n kube-system logs deploy/csi-linode-nfs-controller -c csi-attacher --tail=50
```

A `DeadlineExceeded` here means the access policy is not settling; the attacher retries. In `space` mode, no `VolumeAttachment` should exist. If one does after changing modes, verify that the `CSIDriver` was recreated with `spec.attachRequired: false`.

### Mount times out or is denied

```text
mount.nfs4: Connection timed out
mount.nfs4: No route to host
mount.nfs4: access denied by server while mounting ...
```

The node cannot reach the mount target, or the server refused it. Provisioning succeeding tells you nothing here: the driver does not validate the Space access policy, so a PVC can bind against a Space that will never admit the cluster. Check in this order, cheapest first:

1. **The Space access policy.** This is the most common cause, and in `space` mode it is the only authorization layer. The Space owner maintains it, and the driver never checks or changes it.

   ```sh
   curl -sS -H "Authorization: Bearer ${LINODE_API_TOKEN}" \
     https://api.linode.com/v4beta/nfs/spaces/<space>/access-policy
   ```

   Confirm all of the following:

   - `enabled` is `true`.
   - `status` is `active`. While it is anything else (for example `updating`), entries you just added may be visible but not yet applied.
   - The cluster VPC ID is in `vpc_acl`. Find it from any node's Linode interfaces; it is the VPC the driver infers for the cluster (see [No cluster VPC found](#no-cluster-vpc-found)).
   - The VPC entry covers the subnets every node pool uses. A node in an uncovered subnet fails while others mount fine.

   Fix the policy (or ask the Space owner to), wait for `status` to return to `active`, and the kubelet's next mount retry succeeds without any change to the PVC.

2. **The Linode ACL (`node` mode only).** Is the node's Linode actually in it, and is the policy enabled?

   ```sh
   curl -sS -H "Authorization: Bearer ${LINODE_API_TOKEN}" \
     https://api.linode.com/v4beta/nfs/spaces/<space>/filesystems/<fs>/access-policy
   ```

   Compare `linode_acl` against the node's Linode ID and confirm `enabled` is `true`.

3. **DNS, when using an FQDN.** The mount target FQDN has to resolve from the **host**, not from CoreDNS, because the node plugin uses `hostNetwork` and mounts in the host namespace. IP-only volumes use `mount-target-ips` and do not require DNS for target selection.

   ```sh
   kubectl get pv <pv> -o jsonpath='{.spec.csi.volumeAttributes.mount-target}'
   # then, on the node itself
   getent hosts <fqdn>
   ```

4. **TCP 2049.** A Cloud Firewall or egress policy blocking it looks exactly like an ACL problem. See [firewalls and ports](./access-and-networking.md#-firewalls-and-ports).

### Incorrect mount option

```text
mount.nfs4: an incorrect mount option was specified
```

Usually a bad flag in the `StorageClass` `mountOptions`. Remove it and let the driver assemble the options itself.

### Missing mount target in the volume context

```text
InvalidArgument: mount-target or usable mount-target-ips is required
```

The node received no usable mount source in either `mount-target` or `mount-target-ips`. An empty `mount-target` is valid if usable IPv6 targets are present. IPv4 and IPv4-mapped IPv6 addresses cannot be used as IP mount targets. If provisioning failed because the API returned no usable targets, fix the backend issue and let provisioning retry.

For a statically provisioned PV, you can supply the filesystem's API-reported targets yourself. Include the export path: put the FQDN source in `mount-target` or comma-separated IPv6 sources in `mount-target-ips`. The node prefers a non-empty `mount-target`, so omit it or leave it empty to use IPv6 addresses. It does not refresh these attributes from the API when the backend changes. Use only targets for this filesystem; do not invent a target or use another filesystem's export.

Kubernetes does not allow changes to an existing PV's `spec.csi`, including `volumeAttributes`. To correct its targets:

1. Stop the consumers, set the PV's reclaim policy to `Retain`, and save its manifest.
2. Remove the PVC and PV objects. Keep the backend filesystem.
3. Recreate the PV with the same `volumeHandle`, the corrected targets, and the other original CSI settings.
4. Explicitly bind a PVC to the replacement PV, then restart the consumers.

Do not remove protection finalizers to bypass an in-use volume.

### Mounts work on some nodes only

That is the signature of a node outside the cluster VPC, or in a subnet the Space access policy does not cover. The VPC is inferred from the **first** node alone, so a second node pool created outside it provisions fine and never mounts. Compare each node's Linode interfaces against the Space policy's `vpc_acl`; see [Mount times out or is denied](#mount-times-out-or-is-denied).

### Benign lock contention

```text
Aborted: An operation with the given volume key ... already exists.
```

Not an error to fix. Two operations raced on the same volume and one backed off; the sidecar retries. It only matters if it repeats for minutes, which suggests an operation is wedged, most likely inside a 5-minute wait.

## 🔌 The node plugin never registers

A node missing from `csinodes` will never have a pod mount a volume on it.

```sh
kubectl get csinodes -o custom-columns=NODE:.metadata.name,DRIVERS:.spec.drivers[*].name
```

If `linodenfs.csi.linode.com` is absent for a node:

```sh
kubectl -n kube-system get pods -l app=csi-linode-nfs-node -o wide
kubectl -n kube-system logs <node-pod> -c node-driver-registrar
kubectl -n kube-system logs <node-pod> -c plugin
```

| Cause | Fix |
| --- | --- |
| No node pod on that node at all | The DaemonSet has **no tolerations**. A tainted node gets no node plugin. Add tolerations to the DaemonSet, or remove the taint |
| The registrar cannot reach `/csi/csi.sock` | The plugin container failed to start. Read its log; an invalid `DRIVER_ROLE` exits immediately with `invalid driver role` |
| The registrar starts, then fails registration | The plugin is `Running` and serving, but `NodeGetInfo` is failing. A missing or empty `NODE_NAME` looks exactly like this, because the name is only read when a node RPC needs it (see below) |
| The registrar reports a bad registration path | `kubeletPath` does not match this distribution's kubelet root |
| The plugin cannot create the socket | The kubelet hostPath needs root. Do not weaken `node.pluginSecurityContext` |
| `Internal: metadata service is not configured` | The plugin can reach neither the instance metadata service nor the Kubernetes API. Check the node ServiceAccount and its RBAC on nodes |

**On a missing `NODE_NAME`.** Nothing validates it at startup, so the container does not crash and the log looks healthy. The registrar's log is where it surfaces, because registration calls `NodeGetInfo`:

```text
resolve node metadata: metadata service requires node name
```

The chart populates it from `fieldRef: spec.nodeName`, so this only happens if the DaemonSet's env was overridden. Confirm what the container actually got:

```sh
kubectl -n kube-system get ds csi-linode-nfs-node \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="plugin")].env}' | tr ',' '\n'
```

For a non-standard kubelet root, both values must change together:

```yaml
kubeletPath: /var/lib/k0s/kubelet
image:
  podsMountDir: /var/lib/k0s/kubelet
```

Setting only one of the two produces a plugin that registers but cannot publish, which is the most confusing possible failure mode.

## 🧹 A PVC or PV will not delete

Order matters: delete the workload, then the PVC, then the driver. Out of order, you get stuck finalizers.

```sh
# What is still holding it?
kubectl get pvc <name> -o jsonpath='{.metadata.finalizers}'
kubectl get pv <name> -o jsonpath='{.metadata.finalizers}'
kubectl get volumeattachment | grep <pv>
```

| Situation | What to do |
| --- | --- |
| Pods still using the PVC | Delete them. `kubernetes.io/pvc-protection` holds the PVC while any pod references it |
| The driver was uninstalled first | Reinstall it, let the deletes drain, then uninstall again. Nothing else can satisfy the CSI finalizers |
| `DeleteVolume` returns `PermissionDenied` | Token problem. Fix the token, restart the controller, and the delete retries |
| `VolumeAttachment` stuck in `node` mode | Check the attacher log. `ControllerUnpublishVolume` returns success for an already-deleted filesystem, so a genuine hang here is an API or ACL wait |

Force-removing a finalizer leaks the backend filesystem. If you do it deliberately, note `spec.csi.volumeHandle` first so you can delete the filesystem by hand.

Cleanup that survives a normal uninstall, by design:

- The Storage Space, which the driver never created.
- The Space access policy, which the driver never validates or changes.
- Filesystems whose PVs used `Retain`. `ListVolumes` is unimplemented, so the filesystem label (the old PV name) is your only breadcrumb for finding them.

## 🚫 Permission denied inside the pod

The volume mounts, but the application cannot write.

```sh
kubectl exec <pod> -- ls -ln /data
kubectl exec <pod> -- id
```

| Cause | Fix |
| --- | --- |
| `root_squash` and the container runs as `root` | Expected. Root is remapped to the anonymous user. Run as a UID that owns the directory, or use `filesystem-root-squash: none` |
| `all_squash` | Every UID is remapped; only the anonymous user can write |
| UID mismatch on an existing directory | Set `securityContext.fsGroup` on the pod. `fsGroupPolicy: File` on the `CSIDriver` means the kubelet will apply it |
| The pod mounts the PVC read-only | `readOnly: true` on the volume adds `ro` to the bind mount. Check the pod spec |

An image that `chown`s its data directory at startup will fail under `root_squash`. That is what squashing is for, not a driver bug. See [squash policy](./access-and-networking.md#-squash-policy).

## 🚫 Unsupported requests

### Unsupported volume content source

```text
InvalidArgument: unsupported volume content source
```

You used a `dataSource` of `kind: PersistentVolumeClaim`. Direct PVC cloning is not supported.

## 📇 Error message index

| Message | Meaning | Section |
| --- | --- | --- |
| `this driver requires VPC-backed IPv6 connectivity` | No VPC on the first node's Linode | [No cluster VPC found](#no-cluster-vpc-found) |
| `cluster has multiple regions` | Nodes disagree on region | [Nodes in multiple regions](#nodes-in-multiple-regions) |
| `invalid provider ID` | The CCM has not labelled the node | [Node has no Linode provider ID](#node-has-no-linode-provider-id) |
| `node region label not found` | Missing region label | [Node missing a region label or an address](#node-missing-a-region-label-or-an-address) |
| `node allowlist IP not found` | The node reports no addresses | [Node missing a region label or an address](#node-missing-a-region-label-or-an-address) |
| `StorageClass must set either` | No space parameter | [No Storage Space in the StorageClass](#no-storage-space-in-the-storageclass) |
| `are mutually exclusive` | Both space parameters set | [No Storage Space in the StorageClass](#no-storage-space-in-the-storageclass) |
| `must be a positive integer` | Bad `space-id` | [No Storage Space in the StorageClass](#no-storage-space-in-the-storageclass) |
| `NFS space with label ... was not found` | Label typo or case mismatch | [Storage Space label not found](#storage-space-label-not-found) |
| `multiple NFS spaces match label` | Duplicate space labels | [Duplicate Storage Space labels](#duplicate-storage-space-labels) |
| `PermissionDenied` on any call | Token wrong, expired, or under-scoped | [Token rejected](#token-rejected) |
| `wait for NFS filesystem active` with `DeadlineExceeded` | Backend still settling | [Timed out waiting for the filesystem](#timed-out-waiting-for-the-filesystem) |
| `already exists with incompatible tags` | Label collides with a filesystem created under different parameters | [Existing filesystem does not match the request](#existing-filesystem-does-not-match-the-request) |
| `unsupported ... filesystem-root-squash value` | Bad squash value | [StorageClass parameters](./storage-class-parameters.md#filesystem-root-squash) |
| `only mount volume capabilities are supported` | `volumeMode: Block` requested | [Access modes](./usage.md#-access-modes) |
| `volume access mode is required` | No access mode on the PVC | [Access modes](./usage.md#-access-modes) |
| `unsupported volume content source` | PVC-to-PVC clone attempted | [Unsupported volume content source](#unsupported-volume-content-source) |
| `mount.nfs4: Connection timed out` or `access denied by server` | Space access policy disabled, not `active`, or missing the cluster VPC; or network/DNS | [Mount times out or is denied](#mount-times-out-or-is-denied) |
| `mount-target or usable mount-target-ips is required` | PV attributes contain neither target form | [Missing mount target in the volume context](#missing-mount-target-in-the-volume-context) |
| `metadata service is not configured` | The node plugin cannot resolve itself | [The node plugin never registers](#-the-node-plugin-never-registers) |
| `An operation with the given volume key` | Lock contention; retried automatically | [Benign lock contention](#benign-lock-contention) |
| `operation not implemented` | An unimplemented RPC was called | [What is not implemented](./usage.md#-what-is-not-supported) |
| `volume path not found` | `NodeGetVolumeStats` on a path that is gone | [NodeGetVolumeStats](./architecture.md#nodegetvolumestats) |

## 🧰 Collecting a bug report

When opening an [issue](https://github.com/linode/linode-filestorage-csi-driver/issues), include:

```sh
# Versions
kubectl version
kubectl -n kube-system get deploy csi-linode-nfs-controller \
  -o jsonpath='{.spec.template.spec.containers[*].image}'; echo
helm -n kube-system list

# State
kubectl -n kube-system get pods -l role=csi-linode -o wide
kubectl get csidrivers linodenfs.csi.linode.com -o yaml
kubectl get csinodes -o yaml
kubectl get sc <name> -o yaml
kubectl get pvc <name> -o yaml
kubectl get pv <pv> -o yaml

# Logs, ideally after re-running the failure at -v=4
kubectl -n kube-system logs deploy/csi-linode-nfs-controller --all-containers --tail=500
kubectl -n kube-system logs <node-pod> --all-containers --tail=500

# Events
kubectl get events --sort-by=.lastTimestamp
```

None of those outputs contain the API token, and none of them should: do not include `kubectl get secret linode-api-token -o yaml`.

Say which Linode region you are in and whether the nodes are in a VPC. Those two account for most of the difficulty in reproducing a report.

## 📚 Related pages

- [Installation](./installation.md)
- [Access and Networking](./access-and-networking.md)
- [Configuration Reference](./configuration-reference.md)
