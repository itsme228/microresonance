#pragma once
#include <stdint.h>
#include <esp_wifi.h>

// WiFi TSF (Timing Synchronization Function) — hardware microsecond counter
// synchronized via 802.11 router beacon frames. Inter-device synchronization
// accuracy on the same WLAN: ~1-2 us (~0.2-0.36 deg at 311-500 Hz), providing
// deterministic phase coherence for I/Q lock-in demodulation.
//
// Requires an active WiFi station connection (WIFI_IF_STA). Returns 0 prior to connect().

static inline uint64_t tsfNowUs() {
    return (uint64_t)esp_wifi_get_tsf_time(WIFI_IF_STA);
}
