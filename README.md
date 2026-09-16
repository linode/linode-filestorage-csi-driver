# 📁 Linode File Storage CSI Driver

<p align="center">
<!-- go doc / reference card -->
<a href="https://pkg.go.dev/github.com/linode/linode-filestorage-csi-driver">
<img src="https://pkg.go.dev/badge/github.com/linode/linode-filestorage-csi-driver.svg"></a>
<!-- join kubernetes slack channel for linode -->
<a href="https://kubernetes.slack.com/messages/CD4B15LUR">
<img src="https://img.shields.io/badge/join%20slack-%23linode-brightgreen"></a>
<!-- PRs welcome -->
<a href="http://makeapullrequest.com">
<img src="https://img.shields.io/badge/PRs-welcome-brightgreen.svg"></a>
</p>
<p align="center">
<!-- go build / test CI -->
<a href="https://github.com/linode/linode-filestorage-csi-driver/actions/workflows/ci.yml">
<img src="https://github.com/linode/linode-filestorage-csi-driver/actions/workflows/ci.yml/badge.svg"></a>
<!-- release CI -->
<a href="https://github.com/linode/linode-filestorage-csi-driver/actions/workflows/release.yml">
<img src="https://github.com/linode/linode-filestorage-csi-driver/actions/workflows/release.yml/badge.svg"></a>
</p>

## Table of Contents

- [Overview](#-overview)
  - [What the driver does](#what-the-driver-does)
  - [What it does not do yet](#what-it-does-not-do-yet)
- [Architecture](docs/architecture.md)
  - [Component layout](docs/architecture.md#-component-layout)
  - [Volume and snapshot identity](docs/architecture.md#-volume-and-snapshot-identity)
  - [Provisioning lifecycle](docs/architecture.md#-provisioning-lifecycle)
  - [Mount lifecycle](docs/architecture.md#-mount-lifecycle)
- [Installation](docs/installation.md)
  - [Requirements](docs/installation.md#-requirements)
  - [Secure a Linode API access token](docs/installation.md#-secure-a-linode-api-access-token)
  - [Install with Helm](docs/installation.md#1-using-helm)
  - [Install with kubectl](docs/installation.md#2-using-kubectl)
  - [Upgrade and uninstall](docs/installation.md#-upgrading-the-driver)
- [Usage](docs/usage.md)
  - [Create a StorageClass](docs/usage.md#1-create-a-storageclass)
  - [Create a PersistentVolumeClaim](docs/usage.md#2-create-a-persistentvolumeclaim)
  - [Share one volume between pods](docs/usage.md#4-share-one-volume-between-pods)
  - [StorageClass parameters](docs/storage-class-parameters.md)
  - [Snapshots and restore](docs/snapshots.md)
- [Access and Networking](docs/access-and-networking.md)
  - [VPC requirement](docs/access-and-networking.md#-vpc-requirement)
  - [Access policies](docs/access-and-networking.md#-two-layers-of-access-policy)
  - [mTLS](docs/access-and-networking.md#-mtls)
  - [Squash policy](docs/access-and-networking.md#-squash-policy)
- [Reference](docs/configuration-reference.md)
  - [Environment variables](docs/configuration-reference.md#-driver-environment-variables)
  - [Helm values](docs/configuration-reference.md#-helm-values)
  - [Volume context keys](docs/configuration-reference.md#-volume-context-keys)
  - [CSI RPC support matrix](docs/csi-rpc-reference.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Development](docs/development-setup.md)
  - [Prerequisites](docs/development-setup.md#-prerequisites)
  - [Local toolchain](docs/development-setup.md#-setting-up-the-local-development-environment)
  - [Development cluster](docs/development-setup.md#-creating-a-development-cluster)
  - [Testing](docs/testing.md)
  - [Contributing](docs/contributing.md)
- [License](#license)
- [Disclaimers](#-disclaimers)
- [Community](#-join-us-on-slack)

## 📚 Overview

This is the Container Storage Interface ([CSI](https://github.com/container-storage-interface/spec)) driver for **Linode managed NFS file storage**. It lets Kubernetes provision, share, snapshot, and mount NFSv4 filesystems from a Linode NFS Storage Space as `PersistentVolumes`.

The volumes are network filesystems rather than block devices, so a single volume can be mounted read-write by pods on many nodes at the same time. That makes it suitable for shared caches, shared media, CI artifacts, and any workload that needs `ReadWriteMany`.

For background on Kubernetes CSI, see the [Kubernetes CSI documentation](https://kubernetes-csi.github.io/docs/introduction.html) and the [CSI specification](https://github.com/container-storage-interface/spec/).

### What the driver does

- **Provisions filesystems.** A `PersistentVolumeClaim` becomes a Linode NFS filesystem inside an NFS Storage Space that you pre-create and name in the `StorageClass`.
- **Authorizes nodes.** Before a pod can mount a volume, the controller adds the node's Linode to the filesystem's Linode ACL and waits for that access policy to go active.
- **Attaches the cluster VPC.** The controller resolves the cluster's VPC from the Linode API and adds it to the Storage Space access policy, because the driver requires VPC-backed connectivity to the mount target.
- **Mounts over NFSv4.** The node plugin mounts filesystem DNS name at the kubelet staging path, then bind-mounts it into each pod.
- **Snapshots and restores.** `VolumeSnapshot` objects map to Linode NFS snapshots, and a new PVC can be restored from a snapshot by cloning it into a filesystem.
- **Reports usage.** `NodeGetVolumeStats` reports byte and inode usage read from the mounted filesystem.

### Current Limitations:

- **Volume expansion** is currently not supported.
- **Capacity reporting.** `GetCapacity` returns `Unimplemented`.
- **Volume listing.** `ListVolumes` is intentionally unimplemented and the capability is not advertised. See [the reasoning](docs/csi-rpc-reference.md#listvolumes-is-intentionally-unimplemented).
- **Volume cloning:** cloning happens only through snapshot restore.

See the [CSI RPC support matrix](docs/csi-rpc-reference.md) for the full, per-RPC picture.

## 🚧 Disclaimers

- **The driver is pre-1.0.** The Helm chart version is `0.0.1`, the API surface it targets is `v4beta`, and behavior may change between releases.
- **A VPC is required.** `CreateVolume` fails with `FailedPrecondition` when the cluster's nodes are not attached to a VPC. The driver requires VPC-backed IPv6 connectivity to reach mount targets. See [Access and Networking](docs/access-and-networking.md#-vpc-requirement).
- **Storage Spaces are not created by the driver.** You must create the NFS Storage Space yourself and reference it from every `StorageClass`. The driver creates and deletes filesystems inside that space, never the space itself.
- **Capacity requests are advisory.** The Linode NFS API does not take a size on filesystem creation. The driver echoes the requested capacity back in the CSI response so Kubernetes can bind the PVC, but it does not enforce a quota. Do not treat `spec.resources.requests.storage` as a hard limit.
- **Volume IDs are composite.** A volume handle is `{space_id}/{filesystem_id}`, and a snapshot handle is `{space_id}/{filesystem_id}/{snapshot_id}`. Do not assume a bare integer.
- **Images are `linux/amd64` only.** Akamai/LKE is amd64, and `PLATFORM` defaults to `linux/amd64`.

## 💬 Join Us on Slack

- **General Help/Discussion**: [Kubernetes Slack - #linode](https://kubernetes.slack.com/messages/CD4B15LUR)
- **Development/Debugging**: [Gopher's Slack - #linodego](https://gophers.slack.com/messages/CAG93EB2S)

## License

This project is licensed under the Apache License, Version 2.0.
