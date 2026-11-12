/**
 * ESP32-S3: ADXL355 → Edge DSP → TCP (DspPacket v2).
 */
#include <Arduino.h>
#include <SPI.h>
#include <Wire.h>
#include <WiFi.h>
#include <esp_wifi.h>
#include <PL_ADXL355.h>
#include <freertos/semphr.h>

#include "config_poller.h"
#include "dsp_task.h"

#define ADXL_CS   8
#define ADXL_MOSI 9
#define ADXL_MISO 10
#define ADXL_SCLK 11
#define ADXL_INT1 12
#define ADXL_DRDY 13
#define LED_PIN   2

PL::ADXL355    adxl355;
extern const uint8_t ADXL_DRDY_PIN = ADXL_DRDY;

static const char* WIFI_SSID  = "YOUR_WIFI_SSID";
static const char* WIFI_PASS  = "YOUR_WIFI_PASSWORD";
static const char* SERVER_IP  = "192.168.200.10";
static const uint16_t SERVER_PORT    = 5555;
static const uint16_t HTTP_PORT      = 8080;
static const uint32_t HTTP_POLL_MS   = 5000;
static const char*    HTTP_CFG_PATH  = "/api/protocol";
static const uint32_t RECONNECT_MS   = 1000;

static float    g_demodFreq     = 311.0f;
static uint64_t g_targetTsfUs   = 0;
static uint32_t g_syncSession   = 0;
static bool     g_isRaw         = true;

static uint32_t lastReconnectMs = 0;
static uint32_t lastSyncPollMs  = 0;

static void wifiApplyPsNone() {
    esp_wifi_set_ps(WIFI_PS_NONE);
}

static void wifiConnect() {
    WiFi.persistent(false);
    WiFi.mode(WIFI_STA);
    wifiApplyPsNone();
    WiFi.setAutoReconnect(true);
    WiFi.setSleep(false);
    WiFi.begin(WIFI_SSID, WIFI_PASS);
    for (int i = 0; i < 30 && WiFi.status() != WL_CONNECTED; i++) {
        delay(500);
        Serial.print('.');
    }
}

static void lsmInit() {
    Wire.begin(3, 2, 400000); 
    delay(100);
    Wire.beginTransmission(0x6B);
    if (Wire.endTransmission() != 0) {
        Serial.println("[LSM6DSOX] ERROR: Not found!");
        return;
    }
    
    Wire.beginTransmission(0x6B);
    Wire.write(0x12); Wire.write(0x05); 
    Wire.endTransmission();
    delay(50);

    Wire.beginTransmission(0x6B);
    Wire.write(0x10); Wire.write(0x80); 
    Wire.endTransmission();
    Wire.beginTransmission(0x6B);
    Wire.write(0x11); Wire.write(0x80); 
    Wire.endTransmission();
    Wire.beginTransmission(0x6B);
    Wire.write(0x12); Wire.write(0x44); 
    Wire.endTransmission();
    Wire.beginTransmission(0x6B);
    Wire.write(0x0D); Wire.write(0x01); 
    Wire.endTransmission();
    Wire.beginTransmission(0x6B);
    Wire.write(0x19); Wire.write(0x20); 
    Wire.endTransmission();

    pinMode(6, INPUT); 
    pinMode(1, INPUT); 
    Serial.println("[LSM6DSOX] OK");
}

static void adxlInit() {
    SPI.begin(ADXL_SCLK, ADXL_MISO, ADXL_MOSI, -1);
    pinMode(ADXL_DRDY, INPUT);
    adxl355.beginSPI(ADXL_CS);
    adxl355.setRange(PL::ADXL355_Range::range2g);
    adxl355.setOutputDataRate(PL::ADXL355_OutputDataRate::odr4000);
    adxl355.enableMeasurement();
    Serial.println("[ADXL355] OK");
}

void setup() {
    pinMode(LED_PIN, OUTPUT);
    digitalWrite(LED_PIN, HIGH);
    Serial.begin(115200);
    delay(1500);
    wifiConnect();
    ConfigPollerStart(SERVER_IP, HTTP_PORT, HTTP_CFG_PATH, SERVER_IP, SERVER_PORT, false, HTTP_POLL_MS);
    adxlInit();
    lsmInit();
    DspTaskConfig cfg;
    cfg.sensor_id      = 4;
    cfg.demod_freq_hz  = &g_demodFreq;
    cfg.target_tsf_us  = &g_targetTsfUs;
    cfg.is_raw         = &g_isRaw;
    cfg.tcp_client     = nullptr;
    cfg.tcp_mutex      = nullptr;
    dspTaskStart(cfg);
    digitalWrite(LED_PIN, LOW);
}

void loop() {
    uint32_t now = millis();
    if (WiFi.status() != WL_CONNECTED) {
        if (now - lastReconnectMs >= RECONNECT_MS) {
            lastReconnectMs = now;
            WiFi.reconnect();
        }
    }
    if (now - lastSyncPollMs >= 500) {
        lastSyncPollMs = now;
        SyncParams sp = ConfigPollerGetSyncParams();
        g_demodFreq = sp.demod_freq_hz;
        if (sp.session_id != g_syncSession && sp.session_id != 0) {
            g_syncSession = sp.session_id;
            g_targetTsfUs = sp.target_tsf_us;
        }
    }
    delay(50);
}
