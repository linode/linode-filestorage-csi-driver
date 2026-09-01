#!/usr/bin/env bash
# Fail if deploy/kubernetes/base is not the Helm chart render.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

TEST_DIR="$(mktemp -d)"
trap 'rm -rf "${TEST_DIR}"' EXIT

cp -a charts deploy hack "${TEST_DIR}/"
"${TEST_DIR}/hack/update-kustomize.sh"

if ! diff -ru deploy/kubernetes/base "${TEST_DIR}/deploy/kubernetes/base"; then
  echo "deploy/kubernetes/base is stale. Run: mise run update-kustomize" >&2
  exit 1
fi
