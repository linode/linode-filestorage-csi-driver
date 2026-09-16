# 📖 Configuration Reference

Every knob the driver exposes, in one place: the environment the binary reads, the Helm values that set it, and the volume context the controller hands to the node.

## 📜 Table of Contents

1. [Driver environment variables](#-driver-environment-variables)
2. [Command-line flags](#-command-line-flags)
3. [Helm values](#-helm-values)
4. [StorageClass parameters](#-storageclass-parameters)
5. [Volume context keys](#-volume-context-keys)
6. [Deployed objects](#-deployed-objects)
7. [Sidecar versions](#-sidecar-versions)

## 🌱 Driver environment variables

The binary is configured entirely through the environment. There is no config file.

| Variable | Default | Role | Purpose |
| --- | --- | --- | --- |
| `DRIVER_ROLE` | `controller` | both | `controller` or `node`. Anything else is a startup error: `invalid driver role "..."` |
| `CSI_ENDPOINT` | `unix:///csi/csi.sock` | both | gRPC listen address. The chart overrides it to `unix:///var/lib/csi/sockets/pluginproxy/csi.sock` for the controller |
| `LINODE_TOKEN` | none | controller | Linode API token. **Required** for the controller; the process exits with `ErrTokenRequired` without it. The node plugin ignores it |
| `NODE_NAME` | none | node | The node's Kubernetes name, injected from `spec.nodeName`. Metadata resolution fails without it |
| `TIMEOUT` | `10s` | controller | Per-request Linode API client timeout, as a Go duration. An unparseable value logs a warning and falls back to 10s |
| `LINODE_URL` | `https://api.linode.com` | controller | Linode API base URL. Read by `linodego`, not by the driver's own config loading |
| `LINODE_API_VERSION` | `v4beta` | controller | Linode API version. Also read by `linodego` |
| `LINODE_CA` | none | controller | Path to a root CA certificate for the Linode API client. Useful only against a non-public endpoint |

Note the split: `LINODE_TOKEN`, `TIMEOUT`, `LINODE_CA`, `DRIVER_ROLE`, `CSI_ENDPOINT`, and `NODE_NAME` are read by the driver's own configuration loader; `LINODE_URL` and `LINODE_API_VERSION` are consumed by `linodego` directly from the environment. That is why they appear as chart values but nowhere in the driver's config struct.

`TIMEOUT` bounds a single API call, not an operation. Operations that wait for a resource to become active are bounded separately by a fixed 5-minute internal deadline, which is not configurable.

## 🚩 Command-line flags

The driver defines no flags of its own. It registers `klog`'s flags and forces `-logtostderr=true`, so the only useful one is verbosity, `-v=<level>`.

`klog` flags are command-line arguments, and the chart exposes no `args` value for the plugin container, so raising verbosity means patching the workload:

```sh
kubectl -n kube-system patch deploy csi-linode-nfs-controller --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args","value":["-v=4"]}]'
```

The levels the driver actually uses:

| Level | What appears |
| --- | --- |
| default | Errors and warnings |
| `-v=2` | Startup, role, endpoint, driver configuration, optional-mTLS fallbacks |
| `-v=4` | Every RPC entry (`handling controller rpc`), mount and unmount decisions, metadata fallbacks |
| `-v=5` | Idempotent no-op paths, such as an unstage on an already-unmounted path |

`-v=4` is the level worth reaching for when debugging; see [Troubleshooting](./troubleshooting.md#-increase-log-verbosity).

## ⛭ Helm values

### Top level

| Value | Default | Description |
| --- | --- | --- |
| `apiToken` | `""` | Linode API token. Required unless `secretRef` is set. The chart puts it in a Secret named `linode-api-token` |
| `namespace` | `kube-system` | Namespace for every object the chart creates |
| `kubeletPath` | `/var/lib/kubelet` | Host kubelet root. Controls the plugin socket and registration hostPaths |
| `linodeURL` | `https://api.linode.com` | Sets `LINODE_URL` |
| `linodeAPIVersion` | `v4beta` | Sets `LINODE_API_VERSION` |
| `secretRef.name` | unset | Use an existing Secret instead of creating one |
| `secretRef.apiTokenRef` | `token` | Key within that Secret |
| `driver.name` | `linodenfs.csi.linode.com` | The CSI driver name. Changing it changes the `CSIDriver` object, the socket path, and the `provisioner` every `StorageClass` must use |
| `podAnnotations` | `{}` | Applied to controller and node pods |
| `podLabels` | `{}` | Applied to controller and node pods |

Changing `driver.name` on an existing installation orphans every PV provisioned under the old name. Kubernetes routes by driver name, so those PVs become unmanageable. Do not.

### Driver image

| Value | Default | Description |
| --- | --- | --- |
| `image.repository` | `docker.io/linode/linode-filestorage-csi-driver` | Driver image |
| `image.tag` | `""` | Defaults to `Chart.appVersion` when empty |
| `image.pullPolicy` | `IfNotPresent` | |
| `image.podsMountDir` | `/var/lib/kubelet` | Host directory bind-mounted `Bidirectional` into the node plugin. Must track `kubeletPath` |
| `image.env` | `[]` | Extra env vars appended to the plugin container in **both** workloads |
| `image.volumes` | `[]` | Extra volumes on **both** pods |
| `image.volumeMounts` | `[]` | Extra mounts on the plugin container in **both** workloads |
| `image.resources` | `cpu: 100m`, `memory: 128Mi` requests | Plugin container resources, applied to both roles |

`image.env`, `image.volumes`, and `image.volumeMounts` are shared by the controller and the node. There is no per-role override, so anything you add here lands in both.

### Controller

| Value | Default | Description |
| --- | --- | --- |
| `controller.enabled` | `true` | Deploy the controller Deployment |
| `controller.replicaCount` | `1` | Replicas. See the note below |
| `controller.hostNetwork` | `false` | Host networking for the controller pod |
| `controller.dnsPolicy` | `Default` | Set to `ClusterFirstWithHostNet` if you enable `hostNetwork` |
| `controller.kubeconfig.mountDir` | `""` | Mount path for an external kubeconfig |
| `controller.kubeconfig.secretName` | `""` | Secret holding it |
| `controller.kubeconfig.secretKey` | `""` | Key within that Secret |
| `controller.serviceAccount.enabled` | `true` | Create the ServiceAccount. When false, the Deployment still references the name |
| `controller.serviceAccount.name` | `csi-linode-nfs-controller` | |
| `controller.rbac.enabled` | `true` | Create the ClusterRole and ClusterRoleBinding |
| `controller.nodeSelector` | `{}` | |
| `controller.affinity` | `{}` | |
| `controller.tolerations` | `CriticalAddonsOnly` + `NoExecute` | Lets the controller schedule on control-plane-ish nodes |

Set all three `kubeconfig` fields or none. Each piece of the rendering is guarded separately, and the combinations are not interchangeable:

| Rendered | Guarded by |
| --- | --- |
| The `controller-kubeconfig` volume | `secretName` and `secretKey` |
| The volume mount, on the plugin container and on every sidecar except `liveness-probe` | `mountDir` and `secretName` |
| `--kubeconfig=<mountDir>/<secretKey>` on `csi-provisioner`, `csi-attacher`, `csi-resizer`, and `csi-snapshotter` | `mountDir`, `secretName`, and `secretKey` |

So setting only `mountDir` and `secretName` renders a volume mount with no matching volume, and the API server rejects the pod template outright.

The flag reaches the four sidecars only. The `plugin` container has no `args` at all and takes no `--kubeconfig`: the driver parses only klog's flags, so an unrecognized flag would make it exit non-zero. The controller plugin always uses in-cluster configuration to reach the Kubernetes API; the kubeconfig is there for the sidecars.

**On `replicaCount`:** raising it above 1 gives you multiple driver processes, and the sidecars lease-elect among themselves so only one is active. The driver's own in-process locks (which serialize concurrent operations on the same volume) are per-process and do not coordinate across replicas, so leader election is what keeps that safe. Leave it at 1 unless you have a reason.

### Node

| Value | Default | Description |
| --- | --- | --- |
| `node.enabled` | `true` | Deploy the node DaemonSet |
| `node.pluginSecurityContext` | `privileged: true`, `runAsUser: 0`, `runAsGroup: 0` | Required. Root is needed to create the socket on the kubelet hostPath and to mount in the host namespace |
| `node.serviceAccount.enabled` | `true` | |
| `node.serviceAccount.name` | `csi-linode-nfs-node` | |
| `node.rbac.enabled` | `true` | Create the node ClusterRole and binding (`get`, `list`, `watch` on nodes) |
| `node.registrar.*` | see below | `csi-node-driver-registrar` image, pull policy, resources, plus `env` and `volumeMounts` extension points |
| `node.livenessProbe.*` | see below | `livenessprobe` image, pull policy, resources |

The node DaemonSet always uses `hostNetwork: true`; it is not a value. It has no tolerations either, so it will not schedule onto tainted nodes without help.

### Sidecars

| Value | Default | Enabled by default |
| --- | --- | --- |
| `sidecars.provisioner.*` | `registry.k8s.io/sig-storage/csi-provisioner:v6.2.0` | ✅ always |
| `sidecars.attacher.*` | `registry.k8s.io/sig-storage/csi-attacher:v4.12.0` | ✅ always |
| `sidecars.resizer.enabled` | `false` | ❌ leave off; expansion is unimplemented |
| `sidecars.resizer.*` | `registry.k8s.io/sig-storage/csi-resizer:v2.1.0` | |
| `sidecars.snapshotter.enabled` | `false` | ❌ turn on to use snapshots |
| `sidecars.snapshotter.*` | `registry.k8s.io/sig-storage/csi-snapshotter:v8.2.0` | |
| `sidecars.livenessProbe.*` | `registry.k8s.io/sig-storage/livenessprobe:v2.15.0` | ✅ always |

Each block takes `repository`, `tag`, `pullPolicy`, and `resources`.

## 🎚 StorageClass parameters

Summarized here; the full rules, error messages, and idempotency implications are on [StorageClass parameters](./storage-class-parameters.md).

| Parameter | Required | Values |
| --- | --- | --- |
| `linodenfs.csi.linode.com/space-id` | one of the two | positive integer as a string |
| `linodenfs.csi.linode.com/space-label` | one of the two | exact label of an existing space |
| `linodenfs.csi.linode.com/tags` | no | comma-separated |
| `linodenfs.csi.linode.com/filesystem-root-squash` | no | `none`, `root_squash`, `all_squash` |

## 🧾 Volume context keys

`CreateVolume` returns these in the CSI volume context. Kubernetes stores them as the PV's `spec.csi.volumeAttributes` and passes them back to the node plugin on every stage and publish. They are the controller's only channel to the node, which holds no API token and cannot look anything up.

| Key | Example | Consumed by |
| --- | --- | --- |
| `space-id` | `42` | informational |
| `filesystem-id` | `1337` | informational |
| `mount-target` | `fs-1337.us-ord.nfs.linode.com` | **`NodeStageVolume`, as the NFS mount source** |
| `region` | `us-ord` | informational |
| `mtls-mode` | `required`, `optional`, `disabled` | **`NodeStageVolume`, to decide on `xprtsec=mtls`** |

Two of the five are load-bearing:

- **`mount-target` is required.** `NodeStageVolume` fails with `InvalidArgument` when it is missing. The controller also refuses to return a volume whose filesystem has no mount target, so an empty value should never reach a PV.
- **`mtls-mode` is validated.** A value outside the three accepted strings fails with `invalid mtls-mode value, must be one of: required, optional, disabled`. Empty is allowed and means "no mTLS".

Editing a PV's `volumeAttributes` by hand is how you break a working volume. The values are a snapshot taken at provisioning time, and the driver never refreshes them; see the [mTLS caveat](./access-and-networking.md#-mtls).

## 📦 Deployed objects

What a default install creates, so you know what to look for:

| Kind | Name | Notes |
| --- | --- | --- |
| `CSIDriver` | `linodenfs.csi.linode.com` | `attachRequired: true`, `podInfoOnMount: false`, `fsGroupPolicy: File` |
| `Deployment` | `csi-linode-nfs-controller` | 4 containers by default |
| `DaemonSet` | `csi-linode-nfs-node` | 3 containers, `hostNetwork` |
| `Secret` | `linode-api-token` | Only when `secretRef` is unset |
| `ServiceAccount` | `csi-linode-nfs-controller`, `csi-linode-nfs-node` | |
| `ClusterRole` / `ClusterRoleBinding` | one pair per ServiceAccount | |

Everything carries the label `role: csi-linode`, which is the handle for finding it all:

```sh
kubectl -n kube-system get pods -l role=csi-linode
```

No `StorageClass` and no `VolumeSnapshotClass` are created. Both are yours to write.

## 🏷 Sidecar versions

For reference when matching against your cluster's Kubernetes version:

| Sidecar | Version | Purpose |
| --- | --- | --- |
| `csi-provisioner` | v6.2.0 | Watches PVCs, calls `CreateVolume` and `DeleteVolume` |
| `csi-attacher` | v4.12.0 | Watches `VolumeAttachment`s, calls `ControllerPublishVolume` and `ControllerUnpublishVolume` |
| `csi-snapshotter` | v8.2.0 | Watches `VolumeSnapshotContent`s, calls the snapshot RPCs. Disabled by default |
| `csi-resizer` | v2.1.0 | Would call `ControllerExpandVolume`. Disabled, and unimplemented |
| `csi-node-driver-registrar` | v2.16.0 | Registers the plugin with the kubelet |
| `livenessprobe` | v2.15.0 | Probes the driver's own `Probe` RPC. Runs in both workloads |

## 📚 Related pages

- [Installation](./installation.md)
- [StorageClass parameters](./storage-class-parameters.md)
- [CSI RPC reference](./csi-rpc-reference.md)
- [Troubleshooting](./troubleshooting.md)
