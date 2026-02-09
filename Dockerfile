FROM --platform=$BUILDPLATFORM  golang:1.25-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH
COPY . $GOPATH/src/github.com/combor/k8s-mongo-primary-sidecar/
WORKDIR $GOPATH/src/github.com/combor/k8s-mongo-primary-sidecar/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /primary-sidecar
FROM gcr.io/distroless/static-debian13:nonroot
ARG TARGETOS
ARG TARGETARCH
ARG TARGETPLATFORM
LABEL org.opencontainers.image.description="Kubernetes sidecar that detects MongoDB replica set primary and labels the pod with primary=true for service selection (platform: ${TARGETPLATFORM})." \
      org.opencontainers.image.os="${TARGETOS}" \
      org.opencontainers.image.architecture="${TARGETARCH}" \
      org.opencontainers.image.platform="${TARGETPLATFORM}"
COPY --from=builder /primary-sidecar /primary-sidecar
USER 65532:65532
ENTRYPOINT ["/primary-sidecar"]
