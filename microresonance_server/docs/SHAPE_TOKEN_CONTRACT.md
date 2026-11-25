# Контракт: токенизация по форме и остаточной энтропии

Токены определяются **по форме (shape)**, а не по частоте или мощности. Частота/амплитуда — условия эксперимента; токенизация инвариантна к ним. Разбор сложного сигнала — по **остаточной энтропии**: итеративно выделяем повторяющийся паттерн формы, вычитаем вклад, пока остаток не станет шумом.

**Оговорка:** Полная независимость от частоты физически невозможна (тело и датчик — фильтры). Контракт требует **инвариантности к масштабу частоты** (нормализация по периоду и спектральной огибающей) и **инвариантности к мощности** (нормализация амплитуды, робастные признаки).

---

## A) Архитектура (модули, входы/выходы)

### 1) Acquisition
- **Вход:** поток с датчика (x, y, z, timestamp).
- **Выход:** `raw_ring` буфер 10–20 сек: `x, y, z`, timestamp, `Fs`.
- **Опционально:** `mag2(t) = x² + y² + z²` как инвариант энергии (вычислять на лету или хранить).

### 2) Preprocessing
- **Вход:** срез из `raw_ring` (окно по времени).
- **Выход:**
  - `s_norm` — нормализованный мастер-канал (по окну): remove DC, highpass 0.2–1 Hz, `robust_scale = median(|s|)` или RMS, `s_norm = s / (robust_scale + eps)`; silence gate по порогу.
  - `axis_signature` — признаки осей (XY/XZ/YZ ratios, coherence) для различения торсии/ориентации; **не** ключ токена.
- **Канал для событий:** `mag2(t)` или `sqrt(mag2)`; для формы можно использовать один канал (X или mag2) после нормализации.

### 3) ShapeRepresentation
- **Вход:** окно `s_norm` (например 1.0 s), опционально оценка локального периода.
- **Выход:** `ShapeVector` — вектор длины 64–128 в **нормализованной фазе** (0..1 на цикл), инвариантный к частоте и амплитуде.
- **Внутри:**
  - Оценка локальной периодичности: autocorrelation peak / cepstrum / ZCR на bandpassed сигнале, или multi-resolution (scalogram) → доминирующий scale.
  - Resample по фазе: N точек на цикл (64 или 128).
  - Альтернатива: log-frequency envelope (шкалы, не бины Hz) + нормализация; shape = сглаженная огибающая + относительные пики.
- **Рекомендация:** одно основное представление — phase-normalized cycle glyph + envelope features.

### 4) CandidateDiscovery (не по частоте)
- **Вход:** окно (например 1.0 s) `s_norm`, параметры сегментации.
- **Выход:** список **сегментов-кандидатов**, каждый → `ShapeVector`; затем кластеры форм.
- **Логика:**
  - Сегменты: по energy/onset (магнитуда) + минимальная длина; или sliding window с шагом (например 50 ms).
  - Каждый сегмент → BuildShape(segment) → shape_vector.
  - Кластеризация по форме: `distance(shape_i, shape_j)` = cosine distance или DTW-like по нормализованным векторам (64–128); быстрая аппроксимация: cosine + low-dim (PCA на лету / random projection).
- **Результат:** несколько кластеров форм; каждый кластер — потенциальный токен (глиф).

### 5) ResidualDecomposer (остаточная энтропия)
- **Вход:** окно `residual_features` (или residual в пространстве признаков), алфавит токенов (PatternStore / ShapeClusterStore), параметры `max_iters`, `H_thr`, `min_ΔH`.
- **Выход:** список `Occurrence` (token_id, timestamp, confidence, ΔH); обновлённый residual для следующей итерации.
- **Алгоритм:** см. псевдокод ниже (FindBestToken, SubtractToken, цикл по энтропии).

### 6) PatternStore (Token Store)
- **Вход:** новый кандидат-глиф (ShapeVector + метаданные) или запрос списка.
- **Выход:** merge или новый токен; список токенов для UI.
- **Ключ токена:** только форма (shape_distance). Merge/new **только** по shape_distance; частота/амплитуда — метаданные.
- **Хранить:** GlyphShape (64–128 points, normalized), GlyphEnvelope/features, variability (σ по точкам), Occurrences, last_seen, confidence; опционально freq_range, amp_range (наблюдавшиеся).

### 7) UI
- **RAW:** сырые сигналы и спектры.
- **DISCOVERY** (вместо CANDIDATES): найденные сегменты и кластеры **по форме**, их score ΔH, как вычитаются из residual; мини-графики «оригинал → residual».
- **LOCKED (TOKENS):** алфавит токенов (глифы по форме); рядом метаданные: freq_range, amp_range (для науки).

**LAB-панель (обязательные ручки):** window_len, step; normalization (RMS/median, on/off); representation (phase_norm cycles, points_per_cycle); entropy_gate (H_thr, min_ΔH, max_iters); matching (distance_thr, min_confidence); learning (EMA_update 0/off, update_only_if_conf>thr).

---

## B) Инвариантность (обязательные требования)

- **Мощность:** все shape-векторы нормализуются по robust_scale; абсолютная амплитуда **запрещена** как ключ токена.
- **Частота:** shape строится в нормализованной фазе (0..1) или по scale-инвариантному спектру; merge/new **только** по shape_distance; freq_range — только метаданные.
- **Ориентация датчика:** mag2 — основной канал для сегментации; axis_signature — доп. признак, не ключ.

---

## C) Контракт структур данных (поля без лишнего)

### ShapeVector
Идентичность формы; частота/амплитуда не входят.

| Поле           | Тип           | Описание |
|----------------|---------------|----------|
| Points         | []float64     | Длина 64 или 128; значения в [-1, 1] или [0, 1]; нормализованная форма одного цикла (phase 0..1). |
| PointsSigma    | []float64     | Опционально: σ по точкам для допуска при сравнении. |
| Envelope       | []float64     | Опционально: сглаженная огибающая (например 16 точек) для быстрого сравнения. |

Сравнение: только по Points (и опционально Envelope). Cosine distance или L2 после нормализации; **не** использовать абсолютные значения амплитуды/частоты.

### Token (Pattern по форме)
Токен = один глиф формы; ID по форме, не по частоте.

| Поле           | Тип           | Описание |
|----------------|---------------|----------|
| ID             | int           | Уникальный ID (присваивается кластеру формы). |
| Label          | string        | "Pattern 01" и т.д. |
| GlyphShape     | ShapeVector   | Эталон формы; при merge перезаписывается новым (эволюция). |
| Occurrences    | int           | Счётчик вхождений. |
| LastSeenTs     | int64         | Unix ms последнего совпадения. |
| Confidence     | float64       | 0..1, от качества совпадений. |
| FreqRangeHz    | [2]float64    | Метаданные: [min_f, max_f] наблюдавшиеся (не ключ). |
| AmpRange       | [2]float64    | Метаданные: [min_amp, max_amp] (норм.) (не ключ). |
| AxisSignature  | *AxisSig      | Опционально: XY/XZ/YZ ratios (метаданные). |

Merge: MergeOrAddByShape (форма/CoarseSig или fallback по GlyphData). При merge **перезаписывать GlyphShape и GlyphData новым** (эволюция); история с таймингами — LastNGlyphTs для нейросети.

### Occurrence
Одно срабатывание токена в окне.

| Поле           | Тип           | Описание |
|----------------|---------------|----------|
| TokenID        | int           | ID токена из PatternStore. |
| OffsetSamples  | int           | Смещение в сэмплах (для выравнивания при SubtractToken). |
| StartTs        | int64         | Начало вхождения (unix ms). |
| EndTs          | int64         | Конец (опционально). |
| Confidence     | float64       | 0..1. |
| DeltaH         | float64       | Снижение энтропии при вычитании этого токена. |

### ResidualStep
Один шаг итерации разложения по остаточной энтропии.

| Поле           | Тип           | Описание |
|----------------|---------------|----------|
| Iteration      | int           | Номер итерации (0, 1, …). |
| HBefore        | float64       | Энтропия остатка до шага. |
| HAfter         | float64       | Энтропия после вычитания токена. |
| DeltaH         | float64       | HBefore - HAfter. |
| TokenID        | int           | Какой токен вычли. |
| Occurrence     | Occurrence    | Параметры вхождения (offset, confidence). |

### AxisSignature (опционально)
Только метаданные, не ключ токена.

| Поле           | Тип           | Описание |
|----------------|---------------|----------|
| RatioXY        | float64       | X/Y в выбранном диапазоне. |
| RatioXZ        | float64       | X/Z. |
| RatioYZ        | float64       | Y/Z. |
| Coherence      | float64       | Опционально: согласованность осей. |

---

## D) Псевдокод основных функций

### BuildShape(window []float64, Fs float64, pointsPerCycle int) ShapeVector
```
1. Оценить локальный период T (autocorr peak / cepstrum / ZCR на bandpassed window).
   Если T не найден — использовать fallback: T = len(window)/(2*Fs) (половина окна как один цикл).
2. N_cycles = количество полных циклов в window по T (минимум 1).
3. Выделить один цикл (или усреднить несколько): segment = window[0 : T*samples_per_sec] или взять центральный цикл.
4. Resample segment в pointsPerCycle точек по фазе (линейная интерполяция): phase[i] = i / pointsPerCycle, i = 0..pointsPerCycle-1.
5. Нормализовать: вычесть среднее, поделить на (RMS + eps) или на (max - min + eps) → значения в [-1, 1].
6. Вернуть ShapeVector{ Points: resampled }.
```

### Entropy(features []float64) float64
```
1. features — распределение энергии (например по бинам wavelet scales или по normalized spectrum envelope), либо гистограммные бинны.
2. Нормализовать в распределение: p[i] = features[i] / (sum(features) + eps).
3. H = -sum(p[i] * log2(p[i] + eps)).
4. Нормализовать H в 0..1: H_norm = H / log2(len(features)) (или фиксированный max_entropy).
5. Вернуть H_norm.
```

### FindBestToken(residual_features, tokens []Token) (bestTokenID int, bestDeltaH float64, bestOccurrence Occurrence)
```
1. H0 = Entropy(residual_features).
2. bestTokenID = -1, bestDeltaH = 0.
3. Для каждого токена t в tokens:
   a. Найти выравнивание: offset = argmax cross_correlation(residual, t.GlyphShape.Points) или DTW alignment.
   b. residual_after = SubtractToken(residual, t, offset) (копия, не мутировать исходный residual здесь).
   c. H1 = Entropy(residual_after).
   d. ΔH = H0 - H1.
   e. Если ΔH > bestDeltaH и ΔH >= min_ΔH: bestDeltaH = ΔH, bestTokenID = t.ID, bestOccurrence = { TokenID: t.ID, OffsetSamples: offset, Confidence: ..., DeltaH: ΔH }.
4. Вернуть (bestTokenID, bestDeltaH, bestOccurrence).
```

### SubtractToken(residual []float64, token Token, offset int) []float64
```
1. out = copy(residual).
2. shape = token.GlyphShape.Points; L = len(shape).
3. Для каждой позиции i в residual, где перекрывается shape (i >= offset и i < offset + L):
   j = i - offset; если j < L: out[i] -= scale * shape[j]. (scale = подобранный коэффициент, например по корреляции, или soft subtraction 0.8).
4. Вернуть out.
```

### ClusterShapes(shapes []ShapeVector, distThr float64) [][]int
```
1. Кластеры = пустой список списков индексов.
2. Для каждого индекса i:
   - Найти кластер c, такой что min over j in c of Distance(shapes[i], shapes[j]) < distThr.
   - Если нашёлся: добавить i в c; иначе создать новый кластер [i].
3. Distance = cosine distance или L2 после нормализации: 1 - dot(a,b)/(norm(a)*norm(b)) для векторов a, b (Points).
4. Вернуть кластеры (каждый кластер — список индексов в shapes).
```

### ResidualDecomposer (главный цикл)
```
Вход: window (s_norm), tokens (PatternStore.List()), max_iters, H_thr, min_ΔH.
1. residual = ToFeatures(window)  // например shape-представление всего окна или wavelet coefficients.
2. steps = [].
3. Для iter = 0 .. max_iters-1:
   a. H = Entropy(residual).
   b. Если H <= H_thr: break.
   c. (tokenID, ΔH, occ) = FindBestToken(residual, tokens).
   d. Если tokenID < 0 или ΔH < min_ΔH: break.
   e. Записать ResidualStep{ Iteration: iter, HBefore: H, TokenID: tokenID, DeltaH: ΔH, Occurrence: occ }.
   f. residual = SubtractToken(residual, token_by_id(tokenID), occ.OffsetSamples).
   g. Зафиксировать Occurrence (timestamp, confidence).
4. Вернуть steps и список Occurrence.
```

---

## E) Параметры по умолчанию

| Параметр           | Значение  | Описание |
|--------------------|-----------|----------|
| window_len         | 1.0       | с, длина окна. |
| step               | 0.05      | с, шаг скользящего окна (50 ms). |
| points_per_cycle   | 128       | точек на цикл в ShapeVector. |
| max_iters          | 5         | макс. итераций остаточной энтропии. |
| H_thr              | 0.6       | останов, если H(residual) <= H_thr. |
| min_ΔH             | 0.05      | минимальное снижение энтропии для учёта токена. |
| dist_thr           | 0.2       | порог shape distance для merge/кластеризации (cosine: 0.2 ≈ довольно строго). |
| min_confidence     | 0.5       | порог уверенности для записи Occurrence. |
| normalization      | median    | robust_scale: median(\|s\|) или RMS. |
| representation     | phase_norm | 1 цикл, 128 точек. |

---

## F) Realtime: ESP32 vs сервер

Чтобы «реально поехало» без тяжёлой математики на устройстве:

| Где              | Что делать |
|------------------|------------|
| **ESP32**        | Сбор данных (x,y,z, ts); буфер raw_ring 10–20 с; простая нормализация (DC remove, optional highpass, scale по окну); отправка окна на сервер (HTTP POST: window_len, step, сэмплы). Управление режимами; отрисовка UI по данным, полученным с сервера (токены, DISCOVERY). |
| **Сервер (HTTP)**| Приём окна; Preprocessing (полная нормализация, mag2, axis_signature); ShapeRepresentation; CandidateDiscovery (сегменты + кластеризация форм); ResidualDecomposer (энтропия, FindBestToken, SubtractToken); PatternStore (merge по shape); ответ: список токенов, DISCOVERY (кластеры, ΔH, residual), LOCKED (глифы). |
| **ESP32 UI**     | Рисует RAW из локального буфера; DISCOVERY и LOCKED — по данным с сервера (периодический запрос или WebSocket). |

На ESP32 **не** делать: полный DTW, кластеризацию, итеративную энтропию. Только сбор + нормализация + передача + отображение.

---

## G) Что изменить в текущем коде (конкретно)

1. **Убрать:** идею «candidate = peak f0». CANDIDATES больше не основаны на FFT max.
2. **Добавить:** модуль `ShapeRepresentation`: вход — окно (mag2 или X), выход — `ShapeVector` (phase-normalized, 64–128 точек).
3. **Заменить:** `CandidateStore` на `ShapeClusterStore`: кластеризация по cosine distance (и быстрая projection при необходимости).
4. **Добавить:** `ResidualDecomposer`: хранит residual; на каждом шаге выбирает токен с max ΔH; вычитает вклад (soft subtraction).
5. **PatternStore:** хранить токены по форме; merge **только** по shape_distance (частоту не использовать как ключ).
6. **UI:** вкладка DISCOVERY: текущий residual entropy H; список найденных паттернов с ΔH; визуализация «оригинал → residual» (числа/мини-графики).
7. **Тест:** один и тот же токен должен детектироваться при разных f0 и мощностях, если форма совпадает (shape_distance < thr).

---

## Связь со старым контрактом

Предыдущий контракт `LOCKED_GLYPH_CONTRACT.md` описывал частотную детекцию (пик f0, стационарность по огибающей, merge по Δf + shape + glyph). Текущий контракт **заменяет** ключевую ось идентичности на **форму** и вводит разбор по **остаточной энтропии**. Глиф по-прежнему immutable при merge; замена глифа — только по явному Replace/Rebuild или по правилу обучения (EMA при high confidence).
