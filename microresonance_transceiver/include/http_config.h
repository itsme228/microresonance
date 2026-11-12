/**
 * HTTP client: polls GET /api/protocol, parses JSON, atomically updates TxState.
 * Non-blocking timer-driven execution from loop().
 */
#ifndef HTTP_CONFIG_H
#define HTTP_CONFIG_H

#include "tx_state.h"
#include "config.h"
#include <Arduino.h>

namespace tx {

enum class Preset : uint8_t { Manual, Baseline, AmSine, Burst, Scan, Chirp, DualTone, Phase, Intent };
enum class ProtoMode : uint8_t { Independent, Stereo, DualTone, PhaseScan };
enum class ChannelSel : uint8_t { L, R, Both };

static constexpr uint8_t MAX_SENSORS = 8;

// Per-sensor telemetry summary received from server (sensors[] in /api/protocol).
struct SensorInfo {
    uint8_t  id     = 0;
    float    snr    = 0.0f;    // dB, peaks[0]
    float    mag    = 0.0f;    // g, peaks[0] magnitude
    float    freqHz = 0.0f;    // Hz demodulation carrier
    uint32_t ageMs  = 9999;    // ms since last packet on server
};

struct ProtocolRuntime {
    Preset     preset = Preset::Manual;
    ProtoMode  mode = ProtoMode::Stereo;
    ChannelSel channel = ChannelSel::Both;
    bool       sessionRunning = false;
    bool       panicStop = false;
    bool       muteLeft = false;
    bool       muteRight = false;
    bool       averaging = true;
    bool       intentRandomize = true;
    bool       intentCue = false;
    int        intensity = 50;   // 0..100 (Left channel gain)
    int        intensityR = 50;  // 0..100 (Right channel gain)
    int        windowMs = 400;
    int        repeats = 3;
    int        rampMs = 20;
    int        phaseOffset = 180;
    int        pingIntervalMs = 2000;
    int        pingLengthMs = 100;
    float      frequency = 311.0f;
    float      freqStep = 5.0f;
    // AM-sine mode: carrier + slow envelope (0.5-1.0 Hz, depth 60-70%)
    float      amFreqHz = 0.7f;
    int        amDepthPct = 65;
    // Burst diagnostics: ON 120-180 ms, OFF 350-500 ms, 10-15 bursts, 7-10 s pause
    int        burstOnMs = 150;
    int        burstOffMs = 400;
    int        burstCount = 12;
    int        burstPauseMs = 8000;
    // Probing envelope configuration
    char       probingMode[8] = "";
    char       envType[16] = "sine";
    float      envFreqHz = 1.0f;
    float      envDepth = 0.5f;
    bool       envCounterPhase = false;

    // SYNC_TRIGGER: coordinated start timestamp via WiFi TSF beacon clock
    uint64_t   syncTargetUs = 0;    // TSF us (WiFi beacon); 0 = no trigger armed
    float      syncFreqHz = 0.0f;
    uint32_t   syncSessionId = 0;

    // Active sensors reported by server
    SensorInfo sensors[MAX_SENSORS];
    uint8_t    sensorCount = 0;
};

ProtocolRuntime defaultProtocolRuntime();

/** Initialize WiFi station mode (non-blocking). */
void httpConfigInit();

/** Periodic poll called from loop(): checks WiFi, fetches protocol JSON, updates state. */
bool httpConfigPoll(TxState& state, ProtocolRuntime& proto);

/** Parse and apply a JSON payload to state and protocol runtime. */
void httpConfigApplyJson(TxState& state, ProtocolRuntime& proto, const char* json, size_t len);

/** Append a line to the firmware diagnostic log buffer. */
void httpConfigAppendLog(const char* msg);

/** Periodically flush buffered logs to server (POST /api/logs). */
void httpConfigSendLogs();

} // namespace tx

#endif
