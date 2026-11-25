// reaction_alphabet.go — второй алфавит (reaction alphabet) внутри primary cell (exciter, band).
// Паттерны строятся из реакции по фазам AM, а не из FFT-форм.

package alphabet

import (
	"math"
	"sync"
)

// ReactionPhaseBins — число фазовых бинов AM-цикла для реакции.
const ReactionPhaseBins = 16

// ReactionCellKey — ключ ячейки (exciter, band) для накопления реакции.
type ReactionCellKey struct {
	ExciterID int
	BandID    int
}

// ReactionAccumulator — накопление E по фазам AM в одной ячейке.
type ReactionAccumulator struct {
	E_by_phase [ReactionPhaseBins]float64
	count      [ReactionPhaseBins]int
	lastPhase  float64
	cycleCount int // сколько полных циклов накоплено
}

// ReactionPattern — паттерн реакции внутри ячейки (exciter, band).
type ReactionPattern struct {
	ID          int
	ExciterID   int
	BandID      int
	Vector      [ReactionPhaseBins]float64 // нормализованный вектор реакции (L2)
	TotalEnergy float64                    // сумма сырых E по фазам до нормализации — сила реакции
	Occurrences int
	LastSeenTs  int64
	Captured    int64
}

// ReactionAlphabetStore — хранилище reaction-паттернов внутри primary cells.
// Ключ: (exciter, band) → список ReactionPattern. Merge по cosine similarity внутри ячейки.
type ReactionAlphabetStore struct {
	mu           sync.RWMutex
	cells        map[ReactionCellKey][]*ReactionPattern
	accumulators map[ReactionCellKey]*ReactionAccumulator
	nextID       int
}

// NewReactionAlphabetStore создаёт пустое хранилище.
func NewReactionAlphabetStore() *ReactionAlphabetStore {
	return &ReactionAlphabetStore{
		cells:        make(map[ReactionCellKey][]*ReactionPattern),
		accumulators: make(map[ReactionCellKey]*ReactionAccumulator),
		nextID:       1,
	}
}

// Accumulate добавляет E в фазу phase (0..1) для ячейки (exciter, band).
// Возвращает (reaction_vector, totalEnergy, emit). totalEnergy — сумма сырых E до нормализации.
func (s *ReactionAlphabetStore) Accumulate(exciterID, bandID int, E float64, phase float64, nowMs int64) ([ReactionPhaseBins]float64, float64, bool) {
	if bandID < 0 || bandID >= 8 {
		return [ReactionPhaseBins]float64{}, 0, false
	}
	key := ReactionCellKey{ExciterID: exciterID, BandID: bandID}
	s.mu.Lock()
	defer s.mu.Unlock()
	acc, ok := s.accumulators[key]
	if !ok {
		acc = &ReactionAccumulator{}
		s.accumulators[key] = acc
	}
	// Фаза 0..1 → бин 0..N-1
	bin := int(phase * float64(ReactionPhaseBins))
	if bin >= ReactionPhaseBins {
		bin = ReactionPhaseBins - 1
	}
	if bin < 0 {
		bin = 0
	}
	acc.E_by_phase[bin] += E
	acc.count[bin]++
	// Детект оборота фазы (новый цикл): lastPhase было в конце, phase перескочила в начало
	emit := false
	var result [ReactionPhaseBins]float64
	var totalEnergy float64
	if acc.lastPhase > 0.85 && phase < 0.15 {
		acc.cycleCount++
		if acc.cycleCount >= 1 {
			emit = true
			acc.cycleCount = 0
			// Копируем вектор ДО сброса, считаем totalEnergy
			for i := range result {
				result[i] = acc.E_by_phase[i]
				totalEnergy += result[i]
			}
			normalizeReactionVector(&result)
			// Сбросить для следующего цикла
			for i := range acc.E_by_phase {
				acc.E_by_phase[i] = 0
				acc.count[i] = 0
			}
		}
	}
	acc.lastPhase = phase
	return result, totalEnergy, emit
}

func normalizeReactionVector(v *[ReactionPhaseBins]float64) {
	var sum float64
	for i := range v {
		sum += v[i] * v[i]
	}
	sum = math.Sqrt(sum)
	if sum < 1e-12 {
		sum = 1
	}
	for i := range v {
		v[i] /= sum
	}
}

// ReactionMergeResult — результат MergeOrAddByReaction.
type ReactionMergeResult struct {
	ReactionID int
	Merged     bool
	DistMin    float64
}

const reactionMergeDistThr = 0.25 // cosine distance < 0.25 → merge
const reactionEnergyGate = 0.005  // не создавать новый паттерн, если totalEnergy ниже (шум)

// MergeOrAddByReaction добавляет или мержит reaction-паттерн внутри ячейки (exciter, band).
// totalEnergy — сумма сырых E до нормализации; при < reactionEnergyGate новый паттерн не создаётся.
func (s *ReactionAlphabetStore) MergeOrAddByReaction(exciterID, bandID int, vec [ReactionPhaseBins]float64, totalEnergy float64, nowMs int64) ReactionMergeResult {
	key := ReactionCellKey{ExciterID: exciterID, BandID: bandID}
	s.mu.Lock()
	defer s.mu.Unlock()
	patterns := s.cells[key]
	bestIdx := -1
	bestDist := 1e9
	for i, p := range patterns {
		d := cosineDistanceVecCircular(p.Vector, vec)
		if d < bestDist {
			bestDist = d
			bestIdx = i
		}
	}
	if bestIdx >= 0 && bestDist < reactionMergeDistThr {
		patterns[bestIdx].Occurrences++
		patterns[bestIdx].LastSeenTs = nowMs
		return ReactionMergeResult{ReactionID: patterns[bestIdx].ID, Merged: true, DistMin: bestDist}
	}
	// Energy gate: не создавать новый паттерн на шуме
	if totalEnergy < reactionEnergyGate {
		return ReactionMergeResult{ReactionID: -1, Merged: false, DistMin: bestDist}
	}
	id := s.nextID
	s.nextID++
	np := &ReactionPattern{
		ID:          id,
		ExciterID:   exciterID,
		BandID:      bandID,
		Vector:      vec,
		TotalEnergy: totalEnergy,
		Occurrences: 1,
		LastSeenTs:  nowMs,
		Captured:    nowMs,
	}
	s.cells[key] = append(s.cells[key], np)
	return ReactionMergeResult{ReactionID: id, Merged: false, DistMin: bestDist}
}

// cosineDistanceVecCircular — минимальная cosine distance по всем циклическим сдвигам (0..15).
// Учитывает дрейф фазы AM: один и тот же паттерн со сдвигом не плодит новые ID.
func cosineDistanceVecCircular(a, b [ReactionPhaseBins]float64) float64 {
	best := 1.0
	for shift := 0; shift < ReactionPhaseBins; shift++ {
		var dot, na, nb float64
		for i := range a {
			j := (i + shift) % ReactionPhaseBins
			dot += a[i] * b[j]
			na += a[i] * a[i]
			nb += b[j] * b[j]
		}
		na = math.Sqrt(na)
		nb = math.Sqrt(nb)
		if na < 1e-12 || nb < 1e-12 {
			continue
		}
		cos := dot / (na * nb)
		if cos > 1 {
			cos = 1
		}
		if cos < -1 {
			cos = -1
		}
		d := 1 - cos
		if d < best {
			best = d
		}
	}
	return best
}

// ListByCell возвращает реакционные паттерны по ячейке (exciter, band).
func (s *ReactionAlphabetStore) ListByCell(exciterID, bandID int) []ReactionPattern {
	key := ReactionCellKey{ExciterID: exciterID, BandID: bandID}
	s.mu.RLock()
	defer s.mu.RUnlock()
	patterns := s.cells[key]
	out := make([]ReactionPattern, len(patterns))
	for i, p := range patterns {
		out[i] = *p
	}
	return out
}

// ListAll возвращает все реакционные паттерны по всем ячейкам.
func (s *ReactionAlphabetStore) ListAll() []ReactionPattern {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []ReactionPattern
	for _, patterns := range s.cells {
		for _, p := range patterns {
			out = append(out, *p)
		}
	}
	return out
}

// Count возвращает общее число reaction-паттернов.
func (s *ReactionAlphabetStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, patterns := range s.cells {
		n += len(patterns)
	}
	return n
}
