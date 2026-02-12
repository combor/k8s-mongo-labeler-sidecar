# Kubernetes MongoDB Primary Pod Labeler

This sidecar detects the current MongoDB replica set primary and labels Kubernetes pods so services can target the writable node.

## How it works

Every 5 seconds the sidecar:

1. Connects to MongoDB (`MONGO_ADDRESS`, default `localhost:27017`).
2. Detects the primary pod name.
3. Lists pods in `NAMESPACE` matching `LABEL_SELECTOR`.
4. Patches labels:
   - primary pod: `primary=true`
   - other pods: `primary=false` when `LABEL_ALL=true`
   - other pods: removes `primary` label when `LABEL_ALL=false`

It uses Kubernetes `Patch` (strategic merge), not full-object `Update`.

## Service selector example

```yaml
apiVersion: v1
kind: Service
metadata:
  name: mongo-external
spec:
  type: LoadBalancer
  ports:
  - name: mongo
    port: 27017
  selector:
    role: mongo
    primary: "true"
```

## Configuration

When running inside Kubernetes, in-cluster config is used automatically.  
When running outside a cluster, kubeconfig defaults to `~/.kube/config` and can be overridden with `--kubeconfig`.

Environment variables:

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `LABEL_SELECTOR` | yes | none | Pod label selector (for example `role=mongo`). |
| `NAMESPACE` | no | `default` | Namespace where pods are listed and patched. |
| `MONGO_ADDRESS` | no | `localhost:27017` | MongoDB endpoint used for primary detection. |
| `LABEL_ALL` | no | `false` | Boolean. If `true`, non-primary pods get `primary=false`; if `false`, the label is removed. |
| `DEBUG` | no | `false` | Boolean. If `true`, enables debug logging. |

`LABEL_ALL` and `DEBUG` are parsed as booleans. Invalid values fail startup.

## Published image

Container images are published to GHCR at:

`ghcr.io/combor/k8s-mongo-labeler-sidecar`

```bash
docker pull ghcr.io/combor/k8s-mongo-labeler-sidecar:latest-amd64
docker pull ghcr.io/combor/k8s-mongo-labeler-sidecar:latest-arm64
```

## Deployment

`deployment-example.yaml` can be used as an example deployment manifest.

## Integration test (kind)

The repository includes an end-to-end test environment in `test/integration`.

Prerequisites:

- `kind`
- `kubectl`
- Docker with BuildKit/Buildx enabled

Run:

```bash
./test/integration/run.sh
```

Optional overrides:

- `CLUSTER_NAME` (default `kind-mongo-labeler`)
- `LABELER_IMAGE` (default `mongo-labeler:local`)
- `TIMEOUT` (default `240s`)
- `KEEP_CLUSTER=true` (keep cluster for debugging)

The script creates a temporary kind cluster, deploys a 3-pod Mongo StatefulSet and verifies that exactly one pod has `primary=true` while non-primary pods have `primary=false`.
