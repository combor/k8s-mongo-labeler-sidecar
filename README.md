# Kubernetes MongoDB Primary Pod Labeler

[![CI](https://github.com/combor/k8s-mongo-labeler-sidecar/actions/workflows/ci-release.yml/badge.svg)](https://github.com/combor/k8s-mongo-labeler-sidecar/actions/workflows/ci-release.yml)
[![Release](https://img.shields.io/github/v/release/combor/k8s-mongo-labeler-sidecar?display_name=tag)](https://github.com/combor/k8s-mongo-labeler-sidecar/releases)
[![License](https://img.shields.io/github/license/combor/k8s-mongo-labeler-sidecar)](https://github.com/combor/k8s-mongo-labeler-sidecar/blob/master/LICENSE)
[![Coverage](https://codecov.io/gh/combor/k8s-mongo-labeler-sidecar/graph/badge.svg)](https://codecov.io/gh/combor/k8s-mongo-labeler-sidecar)

This sidecar detects the current MongoDB replica set primary and labels Kubernetes pods so services can target the writable node.

## How it works

At startup and on a 5-second timer, the sidecar:

1. Queries MongoDB's `hello` command at `MONGO_ADDRESS` (default `localhost:27017`).
2. Detects the primary pod name.
3. Lists pods in `NAMESPACE` matching `LABEL_SELECTOR`.
4. Patches labels:
   - primary pod: `primary=true`
   - other pods: `primary=false` when `LABEL_ALL=true`
   - other pods: removes `primary` label when `LABEL_ALL=false`

It patches changed labels only, demoting other pods before promoting the primary.

Member hostnames must start with their pod name, for example `mongo-0.mongo-cluster:27017`.

## Service selector example

```yaml
apiVersion: v1
kind: Service
metadata:
  name: mongo
spec:
  selector:
    role: mongo
    primary: "true"
  ports:
  - name: mongo
    port: 27017
```

## Configuration

When running inside Kubernetes, in-cluster config is used automatically.  
When running outside a cluster, kubeconfig defaults to `~/.kube/config` and can be overridden with `--kubeconfig`.

Environment variables:

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `LABEL_SELECTOR` | yes | none | Selector for all pods in one replica set (for example `role=mongo`). |
| `NAMESPACE` | no | `default` | Namespace where pods are listed and patched. |
| `MONGO_ADDRESS` | no | `localhost:27017` | Single MongoDB endpoint, optionally prefixed with `mongodb://`. URI credentials and connection options are supported. SRV and multiple hosts are unsupported because the sidecar connects directly to its local member. |
| `MONGO_USERNAME` | no | unset | MongoDB username, supplied together with `MONGO_PASSWORD`. |
| `MONGO_PASSWORD` | no | unset | MongoDB password, supplied together with `MONGO_USERNAME`. Both values must be nonempty and are passed literally; do not URL-encode them. |
| `MONGO_AUTH_SOURCE` | no | URI source/database, then `admin` | Database where the user account was created. Requires the environment credential pair. Overrides URI `authSource`, which otherwise takes precedence over the URI database. |
| `K8S_REQUEST_TIMEOUT` | no | `10s` | Timeout for Kubernetes list/patch API requests (Go duration format, for example `5s`, `1m`). |
| `LABEL_ALL` | no | `false` | Boolean. If `true`, non-primary pods get `primary=false`; if `false`, the label is removed. |
| `DEBUG` | no | `false` | Boolean. If `true`, enables debug logging. |

`LABEL_ALL` and `DEBUG` are parsed as booleans. `K8S_REQUEST_TIMEOUT` is parsed as a Go duration. Invalid values fail startup.

Supply `MONGO_USERNAME` and `MONGO_PASSWORD` from Kubernetes Secrets using
`valueFrom.secretKeyRef`; the deployment example includes commented entries.
Alternatively, supply the entire `MONGO_ADDRESS` from a Secret, with URI-encoded
credentials. Combining URI credentials with any of the three authentication
variables fails startup. An explicitly empty `MONGO_AUTH_SOURCE` also fails.
Existing unauthenticated configurations continue to work.

Authentication uses the MongoDB driver's default SCRAM negotiation unless a
mechanism is specified in the URI. Restart the sidecar after rotating credentials
or changing Secret-backed environment variables. MongoDB users are provisioned
separately; the sidecar only runs `ping` and `hello`, which require no database
roles. Those commands are also available without authentication, so successful
primary detection alone does not prove that credentials were configured.

Logs show only the MongoDB host/port and whether authentication is configured.
MongoDB failures report an operation, fixed error category, and an available
numeric server code, without raw driver messages. Driver logging through
`MONGODB_LOG_*` is suppressed even when `DEBUG=true` to keep credentials out of
both startup and failure logs.

## Published image

Container images are published to GHCR at:

`ghcr.io/combor/k8s-mongo-labeler-sidecar`, for `linux/amd64` and `linux/arm64`.

```bash
docker pull ghcr.io/combor/k8s-mongo-labeler-sidecar:0.7.2
```

## Deployment

[deployment-example.yaml](deployment-example.yaml) provides a three-member replica-set example.

> **Demo only:** MongoDB has no authentication or TLS; `emptyDir` data is lost when pods are removed. Configure authentication, TLS, and persistent storage for production. The NetworkPolicy limits ingress to same-namespace traffic on port 27017 only when enforced by the CNI.

## Integration test (kind)

The repository includes an end-to-end test environment in `test/integration`.

Prerequisites:

- [kind](https://kind.sigs.k8s.io/)
- [kubectl](https://kubernetes.io/docs/reference/kubectl/)
- [Docker](https://www.docker.com/) with a running daemon
- [Buildx](https://github.com/docker/buildx) with BuildKit
- Bash
- Python 3 (standard library only)

Use a disposable `CLUSTER_NAME` and dedicated `KUBECONFIG`; the script deletes its named cluster.

```bash
./test/integration/run.sh
```

Optional overrides:

- `CLUSTER_NAME` (default `kind-mongo-labeler`)
- `LABELER_IMAGE` (default `mongo-labeler:local`)
- `USE_PREBUILT_IMAGE` (default `false`) — use an existing local `LABELER_IMAGE`; a missing image fails the test
- `MONGO_AUTH_MODE` (default `none`) — `none`, `env`, `uri`, or `invalid` (wrong password)
- `MONGO_GLIBC_TUNABLES` (default unset) — optional `GLIBC_TUNABLES` override for MongoDB test containers only
- `TIMEOUT` (default `240s`) — rollout timeout; labels have a separate 180-second timeout
- `KEEP_CLUSTER=true` (keep cluster for debugging)

The script creates a temporary kind cluster, deploys the locally built sidecar
image in a 3-pod Mongo StatefulSet, and verifies primary labels and Service
routing via EndpointSlice. It refuses to delete a pre-existing cluster with the
chosen name. When `KUBECONFIG` is unset, it uses a temporary kubeconfig.

The `env` and `uri` scenarios enforce MongoDB authentication using a generated
replica-set keyfile and test user, then test the respective credential inputs.
The `invalid` scenario starts fresh, unlabeled pods with the wrong password and
checks that every sidecar repeatedly fails authentication without patching any
labels. All scenarios run the sidecar with debug logging enabled. The harness
checks logs for generated credentials and their encoded forms before printing
diagnostics; MongoDB server logs are not printed because they can contain
usernames. Generated Secrets stay inside the disposable cluster.

CI runs all four scenarios against the current source on pull requests and
before releases. To run an authenticated scenario locally, use
`MONGO_AUTH_MODE=env CLUSTER_NAME=mongo-labeler-auth-env ./test/integration/run.sh`.

MongoDB 8.3.8 can refuse to start on newer Linux kernels due to its TCMalloc/rseq
compatibility check. For local tests on an affected host, the Docker image
maintainers describe using `glibc.pthread.rseq=1`; pass it with
`MONGO_GLIBC_TUNABLES=glibc.pthread.rseq=1`. This override applies only to the test
containers. See the [upstream discussion](https://github.com/docker-library/mongo/discussions/748).

## Run CI locally with act

The repository uses a single workflow for GitHub and local runs: `.github/workflows/ci-release.yml`.

Prerequisites:

- [Docker Desktop](https://www.docker.com/products/docker-desktop/)
- [act](https://github.com/nektos/act)

Run the full workflow locally:

```bash
act -W .github/workflows/ci-release.yml
```
