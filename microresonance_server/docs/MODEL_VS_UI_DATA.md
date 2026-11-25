# Расхождения: данные в модель vs данные на графиках

## 1. MEANING: features при инференсе часто нулевые (исправлено)

**Проблема:** В модель на вкладке MEANING глифы отправляются при каждом срабатывании пайплайна (`OnGlyphForMeaning`). Инварианты `features` (17 чисел из `ExtractTrainerEvent.Invariants()`) заполняются **только** когда `ExtractTrainerEvent` находит 3 экстремума в форме. Если не находит — `featuresForMeaning` остаётся `nil`, в JSON поле `features` не добавляется, Python дополняет нулями до 17.

**При обучении** события в датасет пишутся только при успешном `ExtractTrainerEvent` (trainer_tf.SaveRunToDataset сохраняет TrainerEvent с Invariants()). То есть в train всегда есть ненулевые features.

**Итог:** Train/inference skew — на инференсе часто приходят нулевые features, на обучении таких не было.

**Исправление:** В пайплайне вызывать `OnGlyphForMeaning` только когда есть ненулевые `featuresForMeaning` (т.е. когда удалось построить glyph-event с 3 экстремумами). Тогда в буфер sequence-модели попадают только «полноценные» события, как при обучении.

---

## 2. Графики vs модель: что отображается и что подаётся

| Место | Что показывается | Что идёт в модель |
|-------|------------------|-------------------|
| **ALPHABET** | `Pattern.GlyphShape.Points` (шаблон при первом merge) или `Pattern.GlyphData` (envelope X). Для 3 осей — GlyphData, GlyphDataY, GlyphDataZ. | При каждом срабатывании пайплайна: **текущая** волна — `shape.Points` (64), `glyphData`→x, `glyphDataY`→y, `glyphDataZ`→z (64 каждый), плюс features при успешном ExtractTrainerEvent. |
| **TRAINER** | События из буфера (TrainerEventBuffer / GlyphShapeBuffer) — то же, что сохраняется в датасет. | То же, что пишется в trainer_dataset: shape (64), x,y,z (64), features (17), при event-based — из ExtractTrainerEvent. |
| **MEANING** | Последнее предсказание модели (action_name, confidence). | Буфер из последних `seq_len` глифов: shape, features, x, y, z (и при необходимости reaction). |

**Важно:** На ALPHABET рисуется **сохранённый шаблон** токена (первый merge). В модель при каждом совпадении уходит **текущая** форма волны. Это задумано: классификация по текущей форме, отображение — по эталону.

---

## 3. Размерности и форматы (согласованы)

- **shape / x / y / z:** 64 точки (SHAPE_DIM, TrainerShapePoints, FrozenGlyphPoints). Go: BuildShape(64), GlyphData 64 (32 min + 32 max). Python: to_fixed_length(..., 64).
- **features:** 17 (EVENT_DIM, TrainerEvent.Invariants()). Нормализация: train_model сохраняет feature_mean, feature_std в meta.json; run_meaning_model применяет при инференсе.
- **derive_features_from_raw:** В train_model.py и run_meaning_model.py реализация совпадает (5 статистик × 4 канала = 20).

---

## 4. Reaction (второй алфавит: 16 фаз AM)

**Откуда берётся:** Reaction alphabet (ReactionAlphabetStore). При каждом тике пайплайна вызывается OnPrimaryGlyphBatch: primaryAlphabetStore обновляется по полосам, reactionAlphabetStore накапливает энергию по фазе AM (AmFreqHz). При эмиссии MergeOrAddByReaction вызывается **OnReactionGlyph**(exciterID, bandID, reactionID, vec[16], nowMs).

**Где используется:**

| Этап | Использование |
|------|----------------|
| **Калибровка (TRAIN)** | RunReactionBuffer.Add(exciterID, bandID, vec, nowMs) при каждом OnReactionGlyph. При сохранении run'а (SaveRunToDataset) к каждому event привязывается reaction по **runReactionBuffer.Get(0, ev.BandIdx)** — пишется в JSON: `reaction` (16 float), `reaction_valid`, `reaction_ts`. |
| **Обучение** | train_model.py загружает reaction из JSON. При варианте **full_plus_reaction** модель получает reaction (17 = 16 фаз + valid). valid=1 только если reaction_valid и \|event_ts − reaction_ts\| ≤ REACTION_TS_WINDOW_MS (2000 ms). |
| **Инференс (MEANING)** | **Исправлено:** Go хранит последний reaction по полосе (meaningReactionByBand), обновляет при каждом OnReactionGlyph; при PushGlyph передаёт в JSON `reaction` (16 float), `reaction_valid`, `reaction_ts` по bandIdx. Python (run_meaning_model) читает их и подаёт в модель при full_plus_reaction. |

**Итог:** Reaction из алфавита **записывается в датасет** и **используется при обучении** варианта full_plus_reaction. На **инференсе** reaction **передаётся** из Go в JSON (последний reaction по band при PushGlyph), так что full_plus_reaction работает без train/inference skew.

---

## 5. Откуда берутся данные

- **Пайплайн** (shape_pipeline.go): для каждой классифицированной волны — shape (BuildShape, 64), glyphData/glyphDataY/glyphDataZ (ComputeFrozenGlyphFromSegment, 64). При успешном ExtractTrainerEvent — features = ev.Invariants() (17).
- **Reaction:** gpu.go OnPrimaryGlyphBatch → ReactionAlphabetStore.Accumulate/MergeOrAddByReaction → OnReactionGlyph → RunReactionBuffer.Add. При SaveRunToDataset — Get(0, ev.BandIdx) → JSON.
- **Датасет:** trainer_tf.go пишет event JSON: shape, x, y, z, features, reaction (если reaction_valid). train_model.py загружает те же поля; для sequence — features нормализуются по mean/std по train.
- **Графики:** screen_patterns.go читает Pattern (GlyphShape.Points, GlyphData, GlyphDataY, GlyphDataZ) — без повторного вызова пайплайна.
