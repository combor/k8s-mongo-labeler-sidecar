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

https://hub.docker.com/repository/docker/combor/k8s-mongo-labeler-sidecar
