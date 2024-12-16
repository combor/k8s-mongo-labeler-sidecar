FROM --platform=$BUILDPLATFORM  golang:1.23.4-bullseye AS builder

ARG TARGETOS
ARG TARGETARCH
COPY . $GOPATH/src/github.com/combor/k8s-mongo-primary-sidecar/
WORKDIR $GOPATH/src/github.com/combor/k8s-mongo-primary-sidecar/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /primary-sidecar
FROM scratch
COPY --from=builder /primary-sidecar /primary-sidecar
ENTRYPOINT ["/primary-sidecar"]
