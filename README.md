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
| `MONGO_ADDRESS` | no | `localhost:27017` | MongoDB endpoint, without the `mongodb://` prefix. |
| `K8S_REQUEST_TIMEOUT` | no | `10s` | Timeout for Kubernetes list/patch API requests (Go duration format, for example `5s`, `1m`). |
| `LABEL_ALL` | no | `false` | Boolean. If `true`, non-primary pods get `primary=false`; if `false`, the label is removed. |
| `DEBUG` | no | `false` | Boolean. If `true`, enables debug logging. |

`LABEL_ALL` and `DEBUG` are parsed as booleans. `K8S_REQUEST_TIMEOUT` is parsed as a Go duration. Invalid values fail startup.

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

Use a disposable `CLUSTER_NAME` and dedicated `KUBECONFIG`; the script deletes its named cluster.

```bash
./test/integration/run.sh
```

Optional overrides:

- `CLUSTER_NAME` (default `kind-mongo-labeler`)
- `LABELER_IMAGE` (default `mongo-labeler:local`)
- `USE_PREBUILT_IMAGE` (default `false`) — use local `LABELER_IMAGE`, falling back to the official `latest` tag if absent
- `TIMEOUT` (default `240s`) — rollout timeout; labels have a separate 180-second timeout
- `KEEP_CLUSTER=true` (keep cluster for debugging)

The script creates a temporary kind cluster, deploys a 3-pod Mongo StatefulSet and verifies that exactly one pod has `primary=true` while non-primary pods have `primary=false`. It also verifies that the `mongo` Service routes to the primary pod via EndpointSlice.

## Run CI locally with act

The repository uses a single workflow for GitHub and local runs: `.github/workflows/ci-release.yml`.

Prerequisites:

- [Docker Desktop](https://www.docker.com/products/docker-desktop/)
- [act](https://github.com/nektos/act)

Run the full workflow locally:

```bash
act -W .github/workflows/ci-release.yml
```
