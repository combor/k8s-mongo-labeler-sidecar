#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-kind-mongo-labeler}"
LABELER_IMAGE="${LABELER_IMAGE:-mongo-labeler:local}"
USE_PREBUILT_IMAGE="${USE_PREBUILT_IMAGE:-false}"
KEEP_CLUSTER="${KEEP_CLUSTER:-false}"
TIMEOUT="${TIMEOUT:-240s}"

cleanup() {
  if [[ "${KEEP_CLUSTER}" == "true" ]]; then
    echo "Keeping cluster '${CLUSTER_NAME}' (KEEP_CLUSTER=true)."
    return
  fi
  kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

export DOCKER_BUILDKIT=1

ensure_docker_available() {
  if docker info >/dev/null 2>&1; then
    return
  fi
  echo "ERROR: Docker daemon is not reachable."
  echo "Verify Docker daemon connectivity with: docker info"
  exit 1
}

ensure_buildx_available() {
  if docker buildx version >/dev/null 2>&1; then
    return
  fi
  echo "ERROR: docker buildx is not available."
  echo "Install/enable Docker Buildx and verify with: docker buildx version"
  exit 1
}

ensure_docker_available

if [[ "${USE_PREBUILT_IMAGE}" == "false" ]]; then
  ensure_buildx_available
fi

echo "Creating kind cluster '${CLUSTER_NAME}'..."
kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
kind create cluster --name "${CLUSTER_NAME}"

if [[ "${USE_PREBUILT_IMAGE}" == "true" ]]; then
  if ! docker image inspect "${LABELER_IMAGE}" >/dev/null 2>&1; then
    OFFICIAL_LABELER_IMAGE="ghcr.io/combor/k8s-mongo-labeler-sidecar:latest"
    echo "Labeler image '${LABELER_IMAGE}' not found locally. Pulling official image '${OFFICIAL_LABELER_IMAGE}'..."
    if ! docker pull "${OFFICIAL_LABELER_IMAGE}"; then
      echo "ERROR: Failed to pull official image '${OFFICIAL_LABELER_IMAGE}'."
      exit 1
    fi
    LABELER_IMAGE="${OFFICIAL_LABELER_IMAGE}"
  fi
  echo "Using prebuilt labeler image '${LABELER_IMAGE}'."
else
  echo "Building labeler image '${LABELER_IMAGE}'..."
  docker build -t "${LABELER_IMAGE}" "${ROOT_DIR}"
fi

echo "Loading labeler image '${LABELER_IMAGE}' into kind..."
kind load docker-image --name "${CLUSTER_NAME}" "${LABELER_IMAGE}"

echo "Deploying integration stack into default namespace..."
kubectl apply -f "${ROOT_DIR}/deployment-example.yaml"
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

echo "Verifying mongo service routes to primary pod..."
primary_pod_ip="$(kubectl get pod -l role=mongo,primary=true -o jsonpath='{.items[0].status.podIP}')"
endpoint_ips="$(kubectl get endpoints mongo -o jsonpath='{.subsets[0].addresses[*].ip}')"

if [[ "${endpoint_ips}" == "${primary_pod_ip}" ]]; then
  echo "PASS: mongo service routes to primary pod (${endpoint_ips})"
else
  echo "FAIL: expected endpoint ${primary_pod_ip}, got ${endpoint_ips}"
  exit 1
fi
