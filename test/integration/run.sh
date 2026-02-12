#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-kind-mongo-labeler}"
LABELER_IMAGE="${LABELER_IMAGE:-mongo-labeler:local}"
USE_PREBUILT_IMAGE="${USE_PREBUILT_IMAGE:-false}"
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

if [[ "${USE_PREBUILT_IMAGE}" != "true" && "${USE_PREBUILT_IMAGE}" != "false" ]]; then
  echo "Invalid USE_PREBUILT_IMAGE value: ${USE_PREBUILT_IMAGE} (expected true or false)"
  exit 1
fi

echo "Creating kind cluster '${CLUSTER_NAME}'..."
kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
kind create cluster --name "${CLUSTER_NAME}"

if [[ "${USE_PREBUILT_IMAGE}" == "true" ]]; then
  if ! docker image inspect "${LABELER_IMAGE}" >/dev/null 2>&1; then
    echo "Labeler image '${LABELER_IMAGE}' not found locally. Pull or build it first."
    exit 1
  fi
  echo "Using prebuilt labeler image '${LABELER_IMAGE}'."
else
  echo "Building labeler image '${LABELER_IMAGE}'..."
  docker build -t "${LABELER_IMAGE}" "${ROOT_DIR}"
fi

echo "Loading labeler image '${LABELER_IMAGE}' into kind..."
kind load docker-image --name "${CLUSTER_NAME}" "${LABELER_IMAGE}"

echo "Deploying integration stack into default namespace..."
kubectl apply -f "${ROOT_DIR}/test/integration/stack.yaml"
kubectl set image statefulset/mongo labeler="${LABELER_IMAGE}"
kubectl rollout status statefulset/mongo --timeout="${TIMEOUT}"

echo "Waiting for primary label on mongo-0..."
deadline="$((SECONDS + 180))"
while true; do
  label="$(kubectl get pod mongo-0 -o jsonpath='{.metadata.labels.primary}' 2>/dev/null || true)"
  if [[ "${label}" == "true" ]]; then
    echo "PASS: mongo-0 has label primary=true"
    break
  fi
  if (( SECONDS >= deadline )); then
    echo "FAIL: timed out waiting for primary=true label on mongo-0"
    echo "Labeler logs:"
    kubectl logs mongo-0 -c labeler || true
    echo "Mongo logs:"
    kubectl logs mongo-0 -c mongo || true
    exit 1
  fi
  sleep 2
done

echo "Pod labels:"
kubectl get pod mongo-0 --show-labels
