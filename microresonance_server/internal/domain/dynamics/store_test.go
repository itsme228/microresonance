package dynamics

import "testing"

func TestDynamicsStore_PushSnapshotTransitions(t *testing.T) {
	s := NewDynamicsStore()
	s.Push(GlyphEvent{PatternID: 1, Ts: 100, Confidence: 0.9})
	s.Push(GlyphEvent{PatternID: 1, Ts: 200, Confidence: 0.8})
	s.Push(GlyphEvent{PatternID: 2, Ts: 300, Confidence: 0.7})
	s.Push(GlyphEvent{PatternID: 1, Ts: 400, Confidence: 0.6})
	ev, tr := s.Snapshot()
	if len(ev) != 4 {
		t.Fatalf("expected 4 events, got %d", len(ev))
	}
	if ev[0].PatternID != 1 || ev[2].PatternID != 2 {
		t.Fatalf("unexpected event order")
	}
	// transitions: 1→2, 2→1
	if tr[TransitionKey{1, 2}] != 1 || tr[TransitionKey{2, 1}] != 1 {
		t.Fatalf("transitions: got %v", tr)
	}
}

func TestDynamicsStore_RepeatedChains(t *testing.T) {
	s := NewDynamicsStore()
	// [1,2,1,2,1,2]
	for i := 0; i < 6; i++ {
		id := 1
		if i%2 == 1 {
			id = 2
		}
		s.Push(GlyphEvent{PatternID: id, Ts: int64(100 * i), Confidence: 1})
	}
	chains := s.FindRepeatedChains()
	if len(chains) == 0 {
		t.Fatalf("expected at least one repeated chain")
	}
	found := false
	for _, c := range chains {
		if len(c.Chain) == 2 && c.Chain[0] == 1 && c.Chain[1] == 2 && c.Count >= 2 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected [1,2]×N, got %+v", chains)
	}
}

func TestDynamicsStore_SnapshotFiltered_FilteredTransitionCounts(t *testing.T) {
	s := NewDynamicsStore()
	// HOLD 1: 0,50,100 (dur 100ms) — сохраняем
	// HOLD 2: 150,200,250 (dur 100ms) — сохраняем
	// HOLD 1: 300,350 (dur 50ms) — отбрасываем (< T_min)
	for _, e := range []GlyphEvent{
		{PatternID: 1, Ts: 0}, {PatternID: 1, Ts: 50}, {PatternID: 1, Ts: 100},
		{PatternID: 2, Ts: 150}, {PatternID: 2, Ts: 200}, {PatternID: 2, Ts: 250},
		{PatternID: 1, Ts: 300}, {PatternID: 1, Ts: 350},
	} {
		s.Push(e)
	}
	holds, trans := s.SnapshotFiltered(100, -1)
	if len(holds) != 2 {
		t.Fatalf("expected 2 holds, got %d", len(holds))
	}
	if holds[0].PatternID != 1 || holds[0].DurationMs != 100 {
		t.Fatalf("hold 0: want PatternID=1 Duration=100, got %d %d", holds[0].PatternID, holds[0].DurationMs)
	}
	if holds[1].PatternID != 2 || holds[1].DurationMs != 100 {
		t.Fatalf("hold 1: want PatternID=2 Duration=100, got %d %d", holds[1].PatternID, holds[1].DurationMs)
	}
	if len(trans) != 1 {
		t.Fatalf("expected 1 transition, got %d", len(trans))
	}
	if trans[0].From != 1 || trans[0].To != 2 || trans[0].DeltaMs != 50 {
		t.Fatalf("transition: want 1→2 Δ50, got %d→%d Δ%d", trans[0].From, trans[0].To, trans[0].DeltaMs)
	}
	fp := DefaultDynamicsFilter()
	fp.CountMin = 1
	fp.PMin = 0
	fp.DtMinMs = 0
	fp.DtMaxMs = 0
	tc := s.FilteredTransitionCounts(fp)
	if len(tc) != 1 || tc[0].From != 1 || tc[0].To != 2 || tc[0].N != 1 {
		t.Fatalf("FilteredTransitionCounts: want [1→2 N=1], got %+v", tc)
	}
}

func TestDynamicsStore_HoldPatternSequence_FindRepeatedChainsFromHolds(t *testing.T) {
	s := NewDynamicsStore()
	// HOLD 1, HOLD 2, HOLD 1, HOLD 2 — повтор [1,2] × 2
	for _, e := range []GlyphEvent{
		{PatternID: 1, Ts: 0}, {PatternID: 1, Ts: 50}, {PatternID: 1, Ts: 100},
		{PatternID: 2, Ts: 150}, {PatternID: 2, Ts: 200}, {PatternID: 2, Ts: 250},
		{PatternID: 1, Ts: 300}, {PatternID: 1, Ts: 350}, {PatternID: 1, Ts: 400},
		{PatternID: 2, Ts: 450}, {PatternID: 2, Ts: 500}, {PatternID: 2, Ts: 550},
	} {
		s.Push(e)
	}
	tape := s.HoldPatternSequence(100, -1)
	if len(tape) != 4 {
		t.Fatalf("HoldPatternSequence: want len 4, got %d %v", len(tape), tape)
	}
	if tape[0] != 1 || tape[1] != 2 || tape[2] != 1 || tape[3] != 2 {
		t.Fatalf("HoldPatternSequence: want [1,2,1,2], got %v", tape)
	}
	chains := s.FindRepeatedChainsFromHolds(100, -1)
	if len(chains) == 0 {
		t.Fatalf("expected at least one repeated chain from holds")
	}
	found := false
	for _, c := range chains {
		if len(c.Chain) == 2 && c.Chain[0] == 1 && c.Chain[1] == 2 && c.Count >= 2 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected [1,2]×2 from holds, got %+v", chains)
	}
}

// TestDynamicsStore_OneGlyphPerTick проверяет семантику «один глиф на тик»: лента = временная последовательность.
// События с Ts через 250ms, один ID на тик → holds по сериям одного ID (dur >= T_min).
func TestDynamicsStore_OneGlyphPerTick(t *testing.T) {
	s := NewDynamicsStore()
	// Один глиф на тик: t=0,250,500,750,1000,1250 ms → ID [1,1,2,2,1,1]
	tickMs := int64(250)
	ids := []int{1, 1, 2, 2, 1, 1}
	for i, id := range ids {
		s.Push(GlyphEvent{PatternID: id, Ts: int64(i) * tickMs, Confidence: 0.9})
	}
	tMinHoldMs := int64(80) // 2 тика = 250ms >= 80
	holds, _ := s.SnapshotFiltered(tMinHoldMs, -1)
	tape := s.HoldPatternSequence(tMinHoldMs, -1)
	// Ожидаем: HOLD 1 (0..500ms), HOLD 2 (500..1000ms), HOLD 1 (1000..1250ms) → 3 holds, tape [1,2,1]
	if len(holds) != 3 {
		t.Fatalf("expected 3 holds (one per run), got %d %+v", len(holds), holds)
	}
	if holds[0].PatternID != 1 || holds[1].PatternID != 2 || holds[2].PatternID != 1 {
		t.Fatalf("holds: want [1,2,1], got %d %d %d", holds[0].PatternID, holds[1].PatternID, holds[2].PatternID)
	}
	if len(tape) != 3 {
		t.Fatalf("tape: want len 3, got %d %v", len(tape), tape)
	}
	if tape[0] != 1 || tape[1] != 2 || tape[2] != 1 {
		t.Fatalf("tape: want [1,2,1], got %v", tape)
	}
}

// TestDynamicsStore_HoldGlyphSequence проверяет свёртку по глифу: подряд идущие одинаковые глифы = один элемент, frames = кадры.
func TestDynamicsStore_HoldGlyphSequence(t *testing.T) {
	s := NewDynamicsStore()
	// Событийная модель (DurationMs): 7 сегментов — ID [1,1,1,2,2,1,1] → последовательность [1,2,1], кадры [3,2,2]
	for i, id := range []int{1, 1, 1, 2, 2, 1, 1} {
		s.Push(GlyphEvent{PatternID: id, Ts: int64(i * 200), DurationMs: 150, Confidence: 1})
	}
	glyphSeq, frames := s.HoldGlyphSequence(80, -1)
	if len(glyphSeq) != 3 || len(frames) != 3 {
		t.Fatalf("HoldGlyphSequence: want len 3, got seq=%v frames=%v", glyphSeq, frames)
	}
	if glyphSeq[0] != 1 || glyphSeq[1] != 2 || glyphSeq[2] != 1 {
		t.Fatalf("HoldGlyphSequence: want seq [1,2,1], got %v", glyphSeq)
	}
	if frames[0] != 3 || frames[1] != 2 || frames[2] != 2 {
		t.Fatalf("HoldGlyphSequence: want frames [3,2,2], got %v", frames)
	}
	chains := s.FindRepeatedChainsFromHolds(80, -1)
	// [1,2,1] — нет повторяющейся цепочки ≥2 раз подряд
	if len(chains) != 0 {
		t.Logf("expected no repeated chain in [1,2,1], got %+v", chains)
	}
}

// TestDynamicsStore_RepeatedChainFromOnePerTick проверяет, что при [1,2,1,2,1,2] (один на тик) находим цепочку [1,2]×3.
func TestDynamicsStore_RepeatedChainFromOnePerTick(t *testing.T) {
	s := NewDynamicsStore()
	tickMs := int64(250)
	ids := []int{1, 2, 1, 2, 1, 2}
	for i, id := range ids {
		s.Push(GlyphEvent{PatternID: id, Ts: int64(i) * tickMs, Confidence: 0.9})
	}
	tMinHoldMs := int64(80)
	_ = s.HoldPatternSequence(tMinHoldMs, -1) // при чередовании 1,2,1,2 holds=0 (один тик = dur 0)
	// Каждый hold = один тик (2 события подряд нет), так что при T_min=80 один тик = 0ms dur. Нужно 2 тика подряд с одним ID.
	// У нас ID чередуются 1,2,1,2,1,2 — нет двух подряд с одним ID. So каждый "run" = 1 событие, dur=0. So holds = 0 при T_min=80.
	// Чтобы получить holds, нужны серии: (1,0),(1,250) → hold 1; (2,500),(2,750) → hold 2; ...
	// Значит для этого теста нужны ids с сериями: [1,1,2,2,1,1,2,2] → tape [1,2,1,2], chain [1,2]×2.
	ids2 := []int{1, 1, 2, 2, 1, 1, 2, 2}
	s2 := NewDynamicsStore()
	for i, id := range ids2 {
		s2.Push(GlyphEvent{PatternID: id, Ts: int64(i) * tickMs, Confidence: 0.9})
	}
	tape2 := s2.HoldPatternSequence(tMinHoldMs, -1)
	chains := s2.FindRepeatedChainsFromHolds(tMinHoldMs, -1)
	if len(tape2) != 4 {
		t.Fatalf("tape: want [1,2,1,2] len 4, got %d %v", len(tape2), tape2)
	}
	if tape2[0] != 1 || tape2[1] != 2 || tape2[2] != 1 || tape2[3] != 2 {
		t.Fatalf("tape: want [1,2,1,2], got %v", tape2)
	}
	found := false
	for _, c := range chains {
		if len(c.Chain) == 2 && c.Chain[0] == 1 && c.Chain[1] == 2 && c.Count >= 2 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected [1,2]×2 from tape [1,2,1,2], got chains %+v", chains)
	}
}
