#include "config_poller.h"
#include "tsf_time.h"

#include <ArduinoJson.h>
#include <HTTPClient.h>
#include <WiFi.h>
#include <WiFiClient.h>
#include <freertos/FreeRTOS.h>
#include <freertos/semphr.h>
#include <freertos/task.h>

namespace {

SemaphoreHandle_t gMu   = nullptr;
TaskHandle_t      gTask = nullptr;

StreamEndpoint gEndpoint = {"", 0, false, 0};
SyncParams     gSync     = {311.0f, 0, 0};

String   gConfigHost;
uint16_t gConfigPort    = 0;
String   gConfigPath;
uint32_t gPollMs        = 2000;
uint32_t gFallbackVer   = 1;
uint32_t gBackoffMs     = 0;

// Parses the JSON response from /api/protocol and updates gEndpoint and gSync.
static bool parseResponse(const String& body) {
    JsonDocument doc;
    if (deserializeJson(doc, body) != DeserializationError::Ok) return false;

    JsonObject root = doc.as<JsonObject>();

    // --- StreamEndpoint (receiver.{server,port,proto}) ---
    {
        JsonObjectConst rcv;
        if (root["receiver"].is<JsonObject>())      rcv = root["receiver"].as<JsonObjectConst>();
        else                                         rcv = root;

        const char* host  = rcv["server"] | (const char*)nullptr;
        int         port  = rcv["port"]   | 0;
        const char* proto = rcv["proto"]  | "tcp";

        if (host && host[0] != '\0' && port > 0 && port < 65536) {
            StreamEndpoint next;
            next.host   = String(host);
            next.port   = (uint16_t)port;
            next.useUDP = (strcmp(proto, "udp") == 0);
            uint32_t wireVer = root["version"] | 0u;

            bool changed = (next.host != gEndpoint.host) ||
                           (next.port != gEndpoint.port) ||
                           (next.useUDP != gEndpoint.useUDP);
            if (changed) {
                next.version  = (wireVer > 0) ? wireVer : ++gFallbackVer;
                gEndpoint     = next;
            } else if (wireVer > gEndpoint.version) {
                gEndpoint.version = wireVer;
            }
        }
    }

    // --- SyncParams (protocol.{frequency/receiverFreq, syncTrigger, streamMode}) ---
    if (root["protocol"].is<JsonObject>()) {
        JsonObjectConst p = root["protocol"].as<JsonObjectConst>();
        // When receiverLinked=false the server demodulates at receiverFreq (not frequency).
        // Use the same field so firmware and server lock on the same frequency.
        bool rcvLinked = p["receiverLinked"] | true;
        if (!rcvLinked && p["receiverFreq"].is<float>()) {
            gSync.demod_freq_hz = p["receiverFreq"] | gSync.demod_freq_hz;
        } else {
            gSync.demod_freq_hz = p["frequency"] | gSync.demod_freq_hz;
        }

        const char* sm = p["streamMode"] | "compact";
        bool new_raw = (strcmp(sm, "raw") == 0);
        if (new_raw != gSync.is_raw) {
            Serial.printf("[Config] streamMode changed: %s (is_raw=%d)\n", sm, new_raw);
            gSync.is_raw = new_raw;
        }

        if (p["syncTrigger"].is<JsonObject>()) {
            JsonObjectConst st = p["syncTrigger"].as<JsonObjectConst>();
            uint32_t newSession = st["sessionId"] | 0u;
            if (newSession != gSync.session_id && newSession != 0) {
                uint64_t tgt = (uint64_t)(st["targetTsfUs"] | (double)0.0);
                uint64_t nowTsf = tsfNowUs();
                int64_t  delta  = (int64_t)tgt - (int64_t)nowTsf;
                Serial.printf("[SYNC-NEW] session=%u targetTsf=%llu nowTsf=%llu delta=%.1f ms\n",
                    newSession,
                    (unsigned long long)tgt,
                    (unsigned long long)nowTsf,
                    (double)delta / 1000.0);
                gSync.target_tsf_us = tgt;
                gSync.session_id     = newSession;
            }
        }
    }

    return true;
}

static void pollerTask(void*) {
    static uint8_t s_pollCnt = 0;
    WiFiClient tcp;
    HTTPClient http;
    
    for (;;) {
        if (WiFi.status() == WL_CONNECTED) {
            String url = String("http://") + gConfigHost + ":" +
                         String(gConfigPort) + gConfigPath;
            if (http.begin(tcp, url)) {
                http.setReuse(true);
                http.useHTTP10(false); // HTTP/1.1 is required for Keep-Alive
                http.setConnectTimeout(1000);
                http.setTimeout(1000);
                // X-Device-TSF allows the host server to track TSF offset and transport latency
                char tsfBuf[24];
                snprintf(tsfBuf, sizeof(tsfBuf), "%llu", (unsigned long long)tsfNowUs());
                http.addHeader("X-Device-TSF", tsfBuf);
                http.addHeader("Connection", "keep-alive");
                int code = http.GET();
                if (code == HTTP_CODE_OK) {
                    String body = http.getString();
                    if (xSemaphoreTake(gMu, pdMS_TO_TICKS(20)) == pdTRUE) {
                        float prevFreq = gSync.demod_freq_hz;
                        parseResponse(body);
                        if (prevFreq != gSync.demod_freq_hz) {
                            Serial.printf("[POLL] freq: %.1f->%.1f Hz\n", prevFreq, gSync.demod_freq_hz);
                        }
                        xSemaphoreGive(gMu);
                    }
                    // Log status every 10 polls
                    if (++s_pollCnt >= 10) {
                        s_pollCnt = 0;
                        SyncParams snap = ConfigPollerGetSyncParams();
                        Serial.printf("[POLL] OK freq=%.1fHz raw=%d session=%u tsf=%llu\n",
                            snap.demod_freq_hz, (int)snap.is_raw, snap.session_id,
                            (unsigned long long)tsfNowUs());
                    }
                    gBackoffMs = 0;
                } else {
                    gBackoffMs = gPollMs;
                    Serial.printf("[POLL] HTTP err=%d url=%s\n", code, url.c_str());
                }
                http.end(); // Closes stream while keeping TCP connection alive when reuse is true
            }
        } else {
            Serial.println("[POLL] WiFi disconnected");
            gBackoffMs = gPollMs;
        }
        uint32_t sleepMs = gPollMs + gBackoffMs;
        vTaskDelay(pdMS_TO_TICKS(sleepMs));
    }
}

}  // namespace

void ConfigPollerStart(const char* configHost,
                       uint16_t    configPort,
                       const char* configPath,
                       const char* fallbackDataHost,
                       uint16_t    fallbackDataPort,
                       bool        fallbackUseUDP,
                       uint32_t    pollMs) {
    if (gTask != nullptr) return;

    gMu = xSemaphoreCreateMutex();
    if (!gMu) return;

    gConfigHost = (configHost && configHost[0]) ? String(configHost) : String("127.0.0.1");
    gConfigPort = (configPort > 0) ? configPort : 8080;
    gConfigPath = (configPath && configPath[0]) ? String(configPath) : String("/api/protocol");
    if (!gConfigPath.startsWith("/")) gConfigPath = String("/") + gConfigPath;
    gPollMs     = (pollMs < 250) ? 250 : pollMs;

    gEndpoint.host   = (fallbackDataHost && fallbackDataHost[0]) ?
                            String(fallbackDataHost) : gConfigHost;
    gEndpoint.port   = (fallbackDataPort > 0) ? fallbackDataPort : 5555;
    gEndpoint.useUDP = fallbackUseUDP;
    gEndpoint.version = 1;
    gFallbackVer      = 1;

    xTaskCreate(pollerTask, "cfg_poller", 6144, nullptr, 1, &gTask);
}

StreamEndpoint ConfigPollerGetEndpoint() {
    StreamEndpoint snap = gEndpoint;
    if (!gMu) return snap;
    if (xSemaphoreTake(gMu, pdMS_TO_TICKS(20)) == pdTRUE) {
        snap = gEndpoint;
        xSemaphoreGive(gMu);
    }
    return snap;
}

SyncParams ConfigPollerGetSyncParams() {
    SyncParams snap = gSync;
    if (!gMu) return snap;
    if (xSemaphoreTake(gMu, pdMS_TO_TICKS(20)) == pdTRUE) {
        snap = gSync;
        xSemaphoreGive(gMu);
    }
    return snap;
}
