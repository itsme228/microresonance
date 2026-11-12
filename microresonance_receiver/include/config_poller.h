#pragma once
#include <Arduino.h>
#include <stdint.h>

struct StreamEndpoint {
    String   host;
    uint16_t port;
    bool     useUDP;
    uint32_t version;
};

// DSP and TSF synchronization parameters from /api/protocol
struct SyncParams {
    float    demod_freq_hz;    // protocol.frequency
    uint64_t target_tsf_us;    // 802.11 TSF timestamp (us); 0 = no trigger armed
    uint32_t session_id;
    bool     is_raw;           // protocol.streamMode == "raw"
};

void ConfigPollerStart(const char* configHost,
                       uint16_t    configPort,
                       const char* configPath,
                       const char* fallbackDataHost,
                       uint16_t    fallbackDataPort,
                       bool        fallbackUseUDP,
                       uint32_t    pollMs);

StreamEndpoint ConfigPollerGetEndpoint();
SyncParams     ConfigPollerGetSyncParams();
