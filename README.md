# kuberentes mongoDB replica set pod labeler

## The problem

You have a mongoDB replica set running as a stateful set on kubernetes and you need to expose it as an external service. If you use `loadbalancer` service it will select one of the mongo pods randomly so you can be redirected to a secondary node which is read only.

## Solution

Use mongo labeler sidecar that will check which pod is primary and will add `primary=true` label so you can use it in your service definition as a selector.
```
apiVersion: v1
kind: Service
metadata:
  name: mongo-external
  labels:
    name: mongo
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

Pod labeler will aultomatically detect kubernetes config wile running inside the cluster but if you want to test it outside it assumes that your k8s config is stored in `~/.kube/config` and mongo runs on `localhost:27017`

You can use `kubectl port-forward mongo-0 27017` command for testing purposes.

### ENV

```
LABEL_SELECTOR - labels that describe your mongo deployment
NAMESPACE - restricts where to look for mongo pods
DEBUG - when set to true increases log verbosity
```

Example:
```
     env:
       - name: LABEL_SELECTOR
         value: "role=mongo,environment=dev"
       - name: NAMESPACE
         value: "dev"
       - name: DEBUG
         value: "true"
```

### Docker image

please use included Dockerfile to build your own

## Deployment

See `deployment-example.yaml` for a complete example of how to deploy MongoDB with the labeler sidecar.

### Important notes

1. **Label Updates**: The labeler uses `Patch` instead of `Update` to modify pod labels, which prevents conflicts with other controllers and is more efficient.

2. **Security**: The container image uses distroless base image and runs as non-root user (UID 65532). The deployment example includes proper seccomp profile configuration (`RuntimeDefault`) to ensure compatibility with modern Kubernetes security policies.

3. **RBAC**: The sidecar requires the following permissions:
   - `get`, `list`, `patch` on pods in the target namespace

4. **Seccomp Profile**: The deployment example includes `seccompProfile.type: RuntimeDefault` which is required for Kubernetes 1.19+ with PodSecurityPolicy/PodSecurity standards enabled.

## Integration test (kind)

The repository includes a tiny end-to-end integration environment that runs:
- a 1-pod MongoDB replica set (`StatefulSet`)
- this labeler as a sidecar in the same pod
- minimal RBAC for pod patching

Run:
```
./test/integration/run.sh
```

The script creates a disposable `kind` cluster, builds and loads the local image, deploys `test/integration/stack.yaml`, and verifies that pod `mongo-0` gets label `primary=true`.

Set `KEEP_CLUSTER=true` to keep the cluster for debugging:
```
KEEP_CLUSTER=true ./test/integration/run.sh
```
