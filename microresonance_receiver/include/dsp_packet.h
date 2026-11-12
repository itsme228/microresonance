#pragma once
#include <stdint.h>

// Wire format v2: Receiver -> Host telemetry packets.

#define DSP_MAGIC        0x4D53u
#define DSP_MAGIC_RAW    0x4D58u
#define DSP_MAX_PEAKS    8
#define DSP_SPEC_BANDS   32
#define DSP_RAW_SAMPLES  120
#define DSP_RAW_Q_G      16384.0f
#define DSP_PACKET_LEN   (183 + DSP_RAW_SAMPLES * 4)

struct __attribute__((packed)) DspPeak {
    float freq_hz;
    float magnitude;
    float phase_deg;   // 0-360 deg, referenced to 802.11 TSF clock
    float snr;         // dB
};

struct __attribute__((packed)) DspPacket {
    uint16_t magic;                    // DSP_MAGIC
    uint8_t  sensor_id;
    uint8_t  n_peaks;                  // Active entries in peaks[]
    uint64_t tsf_us;                   // 802.11 TSF timestamp (us) at window start
    float    demod_freq_hz;
    uint32_t window_samples;
    DspPeak  peaks[DSP_MAX_PEAKS];     // 128 bytes
    uint8_t  spectrum[DSP_SPEC_BANDS]; // Log-magnitude 0-255
    uint8_t  flags;                    // bit0=link_visible, bit1=saturated
    uint16_t battery_mv;
    float    raw_z[DSP_RAW_SAMPLES];
};

// Maximum LSM6DSOX slots per packet: 30 -> total packet size 1460 bytes < 1472 UDP MTU
// Prevents IPv4 fragmentation and Wi-Fi packet loss.
#define DSP_LSM_SLOTS 30

// Ultra-low-latency multi-modal frame for coherent RAW SYNC streaming
struct __attribute__((packed)) RawSyncPacket {
    uint16_t magic;                         // DSP_MAGIC_RAW (0x4D58)
    uint8_t  sensor_id;
    uint8_t  reserved;
    uint64_t tsf_us;                        // 802.11 TSF timestamp (us) at window start
    uint32_t window_samples;                // Valid ADXL355 samples in arrays
    int16_t  raw_x[DSP_RAW_SAMPLES];        // Quantized g: q = round(g * DSP_RAW_Q_G)
    int16_t  raw_y[DSP_RAW_SAMPLES];
    int16_t  raw_z[DSP_RAW_SAMPLES];

    uint16_t lsm_samples;
    int16_t  lsm_accel_x[DSP_LSM_SLOTS];
    int16_t  lsm_accel_y[DSP_LSM_SLOTS];
    int16_t  lsm_accel_z[DSP_LSM_SLOTS];
    int16_t  lsm_gyro_x[DSP_LSM_SLOTS];
    int16_t  lsm_gyro_y[DSP_LSM_SLOTS];
    int16_t  lsm_gyro_z[DSP_LSM_SLOTS];
    uint32_t lsm_ts[DSP_LSM_SLOTS];         // LSM6DSOX 24-bit hardware timestamp

    uint16_t emg_samples;
    uint16_t emg[DSP_RAW_SAMPLES];
};

// 16 + 120*6 + 2 + 30*12 + 30*4 + 2 + 120*2 = 1460 bytes < MTU 1472
#define RAW_SYNC_PACKET_LEN (16 + DSP_RAW_SAMPLES * 6 + 2 + DSP_LSM_SLOTS*12 + DSP_LSM_SLOTS*4 + 2 + DSP_RAW_SAMPLES * 2)

#ifdef __cplusplus
static_assert(sizeof(DspPacket) == DSP_PACKET_LEN, "DspPacket size mismatch");
static_assert(sizeof(RawSyncPacket) == RAW_SYNC_PACKET_LEN, "RawSyncPacket size mismatch");
#endif
