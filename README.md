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
<!-- codecov badge -->
<a href="https://codecov.io/gh/linode/linode-filestorage-csi-driver" >
<img src="https://codecov.io/gh/linode/linode-filestorage-csi-driver/graph/badge.svg?token=Z2L8SN8K8R"/></a>
</p>

## Table of Contents

- [Overview](#-overview)
  - [What the driver does](#what-the-driver-does)
  - [Current limitations](#current-limitations)
- [Before you begin](#-before-you-begin)
- [Quickstart](#-quickstart)
- [Disclaimers](#-disclaimers)
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
  - [Consume it from a pod](docs/usage.md#3-consume-it-from-a-pod)
  - [Share one volume between pods](docs/usage.md#4-share-one-volume-between-pods)
  - [StorageClass parameters](docs/storage-class-parameters.md)
- [Access and Networking](docs/access-and-networking.md)
  - [VPC requirement](docs/access-and-networking.md#-vpc-requirement)
  - [Access-policy modes](docs/access-and-networking.md#-access-policy-modes)
  - [Squash policy](docs/access-and-networking.md#-squash-policy)
- [Reference](docs/configuration-reference.md)
  - [Environment variables](docs/configuration-reference.md#-driver-environment-variables)
  - [Helm values](docs/configuration-reference.md#-helm-values)
  - [Volume context keys](docs/configuration-reference.md#-volume-context-keys)
- [Troubleshooting](docs/troubleshooting.md)
- [Development](docs/development-setup.md)
  - [Prerequisites](docs/development-setup.md#-prerequisites)
  - [Local toolchain](docs/development-setup.md#-setting-up-the-local-development-environment)
  - [Development cluster](docs/development-setup.md#-creating-a-development-cluster)
  - [Testing](docs/testing.md)
  - [Contributing](.github/CONTRIBUTING.md)
- [License](#license)
- [Community](#-join-us-on-slack)

## 📚 Overview

This is the Container Storage Interface ([CSI](https://github.com/container-storage-interface/spec)) driver for **Linode managed NFS file storage**. It lets Kubernetes provision, share, and mount NFSv4 filesystems from a Linode NFS Storage Space as `PersistentVolumes`.

The volumes are network filesystems rather than block devices, so a single volume can be mounted read-write by pods on many nodes at the same time. That makes it suitable for shared caches, shared media, CI artifacts, and any workload that needs `ReadWriteMany`.

For background on Kubernetes CSI, see the [Kubernetes CSI documentation](https://kubernetes-csi.github.io/docs/introduction.html) and the [CSI specification](https://github.com/container-storage-interface/spec/).

### What the driver does

- **Provisions filesystems.** A `PersistentVolumeClaim` becomes a Linode NFS filesystem inside an NFS Storage Space that you pre-create and name in the `StorageClass`.
- **Leaves Space access to you.** The Storage Space access policy must be enabled and admit the cluster VPC. The Space owner maintains it; the driver neither validates nor changes it, so a misconfigured policy surfaces as mount failures. `accessPolicy.mode=node` enables per-filesystem Linode ACL management, but is not currently recommended because every publish and unpublish waits for an asynchronous policy update and scales poorly.
- **Mounts over NFSv4.** The node plugin mounts filesystem DNS name at the kubelet staging path, then bind-mounts it into each pod.
- **Reports usage.** `NodeGetVolumeStats` reports byte and inode usage read from the mounted filesystem.

### Current Limitations:

- **Volume expansion** is planned, but not available yet.
- **Volume cloning** is planned, through snapshots, but not available yet.
- **Snapshots and restores** are planned, but not available yet.

## 🧭 Before you begin

- The driver is in alpha. See the [disclaimers](#-disclaimers).
- Every node must be attached to a VPC with IPv6 connectivity, and all nodes must be in one region. See [Access and Networking](docs/access-and-networking.md#-vpc-requirement).
- The [Linode Cloud Controller Manager](https://github.com/linode/linode-cloud-controller-manager) must be installed and running, because the driver reads each node's `providerID` and region label.
- You create the NFS Storage Space yourself. See [Create an NFS Storage Space](docs/installation.md#-create-an-nfs-storage-space).
- The Space access policy must be enabled and list your cluster VPC. The driver only checks it, and a PVC stays `Pending` until it passes. See [Admit the cluster VPC](docs/installation.md#-admit-the-cluster-vpc-on-the-space).
- You need a Linode API token. See [Secure a Linode API access token](docs/installation.md#-secure-a-linode-api-access-token).

## ⚡ Quickstart

First create the NFS Storage Space and admit your cluster VPC on its access policy. You can do both in Cloud Manager or directly against the API with `curl`. The steps are in [Create an NFS Storage Space](docs/installation.md#-create-an-nfs-storage-space) and [Admit the cluster VPC](docs/installation.md#-admit-the-cluster-vpc-on-the-space).

Then install the driver and create a StorageClass and a claim:

```sh
helm repo add linode-nfs-csi https://linode.github.io/linode-filestorage-csi-driver/
helm install linode-nfs-csi-driver -n kube-system --set apiToken="${LINODE_API_TOKEN}" linode-nfs-csi/linode-nfs-csi-driver
kubectl apply -f - <<EOF
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: linode-nfs
provisioner: linodenfs.csi.linode.com
parameters:
  linodenfs.csi.linode.com/space-id: "${SPACE_ID}"
---
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
EOF
```

Check that the claim binds:

```sh
kubectl get pvc nfs-pvc
```

```text
NAME      STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   AGE
nfs-pvc   Bound    pvc-a1b2c3d4-5e6f-7890-abcd-ef1234567890   10Gi       RWX            linode-nfs     35s
```

Then mount `nfs-pvc` from a pod as shown in [Consume it from a pod](docs/usage.md#3-consume-it-from-a-pod). The first volume in a region can be slow because backend resources are provisioned on first use. See [Installation](docs/installation.md) and [Usage](docs/usage.md) for the full steps.

## 🚧 Disclaimers

- **The driver is in alpha.** Behavior and the interfaces it exposes may change between releases.
- **A VPC is required.** `CreateVolume` fails with `FailedPrecondition` when the cluster's nodes are not attached to a VPC. The driver requires VPC-backed IPv6 connectivity to reach mount targets. See [Access and Networking](docs/access-and-networking.md#-vpc-requirement).
- **Storage Spaces are not created by the driver.** You must create the NFS Storage Space yourself and reference it from every `StorageClass`. The driver creates and deletes filesystems inside that space, never the space itself.
- **Capacity requests are advisory.** The Linode NFS API does not take a size on filesystem creation. The driver echoes the requested capacity back in the CSI response so Kubernetes can bind the PVC, but it does not enforce a quota. Do not treat `spec.resources.requests.storage` as a hard limit.

## 💬 Join Us on Slack

- **General Help/Discussion**: [Kubernetes Slack - #linode](https://kubernetes.slack.com/messages/CD4B15LUR)
- **Development/Debugging**: [Gopher's Slack - #linodego](https://gophers.slack.com/messages/CAG93EB2S)

## License

This project is licensed under the Apache License, Version 2.0.
