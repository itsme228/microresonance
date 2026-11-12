# Microresonance Transceiver Firmware (ESP32-S3)

TSF-locked active acoustic emitter node for coherent probing of biological waveguides.

## Overview

The transceiver generates a phase-continuous acoustic carrier (e.g., `311 Hz` or `500 Hz`) injected into the target muscle group via bone/tissue vibration transducers. To enable lock-in phase demodulation at the receiver without any physical wire between emitter and detector, the carrier phase is synthesized directly from the 802.11 Wi-Fi **TSF (Timing Synchronization Function)** hardware timer.

### Key Engineering Features

1. **Zero-Drift TSF-Locked Absolute Phase NCO (`src/main.cpp`)**
   - Instead of accumulating phase via a free-running local timer (`phase += dPhi`), which drifts due to crystal tolerance (`±10–20 ppm`), every audio sample's instantaneous phase is evaluated directly against the 64-bit Wi-Fi TSF clock (`tsfNowUs()`):
     ```cpp
     double tSample = (double)tsfBase * 1e-6 + (double)i / (double)kSampleRate;
     double curPL   = fmod(tSample * (double)freqL, 1.0) * 2.0 * M_PI;
     ```
   - Guarantees zero cumulative phase drift between emitter and receiver over arbitrary session durations.

2. **Coherent 4-Wire I2S DAC Output (`PCM5102A`)**
   - Uses ESP32-S3 **APLL** (`use_apll = true`) and external Master Clock (`MCLK` on `GPIO 15`) at `44.1 kHz` / 16-bit stereo to eliminate DAC PLL jitter.
   - Includes digital hard-limiting (`±0.98 FS`) and clickless envelope ramping (`rampSamples`).
   - Also includes bit-banged SPI driver (`ad9833.h/.cpp`) for dual **AD9833** DDS waveform generators and **SSD1315** OLED local UI (`ui_menu.h/.cpp`).

3. **Probing Modes & HTTP Synchronization (`http_config.h/.cpp`)**
   - Supports continuous single-tone probing (`Manual`/`Stereo`), dual-frequency MIMO differential probing (`DualTone`), quadrature phase stepping (`Phase`: `0°/90°/180°/270°`), frequency sweeps (`Scan` / `Chirp`), and amplitude-modulated envelopes (`AmSine`).
   - Polls `/api/protocol` via persistent HTTP Keep-Alive and supports microsecond-scheduled `SyncTrigger` session arming (`pendingTargetTsf`).

---

## Hardware Pinout (ESP32-S3-CAM / N16R8)

| Subsystem | Interface | Signal | ESP32-S3 GPIO | Notes |
| :--- | :--- | :--- | :--- | :--- |
| **PCM5102A DAC** | I2S (4-wire) | `BCK` | `GPIO 16` | Bit Clock (APLL-locked) |
| **PCM5102A DAC** | I2S (4-wire) | `LCK / WS` | `GPIO 18` | Word Select (`44.1 kHz`) |
| **PCM5102A DAC** | I2S (4-wire) | `DIN` | `GPIO 17` | 16-bit Stereo PCM Data |
| **PCM5102A DAC** | I2S (4-wire) | `SCK / MCLK` | `GPIO 15` | External Master Clock (`I2S_USE_MCLK=1`) |
| **OLED SSD1315** | I2C | `SDA / SCL` | `GPIO 20 / 19` | Local status & parameter display |
| **Buttons K1–K4** | GPIO | `K1..K4` | `21, 47, 48, 45` | `+/-`, field select, `L/R` channel toggle |
| **AD9833 Left** | SPI | `SDATA/SCLK/FSYNC` | `35, 36, 37` | Optional auxiliary DDS exciter L |
| **AD9833 Right** | SPI | `SDATA/SCLK/FSYNC` | `10, 8, 7` | Optional auxiliary DDS exciter R |

---

## Building and Flashing

Requires [PlatformIO](https://platformio.org/):

1. Configure your network SSID, password, and server host in [`include/config.h`](include/config.h):
   ```cpp
   #define WIFI_SSID        "YOUR_WIFI_SSID"
   #define WIFI_PASS        "YOUR_WIFI_PASSWORD"
   #define HTTP_SERVER_HOST "192.168.200.10"
   #define HTTP_SERVER_PORT 8080
   ```
2. Build and flash:
   ```bash
   pio run -e esp32-s3-cam -t upload
   pio device monitor -b 115200
   ```
