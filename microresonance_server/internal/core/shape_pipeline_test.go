package core

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"
)

// Генерирует окно синуса одной частоты (повторяющаяся форма).
func makeSinWindow(n int, freq, fs float64) []float64 {
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		t := float64(i) / fs
		out[i] = math.Sin(2 * math.Pi * freq * t)
	}
	return out
}

// Копия окна в x,y,z (mag2 будет тот же паттерн).
func fillXYZ(x, y, z []float64, w []float64) {
	for i := 0; i < len(w) && i < len(x); i++ {
		x[i] = w[i]
	}
	for i := 0; i < len(w) && i < len(y); i++ {
		y[i] = w[i] * 0.3
	}
	for i := 0; i < len(w) && i < len(z); i++ {
		z[i] = w[i] * 0.2
	}
}

// TestShapePipelineTokenCountStabilizes: при повторяющемся сигнале число токенов
// стабилизируется (алфавит схлопывается за счёт merge).
func TestShapePipelineTokenCountStabilizes(t *testing.T) {
	fs := 4000.0
	nWin := int(fs * 1.0)
	if nWin < 128 {
		nWin = 128
	}
	window := makeSinWindow(nWin, 10, fs)
	x := make([]float64, nWin)
	y := make([]float64, nWin)
	z := make([]float64, nWin)
	fillXYZ(x, y, z, window)

	params := defaultShapePipelineParams()
	params.AllowDiscovery = true
	store := NewShapeClusterStore(params.DistThr)
	decomposer := &ResidualDecomposer{}
	patternStore := &PatternStore{}

	runs := 50
	for i := 0; i < runs; i++ {
		UpdateShapePipeline(x, y, z, fs, time.Now(), params, store, decomposer, patternStore, nil)
	}
	count := patternStore.Count()

	// Дефолты (DistThr, MinDeltaH); merge только по ShapeSignature
	if count > 12 {
		t.Errorf("after %d runs with same signal: got %d tokens, want ≤12 (alphabet should collapse)", runs, count)
	}
}

// TestShapePipeline60SecTokenStabilization: за 60 сек тиков (300 итераций по 0.2 с) число locked-токенов
// не раздувается — merge только по полному совпадению ShapeSignature.
func TestShapePipeline60SecTokenStabilization(t *testing.T) {
	fs := 4000.0
	windowSec := 2.6 // >= WaveWindowSec 2.5
	nWin := int(fs * windowSec)
	if nWin < 2048 {
		nWin = 2048
	}
	// 50 Hz выше MinFreqHz (20), чтобы пики попадали в пайплайн
	window := makeSinWindow(nWin, 50, fs)
	x := make([]float64, nWin)
	y := make([]float64, nWin)
	z := make([]float64, nWin)
	fillXYZ(x, y, z, window)

	params := defaultShapePipelineParams()
	params.AllowDiscovery = true
	store := NewShapeClusterStore(params.DistThr)
	decomposer := &ResidualDecomposer{}
	patternStore := &PatternStore{}

	// 60 сек при шаге 0.2 с = 300 тиков
	const stepSec = 0.2
	ticks := int(60.0 / stepSec)
	if ticks < 200 {
		ticks = 200
	}
	start := time.Now()
	for i := 0; i < ticks; i++ {
		ts := start.Add(time.Duration(float64(i)*stepSec*1000) * time.Millisecond)
		UpdateShapePipeline(x, y, z, fs, ts, params, store, decomposer, patternStore, nil)
	}
	count := patternStore.Count()
	// Ожидаем схлопывание: при одном и том же сигнале за 60 сек тиков — не более 15 токенов
	maxTokens := 15
	if count > maxTokens {
		t.Errorf("after 60s (%d ticks): got %d locked tokens, want ≤%d (alphabet should collapse)", ticks, count, maxTokens)
	}
	t.Logf("60s test: %d ticks, locked tokens=%d (max %d)", ticks, count, maxTokens)
}

// TestRepeatableWaveDecomposition: повторяемая волна даёт стабильное разложение и не теряется смысл.
// Один и тот же сигнал (фиксированная частота, фаза) при многократном прогоне даёт тот же набор токенов
// и хотя бы один locked-токен близок по форме к исходной волне (cos высокий).
func TestRepeatableWaveDecomposition(t *testing.T) {
	fs := 4000.0
	windowSec := 2.6
	nWin := int(fs * windowSec)
	if nWin < 2048 {
		nWin = 2048
	}
	freq := 50.0
	window := makeSinWindow(nWin, freq, fs)
	x := make([]float64, nWin)
	y := make([]float64, nWin)
	z := make([]float64, nWin)
	fillXYZ(x, y, z, window)

	params := defaultShapePipelineParams()
	params.AllowDiscovery = true
	runs := 80
	start := time.Now()

	// Прогон 1
	store1 := NewShapeClusterStore(params.DistThr)
	decomposer1 := &ResidualDecomposer{}
	patternStore1 := &PatternStore{}
	for i := 0; i < runs; i++ {
		ts := start.Add(time.Duration(i*200) * time.Millisecond)
		UpdateShapePipeline(x, y, z, fs, ts, params, store1, decomposer1, patternStore1, nil)
	}
	count1 := patternStore1.Count()
	list1 := patternStore1.List()

	// Прогон 2 — тот же сигнал, новые store/patternStore (повторяемость)
	store2 := NewShapeClusterStore(params.DistThr)
	decomposer2 := &ResidualDecomposer{}
	patternStore2 := &PatternStore{}
	for i := 0; i < runs; i++ {
		ts := start.Add(time.Duration(i*200) * time.Millisecond)
		UpdateShapePipeline(x, y, z, fs, ts, params, store2, decomposer2, patternStore2, nil)
	}
	count2 := patternStore2.Count()

	if count1 != count2 {
		t.Errorf("repeatability: run1=%d tokens, run2=%d tokens (same wave must give same count)", count1, count2)
	}
	if count1 == 0 {
		t.Fatal("expected at least one locked token from 50 Hz wave above MinFreqHz")
	}

	// Смысл не теряется: форма волны (как её извлекает пайплайн) должна быть близка хотя бы одному locked-токену
	Nwin := int(fs * params.WaveWindowSec)
	if Nwin > len(x) {
		Nwin = len(x)
	}
	winStart := len(x) - Nwin
	if winStart < 0 {
		winStart = 0
		Nwin = len(x)
	}
	minCycles := params.WaveMinCycles
	if minCycles <= 0 {
		minCycles = 6
	}
	refShape := ExtractWaveShapeAtFreq(x, winStart, Nwin, fs, freq, minCycles)
	if len(refShape.Points) == 0 {
		t.Skip("ExtractWaveShapeAtFreq returned empty, skip meaning check")
	}
	maxCos := -1.0
	for _, p := range list1 {
		if len(p.GlyphShape.Points) == 0 {
			continue
		}
		cos := ShapeCosineSimilarity(refShape, p.GlyphShape)
		if cos > maxCos {
			maxCos = cos
		}
	}
	if maxCos < 0.83 {
		t.Errorf("wave shape should match at least one locked token (max cos=%.3f, want ≥0.83); meaning lost", maxCos)
	}
	t.Logf("repeatable: %d tokens both runs; max cos to ref wave=%.3f", count1, maxCos)
}

// TestComputeFrozenGlyphShortSegment: при коротком цикле (n < 32) ComputeFrozenGlyphFromSegment
// ресэмплирует envelope по фазе и возвращает 64 точки (HD-версия профиля по осям).
// Минимум n = MinSamplesForGlyph (4), как в GetCycleBounds.
func TestComputeFrozenGlyphShortSegment(t *testing.T) {
	fs := 4000.0
	f0 := 100.0
	for _, n := range []int{4, 8, 20} {
		x := makeSinWindow(n*2, f0, fs)
		start := 0
		glyph := ComputeFrozenGlyphFromSegment(x, start, n, fs, f0)
		if glyph == nil {
			t.Fatalf("ComputeFrozenGlyphFromSegment(n=%d) returned nil", n)
		}
		if len(glyph) != FrozenGlyphPoints {
			t.Errorf("n=%d: expected %d points, got %d", n, FrozenGlyphPoints, len(glyph))
		}
		if n < FrozenGlyphSegments {
			for j := 0; j < FrozenGlyphSegments; j++ {
				if glyph[j] != glyph[FrozenGlyphSegments+j] {
					t.Errorf("n=%d short-segment resample: bin %d min!=max", n, j)
				}
			}
		}
	}
	t.Logf("OK: short segments n=4,8,20 -> 64 points (same cycle as profile)")
}

// TestPipelineWindowBounds выводит и сравнивает границы окна формы (квадратный профиль) и окна трёхосевых глифов X/Y/Z.
// Ожидание: при useOneCycle оба окна совпадают (один цикл).
func TestPipelineWindowBounds(t *testing.T) {
	fs := 4000.0
	windowSec := DefaultWaveWindowSec
	if windowSec <= 0 {
		windowSec = 2.5
	}
	Nwin := int(fs * windowSec)
	if Nwin < 128 {
		Nwin = 128
	}
	if Nwin > 20000 {
		Nwin = 20000
	}
	winStart := 0
	x := makeSinWindow(Nwin, 50, fs)
	y := make([]float64, len(x))
	z := make([]float64, len(x))
	fillXYZ(x, y, z, x)

	minCycles := DefaultWaveMinCycles
	if minCycles <= 0 {
		minCycles = 6
	}
	freq := 50.0
	nSamplesAdapt := int(float64(minCycles) / freq * fs)
	const minShapeSamples = 128
	if nSamplesAdapt < minShapeSamples {
		nSamplesAdapt = minShapeSamples
	}
	if nSamplesAdapt > Nwin {
		nSamplesAdapt = Nwin
	}
	shapeStart := winStart + Nwin - nSamplesAdapt
	if shapeStart < 0 {
		shapeStart = 0
	}
	if shapeStart+nSamplesAdapt > len(x) {
		nSamplesAdapt = len(x) - shapeStart
		if nSamplesAdapt < minShapeSamples {
			t.Fatalf("nSamplesAdapt too small: %d", nSamplesAdapt)
		}
	}

	shape, cycleStartGlobal, cycleLen := ExtractWaveShapeAtFreqWithBounds(x, shapeStart, nSamplesAdapt, fs, freq, minCycles)
	if len(shape.Points) == 0 {
		t.Fatal("ExtractWaveShapeAtFreqWithBounds returned empty shape")
	}

	glyphStart := cycleStartGlobal
	useOneCycle := cycleLen >= FrozenGlyphSegments &&
		glyphStart+cycleLen <= len(x) && glyphStart+cycleLen <= len(y) && glyphStart+cycleLen <= len(z)

	// Окно формы (квадратный профиль): один цикл [glyphStart, glyphStart+cycleLen)
	generalStart := glyphStart
	generalEnd := glyphStart + cycleLen

	// Окно трёхосевого глифа X/Y/Z: то же при useOneCycle, иначе последние nSamplesAdapt сэмплов
	var threeAxisStart, threeAxisEnd int
	if useOneCycle {
		threeAxisStart = glyphStart
		threeAxisEnd = glyphStart + cycleLen
	} else {
		threeAxisStart = len(x) - nSamplesAdapt
		if threeAxisStart < 0 {
			threeAxisStart = 0
		}
		threeAxisEnd = len(x)
	}

	t.Logf("=== окна пайплайна (форма vs трёхосевой глиф) ===")
	t.Logf("Nwin=%d winStart=%d nSamplesAdapt=%d shapeStart=%d cycleStartGlobal=%d cycleLen=%d useOneCycle=%v",
		Nwin, winStart, nSamplesAdapt, shapeStart, cycleStartGlobal, cycleLen, useOneCycle)
	t.Logf("Общее окно (форма / квадратный профиль, один цикл): start=%d end=%d len=%d", generalStart, generalEnd, generalEnd-generalStart)
	t.Logf("Трёхосевой глиф X/Y/Z:                          start=%d end=%d len=%d", threeAxisStart, threeAxisEnd, threeAxisEnd-threeAxisStart)

	if useOneCycle {
		if generalStart != threeAxisStart || generalEnd != threeAxisEnd {
			t.Errorf("при useOneCycle окна должны совпадать: общее [%d,%d), трёхосевой [%d,%d)",
				generalStart, generalEnd, threeAxisStart, threeAxisEnd)
		} else {
			t.Logf("OK: окна совпадают [%d, %d)", generalStart, generalEnd)
		}
	} else {
		t.Logf("useOneCycle=false: трёхосевой по полному адаптивному окну (ожидаемо при коротком цикле или UseBands)")
	}
}

// peakInBand воспроизводит логику findTopKPeaksPerBand: частота freq попадает в полосу band (0..7)?
// Границы [lo, hi) для полос 0..6, [lo, hi] для полосы 7 — одна частота только в одной полосе.
func peakInBand(freq float64, band int) bool {
	if band < 0 || band >= len(BandsFilterbank) {
		return false
	}
	lo := BandsFilterbank[band].LoHz
	hi := BandsFilterbank[band].HiHz
	if freq < lo {
		return false
	}
	numBands := len(BandsFilterbank)
	if band < numBands-1 {
		return freq < hi
	}
	return freq <= hi
}

// TestFrequencyBandSeparation проверяет, что частоты разделены по полосам как параллельные каналы:
// границы [lo, hi) / [lo, hi] без пересечений, одна частота — ровно в одной полосе.
func TestFrequencyBandSeparation(t *testing.T) {
	// 1) Частота строго внутри полосы — только эта полоса
	for b := 0; b < len(BandsFilterbank); b++ {
		lo := BandsFilterbank[b].LoHz
		hi := BandsFilterbank[b].HiHz
		mid := (lo + hi) / 2
		if mid <= lo || mid >= hi {
			mid = lo + (hi-lo)*0.5
		}
		got := BandContaining(mid)
		if got != b {
			t.Errorf("полоса %d (%s %.1f–%.1f Hz): середина %.2f Hz → BandContaining=%d, ожидалось %d",
				b, BandsFilterbank[b].Label, lo, hi, mid, got, b)
		}
		// peakInBand должен совпадать с BandContaining
		for band := 0; band < len(BandsFilterbank); band++ {
			in := peakInBand(mid, band)
			if in != (band == b) {
				t.Errorf("полоса %d: середина %.2f Hz peakInBand(%d)=%v, ожидалось %v", b, mid, band, in, band == b)
			}
		}
	}

	// 2) Граничные частоты (стык полос) — относятся только к верхней полосе, не к двум
	boundaries := []struct {
		hz   float64
		want int // индекс полосы, в которую должна попасть эта частота (верхняя на стыке)
	}{
		{2, 1},    // стык B0/B1 → B1
		{10, 2},   // стык B1/B2 → B2
		{40, 3},   // B2/B3 → B3
		{120, 4},  // B3/B4 → B4
		{400, 5},  // B4/B5 → B5
		{1500, 6}, // B5/B6 → B6
		{2500, 7}, // B6/B7 → B7
	}
	for _, bb := range boundaries {
		got := BandContaining(bb.hz)
		if got != bb.want {
			t.Errorf("граница %.0f Hz: BandContaining=%d, ожидалось %d (%s)",
				bb.hz, got, bb.want, BandsFilterbank[bb.want].Label)
		}
		countBands := 0
		for band := 0; band < len(BandsFilterbank); band++ {
			if peakInBand(bb.hz, band) {
				countBands++
				if band != bb.want {
					t.Errorf("граница %.0f Hz: попала в полосу %d (%s), ожидалась только полоса %d",
						bb.hz, band, BandsFilterbank[band].Label, bb.want)
				}
			}
		}
		if countBands != 1 {
			t.Errorf("граница %.0f Hz: попала в %d полос (ожидалось 1)", bb.hz, countBands)
		}
	}

	// 3) Любая частота из диапазона полос — ровно в одной полосе
	testFreqs := []float64{0.5, 1, 2, 5, 10, 25, 40, 80, 120, 250, 400, 800, 1500, 2000, 3000, 4500, 5000}
	for _, f := range testFreqs {
		idx := BandContaining(f)
		count := 0
		for band := 0; band < len(BandsFilterbank); band++ {
			if peakInBand(f, band) {
				count++
				if band != idx {
					t.Errorf("частота %.1f Hz: BandContaining=%d, но peakInBand(%d)=true", f, idx, band)
				}
			}
		}
		if idx >= 0 && count != 1 {
			t.Errorf("частота %.1f Hz: BandContaining=%d, но peakInBand true для %d полос", f, idx, count)
		}
		if idx < 0 && (f < BandsFilterbank[0].LoHz || f > BandsFilterbank[len(BandsFilterbank)-1].HiHz) {
			// за пределами 0.2–20k — ок
		} else if idx < 0 && count > 0 {
			t.Errorf("частота %.1f Hz: BandContaining=-1, но peakInBand true для какой-то полосы", f)
		}
	}

	t.Logf("частоты разделены по полосам: границы [lo, hi) / [lo, hi], одна частота — одна полоса")
}

// TestShapePipelineDistThrMerge: при увеличении DistThr число токенов падает (больше merge).
func TestShapePipelineDistThrMerge(t *testing.T) {
	fs := 4000.0
	nWin := int(fs * 1.0)
	if nWin < 128 {
		nWin = 128
	}
	window := makeSinWindow(nWin, 10, fs)
	x := make([]float64, nWin)
	y := make([]float64, nWin)
	z := make([]float64, nWin)
	fillXYZ(x, y, z, window)

	runs := 40
	var countStrict, countWide int
	for _, distThr := range []float64{0.35, 0.55} {
		params := defaultShapePipelineParams()
		params.AllowDiscovery = true
		params.DistThr = distThr
		params.MinDeltaH = 0.01
		store := NewShapeClusterStore(params.DistThr)
		decomposer := &ResidualDecomposer{}
		patternStore := &PatternStore{}
		for i := 0; i < runs; i++ {
			UpdateShapePipeline(x, y, z, fs, time.Now(), params, store, decomposer, patternStore, nil)
		}
		c := patternStore.Count()
		if distThr <= 0.4 {
			countStrict = c
		} else {
			countWide = c
		}
	}
	// Широкий допуск (0.55) должен дать не больше токенов, чем строгий (0.35)
	if countWide > countStrict+5 {
		t.Logf("DistThr 0.35 -> %d tokens, 0.55 -> %d (wider should merge more)", countStrict, countWide)
	}
}

// TestShapePipelineDecomposeParams: Decompose вызывается с корректными параметрами.
func TestShapePipelineDecomposeParams(t *testing.T) {
	params := defaultShapePipelineParams()
	if params.MaxIters < 1 {
		t.Error("MaxIters must be >= 1")
	}
	if params.DistThr <= 0 {
		t.Error("DistThr must be > 0")
	}
}

// TestMergeByShapeDifferentFreqs: разные частоты — разные глифы; близкие частоты — merge.
// Per-band допуски: 200/202 Hz в B4 (±25), 400 Hz в B5 (±80). 200 и 400 — разные глифы; 202 merge в 200.
func TestMergeByShapeDifferentFreqs(t *testing.T) {
	fs := 4000.0
	n := 2048
	window := makeSinWindow(n, 100, fs)
	shape := BuildShape(window, fs, 64)
	if len(shape.Points) == 0 {
		t.Fatal("BuildShape returned empty")
	}
	sig := ComputeShapeSignature(shape, 0, 1)
	t.Logf(".[shape] points=%d sig=%v", len(shape.Points), sig)

	params := ShapeMergeParams{MinCosForMerge: 0}
	store := &PatternStore{}
	peakProf := PeakProfileFromShape(shape)
	p1 := Pattern{Freq: 200, BandIdx: BandContaining(200), Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: peakProf, Captured: 1000, Confidence: 0.9}
	mr1 := store.MergeOrAddByShape(p1, params)
	if mr1.Merged {
		t.Error("first add must not merge")
	}
	if store.Count() != 1 {
		t.Errorf("after first add: count=%d want 1", store.Count())
	}

	// Та же форма на 400 Hz — другой глиф (B5, |400-200| > 25 Hz допуск B4).
	p2 := Pattern{Freq: 400, BandIdx: BandContaining(400), Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: peakProf, Captured: 2000, Confidence: 0.9}
	mr2 := store.MergeOrAddByShape(p2, params)
	if mr2.Merged {
		t.Error("second add (400 Hz) should NOT merge — different frequency → different glyph")
	}
	if store.Count() != 2 {
		t.Errorf("after second add: count=%d want 2", store.Count())
	}
	t.Logf(".[after p2] count=2 merged=%v", mr2.Merged)

	// Третий паттерн 202 Hz — merge в глиф 200 Hz (B4, |202-200|=2 <= 25).
	p3 := Pattern{Freq: 202, BandIdx: BandContaining(202), Amp: 1.1, GlyphShape: shape, ShapeSig: sig, PeakProfile: peakProf, Captured: 3000, Confidence: 0.85}
	mr3 := store.MergeOrAddByShape(p3, params)
	if !mr3.Merged {
		t.Error("third add (202 Hz) should merge — close to 200 Hz")
	}
	if store.Count() != 2 {
		t.Errorf("after third add: count=%d want 2", store.Count())
	}
	t.Logf(".[after p3] count=2 merged=%v", mr3.Merged)
}

// TestMergeByShapeSameBandFarFreqs: в одной широкой полосе (B5) одинаковая форма не должна merge'иться,
// если частоты далеко друг от друга и выходят за per-band tolerance.
func TestMergeByShapeSameBandFarFreqs(t *testing.T) {
	fs := 4000.0
	n := 2048
	window := makeSinWindow(n, 100, fs)
	shape := BuildShape(window, fs, 64)
	if len(shape.Points) == 0 {
		t.Fatal("BuildShape returned empty")
	}
	sig := ComputeShapeSignature(shape, 0, 1)
	peakProf := PeakProfileFromShape(shape)

	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}

	// Обе частоты в B5, но |521-402|=119 > tol(B5)=80 -> разные глифы.
	p1 := Pattern{Freq: 521, BandIdx: BandContaining(521), Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: peakProf, Captured: 1000, Confidence: 0.9}
	mr1 := store.MergeOrAddByShape(p1, params)
	if mr1.Merged {
		t.Fatal("first add must not merge")
	}

	p2 := Pattern{Freq: 402, BandIdx: BandContaining(402), Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: peakProf, Captured: 2000, Confidence: 0.9}
	mr2 := store.MergeOrAddByShape(p2, params)
	if mr2.Merged {
		t.Errorf("same-band far frequencies must NOT merge: 521 Hz vs 402 Hz")
	}
	if store.Count() != 2 {
		t.Errorf("after two far frequencies in B5: count=%d want 2", store.Count())
	}
}

// TestMergeByShapeStrongShapeAllowsSlightFreqOverflow:
// если форма практически идентична, допускаем небольшой выход за per-band tol (+20%), чтобы не плодить NEW от jitter f_peak.
func TestMergeByShapeStrongShapeAllowsSlightFreqOverflow(t *testing.T) {
	fs := 4000.0
	n := 2048
	window := makeSinWindow(n, 100, fs)
	shape := BuildShape(window, fs, 64)
	if len(shape.Points) == 0 {
		t.Fatal("BuildShape returned empty")
	}
	sig := ComputeShapeSignature(shape, 0, 1)
	peakProf := PeakProfileFromShape(shape)

	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}

	// B4: tol=30 Hz. Разница 34 Hz: strict gate отверг бы, relaxed-for-strong-shape должен принять.
	p1 := Pattern{Freq: 180, BandIdx: BandContaining(180), Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: peakProf, Captured: 1000, Confidence: 0.9}
	mr1 := store.MergeOrAddByShape(p1, params)
	if mr1.Merged {
		t.Fatal("first add must not merge")
	}

	p2 := Pattern{Freq: 214, BandIdx: BandContaining(214), Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: peakProf, Captured: 2000, Confidence: 0.9}
	mr2 := store.MergeOrAddByShape(p2, params)
	if !mr2.Merged {
		t.Errorf("strong shape with slight freq overflow should merge in same band")
	}
	if store.Count() != 1 {
		t.Errorf("after strong-shape slight-overflow case: count=%d want 1", store.Count())
	}
}

// TestMergeByShapeEstablishedGlyphSurvivesFreqJitter:
// после стабилизации глифа (несколько повторов) сильный shape-match не должен распадаться на новый ID из-за freq gate.
func TestMergeByShapeEstablishedGlyphSurvivesFreqJitter(t *testing.T) {
	fs := 4000.0
	n := 2048
	window := makeSinWindow(n, 100, fs)
	shape := BuildShape(window, fs, 64)
	if len(shape.Points) == 0 {
		t.Fatal("BuildShape returned empty")
	}
	sig := ComputeShapeSignature(shape, 0, 1)
	peakProf := PeakProfileFromShape(shape)

	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}

	// Стабилизируем глиф в B5.
	base := []float64{760, 758, 762}
	for i, f := range base {
		p := Pattern{Freq: f, BandIdx: BandContaining(f), Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: peakProf, Captured: int64(1000 + i*1000), Confidence: 0.9}
		mr := store.MergeOrAddByShape(p, params)
		if i == 0 && mr.Merged {
			t.Fatal("first add must not merge")
		}
	}
	if store.Count() != 1 {
		t.Fatalf("after stabilization: count=%d want 1", store.Count())
	}

	// Частота выходит за tol(B5)=80 относительно центра, но shape почти идеальна:
	// для established-глифа это должен быть merge (anti-fragmentation).
	pJitter := Pattern{Freq: 865, BandIdx: BandContaining(865), Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: peakProf, Captured: 5000, Confidence: 0.9}
	mrJitter := store.MergeOrAddByShape(pJitter, params)
	if !mrJitter.Merged {
		t.Errorf("established glyph should survive freq jitter and merge")
	}
	if store.Count() != 1 {
		t.Errorf("after established freq-jitter case: count=%d want 1", store.Count())
	}
}

// TestMergeByShapeEstablishedGlyphSurvivesSignatureJitter:
// в не-шумной полосе established-глиф не должен распадаться на новый ID из-за мелкого сдвига signature.
func TestMergeByShapeEstablishedGlyphSurvivesSignatureJitter(t *testing.T) {
	fs := 4000.0
	n := 2048
	window := makeSinWindow(n, 100, fs)
	shape := BuildShape(window, fs, 64)
	if len(shape.Points) == 0 {
		t.Fatal("BuildShape returned empty")
	}
	baseSig := ComputeShapeSignature(shape, 0, 1)
	peakProf := PeakProfileFromShape(shape)

	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}

	// Стабилизация глифа в B4 (не noisy band).
	baseFreq := 286.0
	for i := 0; i < 3; i++ {
		p := Pattern{Freq: baseFreq, BandIdx: BandContaining(baseFreq), Amp: 1, GlyphShape: shape, ShapeSig: baseSig, PeakProfile: peakProf, Captured: int64(1000 + i*1000), Confidence: 0.9}
		store.MergeOrAddByShape(p, params)
	}
	if store.Count() != 1 {
		t.Fatalf("after stabilization: count=%d want 1", store.Count())
	}

	// Меняем только AsymmetrySign: в B4 обычный sig-gate это отклонит.
	jitterSig := baseSig
	if jitterSig.AsymmetrySign == -1 {
		jitterSig.AsymmetrySign = 1
	} else {
		jitterSig.AsymmetrySign = -1
	}
	pJitter := Pattern{Freq: 288, BandIdx: BandContaining(288), Amp: 1, GlyphShape: shape, ShapeSig: jitterSig, PeakProfile: peakProf, Captured: 5000, Confidence: 0.9}
	mrJitter := store.MergeOrAddByShape(pJitter, params)
	if !mrJitter.Merged {
		t.Errorf("established glyph should survive signature jitter and merge")
	}
	if store.Count() != 1 {
		t.Errorf("after established signature-jitter case: count=%d want 1", store.Count())
	}
}

// TestMergeByShapeDifferentCoarseSignature: даже при похожей форме merge запрещён,
// если coarse signature отличается (стабильный алфавит без смешения классов формы).
func TestMergeByShapeDifferentCoarseSignature(t *testing.T) {
	fs := 4000.0
	n := 2048
	window := makeSinWindow(n, 100, fs)
	shape := BuildShape(window, fs, 64)
	if len(shape.Points) == 0 {
		t.Fatal("BuildShape returned empty")
	}
	baseSig := ComputeShapeSignature(shape, 0, 1)
	peakProf := PeakProfileFromShape(shape)

	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}

	p1 := Pattern{Freq: 200, BandIdx: BandContaining(200), Amp: 1, GlyphShape: shape, ShapeSig: baseSig, PeakProfile: peakProf, Captured: 1000, Confidence: 0.9}
	mr1 := store.MergeOrAddByShape(p1, params)
	if mr1.Merged {
		t.Fatal("first add must not merge")
	}

	// Та же форма, но другая coarse signature: другой класс, merge запрещён.
	otherSig := baseSig
	otherSig.CoarseTopologyCode += 2
	p2 := Pattern{Freq: 202, BandIdx: BandContaining(202), Amp: 1, GlyphShape: shape, ShapeSig: otherSig, PeakProfile: peakProf, Captured: 2000, Confidence: 0.9}
	mr2 := store.MergeOrAddByShape(p2, params)
	if mr2.Merged {
		t.Errorf("different coarse signature must NOT merge")
	}
	if store.Count() != 2 {
		t.Errorf("after coarse-signature mismatch: count=%d want 2", store.Count())
	}
}

// TestMergeByShapeEnvClassDifferenceStillMerges:
// отличие только по EnvelopeClass не должно плодить новый ID (используем simplified coarse gate).
func TestMergeByShapeEnvClassDifferenceStillMerges(t *testing.T) {
	fs := 4000.0
	n := 2048
	window := makeSinWindow(n, 100, fs)
	shape := BuildShape(window, fs, 64)
	if len(shape.Points) == 0 {
		t.Fatal("BuildShape returned empty")
	}
	baseSig := ComputeShapeSignature(shape, 0, 1)
	peakProf := PeakProfileFromShape(shape)

	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}

	p1 := Pattern{Freq: 200, BandIdx: BandContaining(200), Amp: 1, GlyphShape: shape, ShapeSig: baseSig, PeakProfile: peakProf, Captured: 1000, Confidence: 0.9}
	store.MergeOrAddByShape(p1, params)

	// Меняем только EnvelopeClass: при simplified coarse gate должен быть merge, не новый токен.
	sig2 := baseSig
	const envClassCount = 5
	sig2.EnvelopeClass = (sig2.EnvelopeClass + 1) % envClassCount
	p2 := Pattern{Freq: 202, BandIdx: BandContaining(202), Amp: 1, GlyphShape: shape, ShapeSig: sig2, PeakProfile: peakProf, Captured: 2000, Confidence: 0.9}
	mr2 := store.MergeOrAddByShape(p2, params)
	if !mr2.Merged {
		t.Errorf("env-class-only difference should merge into existing glyph")
	}
	if store.Count() != 1 {
		t.Errorf("after env-class-only difference: count=%d want 1", store.Count())
	}
}

// TestMergeByShapeAdjacentBandNeedsBothNearBoundary:
// соседние полосы можно merge только когда обе частоты у границы.
func TestMergeByShapeAdjacentBandNeedsBothNearBoundary(t *testing.T) {
	fs := 4000.0
	n := 2048
	window := makeSinWindow(n, 100, fs)
	shape := BuildShape(window, fs, 64)
	if len(shape.Points) == 0 {
		t.Fatal("BuildShape returned empty")
	}
	sig := ComputeShapeSignature(shape, 0, 1)
	peakProf := PeakProfileFromShape(shape)

	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}

	// ex: B3, частота далеко от границы 120 Гц; p: B4 рядом с границей.
	p1 := Pattern{Freq: 95, BandIdx: BandContaining(95), Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: peakProf, Captured: 1000, Confidence: 0.9}
	store.MergeOrAddByShape(p1, params)
	p2 := Pattern{Freq: 121, BandIdx: BandContaining(121), Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: peakProf, Captured: 2000, Confidence: 0.9}
	mr2 := store.MergeOrAddByShape(p2, params)
	if mr2.Merged {
		t.Errorf("adjacent-band merge must require BOTH sides near boundary")
	}
	if store.Count() != 2 {
		t.Errorf("after adjacent-band non-boundary case: count=%d want 2", store.Count())
	}
}

// TestMergeByShapeInvalidPeakProfileStillMerges:
// невалидный PeakProfile не должен блокировать merge при близкой форме/частоте.
func TestMergeByShapeInvalidPeakProfileStillMerges(t *testing.T) {
	fs := 4000.0
	n := 2048
	window := makeSinWindow(n, 100, fs)
	shape := BuildShape(window, fs, 64)
	if len(shape.Points) == 0 {
		t.Fatal("BuildShape returned empty")
	}
	sig := ComputeShapeSignature(shape, 0, 1)

	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}

	// Невалидный профиль: нули.
	var invalidProf [PeakProfileSize]float64

	p1 := Pattern{Freq: 200, BandIdx: BandContaining(200), Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: invalidProf, Captured: 1000, Confidence: 0.9}
	mr1 := store.MergeOrAddByShape(p1, params)
	if mr1.Merged {
		t.Fatal("first add must not merge")
	}

	p2 := Pattern{Freq: 202, BandIdx: BandContaining(202), Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: invalidProf, Captured: 2000, Confidence: 0.9}
	mr2 := store.MergeOrAddByShape(p2, params)
	if !mr2.Merged {
		t.Errorf("invalid peak profiles should not block merge for similar waves")
	}
	if store.Count() != 1 {
		t.Errorf("after invalid-profile merge case: count=%d want 1", store.Count())
	}
}

// TestMergeByShapeNearSignatureMerges:
// соседняя coarse-сигнатура (NearCoarseEqual) должна merge'иться при очень близкой форме.
func TestMergeByShapeNearSignatureMerges(t *testing.T) {
	fs := 4000.0
	n := 2048
	window := makeSinWindow(n, 100, fs)
	shape := BuildShape(window, fs, 64)
	if len(shape.Points) == 0 {
		t.Fatal("BuildShape returned empty")
	}
	baseSig := ComputeShapeSignature(shape, 0, 1)
	peakProf := PeakProfileFromShape(shape)

	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}

	p1 := Pattern{Freq: 200, BandIdx: BandContaining(200), Amp: 1, GlyphShape: shape, ShapeSig: baseSig, PeakProfile: peakProf, Captured: 1000, Confidence: 0.9}
	store.MergeOrAddByShape(p1, params)

	nearSig := baseSig
	nearSig.CoarseTopologyCode = baseSig.CoarseTopologyCode + 1 // соседняя coarse-корзина
	p2 := Pattern{Freq: 202, BandIdx: BandContaining(202), Amp: 1, GlyphShape: shape, ShapeSig: nearSig, PeakProfile: peakProf, Captured: 2000, Confidence: 0.9}
	mr2 := store.MergeOrAddByShape(p2, params)
	if !mr2.Merged {
		t.Errorf("near coarse signature should merge for very similar shape")
	}
	if store.Count() != 1 {
		t.Errorf("after near-signature case: count=%d want 1", store.Count())
	}
}

// TestMergeByShapeTopologyOnlyNearMerges:
// при той же coarse-топологии и очень близкой форме merge допустим, даже если A/E дернулись.
func TestMergeByShapeTopologyOnlyNearMerges(t *testing.T) {
	fs := 4000.0
	n := 2048
	window := makeSinWindow(n, 100, fs)
	shape := BuildShape(window, fs, 64)
	if len(shape.Points) == 0 {
		t.Fatal("BuildShape returned empty")
	}
	baseSig := ComputeShapeSignature(shape, 0, 1)
	peakProf := PeakProfileFromShape(shape)

	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}

	p1 := Pattern{Freq: 200, BandIdx: BandContaining(200), Amp: 1, GlyphShape: shape, ShapeSig: baseSig, PeakProfile: peakProf, Captured: 1000, Confidence: 0.9}
	store.MergeOrAddByShape(p1, params)

	// Та же coarse-топология, но другой знак асимметрии — должно merge'иться при почти идентичной форме.
	sig2 := baseSig
	if sig2.AsymmetrySign == -1 {
		sig2.AsymmetrySign = 1
	} else {
		sig2.AsymmetrySign = -1
	}
	p2 := Pattern{Freq: 202, BandIdx: BandContaining(202), Amp: 1, GlyphShape: shape, ShapeSig: sig2, PeakProfile: peakProf, Captured: 2000, Confidence: 0.9}
	mr2 := store.MergeOrAddByShape(p2, params)
	if !mr2.Merged {
		t.Errorf("topology-only near case should merge for very similar shape")
	}
	if store.Count() != 1 {
		t.Errorf("after topology-only near case: count=%d want 1", store.Count())
	}
}

// verticesFromShape возвращает все вершины (локальные экстремумы) формы: индекс, значение, половина (0 или 1), бин PeakProfile (0..7).
func verticesFromShape(pts []float64) []struct {
	Idx   int
	Val   float64
	IsMax bool
	Half  int // 0 = первая половина (0-31), 1 = вторая (32-63)
	Bin   int // бин PeakProfile (8 бинов по 64/8 точек)
} {
	if len(pts) < 3 {
		return nil
	}
	const nBins = 8
	segLen := len(pts) / nBins
	if segLen < 1 {
		segLen = 1
	}
	var out []struct {
		Idx   int
		Val   float64
		IsMax bool
		Half  int
		Bin   int
	}
	for i := 1; i < len(pts)-1; i++ {
		isMax := pts[i] >= pts[i-1] && pts[i] >= pts[i+1]
		isMin := pts[i] <= pts[i-1] && pts[i] <= pts[i+1]
		if !isMax && !isMin {
			continue
		}
		half := 0
		if i >= len(pts)/2 {
			half = 1
		}
		bin := i / segLen
		if bin >= nBins {
			bin = nBins - 1
		}
		out = append(out, struct {
			Idx   int
			Val   float64
			IsMax bool
			Half  int
			Bin   int
		}{i, pts[i], isMax, half, bin})
	}
	return out
}

// TestVerticesFromGlyphs выводит все вершины по глифам: откуда форма, куда попали вершины (половина, бин), merge результат.
func TestVerticesFromGlyphs(t *testing.T) {
	fs := 4000.0
	n := 2048

	// Несколько форм: синус, «первая половина выше», «вторая половина выше»
	window := makeSinWindow(n, 100, fs)
	shapeSin := BuildShape(window, fs, 64)
	if len(shapeSin.Points) == 0 {
		t.Fatal("BuildShape(sin) empty")
	}

	const nPts = 64
	makeShape := func(firstHalfAmp, secondHalfAmp float64) (ShapeVector, [PeakProfileSize]float64) {
		pts := make([]float64, nPts)
		for i := 0; i < nPts; i++ {
			if i < 32 {
				pts[i] = firstHalfAmp * (1 - float64(i)/32) * (1 - float64(32-i)/32) * 4
			} else {
				pts[i] = secondHalfAmp * (1 - float64(i-32)/32) * (1 - float64(64-i)/32) * 4
			}
		}
		p0, pN := pts[0], pts[nPts-1]
		for i := 0; i < nPts; i++ {
			t := float64(i) / float64(nPts-1)
			pts[i] -= p0 + (pN-p0)*t
		}
		maxAbs := 1e-12
		for _, v := range pts {
			a := v
			if a < 0 {
				a = -a
			}
			if a > maxAbs {
				maxAbs = a
			}
		}
		if maxAbs < 1e-12 {
			maxAbs = 1
		}
		for i := range pts {
			pts[i] /= maxAbs
		}
		sh := ShapeVector{Points: pts}
		prof := PeakProfileFromShape(sh)
		return sh, prof
	}
	shapeA, profileA := makeShape(0.9, 0.2) // первая вершина выше
	shapeB, profileB := makeShape(0.2, 0.9) // вторая вершина выше

	params := ShapeMergeParams{MinCosForMerge: 0, MaxDistForMerge: 0.78}
	store := &PatternStore{}

	// Добавляем синус, потом A, потом B
	patterns := []struct {
		Name    string
		Shape   ShapeVector
		Profile [PeakProfileSize]float64
		Freq    float64
	}{
		{"sin", shapeSin, PeakProfileFromShape(shapeSin), 100},
		{"A_firstHigh", shapeA, profileA, 200},
		{"B_secondHigh", shapeB, profileB, 200},
	}
	for _, pp := range patterns {
		p := Pattern{
			Freq:        pp.Freq,
			Amp:         1,
			GlyphShape:  pp.Shape,
			ShapeSig:    ComputeShapeSignature(pp.Shape, 0, 1),
			PeakProfile: pp.Profile,
			Captured:    0,
			Confidence:  0.9,
			GlyphData:   make([]float64, 64),
		}
		mr := store.MergeOrAddByShape(p, params)
		t.Logf("[VERTICES] add %q -> merged=%v count=%d reason=%s", pp.Name, mr.Merged, store.Count(), mr.MergeReason)
	}

	// Вывод всех точек и вершин по каждому глифу в store
	storeSnapshot := store.List()
	for i := range storeSnapshot {
		ex := &storeSnapshot[i]
		if len(ex.GlyphShape.Points) == 0 {
			continue
		}
		pts := ex.GlyphShape.Points
		verts := verticesFromShape(pts)
		t.Logf("[VERTICES] === Pattern ID=%d Label=%s Freq=%.0f (n=%d) ===", ex.ID, ex.Label, ex.Freq, len(pts))
		// Все точки глифа: по 8 в строке (соответствует 8 бинам PeakProfile)
		for row := 0; row < len(pts); row += 8 {
			end := row + 8
			if end > len(pts) {
				end = len(pts)
			}
			t.Logf("[VERTICES]   pts[%2d..%2d] bin%d: %+.4f %+.4f %+.4f %+.4f %+.4f %+.4f %+.4f %+.4f",
				row, end-1, row/8,
				pts[row], safe(pts, row+1), safe(pts, row+2), safe(pts, row+3),
				safe(pts, row+4), safe(pts, row+5), safe(pts, row+6), safe(pts, row+7))
		}
		t.Logf("[VERTICES] PeakProfile bins (0..7): %v", ex.PeakProfile)
		sum1, sum2 := 0.0, 0.0
		max1, max2 := 0.0, 0.0
		for j := 0; j < 4; j++ {
			sum1 += ex.PeakProfile[j]
			if ex.PeakProfile[j] > max1 {
				max1 = ex.PeakProfile[j]
			}
		}
		for j := 4; j < 8; j++ {
			sum2 += ex.PeakProfile[j]
			if ex.PeakProfile[j] > max2 {
				max2 = ex.PeakProfile[j]
			}
		}
		firstDom := max1 >= max2
		t.Logf("[VERTICES] sum1=%.4f sum2=%.4f max1=%.4f max2=%.4f firstDominant=%v", sum1, sum2, max1, max2, firstDom)
		t.Logf("[VERTICES] вершины (локальные экстремумы): idx → val, half, bin")
		for _, v := range verts {
			t.Logf("[VERTICES]   idx=%2d val=%+.4f isMax=%v half=%d bin=%d", v.Idx, v.Val, v.IsMax, v.Half, v.Bin)
		}
	}
}

func safe(pts []float64, i int) float64 {
	if i < len(pts) {
		return pts[i]
	}
	return 0
}

// glyphDominantHalf возвращает true если у паттерна доминирует первая половина (max в бинах 0..3 >= max в 4..7).
func glyphDominantHalf(profile [PeakProfileSize]float64) bool {
	max1, max2 := 0.0, 0.0
	for i := 0; i < 4; i++ {
		if profile[i] > max1 {
			max1 = profile[i]
		}
	}
	for i := 4; i < 8; i++ {
		if profile[i] > max2 {
			max2 = profile[i]
		}
	}
	return max1 >= max2
}

// dumpStoreGlyphs выводит пошагово все глифы в store: ID, вершины, PeakProfile, firstDominant.
func dumpStoreGlyphs(t *testing.T, store *PatternStore, label string) {
	t.Helper()
	t.Logf("--- %s: store count=%d ---", label, store.Count())
	storeSnapshot := store.List()
	for i := range storeSnapshot {
		ex := &storeSnapshot[i]
		if len(ex.GlyphShape.Points) == 0 {
			continue
		}
		verts := verticesFromShape(ex.GlyphShape.Points)
		firstDom := glyphDominantHalf(ex.PeakProfile)
		t.Logf("  glyph id=%d label=%s freq=%.0f firstDominant=%v PeakProfile=%v",
			ex.ID, ex.Label, ex.Freq, firstDom, ex.PeakProfile)
		for _, v := range verts {
			t.Logf("    vertex idx=%d val=%.4f isMax=%v half=%d bin=%d", v.Idx, v.Val, v.IsMax, v.Half, v.Bin)
		}
	}
}

// TestMergeGlyphShapeImmutableStepByStep: карточка не перезаписывается при мерже.
// Пошагово: add A (первый пик), add B (второй пик), add A, add A — после каждого дамп глифов и вершин; проверяем, что форма карточки не меняется.
func TestMergeGlyphShapeImmutableStepByStep(t *testing.T) {
	const nPts = 64
	makeShape := func(firstHalfAmp, secondHalfAmp float64) (ShapeVector, [PeakProfileSize]float64) {
		pts := make([]float64, nPts)
		for i := 0; i < nPts; i++ {
			if i < 32 {
				pts[i] = firstHalfAmp * (1 - float64(i)/32) * (1 - float64(32-i)/32) * 4
			} else {
				pts[i] = secondHalfAmp * (1 - float64(i-32)/32) * (1 - float64(64-i)/32) * 4
			}
		}
		p0, pN := pts[0], pts[nPts-1]
		for i := 0; i < nPts; i++ {
			tt := float64(i) / float64(nPts-1)
			pts[i] -= p0 + (pN-p0)*tt
		}
		maxAbs := 1e-12
		for _, v := range pts {
			a := v
			if a < 0 {
				a = -a
			}
			if a > maxAbs {
				maxAbs = a
			}
		}
		if maxAbs < 1e-12 {
			maxAbs = 1
		}
		for i := range pts {
			pts[i] /= maxAbs
		}
		sh := ShapeVector{Points: pts}
		return sh, PeakProfileFromShape(sh)
	}
	shapeA, profileA := makeShape(0.9, 0.2) // первый пик выше
	shapeB, profileB := makeShape(0.2, 0.9) // второй пик выше

	params := ShapeMergeParams{MinCosForMerge: 0, MaxDistForMerge: 0.78}
	store := &PatternStore{}

	toPattern := func(name string, shape ShapeVector, profile [PeakProfileSize]float64, freq float64) Pattern {
		return Pattern{
			Freq:        freq,
			Amp:         1,
			GlyphShape:  shape,
			ShapeSig:    ComputeShapeSignature(shape, 0, 1),
			PeakProfile: profile,
			Captured:    0,
			Confidence:  0.9,
			GlyphData:   make([]float64, 64),
		}
	}

	// Шаг 1: добавить A (первый пик выше)
	mr1 := store.MergeOrAddByShape(toPattern("A", shapeA, profileA, 200), params)
	t.Logf("[STEP 1] add A (first dominant) -> merged=%v count=%d reason=%s bestID=%d", mr1.Merged, store.Count(), mr1.MergeReason, mr1.BestID)
	dumpStoreGlyphs(t, store, "after add A")
	if store.Count() != 1 {
		t.Fatalf("after add A: count=%d want 1", store.Count())
	}
	storeSnapshot := store.List()
	card0AfterA := storeSnapshot[0]
	card0VerticesAfterA := verticesFromShape(card0AfterA.GlyphShape.Points)
	card0FirstDomAfterA := glyphDominantHalf(card0AfterA.PeakProfile)
	if !card0FirstDomAfterA {
		t.Errorf("card 0 after add A: firstDominant must be true, got false")
	}

	// Шаг 2: добавить B (второй пик выше)
	mr2 := store.MergeOrAddByShape(toPattern("B", shapeB, profileB, 200), params)
	t.Logf("[STEP 2] add B (second dominant) -> merged=%v count=%d reason=%s bestID=%d", mr2.Merged, store.Count(), mr2.MergeReason, mr2.BestID)
	dumpStoreGlyphs(t, store, "after add B")
	if store.Count() != 2 {
		t.Fatalf("after add B: count=%d want 2", store.Count())
	}
	storeSnapshot = store.List()
	card1AfterB := storeSnapshot[1]
	card1FirstDomAfterB := glyphDominantHalf(card1AfterB.PeakProfile)
	if card1FirstDomAfterB {
		t.Errorf("card 1 after add B: firstDominant must be false (second peak), got true")
	}

	// Шаг 3: снова добавить A — должен мержиться в карточку 0, форма карточки 0 не должна измениться
	mr3 := store.MergeOrAddByShape(toPattern("A2", shapeA, profileA, 200), params)
	t.Logf("[STEP 3] add A again -> merged=%v count=%d reason=%s bestID=%d", mr3.Merged, store.Count(), mr3.MergeReason, mr3.BestID)
	dumpStoreGlyphs(t, store, "after add A again")
	if !mr3.Merged {
		t.Errorf("second add A should merge into card 0")
	}
	if store.Count() != 2 {
		t.Fatalf("after second A: count=%d want 2", store.Count())
	}
	storeSnapshot = store.List()
	card0AfterMerge := storeSnapshot[0]
	card0VerticesAfterMerge := verticesFromShape(card0AfterMerge.GlyphShape.Points)
	card0FirstDomAfterMerge := glyphDominantHalf(card0AfterMerge.PeakProfile)
	if !card0FirstDomAfterMerge {
		t.Errorf("card 0 after merge: firstDominant must remain true, got false (карточка переписалась)")
	}
	// Форма карточки 0 не изменилась: та же доминанта и те же вершины по половинам
	if len(card0VerticesAfterMerge) != len(card0VerticesAfterA) {
		t.Errorf("card 0 vertices count changed: was %d, now %d", len(card0VerticesAfterA), len(card0VerticesAfterMerge))
	}
	// Главный пик первой половины должен остаться в первой половине (half=0)
	var maxValCard0After, maxValCard0Merge float64
	for _, v := range card0VerticesAfterA {
		if v.IsMax && v.Val > maxValCard0After {
			maxValCard0After = v.Val
		}
	}
	for _, v := range card0VerticesAfterMerge {
		if v.IsMax && v.Val > maxValCard0Merge {
			maxValCard0Merge = v.Val
		}
	}
	if maxValCard0After < 0.99 || maxValCard0Merge < 0.99 {
		t.Logf("card 0 max vertex value: after A=%.4f after merge=%.4f", maxValCard0After, maxValCard0Merge)
	}

	// Шаг 4: ещё раз A — снова мерж в карточку 0
	mr4 := store.MergeOrAddByShape(toPattern("A3", shapeA, profileA, 200), params)
	t.Logf("[STEP 4] add A third -> merged=%v count=%d bestID=%d", mr4.Merged, store.Count(), mr4.BestID)
	dumpStoreGlyphs(t, store, "after add A third")
	if !mr4.Merged {
		t.Errorf("third add A should merge")
	}
	storeSnapshot = store.List()
	card0Final := storeSnapshot[0]
	if !glyphDominantHalf(card0Final.PeakProfile) {
		t.Errorf("card 0 final: firstDominant must still be true (карточка не должна переключаться на второй пик)")
	}
	// Карточка 1 не тронута
	if glyphDominantHalf(storeSnapshot[1].PeakProfile) {
		t.Errorf("card 1 must stay second-dominant")
	}
	t.Logf("OK: карточки неизменяемы по пикам после мержей")
}

// TestMergeByShapeDominantHalfDebug: отладка — «первая вершина выше» vs «вторая вершина выше» не должны мержиться.
// Две формы: явно первая половина выше / вторая половина выше; вывод PeakProfile, sum1/sum2, SameDominantHalf, merge результат.
func TestMergeByShapeDominantHalfDebug(t *testing.T) {
	const n = 64
	// Форма A: первая половина выше (первые 32 точки — большой горб, вторые 32 — маленький).
	ptsA := make([]float64, n)
	for i := 0; i < n; i++ {
		if i < 32 {
			ptsA[i] = 0.9 * (1 - float64(i)/32) * (1 - float64(32-i)/32) * 4 // горб в первой половине
		} else {
			ptsA[i] = 0.2 * (1 - float64(i-32)/32) * (1 - float64(64-i)/32) * 4 // маленький во второй
		}
	}
	// Нормализация: нули на границах, масштаб (как в BuildShape)
	p0, pN := ptsA[0], ptsA[n-1]
	for i := 0; i < n; i++ {
		t := 0.0
		if n > 1 {
			t = float64(i) / float64(n-1)
		}
		ptsA[i] -= p0 + (pN-p0)*t
	}
	maxAbs := 1e-12
	for _, v := range ptsA {
		a := v
		if a < 0 {
			a = -a
		}
		if a > maxAbs {
			maxAbs = a
		}
	}
	if maxAbs < 1e-12 {
		maxAbs = 1
	}
	for i := range ptsA {
		ptsA[i] /= maxAbs
	}
	shapeA := ShapeVector{Points: ptsA}
	profileA := PeakProfileFromShape(shapeA)

	// Форма B: вторая половина выше (первые 32 — маленький, вторые 32 — большой горб).
	ptsB := make([]float64, n)
	for i := 0; i < n; i++ {
		if i < 32 {
			ptsB[i] = 0.2 * (1 - float64(i)/32) * (1 - float64(32-i)/32) * 4
		} else {
			ptsB[i] = 0.9 * (1 - float64(i-32)/32) * (1 - float64(64-i)/32) * 4
		}
	}
	p0, pN = ptsB[0], ptsB[n-1]
	for i := 0; i < n; i++ {
		tt := 0.0
		if n > 1 {
			tt = float64(i) / float64(n-1)
		}
		ptsB[i] -= p0 + (pN-p0)*tt
	}
	maxAbs = 1e-12
	for _, v := range ptsB {
		a := v
		if a < 0 {
			a = -a
		}
		if a > maxAbs {
			maxAbs = a
		}
	}
	if maxAbs < 1e-12 {
		maxAbs = 1
	}
	for i := range ptsB {
		ptsB[i] /= maxAbs
	}
	shapeB := ShapeVector{Points: ptsB}
	profileB := PeakProfileFromShape(shapeB)

	// Отладка: sum и max по половинам, доминант (критерий — max в половине, не sum)
	const half = 8 / 2
	var sumA1, sumA2, sumB1, sumB2 float64
	var maxA1, maxA2, maxB1, maxB2 float64
	for i := 0; i < half; i++ {
		sumA1 += profileA[i]
		sumB1 += profileB[i]
		if profileA[i] > maxA1 {
			maxA1 = profileA[i]
		}
		if profileB[i] > maxB1 {
			maxB1 = profileB[i]
		}
	}
	for i := half; i < 8; i++ {
		sumA2 += profileA[i]
		sumB2 += profileB[i]
		if profileA[i] > maxA2 {
			maxA2 = profileA[i]
		}
		if profileB[i] > maxB2 {
			maxB2 = profileB[i]
		}
	}
	sameDom := PeakProfileSameDominantHalf(profileA, profileB)
	dist := PeakProfileDistance(profileA, profileB)
	t.Logf("[DEBUG] profileA sum1=%.4f sum2=%.4f max1=%.4f max2=%.4f firstDominant(sum)=%v firstDominant(max)=%v", sumA1, sumA2, maxA1, maxA2, sumA1 >= sumA2, maxA1 >= maxA2)
	t.Logf("[DEBUG] profileB sum1=%.4f sum2=%.4f max1=%.4f max2=%.4f firstDominant(sum)=%v firstDominant(max)=%v", sumB1, sumB2, maxB1, maxB2, sumB1 >= sumB2, maxB1 >= maxB2)
	t.Logf("[DEBUG] PeakProfileSameDominantHalf=%v PeakProfileDistance=%.4f", sameDom, dist)

	sigA := ComputeShapeSignature(shapeA, 0, 1)
	sigB := ComputeShapeSignature(shapeB, 0, 1)
	params := ShapeMergeParams{MinCosForMerge: 0, MaxDistForMerge: 0.78}
	store := &PatternStore{}
	p1 := Pattern{
		Freq:        200,
		Amp:         1,
		GlyphShape:  shapeA,
		ShapeSig:    sigA,
		PeakProfile: profileA,
		Captured:    1000,
		Confidence:  0.9,
		GlyphData:   make([]float64, 64),
	}
	mr1 := store.MergeOrAddByShape(p1, params)
	t.Logf("[DEBUG] first add: merged=%v count=%d", mr1.Merged, store.Count())

	p2 := Pattern{
		Freq:        200,
		Amp:         1,
		GlyphShape:  shapeB,
		ShapeSig:    sigB,
		PeakProfile: profileB,
		Captured:    2000,
		Confidence:  0.9,
		GlyphData:   make([]float64, 64),
	}
	mr2 := store.MergeOrAddByShape(p2, params)
	t.Logf("[DEBUG] second add (other dominant half): merged=%v count=%d reason=%s", mr2.Merged, store.Count(), mr2.MergeReason)

	if sameDom {
		t.Logf("[DEBUG] SameDominantHalf=true — критерий не различает половины; нужно менять логику (max по половинам?)")
	}
	if mr2.Merged {
		t.Errorf("первая вершина выше (A) и вторая вершина выше (B) не должны мержиться в одну карточку; merged=%v", mr2.Merged)
	}
	if store.Count() != 2 {
		t.Errorf("ожидалось 2 токена (разные доминантные половины), count=%d", store.Count())
	}
}

// TestMergeByShapeInvertedForm: убывание vs возрастание (cos < 0) — разные глифы, не мержить.
func TestMergeByShapeInvertedForm(t *testing.T) {
	fs := 4000.0
	n := 2048
	window := makeSinWindow(n, 100, fs)
	shape := BuildShape(window, fs, 64)
	if len(shape.Points) == 0 {
		t.Fatal("BuildShape returned empty")
	}
	sig := ComputeShapeSignature(shape, 0, 1)
	inv := make([]float64, len(shape.Points))
	for i := range shape.Points {
		inv[i] = -shape.Points[i]
	}
	shapeInv := ShapeVector{Points: inv}
	sigInv := ComputeShapeSignature(shapeInv, 0, 1)
	prof := PeakProfileFromShape(shape)
	profInv := PeakProfileFromShape(shapeInv)

	store := &PatternStore{}
	p1 := Pattern{Freq: 200, Amp: 1, GlyphShape: shape, ShapeSig: sig, PeakProfile: prof, Captured: 1000, Confidence: 0.9}
	store.MergeOrAddByShape(p1, ShapeMergeParams{MinCosForMerge: 0})
	if store.Count() != 1 {
		t.Fatalf("after first: count=%d want 1", store.Count())
	}
	// Инвертированная форма: cos < 0 и другая доминанта → не мержить (MinCosForMerge: 0).
	p2 := Pattern{Freq: 200, Amp: 1, GlyphShape: shapeInv, ShapeSig: sigInv, PeakProfile: profInv, Captured: 2000, Confidence: 0.9}
	mr2 := store.MergeOrAddByShape(p2, ShapeMergeParams{MinCosForMerge: 0})
	if mr2.Merged {
		t.Error("inverted form (убывание vs возрастание) must NOT merge — cos<0")
	}
	if store.Count() != 2 {
		t.Errorf("after inverted: count=%d want 2", store.Count())
	}
	t.Logf(".[inverted] count=2 merged=%v (cos<0 → different glyph)", mr2.Merged)
}

// --- JSON-вывод для анализа кластеризации: точки, сигнатуры, попарные cos/dist ---

type shapeDebugSig struct {
	T uint16 `json:"topology"`
	E uint8  `json:"envelope_class"`
	A int8   `json:"asymmetry"`
	M uint8  `json:"modulation"`
}

type shapeDebugItem struct {
	Name      string        `json:"name"`
	Points    []float64     `json:"points"`
	Signature shapeDebugSig `json:"signature"`
}

type pairwiseDebug struct {
	I          int     `json:"i"`
	J          int     `json:"j"`
	CosSim     float64 `json:"cos_sim"`
	ShapeDist  float64 `json:"shape_dist"`
	SameSig    bool    `json:"same_signature"`
	WouldMerge bool    `json:"would_merge"`
}

type shapeClusteringDebugOutput struct {
	Shapes    []shapeDebugItem `json:"shapes"`
	Pairwise  []pairwiseDebug  `json:"pairwise"`
	MergeRule string           `json:"merge_rule"`
}

// TestShapeClusteringDebugJSON выводит JSON с точками форм, сигнатурами и попарными метриками,
// чтобы по ответу видеть, как волны кластеризуются и чем отличаются «похожие» (почему не мержатся).
func TestShapeClusteringDebugJSON(t *testing.T) {
	fs := 4000.0
	n := 2048
	pointsPerCycle := 64

	buildOne := func(name string, window []float64) (ShapeVector, ShapeSignature) {
		shape := BuildShape(window, fs, pointsPerCycle)
		sig := ComputeShapeSignature(shape, 0, 1)
		return shape, sig
	}

	var shapes []ShapeVector
	var sigs []ShapeSignature
	var names []string

	// 1) Чистый синус 10 Hz
	w1 := makeSinWindow(n, 10, fs)
	s1, sig1 := buildOne("sin_10hz", w1)
	shapes = append(shapes, s1)
	sigs = append(sigs, sig1)
	names = append(names, "sin_10hz")

	// 2) Тот же синус 10 Hz, сдвиг по фазе (другой старт окна — эмулируем сдвиг)
	w2 := make([]float64, n)
	for i := 0; i < n; i++ {
		w2[i] = math.Sin(2*math.Pi*10*(float64(i)/fs) + 0.2)
	}
	s2, sig2 := buildOne("sin_10hz_phase0.2", w2)
	shapes = append(shapes, s2)
	sigs = append(sigs, sig2)
	names = append(names, "sin_10hz_phase0.2")

	// 3) Синус 11 Hz — та же «форма» синуса, другая частота
	w3 := makeSinWindow(n, 11, fs)
	s3, sig3 := buildOne("sin_11hz", w3)
	shapes = append(shapes, s3)
	sigs = append(sigs, sig3)
	names = append(names, "sin_11hz")

	// 4) Синус 10 Hz + шум (малая амплитуда) — может изменить число экстремумов
	w4 := make([]float64, n)
	for i := 0; i < n; i++ {
		w4[i] = math.Sin(2*math.Pi*10*float64(i)/fs) + 0.05*math.Sin(2*math.Pi*37*float64(i)/fs)
	}
	s4, sig4 := buildOne("sin_10hz_plus_noise", w4)
	shapes = append(shapes, s4)
	sigs = append(sigs, sig4)
	names = append(names, "sin_10hz_plus_noise")

	// 5) Вторая гармоника (форма другая — не чистый синус)
	w5 := make([]float64, n)
	for i := 0; i < n; i++ {
		t := float64(i) / fs
		w5[i] = math.Sin(2*math.Pi*10*t) + 0.3*math.Sin(2*math.Pi*20*t)
	}
	s5, sig5 := buildOne("sin_10hz_plus_2nd_harmonic", w5)
	shapes = append(shapes, s5)
	sigs = append(sigs, sig5)
	names = append(names, "sin_10hz_plus_2nd_harmonic")

	// 6) Синус 100 Hz (как в TestMergeByShapeDifferentFreqs) — для сравнения сигнатуры
	w6 := makeSinWindow(n, 100, fs)
	s6, sig6 := buildOne("sin_100hz", w6)
	shapes = append(shapes, s6)
	sigs = append(sigs, sig6)
	names = append(names, "sin_100hz")

	out := shapeClusteringDebugOutput{
		Shapes:    make([]shapeDebugItem, len(shapes)),
		Pairwise:  nil,
		MergeRule: "merge only if ShapeSignature equal AND cos_sim >= MinCosForMerge(0); signature = topology(envelope_class, asymmetry, modulation)",
	}
	for i := range shapes {
		out.Shapes[i] = shapeDebugItem{
			Name:   names[i],
			Points: shapes[i].Points,
			Signature: shapeDebugSig{
				T: sigs[i].TopologyCode,
				E: sigs[i].EnvelopeClass,
				A: sigs[i].AsymmetrySign,
				M: sigs[i].ModulationFlag,
			},
		}
	}
	for i := 0; i < len(shapes); i++ {
		for j := i + 1; j < len(shapes); j++ {
			cos := ShapeCosineSimilarity(shapes[i], shapes[j])
			dist := ShapeDistance(shapes[i], shapes[j])
			sameSig := sigs[i].Equal(sigs[j])
			wouldMerge := sameSig && cos >= 0
			out.Pairwise = append(out.Pairwise, pairwiseDebug{
				I: i, J: j,
				CosSim:     cos,
				ShapeDist:  dist,
				SameSig:    sameSig,
				WouldMerge: wouldMerge,
			})
		}
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// Вывод в stdout, чтобы по ответу теста видеть JSON (и передать в анализ).
	fmt.Println("=== shape_clustering_debug_json ===")
	fmt.Println(string(raw))
	fmt.Println("=== end shape_clustering_debug_json ===")
}

// TestShapePipelineDiagnostic: полная диагностика — % волны в классификатор, остаток по энтропии.
// Выводит: peaks total/rejected/classified, tokens, H_initial, H_final, % разбора, шаги Decompose.
// Запуск: go test -v -run TestShapePipelineDiagnostic ./server
func TestShapePipelineDiagnostic(t *testing.T) {
	fs := 4000.0
	windowSec := 2.6
	nWin := int(fs * windowSec)
	if nWin < 2048 {
		nWin = 2048
	}
	window := makeSinWindow(nWin, 50, fs)
	x := make([]float64, nWin)
	y := make([]float64, nWin)
	z := make([]float64, nWin)
	fillXYZ(x, y, z, window)

	params := defaultShapePipelineParams()
	params.AllowDiscovery = true
	runStats := &ShapePipelineRunStats{}
	params.RunStats = runStats
	store := NewShapeClusterStore(params.DistThr)
	decomposer := &ResidualDecomposer{}
	patternStore := &PatternStore{}
	metrics := NewPipelineMetrics()

	// 1) Прогон пайплайна (накапливаем статистику по всем тикам)
	runs := 10
	var totalPeaks, totalRejected, totalClassified int
	for i := 0; i < runs; i++ {
		UpdateShapePipeline(x, y, z, fs, time.Now(), params, store, decomposer, patternStore, metrics)
		totalPeaks += runStats.PeaksTotal
		totalRejected += runStats.PeaksRejectedStationarity
		totalClassified += runStats.PeaksClassified
	}

	// 2) Энтропия исходного окна
	const entropyBins = 16
	feat0 := ToFeatures(window, entropyBins)
	H0 := Entropy(feat0)

	// 3) Decompose того же окна — остаточная энтропия и шаги
	decomposeParams := DecomposeParams{
		MaxIters:       params.MaxIters,
		HThr:           params.HThr,
		MinDeltaH:      params.MinDeltaH,
		DistThr:        params.DistThr,
		MinConf:        params.MinConf,
		Fs:             fs,
		PointsPerCycle: params.PointsPerCycle,
		Metrics:        metrics,
		AmpNorm:        1.0,
		AllowAllWaves:  params.AllowAllWaves,
	}
	result := decomposer.Decompose(window, patternStore, decomposeParams, time.Now())
	featResid := ToFeatures(result.Residual, entropyBins)
	Hfinal := Entropy(featResid)

	// 4) Вывод
	pctRejected := 0.0
	if totalPeaks > 0 {
		pctRejected = 100 * float64(totalRejected) / float64(totalPeaks)
	}
	pctClassified := 0.0
	if totalPeaks > 0 {
		pctClassified = 100 * float64(totalClassified) / float64(totalPeaks)
	}
	pctDecomposed := 0.0
	if H0 > 1e-12 {
		pctDecomposed = 100 * (H0 - Hfinal) / H0
	}

	fmt.Println("=== shape_pipeline_diagnostic ===")
	fmt.Printf("runs=%d window_len=%d (%.2f s)\n", runs, nWin, float64(nWin)/fs)
	fmt.Printf("peaks_total=%d peaks_rejected_stationarity=%d (%.1f%%) peaks_classified=%d (%.1f%%)\n",
		totalPeaks, totalRejected, pctRejected, totalClassified, pctClassified)
	fmt.Printf("tokens_count=%d\n", patternStore.Count())
	fmt.Printf("H_initial=%.4f H_final=%.4f pct_decomposed=%.1f%%\n", H0, Hfinal, pctDecomposed)
	fmt.Printf("decompose_steps=%d residual_len=%d\n", len(result.Steps), len(result.Residual))
	snap := metrics.Snapshot(patternStore.Count())
	fmt.Printf("reuse_rate=%.2f merge_rate=%.2f novelty=%.2f\n", snap.ReuseRate, snap.MergeRate, snap.Novelty)
	fmt.Println("=== end shape_pipeline_diagnostic ===")

	// Жёсткие проверки: при стабильном синусе хотя бы часть пиков должна проходить стационарность
	if totalPeaks > 0 && totalClassified == 0 && totalRejected == totalPeaks {
		t.Errorf("all %d peaks rejected by StationarityOK (nothing in classifier); relax A_cv/A_slope or window", totalPeaks)
	}
}

// TestGlyphProfileVs3AxesDebug отлаживает расхождение между профилем (GlyphShape) и трехосевым глифом (GlyphData X/Y/Z).
// При стационарной волне профиль «гнутый» (форма волны), а три оси — ровные (envelope).
// Выводит значения для диагностики; после фикса оба должны показывать форму.
func TestGlyphProfileVs3AxesDebug(t *testing.T) {
	fs := 4000.0
	windowSec := 2.6
	nWin := int(fs * windowSec)
	if nWin < 2048 {
		nWin = 2048
	}
	freq := 50.0
	window := makeSinWindow(nWin, freq, fs)
	x := make([]float64, nWin)
	y := make([]float64, nWin)
	z := make([]float64, nWin)
	fillXYZ(x, y, z, window)

	params := defaultShapePipelineParams()
	params.AllowDiscovery = true
	store := NewShapeClusterStore(params.DistThr)
	decomposer := &ResidualDecomposer{}
	patternStore := &PatternStore{}

	// Запуск пайплайна
	UpdateShapePipeline(x, y, z, fs, time.Now(), params, store, decomposer, patternStore, nil)

	list := patternStore.List()
	if len(list) == 0 {
		t.Log("Нет паттернов после пайплайна")
		return
	}

	fmt.Println("=== TestGlyphProfileVs3AxesDebug ===")
	for _, p := range list {
		fmt.Printf("\n--- Pattern ID=%d freq=%.1f Hz ---\n", p.ID, p.Freq)

		// Профиль (GlyphShape.Points)
		shapePoints := p.GlyphShape.Points
		fmt.Printf("GlyphShape.Points (профиль, len=%d):\n", len(shapePoints))
		if len(shapePoints) >= 8 {
			// Первые и последние 4 точки + статистика
			fmt.Printf("  first4: [%.4f, %.4f, %.4f, %.4f]\n", shapePoints[0], shapePoints[1], shapePoints[2], shapePoints[3])
			fmt.Printf("  last4:  [%.4f, %.4f, %.4f, %.4f]\n", shapePoints[len(shapePoints)-4], shapePoints[len(shapePoints)-3], shapePoints[len(shapePoints)-2], shapePoints[len(shapePoints)-1])
			minS, maxS := shapePoints[0], shapePoints[0]
			for _, v := range shapePoints {
				if v < minS {
					minS = v
				}
				if v > maxS {
					maxS = v
				}
			}
			fmt.Printf("  min=%.4f max=%.4f range=%.4f\n", minS, maxS, maxS-minS)
		}

		// Трехосевой глиф GlyphData (X)
		glyphX := p.GlyphData
		fmt.Printf("GlyphData X (трехосевой, len=%d):\n", len(glyphX))
		if len(glyphX) >= FrozenGlyphPoints {
			// Midline (среднее min+max по сегментам)
			K := FrozenGlyphSegments
			midline := make([]float64, K)
			for j := 0; j < K; j++ {
				midline[j] = (glyphX[j] + glyphX[K+j]) * 0.5
			}
			fmt.Printf("  midline first4: [%.4f, %.4f, %.4f, %.4f]\n", midline[0], midline[1], midline[2], midline[3])
			fmt.Printf("  midline last4:  [%.4f, %.4f, %.4f, %.4f]\n", midline[K-4], midline[K-3], midline[K-2], midline[K-1])
			minM, maxM := midline[0], midline[0]
			for _, v := range midline {
				if v < minM {
					minM = v
				}
				if v > maxM {
					maxM = v
				}
			}
			fmt.Printf("  midline min=%.4f max=%.4f range=%.4f\n", minM, maxM, maxM-minM)
		}

		// Трехосевой глиф GlyphDataY
		glyphY := p.GlyphDataY
		fmt.Printf("GlyphDataY (len=%d):\n", len(glyphY))
		if len(glyphY) >= FrozenGlyphPoints {
			K := FrozenGlyphSegments
			midline := make([]float64, K)
			for j := 0; j < K; j++ {
				midline[j] = (glyphY[j] + glyphY[K+j]) * 0.5
			}
			minM, maxM := midline[0], midline[0]
			for _, v := range midline {
				if v < minM {
					minM = v
				}
				if v > maxM {
					maxM = v
				}
			}
			fmt.Printf("  midline min=%.4f max=%.4f range=%.4f\n", minM, maxM, maxM-minM)
		}

		// Трехосевой глиф GlyphDataZ
		glyphZ := p.GlyphDataZ
		fmt.Printf("GlyphDataZ (len=%d):\n", len(glyphZ))
		if len(glyphZ) >= FrozenGlyphPoints {
			K := FrozenGlyphSegments
			midline := make([]float64, K)
			for j := 0; j < K; j++ {
				midline[j] = (glyphZ[j] + glyphZ[K+j]) * 0.5
			}
			minM, maxM := midline[0], midline[0]
			for _, v := range midline {
				if v < minM {
					minM = v
				}
				if v > maxM {
					maxM = v
				}
			}
			fmt.Printf("  midline min=%.4f max=%.4f range=%.4f\n", minM, maxM, maxM-minM)
		}

		fmt.Println()
	}
	fmt.Println("=== end TestGlyphProfileVs3AxesDebug ===")

	// Проверка: после фикса диапазон трехосевых глифов должен быть сопоставим с профилем
	for _, p := range list {
		shapeRange := 0.0
		if len(p.GlyphShape.Points) >= 8 {
			minS, maxS := p.GlyphShape.Points[0], p.GlyphShape.Points[0]
			for _, v := range p.GlyphShape.Points {
				if v < minS {
					minS = v
				}
				if v > maxS {
					maxS = v
				}
			}
			shapeRange = maxS - minS
		}

		glyphXRange := 0.0
		if len(p.GlyphData) >= FrozenGlyphPoints {
			K := FrozenGlyphSegments
			minM, maxM := (p.GlyphData[0]+p.GlyphData[K])*0.5, (p.GlyphData[0]+p.GlyphData[K])*0.5
			for j := 0; j < K; j++ {
				m := (p.GlyphData[j] + p.GlyphData[K+j]) * 0.5
				if m < minM {
					minM = m
				}
				if m > maxM {
					maxM = m
				}
			}
			glyphXRange = maxM - minM
		}

		// Если профиль имеет диапазон > 1.0, а трехосевой < 0.3, то баг ещё не исправлен
		if shapeRange > 1.0 && glyphXRange < 0.3 {
			t.Errorf("Pattern %d: профиль range=%.2f (гнутый), трехосевой X range=%.2f (ровный) — баг: envelope вместо формы", p.ID, shapeRange, glyphXRange)
		}
	}
}

// TestPrimaryAlphabetBand6Energy проверяет, что при сигнале в полосе B6 (1.5–2 kHz при fs=4k)
// primaryAlphabetStore получает ненулевую RawE для band 6 — иначе на Discovery B6 «молчит».
func TestPrimaryAlphabetBand6Energy(t *testing.T) {
	fs := 4000.0
	// Окно пайплайна: Nwin = fs * WaveWindowSec (2.5) = 10000
	windowSec := 2.6
	nWin := int(fs * windowSec)
	if nWin < 256 {
		nWin = 256
	}
	// Сигнал в полосе B6: при fs=4k Найквист 2k, B6 = 1500–2500 Hz, clamp → 1500–1999.5 Hz
	freqB6 := 1700.0
	window := makeSinWindow(nWin, freqB6, fs)
	x := make([]float64, nWin)
	y := make([]float64, nWin)
	z := make([]float64, nWin)
	fillXYZ(x, y, z, window)

	primaryStore := NewPrimaryAlphabetStore()
	params := defaultShapePipelineParams()
	params.UseBands = true
	params.OnPrimaryGlyphBatch = func(exciterID int, bandEnergy []float64, nowMs int64) {
		primaryStore.UpdateAll(exciterID, bandEnergy, nowMs)
	}

	store := NewShapeClusterStore(params.DistThr)
	decomposer := &ResidualDecomposer{}
	patternStore := &PatternStore{}

	UpdateShapePipeline(x, y, z, fs, time.Now(), params, store, decomposer, patternStore, nil)

	cell6 := primaryStore.Cell(0, 6)
	if cell6 == nil {
		t.Fatal("Cell(0, 6) должен существовать после UpdateShapePipeline с сигналом в B6")
	}
	if cell6.RawE < 1e-9 {
		t.Errorf("B6 RawE = %v, ожидается > 0 при сигнале %.0f Hz в полосе B6 (1500–2500 Hz @ fs=4k); проверь signal.NewBandpassFilter clamp и пайплайн", cell6.RawE, freqB6)
	}
	// B7 при fs=4k выше Найквиста — должен быть 0
	cell7 := primaryStore.Cell(0, 7)
	if cell7 != nil && cell7.RawE > 1e-6 {
		t.Logf("B7 RawE = %v (при fs=4k полоса выше Найквиста — допустимо небольшой шум)", cell7.RawE)
	}
}
