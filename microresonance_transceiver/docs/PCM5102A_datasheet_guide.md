# PCM5102A I2S DAC Hardware Integration & Troubleshooting Guide

Reference notes based on the Texas Instruments PCM5102A datasheet (SLAS859C) for driving dual acoustic exciters from an ESP32-S3 over 4-wire I2S.

---

## 1. Key Hardware Configuration Requirements

| # | Parameter | Datasheet Requirement (SLAS859C) |
| :--- | :--- | :--- |
| **1** | **FMT Pin (Pin 16)** | **Low = I2S (Philips)**, High = Left-Justified (Section 9.3.2.2). Must be tied to `GND` to match `I2S_PHILIPS_FORMAT`. |
| **2** | **LRCK / BCK Synchronization** | Section 9.3.2.1: If the relationship between `LRCK` and `BCK` is invalid for more than 4 `LRCK` periods, analog outputs are forced to bipolar zero until resynchronized. Using ESP32-S3 `use_apll = true` with external `MCLK` (`GPIO 15`) maintains deterministic clock ratios. |
| **3** | **XSMT Soft-Mute Pin (Pin 17)** | **High (`3.3V / AVDD`) = Un-muted**, Low = Soft-mute (Section 9.3.3). Tie directly to `3.3V` (`AVDD`). |
| **4** | **Supply Voltage Limits** | Section 8.1 Absolute Maximum: `AVDD`, `CPVDD`, `DVDD` and digital inputs must not exceed **3.9 V** (`3.3 V` nominal). |
| **5** | **SCK / System Clock (Pin 12)** | In 4-wire coherent mode, driven from ESP32-S3 `GPIO 15` (`I2S_USE_MCLK = 1`). In 3-wire mode, `SCK` must be tied firmly to `GND` so the internal PLL locks onto `BCK`. |
| **6** | **Output RC Filter** | Section 8.5 / Figure 33: Recommended post-DAC filter is a **470 Ω series resistor** followed by a **2.2 nF shunt capacitor to GND** on both `OUTL` and `OUTR` ($f_c \approx 150\text{ kHz}$). |

---

## 2. ESP32-S3 to PCM5102A Wiring

| Signal | ESP32-S3 GPIO | PCM5102A Pin | Description |
| :--- | :--- | :--- | :--- |
| `SCK / MCLK` | `GPIO 15` | Pin 12 (`SCK`) | External Master Clock (`I2S_USE_MCLK = 1`) |
| `BCK` | `GPIO 16` | Pin 13 (`BCK`) | Bit Clock (`64 × f_s` or `32 × f_s`) |
| `DIN` | `GPIO 17` | Pin 14 (`DIN`) | Serial Audio Data (16-bit stereo PCM) |
| `LCK / WS` | `GPIO 18` | Pin 15 (`LRCK`) | Word Select (`44.1 kHz`) |
| `FMT` | `GND` | Pin 16 (`FMT`) | Selects standard Philips I2S format |
| `XSMT` | `3.3V` | Pin 17 (`XSMT`) | Disables soft-mute |
| `VIN / AVDD` | `3.3V` | Pin 8, 1, 20 | Regulated `3.3 V` supply |
