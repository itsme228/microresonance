# Архитектура ML: очереди, окна, State-Space + Event Transformer

## 1. Три очереди сообщений

### Q1. FrameSummary (heartbeat, 1s)
- **Когда:** раз в T_heartbeat (1 сек)
- **Содержит:** t0, t1, bandEnergy[8], totalEnergy, exciterState, quality, axisRatio
- **Цель:** непрерывность во времени — модель знает, что «тишина продолжается»

### Q2. EventGlyph (по событию)
- **Когда:** новый/обновлённый глиф в PatternStore (MergeOrAddByShape)
- **Содержит:** t, bandIdx, freqHz, amp, glyphID, protoSig (8–16), axisSig, isNewID
- **Цель:** семантические события формы
- **Пороги:** ampNorm >= 0.8, totalEnergyBand > E_min

### Q3. EventReaction (по AM-циклу)
- **Когда:** ReactionAlphabetStore эмитит паттерн (оборот фазы)
- **Содержит:** t_cycle_end, exciterID, bandIdx, reactionID, phaseVec16, totalEnergy, isNewID
- **Цель:** контекст «как тело отвечает на AM»
- **Пороги:** totalEnergy >= 0.005, ≥3 активных фазовых бинов

## 2. Окна

- **W_short = 4s** — локальная последовательность событий (EventGlyph, EventReaction)
- **W_mid = 30s** — контекст режима (FrameSummary)
- **Онлайн-инференс:** W_short + summary_state

## 3. Нейросеть: двухуровневая

### Уровень 1: Continuous State Model (CSSM)
- Вход: bandEnergy[7], exciterState, quality, axisRatioSummary
- Модель: GRU / SSM
- Выход: state_t (вектор режима)

### Уровень 2: Event Transformer
- Вход: события EventGlyph + EventReaction за W_short
- Open-vocab embedding: MLP(protoSig), MLP(phaseVec) — не только ID
- Self-attention на событиях + cross-attention на state_t

### Multi-head выходы
1. Next-event prediction (self-supervised)
2. Masked reconstruction
3. Contrastive alignment (glyph ↔ reaction)
4. (опционально) Regime segmentation

## 4. Open-vocab: алфавит собирается потоком

- В сеть идёт protoSig/phaseVec; id необязателен
- Когда ID стабилизируется (occurrences>N) — emb_id как ускоритель
- «Алфавит» для сети = непрерывное пространство подписей
