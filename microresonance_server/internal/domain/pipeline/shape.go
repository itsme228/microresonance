// ShapePipeline: пайплайн токенизации по форме.
// FFT-based: волны по пикам спектра, каждая волна — символ, раскладываем по дорожкам (классам).
// Одновременно классифицируем все найденные волны, не по временным окнам.
//
// Где могут теряться волны (все «ворота» до MergeOrAddByShape):
//  1. findTopKPeaksPerBand: только topPerBand=3 пика на полосу (B0..B7) → макс. 24 пика на тик; порог ampNorm >= ampNormThreshold (0.8 в detection.go).
//  2. MinFreqHz / MaxFreqHz: пик вне [0.2, 20k] Гц отбрасывается.
//  3. AmpNormThr (params): пик с ampNorm < порога отбрасывается (default 0.8).
//  4. StationarityOK: при AllowAllWaves=false огибающая нестационарна (фронт, cv или slope выше порога) → пик отбрасывается.
//  5. BuildShape/ExtractWaveShapeAtFreq вернули пустую форму → отбрасывается.
//  6. ShapeLooksCleanCycle: при AllowAllWaves=false форма с >6 пересечений нуля отбрасывается. При AllowAllWaves=true — все волны (фронты, вариации) идут в классификатор.
//  7. ComputeFrozenGlyphFromWindow: меньше FrozenGlyphPoints (64) точек → отбрасывается.
//  8. AllowDiscovery: при false волны не отправляются в MergeOrAddByShape (экран/режим без Discovery).

package pipeline

import (
	"fmt"
	"log"
	"math"
	"os"
	"time"

	"server/internal/platform/signal"
)

// Defaults — подобраны для FFT-wave пайплайна.
const (
	DefaultWindowLen   = 1.0
	DefaultStepSec     = 0.2
	DefaultPointsCycle = 256
	DefaultMaxIters    = 14
	DefaultHThr        = 0.6
	DefaultMinDeltaH   = 0.0005
	DefaultDistThr     = 0.55 // merge похожих форм (коarse signature gate снижает ложные merge)
	DefaultMinCos      = 0.42 // merge только при умеренно высокой косинусной похожести
	DefaultMinConf     = 0.5
	// FFT-wave
	DefaultWaveFFTSize   = 2048    // размер FFT для поиска пиков
	DefaultWaveWindowSec = 2.5     // окно для извлечения волны — длинные участки с фронтами (2.5 с @ 4k)
	DefaultWaveMinCycles = 6       // минимум циклов для формы (длинные зигзаги с деталями)
	DefaultAmpNormThr    = 0.8     // порог amp_norm для учёта пика
	DefaultMinFreqHz     = 0.2     // нижняя граница контентных полос (B0); все полосы B0..B7 участвуют в токенах
	DefaultMaxFreqHz     = 20000.0 // верхняя граница контентных полос (B7); все полосы B0..B7 — контент и глифы
	DefaultContentBands  = 8       // полосы B0..B7 (0.2–20k Hz) — контент и глифы
)

// ShapePipelineRunStats — диагностика одного прогона (опционально, для тестов и тюнинга).
type ShapePipelineRunStats struct {
	PeaksTotal                int // пиков после фильтра freq/amp
	PeaksRejectedStationarity int // отсечено по StationarityOK (фронты)
	PeaksClassified           int // отправлено в MergeOrAddByShape
}

// WaveClassifiedPeak — один классифицированный пик: волна (freq, amp) → токен BestID (Merged = мерж в существующий).
// Для пошаговой отладки: DistMin, SecondGap, MergeReason, DistsToAll, DistsToAllWithSig, ShapeSig.
type WaveClassifiedPeak struct {
	Freq              float64
	Amp               float64
	BestID            int
	Merged            bool
	DistMin           float64
	SecondGap         float64
	MergeReason       string
	DistsToAll        []DistID
	DistsToAllWithSig []DistIDWithSig // id, dist, sig_match — по каким признакам куда легло
	ShapeSig          ShapeSignature
	PointsLen         int
}

// WaveDebugSnapshot — снимок тика для отладки: какие волны куда попали, что ушло в динамику.
// Заполняется при params.WaveDebug != nil (например DEBUG_WAVES=1 в лайв-тесте).
type WaveDebugSnapshot struct {
	NowMs              int64
	PeaksTotal         int
	Rejected           int
	Classified         int
	ClassifiedPeaks    []WaveClassifiedPeak // волна (freq,amp) → токен BestID + dist/reason
	DynamicsCandidates []struct {
		ID  int
		Amp float64
	}
	DominantID    int  // что пушится в DynamicsStore (доминантный по amp)
	SegmentPushed bool // true если в этом тике в DynamicsStore запушен сегмент (смена глифа)
	LockedCount   int  // число паттернов с HasGlyph (для отладки)
}

// ShapePipelineParams — параметры пайплайна.
type ShapePipelineParams struct {
	WindowLen        float64
	StepSec          float64
	PointsPerCycle   int
	UseMedianNorm    bool
	UseMag2          bool
	MaxIters         int
	HThr             float64
	MinDeltaH        float64
	DistThr          float64
	MinCos           float64 // merge: cos >= MinCos
	MaxFreqRatio     float64 // merge: max(freq_new, freq_center)/min <= ratio
	AdjacentDistThr  float64 // merge для соседних полос при частоте у границы
	HysteresisMargin float64 // липкость к предыдущему ID в полосе
	UltraStrongDist  float64 // порог «очень близко» для диагностики (частота в merge всё равно проверяется)
	MinConf          float64
	// FFT-wave (legacy Discovery C поля оставлены для совместимости UI)
	DiscoveryBufferSec  float64
	DiscoverySegmentSec float64
	ThrScore            float64
	ThrH                float64
	HighpassHz          float64
	WaveFFTSize         int                    // размер FFT
	WaveWindowSec       float64                // окно для извлечения волны, сек (длинные участки)
	WaveMinCycles       int                    // минимум циклов для формы волны
	AmpNormThr          float64                // порог amp_norm
	MinFreqHz           float64                // мин частота
	MaxFreqHz           float64                // макс частота для глифов (выше — только подпись/шум); 0 = 1500
	UseBands            bool                   // форма из полосы (band squared), не из голой волны по частоте
	RunStats            *ShapePipelineRunStats // опционально: заполняется при вызове
	// Стационарность: при AllowAllWaves=false — только плато. При AllowAllWaves=true фронты и вариации глифа тоже попадают в классификатор.
	A_cv    float64 // std(env)/mean(env) < A_cv (0 = 1.2)
	A_slope float64 // |slope(env)|/mean(env) < A_slope 1/с (0 = 2.0)

	// AllowAllWaves: true = не фильтровать по StationarityOK и ShapeLooksCleanCycle; все волны (включая фронты, переходы, вариации глифа) идут в классификатор.
	AllowAllWaves bool

	// Discovery-алфавит только в стационарных режимах. false = Chirp/Scan/Fast-scan/Phase-scan/Dual-asym/Burst — metadata, без MergeOrAdd/AddDiscoveryCandidate.
	AllowDiscovery bool

	// DYNAMICS: при не nil сюда пушатся события глифов (PatternID, Ts, Confidence) после каждого MergeOrAddByShape.
	DynamicsStore *DynamicsStore

	// TrainerGlyphBuffer: при не nil сюда добавляется каждый классифицированный глиф (Points, BandIdx, Ts) для калибровки TRAIN → TensorFlow (legacy).
	TrainerGlyphBuffer TrainerGlyphBuffer
	// TrainerEventBuffer: при не nil сюда добавляется каждый glyph-event (один цикл, 3 экстремума, инварианты) для калибровки TRAIN → event-based датасет.
	TrainerEventBuffer TrainerEventBuffer

	// ExciterID: для первого алфавита (exciter, band). 0 = idle, 1..N = режим возбуждения (am-sine, chirp, …).
	ExciterID int
	// AmFreqHz: частота AM-модуляции для reaction alphabet (фаза 0..1).
	AmFreqHz float64
	// OnPrimaryGlyph: при не nil вызывается для обновления одной ячейки (legacy).
	OnPrimaryGlyph func(exciterID, bandID int, E float64, nowMs int64)
	// OnPrimaryGlyphBatch: при не nil вызывается для пакетного обновления (E_rel, гистерезис, decay).
	// bandEnergy — сырая энергия по полосам [8]; внутри: E_rel, T_on/T_off, decay, N_max.
	OnPrimaryGlyphBatch func(exciterID int, bandEnergy []float64, nowMs int64)
	// OnReactionGlyph: при не nil вызывается при эмиссии reaction-паттерна (exciter, band, reaction_id).
	// totalEnergy — сумма сырых E по фазам до нормализации (для ML-порогов).
	OnReactionGlyph func(exciterID, bandID, reactionID int, vector [ReactionPhaseBins]float64, totalEnergy float64, nowMs int64)

	// MLQueue: при не nil — эмиссия EventGlyph при merge/add (новая архитектура).
	MLQueue *MLQueueImpl

	// WaveDebug: при не nil заполняется снимок волны→токен→динамика для отладки (DEBUG_WAVES=1).
	WaveDebug *WaveDebugSnapshot

	// DynamicsLastID: при не nil — гистерезис (legacy, одна лента). Игнорируется если задан DynamicsLastIDPerBand.
	// DynamicsLastIDPerBand: при не nil и len >= DefaultContentBands — гистерезис по полосам; пушим по одному глифу на полосу (сохраняется циклическая динамика).
	// DynamicsSegmentStartMs, DynamicsLastAmp: при не nil — событийная модель (legacy), сегмент при смене глифа.
	// DynamicsSegmentStartMsPerBand: при не nil и len >= DefaultContentBands — событийная модель по полосам: пушим сегмент с DurationMs только при смене глифа (длинные holds, меньше фликера).
	// Только locked-глифы (HasGlyph) участвуют в динамике.
	DynamicsLastID                *int
	DynamicsLastIDPerBand         []int    // [0..7] последний ID по полосам; при nil — legacy один доминант
	DynamicsSegmentStartMs        *int64   // T_start текущего сегмента (событийная модель, legacy)
	DynamicsSegmentStartMsPerBand []int64  // T_start сегмента по полосам (событийная модель per-band)
	DynamicsLastAmp               *float64 // amp при смене сегмента (legacy)
}

// BandContaining возвращает индекс полосы BandsFilterbank, в которую попадает частота freq; -1 если ни в одну.
// Границы полуинтервалы [LoHz, HiHz): частота на стыке (напр. 2 Гц) относится только к верхней полосе (B1), не к обеим.
func BandContaining(freq float64) int {
	for i, b := range BandsFilterbank {
		if freq < b.LoHz {
			continue
		}
		if i < len(BandsFilterbank)-1 {
			if freq < b.HiHz {
				return i
			}
		} else {
			if freq <= b.HiHz {
				return i
			}
		}
	}
	return -1
}

func defaultShapePipelineParams() ShapePipelineParams {
	return ShapePipelineParams{
		WindowLen:           DefaultWindowLen,
		StepSec:             DefaultStepSec,
		PointsPerCycle:      DefaultPointsCycle,
		UseMedianNorm:       false,
		UseMag2:             true,
		MaxIters:            DefaultMaxIters,
		HThr:                DefaultHThr,
		MinDeltaH:           DefaultMinDeltaH,
		DistThr:             DefaultDistThr,
		MinCos:              DefaultMinCos,
		MaxFreqRatio:        1.5,
		AdjacentDistThr:     0.45,
		HysteresisMargin:    0.05,
		UltraStrongDist:     0.01,
		MinConf:             DefaultMinConf,
		DiscoveryBufferSec:  2.0,
		DiscoverySegmentSec: 0.128,
		ThrScore:            0.45,
		ThrH:                0.95,
		HighpassHz:          0.5,
		WaveFFTSize:         DefaultWaveFFTSize,
		WaveWindowSec:       DefaultWaveWindowSec,
		WaveMinCycles:       DefaultWaveMinCycles,
		AmpNormThr:          DefaultAmpNormThr,
		MinFreqHz:           DefaultMinFreqHz,
		MaxFreqHz:           DefaultMaxFreqHz,
		UseBands:            true, // форма из полосы (band squared), не из голой волны
		A_cv:                1.2,  // при AllowAllWaves=false
		A_slope:             2.0,  // при AllowAllWaves=false
		AllowAllWaves:       true, // фронты, переходы, вариации глифа — в классификатор
	}
}

// Кэш IIR-фильтров полос для пайплайна (форма из полосы, не из голой волны).
var pipelineBandFilters []signal.BandpassFilter
var pipelineBandFiltersFs float64

// UpdateShapePipeline выполняет FFT-based пайплайн:
// FFT → top-K пиков → для каждой волны форма из полосы (band squared) или по частоте → MergeOrAddByShape.
// Частота > MaxFreqHz (20k) — не классифицируем.
func UpdateShapePipeline(
	x, y, z []float64, fs float64, now time.Time,
	params ShapePipelineParams,
	shapeStore *ShapeClusterStore,
	residualDecomposer *ResidualDecomposer,
	patternStore *PatternStore,
	metrics *PipelineMetrics,
) {
	if fs <= 0 || len(x) < 256 {
		return
	}

	n := params.WaveFFTSize
	if n <= 0 {
		n = DefaultWaveFFTSize
	}
	if n > len(x) {
		n = len(x)
	}
	if n < 256 {
		return
	}

	magX, freqs := computeFFTScan(x, n, fs)
	magY, _ := computeFFTScan(y, n, fs)
	magZ, _ := computeFFTScan(z, n, fs)
	if len(magX) < 2 || len(freqs) < 2 {
		return
	}

	// Топ по каждому диапазону отдельно: в каждой полосе B0..B7 — свои сильнейшие пики, иначе B1,B2,B3 молчат.
	const topPerBand = 3
	peaks, _ := FindTopKPeaksPerBand(magX, magY, magZ, freqs, n, fs, DefaultContentBands, topPerBand)
	if len(peaks) == 0 {
		return
	}

	windowSec := params.WaveWindowSec
	if windowSec <= 0 {
		windowSec = DefaultWaveWindowSec
	}
	minFreq := params.MinFreqHz
	if minFreq <= 0 {
		minFreq = DefaultMinFreqHz
	}
	maxFreq := params.MaxFreqHz
	if maxFreq <= 0 {
		maxFreq = DefaultMaxFreqHz
	}
	ampThr := params.AmpNormThr
	if ampThr <= 0 {
		ampThr = DefaultAmpNormThr
	}

	Nwin := int(fs * windowSec)
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

	nowMs := now.UnixMilli()
	if params.RunStats != nil {
		params.RunStats.PeaksTotal = 0
		params.RunStats.PeaksRejectedStationarity = 0
		params.RunStats.PeaksClassified = 0
	}
	maxDistForMerge := params.DistThr
	if maxDistForMerge <= 0 {
		maxDistForMerge = DefaultDistThr
	}
	maxDistAdjacentBand := params.AdjacentDistThr
	if maxDistAdjacentBand <= 0 {
		maxDistAdjacentBand = 0.45
	}
	maxFreqRatio := params.MaxFreqRatio
	if maxFreqRatio <= 0 {
		maxFreqRatio = 1.5
	}
	hysteresisMargin := params.HysteresisMargin
	if hysteresisMargin <= 0 {
		hysteresisMargin = 0.05
	}
	ultraStrongDist := params.UltraStrongDist
	if ultraStrongDist <= 0 {
		ultraStrongDist = 0.01
	}
	minCos := params.MinCos
	if minCos > 1 {
		minCos = 1
	}
	mergeParams := ShapeMergeParams{
		NoMerge:              false,
		MinCosForMerge:       minCos,
		MaxDistForMerge:      maxDistForMerge,
		MaxDistAdjacentBand:  maxDistAdjacentBand,
		MaxFreqRatioForMerge: maxFreqRatio,
		HysteresisMargin:     hysteresisMargin,
		UltraStrongDist:      ultraStrongDist,
	}

	// Форма из полос: фильтруем окно по полосам B0..B7, получаем bandSquared и bandX/Y/Z для глифов.
	// Трёхосевой глиф на карточке должен быть из полосы, иначе одна вибрация попадает в B0 и B1 как два дубля.
	var bandSquared [8][]float64
	var bandX, bandY, bandZ [8][]float64
	if params.UseBands && len(BandsFilterbank) >= DefaultContentBands {
		if pipelineBandFilters == nil || pipelineBandFiltersFs != fs {
			pipelineBandFilters = make([]signal.BandpassFilter, DefaultContentBands)
			for i := 0; i < DefaultContentBands; i++ {
				b := &BandsFilterbank[i]
				pipelineBandFilters[i] = signal.NewBandpassFilter(b.LoHz, b.HiHz, fs)
			}
			pipelineBandFiltersFs = fs
		}
		nW := Nwin
		if winStart+nW > len(y) || winStart+nW > len(z) {
			if len(y) < winStart+Nwin {
				nW = len(y) - winStart
			}
			if len(z) < winStart+nW {
				nW = len(z) - winStart
			}
		}
		if nW >= 128 {
			xW := x[winStart : winStart+nW]
			yW := y[winStart : winStart+nW]
			zW := z[winStart : winStart+nW]
			for i := 0; i < DefaultContentBands; i++ {
				pipelineBandFilters[i].Reset()
				xB, yB, zB, sq := pipelineBandFilters[i].Filter(xW, yW, zW)
				bandSquared[i] = sq
				bandX[i], bandY[i], bandZ[i] = xB, yB, zB
			}
		}
	}

	// Первый алфавит (exciter, band): E_rel, гистерезис, decay, лимит активных.
	if params.UseBands && len(bandSquared[0]) >= 64 {
		var bandEnergy [8]float64
		for i := 0; i < DefaultContentBands && i < len(bandSquared); i++ {
			sq := bandSquared[i]
			if len(sq) < 8 {
				continue
			}
			var sum float64
			for _, v := range sq {
				sum += v * v
			}
			bandEnergy[i] = math.Sqrt(sum / float64(len(sq)))
		}
		exciterID := params.ExciterID
		if exciterID < 0 {
			exciterID = 0
		}
		if params.OnPrimaryGlyphBatch != nil {
			params.OnPrimaryGlyphBatch(exciterID, bandEnergy[:], nowMs)
		} else if params.OnPrimaryGlyph != nil {
			const minE = 1e-9
			for b := 0; b < DefaultContentBands; b++ {
				if bandEnergy[b] > minE {
					params.OnPrimaryGlyph(exciterID, b, bandEnergy[b], nowMs)
				}
			}
		}
	}

	// Собираем волны и почти одновременно добавляем в locked классы.
	// DYNAMICS: по одной полосе (B0..B5) пушим по одному глифу на полосу (доминант по амплитуде в полосе), чтобы лента по полосе = правильная циклическая динамика.
	var strongestResult *DiscoveryResult
	var dynamicsCandidates []struct {
		id   int
		amp  float64
		band int
	}
	var waveDebugPeaks []WaveClassifiedPeak
	if params.WaveDebug != nil {
		params.WaveDebug.ClassifiedPeaks = nil
		params.WaveDebug.DynamicsCandidates = nil
	}
	for _, pk := range peaks {
		if pk.Freq < minFreq || pk.AmpNorm < ampThr {
			continue
		}
		// Частота выше MaxFreqHz (20k) — не классифицируем в глифы.
		if pk.Freq > maxFreq {
			continue
		}
		if params.RunStats != nil {
			params.RunStats.PeaksTotal++
		}
		minCycles := params.WaveMinCycles
		if minCycles <= 0 {
			minCycles = DefaultWaveMinCycles
		}
		// При AllowAllWaves=true — все волны (включая фронты, переходы, вариации) идут в классификатор.
		env := computeEnvelope(x, winStart, Nwin, fs, pk.Freq)
		stationarityOK := true
		if !params.AllowAllWaves {
			acv, aslope := params.A_cv, params.A_slope
			if acv <= 0 {
				acv = 1.2
			}
			if aslope <= 0 {
				aslope = 2.0
			}
			stationarityOK = len(env) >= 8 && StationarityOK(env, fs, acv, aslope)
		}
		if len(env) < 8 || !stationarityOK {
			if params.RunStats != nil {
				params.RunStats.PeaksRejectedStationarity++
			}
			if os.Getenv("DEBUG_STATIONARITY") != "" && !params.AllowAllWaves {
				reason := "env<8"
				cv, slopeRel := float64(0), float64(0)
				if len(env) >= 8 {
					cv, slopeRel = EnvelopeCVAndSlope(env, fs)
					acv, aslope := params.A_cv, params.A_slope
					if acv <= 0 {
						acv = 1.2
					}
					if aslope <= 0 {
						aslope = 2.0
					}
					if cv >= acv {
						reason = "cv"
					} else if slopeRel >= aslope {
						reason = "slope"
					}
				}
				log.Printf("[stationarity_reject] f=%.1f cv=%.3f slopeRel=%.3f reason=%s",
					pk.Freq, cv, slopeRel, reason)
			}
			continue
		}
		bandIdx := BandContaining(pk.Freq)
		// Окно по частоте: ~minCycles циклов — на низких частотах длиннее, на высоких короче.
		nSamplesAdapt := int(float64(minCycles) / pk.Freq * fs)
		const minShapeSamples = 128
		if nSamplesAdapt < minShapeSamples {
			nSamplesAdapt = minShapeSamples
		}
		if nSamplesAdapt > Nwin {
			nSamplesAdapt = Nwin
		}

		var shape ShapeVector
		// Границы одного цикла в исходном буфере (глобальные индексы), чтобы трёхосевой глиф X/Y/Z
		// всегда резался по тому же окну, что и профиль Shape.
		shapeStart := winStart + Nwin - nSamplesAdapt
		if shapeStart < 0 {
			shapeStart = 0
		}
		cycleStartGlobal, cycleLen := 0, 0
		shapeStartBand, shapeTband := -1, 0 // границы цикла профиля (UseBands): чтобы трёхосевой глиф использовал тот же сегмент
		if params.UseBands && bandIdx >= 0 && bandIdx < DefaultContentBands && len(bandSquared[bandIdx]) >= 64 {
			bandData := bandSquared[bandIdx]
			if len(bandData) > nSamplesAdapt {
				bandData = bandData[len(bandData)-nSamplesAdapt:]
			}
			shape, shapeStartBand, shapeTband = BuildShapeWithBounds(bandData, fs, 64)
		} else {
			if shapeStart+nSamplesAdapt > len(x) {
				nSamplesAdapt = len(x) - shapeStart
				if nSamplesAdapt < 128 {
					continue
				}
			}
			shape, cycleStartGlobal, cycleLen = ExtractWaveShapeAtFreqWithBounds(x, shapeStart, nSamplesAdapt, fs, pk.Freq, minCycles)
		}
		if len(shape.Points) == 0 {
			continue
		}
		// При AllowAllWaves=false отсекаем сложные формы (>6 пересечений нуля). При true — все волны (вариации глифа) идут в классификатор.
		if !params.AllowAllWaves && !ShapeLooksCleanCycle(shape) {
			continue
		}
		// Трёхосевые глифы X/Y/Z — всегда тот же сегмент (один цикл), что и профиль (Shape), HD-версия профиля по осям.
		// При UseBands используем границы цикла из BuildShapeWithBounds; без UseBands — cycleStart/cycleLen из ExtractWaveShapeAtFreqWithBounds.
		var glyphData, glyphDataY, glyphDataZ []float64
		useBandFiltered := params.UseBands && bandIdx >= 0 && bandIdx < DefaultContentBands &&
			len(bandX[bandIdx]) >= nSamplesAdapt && len(bandY[bandIdx]) >= nSamplesAdapt && len(bandZ[bandIdx]) >= nSamplesAdapt
		if useBandFiltered && shapeStartBand >= 0 && shapeTband >= MinSamplesForGlyph {
			// Окно трёхосевого глифа = точно тот же цикл, что и профиль (shapeStartBand, shapeTband из BuildShapeWithBounds).
			bandData := bandSquared[bandIdx]
			if len(bandData) > nSamplesAdapt {
				bandData = bandData[len(bandData)-nSamplesAdapt:]
			}
			bandOffset := len(bandX[bandIdx]) - len(bandData)
			if bandOffset < 0 {
				bandOffset = 0
			}
			segStart := bandOffset + shapeStartBand
			if segStart+shapeTband <= len(bandX[bandIdx]) && segStart+shapeTband <= len(bandY[bandIdx]) && segStart+shapeTband <= len(bandZ[bandIdx]) {
				glyphData = ComputeFrozenGlyphFromSegment(bandX[bandIdx], segStart, shapeTband, fs, pk.Freq)
				glyphDataY = ComputeFrozenGlyphFromSegment(bandY[bandIdx], segStart, shapeTband, fs, pk.Freq)
				glyphDataZ = ComputeFrozenGlyphFromSegment(bandZ[bandIdx], segStart, shapeTband, fs, pk.Freq)
			}
		}
		if !useBandFiltered {
			// Тот же сегмент, что и профиль (Shape): ExtractWaveShapeAtFreqWithBounds возвращает глобальный start.
			glyphStart := cycleStartGlobal
			if cycleLen >= FrozenGlyphSegments &&
				glyphStart+cycleLen <= len(x) && glyphStart+cycleLen <= len(y) && glyphStart+cycleLen <= len(z) {
				glyphData = ComputeFrozenGlyphFromSegment(x, glyphStart, cycleLen, fs, pk.Freq)
				glyphDataY = ComputeFrozenGlyphFromSegment(y, glyphStart, cycleLen, fs, pk.Freq)
				glyphDataZ = ComputeFrozenGlyphFromSegment(z, glyphStart, cycleLen, fs, pk.Freq)
			}
			// Иначе не берём GlyphData из другого окна — continue ниже отсеет волну
		}
		if len(glyphData) < FrozenGlyphPoints {
			continue
		}
		envSlope := EnvelopeSlopePerSec(env, fs)
		meanEnv := 0.0
		for _, v := range env {
			meanEnv += v
		}
		meanEnv /= float64(len(env))
		if meanEnv < 1e-12 {
			meanEnv = 1.0
		}
		shapeSig := ComputeShapeSignature(shape, envSlope, meanEnv)
		peakProf := PeakProfileFromShape(shape)

		// Кандидат для DISCOVERY экрана по полосе (только чистые формы).
		sNorm := pk.AmpNorm / (1 + pk.AmpNorm)
		if params.AllowDiscovery && shapeStore != nil {
			shapeStore.AddDiscoveryCandidate(shape, glyphData, sNorm, 0, nowMs, bandIdx)
		}

		if params.AllowDiscovery {
			if params.RunStats != nil {
				params.RunStats.PeaksClassified++
			}
			// Merge только по ShapeSignature; частота/амплитуда — metadata. BandIdx = полоса (B0..B7 контент).
			mr := patternStore.MergeOrAddByShape(Pattern{
				ShapeSig:      shapeSig,
				BandIdx:       bandIdx,
				GlyphShape:    shape,
				GlyphData:     glyphData,
				GlyphDataY:    glyphDataY,
				GlyphDataZ:    glyphDataZ,
				Captured:      nowMs,
				Confidence:    clamp01(pk.AmpNorm),
				Freq:          pk.Freq,
				Amp:           pk.AmpNorm,
				RatioXY:       pk.ShapeXY,
				RatioXZ:       pk.ShapeXZ,
				EnvelopeSlope: envSlope,
				PeakProfile:   peakProf,
			}, mergeParams)

			if metrics != nil {
				if mr.Merged {
					metrics.RecordMerge(mr.BestID, mr.DistShapeMin)
				} else {
					reason := "fft-wave:new"
					if mr.RejectGate != "" {
						reason = fmt.Sprintf("fft-wave:new gate=%s reject_id=%d reject_d=%.3f reject_cos=%.3f", mr.RejectGate, mr.BestRejectedID, mr.BestRejectedDist, mr.BestRejectedCos)
					}
					metrics.RecordNew(mr.BestID, mr.DistShapeMin, mr.SecondBestGap, params.MinConf, 1, 0, reason, mr.RejectGate, mr.BestRejectedID, mr.BestRejectedDist, mr.BestRejectedCos)
				}
			}
			// Динамика только по locked-глифам (устойчивый символ, не кластер). Полоса для отдельной ленты по частоте.
			if params.DynamicsStore != nil && mr.BestID >= 0 && patternStore.HasGlyph(mr.BestID) && bandIdx >= 0 && bandIdx < DefaultContentBands {
				dynamicsCandidates = append(dynamicsCandidates, struct {
					id   int
					amp  float64
					band int
				}{mr.BestID, pk.AmpNorm, bandIdx})
			}
			// Trainer (event-based): один glyph-event = один цикл (как на графиках), 3 экстремума + инварианты.
			needEv := params.TrainerEventBuffer != nil && len(shape.Points) == TrainerShapePoints && mr.BestID >= 0
			var featuresForMeaning []float64
			if needEv {
				bandwidthHz := 0.1 * pk.Freq
				if bandIdx >= 0 && bandIdx < len(BandsFilterbank) {
					b := &BandsFilterbank[bandIdx]
					bandwidthHz = b.HiHz - b.LoHz
					if bandwidthHz < 1 {
						bandwidthHz = 0.1 * pk.Freq
					}
				}
				if ev, ok := ExtractTrainerEvent(shape.Points, nSamplesAdapt, glyphData, glyphDataY, glyphDataZ, pk.Freq, bandwidthHz); ok {
					ev.Shape = make([]float64, len(shape.Points))
					copy(ev.Shape, shape.Points)
					if len(glyphData) >= TrainerEventPoints {
						ev.Gx = make([]float64, TrainerEventPoints)
						copy(ev.Gx, glyphData[:TrainerEventPoints])
					}
					if len(glyphDataY) >= TrainerEventPoints {
						ev.Gy = make([]float64, TrainerEventPoints)
						copy(ev.Gy, glyphDataY[:TrainerEventPoints])
					}
					if len(glyphDataZ) >= TrainerEventPoints {
						ev.Gz = make([]float64, TrainerEventPoints)
						copy(ev.Gz, glyphDataZ[:TrainerEventPoints])
					}
					if params.TrainerEventBuffer != nil {
						params.TrainerEventBuffer.AddEvent(&ev, bandIdx, nowMs, mr.BestID)
					}
					featuresForMeaning = ev.Invariants()
				}
			} else if params.TrainerGlyphBuffer != nil && len(shape.Points) == TrainerShapePoints {
				pts := make([]float64, len(shape.Points))
				copy(pts, shape.Points)
				params.TrainerGlyphBuffer.Add(pts, bandIdx, nowMs)
			}
			// ML-очередь и VR: EventGlyph при merge/add (ampNorm уже >= ampThr).
			// VR broadcast — для каждого глифа в алфавите, без доп. фильтров (MLQueue фильтрует по Amp отдельно).
			if mr.BestID >= 0 && pk.AmpNorm >= ampThr {
				protoSig := make([]float64, PeakProfileSize)
				for i := range peakProf {
					protoSig[i] = peakProf[i]
				}
				var axisSig []float64
				if len(featuresForMeaning) >= 17 {
					axisSig = []float64{featuresForMeaning[10], featuresForMeaning[11], featuresForMeaning[12], featuresForMeaning[13]}
				} else if len(glyphData) >= 8 && len(glyphDataY) >= 8 && len(glyphDataZ) >= 8 {
					axisSig = AxisSigFromGlyphData(glyphData, glyphDataY, glyphDataZ)
				}
				eg := EventGlyph{
					T:        nowMs,
					BandIdx:  bandIdx,
					FreqHz:   pk.Freq,
					Amp:      pk.AmpNorm,
					GlyphID:  mr.BestID,
					ProtoSig: protoSig,
					AxisSig:  axisSig,
					IsNewID:  !mr.Merged,
				}
				if len(featuresForMeaning) >= 17 && len(shape.Points) >= 10 && len(glyphData) >= 10 && len(glyphDataY) >= 10 && len(glyphDataZ) >= 10 {
					eg.Shape = make([]float64, len(shape.Points))
					copy(eg.Shape, shape.Points)
					eg.Gx = make([]float64, len(glyphData))
					copy(eg.Gx, glyphData)
					eg.Gy = make([]float64, len(glyphDataY))
					copy(eg.Gy, glyphDataY)
					eg.Gz = make([]float64, len(glyphDataZ))
					copy(eg.Gz, glyphDataZ)
					eg.Features = make([]float64, len(featuresForMeaning))
					copy(eg.Features, featuresForMeaning)
				}
				if params.MLQueue != nil {
					params.MLQueue.PushEventGlyph(eg)
				}
			}
			if params.WaveDebug != nil {
				ptsLen := 0
				if len(shape.Points) > 0 {
					ptsLen = len(shape.Points)
				}
				peak := WaveClassifiedPeak{
					Freq:              pk.Freq,
					Amp:               pk.AmpNorm,
					BestID:            mr.BestID,
					Merged:            mr.Merged,
					DistMin:           mr.DistShapeMin,
					SecondGap:         mr.SecondBestGap,
					MergeReason:       mr.MergeReason,
					DistsToAll:        append([]DistID(nil), mr.DistsToAll...),
					DistsToAllWithSig: append([]DistIDWithSig(nil), mr.DistsToAllWithSig...),
					ShapeSig:          shapeSig,
					PointsLen:         ptsLen,
				}
				waveDebugPeaks = append(waveDebugPeaks, peak)
			}
		}

		// Для residual UI — берём самую сильную волну (первая в peaks — max ampNorm). Score = amp_norm для amp_norm в подписи.
		if strongestResult == nil {
			strongestResult = &DiscoveryResult{
				Segment:     nil,
				Score:       pk.AmpNorm,
				HResid:      0,
				GlyphData:   glyphData,
				ShapePoints: shape.Points,
				Valid:       true,
			}
			if patternStore != nil {
				patternStore.SetLogContextH(strongestResult.HResid)
			}
		}
	}

	if residualDecomposer != nil && strongestResult != nil {
		residualDecomposer.SetDiscoveryResult(*strongestResult)
	}
	// DYNAMICS: по полосам — по одному глифу на полосу (доминант по амплитуде в полосе), циклическая динамика сохраняется.
	// Legacy: один доминант по всем полосам → одна лента (BandIdx -1).
	usePerBandDynamics := params.DynamicsStore != nil && params.DynamicsLastIDPerBand != nil && len(params.DynamicsLastIDPerBand) >= DefaultContentBands
	var dominantID int
	var segmentPushed bool
	if usePerBandDynamics && len(dynamicsCandidates) > 0 {
		// Доминант по полосе: для каждой полосы B0..B7 выбираем лучшего по amp; гистерезис 0.65 — реже переключаемся, длиннее holds.
		segmentModePerBand := params.DynamicsSegmentStartMsPerBand != nil && len(params.DynamicsSegmentStartMsPerBand) >= DefaultContentBands
		for band := 0; band < DefaultContentBands; band++ {
			var bandCands []struct {
				id   int
				amp  float64
				band int
			}
			for _, c := range dynamicsCandidates {
				if c.band == band {
					bandCands = append(bandCands, c)
				}
			}
			if len(bandCands) == 0 {
				continue
			}
			best := bandCands[0]
			for _, c := range bandCands[1:] {
				if c.amp > best.amp {
					best = c
				}
			}
			id, amp := best.id, best.amp
			lastID := -1
			if params.DynamicsLastIDPerBand[band] >= 0 {
				lastID = params.DynamicsLastIDPerBand[band]
				for _, c := range bandCands {
					if c.id == lastID && c.amp >= 0.65*best.amp {
						id, amp = c.id, c.amp
						break
					}
				}
			}
			if segmentModePerBand {
				// Событийная модель: пушим только при смене глифа — предыдущий сегмент с DurationMs (длинные holds, меньше шума).
				if id != lastID {
					if lastID >= 0 {
						startMs := params.DynamicsSegmentStartMsPerBand[band]
						dur := nowMs - startMs
						if dur < 0 {
							dur = 0
						}
						params.DynamicsStore.Push(GlyphEvent{
							PatternID:  lastID,
							Ts:         startMs,
							Confidence: 1,
							DurationMs: dur,
							BandIdx:    band,
						})
						segmentPushed = true
					}
					params.DynamicsSegmentStartMsPerBand[band] = nowMs
				}
			} else {
				params.DynamicsStore.Push(GlyphEvent{
					PatternID:  id,
					Ts:         nowMs,
					Confidence: clamp01(amp),
					BandIdx:    band,
				})
				segmentPushed = true
			}
			params.DynamicsLastIDPerBand[band] = id
			if band == 0 {
				dominantID = id
			}
		}
	} else if params.DynamicsStore != nil && len(dynamicsCandidates) > 0 {
		// Legacy: один доминант по амплитуде, одна лента (BandIdx -1).
		best := dynamicsCandidates[0]
		for _, c := range dynamicsCandidates[1:] {
			if c.amp > best.amp {
				best = c
			}
		}
		dominantID = best.id
		dominantAmp := best.amp
		if params.DynamicsLastID != nil && *params.DynamicsLastID >= 0 {
			for _, c := range dynamicsCandidates {
				if c.id == *params.DynamicsLastID && c.amp >= 0.75*best.amp {
					dominantID = c.id
					dominantAmp = c.amp
					break
				}
			}
		}
		segmentMode := params.DynamicsSegmentStartMs != nil && params.DynamicsLastID != nil && params.DynamicsLastAmp != nil
		if segmentMode {
			if dominantID != *params.DynamicsLastID {
				if *params.DynamicsLastID >= 0 {
					dur := nowMs - *params.DynamicsSegmentStartMs
					if dur < 0 {
						dur = 0
					}
					params.DynamicsStore.Push(GlyphEvent{
						PatternID:  *params.DynamicsLastID,
						Ts:         *params.DynamicsSegmentStartMs,
						Confidence: clamp01(*params.DynamicsLastAmp),
						DurationMs: dur,
						BandIdx:    -1,
					})
					segmentPushed = true
				}
				*params.DynamicsSegmentStartMs = nowMs
			}
			*params.DynamicsLastID = dominantID
			*params.DynamicsLastAmp = dominantAmp
		} else {
			if params.DynamicsLastID != nil {
				*params.DynamicsLastID = dominantID
			}
			params.DynamicsStore.Push(GlyphEvent{
				PatternID:  dominantID,
				Ts:         nowMs,
				Confidence: clamp01(dominantAmp),
				BandIdx:    -1,
			})
			segmentPushed = true
		}
	}
	if params.WaveDebug != nil {
		params.WaveDebug.NowMs = nowMs
		params.WaveDebug.PeaksTotal = 0
		if params.RunStats != nil {
			params.WaveDebug.PeaksTotal = params.RunStats.PeaksTotal
			params.WaveDebug.Rejected = params.RunStats.PeaksRejectedStationarity
			params.WaveDebug.Classified = params.RunStats.PeaksClassified
		}
		params.WaveDebug.ClassifiedPeaks = waveDebugPeaks
		params.WaveDebug.DynamicsCandidates = make([]struct {
			ID  int
			Amp float64
		}, len(dynamicsCandidates))
		for i, c := range dynamicsCandidates {
			params.WaveDebug.DynamicsCandidates[i] = struct {
				ID  int
				Amp float64
			}{c.id, c.amp}
		}
		params.WaveDebug.DominantID = dominantID
		params.WaveDebug.SegmentPushed = segmentPushed
		if patternStore != nil {
			params.WaveDebug.LockedCount = patternStore.CountWithGlyph()
		}
	}
}
