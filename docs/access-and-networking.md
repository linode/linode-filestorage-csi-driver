# 🌐 Access and Networking

Reaching an NFS export is not like attaching a block device. The path from a pod to its data crosses a VPC and two separate access policies. This page covers each layer, who owns it, and what happens when it is wrong.

## 📜 Table of Contents

1. [The data path](#-the-data-path)
2. [VPC requirement](#-vpc-requirement)
3. [Access-policy modes](#-access-policy-modes)
4. [Squash policy](#-squash-policy)
5. [Node identity](#-node-identity)
6. [Firewalls and ports](#-firewalls-and-ports)
7. [Security notes](#-security-notes)

## 🛣 The data path

```text
  pod
   │  bind mount (NodePublishVolume)
   ▼
  /var/lib/kubelet/pods/<uid>/volumes/kubernetes.io~csi/<pv>/mount
   │
   │  one NFSv4 mount per node (NodeStageVolume)
   ▼
  /var/lib/kubelet/plugins/kubernetes.io/csi/<driver>/<hash>/globalmount
   │
   │  nfs4
   ▼
  filesystem DNS name  ── resolves to an address inside the VPC
   │
   ▼
  Linode NFS filesystem
      guarded by: space access policy   (VPC ACL)
                  filesystem squash policy
                  filesystem Linode ACL (node mode only)
```

The selected access-policy mode determines whether the Space access policy is the only network authorization layer or whether each filesystem also restricts access to published Linodes.

## 🔒 VPC requirement

**Every node needs VPC-backed IPv6 connectivity to the NFS mount targets.** This is a networking requirement, not a provisioning precondition checked by the driver.

The controller discovers only the workload region, from one Kubernetes Node on the first successful `CreateVolume`, and reuses it for the process lifetime.

Check each node pool's VPC and subnet assignment in Cloud Manager, and compare them with the Space access policy. A missing VPC, an uncovered subnet, or an incorrect customer ACL surfaces as an error when the node mounts the volume.

## 🚧 Access-policy modes

The Helm value `accessPolicy.mode` selects one installation-wide authorization model:

| Mode | Space access policy | Filesystem Linode ACL | CSI attachment |
| --- | --- | --- | --- |
| `space` (default) | owner-managed; not validated or changed | disabled | `attachRequired: false`; no `VolumeAttachment` objects |
| `node` | owner-managed; not validated or changed | maintained | `attachRequired: true`; publish and unpublish authorize nodes |

`node` mode is not currently recommended. Every publish and unpublish performs an asynchronous filesystem policy replacement and waits for it to become active. Those operations serialize for a volume, causing severe delays as the number of nodes grows. In performance testing, ten pods mounting one existing RWX volume across ten nodes reached Ready in 4.73 seconds with `space` mode versus 874.99 seconds with `node` mode. Removing them took 1.24 seconds with `space` mode versus 1,135.13 seconds with `node` mode. These are point-in-time measurements rather than service guarantees, but the per-node policy waits are inherent to the current implementation.

### Space access policy: the VPC ACL

Scope: the whole Storage Space, and therefore every filesystem in it.

The Space owner configures and maintains this policy in Cloud Manager, through the API, Terraform, or another management tool. For volumes to mount, the policy must be:

- **Enabled** (`enabled: true`).
- **Active** (`status: active`). Requested VPC entries can be visible while an update is still being applied.
- **Admitting the cluster VPC**, with the cluster VPC in `vpc_acl` and covering the subnets used by every node pool.

The driver does not validate any of this. `CreateVolume` reads the policy only to learn its mTLS mode, so it still needs read access, but it provisions regardless of whether the policy is enabled, active, or admits the cluster VPC. A misconfigured policy therefore does not leave the PVC `Pending`. The PVC binds, and the failure surfaces later when a pod mounts the volume, as a `mount.nfs4` timeout or `access denied by server` from `NodeStageVolume`. See [Mount times out or is denied](./troubleshooting.md#mount-times-out-or-is-denied).

Check the policy before creating PVCs, and again whenever a mount fails:

```sh
curl -sS -H "Authorization: Bearer ${LINODE_API_TOKEN}" \
  https://api.linode.com/v4beta/nfs/spaces/42/access-policy
```

Provisioning and uninstalling the driver never add or remove VPC entries. If the policy changes after a volume is provisioned, mounts that were working can start failing. After the Space owner corrects the policy and it returns to `active`, the kubelet's mount retries succeed without changes on the Kubernetes side.

In `space` mode, the controller does not advertise `PUBLISH_UNPUBLISH_VOLUME` and the `CSIDriver` has `attachRequired: false`. Kubernetes mounts a scheduled volume without creating a `VolumeAttachment`. When the StorageClass explicitly sets `filesystem-root-squash`, provisioning enables the filesystem access policy with an empty Linode ACL so the squash setting takes effect. The empty ACL uses Space-level VPC admission, without adding node IDs. When the parameter is omitted or blank, the driver leaves the filesystem policy unchanged.

### Filesystem access policy: the Linode ACL

Scope: one filesystem. The node ACL is maintained only in `node` mode; the squash setting is configurable in both modes.

`ControllerPublishVolume` and `ControllerUnpublishVolume` maintain the ACL. There is no block device to attach; "attach" means "add this node's Linode to the ACL".

**On publish:**

| State | Action |
| --- | --- |
| Linode already in the ACL and the policy is enabled | Return success immediately, no API write |
| Linode already in the ACL but the policy is disabled | Rewrite the policy with `enabled: true`, then wait for active |
| Linode not in the ACL | Append it, set `enabled: true`, then wait for active |

**On unpublish:**

| State | Action |
| --- | --- |
| Empty `node_id` | Return success (nothing to revoke) |
| Filesystem already deleted (`404`) | Return success, so the detach does not block PV cleanup |
| Linode not in the ACL | Return success, no API write |
| Linode in the ACL | Remove it, preserve the current `enabled` value, then wait for active |

Both operations preserve the policy's label, squash policy, and protocol list, because the backend's update endpoint is a full replace and dropping a field would reset it.

Publish **enables** the policy but unpublish does not disable it. A filesystem whose last node has detached keeps an enabled policy with an empty ACL. **An empty ACL uses Space-level VPC admission, so it must not be treated as a deny-all policy.**

In `node` mode, `ControllerGetVolume` surfaces the Linode IDs currently in the ACL as published node IDs. That status can disagree with reality if someone edits the ACL by hand.

## 🎭 Squash policy

Squashing lives on the **filesystem** access policy, and is the only part of it that the `StorageClass` controls, via `linodenfs.csi.linode.com/filesystem-root-squash`.

| Value | Effect on incoming UIDs |
| --- | --- |
| `none` | Nothing is remapped; a client's `root` is `root` on the export |
| `root_squash` | UID/GID 0 becomes the anonymous user; other UIDs pass through |
| `all_squash` | Every UID/GID becomes the anonymous user |

Because NFS trusts the UID the client sends, and any pod with the volume mounted can present any UID, squashing is the mechanism that keeps a container's `root` from being the export's `root`. `root_squash` is the safer default for multi-tenant clusters.

The practical friction: images that run as `root` and `chown` their data directory on startup get `EPERM` under `root_squash`. Either run as a non-root UID that already owns the directory, or use `none` and accept that any pod mounting the volume has root over its contents.

When the parameter is omitted the driver never touches the setting, leaving the backend default. See [StorageClass parameters](./storage-class-parameters.md#filesystem-root-squash).

## 🪪 Node identity

The node plugin supplies its Linode ID as the CSI node ID. In node access-policy mode, the controller uses that ID for filesystem ACL entries.

**Node plugin, resolving itself:** it tries the [Linode instance metadata service](https://www.linode.com/docs/products/compute/compute-instances/guides/metadata/) (link-local, no credentials) and falls back to a Kubernetes node lookup by name if the metadata service is unreachable. This is why the node plugin's RBAC needs `get`, `list`, and `watch` on nodes, and why it needs no API token.

**Controller:** publish and unpublish parse the CSI request's `NodeId` as a Linode ID; they do not look up individual Kubernetes Nodes. `CreateVolume` discovers only the workload region, as described above.

The node plugin's Kubernetes fallback requires the node object to carry:

| Field | Failure when missing |
| --- | --- |
| `spec.providerID` as `linode://<id>` | Node identity resolution fails |
| `topology.kubernetes.io/region` or the beta label | `node region label not found` |
| An `InternalIP` or `ExternalIP` in `status.addresses` | `node allowlist IP not found` |

The Linode Cloud Controller Manager supplies these fields. If the node plugin falls back to Kubernetes before the CCM has populated them, node identity resolution fails until a later retry.

The IP address is resolved but not currently used for ACL entries; the filesystem ACL is keyed on Linode ID. It is still required, so a node with no addresses reported fails metadata resolution.

## 🔌 Firewalls and ports

The node needs outbound NFSv4 to the mount target inside the VPC:

| Direction | Protocol | Port | Purpose |
| --- | --- | --- | --- |
| Node → mount target | TCP | 2049 | NFSv4. This is the only port NFSv4 needs; no portmapper, no separate mountd |
| Node → `api.linode.com` | TCP | 443 | Controller only, for the Linode API |
| Node → link-local metadata | TCP | 80 | Node plugin only, optional; falls back to the Kubernetes API |

If you run a Linode Cloud Firewall or in-cluster egress policy, TCP 2049 to the VPC has to be permitted. A blocked port produces a `NodeStageVolume` that hangs and then fails with a `mount.nfs4` timeout, which looks identical to an access policy problem in the kubelet's logs. Check the Space access policy first, since it is cheaper to verify.

DNS matters too: the filesystem's DNS name has to resolve from the node, using the host's resolver rather than the cluster's. The node plugin runs with `hostNetwork: true` and mounts in the host mount namespace, so `/etc/resolv.conf` on the node is what counts, not CoreDNS.

## 🛡 Security notes

- **The API token is controller-only.** It is mounted from a Secret into the controller Deployment as `LINODE_TOKEN`. The node DaemonSet receives no token and makes no Linode API calls, which limits the blast radius of a compromised node plugin to the mounts already on that node.
- **The node plugin is privileged.** `privileged: true`, `runAsUser: 0`, `hostNetwork: true`, and a `Bidirectional` mount of the kubelet pods directory. It has to be: mounting in the host namespace is the job, and creating the socket under the kubelet hostPath needs root. Treat the node DaemonSet's image and RBAC with the seriousness that implies.
- **Any pod that can mount the PVC can read all of it.** There is no per-pod path restriction; the whole filesystem is exported and bind-mounted. Isolate tenants with separate PVCs, and therefore separate filesystems, rather than separate directories in one volume.
- **Token rotation requires a restart.** `LINODE_TOKEN` is read from the environment at startup, so replacing the Secret's contents has no effect until the controller pod restarts.
- **The ACL is shared state.** The driver rewrites the whole filesystem access policy on each publish, so a manual edit made between a read and a write is lost. Do not hand-manage the Linode ACL on filesystems the driver owns.

## 📚 Related pages

- [Architecture](./architecture.md)
- [StorageClass parameters](./storage-class-parameters.md)
- [Configuration Reference](./configuration-reference.md)
- [Troubleshooting](./troubleshooting.md)
