/**
 * HTTP client: polls GET /api/protocol, parses JSON, atomically updates TxState.
 */
#include "http_config.h"
#include "config.h"
#include "ui_menu.h"
#include <WiFi.h>
#include <HTTPClient.h>
#include <ArduinoJson.h>
#include "tsf_time.h"
#include <cstring>
#include <esp_wifi.h>

// --- Firmware log buffer for remote diagnostics ---
static char     s_txLogBuf[512];
static int      s_txLogLen  = 0;
static uint32_t s_lastLogSendMs = 0;
static const uint32_t LOG_SEND_INTERVAL_MS = 10000;

namespace tx {

void httpConfigAppendLog(const char* msg) {
    if (!msg || !msg[0]) return;
    int n   = strlen(msg);
    int rem = (int)sizeof(s_txLogBuf) - s_txLogLen - 1;
    if (rem > 0) {
        int copy = (n < rem) ? n : rem;
        memcpy(s_txLogBuf + s_txLogLen, msg, copy);
        s_txLogLen += copy;
        s_txLogBuf[s_txLogLen] = '\0';
    }
}

void httpConfigSendLogs() {
    return; // Disabled in production to keep Wi-Fi airtime clear for low-jitter UDP telemetry
    
    uint32_t now = millis();
    if (now - s_lastLogSendMs < LOG_SEND_INTERVAL_MS) return;
    if (s_txLogLen == 0) return;
    if (WiFi.status() != WL_CONNECTED) return;
    s_lastLogSendMs = now;
    static HTTPClient http;
    static WiFiClient tcp;
    char url[128];
    snprintf(url, sizeof(url), "http://" HTTP_SERVER_HOST ":%d/api/logs", HTTP_SERVER_PORT);
    http.begin(tcp, url);
    http.setReuse(true);
    http.useHTTP10(false);
    http.setTimeout(600);
    http.addHeader("Content-Type", "text/plain");
    http.addHeader("X-Device-ID", "tranceiver");
    http.addHeader("Connection", "keep-alive");
    http.POST((uint8_t*)s_txLogBuf, s_txLogLen);
    http.end();
    s_txLogLen = 0;
}

static uint32_t s_lastPoll = 0;
static bool s_wifiStarted = false;
static uint32_t s_pollBackoffMs = HTTP_POLL_MS;

static Preset parsePreset(const char* p) {
    if (!p) return Preset::Manual;
    // Server-stepped presets are applied directly as Manual steps on the device
    if (strcmp(p, "fast-scan") == 0 || strcmp(p, "phase-scan") == 0 || strcmp(p, "dual-asym") == 0)
        return Preset::Manual;
    if (strcmp(p, "baseline") == 0) return Preset::Baseline;
    if (strcmp(p, "am-sine") == 0) return Preset::AmSine;
    if (strcmp(p, "burst") == 0) return Preset::Burst;
    if (strcmp(p, "scan") == 0) return Preset::Scan;
    if (strcmp(p, "chirp") == 0) return Preset::Chirp;
    if (strcmp(p, "dual-tone") == 0) return Preset::DualTone;
    if (strcmp(p, "phase") == 0) return Preset::Phase;
    if (strcmp(p, "intent") == 0) return Preset::Intent;
    return Preset::Manual;
}

static ProtoMode parseProtoMode(const char* mode) {
    if (!mode) return ProtoMode::Stereo;
    if (strcmp(mode, "independent") == 0) return ProtoMode::Independent;
    if (strcmp(mode, "dual-tone") == 0) return ProtoMode::DualTone;
    if (strcmp(mode, "phase") == 0 || strcmp(mode, "phase-scan") == 0) return ProtoMode::PhaseScan;
    return ProtoMode::Stereo;
}

static ChannelSel parseChannelSel(const char* ch) {
    if (!ch) return ChannelSel::Both;
    if (strcmp(ch, "L") == 0) return ChannelSel::L;
    if (strcmp(ch, "R") == 0) return ChannelSel::R;
    return ChannelSel::Both;
}

static void parseMode(const char* mode, TxState& state) {
    if (!mode) return;
    if (strcmp(mode, "independent") == 0 || strcmp(mode, "dual-tone") == 0) state.mode = Mode::Independent;
    else if (strcmp(mode, "stereo") == 0) state.mode = Mode::Stereo;
    else if (strcmp(mode, "phase") == 0 || strcmp(mode, "phase-scan") == 0) state.mode = Mode::Phase;
}

static void parseWave(const char* wave, ChannelState& ch) {
    if (!wave) return;
    if (strcmp(wave, "triangle") == 0) ch.wave = Wave::Triangle;
    else if (strcmp(wave, "square") == 0) ch.wave = Wave::Square;
    else ch.wave = Wave::Sine;
}

static void parseChannel(JsonObject obj, ChannelState& ch) {
    if (obj.containsKey("freq"))  ch.freqHz   = (float)obj["freq"].as<double>();
    if (obj.containsKey("wave")) parseWave(obj["wave"].as<const char*>(), ch);
    if (obj.containsKey("phase")) ch.phaseDeg = (int)obj["phase"].as<int>();
    if (obj.containsKey("enabled")) ch.enabled = obj["enabled"].as<bool>();
    ch.clamp();
}

static void applyFromRoot(TxState& state, ProtocolRuntime& proto, JsonObject root) {
    JsonObject txObj = root;
    if (root.containsKey("transmitter") && root["transmitter"].is<JsonObject>()) {
        txObj = root["transmitter"].as<JsonObject>();
    }
    if (txObj.containsKey("mode")) parseMode(txObj["mode"].as<const char*>(), state);
    if (txObj.containsKey("left"))  parseChannel(txObj["left"].as<JsonObject>(), state.left);
    if (txObj.containsKey("right")) parseChannel(txObj["right"].as<JsonObject>(), state.right);

    if (root.containsKey("protocol") && root["protocol"].is<JsonObject>()) {
        JsonObject p = root["protocol"].as<JsonObject>();
        if (p.containsKey("preset")) proto.preset = parsePreset(p["preset"].as<const char*>());
        if (p.containsKey("mode")) proto.mode = parseProtoMode(p["mode"].as<const char*>());
        if (p.containsKey("channel")) proto.channel = parseChannelSel(p["channel"].as<const char*>());
        if (p.containsKey("sessionState")) {
            const char* st = p["sessionState"].as<const char*>();
            proto.sessionRunning = (st && strcmp(st, "running") == 0);
        }
        if (p.containsKey("panicStop")) proto.panicStop = p["panicStop"].as<bool>();
        if (p.containsKey("muteLeft")) proto.muteLeft = p["muteLeft"].as<bool>();
        if (p.containsKey("muteRight")) proto.muteRight = p["muteRight"].as<bool>();
        if (p.containsKey("averaging")) proto.averaging = p["averaging"].as<bool>();
        if (p.containsKey("intensity")) proto.intensity = p["intensity"].as<int>();
        if (p.containsKey("intensityR")) proto.intensityR = p["intensityR"].as<int>();
        else proto.intensityR = proto.intensity;
        if (p.containsKey("windowMs")) proto.windowMs = p["windowMs"].as<int>();
        if (p.containsKey("repeats")) proto.repeats = p["repeats"].as<int>();
        if (p.containsKey("rampMs")) proto.rampMs = p["rampMs"].as<int>();
        if (p.containsKey("frequency")) proto.frequency = (float)p["frequency"].as<double>();
        if (p.containsKey("freqStep")) proto.freqStep = (float)p["freqStep"].as<double>();
        if (p.containsKey("phaseOffset")) proto.phaseOffset = p["phaseOffset"].as<int>();
        if (p.containsKey("amFreqHz")) proto.amFreqHz = (float)p["amFreqHz"].as<double>();
        if (p.containsKey("amDepthPct")) proto.amDepthPct = p["amDepthPct"].as<int>();
        if (p.containsKey("burstOnMs")) proto.burstOnMs = p["burstOnMs"].as<int>();
        if (p.containsKey("burstOffMs")) proto.burstOffMs = p["burstOffMs"].as<int>();
        if (p.containsKey("burstCount")) proto.burstCount = p["burstCount"].as<int>();
        if (p.containsKey("burstPauseMs")) proto.burstPauseMs = p["burstPauseMs"].as<int>();
        if (p.containsKey("probingMode")) {
            const char* s = p["probingMode"].as<const char*>();
            if (s) { strncpy(proto.probingMode, s, sizeof(proto.probingMode) - 1); proto.probingMode[sizeof(proto.probingMode) - 1] = '\0'; }
        }
        if (p.containsKey("envType")) {
            const char* s = p["envType"].as<const char*>();
            if (s) { strncpy(proto.envType, s, sizeof(proto.envType) - 1); proto.envType[sizeof(proto.envType) - 1] = '\0'; }
        }
        if (p.containsKey("envFreqHz")) proto.envFreqHz = (float)p["envFreqHz"].as<double>();
        if (p.containsKey("envDepth")) proto.envDepth = (float)p["envDepth"].as<double>();
        if (p.containsKey("envCounterPhase")) proto.envCounterPhase = p["envCounterPhase"].as<bool>();

        // SYNC_TRIGGER
        if (p.containsKey("syncTrigger") && p["syncTrigger"].is<JsonObject>()) {
            JsonObject st = p["syncTrigger"].as<JsonObject>();
            uint32_t newSession = st["sessionId"] | 0u;
            if (newSession != proto.syncSessionId && newSession != 0) {
                uint64_t tgt  = (uint64_t)(st["targetTsfUs"] | (double)0.0);
                float    fHz  = (float)(st["freqHz"] | 0.0);
                uint64_t nowTsf = tsfNowUs();
                char buf[128];
                snprintf(buf, sizeof(buf),
                    "[SYNC-PARSE] session=%u tgt=%llu now=%llu delta=%.1fms f=%.1fHz\n",
                    newSession,
                    (unsigned long long)tgt,
                    (unsigned long long)nowTsf,
                    (double)((int64_t)tgt - (int64_t)nowTsf) / 1000.0,
                    fHz);
                httpConfigAppendLog(buf);
                proto.syncTargetUs  = tgt;
                proto.syncFreqHz    = fHz;
                proto.syncSessionId = newSession;
            }
        }

        if (p.containsKey("intent") && p["intent"].is<JsonObject>()) {
            JsonObject it = p["intent"].as<JsonObject>();
            if (it.containsKey("pingIntervalMs")) proto.pingIntervalMs = it["pingIntervalMs"].as<int>();
            if (it.containsKey("pingLengthMs")) proto.pingLengthMs = it["pingLengthMs"].as<int>();
            if (it.containsKey("randomize")) proto.intentRandomize = it["randomize"].as<bool>();
            if (it.containsKey("cue")) proto.intentCue = it["cue"].as<bool>();
        }
        if (p.containsKey("mode")) parseMode(p["mode"].as<const char*>(), state);
        if (p.containsKey("frequency")) {
            state.left.freqHz = (float)p["frequency"].as<double>();
            if (p.containsKey("frequencyR")) state.right.freqHz = (float)p["frequencyR"].as<double>();
            else state.right.freqHz = state.left.freqHz;
        }
        if (p.containsKey("freqStep") && p.containsKey("mode")) {
            const char* m = p["mode"].as<const char*>();
            if (m && strcmp(m, "dual-tone") == 0) {
                float f = state.left.freqHz;
                float step = (float)p["freqStep"].as<double>();
                state.right.freqHz = f + step;
            }
        }
        if (p.containsKey("phaseOffset")) state.right.phaseDeg = p["phaseOffset"].as<int>();
        if (p.containsKey("phaseL")) state.left.phaseDeg = p["phaseL"].as<int>();
        else if (p.containsKey("phaseOffset")) state.left.phaseDeg = 0;
        bool enL = true;
        bool enR = true;
        if (p.containsKey("channel")) {
            const char* ch = p["channel"].as<const char*>();
            if (ch && strcmp(ch, "L") == 0) {
                enR = false;
            } else if (ch && strcmp(ch, "R") == 0) {
                enL = false;
            }
        }
        if (p.containsKey("muteLeft") && p["muteLeft"].as<bool>()) enL = false;
        if (p.containsKey("muteRight") && p["muteRight"].as<bool>()) enR = false;
        if (p.containsKey("panicStop") && p["panicStop"].as<bool>()) {
            enL = false;
            enR = false;
        }
        if (p.containsKey("sessionState")) {
            const char* st = p["sessionState"].as<const char*>();
            if (st && strcmp(st, "stopped") == 0) {
                enL = false;
                enR = false;
            }
        }
        state.left.enabled = enL;
        state.right.enabled = enR;
    }
    state.clamp();

    // --- Sensors (sensors[]) ---
    if (root.containsKey("sensors") && root["sensors"].is<JsonArray>()) {
        JsonArray arr = root["sensors"].as<JsonArray>();
        uint8_t cnt = 0;
        for (JsonObject s : arr) {
            if (cnt >= tx::MAX_SENSORS) break;
            proto.sensors[cnt].id     = s["id"]    | (uint8_t)0;
            proto.sensors[cnt].snr    = s["snr"]   | 0.0f;
            proto.sensors[cnt].mag    = s["mag"]   | 0.0f;
            proto.sensors[cnt].freqHz = s["freq"]  | 0.0f;
            proto.sensors[cnt].ageMs  = s["ageMs"] | (uint32_t)9999;
            cnt++;
        }
        for (uint8_t i = cnt; i < tx::MAX_SENSORS; i++) proto.sensors[i] = tx::SensorInfo{};
        proto.sensorCount = cnt;
    }
}

ProtocolRuntime defaultProtocolRuntime() {
    return ProtocolRuntime{};
}

void httpConfigApplyJson(TxState& state, ProtocolRuntime& proto, const char* json, size_t len) {
    StaticJsonDocument<4096> doc;
    DeserializationError err = deserializeJson(doc, json, len);
    if (err) return;
    applyFromRoot(state, proto, doc.as<JsonObject>());
}

void httpConfigInit() {
    WiFi.mode(WIFI_STA);
    WiFi.setSleep(false);
    esp_wifi_set_ps(WIFI_PS_NONE);
    WiFi.setTxPower(WIFI_POWER_8_5dBm);
    WiFi.begin(WIFI_SSID, WIFI_PASS);
    s_wifiStarted = true;
    // TSF is synchronized in hardware via 802.11 WiFi Beacons — SNTP is not required
}

bool httpConfigPoll(TxState& state, ProtocolRuntime& proto) {
    uint32_t now = millis();
    if (now - s_lastPoll < s_pollBackoffMs) return false;
    s_lastPoll = now;

    if (WiFi.status() != WL_CONNECTED) return false;

    static HTTPClient http;
    static WiFiClient tcp;
    char url[128];
    snprintf(url, sizeof(url), "http://" HTTP_SERVER_HOST ":%d" HTTP_CONFIG_PATH, HTTP_SERVER_PORT);
    if (!http.begin(tcp, url)) {
        uiSetApiStatus(false, -1);
        s_pollBackoffMs = HTTP_POLL_MS * 2;
        return false;
    }
    http.setReuse(true);
    http.useHTTP10(false);
    http.setTimeout(800);
    // Report current hardware TSF to server for SyncTrigger offset tracking
    char tsfBuf[24];
    snprintf(tsfBuf, sizeof(tsfBuf), "%llu", (unsigned long long)tsfNowUs());
    http.addHeader("X-Device-TSF", tsfBuf);
    http.addHeader("Connection", "keep-alive");
    int code = http.GET();
    if (code != HTTP_CODE_OK) {
        http.end();
        uiSetApiStatus(false, code);
        s_pollBackoffMs = HTTP_POLL_MS * 2;
        return false;
    }
    WiFiClient& stream = http.getStream();
    static StaticJsonDocument<4096> doc;
    doc.clear();
    DeserializationError err = deserializeJson(doc, stream);
    http.end();
    if (err) {
        uiSetApiStatus(false, -2);
        s_pollBackoffMs = HTTP_POLL_MS * 2;
        return false;
    }
    s_pollBackoffMs = HTTP_POLL_MS;
    applyFromRoot(state, proto, doc.as<JsonObject>());
    uiSetApiStatus(true, 200);
    char logLine[128];
    snprintf(logLine, sizeof(logLine),
        "[HTTP] OK preset=%d mode=%d f=%.1f run=%d session=%u\n",
        (int)proto.preset, (int)proto.mode, proto.frequency,
        (int)proto.sessionRunning, proto.syncSessionId);
    httpConfigAppendLog(logLine);
    return true;
}

} // namespace tx
