/**
 * ADS8681 driver (datasheet ads8681.pdf)
 * 16-bit SAR ADC, ±2.56V (RANGE_SEL=0100), 1 MSPS, SPI
 * CONVST/CS: rising edge starts conversion. 32 SCLK per frame, NOP to read result.
 */
#ifndef ADS8681_H
#define ADS8681_H

#include <Arduino.h>
#include <SPI.h>

// Registers (Table 7-10), 9-bit addr: 0_xxxxxxxx
#define ADS8681_DEVICE_ID   0x00
#define ADS8681_RST_PWRCTL  0x04
#define ADS8681_SDI_CTL     0x08
#define ADS8681_SDO_CTL     0x0C
#define ADS8681_DATAOUT_CTL 0x10
#define ADS8681_RANGE_SEL   0x14

// RANGE_SEL [3:0]: 0100 = ±0.625×VREF = ±2.56V
#define ADS8681_RANGE_PM2_56V  0x04

// Commands (Table 7-5): 32-bit. NOP=0. WRITE=11010_00_addr16_data16
// WRITE: 0x1A << 24 | (addr9<<15) | data16. Addr 14h: 9bit=0_00010100
#define ADS8681_CMD_NOP     0x00000000u
#define ADS8681_CMD_WRITE   0xD8000000u  // 11011... for SET, 11010 for WRITE

// RANGE_SEL_REG: bits [3:0]=RANGE_SEL. Addr 14h: 0x028 (9bit)
// WRITE to 14h: 11010_00_0_00010100_0000000000000100 = 0xD0140004
// Result: D[31:16] = 16-bit conversion, right-justified in 32-bit output

class ADS8681 {
public:
  ADS8681(SPIClass& spi, int8_t cs, int8_t rst = -1)
    : _spi(spi), _cs(cs), _rst(rst), _freq(10000000) {}

  bool begin(uint32_t freq = 10000000);
  void end();

  // Start conversion (CS high), wait t_conv, then read: CS low, 32 SCLK, CS high (starts next)
  uint16_t readOnce();
  // Pipelined: CS high (start), wait 1us, CS low, 32 SCLK (get previous), CS high
  void startConversion();
  uint16_t getResult();

private:
  SPIClass& _spi;
  int8_t _cs, _rst;
  uint32_t _freq;

  void csLo() { digitalWrite(_cs, LOW); }
  void csHi() { digitalWrite(_cs, HIGH); }
  void writeCommand32(uint32_t cmd);
  uint32_t read32();
};

#endif
