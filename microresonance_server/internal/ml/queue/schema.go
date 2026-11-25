// ml_queue.go — три очереди для ML: FrameSummary (heartbeat), EventGlyph (по событию), EventReaction (по AM-циклу).
// Архитектура: не фиксированный словарь, open-vocab embedding по protoSig/phaseVec.

package queue

import (
	"encoding/json"
	"time"
)

// ExciterState — режим возбуждения (idle / am-sine + AmFreqHz).
type ExciterState struct {
	ID       int     // 0=idle, 1..N=am-sine, chirp, ...
	AmFreqHz float64 // частота AM-модуляции, Гц
}

// FrameSummary — heartbeat раз в T_heartbeat (1s). Короткое состояние даже при тишине.
type FrameSummary struct {
	T0, T1       int64      // временное окно, ms
	BandEnergy   [8]float64 // RMS/energy по полосам B0..B7
	TotalEnergy  float64    // сумма
	ExciterState ExciterState
	Quality      float64    // SNR proxy / clipping / sensor health (0..1)
	AxisRatio    [8]float64 // axis_energy_ratio по полосам (опционально)
}

// EventGlyph — по событию: новый/обновлённый глиф в PatternStore.
// Триггер: MergeOrAddByShape (Occurrences++ или новый ID).
// Для glyph_classifier (run_glyph_model.py) нужны Shape, Gx, Gy, Gz, Features — заполняются при успешном ExtractTrainerEvent.
type EventGlyph struct {
	T        int64     // timestamp ms
	Dt       float64   // секунды с предыдущего события (любого типа)
	BandIdx  int       // полоса B0..B7
	FreqHz   float64   // пик
	Amp      float64   // ampNorm / peakScore
	GlyphID  int       // если уже есть в алфавите; -1 = новый
	ProtoSig []float64 // 8–16 чисел: PeakProfile (8) + axis (u1,u2,e1,e2)
	AxisSig  []float64 // axis_ratio/coherence — если есть
	IsNewID  bool      // true = только что создан
	// Полные данные для glyph_classifier (train_glyph_model.py)
	Shape    []float64 // 64 точки комбинированного профиля
	Gx       []float64 // 64 точки по оси X
	Gy       []float64 // 64 точки по оси Y
	Gz       []float64 // 64 точки по оси Z
	Features []float64 // 17 инвариантов (ev.Invariants())
}

// EventReaction — по завершению AM-цикла (ReactionAlphabetStore emit).
type EventReaction struct {
	TCycleEnd   int64   // время конца цикла, ms
	Dt          float64 // секунды с предыдущего события (любого типа)
	ExciterID   int
	BandIdx     int
	ReactionID  int         // если сматчился; -1 = новый
	PhaseVec16  [16]float64 // 16 фаз AM (нормализованный вектор)
	TotalEnergy float64
	IsNewID     bool
}

// MLQueue — интерфейс очередей для ML-воркера.
type MLQueue interface {
	PushFrameSummary(f FrameSummary)
	PushEventGlyph(e EventGlyph)
	PushEventReaction(e EventReaction)
}

// MLQueueImpl — реализация с каналами (неблокирующая при переполнении).
type MLQueueImpl struct {
	frameCh    chan FrameSummary
	glyphCh    chan EventGlyph
	reactionCh chan EventReaction

	lastEventTs int64 // ms, для вычисления dt

	// Пороги для фильтрации (чтобы не засорять очереди)
	EventGlyphAmpNormMin   float64   // 0.8
	EventGlyphEnergyMin    []float64 // E_min_band[band] — nil = не проверять
	EventReactionEnergyMin float64   // 0.005
	EventReactionMinBins   int       // ≥3 активных фазовых бинов
}

// DefaultMLQueueCap — ёмкость каналов.
const DefaultMLQueueCap = 64

// NewMLQueue создаёт очередь с дефолтными порогами.
func NewMLQueue() *MLQueueImpl {
	return &MLQueueImpl{
		frameCh:                make(chan FrameSummary, DefaultMLQueueCap),
		glyphCh:                make(chan EventGlyph, DefaultMLQueueCap),
		reactionCh:             make(chan EventReaction, DefaultMLQueueCap),
		EventGlyphAmpNormMin:   0.8,
		EventReactionEnergyMin: 0.005,
		EventReactionMinBins:   3,
	}
}

// PushFrameSummary — неблокирующая отправка.
func (q *MLQueueImpl) PushFrameSummary(f FrameSummary) {
	select {
	case q.frameCh <- f:
	default:
		// канал полон — пропускаем
	}
}

// PushEventGlyph — с проверкой порогов.
func (q *MLQueueImpl) PushEventGlyph(e EventGlyph) {
	if e.Amp < q.EventGlyphAmpNormMin {
		return
	}
	if len(q.EventGlyphEnergyMin) > e.BandIdx && e.BandIdx >= 0 {
		// totalEnergyBand проверка — здесь amp как proxy; при необходимости передавать totalEnergyBand отдельно
	}
	dt := 0.0
	if q.lastEventTs > 0 {
		dt = float64(e.T-q.lastEventTs) / 1000.0
	}
	if dt < 0 {
		dt = 0
	}
	e.Dt = dt
	q.lastEventTs = e.T
	select {
	case q.glyphCh <- e:
	default:
	}
}

// PushEventReaction — с проверкой порогов.
func (q *MLQueueImpl) PushEventReaction(e EventReaction) {
	if e.TotalEnergy < q.EventReactionEnergyMin {
		return
	}
	activeBins := 0
	for i := range e.PhaseVec16 {
		if e.PhaseVec16[i] > 0.01 {
			activeBins++
		}
	}
	if activeBins < q.EventReactionMinBins {
		return
	}
	dt := 0.0
	if q.lastEventTs > 0 {
		dt = float64(e.TCycleEnd-q.lastEventTs) / 1000.0
	}
	if dt < 0 {
		dt = 0
	}
	e.Dt = dt
	q.lastEventTs = e.TCycleEnd
	select {
	case q.reactionCh <- e:
	default:
	}
}

// FrameChan возвращает канал FrameSummary (для воркера).
func (q *MLQueueImpl) FrameChan() <-chan FrameSummary {
	return q.frameCh
}

// GlyphChan возвращает канал EventGlyph.
func (q *MLQueueImpl) GlyphChan() <-chan EventGlyph {
	return q.glyphCh
}

// ReactionChan возвращает канал EventReaction.
func (q *MLQueueImpl) ReactionChan() <-chan EventReaction {
	return q.reactionCh
}

// ToJSON сериализует сообщение для отправки в Python.
func (f *FrameSummary) ToJSON() ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"type":         "frame_summary",
		"t0":           f.T0,
		"t1":           f.T1,
		"band_energy":  f.BandEnergy[:],
		"total_energy": f.TotalEnergy,
		"exciter_id":   f.ExciterState.ID,
		"am_freq_hz":   f.ExciterState.AmFreqHz,
		"quality":      f.Quality,
		"axis_ratio":   f.AxisRatio[:],
	})
}

// ToJSON для EventGlyph.
// Для glyph_classifier (run_glyph_model.py) включает shape, x, y, z, features при наличии.
func (e *EventGlyph) ToJSON() ([]byte, error) {
	m := map[string]interface{}{
		"type":      "event_glyph",
		"t":         e.T,
		"dt":        e.Dt,
		"band_idx":  e.BandIdx,
		"freq_hz":   e.FreqHz,
		"amp":       e.Amp,
		"glyph_id":  e.GlyphID,
		"proto_sig": e.ProtoSig,
		"axis_sig":  e.AxisSig,
		"is_new_id": e.IsNewID,
	}
	if len(e.Shape) >= 10 {
		m["shape"] = e.Shape
	}
	if len(e.Gx) >= 10 {
		m["x"] = e.Gx
	}
	if len(e.Gy) >= 10 {
		m["y"] = e.Gy
	}
	if len(e.Gz) >= 10 {
		m["z"] = e.Gz
	}
	if len(e.Features) >= 10 {
		m["features"] = e.Features
	}
	return json.Marshal(m)
}

// ToJSON для EventReaction.
func (e *EventReaction) ToJSON() ([]byte, error) {
	vec := make([]float64, 16)
	copy(vec, e.PhaseVec16[:])
	return json.Marshal(map[string]interface{}{
		"type":         "event_reaction",
		"t_cycle_end":  e.TCycleEnd,
		"dt":           e.Dt,
		"exciter_id":   e.ExciterID,
		"band_idx":     e.BandIdx,
		"reaction_id":  e.ReactionID,
		"phase_vec16":  vec,
		"total_energy": e.TotalEnergy,
		"is_new_id":    e.IsNewID,
	})
}

// THeartbeat — интервал heartbeat (1s).
const THeartbeat = time.Second
