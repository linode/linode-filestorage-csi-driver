# AGENTS.md

## Toolchain And Verification

- `mise.toml` is the source of truth for local tool versions and common tasks. Run `mise install` once, then prefer `mise run fmt`, `mise run vet`, `mise run lint`, `mise run test`, `mise run build`, and `mise run ci` for routine work.
- `go.mod` requires Go `1.26.2`. If you bypass `mise` and invoke `go` directly, use `GOTOOLCHAIN=auto` so the toolchain can self-upgrade if the system Go is older.
- Match CI order before handoff: `fmt -> vet -> lint -> test -> build`.
- Tool provisioning is handled through `mise install`.
- For focused verification, prefer `mise exec -- go test ./internal/driver -run TestName` or another specific package.
- If you change the Helm chart, run `mise run helm-lint` and `mise run update-kustomize`.
- `mise run` tasks currently inherit the top-level tool set from `mise.toml`, so CI/Image egress can expand when the tool list changes. When adjusting workflow allowlists, inspect the latest Step Security network events instead of assuming `install_args` fully constrains runtime downloads.
- GitHub Actions in this repo are pinned to immutable commit SHAs; preserve that pattern when updating workflows.

## GitHub CLI Workflow

- Prefer `gh` for GitHub work instead of the web UI when you need to inspect CI, triage issues or PRs, or leave comments.
- For PR-level CI debugging, start with `gh pr checks` on the PR number, branch, or URL. Use `--watch` to wait on checks and `--required` when you only need the required statuses.
- When you need the underlying workflow runs or specific jobs, use `gh run list --branch <branch>` or `gh run list --workflow <name>` to find the run, then `gh run view <run-id>` to inspect the jobs. Add `--job <job-id>` and `--log` or `--log-failed` to focus on one job.
- Use `gh pr status` for a quick summary of the current branch's PR state, checks, and review requests.
- Use `gh pr list` and `gh issue list` for triage. Filter with `--state`, `--label`, `--author`, `--assignee`, `--search`, or `--milestone` before reaching for the browser.
- Use `gh issue view -c` when you need the issue thread, and `gh issue edit` or `gh pr edit` to update titles, bodies, labels, assignees, reviewers, milestones, and project membership. If you touch projects, remember `gh auth refresh -s project` may be required.
- Use `gh pr comment` and `gh issue comment` for follow-up comments. Use `--body` or `--body-file`, and prefer `gh pr review` when the response is an approval, a review comment, or a request for changes.
- When you need to update a comment instead of adding a new one, use `gh pr comment --edit-last` or `gh issue comment --edit-last`; if there is no prior comment, `--create-if-none` keeps the workflow simple.
- Prefer explicit repository targeting with `-R` when the command is not running from the repo root or when you want to avoid ambiguity.

## Repo Shape

- Runtime entrypoint is `main.go`: it reads env vars directly, builds the Linode client, calls `internal/driver.SetupLinodeDriver`, then starts the gRPC server.
- `internal/driver` owns CSI RPCs, capability wiring, metadata, and socket serving. `pkg/linode-client` only constructs a configured `linodego.Client` today.
- Driver role is env-driven. `DRIVER_ROLE=controller` requires `LINODE_TOKEN`; `DRIVER_ROLE=node` uses `NODE_NAME` for `NodeGetInfo` and falls back to `linode-filestorage-node` if unset.
- `LINODE_URL` overrides the API base URL; keep that path working when changing client setup because it is the current hook for mock or non-default backends.

## Current Implementation Limits

- This repo is still scaffold-level. Identity and capability/info RPCs return real data, but most controller and node lifecycle RPCs still return gRPC `Unimplemented`.
- `VolumeContext` semantics are not finalized yet; do not assume NFS server or export-path keys already exist in the scaffold.
- Advertised capabilities are intentionally narrow: plugin `CONTROLLER_SERVICE`, controller `CREATE_DELETE_VOLUME`, node `STAGE_UNSTAGE_VOLUME` and `GET_VOLUME_STATS`.
- Helm defaults leave `csi-snapshotter` and `csi-resizer` off. Enable them in values and re-render after those APIs are ready.

## Documentation Site

- The docs site is Jekyll with the `just-the-docs` remote theme, built by GitHub Pages from the `gh-pages` branch. Pages are the Markdown files in `docs/`; `README.md` is the site index via `jekyll-readme-index`.
- **No Markdown file in this repo has YAML front matter, and none should get one.** GitHub's Markdown viewer renders a front matter block as a table at the top of the file, which is noise for anyone reading the docs in the repo or in a PR diff.
- Page variables live in the `defaults` block of `_config.yml` instead, one entry per file. Jekyll populates `page.*` from `defaults` exactly as if the keys were in the file, so `nav_order`, `permalink`, and anything else the theme reads keep working. Adding a page means adding a `defaults` entry for it; renaming or deleting one means updating that entry.
- `nav_order` values are contiguous starting at 1 (`README.md` is 1). Inserting a page in the middle means renumbering the entries after it. Reuse defaults as much as possible (generally, aside from page number which is unique per doc, other things such as layout etc should apply for all pages)
- Page titles come from the first heading in the file, via `jekyll-titles-from-headings`. Do not add a `title` key for that.
- `jekyll-optional-front-matter` is what turns a front-matter-less file into a page, and it refuses a hardcoded set of basenames (`README`, `LICENSE`, `LICENCE`, `COPYING`, `CODE_OF_CONDUCT`, `CONTRIBUTING`, `ISSUE_TEMPLATE`, `PULL_REQUEST_TEMPLATE`). A matching file is silently dropped from the site rather than erroring. `include:` in `_config.yml` is the plugin's escape hatch; add an entry if you name a page one of those.
- The contributing guide is not duplicated. `.github/CONTRIBUTING.md` is the only copy, and the site publishes that file directly at `/contributing/` via a `permalink` in its `defaults` entry. Link to it as a normal relative path (`.github/CONTRIBUTING.md` from the README, `../.github/CONTRIBUTING.md` from a page in `docs/`) and `jekyll-relative-links` rewrites it to the permalink. Do not use a root-relative `/.github/...` link: GitHub resolves a leading slash against the domain, not the repo, and `jekyll-relative-links` ignores absolute paths, so it breaks in both places.
- Publishing that file at all takes two `include` entries plus a `.github/` `exclude`, and the interaction is subtle. The comments in `_config.yml` explain it. The short version: Jekyll never descends into a dot directory unless it is in `include`, `include` wins over `exclude`, and inside a subdirectory `include` is matched against the bare filename while `exclude` is matched against the path from the source root. Do not simplify that block without running a build and checking that `_site` contains `contributing/index.html` and nothing else out of `.github/`.
- `optional_front_matter.remove_originals` and `readme_index.remove_originals` keep the raw `.md` files out of `_site`. Leave both on, or the site serves `/docs/usage/` and `/docs/usage.md` both.
- Verify docs changes the way Pages builds them with `mise run build-docs` (one shot) or `mise run serve-docs` (live reload); both run `jekyll/jekyll:pages` against the repo root, so no local Ruby is needed. Check the generated nav order and that in-page anchor links still resolve. See "Working on the docs" in `docs/development-setup.md` for the heading-anchor rules.

## Packaging And Deploy

- Images are built with Docker, not ko. Use `mise run image-build` for local images and `IMAGE_REPO=<repo> IMAGE_VERSION=<tag> mise run image-push` for publishing.
- Images are `linux/amd64` only. Akamai/LKE is amd64. `PLATFORM` defaults to `linux/amd64` so ARM Macs still produce LKE-runnable images.
- Controller and node socket paths are intentionally different. Keep controller at `unix:///var/lib/csi/sockets/pluginproxy/csi.sock`; keep node at `unix:///csi/csi.sock` with kubelet registration at `/var/lib/kubelet/plugins/linodenfs.csi.linode.com/csi.sock`.
- `charts/linode-nfs-csi-driver` is the source of truth. `deploy/kubernetes/base` is generated by `mise run update-kustomize` (`hack/update-kustomize.sh`). Do not hand-edit the rendered YAML; change the chart and re-render. `kustomization.yaml` is hand-written and lists the generated files.
- Raw Kustomize manifests expect a pre-created `linode-api-token` Secret in `kube-system` with key `token`.
- In the Helm chart, if `.Values.secretRef` is unset, `templates/secret.yaml` creates `linode-api-token` from `.Values.apiToken`, and the controller reads `LINODE_TOKEN` from key `token`.
- The Helm controller chart has optional `.Values.controller.kubeconfig` secret wiring; if you touch controller containers or volumes, preserve the mount and `--kubeconfig` plumbing across the plugin and controller sidecars.
- CI and Image workflows are back on Step Security `block` mode with allowlists derived from observed runs; if you change tool downloads or publish destinations, update those allowlists from fresh workflow logs. Release is still on `audit` until a representative tagged run is captured.
