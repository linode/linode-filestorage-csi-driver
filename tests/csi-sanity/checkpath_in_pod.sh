#!/bin/sh
set -eu
: "${CSI_SANITY_NAMESPACE:?CSI_SANITY_NAMESPACE is required}"
: "${CSI_SANITY_NODE_POD:?CSI_SANITY_NODE_POD is required}"
# shellcheck disable=SC2016 # $1 is evaluated by the remote shell.
kubectl exec -n "${CSI_SANITY_NAMESPACE}" -c plugin "${CSI_SANITY_NODE_POD}" -- \
  sh -c 'if [ ! -e "$1" ]; then echo not_found; elif [ -d "$1" ]; then echo directory; elif [ -f "$1" ]; then echo file; else echo other; fi' \
  sh "$1"
