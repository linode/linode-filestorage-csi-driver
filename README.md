# Linode File Storage CSI Driver

This repository contains the initial scaffold for a Linode-managed NFS CSI driver.

The current state is intentionally skeletal:

- Go module and bootstrap wiring are in place.
- CSI identity, controller, and node services are registered.
- Linode API, mount, and validation behavior are stubbed for later implementation.

See `docs/architecture.md` for the current package layout, deployment shape, and deferred work.

## Local Setup

Local development in this repo uses [mise](https://mise.jdx.dev/) to install and run the pinned toolchain from `mise.toml`.

Install `mise` before working on the repo, then run `mise install` once from the repository root to provision the required tools.

## Development

Install the repo toolchain once with `mise install`, then run tasks with `mise run`.

- `mise run fmt`
- `mise run vet`
- `mise run lint`
- `mise run test`
- `mise run build`
- `mise run image-build`
- `IMAGE_REPO=docker.io/<org>/linode-filestorage-csi-driver IMAGE_VERSION=<tag> mise run image-push`

## Install Notes

- Raw Kustomize manifests expect the controller `LINODE_TOKEN` secret to already exist. Create `linode-api-token` in `kube-system` with a `token` key before applying `deploy/kubernetes`.
- The Helm chart can create `linode-api-token` for you from `.Values.apiToken` when `.Values.secretRef` is unset.
