# 🤝 Contributing

First off, thank you for taking the time to contribute.

This page mirrors [`.github/CONTRIBUTING.md`](https://github.com/linode/linode-filestorage-csi-driver/blob/main/.github/CONTRIBUTING.md) and adds the parts specific to this driver. The file in `.github/` is what GitHub shows contributors; if the two ever disagree, that one wins.

## 📜 Table of Contents

1. [Asking a question](#-asking-a-question)
2. [Filing a bug report or feature request](#-filing-a-bug-report-or-feature-request)
3. [Opening a pull request](#-opening-a-pull-request)
4. [Before you push](#-before-you-push)
5. [Things to know about this codebase](#-things-to-know-about-this-codebase)
6. [Cutting releases](#-cutting-releases)
7. [Code of conduct](#-code-of-conduct)
8. [Vulnerability reporting](#-vulnerability-reporting)
9. [Licensing](#-licensing)

## 💬 Asking a question

The [Linode Community](https://www.linode.com/community/questions/) is a good place for general support questions. For development and debugging talk, there is [Gopher's Slack #linodego](https://gophers.slack.com/messages/CAG93EB2S), and for general Kubernetes-on-Linode discussion, [Kubernetes Slack #linode](https://kubernetes.slack.com/messages/CD4B15LUR).

## 🐞 Filing a bug report or feature request

Open a [GitHub issue](https://github.com/linode/linode-filestorage-csi-driver/issues). Check the existing open and recently closed issues first, so we can avoid duplicated effort.

Detailed reports are much easier to act on. Please include:

- A reproducible test case, or the series of steps that triggers it.
- The version of the driver you are running.
- Any modifications you have made that are relevant.
- Anything unusual about your environment or deployment.
- Screenshots and code samples where they help.

For this driver specifically, [Collecting a bug report](./troubleshooting.md#-collecting-a-bug-report) lists the exact commands whose output we will ask for. Running them up front saves a round trip. Always say which region you are in, whether the nodes are in a VPC, and whether the Storage Space has mTLS enabled.

## 🔀 Opening a pull request

We follow the [fork and pull model](https://opensource.guide/how-to-contribute/#opening-a-pull-request).

Tips for a faster merge:

- Address one feature or bug per pull request.
- Keep large formatting changes out of a functional change; they make the real work hard to see.
- Follow the language's coding conventions.
- Make sure the tests pass.
- Keep commits atomic, [one change per commit](https://chris.beams.io/posts/git-commit/).
- Add tests.

## ✅ Before you push

```sh
mise run ci
```

That runs `fmt`, `gen-mock`, `vet`, `lint`, `test`, and `build`, which is exactly what the `CI` workflow runs. A green local run means a green pipeline.

If you touched the Helm chart:

```sh
mise run helm-lint
mise run update-kustomize   # then commit deploy/kubernetes/base
```

`deploy/kubernetes/base` is generated from the chart, and the `Helm` workflow fails when the two have drifted. Never hand-edit it. See [the Helm chart is the source of truth](./development-setup.md#-the-helm-chart-is-the-source-of-truth).

If you changed one of the mocked interfaces (`pkg/linode-client`, `pkg/mount-manager`, `pkg/filesystem`), run `mise run gen-mock` and commit the regenerated files.

## 🧭 Things to know about this codebase

A few conventions that are not obvious from reading a single file:

- **The same binary is both plugins.** `DRIVER_ROLE` selects which server gets constructed. The controller gets a Linode API client and no mounter; the node gets a mounter and no client. If you find yourself wanting to call the Linode API from the node plugin, that is a design signal, not a missing dependency; the node has no token by design.
- **Capabilities are the contract.** `internal/driver/capabilities.go` is the single place that decides what the driver claims to support. Implementing an RPC without advertising it, or the reverse, is a real bug. `ListSnapshots` is the one deliberate exception, and it is documented as such.
- **Every mutating RPC has to be idempotent.** CSI retries aggressively. New code should have a "the resource already exists" path and a test for it, following the existing lookup-by-label pattern.
- **Errors are mapped, not passed through.** Linode HTTP statuses go through `linodeError` so the sidecars can decide what to retry. Return a mapped `status.Error`, not a bare Go error, from an RPC.
- **Decisions belong in the code.** When something is deliberately not implemented, say so where a reader will find it. `ListVolumes` is the model: the reasoning sits in a comment on the function and in [the docs](./csi-rpc-reference.md#listvolumes-is-intentionally-unimplemented), so nobody has to re-derive it.
- **Unit tests are hermetic.** No network, no cluster, no token. Anything that needs those is manual verification; see [Testing](./testing.md#-manual-verification-against-a-real-cluster).

Changes to the CSI capability set, the volume handle format, or the volume context keys are compatibility-sensitive: existing PVs carry the old shape forever. Flag those explicitly in the PR description.

## 🏷 Cutting releases

Every time a commit merges to the default branch, a new patch release is [automatically drafted](https://github.com/linode/linode-filestorage-csi-driver/actions/workflows/release-drafter.yml) with a changelog. Edit the tag, title, and changelog as needed, then publish it from the [releases page](https://github.com/linode/linode-filestorage-csi-driver/releases).

Publishing a release triggers the [release workflow](https://github.com/linode/linode-filestorage-csi-driver/actions/workflows/release.yml), which:

1. Builds the release artifacts with `mise run release`.
2. Builds and pushes the image to Docker Hub and GHCR, tagged with the release tag and `latest`.
3. Publishes the packaged Helm chart with chart-releaser, using the `helm-{version}` release-name template.
4. Attaches the packaged chart and the rendered Kustomize manifest to the release.

See [Releasing](./development-setup.md#-releasing) for what the artifacts contain.

## 📐 Code of conduct

This project follows the [Linode Community Code of Conduct](https://www.linode.com/community/questions/conduct).

## 🔐 Vulnerability reporting

If you discover a potential security issue in this project, please notify Linode Security through the [vulnerability reporting process](https://hackerone.com/linode). Please do **not** open a public GitHub issue.

## 📄 Licensing

This project is licensed under the Apache License, Version 2.0.

## 📚 Related pages

- [Development Setup](./development-setup.md)
- [Testing](./testing.md)
- [Architecture](./architecture.md)
