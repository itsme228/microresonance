# Microresonance Telemetry Server (`receiverd`)

Go-based real-time acquisition, **802.11 TSF-coherent I/Q lock-in demodulation**, emitter control, and **raw telemetry dump recorder** for the Microresonance wireless neuromuscular interface.

## Core Capabilities

- **Primary Function — Telemetry & Dump Recording:**
  - Receives packed 1460-byte `RawSyncPacket` (`0x4D58`) UDP/TCP frames from `microresonance_receiver` (`ADXL355` 4 kHz + `LSM6DSOX` IMU + `sEMG` + hardware TSF timestamps).
  - Automatically streams per-frame multi-modal lock-in + IMU + sEMG telemetry to `data/physio/physio_raw_YYYYMMDD_HHMMSS.jsonl` (the exact format analyzed by [`../scripts/analyze_session.py`](../scripts/analyze_session.py) and [`../scripts/verify_paper_claims.py`](../scripts/verify_paper_claims.py)).
  - Supports on-demand raw session dump toggling (`REC DUMP` button + custom session name on the `FFT` screen).
- **Coherent 3-Axis Lock-In Demodulation:** Real-time quadrature (`I/Q`) demodulation against the 802.11 Wi-Fi TSF clock (`internal/domain/signal/demod.go`).
- **HTTP Config & TSF Sync API (`:8080`):** Serves emitter/receiver protocol configuration and measures `X-Device-TSF` clock offsets.
- **5 Signal-Processing GUI Screens (Ebiten):**
  - **`LOCK-IN`** — 3-axis lock-in magnitudes, unwrapped phases, differential phases (`Δφ_XY`, `Δφ_YZ`, `Δφ_XZ`), and per-axis FFT waterfalls.
  - **`FFT`** — Raw X/Y/Z oscillograms, FFT spectra, waterfalls, and raw session dump recorder.
  - **`DYN`** — Real-time phase/magnitude/gyro/sEMG history and polar/Lissajous orbits (`Z` key tares phase).
  - **`RAW`** — Causal time-domain IIR bandpass filterbank waveforms.
  - **`XCORR`** — Cross-correlation across sensors/axes.
  - Right panel: **`EMITTER`** (carrier tones, levels, waveform) and **`RECEIVER`** (demodulation link, probing presets, TSF sync diagnostics).

> **Note:** Earlier unfinished/experimental prototypes (glyph discovery, alphabet clustering, ML trainer, semantic meaning/bindings, VR WebSocket, multi-sensor tomography) are intentionally excluded from `main` and archived on the `wip/experimental-server-archive` branch.

## Build & Run

Requires Go 1.22+ (and standard X11/OpenGL dev headers on Linux for Ebiten GPU mode, or `-gpu=false` for terminal TUI mode).

```bash
cd microresonance_server
go build -o receiverd ./cmd/receiverd

# GPU dashboard + UDP ingest (:5555) + HTTP emitter/TSF API (:8080)
./receiverd -gpu -addr=:5555 -http=:8080 -https=""

# Headless / terminal TUI fallback
./receiverd -gpu=false -addr=:5555 -http=:8080 -https=""
```
