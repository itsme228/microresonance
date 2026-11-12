/**
 * AD9833 DDS driver — bit-banged SPI, one instance per pin group.
 * Frequency 1-2000 Hz, phase 0-360 deg, waveforms: SINE, TRIANGLE, SQUARE.
 */
#ifndef AD9833_H
#define AD9833_H

#include <cstdint>
#include <cstddef>

namespace tx {

enum class Ad9833Wave : uint8_t {
    Sine = 0,
    Triangle = 1,
    Square = 2,
};

/**
 * Single AD9833 DDS instance on dedicated SPI pins. Non-blocking.
 */
class Ad9833 {
public:
    Ad9833(uint8_t pinSdata, uint8_t pinSclk, uint8_t pinFsync);

    void begin();
    void setEnable(bool on);

    /** Carrier frequency in Hz, 1..2000. */
    void setFrequency(float freqHz);
    /** Phase offset in degrees, 0..360. */
    void setPhase(float phaseDeg);
    void setWaveform(Ad9833Wave w);

    /** Commit frequency, phase, and waveform registers to the AD9833. */
    void apply();

private:
    void writeReg(uint16_t word);
    uint32_t freqToReg(float freqHz) const;
    uint16_t phaseToReg(float phaseDeg) const;

    uint8_t pinSdata_;
    uint8_t pinSclk_;
    uint8_t pinFsync_;

    float   freqHz_  = 100.0f;
    float   phaseDeg_ = 0.0f;
    Ad9833Wave wave_ = Ad9833Wave::Sine;
    bool    enabled_ = false;
};

} // namespace tx

#endif
