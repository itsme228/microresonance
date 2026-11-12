/**
 * lsmTask    (Core 0, prio 3): LSM6DSOX 6-axis IMU (I2C) + analog sEMG capture.
 * sensorTask (Core 1, prio 5): ADXL355 4000 Hz SPI capture -> ring buffer.
 * dspTask    (Core 1, prio 4): ring buffer -> RawSyncPacket (0x4D58) -> WMM AC_VO UDP.
 *
 * Phase Coherence: Every window is stamped with hardware 802.11 TSF microseconds
 * back-dated by the measured DRDY ISR-to-SPI lag, matching the emitter's NCO timebase.
 */
#include "dsp_task.h"
#include "dsp_packet.h"
#include "tsf_time.h"

#include <Arduino.h>
#include <SPI.h>
#include <WiFi.h>
#include <lwip/sockets.h>
#include <lwip/netdb.h>
#include <PL_ADXL355.h>
#include <freertos/FreeRTOS.h>
#include <freertos/semphr.h>
#include <freertos/queue.h>
#include <math.h>
#include <stdarg.h>
#include <esp_task_wdt.h>
#include <string.h>

// --- ADXL355: extern from main.cpp ---
extern PL::ADXL355 adxl355;
extern const uint8_t ADXL_DRDY_PIN;

// --- UDP ---
static int s_udp_sock = -1;
static uint32_t s_udpSendErrors = 0;

static bool ensureUdpReady() {
    if (WiFi.status() != WL_CONNECTED) {
        if (s_udp_sock >= 0) {
            close(s_udp_sock);
            s_udp_sock = -1;
        }
        return false;
    }
    if (s_udp_sock < 0) {
        s_udp_sock = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
        if (s_udp_sock >= 0) {
            // Enable WMM AC_VO (Voice) priority -> disables 802.11 A-MPDU frame aggregation
            // so packets are transmitted immediately without buffering up to 16 frames.
            int tos = 0xE0;
            setsockopt(s_udp_sock, IPPROTO_IP, IP_TOS, &tos, sizeof(tos));
            
            // Non-blocking socket mode to protect real-time FreeRTOS tasks
            int flags = fcntl(s_udp_sock, F_GETFL, 0);
            fcntl(s_udp_sock, F_SETFL, flags | O_NONBLOCK);
        }
    }
    return (s_udp_sock >= 0);
}

static bool sendUdpPacket(const char* host, uint16_t port, const uint8_t* data, size_t len) {
    if (!ensureUdpReady()) {
        return false;
    }
    
    struct sockaddr_in dest;
    memset(&dest, 0, sizeof(dest));
    dest.sin_family = AF_INET;
    dest.sin_port = htons(port);
    dest.sin_addr.s_addr = inet_addr(host);

    int sent = sendto(s_udp_sock, data, len, 0, (struct sockaddr*)&dest, sizeof(dest));
    if (sent != (int)len) {
        s_udpSendErrors++;
        return false;
    }
    return true;
}

static int16_t quantizeRawG(float g) {
    float q = g * DSP_RAW_Q_G;
    if (q > 32767.0f) return 32767;
    if (q < -32768.0f) return -32768;
    return (int16_t)lrintf(q);
}

// --- DSP Constants ---
#define FFT_SIZE       128   // == DSP_WINDOW_SAMPLES
#define DSP_MAGIC_LOG  0x4D4Cu // MagicLog: [0x4C,0x4D,len,text]

// --------------------------------------------------------------- fw_log ------
// Writes to Serial and buffers diagnostic strings for UDP MagicLog forwarding.
// Single-writer from dspTask (no mutex required).
static char     s_fwLogBuf[2048];
static int      s_fwLogLen = 0;

static void fw_log(const char* fmt, ...) {
    char tmp[320];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(tmp, sizeof(tmp), fmt, ap);
    va_end(ap);
    Serial.print(tmp);
    int n = strlen(tmp);
    int rem = (int)sizeof(s_fwLogBuf) - s_fwLogLen - 1;
    if (rem > 0 && n > 0) {
        int copy = (n < rem) ? n : rem;
        memcpy(s_fwLogBuf + s_fwLogLen, tmp, copy);
        s_fwLogLen += copy;
        s_fwLogBuf[s_fwLogLen] = '\0';
    }
}

// Flushes buffered diagnostic logs as UDP MagicLog packets.
static void flushFwLogs(WiFiClient* cl, SemaphoreHandle_t mu) {
    (void)cl;
    (void)mu;
    if (s_fwLogLen <= 0) {
        return;
    }
    uint8_t pkt[3 + 240];
    int off = 0;
    while (off < s_fwLogLen) {
        int chunk = s_fwLogLen - off;
        if (chunk > 240) chunk = 240;
        pkt[0] = 0x4C;
        pkt[1] = 0x4D;
        pkt[2] = (uint8_t)chunk;
        memcpy(pkt + 3, s_fwLogBuf + off, (size_t)chunk);
        sendUdpPacket("192.168.200.10", 5555, pkt, (size_t)(3 + chunk));
        off += chunk;
    }
    s_fwLogLen = 0;
    s_fwLogBuf[0] = '\0';
}

// --- Multi-buffer ring between sensorTask, lsmTask, and dspTask ---

static constexpr int kSampleBufCount = 8;
static SensorSample s_bufs[kSampleBufCount][DSP_WINDOW_SAMPLES];

static int16_t  s_lsmAccelBufX[kSampleBufCount][DSP_LSM_SLOTS];
static int16_t  s_lsmAccelBufY[kSampleBufCount][DSP_LSM_SLOTS];
static int16_t  s_lsmAccelBufZ[kSampleBufCount][DSP_LSM_SLOTS];
static int16_t  s_lsmGyroBufX[kSampleBufCount][DSP_LSM_SLOTS];
static int16_t  s_lsmGyroBufY[kSampleBufCount][DSP_LSM_SLOTS];
static int16_t  s_lsmGyroBufZ[kSampleBufCount][DSP_LSM_SLOTS];
static uint32_t s_lsmTsBuf[kSampleBufCount][DSP_LSM_SLOTS];
static volatile uint16_t s_lsmCnt[kSampleBufCount];
static volatile int g_active_ab = 0;

// EMG buffer — populated by lsmTask on Core 0 to avoid SAR ADC spikes during SPI capture on Core 1
static uint16_t s_emgBufLsm[kSampleBufCount][DSP_RAW_SAMPLES];
static volatile uint16_t s_emgCnt[kSampleBufCount];

#include <Wire.h>

static QueueHandle_t s_ready_queue;
static QueueHandle_t s_drdy_queue;
static volatile uint32_t s_lastDrdyMonoUs = 0;
static volatile uint32_t s_drdyDrops = 0;

// --- Runtime configuration (populated by dspTaskStart) ---
static DspTaskConfig s_cfg;

// --- Diagnostic counters ---
static volatile uint32_t s_dspRuntimeUs  = 0;  // duration of last computeAndSend (us)
static volatile uint32_t s_bufOverruns   = 0;  // dropped windows
static volatile uint32_t s_pktsTotal     = 0;  // total processed windows
static uint32_t s_lastProfileOverruns    = 0;
static volatile uint32_t s_rawSentTotal  = 0;
static uint64_t          s_lastRawSendTsfUs = 0;
static uint32_t          s_lastRawSendMonoUs = 0;
static volatile uint32_t s_staleDropTotal = 0;

// ------------------------------------------------------------------ helpers --

void IRAM_ATTR drdy_isr() {
    uint32_t monoUs = (uint32_t)esp_timer_get_time();
    s_lastDrdyMonoUs = monoUs;
    BaseType_t xHigherPriorityTaskWoken = pdFALSE;
    if (xQueueSendFromISR(s_drdy_queue, &monoUs, &xHigherPriorityTaskWoken) != pdTRUE) {
        s_drdyDrops++;
    }
    if (xHigherPriorityTaskWoken) {
        portYIELD_FROM_ISR();
    }
}

// ------------------------------------------------------------------- FFT r2 --
// Radix-2 Cooley-Tukey in-place FFT (n is a power of 2).
static void fft_r2(float* re, float* im, int n) {
    for (int i = 1, j = 0; i < n; i++) {
        int bit = n >> 1;
        for (; j & bit; bit >>= 1) j ^= bit;
        j ^= bit;
        if (i < j) {
            float t; t=re[i]; re[i]=re[j]; re[j]=t;
                     t=im[i]; im[i]=im[j]; im[j]=t;
        }
    }
    for (int len = 2; len <= n; len <<= 1) {
        float ang = -(float)M_PI * 2.0f / len;
        float wRe = cosf(ang), wIm = sinf(ang);
        for (int i = 0; i < n; i += len) {
            float uRe = 1.0f, uIm = 0.0f;
            for (int j = 0; j < len/2; j++) {
                int a = i+j, b = i+j+len/2;
                float bRe = re[b]*uRe - im[b]*uIm;
                float bIm = re[b]*uIm + im[b]*uRe;
                re[b] = re[a]-bRe; im[b] = im[a]-bIm;
                re[a] = re[a]+bRe; im[a] = im[a]+bIm;
                float t = uRe*wRe - uIm*wIm;
                uIm  = uRe*wIm + uIm*wRe;
                uRe  = t;
            }
        }
    }
}

// --------------------------------------------------------- spectrum compute --
// Logarithmically binned spectrum from FFT magnitude -> 32 bands (0-255).
static void fillSpectrum(const float* mag, int half, float fs, DspPacket* out) {
    float bands[DSP_SPEC_BANDS] = {};
    float nyq = fs * 0.5f;
    float logNyq = logf(nyq / 1.0f + 1.0f);

    for (int i = 1; i < half; i++) {
        float freq = (float)i * fs / (float)(half * 2);
        int band = (int)(logf(freq + 1.0f) / logNyq * DSP_SPEC_BANDS);
        if (band < 0) band = 0;
        if (band >= DSP_SPEC_BANDS) band = DSP_SPEC_BANDS-1;
        if (mag[i] > bands[band]) bands[band] = mag[i];
    }
    float mx = 1e-12f;
    for (int i = 0; i < DSP_SPEC_BANDS; i++) if (bands[i] > mx) mx = bands[i];
    for (int i = 0; i < DSP_SPEC_BANDS; i++)
        out->spectrum[i] = (uint8_t)(bands[i] / mx * 255.0f + 0.5f);
}

// ------------------------------------------------------- DSP core (dspTask) --
static float s_fftRe[FFT_SIZE];
static float s_fftIm[FFT_SIZE];

static void computeAndSend(const SensorSample* w, int n, int ab) {
    if (n < 64) return;

    float freq = 311.0f;
    if (s_cfg.demod_freq_hz) freq = *s_cfg.demod_freq_hz;
    uint64_t tsf0  = w[0].tsf_us;

    const char* server_ip = "192.168.200.10";
    uint16_t server_port = 5555;

    static RawSyncPacket rpkt;
    memset(&rpkt, 0, sizeof(rpkt));
    rpkt.magic          = DSP_MAGIC_RAW;
    rpkt.sensor_id      = s_cfg.sensor_id;
    rpkt.tsf_us         = tsf0;
    
    rpkt.window_samples = (uint32_t)n;
    rpkt.lsm_samples = s_lsmCnt[ab];
    uint16_t emgCnt = s_emgCnt[ab];
    if (emgCnt > DSP_RAW_SAMPLES) emgCnt = DSP_RAW_SAMPLES;
    rpkt.emg_samples = emgCnt;

    for (int i = 0; i < n && i < DSP_RAW_SAMPLES; i++) {
        rpkt.raw_x[i] = quantizeRawG(w[i].x);
        rpkt.raw_y[i] = quantizeRawG(w[i].y);
        rpkt.raw_z[i] = quantizeRawG(w[i].z);
        rpkt.emg[i] = (i < emgCnt) ? s_emgBufLsm[ab][i] : 0;
    }
    for (int i = 0; i < rpkt.lsm_samples && i < DSP_LSM_SLOTS; i++) {
        rpkt.lsm_accel_x[i] = s_lsmAccelBufX[ab][i];
        rpkt.lsm_accel_y[i] = s_lsmAccelBufY[ab][i];
        rpkt.lsm_accel_z[i] = s_lsmAccelBufZ[ab][i];
        rpkt.lsm_gyro_x[i] = s_lsmGyroBufX[ab][i];
        rpkt.lsm_gyro_y[i] = s_lsmGyroBufY[ab][i];
        rpkt.lsm_gyro_z[i] = s_lsmGyroBufZ[ab][i];
        rpkt.lsm_ts[i] = s_lsmTsBuf[ab][i];
    }

    if (!sendUdpPacket(server_ip, server_port, (const uint8_t*)&rpkt, RAW_SYNC_PACKET_LEN)) {
        static uint32_t s_lastUdpErrMs = 0;
        uint32_t nowMs = (uint32_t)(esp_timer_get_time() / 1000);
        if (nowMs - s_lastUdpErrMs >= 1000) {
            s_lastUdpErrMs = nowMs;
            Serial.printf("[UDP-RAW] send failed err_count=%u wifi=%d heap=%u\n",
                s_udpSendErrors, (int)WiFi.status(), ESP.getFreeHeap());
        }
    } else {
        static uint32_t s_lastRawLogMs = 0;
        uint32_t nowMs = (uint32_t)(esp_timer_get_time() / 1000);
        if (nowMs - s_lastRawLogMs >= 5000) {
            s_lastRawLogMs = nowMs;
            Serial.printf("[UDP-RAW] sent magic=0x%04X sensor=%d tsf=%llu samples=%d\n",
                rpkt.magic, rpkt.sensor_id, (unsigned long long)rpkt.tsf_us, (int)rpkt.window_samples);
        }
        s_rawSentTotal++;
        s_lastRawSendTsfUs = tsf0;
        s_lastRawSendMonoUs = (uint32_t)(esp_timer_get_time() / 1000);
    }
}

// ------------------------------------------------------------- sensorTask ---
static volatile uint32_t s_spiRuntimeUs = 0;
static volatile uint32_t s_tsfStampLagUs = 0;
static volatile uint32_t s_tsfStampJitterUs = 0;
static volatile uint32_t s_isrToSpiLagUs = 0;

static void lsmTask(void* arg) {
    Serial.println("[lsmTask] started on Core 0");
    for (;;) {
        // Snapshot active ring buffer index once per iteration
        int ab = g_active_ab;

        // EMG: analog input on GPIO1 is sampled here on Core 0, NOT in sensorTask on Core 1
        // (SAR ADC conversions draw current spikes on the 3.3V rail that couple into ADXL355)
        uint16_t ec = s_emgCnt[ab];
        if (ec < DSP_RAW_SAMPLES) {
            s_emgBufLsm[ab][ec] = (uint16_t)analogRead(1);
            s_emgCnt[ab] = ec + 1;
        }

        // LSM6DSOX: poll when hardware DRDY (GPIO6) is HIGH
        if (digitalRead(6) == HIGH) {
            uint16_t lc = s_lsmCnt[ab];
            if (lc < DSP_LSM_SLOTS) {
                Wire.beginTransmission(0x6B);
                Wire.write(0x22); // OUTX_L_G
                if (Wire.endTransmission(false) == 0) {
                    Wire.requestFrom((uint16_t)0x6B, (uint8_t)12, true);
                    if (Wire.available() == 12) {
                        s_lsmGyroBufX[ab][lc] = Wire.read() | (Wire.read() << 8);
                        s_lsmGyroBufY[ab][lc] = Wire.read() | (Wire.read() << 8);
                        s_lsmGyroBufZ[ab][lc] = Wire.read() | (Wire.read() << 8);
                        s_lsmAccelBufX[ab][lc] = Wire.read() | (Wire.read() << 8);
                        s_lsmAccelBufY[ab][lc] = Wire.read() | (Wire.read() << 8);
                        s_lsmAccelBufZ[ab][lc] = Wire.read() | (Wire.read() << 8);

                        Wire.beginTransmission(0x6B);
                        Wire.write(0x40); // TIMESTAMP0
                        if (Wire.endTransmission(false) == 0) {
                            Wire.requestFrom((uint16_t)0x6B, (uint8_t)3, true);
                            if (Wire.available() == 3) {
                                s_lsmTsBuf[ab][lc] = Wire.read() | (Wire.read() << 8) | (Wire.read() << 16);
                            }
                        }
                        s_lsmCnt[ab] = lc + 1;
                    }
                }
            }
        }
        delay(1);
    }
}

static void sensorTask(void* /*arg*/) {
    int ab  = 0;  // active ring buffer slot
    int cnt = 0;
    bool wasWaiting = false;
    uint64_t waitStartTsf = 0;
    uint64_t s_lastWaitLogUs = 0;
    uint32_t spiTotalUs = 0;
    int spiCount = 0;
    Serial.println("[sensorTask] started on Core 1");

    for (;;) {
        uint32_t monoDrdyUs = 0;
        if (xQueueReceive(s_drdy_queue, &monoDrdyUs, pdMS_TO_TICKS(10)) != pdTRUE) {
            continue;
        }

        uint64_t tsfBefore = tsfNowUs();
        uint32_t tSpi0 = (uint32_t)esp_timer_get_time();
        auto acc     = adxl355.getAccelerations();
        uint32_t tSpi1 = (uint32_t)esp_timer_get_time();
        uint64_t tsfAfter = tsfNowUs();
        spiTotalUs += (tSpi1 - tSpi0);
        spiCount++;

        uint32_t stampLagUs = (uint32_t)(tSpi1 - tSpi0);
        s_tsfStampLagUs = stampLagUs;
        uint64_t tsfMid = tsfBefore + ((tsfAfter - tsfBefore) / 2ULL);
        uint32_t tsfSpanUs = (uint32_t)(tsfAfter - tsfBefore);
        s_tsfStampJitterUs = tsfSpanUs;
        uint32_t isrToSpiLagUs = (monoDrdyUs > 0 && tSpi0 >= monoDrdyUs) ? (tSpi0 - monoDrdyUs) : 0;
        s_isrToSpiLagUs = isrToSpiLagUs;
        uint64_t tsf = tsfMid;
        uint32_t monoNowUs = tSpi1;
        if (monoDrdyUs > 0 && monoNowUs >= monoDrdyUs) {
            uint32_t drdyAgeUs = monoNowUs - monoDrdyUs;
            if (drdyAgeUs <= 5000) {
                uint64_t tsfNow = tsfAfter;
                if (tsfNow >= drdyAgeUs) {
                    tsf = tsfNow - (uint64_t)drdyAgeUs;
                }
            }
        }

        // TSF trigger wait: hold until target TSF timestamp (if SyncTrigger armed)
        uint64_t tgt = *s_cfg.target_tsf_us;
        if (tgt != 0) {
            if (tsf < tgt) {
                if (!wasWaiting) {
                    wasWaiting    = true;
                    waitStartTsf  = tsf;
                    s_lastWaitLogUs = tsf;
                    Serial.printf("[SYNC-WAIT] begin: tsf=%llu tgt=%llu delta=%.1f ms\n",
                        (unsigned long long)tsf, (unsigned long long)tgt,
                        (double)((int64_t)tgt - (int64_t)tsf) / 1000.0);
                } else if (tsf - s_lastWaitLogUs >= 500000ULL) {
                    s_lastWaitLogUs = tsf;
                    Serial.printf("[SYNC-WAIT] tsf=%llu rem=%.1f ms\n",
                        (unsigned long long)tsf,
                        (double)((int64_t)tgt - (int64_t)tsf) / 1000.0);
                }
                cnt = 0;
                continue;
            }
            // Target TSF reached
            wasWaiting = false;
            *s_cfg.target_tsf_us = 0;
        } else if (wasWaiting) {
            wasWaiting = false;
        }

        s_bufs[ab][cnt].x = acc.x;
        s_bufs[ab][cnt].y = acc.y;
        s_bufs[ab][cnt].z = acc.z;
        s_bufs[ab][cnt].tsf_us = tsf;
        cnt++;

        if (cnt >= DSP_WINDOW_SAMPLES) {
            int readyBuf = ab;
            ab  = (ab + 1) % kSampleBufCount;
            cnt = 0;

            // Reset counters for the next buffer slot BEFORE publishing g_active_ab
            // so lsmTask on Core 0 never observes stale counts on the new slot.
            s_lsmCnt[ab] = 0;
            s_emgCnt[ab] = 0;
            g_active_ab = ab;

            s_spiRuntimeUs = spiCount > 0 ? (spiTotalUs / spiCount) : 0;
            spiTotalUs = 0;
            spiCount = 0;
            if (xQueueSend(s_ready_queue, &readyBuf, 0) != pdTRUE) {
                int droppedBuf = 0;
                if (xQueueReceive(s_ready_queue, &droppedBuf, 0) == pdTRUE) {
                    s_staleDropTotal++;
                    if (xQueueSend(s_ready_queue, &readyBuf, 0) == pdTRUE) {
                        continue;
                    }
                }
                s_bufOverruns++;
            }
        }
    }
}

// -------------------------------------------------------------- dspTask ----
static void dspTask(void* /*arg*/) {
    Serial.println("[dspTask] started on Core 0");
    static uint32_t s_lastDiagMs    = 0;
    static uint32_t s_lastLogSendMs = 0;
    
    uint32_t totalDspUs = 0;
    uint32_t dspCount = 0;

    for (;;) {
        int readyBuf = 0;
        if (xQueueReceive(s_ready_queue, &readyBuf, portMAX_DELAY) == pdTRUE) {
            int latestBuf = readyBuf;
            while (xQueueReceive(s_ready_queue, &latestBuf, 0) == pdTRUE) {
                readyBuf = latestBuf;
                s_staleDropTotal++;
            }

            uint32_t t0 = (uint32_t)esp_timer_get_time();
            computeAndSend(s_bufs[readyBuf], DSP_WINDOW_SAMPLES, readyBuf);
            uint32_t t1 = (uint32_t)esp_timer_get_time();
            
            s_dspRuntimeUs = (t1 - t0);
            totalDspUs += s_dspRuntimeUs;
            dspCount++;
            s_pktsTotal++;

            uint32_t nowMs = (uint32_t)(esp_timer_get_time() / 1000);

            // Periodic diagnostics every 10 seconds
            if (nowMs - s_lastDiagMs >= 10000) {
                s_lastDiagMs = nowMs;
                uint64_t tsf_now = tsfNowUs();
                fw_log("[DIAG] pkts=%u raw_sent=%u stale_drop=%u over=%u drdy_drop=%u dsp_us=%u spi_us=%u drdy_to_spi_us=%u tsf_lag_us=%u tsf_span_us=%u mode=RAW tsf=%.3fs\n",
                    s_pktsTotal, s_rawSentTotal, s_staleDropTotal, s_bufOverruns, s_drdyDrops, s_dspRuntimeUs,
                    s_spiRuntimeUs, s_isrToSpiLagUs, s_tsfStampLagUs, s_tsfStampJitterUs, (double)tsf_now * 1e-6);
            }

            if (nowMs - s_lastLogSendMs >= 1000) {
                s_lastLogSendMs = nowMs;
                flushFwLogs(nullptr, nullptr);
            }
        }
    }
}

// ----------------------------------------------------------- public API -----
void dspTaskStart(const DspTaskConfig& cfg) {
    s_cfg     = cfg;
    s_ready_queue = xQueueCreate(kSampleBufCount, sizeof(int));
    s_drdy_queue = xQueueCreate(256, sizeof(uint32_t));

    pinMode(ADXL_DRDY_PIN, INPUT);
    attachInterrupt(digitalPinToInterrupt(ADXL_DRDY_PIN), drdy_isr, RISING);

    // Pin ADXL355 capture and UDP dispatch to App Core (1) away from Wi-Fi / ADC activity on Core 0.
    xTaskCreatePinnedToCore(lsmTask, "lsmTask", 4096, NULL, 3, NULL, 0);
    xTaskCreatePinnedToCore(sensorTask, "sensorTask", 4096, nullptr, 5, nullptr, 1);
    xTaskCreatePinnedToCore(dspTask,    "dspTask",    12288, nullptr, 4, nullptr, 1);
}
