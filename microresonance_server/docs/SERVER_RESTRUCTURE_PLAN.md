# Server Restructure Plan

## Goals

- Keep files focused: target `<= 600` lines, hard cap `<= 1000`.
- Separate runtime code from data/artifacts (`trainer_records`, models).
- Make pipeline changes predictable: clear module boundaries and public contracts.
- Reduce AI confusion: stable folder conventions, one responsibility per package.

## Current Pain Points (observed)

- Flat `server/*.go` with almost all code in `package main`.
- Mixed concerns in root: ingest, UI, HTTP, pipeline, ML, training persistence.
- Very large files (`gpu.go` ~3059 lines, `screen_patterns.go` ~1493, etc.).
- Data directories dominate repo noise (`trainer_records` ~2.3G, `trainer_dataset` ~157M).

## Target Layout

```text
server/
  cmd/
    receiverd/
      main.go
    trainerctl/
      main.go
    modelctl/
      main.go

  internal/
    app/
      bootstrap/
        app.go
        wiring.go
      config/
        flags.go
        env.go
      runtime/
        lifecycle.go
        scheduler.go

    domain/
      signal/
        model.go
        preprocess.go
        filters.go
        spectrum.go
      glyph/
        model.go
        extractor.go
        cluster.go
      alphabet/
        pattern_store.go
        reaction_store.go
        primary_store.go
      pipeline/
        params.go
        detect.go
        shape_pipeline.go
        metrics.go
      dynamics/
        sequence_store.go
        segmentation.go
      protocol/
        transmitter.go
        receiver.go
        session.go

    ports/
      httpapi/
        server.go
        protocol_handlers.go
        trainer_handlers.go
        vr_handlers.go
      ws/
        hub.go
        snapshot.go
      ingest/
        tcp_server.go
        udp_server.go
        packet_decoder.go
      ui/
        game.go
        layout.go
        input.go
        screens/
          raw.go
          bands.go
          discovery.go
          alphabet.go
          trainer.go
          meaning.go
          xcorr.go
      storage/
        actions_repo.go
        dataset_repo.go
        model_repo.go

    ml/
      queue/
        schema.go
        producer.go
      classifier/
        service.go
        ort_runner.go
        python_runner.go
      trainer/
        dataset_export.go
        model_meta.go

    platform/
      tls/
        cert.go
      gpu/
        fft_pool.go
      observability/
        logger.go
        metrics.go

  scripts/
    train/
      train_glyph_model.py
      train_ml_queue_model.py
      train_features_model.py
      export_glyph_onnx.py
    infer/
      run_glyph_model.py
      run_ml_queue_model.py
      state_space_event_model.py

  data/                    # gitignored runtime/generated data
    trainer_dataset/
    trainer_records/
    saved_model/
    saved_model_features/

  api/
    openapi.yaml

  docs/
    ...existing docs...
```

## Mapping: Current -> Target

- `main.go` -> `cmd/receiverd/main.go` + `internal/core/bootstrap/*` + `ports/ingest/*`
- `httpapi.go` -> `internal/ports/httpapi/*` + `internal/domain/protocol/*`
- `gpu.go` -> `internal/ports/ui/game.go`, `layout.go`, `input.go`, `screens/*.go`
- `screen_*.go` -> `internal/ports/ui/screens/*.go` (one screen = one file)
- `shape_pipeline.go`, `detection.go`, `preprocessing.go`, `residual.go` -> `internal/domain/pipeline/*` + `internal/domain/signal/*`
- `screen_patterns.go`, `reaction_alphabet.go`, `primary_alphabet.go` -> `internal/domain/alphabet/*`
- `dynamics.go` -> `internal/domain/dynamics/*`
- `glyph_event.go`, `shape_cluster.go`, `oscillo_glyph.go`, `shapes.go` -> `internal/domain/glyph/*`
- `vr_api.go`, `vr_ws.go` -> `internal/ports/httpapi/vr_handlers.go` + `internal/ports/ws/*`
- `trainer*.go`, `trainer_record.go`, `trainer_persist.go`, `trainer_tf.go` -> `internal/ml/trainer/*` + `internal/ports/storage/*`
- `glyph_classifier*.go`, `ml_worker.go`, `ml_queue.go`, `ml_ring_buffer.go` -> `internal/ml/classifier/*` + `internal/ml/queue/*`
- `gpu_pool.go`, `bandpass_iir.go`, `tls_cert.go` -> `internal/platform/gpu/*`, `internal/domain/signal/filters.go`, `internal/platform/tls/*`
- `train_*.py`, `run_*.py`, `export_*.py` -> `scripts/train/*`, `scripts/infer/*`
- `trainer_dataset`, `trainer_records`, `saved_model*` -> `data/*` (gitignored; configurable path)

## File Size and Ownership Rules

- One package = one bounded context, no cross-import cycles.
- One file = one main concept:
  - handler file: only route handlers + request/response DTOs.
  - store file: only state and methods.
  - algorithm file: one pipeline stage.
- Size policy:
  - soft limit: 350 lines
  - target max: 600 lines
  - hard max: 1000 lines (CI warning at 700, fail at >1000 except generated files)
- Public API placement:
  - exported types/functions in `*_api.go` or top of module file.
  - internal helpers in `*_internal.go`.

## Anti-Duplication Policy

- Introduce central contracts:
  - `internal/domain/pipeline/params.go` as single source for thresholds.
  - `internal/domain/glyph/model.go` for shared glyph/event structs.
  - `internal/ports/storage/model_repo.go` for model path/config access.
- Avoid duplicated transformations:
  - one normalizer in `signal/preprocess.go`.
  - one feature extractor in `glyph/extractor.go`.
  - one serializer per queue schema in `ml/queue/schema.go`.

## Integrating Future Ideas (MDPI and own docs)

- Keep model architecture experiments isolated in `internal/ml/*` + `scripts/train/*`.
- Represent multi-stream events explicitly:
  - `FrameSummary`, `EventGlyph`, `EventReaction` in `ml/queue/schema.go`.
- For intention-model R&D:
  - add `internal/ml/intention/` with:
    - `dataset_builder.go`
    - `sequence_encoder.go`
    - `cross_glyph_attention.go`
    - `inference_service.go`
- Domain layer remains stable while ML architectures evolve.

## Migration Plan (Safe, Incremental)

1. Create skeleton folders and move only entrypoint:
   - add `cmd/receiverd/main.go` calling existing constructors.
2. Extract config and packet ingest from `main.go`.
3. Split `gpu.go` into `ui/game.go` + `ui/screens/*.go` with zero logic change.
4. Move alphabet/pipeline/dynamics into `internal/domain/*`.
5. Move ML and trainer code into `internal/ml/*` and `internal/ports/storage/*`.
6. Move Python scripts to `scripts/*`; parameterize model/data paths via flags/env.
7. Move runtime artifacts to `data/*`; add/update `.gitignore`.
8. Add CI checks:
   - `go test ./...`
   - file-length guard
   - import-lint (no forbidden cross-layer imports).

## Definition of Done

- `server` root has no business-logic `.go` files except small compatibility wrappers.
- `cmd/receiverd` starts app and all tests pass.
- No non-generated file >1000 lines.
- Data/model artifacts are outside code-root modules and ignored by git.
- New feature (pipeline or ML) can be added by touching:
  - one domain package
  - one port adapter
  - one test package
  without edits across unrelated modules.
