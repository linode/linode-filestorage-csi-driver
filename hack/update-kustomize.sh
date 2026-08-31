#!/usr/bin/env bash
# Render deploy/kubernetes/base from the Helm chart.
# Same shape as kubernetes-sigs/aws-ebs-csi-driver hack/update-kustomize.sh.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TEMP_DIR}"' EXIT

cp deploy/kubernetes/base/kustomization.yaml "${TEMP_DIR}/kustomization.yaml"

# image.tag=dev keeps the just release ':dev' -> versioned-tag substitution.
helm template --output-dir "${TEMP_DIR}" --skip-tests --set image.tag=dev \
  kustomize charts/linode-nfs-csi-driver >/dev/null

rm -rf deploy/kubernetes/base
mv "${TEMP_DIR}/linode-nfs-csi-driver/templates" deploy/kubernetes/base

# Strip namespace so kustomization.yaml owns it. grep -v: portable, no sed -i.
while IFS= read -r -d '' file; do
  tmp="${file}.tmp"
  grep -v '^[[:space:]]*namespace:' "${file}" >"${tmp}"
  mv "${tmp}" "${file}"
done < <(find deploy/kubernetes/base -maxdepth 1 -type f -name '*.yaml' -print0)

cp "${TEMP_DIR}/kustomization.yaml" deploy/kubernetes/base/kustomization.yaml
