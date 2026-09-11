---
nav_order: 5
---

# 🎚 StorageClass parameters

Every parameter the driver reads is namespaced with the driver name, `linodenfs.csi.linode.com/`. Unrecognized keys are ignored, so a typo in a parameter name fails silently as far as the driver is concerned; the `StorageClass` will simply behave as if you never set it.

## 📜 Table of Contents

1. [Summary](#-summary)
2. [space-id](#space-id)
3. [space-label](#space-label)
4. [tags](#tags)
5. [filesystem-root-squash](#filesystem-root-squash)
6. [Idempotency and immutability](#-idempotency-and-immutability)
7. [Standard StorageClass fields](#-standard-storageclass-fields)

## 📋 Summary

| Parameter | Required | Values | Default |
| --- | --- | --- | --- |
| `linodenfs.csi.linode.com/space-id` | One of the two | Positive integer as a string | none |
| `linodenfs.csi.linode.com/space-label` | One of the two | Exact label of an existing Storage Space | none |
| `linodenfs.csi.linode.com/tags` | No | Comma-separated list | no tags |
| `linodenfs.csi.linode.com/filesystem-root-squash` | No | `none`, `root_squash`, `all_squash` | backend default, left untouched |

A complete example using every parameter:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: linode-nfs-shared
provisioner: linodenfs.csi.linode.com
parameters:
  linodenfs.csi.linode.com/space-id: "42"
  linodenfs.csi.linode.com/tags: "team-platform,env-prod"
  linodenfs.csi.linode.com/filesystem-root-squash: "root_squash"
reclaimPolicy: Delete
allowVolumeExpansion: false
volumeBindingMode: Immediate
mountOptions:
  - hard
  - noatime
```

## `space-id`

```yaml
parameters:
  linodenfs.csi.linode.com/space-id: "42"
```

The numeric ID of a pre-created NFS Storage Space. Filesystems for this class are created inside it.

- Must parse as an integer greater than zero. `"0"`, `"-1"`, `"abc"`, and `"42x"` all fail with `InvalidArgument: StorageClass parameter linodenfs.csi.linode.com/space-id "..." must be a positive integer`.
- Surrounding whitespace is trimmed, so `" 42 "` is accepted.
- Quote it. YAML would otherwise parse `42` as an integer and Kubernetes rejects non-string parameter values.
- The driver calls `GetNFSSpace` to confirm the space exists. A missing space fails with `NotFound`.

Prefer this over `space-label`: it resolves in one API call and cannot become ambiguous.

## `space-label`

```yaml
parameters:
  linodenfs.csi.linode.com/space-label: "my-cluster-space"
```

The label of a pre-created Storage Space, resolved by an exact-match list at every `CreateVolume`.

| Matches found | Result |
| --- | --- |
| 0 | `NotFound: NFS space with label "..." was not found` |
| 1 | Used |
| 2 or more | `FailedPrecondition: multiple NFS spaces match label "..."` |

Labels are not unique on a Linode account, which is why the multi-match case exists. If you create a second space with the same label later, provisioning for this class breaks until you disambiguate.

`space-id` and `space-label` are **mutually exclusive**. Setting both fails with `InvalidArgument: StorageClass parameters ... are mutually exclusive`; setting neither fails with `InvalidArgument: StorageClass must set either ... or ... for a pre-created NFS Storage Space`.

## `tags`

```yaml
parameters:
  linodenfs.csi.linode.com/tags: "team-platform,env-prod,csi"
```

Linode tags applied to each filesystem this class creates. They show up in the Cloud Manager and in API filters, which makes them the practical way to find the filesystems belonging to a cluster.

Parsing rules:

- Split on `,`.
- Each element is whitespace-trimmed.
- Empty elements are dropped, so `"a,,b"` and `"a, b"` both produce `["a", "b"]`.
- An empty or whitespace-only value produces no tags at all, which is distinct from an empty list: the driver omits the field from the create request rather than sending `[]`.

**Order matters for idempotency.** The retry path compares the existing filesystem's tags to the parameter with an ordered comparison, so `"a,b"` and `"b,a"` are not interchangeable if a retry lands on a filesystem created with the other ordering. In practice this only bites if you edit the parameter on a `StorageClass`, which brings us to the next section.

## `filesystem-root-squash`

```yaml
parameters:
  linodenfs.csi.linode.com/filesystem-root-squash: "root_squash"
```

Sets the squash policy on the new filesystem's access policy. Squashing remaps incoming UIDs so a client's `root` does not get `root` on the export.

| Value | Effect |
| --- | --- |
| `none` | No remapping. A client's `root` is `root` on the export |
| `root_squash` | UID/GID 0 is remapped to an anonymous user; other UIDs pass through |
| `all_squash` | Every UID/GID is remapped to the anonymous user |

Any other value fails with `InvalidArgument: unsupported linodenfs.csi.linode.com/filesystem-root-squash value "..."`. The values are the backend's own spelling, so use the underscore forms exactly.

Omitting the parameter is not the same as setting `none`. When the parameter is absent the driver never touches the access policy's squash setting, leaving whatever the backend defaults to. When it is present, the driver reads the filesystem access policy after creation, and if the current policy differs it issues an update and waits for the policy to go active again. This costs one extra API round trip plus a wait, per volume.

A note on `root_squash` and container images: many images run as `root` and expect to be able to `chown` their data directory. With `root_squash`, those writes land as the anonymous user and `chown` fails with `EPERM`. Either run the workload as a non-root UID that owns the directory, or use `none`.

## 🔁 Idempotency and immutability

`CreateVolume` must be idempotent, so before creating anything the driver looks for a filesystem in the target space with the same label and region. If it finds one, it treats the call as a retry, and then checks that the existing filesystem actually matches what was asked for:

| Mismatch | Error |
| --- | --- |
| Different tags | `AlreadyExists: NFS filesystem "..." already exists with incompatible tags` |
| Different squash policy (only checked when the parameter is set) | `AlreadyExists: NFS filesystem "..." already exists with incompatible root squash policy` |
| Different label, region, or space | `AlreadyExists: NFS filesystem "..." already exists with incompatible parameters` |
| Snapshot restore whose source snapshot does not match | `AlreadyExists: NFS filesystem ... was cloned from snapshot ..., requested snapshot ...` |
| Two filesystems share the label in one space | `FailedPrecondition: multiple NFS filesystems match label "..." in space ...` |

The consequence: **parameters are effectively immutable for existing volumes.** Kubernetes does not re-provision when you edit a `StorageClass`, and it does not version the parameters that a bound PV was created with. Editing `tags` or `filesystem-root-squash` in place affects only volumes created afterwards, with one sharp edge: if a `CreateVolume` retry for an in-flight volume arrives after you edited the class, the retry compares against the new parameters and fails with `AlreadyExists`.

Create a new `StorageClass` instead of editing a live one.

## 🧱 Standard StorageClass fields

Fields that Kubernetes itself interprets, and what they mean for this driver:

| Field | Recommendation |
| --- | --- |
| `provisioner` | Must be exactly `linodenfs.csi.linode.com` |
| `reclaimPolicy` | `Delete` or `Retain`. See [reclaim policy](./usage.md#-reclaim-policy) |
| `allowVolumeExpansion` | Set `false`. Expansion is unimplemented; `true` produces failing resize attempts |
| `volumeBindingMode` | `Immediate` is fine. `WaitForFirstConsumer` also works but buys nothing: the driver publishes no accessible topology, so there is no topology for the scheduler to satisfy |
| `mountOptions` | Passed through to the NFS mount and the pod bind-mount. See [mount options](./usage.md#-mount-options) |
| `allowedTopologies` | Not useful. The driver returns no `accessible_topology` on created volumes |
| `parameters` with a `csi.storage.k8s.io/` prefix | Injected by `csi-provisioner` (PV/PVC name and namespace) and read only for the label. `csi.storage.k8s.io/fstype` is ignored; the type is always `nfs4` |

### Filesystem labels

The filesystem label is derived from the PV name that `csi-provisioner` passes as the `CreateVolume` name, lowercased and truncated to 63 bytes with any trailing hyphens stripped.

The chart runs `csi-provisioner` with `--volume-name-prefix=pvc` and `--volume-name-uuid-length=16`, so a PV name is `pvc-` plus 16 hex characters, 20 in total. That is well inside 63 bytes, so truncation never triggers with the chart's defaults. It becomes reachable only if you override those flags or provision with a different `--volume-name-prefix`, and truncation is what makes two long names able to collide inside one space, which is why the driver strips trailing hyphens rather than leaving a label ending in `-`.

## 📚 Related pages

- [Usage](./usage.md)
- [Access and Networking](./access-and-networking.md)
- [Configuration Reference](./configuration-reference.md)
- [Troubleshooting](./troubleshooting.md)
