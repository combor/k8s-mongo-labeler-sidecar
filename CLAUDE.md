# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Go sidecar that detects the MongoDB replica-set primary and patches Kubernetes pod labels (`primary=true/false`) for service routing. All application logic lives in `main.go` with tests in `main_test.go`.

## Commands

```bash
# Build
go build -o k8s-mongo-labeler-sidecar main.go

# Unit tests
go test ./...

# Lint (golangci-lint v2, config in .golangci.yml)
golangci-lint run --timeout=10m

# Vulnerability scan
go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Integration test (requires kind, Docker, kubectl)
./test/integration/run.sh

# Docker build
docker build -t mongo-labeler:local .
```

## Architecture

Single-package Go application (`main.go`, ~314 lines):

- **Config**: parsed from environment variables at startup. `LABEL_SELECTOR` is required; others have defaults.
- **Labeler**: main controller holding Config, K8sClient, and a pluggable `primaryResolver` function.
- **Main loop**: 5-second ticker calls `setPrimaryLabel()` which queries pods via label selector, resolves the MongoDB primary via the `hello` command, and patches pod labels using `StrategicMergePatchType`.
- **Primary detection**: connects to MongoDB, runs `hello` on admin DB, extracts `primary` field (falls back to `isWritablePrimary`/`ismaster`), parses the hostname to get the pod name.
- **Label patching**: primary gets `primary=true`; non-primary behavior depends on `LABEL_ALL` (true → `primary=false`, false → label removed via null patch).
- **Kubernetes client**: uses in-cluster config when `KUBERNETES_SERVICE_HOST` is set, otherwise falls back to kubeconfig.

## Behavioral invariants

- `LABEL_SELECTOR` is required; missing causes startup failure.
- Primary pod always gets `primary=true`.
- Non-primary with `LABEL_ALL=true`: `primary=false`. With `LABEL_ALL=false`: label removed (null patch).

## Editing guidance

- Keep changes small and focused in `main.go` with matching `main_test.go` updates.
- If behavior or env vars change, update `README.md` and `deployment-example.yaml` in the same PR.
- Keep RBAC least-privilege (pods: get, list, patch only).
- Preserve secure defaults in manifests/images (non-root, dropped capabilities, readOnlyRootFilesystem, seccomp).
- Do not hand-edit `dist/` artifacts.
- Commit messages must explain *why* the change was made, not just what.
- Run `go test ./...` before finishing any change; run `./test/integration/run.sh` when changing labeling logic, Kubernetes interactions, manifests, or image behavior.

## CI

- PR/push: lint + unit tests + govulncheck.
- Tag push: release binaries (goreleaser), multi-arch image build/push to `ghcr.io/combor/k8s-mongo-labeler-sidecar`, integration test.
- Release flow touches `Dockerfile.dist`, `.goreleaser.yaml`, and `test/integration/run.sh`.
