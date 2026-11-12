/**
 * OLED menu and K1-K4 button polling. Non-blocking, redraws on dirty flag.
 *
 * Screen 0 — Status (read-only): emission frequency, active sensors with SNR, API status, IP.
 * Screen 1 — TX Editor: Mode / Ch / Freq / Wave / Phase / Out.
 *
 * K1/K2 on Screen 0 -> switch to Screen 1.
 * K1/K2 on Row 0 of Screen 1 -> return to Screen 0.
 * K1/K2 on Rows 1-6 of Screen 1 -> increment/decrement focused parameter.
 * K3/K4 on Screen 1 -> move cursor row.
 */
#ifndef UI_MENU_H
#define UI_MENU_H

#include "tx_state.h"
#include "http_config.h"
#include <Arduino.h>

#if defined(OLED_SSD1315_ENABLE) && OLED_SSD1315_ENABLE
#include <Wire.h>
#include <oled/OledSsd1315.hpp>
#endif

namespace tx {

struct TxState;
struct UiState;
struct ProtocolRuntime;

/** Initialize SSD1315 OLED (I2C) and buttons K1-K4. */
void uiInit();

/** Poll K1-K4 buttons with non-blocking time-based debounce. */
void uiPollButtons(TxState& state, UiState& ui);

/** Redraw OLED display when ui.dirty is set. */
void uiDrawMenu(const TxState& state, UiState& ui, const ProtocolRuntime& proto);

/** Update HTTP API status indicator (ok/code); returns true if state changed. */
bool uiSetApiStatus(bool ok, int code);

/** Return true if an API status change requires an OLED redraw. */
bool uiApiNeedsRedraw();

#if defined(OLED_SSD1315_ENABLE) && OLED_SSD1315_ENABLE
oled::OledSsd1315* uiGetDisplay();
#endif

} // namespace tx

#endif
