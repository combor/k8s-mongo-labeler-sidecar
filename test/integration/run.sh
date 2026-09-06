#!/usr/bin/env bash
set +x
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-kind-mongo-labeler}"
LABELER_IMAGE="${LABELER_IMAGE:-mongo-labeler:local}"
MONGO_AUTH_MODE="${MONGO_AUTH_MODE:-none}"
TIMEOUT="${TIMEOUT:-240s}"
pods=(mongo-0 mongo-1 mongo-2)
created=false

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

case "${MONGO_AUTH_MODE}" in
  none|env|uri|invalid) ;;
  *) fail 'MONGO_AUTH_MODE must be none, env, uri, or invalid' ;;
esac
for tool in docker kind kubectl; do
  command -v "${tool}" >/dev/null || fail "missing prerequisite: ${tool}"
done

# Secrets and unchecked command output stay in a private temporary directory.
umask 077
temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/mongo-labeler-integration-XXXXXX")"
export KUBECONFIG="${KUBECONFIG:-${temp_dir}/kubeconfig}"
export DOCKER_BUILDKIT=1
touch "${temp_dir}/needles"

run() {
  local status=0
  "$@" >"${temp_dir}/output" 2>"${temp_dir}/error" || status=$?
  if (( status != 0 )); then
    # CLI errors may echo Secret manifests or server errors. Never print them.
    fail "$1 $2 failed (exit ${status}); raw output suppressed"
  fi
}

read_logs() {
  local pod
  local since_args=()
  [[ -z "${1:-}" ]] || since_args+=("--since-time=$1")
  for pod in "${pods[@]}"; do
    kubectl logs "${pod}" -c labeler "${since_args[@]}" \
      >"${temp_dir}/${pod}.log" 2>"${temp_dir}/error" || return 1
    if grep -Fq -f "${temp_dir}/needles" "${temp_dir}/${pod}.log"; then
      return 1
    fi
  done
}

check_logs() {
  read_logs "$@" || fail 'unable to read sidecar logs safely; log content suppressed'
}

cleanup() {
  local status=$?
  trap - EXIT
  if [[ "${created}" == true ]]; then
    if (( status != 0 )); then
      echo 'Diagnostics: pod state and credential-checked sidecar logs'
      kubectl get pods -l role=mongo \
        -o 'custom-columns=NAME:.metadata.name,PHASE:.status.phase,PRIMARY:.metadata.labels.primary' \
        2>/dev/null || true
      # Check every sidecar before printing any logs. MongoDB server logs can
      # contain usernames and are never included in diagnostics.
      if read_logs; then
        for pod in "${pods[@]}"; do
          echo "${pod}:"
          tail -n 20 "${temp_dir}/${pod}.log"
        done
      else
        echo 'Unable to read sidecar logs safely; log content suppressed'
      fi
    fi
    if [[ "${KEEP_CLUSTER:-false}" == true ]]; then
      echo "Kept cluster ${CLUSTER_NAME}; recover access with kind export kubeconfig --name ${CLUSTER_NAME}"
    else
      kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
    fi
  fi
  rm -rf "${temp_dir}"
  exit "${status}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

uri_encode() {
  local value="$1" char i
  local LC_ALL=C
  for ((i = 0; i < ${#value}; i++)); do
    char="${value:i:1}"
    case "${char}" in
      [a-zA-Z0-9.~_-]) printf '%s' "${char}" ;;
      *) printf '%%%02X' "'${char}" ;;
    esac
  done
}

random_hex() {
  od -An -N24 -tx1 /dev/urandom | tr -d ' \n'
}

create_credentials() {
  local file value encoded
  mkdir "${temp_dir}/credentials"
  printf 'sidecar-%s@test' "$(random_hex)" >"${temp_dir}/credentials/username"
  printf '%s:@/%%+?&=' "$(random_hex)" >"${temp_dir}/credentials/password"
  printf '%s:@/%%+?&=' "$(random_hex)" >"${temp_dir}/credentials/bad-password"
  head -c 512 /dev/urandom | base64 | tr -d '\n' >"${temp_dir}/credentials/keyfile"
  printf 'mongodb://%s:%s@localhost:27017/?authSource=admin' \
    "$(uri_encode "$(<"${temp_dir}/credentials/username")")" \
    "$(uri_encode "$(<"${temp_dir}/credentials/password")")" >"${temp_dir}/credentials/uri"
  for file in "${temp_dir}/credentials/"*; do
    value="$(<"${file}")"
    encoded="$(uri_encode "${value}")"
    printf '%s\n' "${value}" "${encoded}" "${encoded//%20/+}" >>"${temp_dir}/needles"
  done
  # Pass file paths, never credentials, on the command line.
  run kubectl create secret generic mongo-auth --from-file="${temp_dir}/credentials"
}

deploy_fixture() {
  # These trusted overlays reuse deployment-example.yaml outside their directory.
  run kubectl kustomize --load-restrictor=LoadRestrictionsNone \
    "${ROOT_DIR}/test/integration/fixtures/${MONGO_AUTH_MODE}"
  mv "${temp_dir}/output" "${temp_dir}/fixture.yaml"
  run kubectl apply -f "${temp_dir}/fixture.yaml"
  # The base starts at zero replicas so all overrides are set before any pod runs.
  run kubectl set image statefulset/mongo "labeler=${LABELER_IMAGE}"
  if [[ -n "${MONGO_GLIBC_TUNABLES:-}" ]]; then
    run kubectl set env statefulset/mongo --containers=mongo "GLIBC_TUNABLES=${MONGO_GLIBC_TUNABLES}"
  fi
  run kubectl scale statefulset/mongo --replicas=3
}

read_labels() {
  run kubectl get pods -l role=mongo \
    -o 'jsonpath={range .items[*]}{.metadata.name}{"|"}{.metadata.labels.primary}{"|"}{.status.podIP}{"\n"}{end}'
  cp "${temp_dir}/output" "${temp_dir}/labels"
}

verify_routing() {
  local deadline=$((SECONDS + 180)) pod label ip primary_ip addresses
  local count true_count false_count
  while (( SECONDS < deadline )); do
    read_labels
    check_logs
    count=0 true_count=0 false_count=0 primary_ip=''
    while IFS='|' read -r pod label ip; do
      count=$((count + 1))
      case "${label}" in
        true) true_count=$((true_count + 1)); primary_ip="${ip}" ;;
        false) false_count=$((false_count + 1)) ;;
      esac
    done <"${temp_dir}/labels"
    if (( count == 3 && true_count == 1 && false_count == 2 )) && [[ -n "${primary_ip}" ]]; then
      run kubectl get endpointslices -l kubernetes.io/service-name=mongo \
        -o 'go-template={{range .items}}{{range .endpoints}}{{if ne .conditions.ready false}}{{range .addresses}}{{.}}{{"\n"}}{{end}}{{end}}{{end}}{{end}}'
      addresses="$(sort -u "${temp_dir}/output")"
      if [[ "${addresses}" == "${primary_ip}" ]]; then
        echo 'PASS: one primary, two secondary labels, and Service routes to the primary'
        return
      fi
    fi
    sleep 2
  done
  fail 'labels or Service endpoints did not converge'
}

authentication_rejected() {
  local pod log_bytes
  for pod in "${pods[@]}"; do
    run kubectl logs "${pod}" -c mongo "--since-time=$1"
    mv "${temp_dir}/output" "${temp_dir}/mongo.log"
    log_bytes="$(wc -c <"${temp_dir}/mongo.log" | tr -d ' ')"
    # Use mongosh already in the MongoDB container to inspect structured events.
    # The account comes from the container environment; neither it nor the raw
    # server logs are printed, including when the assertion fails.
    if ! kubectl exec -i "${pod}" -c mongo -- env MONGO_AUTH_LOG_BYTES="${log_bytes}" mongosh --quiet --nodb --eval '
      (async () => {
        // Consume buffered stdin and stop at the known byte count instead of EOF.
        const expected = Number(process.env.MONGO_AUTH_LOG_BYTES);
        if (expected === 0) quit(1);
        setTimeout(() => quit(2), 15000);
        const chunks = [];
        let received = 0;
        for await (const chunk of process.stdin) {
          const data = Buffer.from(chunk);
          chunks.push(data);
          received += data.length;
          if (received >= expected) break;
        }
        const rejected = Buffer.concat(chunks).toString("utf8").split("\n").some(line => {
          try {
            const event = JSON.parse(line);
            return event.id === 5286307 && event.attr?.result === 18 &&
              event.attr?.user === process.env.MONGO_TEST_USERNAME;
          } catch { return false; }
        });
        quit(rejected ? 0 : 1);
      })();
    ' <"${temp_dir}/mongo.log" >"${temp_dir}/output" 2>"${temp_dir}/error"; then
      return 1
    fi
  done
}

verify_invalid_password() {
  local since start deadline pod label ip count retries retrying rejected
  since="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  start=${SECONDS}
  deadline=$((start + 90))
  while (( SECONDS < deadline )); do
    read_labels
    count=0
    while IFS='|' read -r pod label ip; do
      count=$((count + 1))
      [[ -z "${label}" ]] || fail 'invalid credentials caused a label change'
    done <"${temp_dir}/labels"
    (( count == 3 )) || fail 'expected three MongoDB pods'
    check_logs
    if grep -Eq 'Patching pod|primary detected' "${temp_dir}"/mongo-*.log; then
      fail 'invalid credentials reached pod patching'
    fi
    # Start counting only after every member authenticated the correct user
    # during bootstrap. Paused driver pools can report retry errors after auth.
    check_logs "${since}"
    retrying=true
    for pod in "${pods[@]}"; do
      retries="$(grep -Ec 'authentication_failed|connection_pool_unavailable' "${temp_dir}/${pod}.log" || true)"
      (( retries >= 2 )) || retrying=false
    done
    rejected=false
    if authentication_rejected "${since}"; then rejected=true; fi
    if (( SECONDS - start >= 15 )) && [[ "${rejected}" == true && "${retrying}" == true ]]; then
      echo 'PASS: every sidecar rejected the password and kept failing without patching labels'
      return
    fi
    sleep 2
  done
  fail 'sidecars did not report authentication rejection followed by failed retries'
}

run docker info
run kind get clusters
if grep -Fxq -- "${CLUSTER_NAME}" "${temp_dir}/output"; then
  fail 'the named kind cluster already exists; choose a unique CLUSTER_NAME'
fi
if [[ "${USE_PREBUILT_IMAGE:-false}" == true ]]; then
  run docker image inspect "${LABELER_IMAGE}"
else
  run docker buildx version
  echo 'Building the sidecar image from this checkout'
  run docker build -t "${LABELER_IMAGE}" "${ROOT_DIR}"
fi
echo "Creating isolated cluster for ${MONGO_AUTH_MODE} authentication scenario"
# Clean up partial creation too; the name was confirmed unused above.
created=true
run kind create cluster --name "${CLUSTER_NAME}"
run kind load docker-image "${LABELER_IMAGE}" --name "${CLUSTER_NAME}"
if [[ "${MONGO_AUTH_MODE}" != none ]]; then create_credentials; fi
deploy_fixture
run kubectl rollout status statefulset/mongo "--timeout=${TIMEOUT}"
if [[ "${MONGO_AUTH_MODE}" != none ]]; then
  # ping and hello work without auth; use a protected command to prove that
  # access control is enforced.
  run kubectl exec mongo-0 -c mongo -- mongosh --quiet --eval \
    'try { db.adminCommand({getCmdLineOpts: 1}); quit(1); } catch (error) { quit(error.code === 13 ? 0 : 1); }'
  echo 'PASS: MongoDB rejects an unauthenticated protected command'
fi
if [[ "${MONGO_AUTH_MODE}" == invalid ]]; then
  verify_invalid_password
else
  verify_routing
fi
check_logs
echo 'PASS: sidecar logs contain no credentials'
