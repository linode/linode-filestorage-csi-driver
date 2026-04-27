# Linode File Storage CSI Driver

This repository contains the initial scaffold for a Linode-managed NFS CSI driver.

The current state is intentionally skeletal:

- Go module and bootstrap wiring are in place.
- CSI identity, controller, and node services are registered.
- Linode API, mount, and validation behavior are stubbed for later implementation.

See `docs/architecture.md` for the current package layout, deployment shape, and deferred work.

## Development

- `make fmt`
- `make vet`
- `make lint`
- `make test`
- `make build`
- `make ko-build`
- `make ko-publish KO_DOCKER_REPO=docker.io/<org>/linode-filestorage-csi-driver IMAGE_VERSION=<tag>`

## Install Notes

- Raw Kustomize manifests expect the controller `LINODE_TOKEN` secret to already exist. Create `linode-api-token` in `kube-system` with a `token` key before applying `deploy/kubernetes`.
- The Helm chart can create `linode-api-token` for you from `.Values.apiToken` when `.Values.secretRef` is unset.
