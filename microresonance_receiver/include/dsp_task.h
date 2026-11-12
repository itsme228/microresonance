#pragma once
#include "dsp_packet.h"
#include <stdint.h>

// FreeRTOS task parameters: lsmTask (Core 0), sensorTask (Core 1), and dspTask (Core 1).

#define DSP_ODR_NOMINAL    4000   // Hz — nominal ADXL355 Output Data Rate
#define DSP_WINDOW_MS      30     // 30 ms = 120 samples at 4000 Hz
#define DSP_WINDOW_SAMPLES (DSP_ODR_NOMINAL * DSP_WINDOW_MS / 1000)  // 120

// Single 3-axis accelerometer sample with hardware 802.11 TSF timestamp.
struct SensorSample {
    float    x;
    float    y;
    float    z;
    uint64_t tsf_us;  // Hardware TSF timestamp (microseconds) at DRDY capture
};

struct DspTaskConfig {
    uint8_t   sensor_id;
    float*    demod_freq_hz;    // Pointer updated by config_poller
    uint64_t* target_tsf_us;    // Target TSF timestamp (us); 0 = no trigger armed
    bool*     is_raw;           // Compact or raw streaming mode
    void*     tcp_client;       // Optional WiFiClient*
    void*     tcp_mutex;        // Optional SemaphoreHandle_t
};

void dspTaskStart(const DspTaskConfig& cfg);
