/**
 * OLED menu and K1-K4 button controller. Non-blocking, time-based debounce.
 *
 * Screen 0 (Status): emission frequency, active sensors with SNR, API status, IP (read-only).
 * Screen 1 (TX Editor): Mode / Ch / Freq / Wave / Phase / Out.
 */
#include "ui_menu.h"
#include "config.h"
#include <Arduino.h>
#include <WiFi.h>

#if defined(OLED_SSD1315_ENABLE) && OLED_SSD1315_ENABLE
#include <oled/OledSsd1315.hpp>
#endif

namespace tx {

#if defined(OLED_SSD1315_ENABLE) && OLED_SSD1315_ENABLE
static oled::OledSsd1315* s_display = nullptr;
static bool s_oledOk = false;

static constexpr unsigned DEBOUNCE_MS = 80;
static uint32_t s_firstLow[4]  = {0};
static bool     s_triggered[4] = {false};
static bool s_apiOk   = false;
static int  s_apiCode = 0;
static uint32_t s_apiMs  = 0;
static bool s_apiDirty   = false;

static bool buttonPressed(int pin, int idx) {
    bool low = (digitalRead(pin) == LOW);
    uint32_t now = millis();
    if (!low) { s_firstLow[idx] = 0; s_triggered[idx] = false; return false; }
    if (s_firstLow[idx] == 0) s_firstLow[idx] = now;
    if (!s_triggered[idx] && (now - s_firstLow[idx]) >= DEBOUNCE_MS) {
        s_triggered[idx] = true;
        return true;
    }
    return false;
}

static const char* modeStr(Mode m) {
    switch (m) {
        case Mode::Independent: return "IND";
        case Mode::Stereo:      return "STEREO";
        case Mode::Phase:       return "PHASE";
        default:                return "?";
    }
}

static const char* waveStr(Wave w) {
    switch (w) {
        case Wave::Sine:     return "SINE";
        case Wave::Triangle: return "TRI";
        case Wave::Square:   return "SQR";
        default:             return "?";
    }
}

// Human-readable HTTP status string for OLED display.
static const char* apiErrStr(int code) {
    switch (code) {
        case -1: return "NO CONN";
        case -2: return "JSON ERR";
        case -3: return "TIMEOUT";
        case -4: return "NO HOST";
        case 0:  return "---";
        case 400: return "BAD REQ";
        case 403: return "FORBID";
        case 404: return "NOT FOUND";
        case 503: return "SRV BUSY";
        default:  return "ERR";
    }
}

static ChannelState* focusedChannel(TxState& s, const UiState& ui) {
    return ui.channel == UiChannel::Left ? &s.left : &s.right;
}

static const ChannelState* focusedChannelConst(const TxState& s, const UiState& ui) {
    return ui.channel == UiChannel::Left ? &s.left : &s.right;
}

// row 1..6 on screen 1 -> UiField 0..5
static UiField fieldFromRow(uint8_t row) {
    if (row >= 1 && row <= (int)UiField::_Count) return (UiField)(row - 1);
    return UiField::Mode;
}

static void valueDown(TxState& state, UiState& ui) {
    UiField f = fieldFromRow(ui.cursorRow);
    ChannelState* ch = focusedChannel(state, ui);
    switch (f) {
        case UiField::Mode:
            state.mode = (state.mode == Mode::Independent) ? Mode::Phase
                       : (state.mode == Mode::Stereo)      ? Mode::Independent
                       :                                     Mode::Stereo;
            break;
        case UiField::Channel:
            ui.channel = UiChannel::Left;
            break;
        case UiField::Freq: {
            float step = (state.mode == Mode::Independent) ? (ch->freqHz >= 100 ? 10.f : 1.f) : 1.f;
            ch->freqHz -= step;
            if (state.mode != Mode::Independent) state.left.freqHz = state.right.freqHz = ch->freqHz;
            state.clamp();
            break;
        }
        case UiField::Wave:
            ch->wave = (ch->wave == Wave::Sine) ? Wave::Square
                     : (ch->wave == Wave::Square) ? Wave::Triangle : Wave::Sine;
            break;
        case UiField::Phase:
            ch->phaseDeg -= 15;
            state.clamp();
            break;
        case UiField::Enable:
            ch->enabled = false;
            break;
        default: break;
    }
}

static void valueUp(TxState& state, UiState& ui) {
    UiField f = fieldFromRow(ui.cursorRow);
    ChannelState* ch = focusedChannel(state, ui);
    switch (f) {
        case UiField::Mode:
            state.mode = (state.mode == Mode::Independent) ? Mode::Stereo
                       : (state.mode == Mode::Stereo)      ? Mode::Phase
                       :                                     Mode::Independent;
            break;
        case UiField::Channel:
            ui.channel = UiChannel::Right;
            break;
        case UiField::Freq: {
            float step = (state.mode == Mode::Independent) ? (ch->freqHz >= 100 ? 10.f : 1.f) : 1.f;
            ch->freqHz += step;
            if (state.mode != Mode::Independent) state.left.freqHz = state.right.freqHz = ch->freqHz;
            state.clamp();
            break;
        }
        case UiField::Wave:
            ch->wave = (ch->wave == Wave::Sine) ? Wave::Triangle
                     : (ch->wave == Wave::Triangle) ? Wave::Square : Wave::Sine;
            break;
        case UiField::Phase:
            ch->phaseDeg += 15;
            state.clamp();
            break;
        case UiField::Enable:
            ch->enabled = true;
            break;
        default: break;
    }
}
#endif // OLED_SSD1315_ENABLE

void uiInit() {
#if defined(OLED_SSD1315_ENABLE) && OLED_SSD1315_ENABLE
    pinMode(BTN_K1_GPIO, INPUT_PULLUP);
    pinMode(BTN_K2_GPIO, INPUT_PULLUP);
    pinMode(BTN_K3_GPIO, INPUT_PULLUP);
    pinMode(BTN_K4_GPIO, INPUT_PULLUP);

    Wire.begin(OLED_SDA_GPIO, OLED_SCL_GPIO);
    s_display = new oled::OledSsd1315(Wire);
    oled::OledConfig cfg;
    cfg.i2cAddr7  = 0x3C;
    cfg.width     = 128;
    cfg.height    = 64;
    cfg.vccMode   = oled::VccMode::InternalChargePump;
    s_oledOk = (s_display->begin(cfg) == oled::OledResult::Ok);
#endif
}

void uiPollButtons(TxState& state, UiState& ui) {
#if defined(OLED_SSD1315_ENABLE) && OLED_SSD1315_ENABLE
    // K3/K4 — scroll cursor on editor screen (screen 1), ignored on screen 0.
    if (buttonPressed(BTN_K3_GPIO, 2)) {
        ui.dirty = true;
        if (ui.screen == 1)
            ui.cursorRow = (ui.cursorRow + 1) % UI_CURSOR_ROW_MAX;
    }
    if (buttonPressed(BTN_K4_GPIO, 3)) {
        ui.dirty = true;
        if (ui.screen == 1)
            ui.cursorRow = (ui.cursorRow + UI_CURSOR_ROW_MAX - 1) % UI_CURSOR_ROW_MAX;
    }
    // K1 — increment value / enter editor.
    if (buttonPressed(BTN_K1_GPIO, 0)) {
        ui.dirty = true;
        if (ui.screen == 0) {
            ui.screen = 1; ui.cursorRow = 1;
        } else {
            if (ui.cursorRow == 0) { ui.screen = 0; ui.cursorRow = 0; }
            else                    valueUp(state, ui);
        }
    }
    // K2 — decrement value / return to status screen.
    if (buttonPressed(BTN_K2_GPIO, 1)) {
        ui.dirty = true;
        if (ui.screen == 0) {
            ui.screen = 1; ui.cursorRow = 1;
        } else {
            if (ui.cursorRow == 0) { ui.screen = 0; ui.cursorRow = 0; }
            else                    valueDown(state, ui);
        }
    }
#else
    (void)state; (void)ui;
#endif
}

static const int ROW_H = 8;

#if defined(OLED_SSD1315_ENABLE) && OLED_SSD1315_ENABLE
static void drawRow(int y, bool cursor, const char* text) {
    s_display->setCursor(0, y);
    if (cursor) s_display->print("> ");
    s_display->print(text);
}
#endif

void uiDrawMenu(const TxState& state, UiState& ui, const ProtocolRuntime& proto) {
#if defined(OLED_SSD1315_ENABLE) && OLED_SSD1315_ENABLE
    if (!s_display || !s_oledOk || !ui.dirty) return;

    s_display->clear();
    s_display->setTextColor(true);
    s_display->setTextSize(1);

    char buf[24];

    if (ui.screen == 0) {
        // ===================== SCREEN 0: STATUS =====================
        // Row 0: header — carrier frequency and session state
        const char* sess = proto.panicStop       ? "STOP"
                         : proto.sessionRunning  ? "RUN"
                         :                         "IDLE";
        snprintf(buf, sizeof(buf), "EMIT %.0fHz %s",
                 (double)state.left.freqHz, sess);
        drawRow(0, true, buf);

        // Rows 1-5: active sensors (up to 5)
        int shown = proto.sensorCount < 5 ? proto.sensorCount : 5;
        for (int i = 0; i < shown; i++) {
            const SensorInfo& s = proto.sensors[i];
            if (s.ageMs >= 2000) {
                snprintf(buf, sizeof(buf), "S%u  no sig", s.id);
            } else if (s.snr < 6.0f) {
                snprintf(buf, sizeof(buf), "S%u %.0fdB WEAK", s.id, (double)s.snr);
            } else {
                snprintf(buf, sizeof(buf), "S%u %.0fdB %.3fg",
                         s.id, (double)s.snr, (double)s.mag);
            }
            s_display->setCursor(0, (i + 1) * ROW_H);
            s_display->print(buf);
        }

        // Row 6: HTTP API status
        uint32_t age = (s_apiMs == 0) ? 0 : (millis() - s_apiMs) / 1000;
        if (s_apiOk)
            snprintf(buf, sizeof(buf), "API OK %lus", (unsigned long)age);
        else
            snprintf(buf, sizeof(buf), "API %s", apiErrStr(s_apiCode));
        s_display->setCursor(0, 6 * ROW_H);
        s_display->print(buf);

        // Row 7: WiFi IP or connection state
        if (WiFi.status() == WL_CONNECTED)
            snprintf(buf, sizeof(buf), "%s", WiFi.localIP().toString().c_str());
        else
            snprintf(buf, sizeof(buf), "WiFi connecting...");
        s_display->setCursor(0, 7 * ROW_H);
        s_display->print(buf);

    } else {
        // ===================== SCREEN 1: EDIT TX =====================
        // Row 0: back link + active carrier frequency
        snprintf(buf, sizeof(buf), "< EDIT %.0fHz %s",
                 (double)state.left.freqHz,
                 state.left.enabled ? "ON" : "off");
        drawRow(0, ui.cursorRow == 0, buf);

        snprintf(buf, sizeof(buf), "Mode %s", modeStr(state.mode));
        drawRow(1 * ROW_H, ui.cursorRow == 1, buf);

        const char* chLabel = (ui.channel == UiChannel::Left) ? "L" : "R";
        snprintf(buf, sizeof(buf), "Ch %s", chLabel);
        drawRow(2 * ROW_H, ui.cursorRow == 2, buf);

        const ChannelState* ch = focusedChannelConst(state, ui);
        snprintf(buf, sizeof(buf), "Freq %s %.0f Hz", chLabel, (double)ch->freqHz);
        drawRow(3 * ROW_H, ui.cursorRow == 3, buf);

        snprintf(buf, sizeof(buf), "Wave %s %s", chLabel, waveStr(ch->wave));
        drawRow(4 * ROW_H, ui.cursorRow == 4, buf);

        snprintf(buf, sizeof(buf), "Phase %s %d deg", chLabel, ch->phaseDeg);
        drawRow(5 * ROW_H, ui.cursorRow == 5, buf);

        snprintf(buf, sizeof(buf), "Out %s %s", chLabel, ch->enabled ? "ON" : "OFF");
        drawRow(6 * ROW_H, ui.cursorRow == 6, buf);
    }

    s_display->flush();
    ui.dirty = false;
#else
    (void)state; (void)ui; (void)proto;
#endif
}

#if defined(OLED_SSD1315_ENABLE) && OLED_SSD1315_ENABLE
oled::OledSsd1315* uiGetDisplay() { return s_display; }
#endif

bool uiSetApiStatus(bool ok, int code) {
#if defined(OLED_SSD1315_ENABLE) && OLED_SSD1315_ENABLE
    bool changed = (ok != s_apiOk) || (code != s_apiCode);
    s_apiOk   = ok;
    s_apiCode = code;
    s_apiMs   = millis();
    if (changed) s_apiDirty = true;
    return changed;
#else
    (void)ok; (void)code;
    return false;
#endif
}

bool uiApiNeedsRedraw() {
#if defined(OLED_SSD1315_ENABLE) && OLED_SSD1315_ENABLE
    if (s_apiDirty) { s_apiDirty = false; return true; }
#endif
    return false;
}

} // namespace tx
