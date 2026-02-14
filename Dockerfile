# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src
ENV GOMODCACHE=/go/pkg/mod
ENV GOCACHE=/root/.cache/go-build

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,id=k8s-mongo-labeler-go-mod-cache \
    go mod download

COPY *.go ./
RUN --mount=type=cache,target=/go/pkg/mod,id=k8s-mongo-labeler-go-mod-cache \
    --mount=type=cache,target=/root/.cache/go-build,id=k8s-mongo-labeler-go-build-cache \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /primary-sidecar

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
