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
kubectl kustomize --load-restrictor=LoadRestrictionsNone "${ROOT_DIR}/test/integration" | kubectl apply -f -
kubectl set image statefulset/mongo labeler="${LABELER_IMAGE}"
kubectl rollout status statefulset/mongo --timeout="${TIMEOUT}"

pods=(mongo-0 mongo-1 mongo-2)
expected_false_count=$(( ${#pods[@]} - 1 ))
echo "Waiting for label state: one pod primary=true and others primary=false..."
deadline="$((SECONDS + 180))"
while true; do
  true_count=0
  false_count=0
  other_count=0
  summary=()
  for pod in "${pods[@]}"; do
    label="$(kubectl get pod "${pod}" -o jsonpath='{.metadata.labels.primary}' 2>/dev/null || true)"
    if [[ "${label}" == "true" ]]; then
      ((true_count += 1))
    elif [[ "${label}" == "false" ]]; then
      ((false_count += 1))
    else
      ((other_count += 1))
    fi
    if [[ -z "${label}" ]]; then
      summary+=("${pod}=<unset>")
    else
      summary+=("${pod}=${label}")
    fi
  done

  if [[ "${true_count}" -eq 1 && "${false_count}" -eq "${expected_false_count}" && "${other_count}" -eq 0 ]]; then
    echo "PASS: label distribution is correct (${summary[*]})"
    break
  fi

  if (( SECONDS >= deadline )); then
    echo "FAIL: expected 1 true and ${expected_false_count} false labels, got true=${true_count} false=${false_count} other=${other_count} (${summary[*]})"
    for pod in "${pods[@]}"; do
      echo "Labeler logs (${pod}):"
      kubectl logs "${pod}" -c labeler || true
      echo "Mongo logs (${pod}):"
      kubectl logs "${pod}" -c mongo || true
    done
    exit 1
  fi
  sleep 2
done

echo "Pod labels:"
kubectl get pod -l role=mongo --show-labels
