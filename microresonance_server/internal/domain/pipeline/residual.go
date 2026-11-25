// ResidualDecomposer: разложение по остаточной энтропии.
// Итеративно находим токен с max ΔH, вычитаем вклад, пока остаток не станет шумом.

package pipeline

import (
	"math"
	"sync"
	"time"
)

const (
	residualEps         = 1e-12
	entropyBins         = 16 // меньше бинов → быстрее Entropy
	softSubtractScale   = 0.8
	crossCorrMaxLen     = 512 // даунсэмпл residual до 512 для cross-corr
	crossCorrOffsetStep = 4   // шаг по offset (каждый 4-й)
)

// Occurrence — одно срабатывание токена в окне.
type Occurrence struct {
	TokenID       int
	OffsetSamples int
	StartTs       int64
	EndTs         int64
	Confidence    float64
	DeltaH        float64
}

// ResidualStep — один шаг итерации разложения.
type ResidualStep struct {
	Iteration  int
	HBefore    float64
	HAfter     float64
	DeltaH     float64
	TokenID    int
	Occurrence Occurrence
}

// ToFeatures превращает окно в признаковое представление для энтропии (бины энергии).
func ToFeatures(window []float64, nBins int) []float64 {
	if len(window) == 0 || nBins <= 0 {
		return nil
	}
	bins := make([]float64, nBins)
	step := float64(len(window)) / float64(nBins)
	for i := 0; i < len(window); i++ {
		bin := int(float64(i) / step)
		if bin >= nBins {
			bin = nBins - 1
		}
		bins[bin] += window[i] * window[i]
	}
	return bins
}

// Entropy считает нормированную энтропию распределения энергии (0..1).
func Entropy(features []float64) float64 {
	if len(features) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range features {
		sum += v
	}
	if sum < residualEps {
		return 0
	}
	H := 0.0
	for _, v := range features {
		p := v / sum
		if p > residualEps {
			H -= p * math.Log2(p+residualEps)
		}
	}
	maxH := math.Log2(float64(len(features)))
	if maxH < residualEps {
		maxH = 1
	}
	Hnorm := H / maxH
	if Hnorm > 1 {
		Hnorm = 1
	}
	if Hnorm < 0 {
		Hnorm = 0
	}
	return Hnorm
}

// FindBestToken находит токен, дающий максимальное снижение энтропии, и смещение (cross-correlation).
func FindBestToken(residual []float64, tokens []Pattern, minDeltaH float64) (bestTokenID int, bestDeltaH float64, bestOcc Occurrence) {
	bestTokenID = -1
	bestDeltaH = 0
	features := ToFeatures(residual, entropyBins)
	H0 := Entropy(features)
	if H0 < residualEps {
		return -1, 0, bestOcc
	}
	for _, t := range tokens {
		if len(t.GlyphShape.Points) == 0 {
			continue
		}
		offset := crossCorrOffset(residual, t.GlyphShape.Points)
		resAfter := SubtractToken(residual, t.GlyphShape.Points, offset, softSubtractScale)
		featAfter := ToFeatures(resAfter, entropyBins)
		H1 := Entropy(featAfter)
		deltaH := H0 - H1
		if deltaH > bestDeltaH && deltaH >= minDeltaH {
			bestDeltaH = deltaH
			bestTokenID = t.ID
			bestOcc = Occurrence{
				TokenID:       t.ID,
				OffsetSamples: offset,
				Confidence:    clamp01(deltaH * 4),
				DeltaH:        deltaH,
			}
		}
	}
	return bestTokenID, bestDeltaH, bestOcc
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// crossCorrOffset возвращает смещение с макс. cross-correlation.
// Ускорение: даунсэмпл residual до crossCorrMaxLen, шаг по offset = crossCorrOffsetStep.
func crossCorrOffset(residual, shape []float64) int {
	n := len(residual)
	L := len(shape)
	if n < L || L == 0 {
		return 0
	}
	// Даунсэмпл residual до maxLen для быстрого поиска
	maxLen := crossCorrMaxLen
	if maxLen > n {
		maxLen = n
	}
	var res []float64
	if n <= maxLen {
		res = residual
	} else {
		res = make([]float64, maxLen)
		step := float64(n-1) / float64(maxLen-1)
		for i := 0; i < maxLen; i++ {
			idx := int(step * float64(i))
			if idx >= n {
				idx = n - 1
			}
			res[i] = residual[idx]
		}
	}
	nr := len(res)
	if nr < L {
		return 0
	}
	bestIdx := 0
	bestCorr := -1e20
	for offset := 0; offset <= nr-L; offset += crossCorrOffsetStep {
		var sum float64
		for j := 0; j < L; j++ {
			sum += res[offset+j] * shape[j]
		}
		if sum > bestCorr {
			bestCorr = sum
			bestIdx = offset
		}
	}
	// Переводим индекс в исходные сэмплы
	if n > nr && nr > L {
		bestIdx = bestIdx * (n - L) / (nr - L)
	}
	if bestIdx > n-L {
		bestIdx = n - L
	}
	if bestIdx < 0 {
		bestIdx = 0
	}
	return bestIdx
}

// SubtractToken вычитает вклад формы из residual (soft subtraction).
func SubtractToken(residual []float64, shape []float64, offset int, scale float64) []float64 {
	out := make([]float64, len(residual))
	copy(out, residual)
	L := len(shape)
	if offset < 0 || offset+L > len(out) {
		return out
	}
	for i := 0; i < L && offset+i < len(out); i++ {
		out[offset+i] -= scale * shape[i]
	}
	return out
}

// DecomposeResult — результат разложения по остаточной энтропии.
type DecomposeResult struct {
	Steps       []ResidualStep
	Occurrences []Occurrence
	HFinal      float64
	Residual    []float64
}

// ResidualDecomposer выполняет итеративное разложение окна по остаточной энтропии.
// Discovery C: SetDiscoveryResult сохраняет результат best repeating segment для UI.
type ResidualDecomposer struct {
	mu             sync.Mutex
	steps          []ResidualStep
	occurrences    []Occurrence
	HFinal         float64
	residual       []float64
	discoveryScore float64   // Discovery C: NCC score
	discoveryH     float64   // Discovery C: residual entropy
	discoverySeg   []float64 // Discovery C: best segment (для residual plot)
}

// SetDiscoveryResult сохраняет результат Discovery C для отображения в DISCOVERY.
func (d *ResidualDecomposer) SetDiscoveryResult(result DiscoveryResult) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.discoveryScore = result.Score
	d.discoveryH = result.HResid
	d.discoverySeg = nil
	if len(result.Segment) > 0 {
		d.discoverySeg = append([]float64(nil), result.Segment...)
	}
}

// DecomposeParams — параметры разложения и классификатора (допуск merge).
type DecomposeParams struct {
	MaxIters       int
	HThr           float64
	MinDeltaH      float64
	DistThr        float64 // допуск формы: merge если ShapeDistance < DistThr
	MinConf        float64
	Fs             float64
	PointsPerCycle int
	Metrics        *PipelineMetrics // опционально: сбор KPI
	AmpNorm        float64          // для метрик (RMS окна до нормализации)
	// AllowAllWaves: true = кластеризовать и фронты (нестационарные окна); false = только плато.
	AllowAllWaves bool
}

// Decompose разбирает волну до конца: всегда max_iters итераций или пока H <= H_thr.
// Каждый шаг: либо вычитаем существующий токен (классификация = существующий), либо извлекаем форму из остатка и MergeOrAddByShape (новая или merge).
func (d *ResidualDecomposer) Decompose(window []float64, store *PatternStore, params DecomposeParams, now time.Time) DecomposeResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := DecomposeResult{}
	if len(window) < 16 {
		return result
	}
	residual := make([]float64, len(window))
	copy(residual, window)
	maxIters := params.MaxIters
	if maxIters <= 0 {
		maxIters = 5
	}
	HThr := params.HThr
	if HThr <= 0 {
		HThr = 0.6
	}
	minDeltaH := params.MinDeltaH
	if minDeltaH <= 0 {
		minDeltaH = 0.02
	}
	distThr := params.DistThr
	if distThr <= 0 {
		distThr = 0.35
	}
	fs := params.Fs
	if fs <= 0 {
		fs = 4000
	}
	pointsPerCycle := params.PointsPerCycle
	if pointsPerCycle <= 0 {
		pointsPerCycle = 64
	}
	nowMs := now.UnixMilli()
	var steps []ResidualStep
	var occs []Occurrence

	// При AllowAllWaves=true — фронты тоже кластеризуем (вариации глифа). Иначе только плато.
	T := estimatePeriodSamples(window)
	f0 := 0.0
	if T >= 4 && fs > 0 {
		f0 = fs / float64(T)
	}
	windowStable := params.AllowAllWaves
	if !windowStable && f0 >= 0.5 {
		env := computeEnvelope(window, 0, len(window), fs, f0)
		windowStable = len(env) >= 8 && StationarityOK(env, fs, 0.15, 0.25)
	}

	for iter := 0; iter < maxIters; iter++ {
		feat := ToFeatures(residual, entropyBins)
		H := Entropy(feat)
		if H <= HThr {
			result.HFinal = H
			result.Residual = residual
			result.Steps = steps
			result.Occurrences = occs
			d.steps = steps
			d.occurrences = occs
			d.HFinal = H
			d.residual = residual
			return result
		}
		tokens := store.List()
		bestID, deltaH, occ := FindBestToken(residual, tokens, minDeltaH)

		if bestID >= 0 && deltaH >= minDeltaH {
			// Классификация: существующий токен — вычитаем и фиксируем
			if params.Metrics != nil {
				params.Metrics.RecordHit(bestID)
			}
			occ.StartTs = nowMs
			occs = append(occs, occ)
			var tokenPoints []float64
			for _, t := range tokens {
				if t.ID == bestID {
					tokenPoints = t.GlyphShape.Points
					break
				}
			}
			steps = append(steps, ResidualStep{
				Iteration:  iter,
				HBefore:    H,
				DeltaH:     deltaH,
				TokenID:    bestID,
				Occurrence: occ,
			})
			residual = SubtractToken(residual, tokenPoints, occ.OffsetSamples, softSubtractScale)
			featAfter := ToFeatures(residual, entropyBins)
			HAfter := Entropy(featAfter)
			steps[len(steps)-1].HAfter = HAfter
		} else {
			// Нет подходящего токена — из остатка извлекаем форму. Новый shape-ID только если окно плато (не фронт).
			shape := BuildShape(residual, fs, pointsPerCycle)
			if len(shape.Points) > 0 {
				if windowStable {
					shapeSig := ComputeShapeSignature(shape, 0, 1)
					peakProf := PeakProfileFromShape(shape)
					mr := store.MergeOrAddByShape(Pattern{
						ShapeSig:    shapeSig,
						GlyphShape:  shape,
						PeakProfile: peakProf,
						Captured:    nowMs,
						Confidence:  params.MinConf,
						Freq:        0,
						Amp:         1,
					}, ShapeMergeParams{MinCosForMerge: DefaultMinCos, MaxDistForMerge: 0.65})
					if params.Metrics != nil {
						if mr.Merged {
							params.Metrics.RecordMerge(mr.BestID, mr.DistShapeMin)
						} else {
							params.Metrics.RecordNew(mr.BestID, mr.DistShapeMin, mr.SecondBestGap, params.MinConf, params.AmpNorm, 0, "new_shape", mr.RejectGate, mr.BestRejectedID, mr.BestRejectedDist, mr.BestRejectedCos)
							LogNewPatternBlindSpotFromMergeResult(mr, distThr)
						}
					}
				}
				offset := crossCorrOffset(residual, shape.Points)
				residual = SubtractToken(residual, shape.Points, offset, softSubtractScale)
				featAfter := ToFeatures(residual, entropyBins)
				HAfter := Entropy(featAfter)
				deltaH := H - HAfter
				steps = append(steps, ResidualStep{
					Iteration:  iter,
					HBefore:    H,
					HAfter:     HAfter,
					DeltaH:     deltaH,
					TokenID:    -1, // только что добавлен/объединён
					Occurrence: Occurrence{Confidence: params.MinConf, DeltaH: deltaH},
				})
				occs = append(occs, Occurrence{StartTs: nowMs, Confidence: params.MinConf, DeltaH: deltaH})
			} else {
				// Не удалось извлечь форму — останавливаем итерации
				break
			}
		}
	}
	feat := ToFeatures(residual, entropyBins)
	result.HFinal = Entropy(feat)
	result.Residual = residual
	result.Steps = steps
	result.Occurrences = occs
	d.steps = steps
	d.occurrences = occs
	d.HFinal = result.HFinal
	d.residual = residual
	return result
}

// LastResult возвращает последний результат разложения (для UI).
// Discovery C: при наличии discoveryResult возвращает H=discoveryH, residual=discoverySeg.
func (d *ResidualDecomposer) LastResult() (steps []ResidualStep, occurrences []Occurrence, H float64, residual []float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.steps != nil {
		steps = append([]ResidualStep(nil), d.steps...)
	}
	if d.occurrences != nil {
		occurrences = append([]Occurrence(nil), d.occurrences...)
	}
	H = d.HFinal
	if d.residual != nil {
		residual = append([]float64(nil), d.residual...)
	}
	// Discovery C: при наличии discovery result — используем его для H и residual
	if len(d.discoverySeg) > 0 {
		H = d.discoveryH
		residual = append([]float64(nil), d.discoverySeg...)
	}
	return steps, occurrences, H, residual
}

// DiscoveryScore возвращает score Discovery C (NCC) для UI.
func (d *ResidualDecomposer) DiscoveryScore() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.discoveryScore
}
