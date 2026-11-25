# Протокол Microresonance V1.1

## Архитектура системы

```
Emitter (ESP32-S3-CAM)          Receivers (ESP32-S3 × N)
  PCM5102A I2S audio                ADXL355 SPI 4000 Hz
  f = protocol.frequency            IQ-демодуляция по Unix-времени
         │                                    │
         └──── HTTP GET /api/protocol ◄────── ┘
                         │
                    Server (Go)
                    :5555 TCP  — приём DspPacket / RawSyncPacket
                    :8080 HTTP — /api/protocol, /api/sync_trigger
```

**Общие часы (V1.1):** SNTP (`pool.ntp.org`) на каждом устройстве + Linux NTP на сервере.
Точность: 1–5 мс в типичных сетях Wi-Fi. Постоянный SNTP-оффсет между устройствами
автоматически компенсируется сервером за первые 8 пакетов (circular-mean калибровка).

---

## Пакет v2 COMPACT (MAGIC 0x4D53) — 183 байта

Ресивер → Сервер, каждые ~128 мс (DSP_WINDOW_MS).

| Смещение | Размер | Поле              | Тип      | Описание                              |
|----------|--------|-------------------|----------|---------------------------------------|
| 0        | 2      | magic             | uint16 LE | 0x4D53                               |
| 2        | 1      | sensor_id         | uint8    | ID датчика (задаётся в main.cpp)      |
| 3        | 1      | n_peaks           | uint8    | кол-во заполненных peaks[], 1..8      |
| 4        | 8      | tsf_us            | uint64 LE | **Unix мкс (gettimeofday)** начала окна |
| 12       | 4      | demod_freq_hz     | float32 LE | частота несущей                     |
| 16       | 4      | window_samples    | uint32 LE | обычно DSP_WINDOW_SAMPLES = 512      |
| 20       | 128    | peaks[8]          | 8×DspPeak | подробнее ниже                       |
| 148      | 32     | spectrum[32]      | uint8[32] | лог-амплитуды 0–255, 32 полосы       |
| 180      | 1      | flags             | uint8    | bit0=SNR>6dB, bit1=saturated         |
| 181      | 2      | battery_mv        | uint16 LE | (зарезервировано, = 0)               |

**DspPeak (16 байт):**
| Поле       | Тип      | Описание                          |
|------------|----------|-----------------------------------|
| freq_hz    | float32  | центральная частота пика          |
| magnitude  | float32  | амплитуда (g)                     |
| phase_deg  | float32  | фаза 0–360°, привязана к Unix-времени |
| snr        | float32  | SNR в дБ                          |

---

## Пакет v2 RAW SYNC (MAGIC 0x4D54) — 6160 байт

Ресивер → Сервер, только в режиме `streamMode = "raw"`.

| Смещение | Размер | Поле              | Тип       | Описание                    |
|----------|--------|-------------------|-----------|-----------------------------|
| 0        | 2      | magic             | uint16 LE | 0x4D54                      |
| 2        | 1      | sensor_id         | uint8     |                             |
| 3        | 1      | reserved          | uint8     | = 0                         |
| 4        | 8      | tsf_us            | uint64 LE | Unix мкс начала окна        |
| 12       | 4      | window_samples    | uint32 LE | = 512                       |
| 16       | 2048   | raw_x[512]        | float32[] | сырые данные оси X, g       |
| 2064     | 2048   | raw_y[512]        | float32[] | оси Y                       |
| 4112     | 2048   | raw_z[512]        | float32[] | оси Z                       |

---

## IQ-демодуляция и когерентность фазы (V1.1)

### Формула опорного фазора

```
phi_ref(t_unix_s) = frac(f × t_unix_s) × 2π
```

где `frac(x) = x - floor(x)` — дробная часть.

Реализация на ресивере (`dsp_task.cpp`): **rotating phasor**
```cpp
double phi0 = fmod(freq * (w[0].tsf_us * 1e-6), 1.0) * 2π;  // начальная фаза
double dphi = 2π × freq / DSP_ODR_NOMINAL;                    // шаг 1/fs
// далее c_ref, s_ref обновляются 4 умножениями без trig вызовов
```

Реализация на эмиттере (`main.cpp`): при `SYNC_TRIGGER`
```cpp
double t = unixNowUs() * 1e-6;
phaseL = fmod(fL * t, 1.0) * 2π;  // та же формула
```

Оба устройства используют **`gettimeofday()`** (SNTP-синхронизированное Unix-время).

### Аналитический SNR (без второго прохода)

```
noise_power = total_power - 2 × sig_power
SNR_dB = 10 × log10(sig_power / noise_power)
```

Точна при >1 полного периода частоты в окне (512 сэмплов при 311 Гц = 39.8 периодов ✓).

---

## SYNC_TRIGGER

Сервер публикует в `GET /api/protocol` → `protocol.syncTrigger`:

```json
{
  "syncTrigger": {
    "targetUnixUs": 1748000000800000,
    "freqHz": 311.0,
    "sessionId": 42
  }
}
```

Запуск: `POST /api/sync_trigger` → сервер вычисляет `targetUnixUs = now + 800 мс`.

При `gettimeofday() >= targetUnixUs`:
- **Эмиттер:** `resetPhaseCoherent()` — фаза синхронизируется с Unix-временем
- **Ресивер:** сбрасывает счётчик накопления (пропускает сэмплы до целевого времени)

---

## Метрики (сервер)

| Метрика              | Цель     | Вычисление (buffer.go)                                              |
|----------------------|----------|---------------------------------------------------------------------|
| Sync_Drift_Us        | < 50 мкс | `max(tsf_us) − min(tsf_us)` по всем датчикам (последний пакет)     |
| Phase_Stability_Deg  | < 2°     | StdDev `peaks[0].phase_deg` за последние 240 пакетов (~60 с)       |
| Mag_Stability_G      | < 0.005g | StdDev `peaks[0].magnitude` за 240 пакетов                         |
| Transport_Latency_Ms | < 250 мс | `server_arrival_ns/1e6 − tsf_us/1000 − window_ms`                  |
| Delta_Phi_Deg        | —        | `phase[s0] − phase[s1]` — PhaseCalibOffsetDeg                      |
| PhaseCalibOffset_Deg | —        | circular mean DeltaPhiDeg за первые 8 пакетов (SNTP-компенсация)   |

---

## Диагностика

### Сервер (Go)

Флаг `-diagInterval=10s` запускает периодический лог:
```
[SENSOR01] MODE:COMPACT  SNR: 18.4dB Mag: 0.0231g Phase: 45.2° Lat:  142ms Peaks:3 Age:128ms
           Peak[0]  311.0Hz  0.023g  45.2° SNR: 18.4dB
           Peak[1]  622.0Hz  0.008g  90.4° SNR: 11.2dB
           Stability phase=1.82° mag=0.00021g [STABLE]
[SYNC   ] Drift:  12.3 µs | ΔΦ:  +3.1° | Calib: YES offset=45.1°
[QUALITY] SyncOK:true Sensors:2 ΔΦ:+3.1° StdDev:1.82°
```

Флаг `-diagFile=diag.log` — запись в файл вместо stderr.

### ESP32 ресивер (Serial)

Каждые 10 секунд:
```
[DIAG] pkts=78 overruns=0 dsp_us=412 mode=COMPACT freq=311.0Hz
```

- `dsp_us` < 1000 мкс — контроллер успевает (осталось ~99.7% CPU на захват)
- `overruns > 0` — dspTask не успевает, уменьшить окно или переключить в RAW

---

## Обратная совместимость

| MAGIC  | Формат           | Размер  | Обработка сервером               |
|--------|------------------|---------|----------------------------------|
| 0x4D52 | v1 сырые сэмплы  | ~780 B  | `DecodeDataPacket` → `AdxlX/Y/Z` |
| 0x4D53 | v2 COMPACT       | 183 B   | `DecodeDspPacket` → `DspSnapshot`|
| 0x4D54 | v2 RAW SYNC      | 6160 B  | `DecodeDspPacket` → `AdxlX/Y/Z`  |
