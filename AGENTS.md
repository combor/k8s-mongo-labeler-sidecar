# AGENTS.md

## Project scope

This repository builds `k8s-mongo-labeler-sidecar`, a Go sidecar that detects the MongoDB replica-set primary and patches Kubernetes Pod labels for service routing.

## Stack and runtime

- Language: Go
- Main entrypoint: `main.go`
- Unit tests: `main_test.go`
- Integration test: `test/integration/run.sh` (kind + Docker + kubectl)
- Release/container files: `Dockerfile`, `Dockerfile.dist`, `.goreleaser.yaml`
- CI workflows: `.github/workflows/ci-release.yml`, `.github/workflows/codeql-analysis.yml`

## Behavioral invariants (do not change unintentionally)

- `LABEL_SELECTOR` is required; startup fails if missing.
- Label behavior:
  - Primary pod: `primary=true`
  - Non-primary pods with `LABEL_ALL=true`: `primary=false`
  - Non-primary pods with `LABEL_ALL=false`: remove `primary` label via `null` patch value

## Editing guidance

- Prefer small, focused changes in `main.go` and matching updates in `main_test.go`.
- If behavior or env vars change, update `README.md` and `deployment-example.yaml` in the same PR.
- Keep RBAC least-privilege (`pods`: `get`, `list`, `patch`).
- Preserve secure defaults in manifests/images (non-root, dropped capabilities, `readOnlyRootFilesystem`, runtime default seccomp).
- Do not hand-edit `dist/` artifacts unless the task is explicitly release-artifact related.

## Validation checklist

Run from repo root before finishing:

1. `go test ./...`

Run integration test when changing labeling logic, Kubernetes interactions, manifests, or image behavior:

1. `./test/integration/run.sh`

## CI awareness

- PR/push runs lint + unit tests.
- Tag push additionally runs release binaries, multi-arch image build/push, and integration test.
- Changes affecting release flow should be checked across `Dockerfile.dist`, `.goreleaser.yaml`, and `test/integration/run.sh`.
