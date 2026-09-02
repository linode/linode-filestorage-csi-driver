#!/bin/sh
set -eu
: "${CSI_SANITY_NAMESPACE:?CSI_SANITY_NAMESPACE is required}"
: "${CSI_SANITY_NODE_POD:?CSI_SANITY_NODE_POD is required}"
# Suggested path is ignored; stdout is the path NodeStage/Publish will use.
kubectl exec -n "${CSI_SANITY_NAMESPACE}" -c plugin "${CSI_SANITY_NODE_POD}" -- mktemp -d
