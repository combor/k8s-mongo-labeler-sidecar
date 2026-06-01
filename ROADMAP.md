# ROADMAP

Tracking file for upcoming improvement work on **k8s-mongo-labeler-sidecar**.

Findings come from a multi-agent audit (performance, simplification, security, correctness) in which **every finding was adversarially verified against the actual code**. Severities below are the *verified* ones (several initially-inflated claims were downgraded during verification). Items are sorted **highest value at the top** — pick from the top down.

## How to use this file
- Each item has a stable ID (`R1`, `R2`, …) and is **self-contained**: problem, location, context, fix, and verification.
- **Status is tracked in one place — the `Status` column of the Summary table below** (single source of truth; there are deliberately no per-item checkboxes to keep in sync). When you start or finish an item, update its `Status` cell and drop the commit/PR link in its `PR / notes` cell.
- Work one item at a time, top-down.
- Per `CLAUDE.md`: keep `main.go` and `main_test.go` in lockstep; run `go test ./...` before finishing any change; run `./test/integration/run.sh` when changing labeling logic, Kubernetes interactions, manifests, or image behavior; update `README.md` + `deployment-example.yaml` when behavior or env vars change.

## Behavioral invariants (must hold after every change)
1. `LABEL_SELECTOR` is required; missing causes startup failure.
2. The primary pod always gets `primary=true`.
3. Non-primary with `LABEL_ALL=true` → `primary=false`; with `LABEL_ALL=false` → label removed (null strategic-merge patch).

## Status legend
Values for the `Status` column: `todo` · `wip` (in progress) · `done` · `wontfix`

---

## Summary (pick order)

| ID | Title | Dim | Severity | Effort | Status | PR / notes |
| --- | --- | --- | --- | --- | --- | --- |
| R1 | Reuse one MongoDB client instead of reconnecting every tick | perf | medium | M | done | unit+lint green |
| R2 | Skip patches when the label already matches desired state | perf | medium | S | done | unit+lint green; tests updated; integration test passed |
| R3 | Pin all GitHub Actions to commit SHAs | security | medium | S | done | 27 uses: pinned (2 workflows) |
| R4 | Harden the `mongo` container in the example manifest | security | medium | S | done | drop-ALL+noPrivEsc; non-root is follow-up; integration test passed |
| R5 | Reconcile once before the ticker (+ optional SIGTERM) | correctness | low* | XS | done | unit+lint green |
| R6 | Demote-before-promote patch ordering | correctness | low | S | done | reviewed; ordering test added; run.sh pending |
| R7 | Advance `lastPrimary` only after the promotion patch succeeds | correctness | low | S | done | reviewed; promotion-failure test added |
| R8 | Remove unused `actions: write` from build-and-push job | security | low | XS | done | reviewed safe (no Actions-API step) |
| R9 | Add resource requests/limits to example containers | security | low | S | done | review ok; run.sh pending |
| R10 | Add NetworkPolicy + README note (example Mongo is unauth/demo) | security | low | S | done | review ok (allows replica+loopback); run.sh pending |
| R11 | Per-call timeout contexts for List vs Patch | perf | low | S | done | reviewed; patchPrimaryLabel helper |
| R12 | Replace `flag.String`/`flag.Parse` side effect in `getKubeClientSet` | simplify | low | S | done | local FlagSet (no global flag.Parse/re-entrancy); --kubeconfig preserved + test |
| R13 | Add `-trimpath -ldflags="-s -w"` to Dockerfile build | perf | low | XS | done | Dockerfile + goreleaser; goreleaser check ok |
| R14 | Add `toolchain` directive to `go.mod` | security | low | XS | wontfix | n/a: `go 1.26.3` already pins the patch; explicit toolchain line is redundant and Go rejects it |
| R15 | Deduplicate patch-collection block in tests | simplify | low | XS | done | uses collectPrimaryPatchValues |
| R16 | Image provenance / SBOM / signing (cosign) | security | low | M | todo | |
| R17 | Prometheus metrics + `/healthz` surface | observability | info | M | todo | |
| R18 | Test error paths of `getMongoPrimary`, `New`, `getKubeClientSet` | correctness | low | M | todo | |
| R19 | Add rollout diagnostics to integration test | correctness | low | S | todo | |
| R20 | Build image from source instead of downloading release asset | correctness | info | S | todo | |
| R21 | Quieter example defaults: `DEBUG=false`, ConfigMap `0644` | hygiene | info | XS | todo | |
| R22 | `strings.Cut` instead of `strings.Split(host,".")[0]` | simplify | info | XS | todo | |
| R23 | Clarify `primaryLabelPatch` signature (avoid two-bool API) | simplify | info | S | todo | |
| R24 | Reduce `LookupEnv`+parse boilerplate in `getConfigFromEnvironment` | simplify | info | S | todo | |
| R25 | (Enhancement) Optional MongoDB auth/TLS env knobs | security | enh | M | todo | |

`*` R5 is low-severity but high-value/low-cost — strongly recommended in the first batch.

Effort: XS ≈ a few lines · S ≈ one function/file · M ≈ multi-file or new tests/infra.

---

## Tier 1 — highest value (do first)

### R1 · Reuse one MongoDB client instead of reconnecting every tick
- **Dimension:** performance · **Severity:** medium · **Files:** `main.go`
- **Location:** `getMongoPrimary` lines 233-263 (`mongo.Connect` ~240, `Ping` 250, `Disconnect` 244-249); wired via `primaryResolver` at line 75; driven by the `time.NewTicker(5s)` loop at line 313.
- **Problem:** Every 5s tick builds a brand-new client (`options.Client().ApplyURI(...).SetDirect(true)`), runs `Connect` → topology monitoring (2 sockets/server) → `Ping` → `hello` → `Disconnect`. That's ~17,280 full connect/discovery/teardown cycles/day, all discarded. mongo-driver v2 docs explicitly call this an anti-pattern: *"create a client once for each process and reuse it … avoid creating a new client for each request as this will increase latency."* `Client` is concurrency-safe with a built-in pool; there is a single sequential ticker, so a long-lived client is trivially safe.
- **Fix:** Create the client once and reuse it. Store `mongoClient *mongo.Client` on `Labeler`. Prefer **lazy init on first use** (connect once, cache, reuse) rather than connecting in `New()` — this preserves today's resilient behavior where a not-yet-ready Mongo at startup just logs an error and retries next tick (vs. failing `New()` hard). Per tick, only `Ping`+`hello` against the stored client. Disconnect once at shutdown (pairs with R5's signal handling). Tighten the per-call context from 20s to ~5-10s covering just ping+command, kept independent of `K8sRequestTimeout`. Optionally `SetMinPoolSize(1)`/`SetMaxPoolSize(1)` since only one connection is needed.
- **Invariant safety:** No labeling behavior changes; keep `getMongoPrimary` as the default resolver so the injected-`primaryResolver` test path in `main_test.go` still works.
- **Verify:** `go test ./...`; `./test/integration/run.sh` (touches Mongo interaction). No README/manifest change (no env vars change).

### R2 · Skip patches when the label already matches desired state
- **Dimension:** performance · **Severity:** medium · **Files:** `main.go`
- **Location:** patch loop lines 105-133; existing-label read at line 108; `Patch` call lines 123-129.
- **Problem:** `pod.Labels["primary"]` is read at line 108 only to choose the log message — it never gates the `Patch`. So **every** selected pod is patched on **every** tick even when its label already matches. For 3 pods that's ~51,840 PATCH calls/day for a topology that almost never changes. (Note: the apiserver no-ops a strategic-merge patch with no semantic change — it does *not* write etcd or bump resourceVersion — so the real cost is the request/auth/admission round-trip per pod per tick, not watch churn.)
- **Fix:** Before marshal/patch, compute desired state and `continue` when it already matches, using a two-value map read so an absent key is handled:
  - primary: `if pod.Labels["primary"] == "true" { continue }`
  - non-primary, `LabelAll=true`: `if pod.Labels["primary"] == "false" { continue }`
  - non-primary, `LabelAll=false`: `if _, ok := pod.Labels["primary"]; !ok { continue }`
  Keep the `lastPrimary`/transition-log bookkeeping (lines 108-115) **before** any `continue` so failover logging is unaffected. Side benefit: elides the per-pod `json.Marshal` allocation in steady state.
- **Invariant safety:** All three invariants preserved. Existing tests still pass — their fixtures start without the target value, so they still patch (`TestSetPrimaryLabel_LabelAllVariants`, `_PrimaryFailover`).
- **Verify:** `go test ./...`; `./test/integration/run.sh`.

### R3 · Pin all GitHub Actions to commit SHAs
- **Dimension:** security (supply chain) · **Severity:** medium · **Files:** `.github/workflows/ci-release.yml`, `.github/workflows/codeql-analysis.yml`
- **Location:** every `uses:` — e.g. `ci-release.yml` lines 29/32/37/62/84/100/111/137/163/166/170/188/198/220/252/267/270; `codeql-analysis.yml` lines 30/33/40.
- **Problem:** All actions are pinned to mutable version tags (`actions/checkout@v6.0.2`, `docker/build-push-action@v7.2.0`, `goreleaser/goreleaser-action@v7.2.2`, `codecov/...`, `helm/kind-action@...`, etc.). Tags can be force-moved; a compromised third-party action would run with this repo's `GITHUB_TOKEN`. The `release` (contents:write), `build-and-push` (packages:write), and `manifest` (packages:write) jobs use that token to create releases and push images to ghcr.io, so a re-pointed tag in those jobs directly poisons published artifacts (the tj-actions/changed-files class, March 2025).
- **Fix:** Pin each `uses:` to a full 40-char commit SHA with a trailing `# vX.Y.Z` comment, e.g. `uses: docker/build-push-action@<sha> # v7.2.0`. Prioritize third-party actions in the write-token jobs first (build-push, login, metadata, goreleaser, qemu, buildx), then first-party `actions/*` and `github/*`. Dependabot (`.github/dependabot.yml`, github-actions weekly) keeps SHAs bumped after the initial pin.
- **Invariant safety:** CI-only; no app behavior affected.
- **Verify:** push a branch / run `act` to confirm the workflow still resolves and runs.

### R4 · Harden the `mongo` container in the example manifest
- **Dimension:** security · **Severity:** medium · **Files:** `deployment-example.yaml` (+ maybe `README.md`)
- **Location:** `mongo` container lines 110-121 (no `securityContext`); contrast sidecar lines 141-151; pod-level seccomp lines 107-109.
- **Problem:** The sidecar is fully hardened but the `mongo` container has **no** container-level `securityContext`. Its custom `command: ["/bin/bash","/scripts/start-mongo.sh"]` (line 113) bypasses the image entrypoint's user-switching, so `mongod` runs as **root**, with the **default (non-dropped) capabilities**, `allowPrivilegeEscalation` defaulting to **true**, and a **writable root filesystem**. This is the canonical copy-paste manifest and it contradicts the repo's own `CLAUDE.md`/`AGENTS.md` "preserve secure defaults" guidance.
- **Fix:** Add a container-level `securityContext` to `mongo`. Minimum (zero-risk): `allowPrivilegeEscalation: false` and `capabilities: { drop: [ALL] }`. Fuller parity: `runAsNonRoot: true`, `runAsUser: 999`/`runAsGroup: 999` (the `mongodb` user in the official image), plus pod-level `securityContext.fsGroup: 999` so the emptyDir-backed `/data/db` (lines 153-154) is writable (required because the custom command bypasses the entrypoint chown). `readOnlyRootFilesystem: true` is achievable but needs an extra writable emptyDir at `/tmp` (mongod + bootstrap mongosh write there) — call this out rather than enabling blind. `--logpath /proc/1/fd/1` is fine under non-root.
- **Invariant safety:** Manifest-only; labeling logic untouched.
- **Verify:** `./test/integration/run.sh` (changes manifest/image behavior). Consider a README note that the example should mirror the sidecar's hardening.

### R5 · Reconcile once before the ticker (+ optional SIGTERM handling)
- **Dimension:** correctness/robustness · **Severity:** low (but high value, ~3 lines) · **Files:** `main.go`
- **Location:** `main()` lines 313-319 (`time.NewTicker(5s)` then `for range ticker.C`). `setPrimaryLabel` is called only at line 316 — no pre-loop reconcile.
- **Problem:** `time.NewTicker` doesn't fire until the first 5s elapses, so after every startup/crash-restart the pod `primary` labels are stale for ≥5s. If a Mongo election happened while the sidecar was down, the dead/old primary still carries `primary=true` and the write Service keeps routing at it for the whole window. Also: no signal handling, so on rollout the process is SIGKILLed after the grace period.
- **Fix:** Extract a `reconcile()` closure (keep the existing error logging) and call it once **before** entering the loop. Optionally: `ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)` (add `os/signal`, `syscall` imports), switch to `for { select { case <-ctx.Done(): return; case <-ticker.C: reconcile() } }`, and thread `ctx` into `setPrimaryLabel`/`getMongoPrimary` (replacing `context.Background()`) so in-flight work actually cancels. The run-once-before-loop change is the highest-value piece; signal plumbing is optional polish (each tick already opens/closes its own client today — pairs with R1's persistent client + shutdown disconnect).
- **Invariant safety:** Only timing/control-flow changes; all invariants preserved. Tests call `setPrimaryLabel` directly, so none break.
- **Verify:** `go test ./...`. (`Benefit at startup depends on Mongo being reachable; if not, the first call just logs and retries.`)

---

## Tier 2 — worth doing (low effort, low risk)

### R6 · Demote-before-promote patch ordering
- **Dimension:** correctness · **Severity:** low · **Files:** `main.go`
- **Location:** patch loop lines 105-133 (iterates `pods.Items` in List order).
- **Problem:** During a real failover the loop patches in List order. If the new primary appears before the old one, both transiently carry `primary=true`, so the `mongo` Service (selector `primary: "true"`, `deployment-example.yaml:83-85`) briefly has two endpoints. (Not a data-integrity bug — Mongo rejects writes to a secondary with `NotWritablePrimary`; it's a transient routing/retry window that self-heals next tick.)
- **Fix:** Two passes over the same data: first patch all non-primary pods (demotions), then patch the primary last (promotion). This narrows the window to a brief *zero*-primary state, which fails safe (writes retry until endpoints repopulate). Optionally give the promotion `Patch` its own fresh-deadline context.
- **Invariant safety:** Primary still ends at `primary=true`; `LABEL_ALL` semantics unchanged.
- **Verify:** `go test ./...`; add a regression test feeding pods in primary-first order and asserting the primary's patch is issued last; `./test/integration/run.sh`.

### R7 · Advance `lastPrimary` only after the promotion patch succeeds
- **Dimension:** correctness · **Severity:** low · **Files:** `main.go`
- **Location:** `lastPrimary` set at line 114 (inside the `pod.Labels["primary"] != "true"` branch), before the promotion `Patch` at lines 123-129 which may fail (line 131 returns).
- **Problem:** `l.lastPrimary` advances based on the stale List snapshot **before** the promotion patch is confirmed and regardless of its result. If that patch fails, `lastPrimary` has already moved to the new primary; on the next tick the new primary still isn't `"true"`, so the branch re-enters and emits a misleading `primary changed from=<new> to=<new>` every tick until the patch succeeds — i.e. exactly during an incident. (Genuine first-time failover logs correctly; demotions of the former primary are never logged — a cosmetic asymmetry.)
- **Fix:** Compute the intended log (detected vs changed), perform the promotion `Patch`, and only on success update `l.lastPrimary` and emit the log. Optionally add a one-line demotion log to remove the asymmetry.
- **Invariant safety:** `lastPrimary` is log-only; no control-flow/payload impact.
- **Verify:** `go test ./...`.

### R8 · Remove unused `actions: write` from the build-and-push job
- **Dimension:** security (least privilege) · **Severity:** low · **Files:** `.github/workflows/ci-release.yml`
- **Location:** `build-and-push` permissions block lines 128-131 (`actions: write` at line 129).
- **Problem:** No step in the job touches the Actions API (checkout, `gh release download`, ghcr login, docker build/push, set output). `actions: write` (cancel/re-run runs, manage artifacts/caches) needlessly widens blast radius for a job that already holds `packages: write` and runs on tag pushes. Other jobs correctly scope to `contents: read`.
- **Fix:** Delete line 129, leaving `contents: read` + `packages: write`.
- **Verify:** confirm the tag-triggered workflow still builds/pushes (all steps work with the reduced scope).

### R9 · Add resource requests/limits to example containers
- **Dimension:** security/reliability · **Severity:** low · **Files:** `deployment-example.yaml` (+ maybe `README.md`)
- **Location:** `mongo` container lines 111-121, `labeler` container lines 123-151 — neither has a `resources` block.
- **Problem:** With no requests/limits the pod is **BestEffort** QoS (first OOM-killed), and a leak/spike can starve co-located workloads. The example otherwise demonstrates secure defaults, so this is an inconsistency users inherit.
- **Fix:** Add conservative requests/limits to both (tune per workload). Starting point — labeler: `requests cpu 10m / mem 32Mi`, `limits cpu 100m / mem 64Mi` (don't set the mem limit below the Go runtime's RSS or it OOM-loops); mongo: `requests cpu 250m / mem 256Mi`, `limits mem ~512Mi-1Gi`, consider omitting the CPU limit for the DB to avoid throttling.
- **Verify:** `./test/integration/run.sh`. Keep README in sync if it documents the example.

### R10 · NetworkPolicy + README note that the example Mongo is unauthenticated/demo-only
- **Dimension:** security · **Severity:** low · **Files:** `deployment-example.yaml`, `README.md`
- **Location:** Services lines 66-88 (port 27017); ConfigMap `mongod --bind_ip_all` with no `--auth` lines 36-42; no NetworkPolicy object anywhere.
- **Problem:** In a cluster without default-deny, any pod can reach an unauthenticated `mongod` on 27017 and read/write the DB. The README calls the file "an example deployment manifest" with no warning.
- **Fix (primary, CNI-independent):** Add a README note (and/or a comment block atop `deployment-example.yaml`) stating the example Mongo runs without auth/TLS and is demonstration-only; production must enable Mongo auth/keyFile + TLS. **Optional:** add an ingress NetworkPolicy restricting 27017 to `role=mongo` + the consumer's selector — but note it only takes effect on a CNI that enforces NetworkPolicies.
- **Verify:** docs change; `./test/integration/run.sh` if a NetworkPolicy is added.

### R11 · Per-call timeout contexts for List vs Patch
- **Dimension:** performance/robustness · **Severity:** low · **Files:** `main.go`
- **Location:** single `context.WithTimeout(..., K8sRequestTimeout)` created at lines 81-82, shared by the `List` (line 84) and every `Patch` (line 124).
- **Problem:** The default 10s is a wall-clock budget for the whole sequence, not per-request: a slow List eats into the patch budget, and the loop aborts on the first patch error (lines 130-132). Low impact here because N is tiny (replica sets are 3-7 pods) and a timed-out tick self-heals in 5s.
- **Fix:** Give the List its own `context.WithTimeout` and derive a fresh per-Patch context inside the loop (cancel each via defer/explicit cancel to avoid leaking until loop end). Optional polish.
- **Invariant safety:** Context scoping only.
- **Verify:** `go test ./...`.

### R12 · Replace `flag.String`/`flag.Parse` side effect in `getKubeClientSet`
- **Dimension:** simplification · **Severity:** low · **Files:** `main.go`
- **Location:** `getKubeClientSet` lines 210-219 (`flag.String("kubeconfig", ...)` at 212/214, `flag.Parse()` at 216); `flag` import line 6 (used only here).
- **Problem:** Registers a package-level flag and calls `flag.Parse()` inside a constructor-style helper — mutates global `flag.CommandLine`, consumes `os.Args`, and is non-re-entrant (a second call would panic `flag redefined: kubeconfig`). Inconsistent with the env-var config style used everywhere else. Dead code in production (in-cluster path is taken when `KUBERNETES_SERVICE_HOST` is set).
- **Fix:** Replace with a direct lookup honoring `KUBECONFIG`, falling back to the default path:
  ```go
  kubeconfig := os.Getenv("KUBECONFIG")
  if kubeconfig == "" {
      if home := homeDir(); home != "" {
          kubeconfig = filepath.Join(home, ".kube", "config")
      }
  }
  config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
  ```
  Then remove the now-unused `flag` import.
- **Invariant safety:** In-cluster production path untouched; empty path falls back to default loading rules.
- **Verify:** `go test ./...`; `go build`.

### R13 · Add `-trimpath -ldflags="-s -w"` to the Dockerfile build
- **Dimension:** performance/reproducibility · **Severity:** low · **Files:** `Dockerfile` (+ `.goreleaser.yaml`)
- **Location:** `Dockerfile` line 18 (`RUN ... go build -o /primary-sidecar`).
- **Problem:** The release binary is stripped via goreleaser (`-s -w`), but the directly-built image uses a plain `go build` — retaining DWARF/symbol tables (larger layer) and embedding absolute build paths (`/src`, GOPATH) in the shipped artifact. `Dockerfile.dist` sidesteps this by copying the goreleaser binary, so the two image variants are inconsistent.
- **Fix:** `go build -trimpath -ldflags="-s -w" -o /primary-sidecar`. Also add `-trimpath` to the `.goreleaser.yaml` build block (goreleaser doesn't add it implicitly) for path-free, reproducible release binaries.
- **Verify:** build the image; `./test/integration/run.sh`.

### R14 · Add a `toolchain` directive to `go.mod`
- **Dimension:** security/reproducibility · **Severity:** low · **Files:** `go.mod`
- **Location:** line 3 (`go 1.26.3`); no `toolchain` line.
- **Problem:** Without a `toolchain` directive, Go's auto-selection may transparently download and run a different patch toolchain across CI (`go-version-file: go.mod`), local dev, and the `golang:1.26.3-bookworm` builder — defeating reproducible builds and allowing an unreviewed toolchain at build time.
- **Fix:** Add `toolchain go1.26.3`; bump deliberately rather than via implicit auto-download.
- **Verify:** `go build`, `go test ./...`.

### R15 · Deduplicate the patch-collection block in tests
- **Dimension:** simplification · **Severity:** low · **Files:** `main_test.go`
- **Location:** inline block in `TestSetPrimaryLabel_LabelAllVariants` lines 236-253 vs the `collectPrimaryPatchValues` helper lines 404-425 (already used by `TestSetPrimaryLabel_PrimaryFailover` at line 449).
- **Problem:** The inline block is a verbatim reimplementation of the helper — duplicated test logic that can drift.
- **Fix:** Replace the inline loop with:
  ```go
  primaryValuesByPod := collectPrimaryPatchValues(t, k8sClient)
  assert.Equal(t, tt.expectedPrimaryByPod, primaryValuesByPod)
  ```
- **Verify:** `go test ./...`.

---

## Tier 3 — observability & supply-chain hardening (nice to have)

### R16 · Container image provenance / SBOM / signing
- **Dimension:** security (supply chain) · **Severity:** low · **Files:** `.github/workflows/ci-release.yml`, `.goreleaser.yaml`
- **Location:** `build-and-push` docker step lines 197-207 (no `provenance:`/`sbom:`); no cosign anywhere; `.goreleaser.yaml` has no `sboms:`/`signs:` and `checksums.txt` is unsigned.
- **Problem:** Consumers can't verify who built the public image, from what source, or what it contains, nor detect tampering of images/tarballs.
- **Fix:** Add `provenance: mode=max` and `sbom: true` to build-push; add `permissions: id-token: write` + `sigstore/cosign-installer` + `cosign sign --yes` on pushed digests (or `actions/attest-build-provenance`); add `sboms:` (syft) and `signs:` (cosign keyless) to `.goreleaser.yaml`. **Caveat:** this pipeline pushes per-arch images then stitches with `docker buildx imagetools create`, which does **not** propagate per-image attestations into the final manifest — generate attestations at the manifest step or switch to a single multi-platform build/push. **Highest-value single step:** `cosign sign` + signed `checksums.txt`.
- **Verify:** tag a test release / `act`; `cosign verify` the artifacts.

### R17 · Prometheus metrics + `/healthz` surface
- **Dimension:** observability · **Severity:** info · **Files:** `main.go` (+ `deployment-example.yaml`)
- **Location:** reconcile loop lines 315-318 (logs only); no HTTP server.
- **Problem:** No way to alert on a silently-failing sidecar (Mongo unreachable, RBAC denied) beyond scraping logs, and no SLO signal for label staleness — meaningful for a component that gates Service routing.
- **Fix:** Add a minimal HTTP server with `promhttp` exposing e.g. `reconcile_total{result="success|error"}`, `primary_changes_total`, and a `last_successful_reconcile_timestamp` gauge. The same server can back a freshness-based readiness/liveness probe (combine with R5/R12). Wire probes in `deployment-example.yaml`.
- **Verify:** `go test ./...`; `./test/integration/run.sh`.

### R18 · Test error paths of `getMongoPrimary`, `New`, `getKubeClientSet`
- **Dimension:** correctness (test coverage) · **Severity:** low · **Files:** `main_test.go`, `main.go`
- **Location:** untested: `getMongoPrimary` 233-263, `New` 61-70, `getKubeClientSet` 200-224, `configureLogger` 44-59. (codecov target is `auto`, so the gap won't fail CI.)
- **Problem:** `getMongoPrimary` embeds non-trivial logic (URI formatting, `SetDirect`, connect/ping/runcommand error wrapping) run every tick, with zero failure-mode coverage. The mongo path was never made injectable despite the existing `primaryResolver` indirection.
- **Fix:** Make the mongo command runner injectable (or split bson handling — already in `parsePrimaryPodName` — from transport). Add a table test asserting the error strings (`connect to mongo at`, `ping mongo at`, `run hello command`) so error-context regressions are caught.
- **Verify:** `go test ./...`.

### R19 · Add rollout diagnostics to the integration test
- **Dimension:** correctness (CI ergonomics) · **Severity:** low · **Files:** `test/integration/run.sh`
- **Location:** `kubectl apply`/`set image`/`rollout status` lines 70-72 (no failure diagnostics), vs the label-wait diagnostics at lines 104-112.
- **Problem:** If the StatefulSet never becomes ready (mongo crashloop, wrong-arch image, failed `rs.initiate`), the script dies on `rollout status` with only its terse message — no `describe`/`get pods`/container logs to triage in CI.
- **Fix:** Wrap the apply/rollout section so non-zero exit (or rollout timeout) dumps `kubectl get pods -o wide`, `kubectl describe statefulset/mongo`, and per-pod/per-container logs before exiting — mirroring the label-wait failure branch. Consider an `ERR` trap that dumps cluster state once.
- **Verify:** run `./test/integration/run.sh`; optionally force a failure to confirm diagnostics fire.

### R20 · Build the image from source instead of downloading the release asset
- **Dimension:** correctness (supply chain) · **Severity:** info · **Files:** `.github/workflows/ci-release.yml`
- **Location:** `build-and-push` "Download release binary" step lines 151-159 (`gh release download "${TAG}"`); `needs: release` line 122.
- **Problem:** The image is built from the goreleaser GitHub Release asset, not the tagged source. A partial/failed/edited release, a retried run after assets rotated, or any drift bakes a stale/mismatched binary into the published image — so the image isn't guaranteed to match the source at that SHA.
- **Fix:** Build the binary from source inside the job (the `nektos/act` branch at lines 145-149 shows the exact `go build`), or verify the downloaded archive against goreleaser's `checksums.txt` before packaging.
- **Verify:** tag a test release / `act`.

### R21 · Quieter example defaults (`DEBUG=false`, ConfigMap `0644`)
- **Dimension:** hygiene · **Severity:** info · **Files:** `deployment-example.yaml`
- **Location:** `DEBUG: "true"` line 139; `LABEL_ALL: "true"` line 137; ConfigMap `defaultMode: 0755` line 158.
- **Problem:** The canonical copy-paste manifest defaults to the most verbose log level (a debug line per pod per 5s tick). The `0755` mount adds an unneeded execute bit (bash reads the script; mount is already `readOnly`). Neither is a security exposure (pod names are already authorized-visible; the script is non-secret and public in the repo).
- **Fix:** Set `DEBUG: "false"` (or omit to inherit the `InfoLevel` code default) with a comment that it's for troubleshooting. Keep `LABEL_ALL: "true"` (a deliberate default) — optionally a one-line comment on the remove-vs-false trade-off. Optionally `defaultMode: 0644` (note: does **not** change world-readability — purely drops the redundant exec bit).
- **Verify:** `./test/integration/run.sh`.

### R22 · `strings.Cut` instead of `strings.Split(host,".")[0]`
- **Dimension:** simplification · **Severity:** info · **Files:** `main.go`
- **Location:** `parsePrimaryPodName` line 284.
- **Problem:** `strings.Split(host, ".")[0]` allocates a full slice of all labels to read index 0.
- **Fix:** `primaryPodName, _, _ := strings.Cut(host, ".")` — byte-identical result in both dot/no-dot cases, no intermediate slice. `strings` already imported; behavior unchanged (empty host still rejected at line 285).
- **Verify:** `go test ./...`.

### R23 · Clarify `primaryLabelPatch` signature (avoid the two-bool API)
- **Dimension:** simplification · **Severity:** info · **Files:** `main.go`
- **Location:** `primaryLabelPatch(value bool, remove bool)` lines 137-150; caller lines 116-117.
- **Problem:** Two booleans with an implicit relationship — `(value=true, remove=true)` is unrepresentable in practice but expressible in the type. NOTE the existing `any(strconv.FormatBool(value))` box at line 138 is **load-bearing**, not redundant: without it the local would be `string` and `labelValue = nil` at line 141 wouldn't compile. The box only becomes removable after restructuring.
- **Fix (optional):** Pass a single `any` label (nil = remove), moving the `"true"`/`"false"`/nil decision into the caller via a `switch` on `currentPodIsPrimary`/`LabelAll`.
- **Invariant safety:** Tests assert on patch JSON (`labels["primary"]` = `"true"`/`"false"`/nil), which is unchanged.
- **Verify:** `go test ./...`.

### R24 · Reduce `LookupEnv`+parse boilerplate in `getConfigFromEnvironment`
- **Dimension:** simplification · **Severity:** info · **Files:** `main.go`
- **Location:** `getConfigFromEnvironment` lines 152-198 (five repeated lookups; bool/duration error-wrap repeated at 174-196; reused `l`/`ok`).
- **Problem:** Repetitive, though the current explicit form is idiomatic and perfectly readable — marginal benefit.
- **Fix (optional):** Local `envString`/`envBool`/`envDuration` helpers, **keeping the exact `invalid <KEY> value %q` phrasing** the tests assert (`main_test.go:165/174/183`) and keeping DEBUG's `true→DebugLevel` mapping outside the helper. Reasonable to leave as-is.
- **Verify:** `go test ./...`.

### R25 · (Enhancement) Optional MongoDB auth/TLS env knobs
- **Dimension:** security (enhancement, not a defect) · **Files:** `main.go`, `README.md`, `deployment-example.yaml`
- **Location:** `getMongoPrimary` lines 237-239 (`mongodb://<addr>` + `SetDirect(true)`, no `SetAuth`/`SetTLSConfig`); `Config`/`getConfigFromEnvironment` have no auth/TLS fields.
- **Context:** This was **considered and judged NOT a vulnerability** in the current design: `hello` is exempt from Mongo access control, the default/example topology is pod-loopback (`localhost:27017`), and no secrets are ever transmitted. It's a **feature gap**, not a bug. But it's the single most useful security enhancement if anyone runs Mongo non-co-located, cross-node, or with `--tlsMode requireTLS`.
- **Fix:** Add optional `MONGO_USERNAME`/`MONGO_PASSWORD` (or full URI), `authSource`, and TLS/CA env knobs, applied to client options only when set (preserving the loopback-no-auth default). Document in README; never log credentials.
- **Verify:** `go test ./...`; `./test/integration/run.sh`.

---

## Confirmed good — preserve as-is (no action)
- **RBAC is least-privilege:** namespaced `Role`, `pods: get/list/patch` only (`deployment-example.yaml:6-13`). `get` is technically unused by the code — could trim to `list/patch`, but not worth a standalone PR.
- **Sidecar hardening + images:** `runAsNonRoot`, drop-ALL caps, `readOnlyRootFilesystem`, seccomp `RuntimeDefault` (`:141-151`); `CGO_ENABLED=0` static binary on `distroless/static-debian13:nonroot`, `USER 65532` (`Dockerfile:29`, `Dockerfile.dist:14`).
- **Spoofed-`hello` trust boundary is bounded:** the selector-scoped membership check (`main.go:94-103`) ensures a malicious Mongo response can only move the label among already-matching pods, never patch an arbitrary pod. **Keep that guard** (do not patch by the raw parsed name).

## Considered but rejected (do NOT implement)
- **Merge the two pod passes / drop the first loop** — the first loop is a guard ensuring zero pods are patched when the primary is absent; `TestSetPrimaryLabel_PrimaryNotFound` asserts exactly that. Merging would patch-then-error and break it.
- **Static sentinel error for "primary not found"** — motivated by `err113`/`perfsprint`, but neither is enabled in `.golangci.yml`. Pure taste.
- **Remove the "redundant" first `configureLogger` call** (`main.go:292`) — deliberate two-phase init so the config-failure `Fatal` (line 296) gets console formatting before `LogLevel` is known.
- **"No Mongo auth/TLS" as a vulnerability** — reframed as the R25 enhancement; the current loopback/no-secret default is intentional and sound.
- **Error messages embed address/selector** — host:port and selectors are operational identifiers, not secrets; logging them is correct and useful.

---

*Generated from an adversarially-verified audit (39 agents: 5 finders → per-finding verification → completeness critic): 26 findings confirmed and 7 added by the completeness critic, with 6 refuted. These were consolidated into the 25 tracked items above (R1–R25) — overlapping findings were merged (e.g. the startup first-reconcile, SIGTERM, and ticker-delay findings all map to R5; the DEBUG and ConfigMap-mode findings to R21), and confirmed-but-non-actionable findings are recorded under "Confirmed good — preserve as-is" and "Considered but rejected" rather than as R items. The IDs are deliberately not 1:1 with raw findings, and there is no R26.*
