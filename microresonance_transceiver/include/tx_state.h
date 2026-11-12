/**
 * Transmitter state: operating mode, L/R channels, and local UI state.
 * Synchronized across HTTP protocol configuration and the local OLED menu.
 */
#ifndef TX_STATE_H
#define TX_STATE_H

#include "ad9833.h"
#include "config.h"
#include <cstdint>

namespace tx {

enum class Wave : uint8_t { Sine, Triangle, Square };
enum class Mode : uint8_t { Independent, Stereo, Phase };

/** Single-channel parameters (left/right). */
struct ChannelState {
    float   freqHz   = 311.0f;
    Wave    wave     = Wave::Sine;
    int     phaseDeg = 0;
    bool    enabled  = true;

    void clamp() {
        if (freqHz < FREQ_MIN_HZ) freqHz = FREQ_MIN_HZ;
        if (freqHz > FREQ_MAX_HZ) freqHz = FREQ_MAX_HZ;
        if (phaseDeg < PHASE_MIN_DEG) phaseDeg = PHASE_MIN_DEG;
        if (phaseDeg > PHASE_MAX_DEG) phaseDeg = PHASE_MAX_DEG;
    }

    Ad9833Wave toAd9833Wave() const {
        switch (wave) {
            case Wave::Triangle: return Ad9833Wave::Triangle;
            case Wave::Square:   return Ad9833Wave::Square;
            default:            return Ad9833Wave::Sine;
        }
    }
};

/** Complete emitter state (single source of truth for DAC/DDS and OLED). */
struct TxState {
    Mode         mode   = Mode::Stereo;
    ChannelState left;
    ChannelState right;

    void clamp() {
        left.clamp();
        right.clamp();
        if (mode == Mode::Stereo || mode == Mode::Phase) {
            float f = left.freqHz;
            left.freqHz = f;
            right.freqHz = f;
        }
    }
};

/** Parameter currently focused in the local editor menu. */
enum class UiField : uint8_t {
    Mode,
    Channel,
    Freq,
    Wave,
    Phase,
    Enable,
    _Count
};

/** Focused channel (for Independent mode / L-R selection). */
enum class UiChannel : uint8_t { Left, Right };

/** Cursor row count on the editor screen: 0=header, 1=Mode .. 6=Enable. */
static constexpr uint8_t UI_CURSOR_ROW_MAX = 7;

/** Screen index count: 0=status overview, 1=TX parameter editor. */
static constexpr uint8_t UI_SCREEN_MAX = 2;

/** UI runtime state: active cursor row, screen index, channel, redraw flag. */
struct UiState {
    uint8_t   cursorRow   = 1;   // 0=header, 1..6=Mode,Ch,Freq,Wave,Phase,Enable
    uint8_t   screen      = 0;   // 0=status, 1=edit
    UiChannel channel     = UiChannel::Left;
    bool      dirty       = true;
};

} // namespace tx

#endif
