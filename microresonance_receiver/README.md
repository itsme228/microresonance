# Microresonance Receiver Firmware (ESP32-S3)

Real-time multi-modal coherent acoustic & physiological telemetry node for the **Microresonance** platform.

## Overview

The receiver node captures 3-axis micro-vibrations of the muscle waveguide alongside 6-axis inertial motion and surface electromyography (sEMG), stamping every window with hardware-synchronized 802.11 Wi-Fi **TSF (Timing Synchronization Function)** microsecond timestamps.

### Key Engineering Features

1. **Microsecond Coherent Timebase (`tsf_time.h`)**
   - Uses `esp_wifi_get_tsf_time(WIFI_IF_STA)` to lock the receiver's sampling timeline to the same 802.11 beacon clock used by the emitter's Numerically Controlled Oscillator (NCO).
   - Compensates ISR-to-SPI readout latency in `sensorTask` by measuring `drdyAgeUs` from the hardware `DRDY` rising-edge interrupt (`drdy_isr`) and back-dating the TSF timestamp (`tsf = tsfAfter - drdyAgeUs`), maintaining `< 2 µs` phase coherence (`< 0.36°` at 500 Hz).

2. **Hardware Cross-Talk Isolation via Asymmetric Dual-Core FreeRTOS Scheduling (`dsp_task.cpp`)**
   - **Core 1 (`sensorTask`, Priority 5 & `dspTask`, Priority 4):** Dedicated to deterministic 4000 Hz SPI readout of the **ADXL355** ultra-low-noise MEMS accelerometer (`±2g`, 20-bit, `~3.9 µg/LSB`) and non-blocking UDP packet dispatch.
   - **Core 0 (`lsmTask`, Priority 3):** Handles **LSM6DSOX** 6-axis IMU (`I2C` at 400 kHz, with 24-bit hardware timestamp registers `0x40..0x42`) and **sEMG** SAR ADC reads (`analogRead(GPIO1)`).
   - *Why this matters:* Executing SAR ADC conversions inside the high-rate SPI capture loop induces transient current spikes on the 3.3V rail that couple into the ADXL355 analog front-end. Isolating ADC and I2C transactions onto Core 0 eliminates supply-rail cross-talk and enables stable phase **hard-lock**.

3. **Zero-Fragmentation Low-Latency UDP Streaming (`dsp_packet.h`)**
   - Packs 120 samples (`30 ms` window at 4000 Hz) of 3-axis ADXL355 data, 30 slots of 6-axis LSM6DSOX IMU + hardware timestamps, and 120 sEMG samples into a fixed **1460-byte** `RawSyncPacket` (`magic = 0x4D58`), strictly below the 1472-byte IPv4 UDP MTU to prevent IP fragmentation.
   - Sets socket `IP_TOS = 0xE0` (**WMM `AC_VO` Voice priority**), disabling 802.11 A-MPDU frame aggregation in the Wi-Fi MAC layer for immediate packet transmission.

---

## Hardware Pinout (ESP32-S3)

| Subsystem | Interface | Signal | ESP32-S3 GPIO | Notes |
| :--- | :--- | :--- | :--- | :--- |
| **ADXL355** | SPI | `CS` | `GPIO 8` | `±2g` range, `4000 Hz` ODR |
| **ADXL355** | SPI | `MOSI` | `GPIO 9` | |
| **ADXL355** | SPI | `MISO` | `GPIO 10` | |
| **ADXL355** | SPI | `SCLK` | `GPIO 11` | |
| **ADXL355** | IRQ | `INT1` | `GPIO 12` | |
| **ADXL355** | IRQ | `DRDY` | `GPIO 13` | Hardware rising-edge ISR trigger |
| **LSM6DSOX** | I2C | `SDA` | `GPIO 3` | 400 kHz Fast-Mode I2C (`0x6B`) |
| **LSM6DSOX** | I2C | `SCL` | `GPIO 2` | |
| **LSM6DSOX** | IRQ | `INT1 / DRDY` | `GPIO 6` | Data-ready polling on Core 0 |
| **sEMG** | ADC | `OUT` | `GPIO 1` | Analog sEMG reference channel |

---

## Wire Protocol (`RawSyncPacket` — `0x4D58`)

Defined in [`include/dsp_packet.h`](include/dsp_packet.h) (`sizeof(RawSyncPacket) == 1460` bytes, packed little-endian):

```c
struct __attribute__((packed)) RawSyncPacket {
    uint16_t magic;                         // 0x4D58 (DSP_MAGIC_RAW)
    uint8_t  sensor_id;                     // Node ID (e.g., 4)
    uint8_t  reserved;
    uint64_t tsf_us;                        // 802.11 TSF timestamp (microseconds) at window start
    uint32_t window_samples;                // Number of ADXL355 samples (120 @ 4000 Hz = 30 ms)
    int16_t  raw_x[120];                    // Quantized g: q = round(g * 16384.0)
    int16_t  raw_y[120];
    int16_t  raw_z[120];

    uint16_t lsm_samples;                   // Number of IMU samples in window (up to 30)
    int16_t  lsm_accel_x[30];
    int16_t  lsm_accel_y[30];
    int16_t  lsm_accel_z[30];
    int16_t  lsm_gyro_x[30];
    int16_t  lsm_gyro_y[30];
    int16_t  lsm_gyro_z[30];
    uint32_t lsm_ts[30];                    // LSM6DSOX 24-bit hardware timestamp

    uint16_t emg_samples;                   // Number of sEMG samples (up to 120)
    uint16_t emg[120];                      // 12-bit SAR ADC readings
};
```

---

## Building and Flashing

Requires [PlatformIO](https://platformio.org/):

1. Configure your network SSID, password, and host IP in [`src/main.cpp`](src/main.cpp):
   ```cpp
   static const char* WIFI_SSID = "YOUR_WIFI_SSID";
   static const char* WIFI_PASS = "YOUR_WIFI_PASSWORD";
   static const char* SERVER_IP = "192.168.200.10";
   ```
2. Build and upload:
   ```bash
   pio run -e esp32-s3 -t upload
   pio device monitor -b 115200
   ```
