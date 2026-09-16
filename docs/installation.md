# 🚀 Installation

## 📜 Table of Contents

1. [Requirements](#-requirements)
2. [Secure a Linode API access token](#-secure-a-linode-api-access-token)
3. [Create an NFS Storage Space](#-create-an-nfs-storage-space)
4. [Deployment methods](#-deployment-methods)
    - [Using Helm](#1-using-helm)
    - [Using kubectl](#2-using-kubectl)
5. [Verify the installation](#-verify-the-installation)
6. [Upgrading the driver](#-upgrading-the-driver)
7. [Uninstalling the driver](#-uninstalling-the-driver)
8. [Optional configuration](#-optional-configuration)

## 🔧 Requirements

| Requirement | Detail |
| --- | --- |
| **Kubernetes** | A cluster with the CSI feature set enabled and `snapshot.storage.k8s.io` CRDs installed if you want snapshots |
| **Linode Cloud Controller Manager** | Required. The driver reads `spec.providerID` (`linode://12345`) off node objects to resolve Linode IDs |
| **Region topology labels** | Nodes must carry `topology.kubernetes.io/region` (or the beta label). The CCM sets these |
| **A VPC** | Every node must be attached to a VPC. The driver requires VPC-backed IPv6 connectivity to reach mount targets |
| **A single region** | All nodes must be in one region. A mixed-region cluster is rejected when resolving cluster metadata |
| **An NFS Storage Space** | Pre-created. The driver provisions filesystems inside it but never creates the space |
| **`LINODE_TOKEN`** | A [Personal Access Token](https://cloud.linode.com/profile/tokens) with read/write on Linodes, VPCs, and NFS resources |

> The VPC requirement is a hard precondition, not a recommendation. `CreateVolume` fails with `FailedPrecondition: this driver requires VPC-backed IPv6 connectivity; cluster VPC not found` when the node's Linode has no VPC interface. See [Access and Networking](./access-and-networking.md#-vpc-requirement).

## 🔐 Secure a Linode API access token

Generate a Personal Access Token (PAT) in the [Linode Cloud Manager](https://cloud.linode.com/profile/tokens). The controller uses it for every Linode API call, so scope it to what the driver actually touches:

- **Linodes**: read/write, to resolve instance interfaces and configs when finding the cluster VPC
- **VPCs**: read, to attach the cluster VPC to the space access policy
- **NFS / file storage**: read/write, to create and delete filesystems, snapshots, and access policies

Give it an expiry long enough for continued use, and plan to rotate it. The token is injected as the `LINODE_TOKEN` environment variable, so a rotation requires restarting the controller pod.

The node DaemonSet does not receive the token and does not call the Linode API.

## 📂 Create an NFS Storage Space

The driver never creates a Storage Space. Create one for your cluster, then note either its numeric ID or its label; every `StorageClass` must name one or the other.

Create it in the Cloud Manager, or against the API directly:

```sh
curl -sS -X POST \
  -H "Authorization: Bearer ${LINODE_API_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"label": "my-cluster-space"}' \
  https://api.linode.com/v4beta/nfs/spaces
```

List the spaces on the account to find the ID:

```sh
curl -sS \
  -H "Authorization: Bearer ${LINODE_API_TOKEN}" \
  https://api.linode.com/v4beta/nfs/spaces
```

You will reference it from a `StorageClass` as either:

```yaml
parameters:
  linodenfs.csi.linode.com/space-id: "42"
  # or
  linodenfs.csi.linode.com/space-label: "my-cluster-space"
```

The two are mutually exclusive, and one of them is required. See [StorageClass parameters](./storage-class-parameters.md).

## 🧰 Deployment methods

There are two ways to deploy the driver:

1. **Using Helm** (recommended)
2. **Using kubectl** with the released Kustomize manifest

The Helm chart in `charts/linode-nfs-csi-driver` is the source of truth. `deploy/kubernetes/base` is *rendered from it* by `mise run update-kustomize`, and CI fails if the two drift apart, so both paths deploy the same objects.

### 1. Using Helm

#### 🔄 Add the linode-csi repo

```sh
helm repo add linode-nfs-csi https://linode.github.io/linode-filestorage-csi-driver/
helm repo update linode-nfs-csi
```

Every tagged release publishes the chart to that repository, so this is the only step that needs repeating when a new version lands.

#### 🚀 Deploy the CSI driver

```sh
export LINODE_API_TOKEN="...your Linode API token..."

helm install linode-nfs-csi-driver \
  --namespace kube-system \
  --set apiToken="${LINODE_API_TOKEN}" \
  linode-nfs-csi/linode-nfs-csi-driver
```

Pin a version with `--version`, and use `helm upgrade --install` if you want the same command to work whether or not the release already exists.

Or install from a checkout of the repository:

```sh
export LINODE_TOKEN="...your Linode API token..."
mise run helm-install
```

`mise run helm-install` wraps `helm upgrade --install` against `charts/linode-nfs-csi-driver`, targeting the `kube-system` namespace and setting the image repository and tag from `IMAGE_REPO` and `IMAGE_VERSION`. It refuses to run without `LINODE_TOKEN` set.

*See [helm install](https://helm.sh/docs/helm/helm_install/) for command documentation.*

#### 🔑 Bring your own Secret

When `secretRef` is unset and `apiToken` is provided, the chart creates a Secret named `linode-api-token` in the release namespace with the token under the key `token`.

To point at a Secret you manage instead, set `secretRef` and leave `apiToken` empty:

```yaml
# values.yaml
secretRef:
  name: my-linode-secret
  apiTokenRef: token   # defaults to "token" when omitted
```

The chart then skips creating a Secret and wires `LINODE_TOKEN` to `my-linode-secret`'s `token` key.

#### 🔩 Configuring the chart

Override values with `--set var=value`, or with a custom values file:

```sh
helm upgrade --install linode-nfs-csi-driver \
  --namespace kube-system \
  -f custom-values.yaml \
  charts/linode-nfs-csi-driver
```

Using a values file is the recommended approach; `--set` on deeply nested keys is easy to get subtly wrong. For the full list of values, see the [Configuration Reference](./configuration-reference.md#-helm-values) or [`charts/linode-nfs-csi-driver/values.yaml`](https://github.com/linode/linode-filestorage-csi-driver/blob/main/charts/linode-nfs-csi-driver/values.yaml).

### 2. Using kubectl

#### 🔑 Create the Secret first

The raw manifests **do not** create the API token Secret. Create it before applying them:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: linode-api-token
  namespace: kube-system
stringData:
  token: "your linode api token"
type: Opaque
```

```sh
kubectl apply -f secret.yaml
kubectl get secret linode-api-token -n kube-system
```

The Secret name and key are not configurable in the raw manifests: the controller expects `linode-api-token` in `kube-system` with a `token` key. Use Helm if you need different names.

#### 🚀 Apply the manifest

Apply a released manifest:

```sh
export VERSION="v0.0.1"
kubectl apply -f https://github.com/linode/linode-filestorage-csi-driver/releases/download/${VERSION}/linode-filestorage-csi-driver-${VERSION}.yaml
```

Or build it from a checkout with Kustomize:

```sh
kustomize build deploy/kubernetes/base | kubectl apply -f -
```

There are also `deploy/kubernetes/overlays/ci` and `deploy/kubernetes/overlays/dev` overlays, both of which currently just include the base.

> Do not hand-edit `deploy/kubernetes/base`. It is generated. Change the Helm chart and run `mise run update-kustomize`.

## ✅ Verify the installation

```sh
kubectl -n kube-system get pods -l role=csi-linode
kubectl get csidrivers linodenfs.csi.linode.com
```

You should see the controller Deployment and one node pod per node:

```text
NAME                                       READY   STATUS    RESTARTS   AGE
csi-linode-nfs-controller-6b8f9c4d7-x2klm  4/4     Running   0          45s
csi-linode-nfs-node-2rq8w                  3/3     Running   0          45s
csi-linode-nfs-node-9fzp4                  3/3     Running   0          45s
```

The controller has 4 containers by default (`plugin`, `csi-provisioner`, `csi-attacher`, `liveness-probe`), plus `csi-resizer` and `csi-snapshotter` if you enabled them. Each node pod has 3 (`plugin`, `node-driver-registrar`, `liveness-probe`).

Confirm the node plugins registered with their kubelets:

```sh
kubectl get csinodes -o custom-columns=NODE:.metadata.name,DRIVERS:.spec.drivers[*].name
```

Every node should list `linodenfs.csi.linode.com`. If one does not, see [Troubleshooting](./troubleshooting.md#the-node-plugin-never-registers).

## ⏫ Upgrading the driver

```sh
export LINODE_API_TOKEN="...your Linode API token..."

helm repo update linode-csi

helm upgrade linode-nfs-csi-driver \
  --install \
  --namespace kube-system \
  --set apiToken="${LINODE_API_TOKEN}" \
  linode-nfs-csi/linode-nfs-csi-driver
```

`helm repo update` first, or Helm upgrades to the newest chart it already knows about rather than the newest chart that exists.

Upgrades roll the controller Deployment and the node DaemonSet. Mounted volumes stay mounted across a node plugin restart, because the mounts live in the host mount namespace, not the plugin container. In-flight provisioning retries once the controller is back.

*See [helm upgrade](https://helm.sh/docs/helm/helm_upgrade/) for command documentation.*

## 🧹 Uninstalling the driver

Delete workloads and PVCs **before** removing the driver. Once the controller is gone, nothing can process `DeleteVolume`, and PVs with a `Delete` reclaim policy will hang in `Terminating` with their finalizers unsatisfied.

```sh
kubectl delete pvc --all -n <your-namespace>
helm uninstall linode-nfs-csi-driver -n kube-system
```

Uninstalling does not remove:

- The NFS Storage Space, which the driver never owned.
- The cluster VPC entry the driver added to the space access policy. Remove it by hand if you are decommissioning the cluster.
- Filesystems whose PVs used the `Retain` reclaim policy.

*See [helm uninstall](https://helm.sh/docs/helm/helm_uninstall/) for command documentation.*

## 🧩 Optional configuration

### Enable the snapshotter

Snapshot RPCs are implemented and `CREATE_DELETE_SNAPSHOT` is advertised, but the sidecar ships disabled. Turn it on to use `VolumeSnapshot` objects:

```yaml
sidecars:
  snapshotter:
    enabled: true
```

You also need the `snapshot.storage.k8s.io` CRDs installed in the cluster. See [Snapshots](./snapshots.md).

### The resizer stays off

```yaml
sidecars:
  resizer:
    enabled: false   # leave it here
```

`ControllerExpandVolume` returns `Unimplemented` and `EXPAND_VOLUME` is not advertised. Enabling `csi-resizer` produces a sidecar that logs errors against every expansion attempt without accomplishing anything.

### ServiceAccount and RBAC toggles

The chart creates both ServiceAccounts and their ClusterRoles/ClusterRoleBindings by default. Disable them to manage them yourself:

```yaml
controller:
  serviceAccount:
    enabled: true          # false: skip creating it; the Deployment still references the name
    name: csi-linode-nfs-controller
  rbac:
    enabled: true          # false: skip the ClusterRole and ClusterRoleBinding

node:
  serviceAccount:
    enabled: true
    name: csi-linode-nfs-node
  rbac:
    enabled: true
```

The controller role needs PVs, PVCs, nodes, events, secrets, CSINodes, VolumeAttachments, StorageClasses, leases, and the snapshot API group. The node role needs only `get`, `list`, and `watch` on nodes, which is how the node plugin resolves its own Linode ID when instance metadata is unavailable.

### Alternate kubelet path

Distributions that do not use `/var/lib/kubelet` need both values changed together:

```yaml
kubeletPath: /var/lib/k0s/kubelet
image:
  podsMountDir: /var/lib/k0s/kubelet
```

`kubeletPath` controls the plugin socket and registration directories; `podsMountDir` is the bidirectional host mount where pod volume directories live. Leaving them inconsistent produces a node plugin that registers but cannot publish.

### Non-default Linode API endpoint

```yaml
linodeURL: https://api.linode.com
linodeAPIVersion: v4beta
```

Both are read by `linodego` from the environment (`LINODE_URL`, `LINODE_API_VERSION`). The default API version is `v4beta` because the managed NFS endpoints are still beta.
