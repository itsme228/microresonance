// primary_alphabet.go — первый алфавит: (exciter_id, band_id).
// Статический, не учится, не растёт, не вытесняется. Ячейки адреса.
// Bands НЕ меняем — используем BandsFilterbank как есть.

package alphabet

import (
	"math"
	"sync"
)

// PrimaryGlyphKey — ключ первичного глифа: (exciter_id, band_id).
// Первичный глиф = якорь, не смысл. G = (A, B4).
type PrimaryGlyphKey struct {
	ExciterID int
	BandID    int
}

// PrimaryCellState — состояние ячейки (exciter, band).
// Накопленные метрики реакции. Эти значения НЕ создают новые глифы.
type PrimaryCellState struct {
	// E(t) — относительная энергия E_rel (доля от суммы полос), для гистерезиса и порогов
	E float64
	// RawE — сырая энергия полосы (RMS band), для отображения на Discovery (чтобы B6/B7 были видны при малой доле)
	RawE float64
	// E_rise_slope — наклон фронта нарастания
	E_rise_slope float64
	// E_decay_tau — постоянная затухания
	E_decay_tau float64
	// lag_to_response — задержка до отклика (мс)
	LagToResponse float64
	// harmonic_activity — активность гармоник (0..1)
	HarmonicActivity float64
	// afterglow_energy — энергия «хвоста» после возбуждения
	AfterglowEnergy float64

	// Служебные: для вычисления rise/decay
	lastE  float64
	lastTs int64
	prevE  float64
	prevTs int64
}

// PrimaryAlphabetStore — хранилище первого алфавита.
// Карта (exciter, band) → состояние. Максимум exciter_count × band_count ячеек.
type PrimaryAlphabetStore struct {
	mu    sync.RWMutex
	cells map[PrimaryGlyphKey]*PrimaryCellState
}

// Константы для фильтра энергии: относительный порог, гистерезис, затухание.
const (
	PrimaryEpsilon   = 1e-12
	PrimaryTOn       = 0.03  // включить полосу при E_rel > 3%
	PrimaryTOff      = 0.015 // выключить только при E_rel < 1.5%
	PrimaryDecay     = 0.9   // затухание на шаг (250 ms) — хвост вместо обнуления
	PrimaryMaxActive = 8     // макс активных полос (все 8 — лимит не режет при норме)
)

// NewPrimaryAlphabetStore создаёт пустое хранилище.
func NewPrimaryAlphabetStore() *PrimaryAlphabetStore {
	return &PrimaryAlphabetStore{cells: make(map[PrimaryGlyphKey]*PrimaryCellState)}
}

// Update обновляет одну ячейку (legacy, для тестов).
func (s *PrimaryAlphabetStore) Update(exciterID, bandID int, E float64, nowMs int64) {
	if bandID < 0 || bandID >= 8 {
		return
	}
	key := PrimaryGlyphKey{ExciterID: exciterID, BandID: bandID}
	s.mu.Lock()
	defer s.mu.Unlock()
	cell, ok := s.cells[key]
	if !ok {
		cell = &PrimaryCellState{}
		s.cells[key] = cell
	}
	cell.update(E, nowMs)
}

// UpdateAll обновляет все полосы для exciter: E_rel, гистерезис, затухание, лимит активных.
func (s *PrimaryAlphabetStore) UpdateAll(exciterID int, bandEnergy []float64, nowMs int64) {
	if exciterID < 0 {
		exciterID = 0
	}
	n := len(bandEnergy)
	if n > 8 {
		n = 8
	}
	if n == 0 {
		return
	}
	// E_rel[b] = E[b] / (sum(E)+eps)
	var sumE float64
	for i := 0; i < n; i++ {
		sumE += bandEnergy[i]
	}
	sumE += PrimaryEpsilon
	E_rel := make([]float64, n)
	for i := 0; i < n; i++ {
		E_rel[i] = bandEnergy[i] / sumE
	}
	// Кто выше T_on — кандидаты на обновление
	var aboveOn []int
	for b := 0; b < n; b++ {
		if E_rel[b] > PrimaryTOn {
			aboveOn = append(aboveOn, b)
		}
	}
	// Лимит: не больше N_max активных; если больше — оставляем топ по E_rel
	if len(aboveOn) > PrimaryMaxActive {
		// сортируем по E_rel убыванию
		for i := 0; i < len(aboveOn); i++ {
			for j := i + 1; j < len(aboveOn); j++ {
				if E_rel[aboveOn[j]] > E_rel[aboveOn[i]] {
					aboveOn[i], aboveOn[j] = aboveOn[j], aboveOn[i]
				}
			}
		}
		aboveOn = aboveOn[:PrimaryMaxActive]
	}
	aboveSet := make(map[int]bool)
	for _, b := range aboveOn {
		aboveSet[b] = true
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for b := 0; b < n; b++ {
		key := PrimaryGlyphKey{ExciterID: exciterID, BandID: b}
		cell, ok := s.cells[key]
		if !ok {
			cell = &PrimaryCellState{}
			s.cells[key] = cell
		}
		cell.updateWithHysteresis(E_rel[b], bandEnergy[b], aboveSet[b], nowMs)
	}
}

func (c *PrimaryCellState) update(E float64, nowMs int64) {
	dt := float64(nowMs - c.lastTs)
	if dt <= 0 {
		dt = 1
	}
	alpha := 0.3
	c.E = alpha*c.E + (1-alpha)*E
	if c.prevTs > 0 && dt > 0 {
		deltaE := c.E - c.prevE
		c.E_rise_slope = deltaE / (dt / 1000.0)
		if c.E_rise_slope < 0 {
			c.E_decay_tau = -dt / 1000.0 / (math.Log(0.5) + 1e-9)
		}
	}
	c.prevE, c.prevTs = c.lastE, c.lastTs
	c.lastE, c.lastTs = c.E, nowMs
}

// updateWithHysteresis: E_rel, сырая энергия rawE, гистерезис (T_on/T_off), затухание вместо обнуления.
func (c *PrimaryCellState) updateWithHysteresis(E_rel, rawE float64, aboveThreshold bool, nowMs int64) {
	dt := float64(nowMs - c.lastTs)
	if dt <= 0 {
		dt = 1
	}
	prevE := c.E

	c.RawE = rawE
	if aboveThreshold {
		// E_rel > T_on: включаем/обновляем
		c.E = E_rel
	} else if E_rel >= PrimaryTOff && E_rel <= PrimaryTOn {
		// Зона гистерезиса [T_off, T_on]: не меняем — не мигаем
		c.E = prevE
	} else {
		// E_rel < T_off: затухание вместо обнуления
		c.E = prevE * PrimaryDecay
		if c.E < PrimaryEpsilon {
			c.E = 0
		}
	}

	if c.prevTs > 0 && dt > 0 {
		deltaE := c.E - c.prevE
		c.E_rise_slope = deltaE / (dt / 1000.0)
		if c.E_rise_slope < 0 {
			c.E_decay_tau = -dt / 1000.0 / (math.Log(0.5) + 1e-9)
		}
	}
	c.prevE, c.prevTs = prevE, c.lastTs
	c.lastE, c.lastTs = c.E, nowMs
}

// Cell возвращает состояние ячейки (копия) или nil.
func (s *PrimaryAlphabetStore) Cell(exciterID, bandID int) *PrimaryCellState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := PrimaryGlyphKey{ExciterID: exciterID, BandID: bandID}
	if c, ok := s.cells[key]; ok {
		cp := *c
		return &cp
	}
	return nil
}

// Count возвращает число активных ячеек.
func (s *PrimaryAlphabetStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.cells)
}

// ExciterIDToName — имя режима возбуждения для отображения.
func ExciterIDToName(exciterID int) string {
	switch exciterID {
	case 0:
		return "idle"
	case 1:
		return "am-sine"
	case 2:
		return "chirp"
	case 3:
		return "phase-scan"
	case 4:
		return "dual-asym"
	case 5:
		return "fast-scan"
	case 6:
		return "burst"
	case 7:
		return "mode-a"
	case 8:
		return "mode-b"
	case 9:
		return "mode-c"
	case 10:
		return "mode-d"
	default:
		return "?"
	}
}

// ExciterIDFromPresetName — маппинг AutoPresetName → exciter_id.
// 0 = idle/manual, 1..N = режимы возбуждения.
func ExciterIDFromPresetName(preset string) int {
	switch preset {
	case "am-sine":
		return 1
	case "chirp":
		return 2
	case "phase-scan":
		return 3
	case "dual-asym":
		return 4
	case "fast-scan":
		return 5
	case "burst":
		return 6
	case "mode-a":
		return 7
	case "mode-b":
		return 8
	case "mode-c":
		return 9
	case "mode-d":
		return 10
	default:
		return 0
	}
}
