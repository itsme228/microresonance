// Детекция: RAW → CANDIDATES (top-K пиков, нормализация, кластеры) → LOCKED (по T_lock, Δf, ΔA).

package pipeline

import (
	"math"
	"sort"
	"sync"
	"time"

	domainalphabet "server/internal/domain/alphabet"
	domainglyph "server/internal/domain/glyph"
	domainsignal "server/internal/domain/signal"
)

var ComputeFrozenGlyphFromWindow = domainglyph.ComputeFrozenGlyphFromWindow
var ExtractWaveShapeAtFreq = domainglyph.ExtractWaveShapeAtFreq
var ComputeShapeSignature = domainglyph.ComputeShapeSignature
var PeakProfileFromShape = domainglyph.PeakProfileFromShape
var ComputeEnvelope = domainglyph.ComputeEnvelope
var EnvelopeSlopePerSec = domainglyph.EnvelopeSlopePerSec
var StationarityOK = domainglyph.StationarityOK

const (
	freqAnalysisMinHz = 0.2
	freqAnalysisMaxHz = 2000
	topKPeaks         = 5
	ampNormThreshold  = 0.8
	deltaFCandidate   = 3   // бинов: объединять кандидата если |Δf| <= 3 бинов
	deltaFMergeBins   = 1   // бинов: merge строже lock; объединять только если |Δf| <= 1 бин
	minFreqHz         = 20  // не лочить токены ниже 20 Гц (поза/качание)
	emaAlpha          = 0.3 // EMA для f_est, amp_norm_est, shape_*
	// Стационарное окно: LOCK только если окно W стационарно (f0 + огибающая env)
	windowWDefault = 0.6  // сек: скользящее окно для глифа и проверки стационарности
	aCvDefault     = 0.15 // std(env)/mean(env) < A_cv
	aSlopeDefault  = 0.25 // |slope(env)|/mean(env) < A_slope (1/с)
)

// Candidate — частотный кандидат (пик/кластер), ещё не залочен.
type Candidate struct {
	ID                int
	FEst              float64 // скользящее среднее частоты, Гц
	AmpNormEst        float64 // EMA нормализованной амплитуды
	ShapeXYEst        float64 // EMA X/Y в бине
	ShapeXZEst        float64 // EMA X/Z в бине
	LastSeenTs        int64   // unix ms
	HitCount          int
	StableTime        float64 // сек: сколько подряд в допусках
	StabilityProgress float64 // 0..1 = stable_time / T_lock
	// Дрейф: «шум» = быстрый, «поза» = медленный, «стабильность» ≈ 0.
	LastFreq     float64 // сырая частота пика при прошлом обновлении
	LastAmpNorm  float64 // сырая amp_norm при прошлом обновлении
	LastTs       int64   // unix ms последнего обновления
	DriftF       float64 // Δf/dt, Hz/s
	DriftAmpNorm float64 // Δamp_norm/dt
}

// LockedToken — стабильный токен (бывший Pattern), создаётся из кандидата при lock.
type LockedToken struct {
	ID          int
	Label       string
	FLock       float64
	AmpNormLock float64
	ShapeXYLock float64
	ShapeXZLock float64
	CreatedTs   int64
	UpdatedTs   int64
	Occurrences int
}

// CandidateStore — хранилище кандидатов, потокобезопасное.
type CandidateStore struct {
	mu            sync.Mutex
	candidates    []Candidate
	nextID        int
	maxCandidates int
}

func NewCandidateStore() *CandidateStore {
	return &CandidateStore{
		candidates:    make([]Candidate, 0, 32),
		nextID:        1,
		maxCandidates: 12,
	}
}

// driftToConfidence превращает дрейф в уверенность 0..1: чем меньше дрейф, тем выше.
func driftToConfidence(driftF, driftAmp float64) float64 {
	af := math.Abs(driftF)
	aa := math.Abs(driftAmp)
	x := 1.0 + af/30.0 + aa/15.0
	c := 1.0 / x
	return clamp(c, 0, 1)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (s *CandidateStore) List() []Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Candidate, len(s.candidates))
	copy(out, s.candidates)
	return out
}

// peakInfo — пик из FFT для кластеризации.
type PeakInfo struct {
	BinIdx  int
	Freq    float64
	AmpRaw  float64
	AmpNorm float64
	ShapeXY float64
	ShapeXZ float64
}

// findTopKPeaksInRange возвращает до K пиков в [freqMinHz, freqMaxHz], исключая DC. Сортировка по AmpNorm по убыванию.
// Используется findTopKPeaksPerBand с полным диапазоном полос (0.2–20k Гц), чтобы B6/B7 не теряли пики.
func FindTopKPeaksInRange(magX, magY, magZ, freqs []float64, n int, fs float64, freqMinHz, freqMaxHz float64, k int) ([]PeakInfo, float64) {
	if len(magX) < 2 || len(freqs) < 2 || k <= 0 || freqMaxHz <= freqMinHz {
		return nil, 0
	}
	minBin := 1
	for i := 1; i < len(freqs); i++ {
		if freqs[i] >= freqMinHz {
			minBin = i
			break
		}
	}
	maxBin := len(magX) - 1
	for i := len(freqs) - 1; i >= 0; i-- {
		if freqs[i] <= freqMaxHz {
			maxBin = i
			break
		}
	}
	if minBin >= maxBin {
		return nil, 0
	}
	sliceMag := make([]float64, 0, maxBin-minBin+1)
	for i := minBin; i <= maxBin; i++ {
		sliceMag = append(sliceMag, magX[i])
	}
	sort.Float64s(sliceMag)
	medianAmp := 1e-12
	if len(sliceMag) > 0 {
		medianAmp = sliceMag[len(sliceMag)/2]
	}
	if medianAmp < 1e-12 {
		medianAmp = 1e-12
	}
	var peaks []PeakInfo
	for i := minBin; i <= maxBin; i++ {
		mx := magX[i]
		ampNorm := mx / medianAmp
		my := 1e-12
		mz := 1e-12
		if i < len(magY) {
			my = magY[i]
		}
		if i < len(magZ) {
			mz = magZ[i]
		}
		if my < 1e-12 {
			my = 1e-12
		}
		if mz < 1e-12 {
			mz = 1e-12
		}
		shapeXY := mx / my
		shapeXZ := mx / mz
		peaks = append(peaks, PeakInfo{
			BinIdx:  i,
			Freq:    freqs[i],
			AmpRaw:  mx,
			AmpNorm: ampNorm,
			ShapeXY: shapeXY,
			ShapeXZ: shapeXZ,
		})
	}
	sort.Slice(peaks, func(a, b int) bool { return peaks[a].AmpNorm > peaks[b].AmpNorm })
	out := make([]PeakInfo, 0, k)
	for i := 0; i < len(peaks) && len(out) < k; i++ {
		if peaks[i].AmpNorm >= ampNormThreshold {
			out = append(out, peaks[i])
		}
	}
	return out, medianAmp
}

// findTopKPeaks возвращает до K пиков в [freqAnalysisMinHz, freqAnalysisMaxHz] (0.2–2000 Гц для CandidateStore).
func FindTopKPeaks(magX, magY, magZ, freqs []float64, n int, fs float64, k int) ([]PeakInfo, float64) {
	return FindTopKPeaksInRange(magX, magY, magZ, freqs, n, fs, freqAnalysisMinHz, freqAnalysisMaxHz, k)
}

// findTopKPeaksPerBand возвращает по kPerBand сильнейших пиков в каждой полосе BandsFilterbank[0..numBands-1].
// Так каждая полоса получает свой топ, а не общий — B1, B2, B3 и т.д. не теряются из-за доминанты B0/B4.
func FindTopKPeaksPerBand(magX, magY, magZ, freqs []float64, n int, fs float64, numBands int, kPerBand int) ([]PeakInfo, float64) {
	if len(magX) < 2 || len(freqs) < 2 || numBands <= 0 || kPerBand <= 0 {
		return nil, 0
	}
	if numBands > len(domainsignal.BandsFilterbank) {
		numBands = len(domainsignal.BandsFilterbank)
	}
	// Все пики в полном диапазоне полос 0.2–20k Гц (иначе B6/B7 не получают пиков из-за лимита 2k в findTopKPeaks).
	const pipelineFreqMaxHz = 20000.0
	allPeaks, medianAmp := FindTopKPeaksInRange(magX, magY, magZ, freqs, n, fs, freqAnalysisMinHz, pipelineFreqMaxHz, 500)
	if len(allPeaks) == 0 {
		return nil, medianAmp
	}
	// По полосам: свой топ в каждом диапазоне. Границы [lo, hi) — верхняя исключена, чтобы одна частота не попадала в две полосы.
	var out []PeakInfo
	for band := 0; band < numBands; band++ {
		lo := domainsignal.BandsFilterbank[band].LoHz
		hi := domainsignal.BandsFilterbank[band].HiHz
		var inBand []PeakInfo
		for _, p := range allPeaks {
			if p.Freq < lo || p.AmpNorm < ampNormThreshold {
				continue
			}
			inBandHere := (band < numBands-1 && p.Freq < hi) || (band == numBands-1 && p.Freq <= hi)
			if inBandHere {
				inBand = append(inBand, p)
			}
		}
		sort.Slice(inBand, func(a, b int) bool { return inBand[a].AmpNorm > inBand[b].AmpNorm })
		for i := 0; i < kPerBand && i < len(inBand); i++ {
			out = append(out, inBand[i])
		}
	}
	return out, medianAmp
}

// UpdateFromFFT обновляет кандидатов по текущему FFT и при необходимости создаёт LOCKED токены.
// Кластеризация по форме (морфология пика): ровные синусы с ровными, хвосты с хвостами, фронты с фронтами — по относительности формы, не по уровню.
// distThr — порог merge по форме; tGlyph — FallbackGlyphDistThr при пустой форме.
// x — полный буфер X (для окна W и glyph только в момент LOCK). dt — время с прошлого вызова (сек).
func (s *CandidateStore) UpdateFromFFT(
	magX, magY, magZ, freqs []float64,
	x []float64, fs float64, n int, now time.Time,
	dt float64,
	T_lock float64, deltaFBins int, deltaARel float64,
	windowW, A_cv, A_slope float64,
	distThr, tShape, tGlyph float64,
	patternStore *domainalphabet.PatternStore,
) {
	if patternStore == nil || n < 256 {
		return
	}
	peaks, _ := FindTopKPeaks(magX, magY, magZ, freqs, n, fs, topKPeaks)
	if len(peaks) == 0 {
		return
	}
	if windowW <= 0 {
		windowW = windowWDefault
	}
	if A_cv <= 0 {
		A_cv = aCvDefault
	}
	if A_slope <= 0 {
		A_slope = aSlopeDefault
	}

	binWidth := fs / float64(n)
	if binWidth < 1e-6 {
		binWidth = 1
	}
	// Окно для стационарности и глифа: последние W секунд из x
	Nwin := int(fs * windowW)
	if Nwin < 128 {
		Nwin = 128
	}
	if Nwin > len(x) {
		Nwin = len(x)
	}
	winStart := len(x) - Nwin
	if winStart < 0 {
		winStart = 0
		Nwin = len(x)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	nowMs := now.UnixMilli()

	// Обновить существующих кандидатов: для каждого пика ищем ближайшего кандидата по частоте (в пределах deltaFCandidate бинов)
	usedPeak := make([]bool, len(peaks))
	for i := range s.candidates {
		c := &s.candidates[i]
		bestJ := -1
		bestDf := 1e9
		for j, pk := range peaks {
			if usedPeak[j] {
				continue
			}
			dfBins := math.Abs(pk.Freq-c.FEst) / binWidth
			if dfBins <= float64(deltaFCandidate) && dfBins < bestDf {
				bestDf = dfBins
				bestJ = j
			}
		}
		if bestJ >= 0 {
			pk := peaks[bestJ]
			usedPeak[bestJ] = true
			// Дрейф Δf/dt, Δamp_norm/dt (по времени между обновлениями кандидата)
			if c.LastTs > 0 {
				dtDrift := (float64(nowMs-c.LastTs) / 1000.0)
				if dtDrift > 1e-6 {
					c.DriftF = (pk.Freq - c.LastFreq) / dtDrift
					c.DriftAmpNorm = (pk.AmpNorm - c.LastAmpNorm) / dtDrift
				}
			}
			c.LastFreq = pk.Freq
			c.LastAmpNorm = pk.AmpNorm
			c.LastTs = nowMs

			// 1) Допуски по частоте и амплитуде (Δf_lock, ΔA_lock)
			dfBins := math.Abs(pk.Freq-c.FEst) / binWidth
			dA := 0.0
			if c.AmpNormEst > 1e-12 {
				dA = math.Abs(pk.AmpNorm-c.AmpNormEst) / c.AmpNormEst
			}
			freqAmpOK := dfBins <= float64(deltaFBins) && dA <= deltaARel

			// 2) Стационарность окна: огибающая env вокруг f0 — cv и slope
			envOK := false
			if Nwin >= 16 && c.FEst >= freqAnalysisMinHz && c.FEst <= freqAnalysisMaxHz {
				env := ComputeEnvelope(x, winStart, Nwin, fs, c.FEst)
				if len(env) >= 8 {
					envOK = StationarityOK(env, fs, A_cv, A_slope)
				}
			}

			// Hold растёт только если и допуски, и стационарность окна выполнены
			if freqAmpOK && envOK {
				c.StableTime += dt
			} else {
				c.StableTime = 0
			}
			if T_lock > 0 {
				c.StabilityProgress = math.Min(1, c.StableTime/T_lock)
			} else {
				c.StabilityProgress = 0
			}

			// Lock: только при стационарном окне и stable_time >= T_lock; глиф = только из этого окна W.
			// DISCOVERY: один глиф на наблюдение; merge только при повторном наблюдении того же пика (тот же бин, та же форма, без инверсии).
			if T_lock > 0 && c.StableTime >= T_lock && c.FEst >= minFreqHz && envOK {
				deltaFHz := float64(deltaFMergeBins) * binWidth
				if deltaFHz < 1e-6 {
					deltaFHz = 1e-6
				}
				// Только тот же FFT-бин = тот же пик; разные частоты = разные глифы.
				sameBinHz := binWidth * 0.5
				if sameBinHz < 1e-6 {
					sameBinHz = deltaFHz
				}
				confidence := driftToConfidence(c.DriftF, c.DriftAmpNorm)
				glyphData := ComputeFrozenGlyphFromWindow(x, fs, c.FEst, windowW)
				shape := ExtractWaveShapeAtFreq(x, winStart, Nwin, fs, c.FEst, 4)
				envSlope := 0.0
				meanEnv := 1.0
				if env := ComputeEnvelope(x, winStart, Nwin, fs, c.FEst); len(env) >= 8 {
					envSlope = EnvelopeSlopePerSec(env, fs)
					sumEnv := 0.0
					for _, v := range env {
						sumEnv += v
					}
					meanEnv = sumEnv / float64(len(env))
				}
				shapeSig := ComputeShapeSignature(shape, envSlope, meanEnv)
				peakProf := PeakProfileFromShape(shape)
				p := domainalphabet.Pattern{
					Freq:          c.FEst,
					Amp:           c.AmpNormEst,
					RatioXY:       c.ShapeXYEst,
					RatioXZ:       c.ShapeXZEst,
					Density:       clamp(c.AmpNormEst/3, 0, 1),
					Captured:      nowMs,
					Confidence:    confidence,
					ShapeSig:      shapeSig,
					GlyphData:     glyphData,
					GlyphShape:    shape,
					EnvelopeSlope: envSlope,
					PeakProfile:   peakProf,
				}
				maxDist := distThr
				if maxDist <= 0 {
					maxDist = 0.65
				}
				patternStore.MergeOrAddByShape(p, domainalphabet.ShapeMergeParams{MinCosForMerge: 0, MaxDistForMerge: maxDist})
				c.StableTime = 0
				c.StabilityProgress = 0
			}

			// EMA после проверки стабильности
			c.FEst = (1-emaAlpha)*c.FEst + emaAlpha*pk.Freq
			c.AmpNormEst = (1-emaAlpha)*c.AmpNormEst + emaAlpha*pk.AmpNorm
			c.ShapeXYEst = (1-emaAlpha)*c.ShapeXYEst + emaAlpha*pk.ShapeXY
			c.ShapeXZEst = (1-emaAlpha)*c.ShapeXZEst + emaAlpha*pk.ShapeXZ
			c.LastSeenTs = nowMs
			c.HitCount++
		} else {
			// Кандидат не совпал с пиком — сбрасываем стабильность (дрейф не трогаем)
			c.StableTime = 0
			c.StabilityProgress = 0
		}
	}

	// Новые кандидаты из неиспользованных пиков
	for j, pk := range peaks {
		if usedPeak[j] {
			continue
		}
		// Проверяем, нет ли уже кандидата близко по частоте (при добавлении нового)
		found := false
		for i := range s.candidates {
			dfBins := math.Abs(pk.Freq-s.candidates[i].FEst) / binWidth
			if dfBins <= float64(deltaFCandidate) {
				found = true
				break
			}
		}
		if found {
			continue
		}
		if len(s.candidates) >= s.maxCandidates {
			// Удалить самый старый по LastSeenTs
			oldest := 0
			for i := 1; i < len(s.candidates); i++ {
				if s.candidates[i].LastSeenTs < s.candidates[oldest].LastSeenTs {
					oldest = i
				}
			}
			s.candidates[oldest] = s.candidates[len(s.candidates)-1]
			s.candidates = s.candidates[:len(s.candidates)-1]
		}
		s.candidates = append(s.candidates, Candidate{
			ID:          s.nextID,
			FEst:        pk.Freq,
			AmpNormEst:  pk.AmpNorm,
			ShapeXYEst:  pk.ShapeXY,
			ShapeXZEst:  pk.ShapeXZ,
			LastSeenTs:  nowMs,
			HitCount:    1,
			LastFreq:    pk.Freq,
			LastAmpNorm: pk.AmpNorm,
			LastTs:      nowMs,
		})
		s.nextID++
	}

	// Удалить кандидатов, которых давно не видели (например > 2 сек)
	cutoff := nowMs - 2000
	newList := s.candidates[:0]
	for _, c := range s.candidates {
		if c.LastSeenTs >= cutoff {
			newList = append(newList, c)
		}
	}
	s.candidates = newList
}
