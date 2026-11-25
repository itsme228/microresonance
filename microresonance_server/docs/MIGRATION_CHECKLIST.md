# Server Migration Checklist (safe-commit sequence)

Goal: reduce `core` overload, remove temporary aliases, and move real app orchestration into `internal/app/*` without breaking runtime.

Legend:
- Risk: Low / Medium / High
- Verify: minimum command(s) to run after each commit

1. Core test build stabilization (already done)
- Scope:
  - fix `st any -> *PatternStore` cast in `core/live_sensor_test.go`
  - remove tests access to private `PatternStore.patterns` in `core/shape_pipeline_test.go`
  - expose `ChainTrivial` via `core/dynamics_alias.go`
- Risk: Low
- Verify: `go test -run '^$' ./internal/core/...`

2. Remove UI formatting duplication from domain (already done)
- Scope:
  - keep `formatFreqForCard/formatBandForCard` only in `core/pattern_ui_helpers.go`
  - remove duplicates from `domain/alphabet/pattern_store.go`
- Risk: Low
- Verify: `go test -run '^$' ./internal/domain/alphabet ./internal/core`

3. Trainer constant compatibility shim (already done)
- Scope:
  - add `ml/trainer/TrainerShapePoints` alias to domain constant
  - keep event-point constant tied to the same source
- Risk: Low
- Verify: `go test -run '^$' ./internal/ml/trainer`

4. Make `app/config` real: flags + env parsing
- Scope:
  - move CLI/env parsing from `core/main` to `internal/app/config`
  - keep `core` as consumer of typed config struct only
- Risk: Medium
- Verify: `go test -run '^$' ./...` and smoke-run receiver start

5. Make `app/runtime` real: lifecycle/wiring
- Scope:
  - move start/stop/orchestration from `core/main` to `internal/app/runtime`
  - leave `core` with business/pipeline logic only
- Risk: Medium
- Verify: boot app + graceful shutdown test

6. Split `ports/httpapi/config_store.go` (types/store/handlers/routes)
- Scope:
  - `types.go`, `store.go`, `handlers_protocol.go`, `routes.go`
  - no behavior changes, only file decomposition and constructor wiring
- Risk: Medium
- Verify: `go test -run '^$' ./internal/ports/httpapi`

7. Isolate diagnostics from runtime
- Scope:
  - move `core/pipeline_spectrum.go` into diagnostics package (`internal/diagnostics/...` or `core/diagnostics/...`)
  - keep explicit call-sites from tests/dev hooks only
- Risk: Medium
- Verify: target tests using spectrum logging

8. Ingest lock/contention cleanup
- Scope:
  - decode packets outside lock in `core/ingest_runtime.go`
  - switch to direct `binary.LittleEndian.Uint32` reads in hot loop
- Risk: Medium
- Verify: ingest bench or packet-rate smoke test

9. Start alias layer collapse: dynamics
- Scope:
  - replace `core` consumers from `dynamics_alias.go` to direct `domain/dynamics`
  - delete alias file when no references remain
- Risk: Medium
- Verify: `rg "core\..*Dynamics|dynamics_alias"` + `go test -run '^$' ./...`

10. Alias layer collapse: alphabet and detection/discovery
- Scope:
  - move imports in consumers to direct `domain/alphabet`, `domain/pipeline`, `ml/*`
  - remove now-unused `*_alias.go` files in small batches
- Risk: Medium/High (many touch points)
- Verify: compile-only all + selected runtime tests

11. Decompose `gpu_runtime.go`
- Scope:
  - extract `update_loop.go`, `input.go`, `draw_common.go`, `network_hooks.go`
  - keep same package and private state; no cross-package API change
- Risk: High
- Verify: `go test -run '^$' ./internal/core` + manual UI smoke

12. Decompose `gpu.go` state object
- Scope:
  - split `gpuGame` concerns into `state_ui.go`, `state_pipeline.go`, `state_trainer.go`
  - keep constructor + public API stable
- Risk: High
- Verify: UI boot + trainer interactions smoke

13. Pull shape-domain logic out of `core/shape_pipeline.go`
- Scope:
  - move pure domain decisions/heuristics to `domain/pipeline`
  - retain `core` for orchestration and side effects
- Risk: High
- Verify: existing shape pipeline tests + live sensor test (manual env)

14. Switch bootstrap to app-first entrypoint
- Scope:
  - `internal/app/bootstrap/app.go` should build runtime from `app/config` + `app/runtime` (not `core.Main()` passthrough)
- Risk: Medium
- Verify: run binary + integration smoke

15. Remove dead stubs and finalize boundaries
- Scope:
  - delete placeholder code in `app/config` and `app/runtime`
  - update docs with final package responsibilities
- Risk: Low
- Verify: full compile-only + selective real tests

## Batch strategy
- Max 1 structural concern per commit.
- After each commit: run compile-only all (`go test -run '^$' ./...`).
- For High-risk commits: split into prep commit (moves/renames) + behavior-preserving commit.
