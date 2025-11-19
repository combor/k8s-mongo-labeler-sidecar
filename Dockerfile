FROM --platform=$BUILDPLATFORM  golang:1.24-bullseye AS builder

ARG TARGETOS
ARG TARGETARCH
COPY . $GOPATH/src/github.com/combor/k8s-mongo-primary-sidecar/
WORKDIR $GOPATH/src/github.com/combor/k8s-mongo-primary-sidecar/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /primary-sidecar
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /primary-sidecar /primary-sidecar
USER 65532:65532
ENTRYPOINT ["/primary-sidecar"]
