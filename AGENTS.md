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
2. Run linter: `golangci-lint run --timeout=10m`
3. When changing labeling logic, Kubernetes interactions, manifests, or image behavior, also run the integration test: `./test/integration/run.sh`

## CI awareness

- PR/push runs lint + unit tests.
- Tag push additionally runs release binaries, multi-arch image build/push, and the integration test.
- Changes affecting the release flow should be checked across `Dockerfile.dist`, `.goreleaser.yaml`, and `test/integration/run.sh`.


<!-- headroom:rtk-instructions -->
# RTK (Rust Token Killer) - Token-Optimized Commands

When running shell commands, **always prefix with `rtk`**. This reduces context
usage by 60-90% with zero behavior change. If rtk has no filter for a command,
it passes through unchanged — so it is always safe to use.

## Key Commands
```bash
# Git (59-80% savings)
rtk git status          rtk git diff            rtk git log

# Files & Search (60-75% savings)
rtk ls <path>           rtk read <file>         rtk grep <pattern>
rtk find <pattern>      rtk diff <file>

# Test (90-99% savings) — shows failures only
rtk pytest tests/       rtk cargo test          rtk test <cmd>

# Build & Lint (80-90% savings) — shows errors only
rtk tsc                 rtk lint                rtk cargo build
rtk prettier --check    rtk mypy                rtk ruff check

# Analysis (70-90% savings)
rtk err <cmd>           rtk log <file>          rtk json <file>
rtk summary <cmd>       rtk deps                rtk env

# GitHub (26-87% savings)
rtk gh pr view <n>      rtk gh run list         rtk gh issue list

# Infrastructure (85% savings)
rtk docker ps           rtk kubectl get         rtk docker logs <c>

# Package managers (70-90% savings)
rtk pip list            rtk pnpm install        rtk npm run <script>
```

## Rules
- In command chains, prefix each segment: `rtk git add . && rtk git commit -m "msg"`
- For debugging, use raw command without rtk prefix
- `rtk proxy <cmd>` runs command without filtering but tracks usage
<!-- /headroom:rtk-instructions -->
