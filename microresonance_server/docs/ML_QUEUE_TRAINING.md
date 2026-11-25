# ML Queue: датасет, архитектура, обучение

## 1. Данные в датасете

**Структура:** `trainer_dataset/<action_id>_<name>/run_XXX/<glyph_id>/NNNNNN.json`

Каждый JSON — одно событие (glyph-event):
- **features** (17): инварианты формы — A1,A2,A3, DeltaA12, DeltaA23, T12, T23, Symmetry, FreqCenter, Bandwidth, U1, U2, E1, E2, AgeSincePrevEvent, EventIndexInRun, RunLength
- **shape** (64): общий график глифа
- **x, y, z** (64): оси X/Y/Z
- **reaction** (16): AM-фазы (если есть)
- **meta**: ts, band_idx, glyph_id, freq_center, bandwidth, age_since_prev_ms, event_index_in_run, run_length

**Источник:** Go сервер сохраняет через `SaveRunToDataset` при каждом End (нажатие Start–End в калибровке).

## 2. Архитектура

**Двухуровневая:**

1. **CSSM (Continuous State Model)** — GRU по FrameSummary sequence
   - Вход: bandEnergy[7], totalEnergy, quality, axisRatio, exciter, am_freq
   - Выход: state_t (64)

2. **Glyph Encoder** — MLP по сырым признакам события
   - Вход: proto_sig (8), axis_sig (4), amp, dt, band_idx, is_new
   - Выход: embedding (64)

3. **Head** — Event Transformer + классификатор
   - Вход: state + event embeddings + mask
   - Self-attention (4 головы), pooling, Dense → softmax

## 3. Почему val_accuracy была 0

1. **Glyph encoder не обучался** — embeddings получались из необученного `glyph_enc`, т.е. случайные векторы.
2. **CSSM не обучался** — state вычислялся необученным CSSM.
3. **Обучался только head** — на случайных embeddings + случайном state.
4. **Без shuffle** — validation_split брал последние 20% батчей; батчи шли по action_id, поэтому val мог содержать только один класс.
5. **Мало батчей** — при 4 батчах val = 1 батч, модель не успевала.

## 4. Исправления (v2 - новая архитектура)

### Проблемы старой архитектуры:
- Использовала только `proto_sig` (8 чисел) и `axis_sig` (4 числа) — слишком сжато
- Терялась информация о форме волны и трёхосевых данных

### Новая архитектура (`train_glyph_model.py`):
```
Inputs:
  - shape (64) → 1D CNN → coarse shape detection
  - x, y, z (32 каждая) → 1D CNN per axis → detailed orientation
  - features (17) → MLP → invariants (нормализованы z-score)
  - reaction (16) → MLP → AM phases

Fusion → BatchNorm → Dropout → Dense → softmax
```

### Ключевые улучшения:
- **Полные данные:** используются shape (64), x/y/z (первые 32 точки — вторые 32 дублируют), features (17), reaction (16)
- **Z-score нормализация:** features нормализуются по mean/std с train set, параметры сохраняются в meta.json
- **Batch normalization:** стабилизирует обучение
- **Class weights:** для балансировки классов
- **Stratified split по run:** val не пересекается с train по сессиям
- **Early stopping + ModelCheckpoint:** сохраняется лучшая модель по val_accuracy

### Результаты:
- **Полная модель** (shape + x/y/z + features + reaction): **val_accuracy = 99.2%**
- **Features-only модель** (`train_features_model.py`): **val_accuracy = 80.5%** (для realtime когда shape недоступен)

## 5. Запуск

```bash
cd server
python3 train_ml_queue_model.py --dataset trainer_dataset --epochs 30 --batch 16
```

## 6. Выход

- `saved_model/model.keras` — полный pipeline (trainable_pipeline)
- `saved_model/meta.json` — num_classes, idx_to_id, id_to_name
