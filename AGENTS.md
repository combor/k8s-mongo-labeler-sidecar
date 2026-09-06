# AGENTS.md

Go sidecar that detects the MongoDB replica-set primary and patches Kubernetes Pod labels for service routing. Runtime code and unit tests are in the root Go package.

## Behavioral contracts

- `LABEL_SELECTOR` must be set; missing configuration fails startup.
- Primary pods receive `primary="true"`. Other pods receive `primary="false"` when `LABEL_ALL=true`; otherwise remove the label using a JSON `null` patch value.
- Demote or unlabel other pods before promoting the primary. Stop reconciliation on patch failure and skip labels already in the desired state.
- Keep Pod RBAC limited to `get`, `list`, and `patch`.
- Preserve sidecar hardening: non-root, read-only root filesystem, no privilege escalation, all capabilities dropped, and `RuntimeDefault` seccomp.

## Making changes

- Keep changes focused and add regression coverage for behavior changes. Review findings must identify affected code and a concrete failure scenario.
- Update `README.md` and `deployment-example.yaml` when behavior or environment variables change.
- `dist/` contains generated release artifacts; regenerate them rather than editing them.
- Use the Go version in `go.mod` and linter version in CI. Keep the Docker builder's Go version aligned.
- Commit messages should explain why the change was needed.

## Validation

Run applicable checks from the repository root:

- Go code, tests, dependencies, or Go/lint configuration: `go test ./...` and `golangci-lint run --timeout=10m`.
- Go code or dependency changes: also `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`.
- Labeling, Kubernetes interactions, manifests, images, or integration harness changes: also `./test/integration/run.sh`. Requires kind, kubectl, Docker, and Buildx. Use a unique `CLUSTER_NAME` and temporary `KUBECONFIG`; the script deletes its named cluster.
- Release changes: run `goreleaser check` and check the release configuration, `Dockerfile.dist`, and integration harness together.
- Prose-only changes: check the diff and referenced paths/commands; Go and integration checks are unnecessary.

Report checks run and any failures or unavailable prerequisites. Repeat passing checks only after relevant changes or new evidence.
