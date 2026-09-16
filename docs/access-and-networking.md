# 🌐 Access and Networking

Reaching an NFS export is not like attaching a block device. The path from a pod to its data crosses a VPC, two separate access policies, and optionally a mutual-TLS transport. This page covers each layer, who owns it, and what happens when it is wrong.

## 📜 Table of Contents

1. [The data path](#-the-data-path)
2. [VPC requirement](#-vpc-requirement)
3. [Two layers of access policy](#-two-layers-of-access-policy)
4. [mTLS](#-mtls)
5. [Squash policy](#-squash-policy)
6. [Node identity](#-node-identity)
7. [Firewalls and ports](#-firewalls-and-ports)
8. [Security notes](#-security-notes)

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
   │  nfs4, optionally xprtsec=mtls
   ▼
  mount_target_fqdn  ── resolves to an address inside the VPC
   │
   ▼
  Linode NFS filesystem
      guarded by: space access policy   (VPC ACL, mTLS mode)
                  filesystem access policy (Linode ACL, squash policy)
```

Two independent guards sit in front of the data. A request has to satisfy both: the **space** policy has to admit the VPC, and the **filesystem** policy has to admit the specific Linode. The driver maintains one entry in each.

## 🔒 VPC requirement

**Every node must be attached to a VPC.** This is a hard precondition, checked on every `CreateVolume`:

```text
FailedPrecondition: this driver requires VPC-backed IPv6 connectivity; cluster VPC not found
```

The driver resolves the cluster VPC from the Linode API rather than from Kubernetes:

1. List all nodes. Read `topology.kubernetes.io/region` (or the beta label) from each and require them all to agree, otherwise `cluster has multiple regions: "us-ord" and "us-east"`.
2. Take the **first** node, parse its Linode ID out of `spec.providerID` (`linode://12345`).
3. Fetch that Linode and branch on its `interface_generation`:
   - **`legacy_config`** (LKE and CAPI nodes today): list the instance's config profiles and look for an active interface with `purpose: vpc`.
   - anything else: list the Linode's interfaces and look for one carrying a VPC.
4. If the Linode is attached to more than one VPC, fail rather than guess.
5. If no VPC is found, fail with the `FailedPrecondition` above.

Two consequences worth internalizing:

- **The first node decides.** The whole cluster's VPC is inferred from one node. A cluster whose nodes live in different VPCs will provision volumes reachable from only some of them, and the failure shows up later as a mount timeout, not as a provisioning error.
- **Region agreement is checked across all nodes, VPC membership is not.** Adding a node pool outside the VPC will not fail provisioning; it will fail mounting.

Checking your own cluster:

```sh
kubectl get nodes -o custom-columns=\
NAME:.metadata.name,\
PROVIDER:.spec.providerID,\
REGION:'.metadata.labels.topology\.kubernetes\.io/region'

# then, for one Linode ID from above
curl -sS -H "Authorization: Bearer ${LINODE_API_TOKEN}" \
  https://api.linode.com/v4/linode/instances/12345/configs
```

Look for an interface with `"purpose": "vpc"` and a non-null `vpc_id`.

## 🚧 Two layers of access policy

### Space access policy: the VPC ACL

Scope: the whole Storage Space, and therefore every filesystem in it.

The driver adds the cluster VPC to this policy the first time it provisions a volume in the space, inside `CreateVolume`:

- Read the current policy. If the VPC is already in `vpc_acl`, do nothing.
- Otherwise rewrite the policy with the existing VPC entries plus the cluster VPC, preserving each entry's label, enabled flag, subnets, and mTLS mode.
- Wait up to 5 minutes for the policy to go `active`.

The VPC is added **without subnet restrictions**, meaning all subnets of that VPC are admitted. If you need subnet-level narrowing, set it yourself in the Cloud Manager; the driver preserves subnets it did not create, so a manual narrowing survives later provisioning as long as the VPC entry stays present.

```sh
curl -sS -H "Authorization: Bearer ${LINODE_API_TOKEN}" \
  https://api.linode.com/v4beta/nfs/spaces/42/access-policy
```

The driver never removes a VPC from this policy, including at uninstall. Decommissioning a cluster leaves a stale VPC entry behind for you to clean up.

### Filesystem access policy: the Linode ACL

Scope: one filesystem.

This is what `ControllerPublishVolume` and `ControllerUnpublishVolume` actually do. There is no block device to attach; "attach" means "add this node's Linode to the ACL".

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

Note that publish **enables** the policy but unpublish does not disable it. A filesystem whose last node has detached keeps an enabled policy with an empty ACL, which admits nothing but leaves the switch on.

`ControllerGetVolume` surfaces this state to Kubernetes: the volume status lists the Linode IDs currently in the ACL as published node IDs, which is how a `kubectl describe pv` can disagree with reality if someone edited the ACL by hand.

## 🔐 mTLS

Mutual TLS is a property of the **Storage Space access policy**, not of the driver or the `StorageClass`. Set it on the space; the driver reads it and adapts.

`CreateVolume` reads the space policy's mTLS mode and puts it in the volume context under `mtls-mode`, which lands in the PV's `volumeAttributes` and comes back to the node plugin on every stage.

| `mtls-mode` | `NodeStageVolume` behavior |
| --- | --- |
| `required` | Appends `xprtsec=mtls` to the mount flags. A failure is a failure |
| `optional` | Tries the mount **with** `xprtsec=mtls` first; on any error, retries without it |
| `disabled` | Plain NFSv4, no `xprtsec` flag |
| unset / empty | Same as `disabled`. The flag is not added |
| anything else | `InvalidArgument: invalid mtls-mode value, must be one of: required, optional, disabled` |

Do not add `xprtsec=mtls` to `mountOptions` yourself. With `required` you would get it twice, and with `disabled` you would get a mount the backend rejects; either way you have taken the decision away from the code that knows the space's actual setting.

Two caveats on `optional`:

- The fallback triggers on **any** mount error, not specifically on an mTLS negotiation failure. A transient network problem during the first attempt silently produces a non-mTLS mount. This is a known rough edge, marked with a `TODO` in the source pending better error typing from the NFS service.
- `mtls-mode` is captured at provisioning time. Changing the space's mTLS mode afterwards does **not** update existing PVs' `volumeAttributes`, so already-provisioned volumes keep staging with the old mode until the PV is recreated. If you tighten a space to `required`, expect existing volumes to keep mounting without mTLS.

`xprtsec=mtls` requires client-side support: an NFS client and kernel that implement RPC-with-TLS, plus a running `tlshd`. The driver image bundles the NFS userspace tooling, but transport security is negotiated by the host kernel, so a host without it will fail a `required` mount with an error from `mount.nfs4` rather than from the driver.

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

Everything above hinges on mapping a Kubernetes node to a Linode ID, which the driver does in two ways.

**Node plugin, resolving itself:** it tries the [Linode instance metadata service](https://www.linode.com/docs/products/compute/compute-instances/guides/metadata/) (link-local, no credentials) and falls back to a Kubernetes node lookup by name if the metadata service is unreachable. This is why the node plugin's RBAC needs `get`, `list`, and `watch` on nodes, and why it needs no API token.

**Controller, resolving any node:** always a Kubernetes node lookup, parsing `spec.providerID`.

Both paths require the node object to carry:

| Field | Failure when missing |
| --- | --- |
| `spec.providerID` as `linode://<id>` | `node "..." : invalid provider ID` and provisioning fails |
| `topology.kubernetes.io/region` or the beta label | `node region label not found` |
| An `InternalIP` or `ExternalIP` in `status.addresses` | `node allowlist IP not found` |

All three come from the Linode Cloud Controller Manager, which is why the CCM is a hard requirement. A node whose `providerID` is still empty because the CCM has not processed it yet will make `CreateVolume` fail; it recovers on retry once the CCM catches up.

The IP address is resolved but not currently used for ACL entries; the filesystem ACL is keyed on Linode ID. It is still required, so a node with no addresses reported fails metadata resolution.

## 🔌 Firewalls and ports

The node needs outbound NFSv4 to the mount target inside the VPC:

| Direction | Protocol | Port | Purpose |
| --- | --- | --- | --- |
| Node → mount target | TCP | 2049 | NFSv4. This is the only port NFSv4 needs; no portmapper, no separate mountd |
| Node → `api.linode.com` | TCP | 443 | Controller only, for the Linode API |
| Node → link-local metadata | TCP | 80 | Node plugin only, optional; falls back to the Kubernetes API |

If you run a Linode Cloud Firewall or in-cluster egress policy, TCP 2049 to the VPC has to be permitted. A blocked port produces a `NodeStageVolume` that hangs and then fails with a `mount.nfs4` timeout, which looks identical to an ACL problem in the kubelet's logs. Check the ACL first, since it is cheaper to verify.

DNS matters too: `mount_target_fqdn` has to resolve from the node, using the host's resolver rather than the cluster's. The node plugin runs with `hostNetwork: true` and mounts in the host mount namespace, so `/etc/resolv.conf` on the node is what counts, not CoreDNS.

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
