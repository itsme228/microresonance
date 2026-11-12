/**
 * Stub for i2c_ll_slave_set_fifo_mode on ESP32-C6 targets.
 * Excluded on ESP32-S3 via build_src_filter in platformio.ini.
 */
#include <stdint.h>

void i2c_ll_slave_set_fifo_mode(void *dev, uint32_t en) {
  (void)dev;
  (void)en;
}
