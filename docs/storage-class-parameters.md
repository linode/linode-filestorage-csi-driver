# 🎚 StorageClass parameters

Every parameter the driver reads is namespaced with the driver name, `linodenfs.csi.linode.com/`. Keys the driver does not recognize are ignored, so a misspelled parameter behaves as if it were never set.

## 📜 Table of Contents

1. [Summary](#-summary)
2. [space-id and space-label](#space-id-and-space-label)
3. [tags](#tags)
4. [filesystem-root-squash](#filesystem-root-squash)
5. [Parameters are immutable](#-parameters-are-immutable)
6. [Standard StorageClass fields](#-standard-storageclass-fields)

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

## `space-id` and `space-label`

Each class names exactly one pre-created NFS Storage Space, by numeric ID or by label. Filesystems for the class are created inside it. The two parameters are mutually exclusive, and one of them is required.

```yaml
parameters:
  linodenfs.csi.linode.com/space-id: "42"
  # or
  linodenfs.csi.linode.com/space-label: "my-cluster-space"
```

Quote the value. Kubernetes rejects `parameters` values that are not strings, and YAML would read an unquoted `42` as an integer.

Prefer `space-id`. It resolves in a single API call, while `space-label` costs an exact-match list on every `CreateVolume`. Labels are matched exactly, including case.

| Mistake | Error |
| --- | --- |
| Neither parameter set | `InvalidArgument: StorageClass must set either ... or ...` |
| Both set | `InvalidArgument: StorageClass parameters ... are mutually exclusive` |
| `space-id` is not a positive integer | `InvalidArgument: StorageClass parameter ... must be a positive integer` |
| No such space | `NotFound` |

## `tags`

```yaml
parameters:
  linodenfs.csi.linode.com/tags: "team-platform,env-prod"
```

Linode tags applied to every filesystem the class creates. They show up in the Cloud Manager and in API filters, which makes them the practical way to find the filesystems belonging to a cluster.

The value is split on commas and each element is whitespace-trimmed. An empty value means no tags.

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

These are the backend's own spellings, so use the underscore forms exactly. Anything else fails with `InvalidArgument: unsupported linodenfs.csi.linode.com/filesystem-root-squash value "..."`.

Omitting the parameter is not the same as setting `none`. When it is absent the driver never touches the squash setting, leaving whatever the backend defaults to.

A note on `root_squash` and container images: many images run as `root` and expect to `chown` their data directory on startup. Under `root_squash` those writes land as the anonymous user and `chown` fails with `EPERM`. Either run the workload as a non-root UID that owns the directory, or use `none`.

## 🔒 Parameters are immutable

**`parameters` cannot be changed on an existing `StorageClass`.** This is Kubernetes, not the driver: the API server rejects an update that modifies `parameters`, and likewise `provisioner`, `reclaimPolicy`, or `volumeBindingMode`. A `kubectl apply` or `kubectl edit` that touches any of them fails with an error naming the immutable field.

To change parameters, create a new `StorageClass` and point new PVCs at it. Volumes that are already provisioned keep the settings they were created with; nothing re-provisions them.

`mountOptions` is editable, but it is copied into each PV at provisioning time, so an edit only affects volumes created afterwards.

## 🧱 Standard StorageClass fields

Fields that Kubernetes itself interprets, and what they mean for this driver:

| Field | Recommendation |
| --- | --- |
| `provisioner` | Must be exactly `linodenfs.csi.linode.com` |
| `reclaimPolicy` | `Delete` or `Retain`. See [reclaim policy](./usage.md#-reclaim-policy) |
| `allowVolumeExpansion` | Set `false`. Expansion is unimplemented; `true` produces failing resize attempts |
| `volumeBindingMode` | `Immediate`. `WaitForFirstConsumer` also works but buys nothing: the driver publishes no accessible topology |
| `mountOptions` | Passed through to the NFS mount and the pod bind-mount. See [mount options](./usage.md#-mount-options) |
| `allowedTopologies` | Not useful. The driver returns no `accessible_topology` on created volumes |
| `parameters` with a `csi.storage.k8s.io/` prefix | Injected by `csi-provisioner` (PV/PVC name and namespace). `csi.storage.k8s.io/fstype` is ignored; the type is always `nfs4` |

### Filesystem labels

The filesystem label comes from the PV name that `csi-provisioner` passes as the `CreateVolume` name, lowercased and truncated to 63 bytes. The chart runs `csi-provisioner` with `--volume-name-prefix=pvc` and `--volume-name-uuid-length=16`, so a PV name is 20 characters and truncation does not happen with the chart's defaults.

## 📚 Related pages

- [Usage](./usage.md)
- [Access and Networking](./access-and-networking.md)
- [Configuration Reference](./configuration-reference.md)
- [Troubleshooting](./troubleshooting.md)
