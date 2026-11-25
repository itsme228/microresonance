// Discovery C: best repeating segment (вариант C — самоподобие / автокорреляция).
// Ищем в последних T секундах сегмент длиной L, который лучше всего повторяется (max NCC).
// Этот сегмент становится глифом токена. Shape-first, без привязки к f0.

package pipeline

import (
	"math"
	domainglyph "server/internal/domain/glyph"
)

const (
	// Discovery defaults
	discoveryTauMinSec   = 0.2  // мин лаг τ (сек)
	discoveryTauMaxSec   = 2.0  // макс лаг τ (сек)
	discoveryTauStepSec  = 0.02 // шаг по τ (20 ms)
	discoveryDownsample  = 64   // downsample окна до 64 точек для быстрого NCC
	discoveryEntropyBins = 32   // бины для residual entropy
	discoveryEps         = 1e-12
	// Keep contract with glyph representation (16 segments * 2 envelope points).
	FrozenGlyphSegments = 16
	FrozenGlyphPoints   = 32
)

// DiscoveryParams — параметры алгоритма Discovery C.
type DiscoveryParams struct {
	BufferSec   float64 // T: последние T секунд буфера (2..5)
	SegmentSec  float64 // L: длина сегмента (256 ms = 0.256)
	TauMinSec   float64
	TauMaxSec   float64
	TauStepSec  float64
	DownsampleN int
	ThrScore    float64 // порог NCC для lock (например 0.7)
	ThrH        float64 // порог residual entropy (ниже = устойчивее)
}

func defaultDiscoveryParams() DiscoveryParams {
	return DiscoveryParams{
		BufferSec:   2.0,
		SegmentSec:  0.128, // 128 ms = 512 samples @ 4k
		TauMinSec:   discoveryTauMinSec,
		TauMaxSec:   discoveryTauMaxSec,
		TauStepSec:  discoveryTauStepSec,
		DownsampleN: discoveryDownsample,
		ThrScore:    0.45,
		ThrH:        0.95,
	}
}

// DiscoveryResult — результат поиска best repeating segment.
type DiscoveryResult struct {
	Segment      []float64 // w* — лучший сегмент (сырой, L сэмплов)
	Score        float64   // max NCC
	BestTauSec   float64   // лаг лучшего совпадения
	BestMatchIdx int       // индекс начала best match в s
	HResid       float64   // residual entropy (w* - best_match)
	GlyphData    []float64 // envelope min/max, 64 точки (для LOCKED)
	ShapePoints  []float64 // для merge (64 точек, resampled w*)
	Valid        bool
}

// Build1DInvariantMag2 строит 1D инвариант: s[t] = x² + y² + z² (без sqrt — быстрее).
func Build1DInvariantMag2(x, y, z []float64) []float64 {
	n := len(x)
	if len(y) < n {
		n = len(y)
	}
	if len(z) < n {
		n = len(z)
	}
	if n < 8 {
		return nil
	}
	s := make([]float64, n)
	for i := 0; i < n; i++ {
		xi, yi, zi := 0.0, 0.0, 0.0
		if i < len(x) {
			xi = x[i]
		}
		if i < len(y) {
			yi = y[i]
		}
		if i < len(z) {
			zi = z[i]
		}
		s[i] = xi*xi + yi*yi + zi*zi
	}
	return s
}

// PreprocessDiscovery: DC remove, RMS normalize, optional highpass.
func PreprocessDiscovery(s []float64, fs float64, highpassHz float64) []float64 {
	if len(s) < 8 {
		return nil
	}
	n := len(s)
	out := make([]float64, n)
	copy(out, s)

	// DC remove
	dc := 0.0
	for _, v := range out {
		dc += v
	}
	dc /= float64(n)
	for i := range out {
		out[i] -= dc
	}

	// Highpass (опционально)
	if fs > 0 && highpassHz > 0 && highpassHz < fs/4 {
		alpha := 1.0 / (1.0 + 1.0/(2*math.Pi*highpassHz/fs))
		yp := 0.0
		xp := out[0]
		for i := 1; i < n; i++ {
			nextX := out[i]
			yp = alpha * (yp + nextX - xp)
			out[i] = yp
			xp = nextX
		}
		out[0] = 0
	}

	// RMS normalize
	r := rms(out)
	if r < discoveryEps {
		r = discoveryEps
	}
	for i := range out {
		out[i] /= r
	}
	return out
}

func rms(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(s)))
}

// DownsampleSegment даунсэмплирует сегмент до n точек (линейная интерполяция).
func DownsampleSegment(seg []float64, n int) []float64 {
	if len(seg) < 2 || n < 2 {
		return nil
	}
	out := make([]float64, n)
	step := float64(len(seg)-1) / float64(n-1)
	for i := 0; i < n; i++ {
		idx := step * float64(i)
		if idx >= float64(len(seg)-1) {
			idx = float64(len(seg) - 1)
		}
		i0 := int(idx)
		i1 := i0 + 1
		if i1 >= len(seg) {
			i1 = len(seg) - 1
		}
		frac := idx - float64(i0)
		out[i] = seg[i0]*(1-frac) + seg[i1]*frac
	}
	return out
}

// NCC возвращает normalized cross-correlation между a и b (0..1, 1 = идентичны).
func NCC(a, b []float64) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n < 2 {
		return 0
	}
	var sumA, sumB, sumA2, sumB2, sumAB float64
	for i := 0; i < n; i++ {
		ai, bi := a[i], b[i]
		sumA += ai
		sumB += bi
		sumA2 += ai * ai
		sumB2 += bi * bi
		sumAB += ai * bi
	}
	meanA := sumA / float64(n)
	meanB := sumB / float64(n)
	var cov, varA, varB float64
	for i := 0; i < n; i++ {
		da := a[i] - meanA
		db := b[i] - meanB
		cov += da * db
		varA += da * da
		varB += db * db
	}
	if varA < discoveryEps || varB < discoveryEps {
		return 0
	}
	corr := cov / (math.Sqrt(varA) * math.Sqrt(varB))
	if corr > 1 {
		corr = 1
	}
	if corr < -1 {
		corr = -1
	}
	// Преобразуем в 0..1: (corr + 1) / 2
	return (corr + 1) / 2
}

// ResidualEntropy считает энтропию residual = a - b (гистограмма nBins).
func ResidualEntropy(a, b []float64, nBins int) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n < 4 || nBins < 2 {
		return 0
	}
	resid := make([]float64, n)
	for i := 0; i < n; i++ {
		resid[i] = a[i] - b[i]
	}
	// Гистограмма значений
	minV, maxV := resid[0], resid[0]
	for _, v := range resid {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	rangeV := maxV - minV
	if rangeV < discoveryEps {
		rangeV = discoveryEps
	}
	bins := make([]float64, nBins)
	for _, v := range resid {
		bin := int((v - minV) / rangeV * float64(nBins))
		if bin < 0 {
			bin = 0
		}
		if bin >= nBins {
			bin = nBins - 1
		}
		bins[bin]++
	}
	// Нормализованная энтропия
	sum := 0.0
	for _, c := range bins {
		sum += c
	}
	if sum < discoveryEps {
		return 0
	}
	H := 0.0
	for _, c := range bins {
		p := c / sum
		if p > discoveryEps {
			H -= p * math.Log2(p+discoveryEps)
		}
	}
	maxH := math.Log2(float64(nBins))
	if maxH < discoveryEps {
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

// EnvelopeFromSegment строит envelope min/max по nSegments сегментам → 64 точки (32×min + 32×max).
func EnvelopeFromSegment(seg []float64, nSegments int) []float64 {
	if len(seg) < 4 || nSegments < 2 {
		return nil
	}
	K := nSegments
	if K > 32 {
		K = 32
	}
	out := make([]float64, FrozenGlyphPoints) // 64
	for j := 0; j < K; j++ {
		segStart := j * len(seg) / K
		segEnd := (j + 1) * len(seg) / K
		if segEnd > len(seg) {
			segEnd = len(seg)
		}
		if segStart >= segEnd {
			segEnd = segStart + 1
			if segEnd > len(seg) {
				segEnd = len(seg)
				segStart = segEnd - 1
			}
		}
		ymin := seg[segStart]
		ymax := seg[segStart]
		for i := segStart; i < segEnd; i++ {
			if seg[i] < ymin {
				ymin = seg[i]
			}
			if seg[i] > ymax {
				ymax = seg[i]
			}
		}
		out[j] = ymin
		out[K+j] = ymax
	}
	// Нормализация к [-1, 1]
	minAll, maxAll := out[0], out[0]
	for _, v := range out {
		if v < minAll {
			minAll = v
		}
		if v > maxAll {
			maxAll = v
		}
	}
	rangeV := maxAll - minAll
	if rangeV < discoveryEps {
		rangeV = discoveryEps
	}
	for i := range out {
		v := (out[i]-minAll)/rangeV*2 - 1
		if v < -1 {
			v = -1
		}
		if v > 1 {
			v = 1
		}
		out[i] = v
	}
	return out
}

// SegmentToShapeVector превращает сегмент в domainglyph.ShapeVector (64 точек для merge).
// Глиф обязательно начинается нулём и заканчивается нулём, масштаб в [-1, 1] — разделение по нулю.
func SegmentToShapeVector(seg []float64, nPoints int) domainglyph.ShapeVector {
	if len(seg) < 4 || nPoints < 4 {
		return domainglyph.ShapeVector{}
	}
	pts := DownsampleSegment(seg, nPoints)
	if len(pts) == 0 {
		return domainglyph.ShapeVector{}
	}
	n := len(pts)
	p0, pN := pts[0], pts[n-1]
	for i := 0; i < n; i++ {
		t := 0.0
		if n > 1 {
			t = float64(i) / float64(n-1)
		}
		pts[i] -= p0 + (pN-p0)*t
	}
	maxAbs := discoveryEps
	for _, v := range pts {
		a := v
		if a < 0 {
			a = -a
		}
		if a > maxAbs {
			maxAbs = a
		}
	}
	if maxAbs < discoveryEps {
		maxAbs = discoveryEps
	}
	for i := range pts {
		pts[i] /= maxAbs
		if pts[i] < -1 {
			pts[i] = -1
		}
		if pts[i] > 1 {
			pts[i] = 1
		}
	}
	return domainglyph.ShapeVector{Points: pts}
}

// FindBestRepeatingSegment ищет сегмент длиной L, который лучше всего повторяется в последних T секундах.
func FindBestRepeatingSegment(s []float64, fs float64, params DiscoveryParams) DiscoveryResult {
	res := DiscoveryResult{}
	if fs <= 0 || len(s) < 64 {
		return res
	}
	T := int(params.BufferSec * fs)
	L := int(params.SegmentSec * fs)
	if T > len(s) {
		T = len(s)
	}
	if L < 32 {
		L = 32
	}
	// Адаптивно: L не больше T/4, чтобы tauMin=L и tauMax=T-L-1 давали tauMin < tauMax
	if L > T/4 {
		L = T / 4
	}
	if L < 32 {
		L = 32
	}
	if T < 2*L+1 {
		return res
	}
	// Берём последние T сэмплов
	start := len(s) - T
	if start < 0 {
		start = 0
		T = len(s)
	}
	buf := s[start : start+T]

	// Лаги τ: tauMin=L (непересекающиеся окна), tauMax=T-L-1
	tauMinSamp := L
	tauMaxSamp := T - L - 1
	tauStepSamp := int(params.TauStepSec * fs)
	if tauStepSamp < 1 {
		tauStepSamp = 1
	}
	if tauStepSamp > (tauMaxSamp-tauMinSamp)/4 {
		tauStepSamp = (tauMaxSamp - tauMinSamp) / 4
		if tauStepSamp < 1 {
			tauStepSamp = 1
		}
	}
	if tauMinSamp >= tauMaxSamp {
		return res
	}

	downN := params.DownsampleN
	if downN < 8 {
		downN = discoveryDownsample
	}

	// Скользящее окно: кандидат w = buf[t:t+L], t от 0 до T-L-1
	// Сравниваем с buf[t-tau : t-tau+L] для tau в [tauMin, tauMax]
	bestScore := -1.0
	bestT := 0
	bestTau := tauMinSamp
	var bestMatch []float64

	// Шаг по кандидатам (не каждый сэмпл — ускоряем)
	candStep := L / 4
	if candStep < 8 {
		candStep = 8
	}

	for t := 0; t <= T-L-1; t += candStep {
		w := buf[t : t+L]
		wDown := DownsampleSegment(w, downN)
		if len(wDown) < 4 {
			continue
		}
		bestTauLocal := tauMinSamp
		bestNCC := -1.0
		for tau := tauMinSamp; tau <= tauMaxSamp && tau <= t; tau += tauStepSamp {
			tPast := t - tau
			if tPast+L > T {
				continue
			}
			m := buf[tPast : tPast+L]
			mDown := DownsampleSegment(m, downN)
			if len(mDown) < 4 {
				continue
			}
			ncc := NCC(wDown, mDown)
			if ncc > bestNCC {
				bestNCC = ncc
				bestTauLocal = tau
			}
		}
		if bestNCC > bestScore {
			bestScore = bestNCC
			bestT = t
			bestTau = bestTauLocal
			tPast := bestT - bestTau
			if tPast >= 0 && tPast+L <= T {
				bestMatch = make([]float64, L)
				copy(bestMatch, buf[tPast:tPast+L])
			}
		}
	}

	if bestScore < 0 || bestT+L > T {
		return res
	}

	wStar := make([]float64, L)
	copy(wStar, buf[bestT:bestT+L])

	// Residual entropy: w* vs best_match
	H := 0.0
	if len(bestMatch) == L {
		H = ResidualEntropy(wStar, bestMatch, discoveryEntropyBins)
	}

	// GlyphData: envelope min/max, 64 точки
	glyphData := EnvelopeFromSegment(wStar, FrozenGlyphSegments)

	// domainglyph.ShapeVector для merge
	shapeVec := SegmentToShapeVector(wStar, 64)

	res = DiscoveryResult{
		Segment:      wStar,
		Score:        bestScore,
		BestTauSec:   float64(bestTau) / fs,
		BestMatchIdx: start + bestT - bestTau,
		HResid:       H,
		GlyphData:    glyphData,
		ShapePoints:  shapeVec.Points,
		Valid:        true,
	}
	return res
}

// RunDiscovery выполняет полный цикл Discovery C: invariant → preprocess → find best segment.
func RunDiscovery(x, y, z []float64, fs float64, params DiscoveryParams, highpassHz float64) DiscoveryResult {
	s := Build1DInvariantMag2(x, y, z)
	if len(s) == 0 {
		return DiscoveryResult{}
	}
	sNorm := PreprocessDiscovery(s, fs, highpassHz)
	if len(sNorm) == 0 {
		return DiscoveryResult{}
	}
	return FindBestRepeatingSegment(sNorm, fs, params)
}
