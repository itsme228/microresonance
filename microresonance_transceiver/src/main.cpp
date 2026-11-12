/**
 * ESP32-S3-CAM (N16R8) — TSF-locked active acoustic probe emitter.
 * PCM5102A (4-wire I2S stereo) via arduino-audio-tools I2SStream, SSD1315 OLED,
 * buttons K1-K4, and HTTP GET /api/protocol synchronization.
 * Non-blocking control loop.
 */
#include <Arduino.h>
#include <esp_timer.h>
#include "config.h"
#include "tx_state.h"
#include "ui_menu.h"
#include "http_config.h"
#include "tsf_time.h"
#include "AudioTools.h"

using namespace audio_tools;

namespace {
    tx::TxState state;
    tx::UiState ui;
    tx::ProtocolRuntime proto = tx::defaultProtocolRuntime();
    constexpr int kSampleRate = 44100;  // 44.1 kHz sample rate for PCM5102A
    constexpr float kAmp = 32767.0f;    // 16-bit full scale for PCM5102A
    float phaseL = 0.0f;
    float phaseR = 0.0f;
    float freqL = 311.0f;
    float freqR = 311.0f;
    tx::Wave waveL = tx::Wave::Sine;
    tx::Wave waveR = tx::Wave::Sine;
    bool enL = true;
    bool enR = true;
    float gainL = 0.5f;
    float gainR = 0.5f;
    float targetGainL = 0.5f;
    float targetGainR = 0.5f;
    uint32_t lastStepMs = 0;
    uint32_t lastPingMs = 0;
    uint32_t nextPingMs = 0;
    bool pingOn = true;
    int phaseIdx = 0;
    float scanFreq = 0.0f;
    int cycleCount = 0;
    tx::Preset lastPreset = tx::Preset::Manual;
    float lastProtoFreq = 0.0f;
    float lastProtoStep = 0.0f;
    bool phaseLinked = false;
    float phaseOffsetRad = 0.0f;
    int16_t i2sBuf[256 * 2];  // PCM5102A: 16-bit stereo DMA buffer
    int lastPhaseDegL = 0;
    int lastPhaseDegR = 0;
    tx::ProtoMode lastMode = tx::ProtoMode::Stereo;
    uint32_t lastLogMs = 0;
    I2SStream i2sOut;

    // SyncTrigger: last applied session_id and pending armed flag
    uint32_t appliedSyncSession = 0;
    bool     syncPending = false;
    uint64_t pendingTargetTsf = 0;
    float    pendingSyncFreq  = 0.0f;
}

static float renderWave(tx::Wave w, float phase) {
    // phase in radians [0, 2*PI]
    float p = phase;
    while (p < 0) p += 2.0f * PI;
    while (p >= 2.0f * PI) p -= 2.0f * PI;
    
    switch (w) {
        case tx::Wave::Triangle: {
            float t = p / (2.0f * PI);
            return (t < 0.5f) ? (t * 4.0f - 1.0f) : (3.0f - t * 4.0f);
        }
        case tx::Wave::Square:
            return (p < PI) ? 1.0f : -1.0f;
        default:
            return sinf(p);
    }
}

static float clampf(float v, float lo, float hi) {
    if (v < lo) return lo;
    if (v > hi) return hi;
    return v;
}

static void applyProtocol(uint32_t nowMs) {
    if (proto.preset != lastPreset) {
        lastPreset = proto.preset;
        lastStepMs = nowMs;
        lastPingMs = nowMs;
        nextPingMs = nowMs;
        pingOn = true;
        phaseIdx = 0;
        cycleCount = 0;
        scanFreq = proto.frequency;
        lastProtoFreq = proto.frequency;
        lastProtoStep = proto.freqStep;
        Serial.printf("[PRESET] changed to %d freq=%.1fHz run=%d\n",
            (int)proto.preset, proto.frequency, (int)proto.sessionRunning);
    }
    if (proto.frequency != lastProtoFreq || proto.freqStep != lastProtoStep) {
        // reset scan/chirp state when operator changes base or step
        lastProtoFreq = proto.frequency;
        lastProtoStep = proto.freqStep;
        scanFreq = proto.frequency;
        cycleCount = 0;
        lastStepMs = nowMs;
    }

    // Manual/DualTone/Phase: emit continuously (unless PanicStop) for lock-in synchronization.
    // Automated sweep/envelope modes (Scan, AmSine, Burst, Chirp) gate on sessionRunning below.
    bool running = !proto.panicStop;
    if (proto.preset == tx::Preset::Baseline) running = false;

    float baseFreqL = proto.frequency;
    float baseFreqR = proto.frequency;
    if (proto.mode == tx::ProtoMode::Stereo || proto.mode == tx::ProtoMode::PhaseScan) {
        baseFreqR = baseFreqL;
    } else if (proto.mode == tx::ProtoMode::DualTone) {
        baseFreqR = baseFreqL + proto.freqStep;
    } else {
        baseFreqL = state.left.freqHz;
        baseFreqR = state.right.freqHz;
    }

    float fL = baseFreqL;
    float fR = baseFreqR;
    int phaseOffset = proto.phaseOffset;
    bool burstGate = true;  // for Preset::Burst output gating

    if (proto.preset == tx::Preset::Scan) {
        if (scanFreq <= 0.0f) scanFreq = proto.frequency;
        if (proto.windowMs > 0 && nowMs - lastStepMs >= (uint32_t)proto.windowMs) {
            lastStepMs = nowMs;
            scanFreq += proto.freqStep;
            if (scanFreq > FREQ_MAX_HZ) {
                scanFreq = FREQ_MIN_HZ;
                cycleCount++;
            }
        }
        // Automated modes stop when sessionState=stopped
        if (proto.preset != tx::Preset::AmSine && proto.preset != tx::Preset::Burst && proto.preset != tx::Preset::Chirp &&
            proto.repeats > 0 && cycleCount >= proto.repeats) {
            running = false;
        }
        running = running && proto.sessionRunning;
        fL = scanFreq;
        fR = fL;
    } else if (proto.preset == tx::Preset::Chirp) {
        if (proto.windowMs > 0 && nowMs - lastStepMs >= (uint32_t)proto.windowMs) {
            lastStepMs = nowMs;
            cycleCount++;
        }
        float range = proto.freqStep * 20.0f;
        if (range < 100.0f) range = 100.0f;
        float start = proto.frequency;
        float end = clampf(start + range, FREQ_MIN_HZ, FREQ_MAX_HZ);
        float t = (proto.windowMs > 0) ? (float)(nowMs - lastStepMs) / (float)proto.windowMs : 0.0f;
        if (t > 1.0f) t = 1.0f;
        fL = start + (end - start) * t;
        fR = fL;
        running = running && proto.sessionRunning;
    } else if (proto.preset == tx::Preset::AmSine) {
        // Carrier + slow AM envelope (runs while sessionRunning)
        fL = proto.frequency;
        fR = fL;
        running = running && proto.sessionRunning;
    } else if (proto.preset == tx::Preset::Burst) {
        // Diagnostic burst train: ON/OFF series followed by pause
        fL = proto.frequency;
        fR = fL;
        running = running && proto.sessionRunning;
        int segMs = proto.burstOnMs + proto.burstOffMs;
        if (segMs <= 0) segMs = 1;
        int seriesMs = proto.burstCount * segMs;
        int cycleMs = seriesMs + proto.burstPauseMs;
        if (cycleMs <= 0) cycleMs = 1;
        uint32_t elapsed = (nowMs - lastStepMs) % (uint32_t)cycleMs;
        burstGate = (elapsed < (uint32_t)seriesMs) && ((elapsed % (uint32_t)segMs) < (uint32_t)proto.burstOnMs);
    } else if (proto.preset == tx::Preset::DualTone) {
        fL = proto.frequency;
        fR = proto.frequency + proto.freqStep;
    } else if (proto.preset == tx::Preset::Phase) {
        if (proto.windowMs > 0 && nowMs - lastStepMs >= (uint32_t)proto.windowMs) {
            lastStepMs = nowMs;
            phaseIdx = (phaseIdx + 1) % 4;
            if (phaseIdx == 0) cycleCount++;
        }
        static const int phases[4] = {0, 90, 180, 270};
        phaseOffset = phases[phaseIdx];
        fL = proto.frequency;
        fR = fL;
        if (proto.repeats > 0 && cycleCount >= proto.repeats) {
            running = false;
        }
    } else if (proto.preset == tx::Preset::Intent) {
        if (proto.sessionRunning) {
            if (!pingOn && nowMs >= nextPingMs) {
                pingOn = true;
                lastPingMs = nowMs;
            }
            if (pingOn && (nowMs - lastPingMs) >= (uint32_t)proto.pingLengthMs) {
                pingOn = false;
                int interval = proto.pingIntervalMs;
                if (proto.intentRandomize && interval > 50) {
                    int jitter = random(-interval / 4, interval / 4);
                    interval += jitter;
                    if (interval < 50) interval = 50;
                }
                nextPingMs = nowMs + (uint32_t)interval;
            }
        } else {
            pingOn = true;
        }
        fL = proto.frequency;
        fR = fL;
    }

    freqL = clampf(fL, FREQ_MIN_HZ, FREQ_MAX_HZ);
    freqR = clampf(fR, FREQ_MIN_HZ, FREQ_MAX_HZ);

    waveL = state.left.wave;
    waveR = state.right.wave;

    bool useL = true;
    bool useR = true;
    if (proto.channel == tx::ChannelSel::L) useR = false;
    if (proto.channel == tx::ChannelSel::R) useL = false;
    if (proto.muteLeft) useL = false;
    if (proto.muteRight) useR = false;
    if (proto.preset == tx::Preset::Burst) {
        useL = burstGate && useL;
        useR = burstGate && useR;
    }
    if (!running) {
        useL = false;
        useR = false;
    }
    // NOTE: Intent ping gating disabled for now to ensure continuous tone.
    enL = useL;
    enR = useR;

    float intensityL = clampf((float)proto.intensity / 100.0f, 0.0f, 1.0f);
    float intensityR = clampf((float)proto.intensityR / 100.0f, 0.0f, 1.0f);
    float gL = intensityL;
    float gR = intensityR;
    if (!enL) gL = 0.0f;
    if (!enR) gR = 0.0f;
    targetGainL = gL;
    targetGainR = gR;

    phaseLinked = (proto.mode == tx::ProtoMode::Stereo || proto.mode == tx::ProtoMode::PhaseScan || proto.preset == tx::Preset::Phase);
    phaseOffsetRad = (float)phaseOffset * (PI / 180.0f);

    if (proto.mode != lastMode || state.left.phaseDeg != lastPhaseDegL || state.right.phaseDeg != lastPhaseDegR) {
        lastMode = proto.mode;
        lastPhaseDegL = state.left.phaseDeg;
        lastPhaseDegR = state.right.phaseDeg;
        phaseL = (float)state.left.phaseDeg * (PI / 180.0f);
        if (!phaseLinked) {
            phaseR = (float)state.right.phaseDeg * (PI / 180.0f);
        }
    }
}

static void pumpAudio(int frames) {
    if (proto.panicStop) {
        gainL = 0.0f;
        gainR = 0.0f;
        targetGainL = 0.0f;
        targetGainR = 0.0f;
    }

    // Base timestamp for this DMA buffer in 802.11 TSF microseconds
    uint64_t tsfBase = tsfNowUs();
    double tBase = (double)tsfBase * 1e-6;

    int rampSamples = (proto.panicStop || proto.rampMs <= 0) ? 0 : (proto.rampMs * kSampleRate / 1000);
    float stepL = 0.0f;
    float stepR = 0.0f;
    if (rampSamples > 0) {
        stepL = (targetGainL - gainL) / (float)rampSamples;
        stepR = (targetGainR - gainR) / (float)rampSamples;
    } else if (!proto.panicStop) {
        gainL = targetGainL;
        gainR = targetGainR;
    }

    // Optional amplitude modulation (AM) envelope also locked to TSF
    bool useEnvelope = (proto.preset == tx::Preset::AmSine && (enL || enR));
    if (!useEnvelope && (enL || enR) && proto.probingMode[0] != '\0') {
        useEnvelope = (proto.envFreqHz > 0.0f && proto.envDepth >= 0.0f);
    }
    double amF = 0.7;
    double amD = 0.65;
    if (useEnvelope) {
        amF = (proto.probingMode[0] != '\0') ? (double)proto.envFreqHz : (double)proto.amFreqHz;
        amD = (proto.probingMode[0] != '\0') ? (double)proto.envDepth : ((double)proto.amDepthPct / 100.0);
    }

    const float kHardLimit = 0.98f;

    for (int i = 0; i < frames; i++) {
        if (rampSamples > 0 && !proto.panicStop) {
            gainL += stepL;
            gainR += stepR;
        }

        // Exact TSF time in seconds for sample i
        double tSample = tBase + (double)i / (double)kSampleRate;

        // Instantaneous carrier phase for sample i strictly locked to hardware TSF
        double curPL = fmod(tSample * (double)freqL, 1.0) * 2.0 * M_PI;
        double curPR = phaseLinked ? (curPL + (double)phaseOffsetRad)
                                   : fmod(tSample * (double)freqR, 1.0) * 2.0 * M_PI;

        float sL = enL ? renderWave(waveL, (float)curPL) : 0.0f;
        float sR = 0.0f;
        if (enR) {
            if (phaseLinked) {
                sR = renderWave(waveR, (float)(curPL + (double)phaseOffsetRad));
            } else {
                sR = renderWave(waveR, (float)curPR);
            }
        }

        if (useEnvelope) {
            double envP = fmod(tSample * amF, 1.0) * 2.0 * M_PI;
            float e = cosf((float)envP);
            float envValL = 1.0f + (float)amD * e;
            float envValR = proto.envCounterPhase ? (1.0f - (float)amD * e) : envValL;
            sL *= envValL;
            sR *= envValR;
        }

        // Hard limiting
        if (sL > kHardLimit) sL = kHardLimit;
        if (sL < -kHardLimit) sL = -kHardLimit;
        if (sR > kHardLimit) sR = kHardLimit;
        if (sR < -kHardLimit) sR = -kHardLimit;

        int16_t l = (int16_t)(sL * kAmp * gainL);
        int16_t r = (int16_t)(sR * kAmp * gainR);

#if I2S_SWAP_LR
        i2sBuf[i * 2 + 0] = r;
        i2sBuf[i * 2 + 1] = l;
#else
        i2sBuf[i * 2 + 0] = l;
        i2sBuf[i * 2 + 1] = r;
#endif
    }
    
    size_t len = (size_t)frames * 2U * sizeof(int16_t);
    i2sOut.write((uint8_t*)i2sBuf, len);
}

static void audioTask(void* /*arg*/) {
    while (true) {
        pumpAudio(256);
        uint32_t nowMs = millis();
        if (nowMs - lastLogMs >= 1000) {
            lastLogMs = nowMs;
#if I2S_MINIMAL_TEST
            // Serial.println("[AUDIO] MINIMAL 440Hz L=R");
#else
            // Serial.printf("[AUDIO] fL=%.1f fR=%.1f enL=%d enR=%d gainL=%.2f gainR=%.2f preset=%d run=%d\n",
            //               freqL, freqR, (int)enL, (int)enR, gainL, gainR,
            //               (int)proto.preset, (int)proto.sessionRunning);
#endif
        }
    }
}

// TX_LOG: buffers firmware events for server telemetry
static char s_txLogTmp[128];
#define TX_LOG(fmt, ...) do { \
    snprintf(s_txLogTmp, sizeof(s_txLogTmp), fmt, ##__VA_ARGS__); \
    /* Serial.print(s_txLogTmp); */ \
    tx::httpConfigAppendLog(s_txLogTmp); \
} while(0)

void setup() {
    // Force blue LED off to prevent power spikes and UART/Status blinking
    pinMode(2, OUTPUT);
    digitalWrite(2, LOW);
    pinMode(33, OUTPUT);
    digitalWrite(33, HIGH); // On Ai-Thinker it's inverted

    Serial.begin(115200);

    // PCM5102A via arduino-audio-tools I2SStream
    auto config = i2sOut.defaultConfig(TX_MODE);
    config.sample_rate = kSampleRate;
    config.channels = 2;
    config.bits_per_sample = 16;
    config.pin_bck = I2S_BCK_GPIO;
    config.pin_ws = I2S_LCK_GPIO;
    config.pin_data = I2S_DIN_GPIO;
    config.pin_mck = I2S_USE_MCLK ? I2S_SCK_GPIO : -1;
    config.use_apll = true;  // low-jitter APLL bit clock for PCM5102A
    config.i2s_format = I2S_PHILIPS_FORMAT;  // Philips I2S standard for PCM5102A
    if (!i2sOut.begin(config)) {
        Serial.println("[I2S] ERROR begin failed");
    }
#if !I2S_MINIMAL_TEST
    applyProtocol(millis());
#endif
    xTaskCreatePinnedToCore(audioTask, "audioTask", 4096, nullptr, 3, nullptr, AUDIO_TASK_CORE);
    Serial.printf("[I2S] BCK=%d WS=%d DIN=%d MCLK=%d use_mclk=%d rate=%d ch=2 bits=16 philips%s\n",
                  I2S_BCK_GPIO, I2S_LCK_GPIO, I2S_DIN_GPIO, I2S_SCK_GPIO, I2S_USE_MCLK, kSampleRate,
                  I2S_MINIMAL_TEST ? " MINIMAL_TEST=1" : "");

#if !I2S_MINIMAL_TEST
    tx::uiInit();
    tx::httpConfigInit();
    state.clamp();
    ui.dirty = true;
#endif
}

void loop() {
#if I2S_MINIMAL_TEST
    delay(10);
    return;
#endif
    tx::uiPollButtons(state, ui);
    applyProtocol(millis());

    if (tx::httpConfigPoll(state, proto))
        ui.dirty = true;

    // Handle new SyncTrigger session
    if (proto.syncSessionId != appliedSyncSession && proto.syncSessionId != 0) {
        appliedSyncSession = proto.syncSessionId;
        pendingTargetTsf   = proto.syncTargetUs;
        pendingSyncFreq    = (proto.syncFreqHz > 0.0f) ? proto.syncFreqHz : proto.frequency;
        syncPending        = (pendingTargetTsf > 0);
        uint64_t nowTsf    = tsfNowUs();
        TX_LOG("[SYNC-ARM] session=%u tgt=%llu now=%llu delta=%.1fms f=%.1fHz\n",
            appliedSyncSession,
            (unsigned long long)pendingTargetTsf,
            (unsigned long long)nowTsf,
            (double)((int64_t)pendingTargetTsf - (int64_t)nowTsf) / 1000.0,
            pendingSyncFreq);
    }

    // Monitor TSF target activation
    if (syncPending) {
        uint64_t nowTsf = tsfNowUs();
        if (nowTsf >= pendingTargetTsf) {
            syncPending = false;
            TX_LOG("[SYNC-FIRED] session=%u tsf=%llu f=%.1fHz\n",
                appliedSyncSession,
                (unsigned long long)nowTsf,
                pendingSyncFreq);
        } else {
            static uint32_t s_lastSyncWaitMs = 0;
            uint32_t nowMs2 = millis();
            if (nowMs2 - s_lastSyncWaitMs >= 100) {
                s_lastSyncWaitMs = nowMs2;
            }
        }
    }

    // Periodic emission status heartbeat every 30 s
    {
        static uint32_t s_emitLogMs = 0;
        uint32_t nowMs3 = millis();
        if (!proto.panicStop && (enL || enR) && nowMs3 - s_emitLogMs >= 30000) {
            s_emitLogMs = nowMs3;
            uint64_t tsfNow = tsfNowUs();
            TX_LOG("[EMIT] tsf=%llu t=%.3fs fL=%.1fHz run=%d session=%u\n",
                (unsigned long long)tsfNow,
                (double)tsfNow * 1e-6,
                freqL,
                (int)proto.sessionRunning,
                proto.syncSessionId);
        }
    }

    if (tx::uiApiNeedsRedraw())
        ui.dirty = true;

    tx::uiDrawMenu(state, ui, proto);

    // Flush firmware logs if enabled
    tx::httpConfigSendLogs();
}
