# Обучение на датасете: как добиться стабильного обучения

Краткие выводы из экспериментов по перебору параметров (experiment_sweep.py).

---

## Рекомендуемые параметры (лучший val_acc на текущем датасете)

На переборе 81 комбинации (seq_len × shape_size × window_step × variant) лучший результат:

- **val_acc ~ 0.835**
- `--sequence-length 32 --shape-size 64 --window-step 4 --sequence-variant features_only`

То есть:

1. **Окно последовательности:** 32 события (seq_len=32).
2. **Разрешение формы волны:** 64 точки (shape_size=64; можно пробовать 48).
3. **Шаг окон:** 4 — плотная нарезка окон (больше сэмплов, обучение идёт стабильнее, чем при auto или 8).
4. **Вариант архитектуры:** `features_only` — только 17 инвариантов по времени + GRU; на малом датасете часто выигрывает у features_plus_shape.

Другие сильные комбинации:

- seq_len=32, shape_size=48, window_step=4, **features_only** → val_acc ~ 0.797  
- seq_len=32, shape_size=32, window_step=4, **features_plus_shape** → val_acc ~ 0.774  

---

## Как запускать обучение

Рекомендуемая команда (подставить свой путь к датасету при необходимости):

```bash
cd server
python3 train_model.py --input trainer_dataset \
  --arch sequence \
  --sequence-length 32 \
  --shape-size 64 \
  --window-step 4 \
  --sequence-variant features_only \
  --epochs 30 \
  --early-stop 8 \
  --out saved_model
```

Если не уверены в параметрах — сначала перебор:

```bash
python3 experiment_sweep.py trainer_dataset --epochs 18
```

Режим `--quick` (меньше комбинаций, быстрее):

```bash
python3 experiment_sweep.py trainer_dataset --epochs 12 --quick
```

Скрипт выведет таблицу val_acc и строку **Recommended** с лучшими флагами для `train_model.py`.

---

## Что важно для датасета

- **Структура:** `trainer_dataset/<action_id>_<name>/run_XXX/<glyph_id>/*.json` — по одному run на нажатие Start–End, внутри run — события по глифам с полями `shape`, `features`, `x`, `y`, `z`, `meta`.
- **Разбиение train/val:** по run (стратифицировано по классу), чтобы в val попадали целые сессии, а не куски — так метрика честнее.
- **Параметры как ручки:** `shape_size` (32/48/64) и `window_step` (4, 8 или auto) — полноценные параметры обучения; при сжатии/разжатии волн имеет смысл их перебирать.

---

## Если обучение «не идёт»

1. Запустить перебор: `python3 experiment_sweep.py trainer_dataset --epochs 18` и взять из вывода **Recommended**.
2. Использовать **window_step=4** при seq_len=32 (или 8 при seq_len=16) — плотные окна обычно лучше.
3. Попробовать **features_only** — меньше входов, меньше риск переобучения на малых данных.
4. Увеличить объём датасета (больше run’ов и классов) и при необходимости снова прогнать experiment_sweep.
