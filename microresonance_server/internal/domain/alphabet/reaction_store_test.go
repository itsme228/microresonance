package alphabet

import (
	"math"
	"testing"
)

func TestReactionAlphabetStoreAccumulate(t *testing.T) {
	s := NewReactionAlphabetStore()
	// Эмулируем wrap фазы: lastPhase > 0.85, phase < 0.15
	_, _, _ = s.Accumulate(1, 2, 0.1, 0.9, 1000)
	vec, _, emit := s.Accumulate(1, 2, 0.1, 0.1, 1200)
	if emit {
		var sum float64
		for _, v := range vec {
			sum += v * v
		}
		if math.Abs(sum-1) > 0.01 {
			t.Errorf("normalized vector sum sq = %f, want ~1", sum)
		}
	}
}

func TestReactionAlphabetStoreMergeOrAddByReaction(t *testing.T) {
	s := NewReactionAlphabetStore()
	var v [ReactionPhaseBins]float64
	for i := range v {
		v[i] = math.Sin(float64(i) * math.Pi / float64(ReactionPhaseBins))
	}
	normalizeReactionVector(&v)
	mr := s.MergeOrAddByReaction(1, 2, v, 1.0, 1000)
	if mr.ReactionID < 1 {
		t.Errorf("ReactionID = %d, want >= 1", mr.ReactionID)
	}
	if mr.Merged {
		t.Error("first add should not merge")
	}
	mr2 := s.MergeOrAddByReaction(1, 2, v, 1.0, 2000)
	if !mr2.Merged {
		t.Error("same vector should merge")
	}
	if mr2.ReactionID != mr.ReactionID {
		t.Errorf("merged ID = %d, want %d", mr2.ReactionID, mr.ReactionID)
	}
	if s.Count() != 1 {
		t.Errorf("Count() = %d, want 1", s.Count())
	}
}

func TestReactionAlphabetCircularShift(t *testing.T) {
	s := NewReactionAlphabetStore()
	var v [ReactionPhaseBins]float64
	for i := range v {
		v[i] = math.Sin(float64(i) * math.Pi / float64(ReactionPhaseBins))
	}
	normalizeReactionVector(&v)
	mr := s.MergeOrAddByReaction(1, 0, v, 1.0, 1000)
	if mr.Merged {
		t.Fatal("first add should not merge")
	}
	// Сдвиг на 4 бина — тот же «рисунок», другой сдвиг по фазе
	var shifted [ReactionPhaseBins]float64
	for i := range shifted {
		shifted[i] = v[(i+4)%ReactionPhaseBins]
	}
	mr2 := s.MergeOrAddByReaction(1, 0, shifted, 1.0, 2000)
	if !mr2.Merged {
		t.Error("shifted vector should merge (circular distance)")
	}
	if mr2.ReactionID != mr.ReactionID {
		t.Errorf("merged ID = %d, want %d", mr2.ReactionID, mr.ReactionID)
	}
}

func TestReactionAlphabetEnergyGate(t *testing.T) {
	s := NewReactionAlphabetStore()
	var v [ReactionPhaseBins]float64
	for i := range v {
		v[i] = 0.001 // очень слабый сигнал
	}
	normalizeReactionVector(&v)
	mr := s.MergeOrAddByReaction(1, 0, v, 0.001, 1000) // totalEnergy < reactionEnergyGate
	if mr.ReactionID >= 0 {
		t.Errorf("energy gate should reject: ReactionID = %d", mr.ReactionID)
	}
	if s.Count() != 0 {
		t.Errorf("Count() = %d, want 0 (rejected by gate)", s.Count())
	}
}
