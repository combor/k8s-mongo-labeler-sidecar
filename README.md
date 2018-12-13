# kuberentes mongoDB replica set pod labeler

## The problem

You have a mongoDB replica set up as a stateful set on kubernetes and you need to expose it as an external service. You set up `loadbalancer` service like this:
```
---
apiVersion: v1
kind: Service
metadata:
  annotations:
  namespace: dev
  name: mongo
  labels:
    name: mongo
spec:
  type: LoadBalancer
  ports:
  - name: mongo
    port: 27017
    targetPort: 27017
  selector:
    role: mongo
```
Unfortunately when you try to access it you are redireced to a random node which may not be primary.

## Solution

Use mongo labeler sidecar that will check which node is primary and will update it's pod to have `primary=true` label so you can use it in your service definition as a selector.
