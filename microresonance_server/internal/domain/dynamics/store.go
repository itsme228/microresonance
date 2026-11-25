// Dynamics: поток глифов во времени, переходы, грамматика алфавита.
// Экран DYNAMICS — не ещё один «вид сигнала», а слой смысла: цепочки, переходы, паттерны.

package dynamics

import (
	"sync"
)

const (
	DynamicsRingCap     = 1024 // макс. событий в кольце (последние ~мин при 4 Hz)
	TMinHoldMsDefault   = 100  // глиф < T_min → игнор
	PMinEdgeDefault     = 0.15 // P(From→To) ≥ 15%, иначе связь не считаем (эксперт: min_transition_prob >= 0.15)
	CountMinEdgeDefault = 3    // N ≥ 3, иначе связь не рисуем (эксперт: min_transition_count >= 3)
	DtMinMsDefault      = 0
	DtMaxMsDefault      = 0
	SeqMinLenDefault    = 2  // последовательность: минимум 2 глифа (схлопывание алфавита → цепочки A→B)
	SeqMaxLenDefault    = 12 // последовательность: максимум 12 глифов
	SeqMinCountDefault  = 2  // показывать последовательность только если повторилась ≥ 2 раз
)

// GlyphEvent — одно наблюдение глифа в потоке (вход для DYNAMICS).
// BandIdx: полоса B0..B7; -1 = legacy/все полосы в одной ленте.
type GlyphEvent struct {
	PatternID  int     // ID из PatternStore (семейство)
	Ts         int64   // unix ms
	Confidence float64 // 0..1
	DurationMs int64   // опционально: длительность сегмента
	IntervalMs int64   // опционально: интервал с предыдущим
	BandIdx    int     // полоса (0..5); -1 = не задано (старая лента)
}

// TransitionKey — пара (from, to) для подсчёта переходов.
type TransitionKey struct {
	From, To int
}

// HoldEvent — устойчивое удержание глифа (duration >= T_min_hold). Не переход.
type HoldEvent struct {
	PatternID  int
	StartMs    int64
	EndMs      int64
	DurationMs int64
}

// TransitionEvent — переход между двумя разными глифами (HOLD A → HOLD B).
type TransitionEvent struct {
	From    int
	To      int
	DeltaMs int64
}

// DynamicsFilterParams — пороги семантического фильтра графа.
type DynamicsFilterParams struct {
	TMinHoldMs int64   // игнор глифов короче этого (ms)
	CountMin   int     // не рисовать рёбра с N < CountMin
	PMin       float64 // не рисовать рёбра с P(From→To) < PMin
	DtMinMs    int64   // Δt перехода >= это (0 = не фильтровать)
	DtMaxMs    int64   // Δt перехода <= это (0 = не фильтровать)
	BandIdx    int     // полоса для ленты/цепочек (0..5); -1 = все полосы вперемешку (legacy)
}

// DefaultDynamicsFilter возвращает рекомендуемые пороги. BandIdx 0 = одна полоса (правильная циклическая динамика).
func DefaultDynamicsFilter() DynamicsFilterParams {
	return DynamicsFilterParams{
		TMinHoldMs: TMinHoldMsDefault,
		CountMin:   CountMinEdgeDefault,
		PMin:       PMinEdgeDefault,
		DtMinMs:    DtMinMsDefault,
		DtMaxMs:    DtMaxMsDefault,
		BandIdx:    0,
	}
}

// TransitionCount — частота перехода A→B.
type TransitionCount struct {
	From int
	To   int
	N    int
}

// RepeatedChain — повторяющаяся цепочка [A→…→B] × count.
type RepeatedChain struct {
	Chain []int // например [1,1,2,3]
	Count int   // сколько раз подряд
}

// DynamicsStore — кольцевой буфер событий. Переходы и паттерны считаются по снимку.
type DynamicsStore struct {
	mu     sync.Mutex
	events []GlyphEvent
	head   int
	size   int
}

// NewDynamicsStore создаёт хранилище динамики.
func NewDynamicsStore() *DynamicsStore {
	return &DynamicsStore{
		events: make([]GlyphEvent, DynamicsRingCap),
	}
}

// Push добавляет событие в кольцо. IntervalMs заполняется по Ts предыдущего события.
func (s *DynamicsStore) Push(e GlyphEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if DynamicsRingCap == 0 {
		return
	}
	if s.size > 0 {
		prev := (s.head - 1 + DynamicsRingCap) % DynamicsRingCap
		e.IntervalMs = e.Ts - s.events[prev].Ts
	}
	s.events[s.head] = e
	s.head = (s.head + 1) % DynamicsRingCap
	if s.size < DynamicsRingCap {
		s.size++
	}
}

func (s *DynamicsStore) snapshotLocked() []GlyphEvent {
	events := make([]GlyphEvent, s.size)
	if s.size == 0 {
		return events
	}
	start := s.head - s.size
	if start < 0 {
		start += DynamicsRingCap
	}
	for i := 0; i < s.size; i++ {
		idx := (start + i) % DynamicsRingCap
		events[i] = s.events[idx]
	}
	return events
}

// Snapshot возвращает копию последних событий (от старых к новым) и карту переходов по окну (сырая, без фильтров).
func (s *DynamicsStore) Snapshot() (events []GlyphEvent, transitions map[TransitionKey]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events = s.snapshotLocked()
	transitions = make(map[TransitionKey]int)
	for i := 1; i < len(events); i++ {
		a, b := events[i-1].PatternID, events[i].PatternID
		if a != b {
			transitions[TransitionKey{From: a, To: b}]++
		}
	}
	return events, transitions
}

// collapseHolds группирует сырые события в серии по PatternID, отбрасывает hold < minHoldMs,
// возвращает HOLD и TRANSITION между ними. Self-edge (A→A) не создаётся — это удержание.
// Событийная модель: событие с DurationMs > 0 = один сегмент (T_start..T_end), не группируем с соседями.
func collapseHolds(events []GlyphEvent, minHoldMs int64) (holds []HoldEvent, transitions []TransitionEvent) {
	if len(events) == 0 {
		return nil, nil
	}
	var run []GlyphEvent
	flushRun := func() {
		if len(run) == 0 {
			return
		}
		id := run[0].PatternID
		var start, end int64
		if len(run) == 1 && run[0].DurationMs > 0 {
			// Событийная модель: один event = один сегмент
			start = run[0].Ts
			end = run[0].Ts + run[0].DurationMs
		} else {
			start = run[0].Ts
			end = run[len(run)-1].Ts
		}
		dur := end - start
		if dur < minHoldMs {
			run = nil
			return
		}
		holds = append(holds, HoldEvent{PatternID: id, StartMs: start, EndMs: end, DurationMs: dur})
		run = nil
	}
	for _, e := range events {
		// Событие с DurationMs = уже целый сегмент; не объединяем с тем же ID
		if len(run) > 0 && (run[len(run)-1].PatternID != e.PatternID || e.DurationMs > 0) {
			flushRun()
		}
		if e.DurationMs > 0 {
			// Один сегмент — сразу как отдельный run
			run = append(run, e)
			flushRun()
			continue
		}
		run = append(run, e)
	}
	flushRun()
	// переходы между последовательными holds
	for i := 1; i < len(holds); i++ {
		a, b := holds[i-1], holds[i]
		if a.PatternID == b.PatternID {
			continue
		}
		delta := b.StartMs - a.EndMs
		transitions = append(transitions, TransitionEvent{From: a.PatternID, To: b.PatternID, DeltaMs: delta})
	}
	return holds, transitions
}

// eventsForBand возвращает события одной полосы в порядке по времени.
// bandIdx < 0 = все события. bandIdx == 0 включает и legacy (BandIdx -1), чтобы старые/тестовые данные не пропадали.
func eventsForBand(events []GlyphEvent, bandIdx int) []GlyphEvent {
	if bandIdx < 0 {
		return events
	}
	out := make([]GlyphEvent, 0, len(events)/3)
	for i := range events {
		b := events[i].BandIdx
		if b == bandIdx || (bandIdx == 0 && b < 0) {
			out = append(out, events[i])
		}
	}
	return out
}

// SnapshotFiltered возвращает HOLD-события и переходы для выбранной полосы (без self-edge).
// bandIdx < 0 = все полосы вперемешку (legacy); 0..5 = одна полоса — сохраняется циклическая динамика.
func (s *DynamicsStore) SnapshotFiltered(minHoldMs int64, bandIdx int) (holds []HoldEvent, transitions []TransitionEvent) {
	s.mu.Lock()
	events := s.snapshotLocked()
	s.mu.Unlock()
	events = eventsForBand(events, bandIdx)
	return collapseHolds(events, minHoldMs)
}

// FilteredTransitionCounts возвращает переходы для графа с порогами CountMin, PMin и окном Δt.
// Рёбра с N < CountMin или P(From→To) < PMin не включаются. Self-edge не рисуем (уже нет в transitions).
func (s *DynamicsStore) FilteredTransitionCounts(fp DynamicsFilterParams) []TransitionCount {
	holds, trans := s.SnapshotFiltered(fp.TMinHoldMs, fp.BandIdx)
	if len(holds) < 2 || len(trans) == 0 {
		return nil
	}
	// считаем N(From→To), только переходы с Δt в [DtMin, DtMax] если задано
	count := make(map[TransitionKey]int)
	for _, t := range trans {
		if fp.DtMinMs > 0 && t.DeltaMs < fp.DtMinMs {
			continue
		}
		if fp.DtMaxMs > 0 && t.DeltaMs > fp.DtMaxMs {
			continue
		}
		count[TransitionKey{From: t.From, To: t.To}]++
	}
	// исходящая сумма по From для P(From→To)
	outSum := make(map[int]int)
	for k, n := range count {
		outSum[k.From] += n
	}
	out := make([]TransitionCount, 0, len(count))
	for k, n := range count {
		if n < fp.CountMin {
			continue
		}
		tot := outSum[k.From]
		if tot == 0 {
			continue
		}
		p := float64(n) / float64(tot)
		if p < fp.PMin {
			continue
		}
		out = append(out, TransitionCount{From: k.From, To: k.To, N: n})
	}
	// сортировка по N
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].N > out[i].N {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// TransitionCounts возвращает переходы как слайс, отсортированный по убыванию N.
func (s *DynamicsStore) TransitionCounts() []TransitionCount {
	_, trans := s.Snapshot()
	out := make([]TransitionCount, 0, len(trans))
	for k, n := range trans {
		out = append(out, TransitionCount{From: k.From, To: k.To, N: n})
	}
	// сортировка по N
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].N > out[i].N {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// HoldPatternSequence возвращает последовательность ID глифов по ленте HOLD (один ID на каждый hold).
// Для цепочек и отображения «последовательность по глифу» используйте HoldGlyphSequence.
func (s *DynamicsStore) HoldPatternSequence(minHoldMs int64, bandIdx int) []int {
	holds, _ := s.SnapshotFiltered(minHoldMs, bandIdx)
	ids := make([]int, len(holds))
	for i := range holds {
		ids[i] = holds[i].PatternID
	}
	return ids
}

// HoldGlyphSequence возвращает последовательность по глифу: подряд идущие одинаковые глифы свёрнуты в один элемент.
// Глиф = элемент последовательности; его вариации (подряд N holds) = кадры (frames[i]).
// Используется для построения цепочек и ленты на экране DYNAMICS.
func (s *DynamicsStore) HoldGlyphSequence(minHoldMs int64, bandIdx int) (glyphSeq []int, frames []int) {
	holds, _ := s.SnapshotFiltered(minHoldMs, bandIdx)
	if len(holds) == 0 {
		return nil, nil
	}
	var seq []int
	var f []int
	cur := holds[0].PatternID
	n := 1
	for i := 1; i < len(holds); i++ {
		if holds[i].PatternID == cur {
			n++
			continue
		}
		seq = append(seq, cur)
		f = append(f, n)
		cur = holds[i].PatternID
		n = 1
	}
	seq = append(seq, cur)
	f = append(f, n)
	return seq, f
}

// findRepeatedChainsInSequence ищет повторяющиеся цепочки длиной 2–maxLen, встречающиеся подряд ≥ 2 раз.
func findRepeatedChainsInSequence(ids []int, maxLen int) []RepeatedChain {
	if len(ids) < 4 || maxLen < 2 {
		return nil
	}
	if maxLen > len(ids)/2 {
		maxLen = len(ids) / 2
	}
	var result []RepeatedChain
	seen := make(map[string]struct{})
	for ln := 2; ln <= maxLen; ln++ {
		for i := 0; i+ln*2 <= len(ids); i++ {
			chain := ids[i : i+ln]
			count := 1
			j := i + ln
			for j+ln <= len(ids) {
				match := true
				for k := 0; k < ln; k++ {
					if ids[j+k] != chain[k] {
						match = false
						break
					}
				}
				if !match {
					break
				}
				count++
				j += ln
			}
			if count < 2 {
				continue
			}
			key := chainKey(chain)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			dup := make([]int, ln)
			copy(dup, chain)
			result = append(result, RepeatedChain{Chain: dup, Count: count})
		}
	}
	// по убыванию count, затем по длине цепочки
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			a, b := &result[i], &result[j]
			if b.Count > a.Count || (b.Count == a.Count && len(b.Chain) > len(a.Chain)) {
				*a, *b = *b, *a
			}
		}
	}
	return result
}

// FindRepeatedChainsFromHolds возвращает список повторяющихся последовательностей по ленте глифов (свёрнутой по глифу).
func (s *DynamicsStore) FindRepeatedChainsFromHolds(minHoldMs int64, bandIdx int) []RepeatedChain {
	glyphSeq, _ := s.HoldGlyphSequence(minHoldMs, bandIdx)
	return findRepeatedChainsInSequence(glyphSeq, SeqMaxLenDefault)
}

// FindFrequentNgrams возвращает n-граммы по последовательности глифов (свёрнутой), встречающиеся >= minCount раз.
func (s *DynamicsStore) FindFrequentNgrams(minHoldMs int64, bandIdx int, minLen, maxLen, minCount int) []RepeatedChain {
	glyphSeq, _ := s.HoldGlyphSequence(minHoldMs, bandIdx)
	if len(glyphSeq) < minLen {
		return nil
	}
	count := make(map[string]int)
	chainForKey := make(map[string][]int)
	for ln := minLen; ln <= maxLen && ln <= len(glyphSeq); ln++ {
		for i := 0; i+ln <= len(glyphSeq); i++ {
			key := chainKey(glyphSeq[i : i+ln])
			count[key]++
			if _, ok := chainForKey[key]; !ok {
				dup := make([]int, ln)
				copy(dup, glyphSeq[i:i+ln])
				chainForKey[key] = dup
			}
		}
	}
	var out []RepeatedChain
	for key, n := range count {
		if n < minCount {
			continue
		}
		chain := chainForKey[key]
		if ChainTrivial(chain) {
			continue
		}
		out = append(out, RepeatedChain{Chain: chain, Count: n})
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Count > out[i].Count || (out[j].Count == out[i].Count && len(out[j].Chain) > len(out[i].Chain)) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// FindRepeatedChains ищет повторяющиеся цепочки длиной 2–6 по сырым событиям (legacy).
func (s *DynamicsStore) FindRepeatedChains() []RepeatedChain {
	events, _ := s.Snapshot()
	if len(events) < 4 {
		return nil
	}
	ids := make([]int, len(events))
	for i := range events {
		ids[i] = events[i].PatternID
	}
	return findRepeatedChainsInSequence(ids, 6)
}

func chainKey(c []int) string {
	b := make([]byte, 0, len(c)*4)
	for _, id := range c {
		b = append(b, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	}
	return string(b)
}

// ChainTrivial возвращает true, если цепочка из одного глифа [A→A→…] (нет перехода между разными).
func ChainTrivial(chain []int) bool {
	if len(chain) == 0 {
		return true
	}
	id := chain[0]
	for _, x := range chain[1:] {
		if x != id {
			return false
		}
	}
	return true
}

// Clear сбрасывает буфер (для тестов или «новый сеанс»).
func (s *DynamicsStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.size = 0
	s.head = 0
}
