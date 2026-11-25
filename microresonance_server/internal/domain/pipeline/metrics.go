// PipelineMetrics: KPI для тюнинга пайплайна по трём кривым:
// growth/collapse, reuse/repeatability, effective_vocab/ML richness.

package pipeline

import (
	"log"
	"math"
	"sync"
	"time"
)

const (
	metricsWindow10s = 10 * time.Second
	metricsWindow60s = 60 * time.Second
	metricsMaxEvents = 500
)

// EventType — тип события пайплайна.
type EventType int

const (
	EventHit   EventType = iota // совпадение с существующим токеном
	EventMerge                  // merge в существующий
	EventNew                    // новый токен
)

// PipelineEvent — одно событие для метрик.
type PipelineEvent struct {
	Ts       time.Time
	Type     EventType
	TokenID  int
	DistMin  float64
	Dist2Gap float64
	Conf     float64
	AmpNorm  float64
	F0       float64
	Reason   string
	// Диагностика отказа merge при NEW.
	RejectGate string
	RejectID   int
	RejectDist float64
	RejectCos  float64
}

// PreprocessSnapshot — снимок препроцессинга (amp_norm, noise_floor).
type PreprocessSnapshot struct {
	Ts         time.Time
	AmpNorm    float64
	NoiseFloor float64
	Conf       float64
}

// PipelineMetrics — агрегаты для тюнинга.
type PipelineMetrics struct {
	mu sync.Mutex

	events10s     []PipelineEvent
	events60s     []PipelineEvent
	preprocess10s []PreprocessSnapshot
	newDists      []float64 // dist_shape_min для NEW событий
	startTs       time.Time

	// OnNew — опционально: вызывается на каждое NEW_PATTERN (reason, dist_min, conf, amp_norm, f0, best_match_id).
	OnNew func(e PipelineEvent)
	// OnSpectrum — опционально: вызывается каждый тик для спектров (raw, prep, residual, store, fs).
	OnSpectrum func(raw, prep, residual []float64, store any, fs float64)
}

// NewPipelineMetrics создаёт коллектор метрик.
func NewPipelineMetrics() *PipelineMetrics {
	return &PipelineMetrics{
		events10s:     make([]PipelineEvent, 0, 128),
		events60s:     make([]PipelineEvent, 0, 512),
		preprocess10s: make([]PreprocessSnapshot, 0, 64),
		newDists:      make([]float64, 0, 64),
		startTs:       time.Now(),
	}
}

// RecordHit фиксирует совпадение с существующим токеном.
func (m *PipelineMetrics) RecordHit(tokenID int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.appendEvent(PipelineEvent{Ts: now, Type: EventHit, TokenID: tokenID})
}

// RecordMerge фиксирует merge в существующий токен.
func (m *PipelineMetrics) RecordMerge(tokenID int, distMin float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.appendEvent(PipelineEvent{Ts: now, Type: EventMerge, TokenID: tokenID, DistMin: distMin})
}

// RecordNew фиксирует добавление нового токена.
func (m *PipelineMetrics) RecordNew(tokenID int, distMin, dist2Gap, conf, ampNorm, f0 float64, reason string, rejectGate string, rejectID int, rejectDist, rejectCos float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.appendEvent(PipelineEvent{
		Ts: now, Type: EventNew, TokenID: tokenID,
		DistMin: distMin, Dist2Gap: dist2Gap, Conf: conf, AmpNorm: ampNorm, F0: f0, Reason: reason,
		RejectGate: rejectGate, RejectID: rejectID, RejectDist: rejectDist, RejectCos: rejectCos,
	})
	if distMin < 100 { // фильтр sentinel (1e9 при пустом store)
		m.newDists = append(m.newDists, distMin)
		if len(m.newDists) > 100 {
			m.newDists = m.newDists[len(m.newDists)-100:]
		}
	}
	if m.OnNew != nil {
		m.OnNew(PipelineEvent{
			Ts: time.Now(), Type: EventNew, TokenID: tokenID,
			DistMin: distMin, Dist2Gap: dist2Gap, Conf: conf, AmpNorm: ampNorm, F0: f0, Reason: reason,
			RejectGate: rejectGate, RejectID: rejectID, RejectDist: rejectDist, RejectCos: rejectCos,
		})
	}
}

// RecordPreprocess фиксирует снимок препроцессинга.
func (m *PipelineMetrics) RecordPreprocess(ampNorm, noiseFloor, conf float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.preprocess10s = append(m.preprocess10s, PreprocessSnapshot{Ts: now, AmpNorm: ampNorm, NoiseFloor: noiseFloor, Conf: conf})
	m.prunePreprocess(now)
}

func (m *PipelineMetrics) appendEvent(e PipelineEvent) {
	m.events10s = append(m.events10s, e)
	m.events60s = append(m.events60s, e)
	m.pruneEvents(e.Ts)
}

func (m *PipelineMetrics) pruneEvents(now time.Time) {
	cut10 := now.Add(-metricsWindow10s)
	cut60 := now.Add(-metricsWindow60s)
	i10 := 0
	for i10 < len(m.events10s) && m.events10s[i10].Ts.Before(cut10) {
		i10++
	}
	m.events10s = m.events10s[i10:]
	if len(m.events10s) > metricsMaxEvents {
		m.events10s = m.events10s[len(m.events10s)-metricsMaxEvents:]
	}
	i60 := 0
	for i60 < len(m.events60s) && m.events60s[i60].Ts.Before(cut60) {
		i60++
	}
	m.events60s = m.events60s[i60:]
	if len(m.events60s) > metricsMaxEvents*2 {
		m.events60s = m.events60s[len(m.events60s)-metricsMaxEvents*2:]
	}
}

func (m *PipelineMetrics) prunePreprocess(now time.Time) {
	cut := now.Add(-metricsWindow10s)
	i := 0
	for i < len(m.preprocess10s) && m.preprocess10s[i].Ts.Before(cut) {
		i++
	}
	m.preprocess10s = m.preprocess10s[i:]
	if len(m.preprocess10s) > 64 {
		m.preprocess10s = m.preprocess10s[len(m.preprocess10s)-64:]
	}
}

// Snapshot возвращает снимок метрик за окна 10s и 60s.
func (m *PipelineMetrics) Snapshot(tokensTotal int) MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.pruneEvents(now)
	m.prunePreprocess(now)

	s := MetricsSnapshot{
		Ts:          now,
		TokensTotal: tokensTotal,
		ElapsedSec:  now.Sub(m.startTs).Seconds(),
	}
	ev10 := m.events10s
	ev60 := m.events60s

	var hits10, merges10, new10 int
	tokenCounts10 := make(map[int]int)
	for _, e := range ev10 {
		switch e.Type {
		case EventHit:
			hits10++
			tokenCounts10[e.TokenID]++
		case EventMerge:
			merges10++
			tokenCounts10[e.TokenID]++
		case EventNew:
			new10++
			switch e.RejectGate {
			case "signature":
				s.RejectSig10s++
				if e.RejectDist > 0 && e.RejectDist <= 0.14 && e.RejectCos >= 0.88 {
					s.RejectSigNear10s++
				}
			case "frequency":
				s.RejectFreq10s++
				if e.RejectDist >= 0 && e.RejectDist <= 0.06 && e.RejectCos >= 0.94 {
					s.RejectFreqPerfect10s++
				}
			case "profile":
				s.RejectProfile10s++
			case "distance":
				s.RejectDistance10s++
			case "cosine":
				s.RejectCosine10s++
			}
		}
	}
	s.NewPer10s = new10
	s.MergesPer10s = merges10
	s.HitsPer10s = hits10
	total10 := hits10 + merges10 + new10
	if total10 > 0 {
		if merges10+new10 == 0 {
			s.MergeRate = 1
		} else {
			s.MergeRate = float64(merges10) / float64(merges10+new10)
		}
		s.ReuseRate = float64(hits10+merges10) / float64(total10)
		s.Novelty = float64(new10) / float64(total10)
	}
	if new10 > 0 {
		s.RejectSigShare10s = float64(s.RejectSig10s) / float64(new10)
		s.RejectFreqShare10s = float64(s.RejectFreq10s) / float64(new10)
		s.RejectProfileShare10s = float64(s.RejectProfile10s) / float64(new10)
		s.RejectDistanceShare10s = float64(s.RejectDistance10s) / float64(new10)
		s.RejectCosineShare10s = float64(s.RejectCosine10s) / float64(new10)
	}

	// Авто-подсказки тюнинга по метрикам отказов.
	if s.RejectFreqShare10s >= 0.30 && s.RejectFreqPerfect10s >= 5 {
		s.TuneHints = append(s.TuneHints, "freq_gate dominates with near-identical shapes: relax BandFreqTolerance (B1/B4/B5) by +15-25%")
	}
	if s.RejectSigShare10s >= 0.30 && s.RejectSigNear10s >= 5 {
		s.TuneHints = append(s.TuneHints, "signature gate dominates with near-shape matches: allow near-signature merge for very low dist/high cos")
	}
	if s.RejectProfileShare10s >= 0.25 {
		s.TuneHints = append(s.TuneHints, "profile gate contributes materially: consider keeping profile as soft gate in noisy/front windows")
	}
	s.Unique10s = len(tokenCounts10)
	s.EffectiveVocab10s = expEntropy(tokenCounts10)
	s.Top5Share10s = topKShare(tokenCounts10, 5)

	var new60 int
	for _, e := range ev60 {
		if e.Type == EventNew {
			new60++
		}
	}
	s.NewPerMin = new60

	// Preprocess
	if len(m.preprocess10s) > 0 {
		var sumAmp, sumNoise, sumConf float64
		for _, p := range m.preprocess10s {
			sumAmp += p.AmpNorm
			sumNoise += p.NoiseFloor
			sumConf += p.Conf
		}
		n := float64(len(m.preprocess10s))
		s.AmpNormMean = sumAmp / n
		s.NoiseFloorMean = sumNoise / n
		s.ConfMean = sumConf / n
		var varAmp float64
		for _, p := range m.preprocess10s {
			d := p.AmpNorm - s.AmpNormMean
			varAmp += d * d
		}
		s.AmpNormStd = math.Sqrt(varAmp / n)
	}

	// dist_shape_min для NEW
	if len(m.newDists) > 0 {
		copied := append([]float64(nil), m.newDists...)
		s.DistShapeMinMean = mean(copied)
		s.DistShapeMinP50 = percentile(copied, 0.5)
		s.DistShapeMinP95 = percentile(copied, 0.95)
	}

	return s
}

func mean(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	var s float64
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	// sort
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	idx := p * float64(len(sorted)-1)
	if idx < 0 {
		idx = 0
	}
	i0 := int(idx)
	if i0 >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	frac := idx - float64(i0)
	return sorted[i0]*(1-frac) + sorted[i0+1]*frac
}

func expEntropy(counts map[int]int) float64 {
	var total int
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	H := 0.0
	for _, c := range counts {
		p := float64(c) / float64(total)
		if p > 1e-12 {
			H -= p * math.Log2(p+1e-12)
		}
	}
	return math.Pow(2, H)
}

func topKShare(counts map[int]int, k int) float64 {
	if len(counts) == 0 {
		return 0
	}
	type pair struct {
		id  int
		cnt int
	}
	vec := make([]pair, 0, len(counts))
	var total int
	for id, c := range counts {
		vec = append(vec, pair{id, c})
		total += c
	}
	for i := 0; i < len(vec); i++ {
		for j := i + 1; j < len(vec); j++ {
			if vec[j].cnt > vec[i].cnt {
				vec[i], vec[j] = vec[j], vec[i]
			}
		}
	}
	if k > len(vec) {
		k = len(vec)
	}
	var topSum int
	for i := 0; i < k; i++ {
		topSum += vec[i].cnt
	}
	if total == 0 {
		return 0
	}
	return float64(topSum) / float64(total)
}

// LogKPI выводит снимок KPI в log (каждые 10s).
func LogKPI(snap MetricsSnapshot) {
	log.Printf("[KPI] t=%.0fs tokens=%d new_10s=%d new_min=%d merges_10s=%d hits_10s=%d merge_rate=%.2f reuse_rate=%.2f novelty=%.2f",
		snap.ElapsedSec, snap.TokensTotal, snap.NewPer10s, snap.NewPerMin, snap.MergesPer10s, snap.HitsPer10s,
		snap.MergeRate, snap.ReuseRate, snap.Novelty)
	log.Printf("[KPI] unique_10s=%d eff_vocab=%.1f top5_share=%.2f amp_norm=%.2e±%.2e noise=%.2e conf=%.2f",
		snap.Unique10s, snap.EffectiveVocab10s, snap.Top5Share10s, snap.AmpNormMean, snap.AmpNormStd,
		snap.NoiseFloorMean, snap.ConfMean)
	log.Printf("[KPI] dist_shape_min mean=%.3f p50=%.3f p95=%.3f (NEW events)",
		snap.DistShapeMinMean, snap.DistShapeMinP50, snap.DistShapeMinP95)
	log.Printf("[KPI] rejects/new_10s: sig=%d(%.0f%%, near=%d) freq=%d(%.0f%%, perfect=%d) profile=%d(%.0f%%) dist=%d(%.0f%%) cos=%d(%.0f%%)",
		snap.RejectSig10s, 100*snap.RejectSigShare10s, snap.RejectSigNear10s,
		snap.RejectFreq10s, 100*snap.RejectFreqShare10s, snap.RejectFreqPerfect10s,
		snap.RejectProfile10s, 100*snap.RejectProfileShare10s,
		snap.RejectDistance10s, 100*snap.RejectDistanceShare10s,
		snap.RejectCosine10s, 100*snap.RejectCosineShare10s)
	for _, h := range snap.TuneHints {
		log.Printf("[KPI] tune_hint: %s", h)
	}
}

// MetricsSnapshot — снимок KPI для лога.
type MetricsSnapshot struct {
	Ts                     time.Time
	TokensTotal            int
	ElapsedSec             float64
	NewPer10s              int
	NewPerMin              int
	MergesPer10s           int
	HitsPer10s             int
	MergeRate              float64
	ReuseRate              float64
	Novelty                float64
	Unique10s              int
	EffectiveVocab10s      float64
	Top5Share10s           float64
	AmpNormMean            float64
	AmpNormStd             float64
	NoiseFloorMean         float64
	ConfMean               float64
	DistShapeMinMean       float64
	DistShapeMinP50        float64
	DistShapeMinP95        float64
	RejectSig10s           int
	RejectSigNear10s       int
	RejectFreq10s          int
	RejectFreqPerfect10s   int
	RejectProfile10s       int
	RejectDistance10s      int
	RejectCosine10s        int
	RejectSigShare10s      float64
	RejectFreqShare10s     float64
	RejectProfileShare10s  float64
	RejectDistanceShare10s float64
	RejectCosineShare10s   float64
	TuneHints              []string
}
