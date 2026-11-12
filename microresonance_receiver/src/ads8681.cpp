/**
 * ADS8681 implementation (TI ADS8681 datasheet)
 * SPI-00-S (CPOL=0, CPHA=0). CONVST/CS: rising edge starts conversion. 32 SCLK, NOP to read.
 * ±2.56V: RANGE_SEL=0100. t_conv=665ns, 1 MSPS.
 */
#include "ads8681.h"

bool ADS8681::begin(uint32_t freq) {
  _freq = freq;
  Serial.printf("[ADS8681] begin: CS=%d RST=%d freq=%lu Hz\n", _cs, _rst, (unsigned long)freq);

  pinMode(_cs, OUTPUT);
  digitalWrite(_cs, HIGH);

  if (_rst >= 0) {
    Serial.println("[ADS8681] RST: L 1ms -> H 25ms");
    pinMode(_rst, OUTPUT);
    digitalWrite(_rst, LOW);
    delay(1);
    digitalWrite(_rst, HIGH);
    delay(25);
  } else {
    Serial.println("[ADS8681] RST pin unused (pin=-1), assuming RST=VCC");
  }

  _spi.setFrequency(freq);
  _spi.setDataMode(SPI_MODE0);
  _spi.setBitOrder(MSBFIRST);
  Serial.println("[ADS8681] SPI: MODE0, MSBFIRST");

  // Read DEVICE_ID (expected 0x8x01 for ADS8681)
  // READ command: opcode=01001 (5 bit), addr=0x00 (9 bit): 0x48000000
  const uint32_t readIdCmd = 0x48000000u;
  writeCommand32(readIdCmd);
  delayMicroseconds(10);
  uint32_t idRaw = read32();
  uint16_t deviceId = (uint16_t)(idRaw >> 16);
  Serial.printf("[ADS8681] DEVID: raw32=0x%08lX -> id=0x%04X (expected 0x8x01)\n",
                (unsigned long)idRaw, (unsigned)deviceId);

  // RANGE_SEL (0x14) = 0x04 (±2.56V)
  // WRITE cmd: opcode 11010, addr 0x14, data=0x0004 = 0xD0140004
  const uint32_t rangeCmd = 0xD0140004u;
  Serial.printf("[ADS8681] write RANGE_SEL: 0x%08lX (±2.56V)\n", (unsigned long)rangeCmd);
  writeCommand32(rangeCmd);
  delay(1);

  // CS pulse (L->H) triggers first conversion
  Serial.println("[ADS8681] CS L 2us -> H (start first conversion)");
  csLo();
  delayMicroseconds(2);
  csHi();
  delayMicroseconds(2);  // t_conv=665ns, 2us margin

  uint32_t raw = read32();
  uint16_t data = (uint16_t)(raw >> 16);
  Serial.printf("[ADS8681] test: raw32=0x%08lX -> data=0x%04X (%u)\n", (unsigned long)raw, (unsigned)data, (unsigned)data);
  if (data == 0x0000 || data == 0xFFFF) {
    Serial.println("[ADS8681] WARNING: 0x0000/0xFFFF — verify MISO, supply, GND, CS");
  }

  // Verify RANGE_SEL register readback
  const uint32_t readRangeCmd = 0x48140000u;  // READ addr=0x14
  writeCommand32(readRangeCmd);
  delayMicroseconds(10);
  uint32_t rangeRaw = read32();
  Serial.printf("[ADS8681] verify RANGE_SEL: raw32=0x%08lX -> reg=0x%04X (expected 0x0004)\n",
                (unsigned long)rangeRaw, (unsigned)(rangeRaw >> 16));

  return true;
}

void ADS8681::end() {
  digitalWrite(_cs, HIGH);
}

void ADS8681::writeCommand32(uint32_t cmd) {
  csLo();
  _spi.beginTransaction(SPISettings(_freq, MSBFIRST, SPI_MODE0));
  _spi.transfer((cmd >> 24) & 0xFF);
  _spi.transfer((cmd >> 16) & 0xFF);
  _spi.transfer((cmd >> 8) & 0xFF);
  _spi.transfer(cmd & 0xFF);
  _spi.endTransaction();
  csHi();
}

uint32_t ADS8681::read32() {
  csLo();
  _spi.beginTransaction(SPISettings(_freq, MSBFIRST, SPI_MODE0));
  uint32_t v = (uint32_t)_spi.transfer(0) << 24
             | (uint32_t)_spi.transfer(0) << 16
             | (uint32_t)_spi.transfer(0) << 8
             | (uint32_t)_spi.transfer(0);
  _spi.endTransaction();
  csHi();
  return v;
}

uint16_t ADS8681::readOnce() {
  csHi();
  delayMicroseconds(1);
  csLo();
  _spi.beginTransaction(SPISettings(_freq, MSBFIRST, SPI_MODE0));
  uint32_t r = (uint32_t)_spi.transfer(0) << 24 | (uint32_t)_spi.transfer(0) << 16
             | (uint32_t)_spi.transfer(0) << 8  | (uint32_t)_spi.transfer(0);
  _spi.endTransaction();
  csHi();
  return (uint16_t)(r >> 16);
}

void ADS8681::startConversion() {
  csHi();
}

uint16_t ADS8681::getResult() {
  csLo();
  _spi.beginTransaction(SPISettings(_freq, MSBFIRST, SPI_MODE0));
  uint32_t r = (uint32_t)_spi.transfer(0) << 24 | (uint32_t)_spi.transfer(0) << 16
             | (uint32_t)_spi.transfer(0) << 8  | (uint32_t)_spi.transfer(0);
  _spi.endTransaction();
  csHi();
  return (uint16_t)(r >> 16);
}
