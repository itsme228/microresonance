/**
 * AD9833 DDS driver implementation. MCLK = 25 MHz (defined in config.h).
 */
#include "ad9833.h"
#include "config.h"
#include <Arduino.h>

namespace tx {

Ad9833::Ad9833(uint8_t pinSdata, uint8_t pinSclk, uint8_t pinFsync)
    : pinSdata_(pinSdata), pinSclk_(pinSclk), pinFsync_(pinFsync) {}

void Ad9833::begin() {
    pinMode(pinSdata_, OUTPUT);
    pinMode(pinSclk_, OUTPUT);
    pinMode(pinFsync_, OUTPUT);
    digitalWrite(pinFsync_, HIGH);
    digitalWrite(pinSclk_, LOW);
    digitalWrite(pinSdata_, LOW);
    setEnable(false);
    apply();
}

void Ad9833::writeReg(uint16_t word) {
    digitalWrite(pinFsync_, LOW);
    for (int b = 15; b >= 0; --b) {
        digitalWrite(pinSclk_, LOW);
        digitalWrite(pinSdata_, (word >> b) & 1 ? HIGH : LOW);
        digitalWrite(pinSclk_, HIGH);
    }
    digitalWrite(pinFsync_, HIGH);
}

uint32_t Ad9833::freqToReg(float freqHz) const {
    if (freqHz < FREQ_MIN_HZ) freqHz = FREQ_MIN_HZ;
    if (freqHz > FREQ_MAX_HZ) freqHz = FREQ_MAX_HZ;
    return (uint32_t)((uint64_t)(freqHz * (1ull << 28)) / (uint32_t)AD9833_MCLK_HZ);
}

uint16_t Ad9833::phaseToReg(float phaseDeg) const {
    if (phaseDeg < 0) phaseDeg = 0;
    if (phaseDeg >= 360.0f) phaseDeg = 359.99f;
    return (uint16_t)((phaseDeg / 360.0f) * 4096.0f) & 0x0FFF;
}

void Ad9833::setEnable(bool on) {
    enabled_ = on;
}

void Ad9833::setFrequency(float freqHz) {
    freqHz_ = freqHz;
}

void Ad9833::setPhase(float phaseDeg) {
    phaseDeg_ = phaseDeg;
}

void Ad9833::setWaveform(Ad9833Wave w) {
    wave_ = w;
}

void Ad9833::apply() {
    const uint32_t freg = freqToReg(freqHz_);
    const uint16_t preg = (phaseToReg(phaseDeg_) & 0x0FFF);

    // Control: RESET=1 (bit 8), B28=1 (bit 1) — AD9833 datasheet
    uint16_t ctrl = 0x0102u;
    if (wave_ == Ad9833Wave::Triangle) ctrl |= 0x0004u;   // MODE
    if (wave_ == Ad9833Wave::Square)   ctrl |= 0x0030u;   // OPBITEN | DIV2

    writeReg(ctrl);

    // FREQ0 LSW (bits 13..0)
    writeReg(0x4000u | (uint16_t)(freg & 0x3FFFu));
    // FREQ0 MSW (bits 27..14)
    writeReg(0x4000u | (uint16_t)((freg >> 14) & 0x3FFFu));

    // PHASE0 (0xC000 = phase reg 0)
    writeReg(0xC000u | preg);

    // Control: RESET=0, B28=1, SLEEP1=1 if disabled
    ctrl = 0x0002u;  // B28
    if (wave_ == Ad9833Wave::Triangle) ctrl |= 0x0004u;
    if (wave_ == Ad9833Wave::Square)   ctrl |= 0x0030u;
    if (!enabled_) ctrl |= 0x0040u;   // SLEEP1 — DAC off

    writeReg(ctrl);
}

} // namespace tx
