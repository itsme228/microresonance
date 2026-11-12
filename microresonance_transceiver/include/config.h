/**
 * Pinout and parameter limits for ESP32-S3-CAM (N16R8).
 * Active vibration exciter probe: dual AD9833 / PCM5102A I2S DAC, SSD1315 OLED, buttons K1-K4.
 */
#ifndef CONFIG_H
#define CONFIG_H

#include <cstdint>

// --- OLED SSD1315, I2C ---
#define OLED_SDA_GPIO  20
#define OLED_SCL_GPIO  19

// --- Navigation Buttons ---
#define BTN_K1_GPIO    21
#define BTN_K2_GPIO    47
#define BTN_K3_GPIO    48
#define BTN_K4_GPIO    45

// --- AD9833 LEFT (left pelvic exciter) ---
#define AD9833_LEFT_SDATA_GPIO  35
#define AD9833_LEFT_SCLK_GPIO   36
#define AD9833_LEFT_FSYNC_GPIO  37

// --- AD9833 RIGHT (right pelvic exciter) ---
#define AD9833_RIGHT_SDATA_GPIO  10
#define AD9833_RIGHT_SCLK_GPIO   8
#define AD9833_RIGHT_FSYNC_GPIO  7

// --- PCM5102A I2S ---
// 4-wire mode for coherent SDR-style emission:
// BCK=16, LCK=18, DIN=17, SCK/MCLK=15.
#define I2S_BCK_GPIO  16   // BCK — bit clock
#define I2S_LCK_GPIO  18   // LRCK/WS — word select
#define I2S_DIN_GPIO  17   // DIN — data to DAC
#define I2S_SCK_GPIO  15   // SCK/MCLK from ESP32-S3 to PCM5102A
#define I2S_USE_MCLK  1    // enable external MCLK on GPIO15
#define AUDIO_TASK_CORE 1
// 1 = standalone 440 Hz tone test without HTTP/UI (I2S/DAC verification)
#define I2S_MINIMAL_TEST  0
// 1 = swap L/R channels
#define I2S_SWAP_LR       0

// --- AD9833 ---
constexpr float AD9833_MCLK_HZ = 25'000'000.0f;

// --- Parameter Limits ---
constexpr float FREQ_MIN_HZ = 1.0f;
constexpr float FREQ_MAX_HZ = 2000.0f;
constexpr int   PHASE_MIN_DEG = 0;
constexpr int   PHASE_MAX_DEG = 360;

// --- WiFi / HTTP (configure for your local network) ---
#define WIFI_SSID        "YOUR_WIFI_SSID"
#define WIFI_PASS        "YOUR_WIFI_PASSWORD"
#define HTTP_SERVER_HOST "192.168.200.10"
#define HTTP_SERVER_PORT 8080
#define HTTP_CONFIG_PATH "/api/protocol"

// HTTP config polling interval, ms
#define HTTP_POLL_MS     2000

#endif
