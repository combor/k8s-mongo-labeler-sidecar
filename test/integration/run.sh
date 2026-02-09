#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-mongo-labeler-it}"
NAMESPACE="${NAMESPACE:-mongo-it}"
LABELER_IMAGE="${LABELER_IMAGE:-mongo-labeler-it:local}"
KEEP_CLUSTER="${KEEP_CLUSTER:-false}"
TIMEOUT="${TIMEOUT:-240s}"
DOCKER_CONFIG_TMP=""

prepare_docker_config() {
  if [[ -n "${DOCKER_CONFIG:-}" ]]; then
    return
  fi
  DOCKER_CONFIG_TMP="$(mktemp -d)"
  export DOCKER_CONFIG="${DOCKER_CONFIG_TMP}"
  cat >"${DOCKER_CONFIG}/config.json" <<'JSON'
{
  "auths": {},
  "cliPluginsExtraDirs": [
    "/opt/homebrew/lib/docker/cli-plugins"
  ]
}
JSON
}

prepare_docker_host() {
  if [[ -n "${DOCKER_HOST:-}" ]]; then
    return
  fi
  local colima_socket="${HOME}/.colima/default/docker.sock"
  if [[ -S "${colima_socket}" ]]; then
    export DOCKER_HOST="unix://${colima_socket}"
  fi
}

cleanup() {
  if [[ -n "${DOCKER_CONFIG_TMP}" ]]; then
    rm -rf "${DOCKER_CONFIG_TMP}"
  fi
  if [[ "${KEEP_CLUSTER}" == "true" ]]; then
    echo "Keeping cluster '${CLUSTER_NAME}' (KEEP_CLUSTER=true)."
    return
  fi
  kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

prepare_docker_config
prepare_docker_host
export DOCKER_BUILDKIT=1

echo "Creating kind cluster '${CLUSTER_NAME}'..."
kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
kind create cluster --name "${CLUSTER_NAME}"

echo "Building labeler image '${LABELER_IMAGE}'..."
docker build -t "${LABELER_IMAGE}" "${ROOT_DIR}"
kind load docker-image --name "${CLUSTER_NAME}" "${LABELER_IMAGE}"

echo "Deploying integration stack..."
kubectl apply -f "${ROOT_DIR}/test/integration/stack.yaml"
kubectl -n "${NAMESPACE}" rollout status statefulset/mongo --timeout="${TIMEOUT}"

echo "Waiting for primary label on mongo-0..."
deadline="$((SECONDS + 180))"
while true; do
  label="$(kubectl -n "${NAMESPACE}" get pod mongo-0 -o jsonpath='{.metadata.labels.primary}' 2>/dev/null || true)"
  if [[ "${label}" == "true" ]]; then
    echo "PASS: mongo-0 has label primary=true"
    break
  fi
  if (( SECONDS >= deadline )); then
    echo "FAIL: timed out waiting for primary=true label on mongo-0"
    echo "Labeler logs:"
    kubectl -n "${NAMESPACE}" logs mongo-0 -c labeler || true
    echo "Mongo logs:"
    kubectl -n "${NAMESPACE}" logs mongo-0 -c mongo || true
    exit 1
  fi
  sleep 2
done

echo "Pod labels:"
kubectl -n "${NAMESPACE}" get pod mongo-0 --show-labels
