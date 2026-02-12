# AGENTS.md

This file is a quick guide for AI coding agents and human contributors working on this repo.

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
  - Primary pod: set `primary=true`
  - Non-primary pods with `LABEL_ALL=true`: set `primary=false`
  - Non-primary pods with `LABEL_ALL=false`: remove the `primary` label via `null` patch value

## Editing guidance

- Prefer small, focused changes in `main.go` and matching updates in `main_test.go`.
- If behavior or env vars change, update `README.md` and `deployment-example.yaml` in the same PR.
- Keep RBAC least-privilege (`pods`: `get`, `list`, `patch`).
- Preserve secure defaults in manifests/images (non-root, dropped capabilities, `readOnlyRootFilesystem`, runtime default seccomp).
- Do not hand-edit `dist/` artifacts unless the task is explicitly release-artifact related.
- Keep scope tight to the changed areas; avoid broad refactors
- Prefer small, reliable tests that fail before and pass after
- Avoid overconfident root-cause claims
- Do NOT invent bugs; if evidence is weak, say so and skip.
- Prefer the smallest safe fix; avoid refactors and unrelated cleanup.
- Anchor each suggestion to concrete evidence
- Avoid generic advice; make each recommendation actionable and specific
- in the commit messages provide explanation why the chage was made

## Validation checklist

From the repo root, before finishing a change:

1. Run unit tests: `go test ./...`
2. When changing labeling logic, Kubernetes interactions, manifests, or image behavior, also run the integration test: `./test/integration/run.sh`

## CI awareness

- PR/push runs lint + unit tests.
- Tag push additionally runs release binaries, multi-arch image build/push, and the integration test.
- Changes affecting the release flow should be checked across `Dockerfile.dist`, `.goreleaser.yaml`, and `test/integration/run.sh`.
