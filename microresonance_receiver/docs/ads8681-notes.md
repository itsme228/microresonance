# ADS8681 16-Bit SAR ADC Hardware Integration Notes

## Pin Functions
| Pin | Function |
| :--- | :--- |
| `CONVST/CS` | Starts conversion on rising edge (`↑`); `SDO` is High-Z when `CS = 1` |
| `SDO-0` | Primary serial data output |
| `ALARM/SDO-1` | Alarm output or second bit in dual-SDO mode |
| `SDI` | Serial command/register input |
| `SCLK` | Serial clock input |

## 32-Bit Frame Format (Tables 7-6, 7-7)
- **`D[31:16]`** — 16-bit conversion result, right-justified
- **`D[15:0]`** — Optional `DEVICE_ADDR`, `RANGE`, `ALARM`, `PARITY` depending on `DATAOUT_CTL`

## SPI-00-S Protocol
- `CPOL = 0`, `CPHA = 0` (`SPI_MODE0`)
- `SDI_CTL = 00h`, `SDO_CTL = 00h`
- **MSB first**, 32 `SCLK` cycles per frame
- First 16 bits clocked out on `SDO-0` (`D[31:16]`) contain the conversion sample

## Key Registers
- **`SDO_CTL (0Ch)`:** `SDO_MODE = 00` (follows `SDI` SPI mode), `SDO1_CONFIG = 00` (single `SDO-0`)
- **`DATAOUT_CTL (10h)`:** `DATA_VAL = 000` (output conversion value)
- **`RANGE_SEL (14h)`:** `0100b` $\to \pm 0.625 \times V_{\text{REF}} = \pm 2.56\text{ V}$

## Timing
- $t_{\text{conv}} = 665\text{ ns}$
- Throughput up to `1 MSPS` (`32 SCLK` per read)
