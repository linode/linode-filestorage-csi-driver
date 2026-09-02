#!/usr/bin/env bash
# Dual-socat CSI sanity runner for the live cluster in KUBECONFIG.
#
# Controller socket is pod emptyDir → sidecar TCP 10000.
# Node socket is kubelet hostPath → generated Pod TCP 10001 on the same node.
# Path helpers exec the node plugin container. Never inject LINODE_TOKEN on the node.
set -euo pipefail

NS="kube-system"
CONTROLLER_DEPLOY="csi-linode-nfs-controller"
NODE_APP="csi-linode-nfs-node"
CONTROLLER_PORT=10000
NODE_PORT=10001
CONTROLLER_SOCKET="/var/lib/csi/sockets/pluginproxy/csi.sock"
SOCAT_IMAGE="alpine/socat:1.0.3"
SOCAT_CONTAINER="csi-sanity-socat"
ARTIFACT_DIR="${CSI_SANITY_ARTIFACT_DIR:-artifacts/csi-sanity}"

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CREATE_DIRECTORY="${DIR}/mkdir_in_pod.sh"
DELETE_DIRECTORY="${DIR}/rmdir_in_pod.sh"
CHECK_PATH="${DIR}/checkpath_in_pod.sh"

# Snapshot API is not live yet; all other advertised sanity capabilities run.
SKIP_TESTS="CreateSnapshot|DeleteSnapshot|ListSnapshots|GetSnapshot|volume source snapshot|create volume from an existing source snapshot"

added_controller_socat=0
pf_controller_pid=""
pf_node_pid=""
params=""
NODE_PROXY_NAME=""

log() { printf '%s\n' "$*"; }

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

wait_tcp() {
  local port="$1"
  local i
  for ((i = 0; i < 50; i++)); do
    if bash -c "echo >/dev/tcp/127.0.0.1/${port}" 2>/dev/null; then
      return 0
    fi
    sleep 0.2
  done
  return 1
}

controller_pod_with_socat() {
  local pod
  while IFS= read -r pod; do
    [[ -n "${pod}" ]] || continue
    if kubectl get pod "${pod}" -n "${NS}" \
      -o jsonpath='{range .spec.containers[*]}{.name}{"\n"}{end}' \
      | grep -qx "${SOCAT_CONTAINER}"; then
      printf '%s\n' "${pod}"
      return 0
    fi
  done < <(kubectl get pod -n "${NS}" -l "app=${CONTROLLER_DEPLOY}" \
    --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
  return 1
}

cleanup() {
  local status=$?
  local idx
  set +e
  if [[ -n "${pf_controller_pid}" ]]; then
    kill "${pf_controller_pid}" 2>/dev/null
    wait "${pf_controller_pid}" 2>/dev/null
  fi
  if [[ -n "${pf_node_pid}" ]]; then
    kill "${pf_node_pid}" 2>/dev/null
    wait "${pf_node_pid}" 2>/dev/null
  fi
  if [[ -n "${NODE_PROXY_NAME}" ]]; then
    kubectl delete pod "${NODE_PROXY_NAME}" -n "${NS}" --ignore-not-found --wait=false >/dev/null
  fi
  if (( added_controller_socat )); then
    idx="$(kubectl get deploy "${CONTROLLER_DEPLOY}" -n "${NS}" \
      -o jsonpath='{range .spec.template.spec.containers[*]}{.name}{"\n"}{end}' \
      | awk -v name="${SOCAT_CONTAINER}" '$0 == name { print NR - 1; exit }')"
    if [[ -n "${idx}" ]]; then
      kubectl patch deploy "${CONTROLLER_DEPLOY}" -n "${NS}" --type json \
        --patch "[{\"op\":\"remove\",\"path\":\"/spec/template/spec/containers/${idx}\"}]"
    fi
    kubectl rollout status "deploy/${CONTROLLER_DEPLOY}" -n "${NS}" --timeout=120s >/dev/null
  fi
  [[ -z "${params}" ]] || rm -f "${params}"
  return "${status}"
}

command -v kubectl >/dev/null || die "kubectl not found"
command -v csi-sanity >/dev/null || die "csi-sanity not found (mise install)"

[[ -n "${KUBECONFIG:-}" ]] || die "KUBECONFIG is required"
[[ -r "${KUBECONFIG}" ]] || die "KUBECONFIG is not readable: ${KUBECONFIG}"
[[ "${NFS_CSI_SANITY_SPACE_ID:-}" =~ ^[1-9][0-9]*$ ]] \
  || die "NFS_CSI_SANITY_SPACE_ID must be a positive integer for live provisioning tests"
mkdir -p "${ARTIFACT_DIR}"
[[ -d "${ARTIFACT_DIR}" && -w "${ARTIFACT_DIR}" ]] \
  || die "CSI_SANITY_ARTIFACT_DIR is not writable: ${ARTIFACT_DIR}"
export CSI_SANITY_NAMESPACE="${NS}"

kubectl get "deploy/${CONTROLLER_DEPLOY}" -n "${NS}" >/dev/null \
  || die "controller deployment ${CONTROLLER_DEPLOY} not found in ${NS}"

NODE_POD="$(kubectl get pod -n "${NS}" -l "app=${NODE_APP}" --field-selector=status.phase=Running \
  -o jsonpath='{.items[0].metadata.name}')"
[[ -n "${NODE_POD}" ]] || die "no running ${NODE_APP} pod in ${NS}"
NODE_NAME="$(kubectl get pod -n "${NS}" "${NODE_POD}" -o jsonpath='{.spec.nodeName}')"
[[ -n "${NODE_NAME}" ]] || die "could not resolve nodeName for ${NODE_POD}"
export CSI_SANITY_NODE_POD="${NODE_POD}"
log "using node plugin pod ${NODE_POD} on ${NODE_NAME}"

# Do not put LINODE_TOKEN on the node DaemonSet.
if kubectl get pod -n "${NS}" "${NODE_POD}" \
  -o jsonpath='{range .spec.containers[?(@.name=="plugin")].env[*]}{.name}{"\n"}{end}' \
  | grep -qx LINODE_TOKEN; then
  die "node plugin has LINODE_TOKEN; refuse to run sanity against a token-on-node deploy"
fi

trap cleanup EXIT

if kubectl get deploy "${CONTROLLER_DEPLOY}" -n "${NS}" \
  -o jsonpath='{range .spec.template.spec.containers[*]}{.name}{"\n"}{end}' \
  | grep -Fxq "${SOCAT_CONTAINER}"; then
  die "controller proxy ${SOCAT_CONTAINER} already exists"
fi
kubectl patch deploy "${CONTROLLER_DEPLOY}" -n "${NS}" --type json --patch "$(cat <<EOF
[
  {
    "op": "add",
    "path": "/spec/template/spec/containers/-",
    "value": {
      "name": "${SOCAT_CONTAINER}",
      "image": "${SOCAT_IMAGE}",
      "args": [
        "tcp-listen:${CONTROLLER_PORT},bind=127.0.0.1,fork,reuseaddr",
        "unix-connect:${CONTROLLER_SOCKET}"
      ],
      "ports": [
        {
          "name": "csi-socat",
          "containerPort": ${CONTROLLER_PORT}
        }
      ],
      "volumeMounts": [
        {
          "name": "socket-dir",
          "mountPath": "/var/lib/csi/sockets/pluginproxy/"
        }
      ]
    }
  }
]
EOF
)"
added_controller_socat=1
kubectl rollout status "deploy/${CONTROLLER_DEPLOY}" -n "${NS}" --timeout=180s

NODE_PROXY_NAME="$(sed \
  -e "s|__NODE_NAME__|${NODE_NAME}|g" \
  -e "s|__SOCAT_IMAGE__|${SOCAT_IMAGE}|g" \
  "${DIR}/socat-node.yaml" \
  | kubectl create -n "${NS}" -f - -o jsonpath='{.metadata.name}')"
[[ -n "${NODE_PROXY_NAME}" ]] || die "could not record the created node proxy name"
kubectl wait --namespace "${NS}" --for=condition=Ready "pod/${NODE_PROXY_NAME}" --timeout=120s

CONTROLLER_POD="$(controller_pod_with_socat)" || die "no running controller pod has ${SOCAT_CONTAINER}"
kubectl wait --namespace "${NS}" --for=condition=Ready "pod/${CONTROLLER_POD}" --timeout=180s

kubectl port-forward --address=127.0.0.1 -n "${NS}" \
  "pod/${CONTROLLER_POD}" "${CONTROLLER_PORT}:${CONTROLLER_PORT}" >/dev/null &
pf_controller_pid=$!
kubectl port-forward --address=127.0.0.1 -n "${NS}" \
  "pod/${NODE_PROXY_NAME}" "${NODE_PORT}:${NODE_PORT}" >/dev/null &
pf_node_pid=$!

wait_tcp "${CONTROLLER_PORT}" || die "controller port-forward :${CONTROLLER_PORT} never became ready"
wait_tcp "${NODE_PORT}" || die "node port-forward :${NODE_PORT} never became ready"

params="$(mktemp)"
sed "s|__SPACE_ID__|${NFS_CSI_SANITY_SPACE_ID}|g" "${DIR}/volume-parameters.yaml.tpl" >"${params}"

sanity_args=(
  --ginkgo.v
  --ginkgo.skip="${SKIP_TESTS}"
  --ginkgo.junit-report="${ARTIFACT_DIR%/}/csi-sanity-junit.xml"
  --csi.endpoint="dns:///127.0.0.1:${NODE_PORT}"
  --csi.controllerendpoint="dns:///127.0.0.1:${CONTROLLER_PORT}"
  --csi.testvolumeaccesstype=mount
  --csi.testvolumeparameters="${params}"
  --csi.createstagingpathcmd="${CREATE_DIRECTORY}"
  --csi.createmountpathcmd="${CREATE_DIRECTORY}"
  --csi.removestagingpathcmd="${DELETE_DIRECTORY}"
  --csi.removemountpathcmd="${DELETE_DIRECTORY}"
  --csi.checkpathcmd="${CHECK_PATH}"
)

log "running csi-sanity (controller :${CONTROLLER_PORT}, node :${NODE_PORT})"
csi-sanity "${sanity_args[@]}"
