// Экран PATTERNS: сетка устойчивых телесных откликов (иероглифы).
// Язык состояний тела — форма, плотность, асимметрия, стабильность.

package alphabet

import (
	"fmt"
	"log"
	"math"
	"sort"
	"sync"

	domainglyph "server/internal/domain/glyph"
	domainsignal "server/internal/domain/signal"
)

const PeakProfileSize = domainglyph.PeakProfileSize
const FrozenGlyphPoints = domainglyph.FrozenGlyphPoints

type ShapeSignature = domainglyph.ShapeSignature
type ShapeVector = domainglyph.ShapeVector

var ShapeDistance = domainglyph.ShapeDistance
var ShapeCosineSimilarity = domainglyph.ShapeCosineSimilarity
var ShapeVectorEMA = domainglyph.ShapeVectorEMA
var PeakProfileValid = domainglyph.PeakProfileValid
var PeakProfileDistance = domainglyph.PeakProfileDistance
var PeakProfileSameDominantHalf = domainglyph.PeakProfileSameDominantHalf

// Pattern — один shape-ID (глиф как фундамент) + metadata экземпляров.
// Идентичность = ShapeSignature (топология + класс огибающей + асимметрия). Частота/амплитуда/поза — metadata.
// BandIdx = полоса filterbank (0..5 контент, 6..7 шум только подпись); -1 = legacy без полос.
type Pattern struct {
	ID          int
	Label       string  // "Pattern 01"
	BandIdx     int     // полоса B0..B7 (контент); -1 = не по полосам
	Freq        float64 // метаданные: доминирующая частота, Гц (выше 1500 — только подпись/шум)
	Amp         float64 // метаданные: амплитуда (норма)
	RatioXY     float64 // метаданные: X/Y
	RatioXZ     float64 // X/Z
	Density     float64 // 0..1 — плотность
	Captured    int64   // unix ms, первый lock
	LastSeenTs  int64   // unix ms, последнее совпадение
	Occurrences int     // сколько раз встречался
	Confidence  float64 // 0..1
	// ShapeSig — дискретная идентичность формы. Merge только по равенству ShapeSig (конечный алфавит 20–80).
	ShapeSig ShapeSignature
	// GlyphShape — эталон формы для отрисовки; при merge перезаписывается новым (эволюция).
	GlyphShape ShapeVector
	GlyphData  []float64 // ось X (envelope)
	GlyphDataY []float64 // ось Y для режима «3 оси»
	GlyphDataZ []float64 // ось Z для режима «3 оси»
	// LastClassifiedShape — последняя волна, классифицированная в этот класс; для сравнения с новой волной (плавность, без замены паттернов местами).
	LastClassifiedShape ShapeVector
	LastClassifiedTs    int64 // unix ms; если давно — сравнение идёт с GlyphShape
	// Instance metadata (не участвуют в merge): поза/прижим меняют распределения, не shape-ID.
	EnvelopeSlope float64
	PeakProfile   [PeakProfileSize]float64
	FreqRangeHz   [2]float64
	AmpRange      [2]float64
	// LastNShapes — последние до maxLastNShapes форм, попавших в этот класс (для анимации во всплывающем окне).
	LastNShapes []ShapeVector
	// LastNGlyphX, LastNGlyphY, LastNGlyphZ — кольцо последних глифов по осям (для трёхосевой анимации в попапе).
	LastNGlyphX [][]float64
	LastNGlyphY [][]float64
	LastNGlyphZ [][]float64
	// LastNGlyphTs — таймстемпы каждого глифа в истории (для нейросети: эволюция с таймингами).
	LastNGlyphTs []int64
}

const maxLastNShapes = 256

const (
	DefaultTShape = 0.35 // |log(xy_new/xy_ref)| + |log(xz_new/xz_ref)| < T_shape (glyph_fallback)
	DefaultTGlyph = 0.20 // mean(|glyph_new - glyph_ref|) при норме 0..1 < T_glyph (glyph_fallback)
)

// GlyphLogContext — контекст для логирования глифов (f1, f2, phase, env_type, H_resid). Устанавливается перед пайплайном.
type GlyphLogContext struct {
	F1       float64
	F2       float64
	PhaseDeg int
	EnvType  string
	HResid   float64
}

// PatternStore — хранилище паттернов, потокобезопасное.
type PatternStore struct {
	mu                  sync.Mutex
	patterns            []Pattern
	nextID              int
	logCtx              *GlyphLogContext
	glyphLogger         *log.Logger
	lastMergedIDPerBand [8]int // гистерезис: последний смайтченный ID по полосам B0..B7 (−1 = нет)
}

// SetLogContext задаёт контекст для лога глифов (f1, f2, phase, env_type). HResid задаётся SetLogContextH.
func (s *PatternStore) SetLogContext(ctx *GlyphLogContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logCtx = ctx
}

// SetLogContextH обновляет H_resid в текущем контексте логирования.
func (s *PatternStore) SetLogContextH(h float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logCtx != nil {
		s.logCtx.HResid = h
	}
}

// SetGlyphLogger задаёт отдельный логгер для детального лога merge/new (например, в файл при запуске с -glyph).
func (s *PatternStore) SetGlyphLogger(l *log.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.glyphLogger = l
}

func (s *PatternStore) glyphLogf(format string, args ...interface{}) {
	if s.glyphLogger == nil {
		return
	}
	s.glyphLogger.Printf(format, args...)
}

func (s *PatternStore) Add(p Pattern) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.ID = s.nextID
	s.nextID++
	p.Label = fmt.Sprintf("Pattern %02d", p.ID+1)
	p.Occurrences = 1
	p.LastSeenTs = p.Captured
	if p.Confidence < 0 {
		p.Confidence = 0
	}
	if p.Confidence > 1 {
		p.Confidence = 1
	}
	s.patterns = append(s.patterns, p)
}

func clampConfidence(c float64) float64 {
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}

// distanceShape = |log(xy_new/xy_ref)| + |log(xz_new/xz_ref)|. Эпсилон для защиты от log(0).
func distanceShape(xyNew, xzNew, xyRef, xzRef float64) float64 {
	eps := 1e-12
	if xyRef < eps {
		xyRef = eps
	}
	if xzRef < eps {
		xzRef = eps
	}
	if xyNew < eps {
		xyNew = eps
	}
	if xzNew < eps {
		xzNew = eps
	}
	d := math.Abs(math.Log(xyNew/xyRef)) + math.Abs(math.Log(xzNew/xzRef))
	return d
}

// distanceGlyph — mean(|g_new[i] - g_ref[i]|), точки в [-1,1], нормализуем к 0..1.
// Если len разный или < FrozenGlyphPoints — возвращаем большое значение (не похожи).
func distanceGlyph(ref, new []float64) float64 {
	if len(ref) < FrozenGlyphPoints || len(new) < FrozenGlyphPoints {
		return 1e9
	}
	n := FrozenGlyphPoints
	if n > len(ref) || n > len(new) {
		n = len(ref)
		if len(new) < n {
			n = len(new)
		}
	}
	var sum float64
	for i := 0; i < n; i++ {
		r := (ref[i] + 1) * 0.5
		v := (new[i] + 1) * 0.5
		d := v - r
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return sum / float64(n)
}

// ShapeMergeParams — параметры merge (единый алгоритм, без fallback).
type ShapeMergeParams struct {
	NoMerge bool // если true — каждый пик создаёт новый токен
	// AllowCoarseSigMismatch: false (default) — merge только при совпадении coarse signature (топология+envelope+asymmetry).
	// true — разрешить merge между разными coarse signature (legacy/эксперимент).
	AllowCoarseSigMismatch bool
	MinCosForMerge         float64 // merge только если cos >= (0 = запрет инверсии)
	MaxDistForMerge        float64 // merge только если ShapeDistance < (0 = 0.65)
	MaxDistAdjacentBand    float64 // merge между соседними полосами только если ShapeDistance < (0 = 0.45)
	MaxFreqRatioForMerge   float64 // merge только если max(f_new,f_center)/min <= ratio (0 = отключить; 1.5 = разные частоты в разные глифы)
	HysteresisMargin       float64 // липкость к lastMergedIDPerBand: остаёмся, если D_last <= D_best + margin (0 = 0.05)
	UltraStrongDist        float64 // порог ultra-close для диагностики/логов (частота всё равно проверяется), 0 = 0.01
}

// DistID — пара (ID токена, расстояние) для BLIND-лога.
type DistID struct {
	ID int
	D  float64
}

// DistIDWithSig — ID, расстояние и совпадение ShapeSignature (для отладки классификатора).
type DistIDWithSig struct {
	ID       int
	D        float64
	SigMatch bool
}

// MergeResult — результат MergeOrAddByShape для метрик.
type MergeResult struct {
	Merged        bool
	DistShapeMin  float64
	BestID        int
	SecondBestGap float64
	DistsToAll    []DistID // заполняется при !Merged (до добавления), для BLIND
	// DistsToAllWithSig — расстояния до всех токенов и совпадение sig (для отладки: почему куда легло).
	DistsToAllWithSig []DistIDWithSig
	// MergeReason — причина решения для отладки: "sig" (совпадение по ShapeSig), "fallback" (merge по dist без sig), "new" (новый токен).
	MergeReason string
	// Отладка классификации по соотношению пиков: при merge — расстояние PeakProfile до выбранной карточки; при new — до ближайшей по форме (почему не влили).
	PeakProfileDist     float64
	RejectProfileReason string // при new: "profile_dist", "same_dominant", "profile_invalid", ""
	// Диагностика NEW: лучший отклонённый кандидат и первый gate, который не прошёл.
	BestRejectedID   int
	BestRejectedDist float64
	BestRejectedCos  float64
	RejectGate       string // "profile" | "signature" | "distance" | "frequency" | "cosine" | ""
}

// freqCenter возвращает центральную частоту токена (для разрешения ничьих).
func freqCenter(ex *Pattern) float64 {
	if ex.FreqRangeHz[1] > ex.FreqRangeHz[0] {
		return (ex.FreqRangeHz[0] + ex.FreqRangeHz[1]) * 0.5
	}
	if ex.Freq > 0 {
		return ex.Freq
	}
	return ex.FreqRangeHz[0]
}

// bandBoundaryMarginHz — волна в пределах margin от границы полос считается «на границе».
const bandBoundaryMarginHz = 5

// waveNearBandBoundary возвращает true, если p.Freq близко к границе между соседними полосами (в т.ч. p.BandIdx и ex.BandIdx).
func waveNearBandBoundary(p *Pattern, ex *Pattern) bool {
	if p.BandIdx < 0 || ex.BandIdx < 0 || len(domainsignal.BandsFilterbank) < 2 {
		return false
	}
	da := ex.BandIdx - p.BandIdx
	if da < 0 {
		da = -da
	}
	if da != 1 {
		return false
	}
	lo, hi := p.BandIdx, ex.BandIdx
	if lo > hi {
		lo, hi = hi, lo
	}
	if hi >= len(domainsignal.BandsFilterbank) {
		return false
	}
	boundary := domainsignal.BandsFilterbank[lo].HiHz
	delta := p.Freq - boundary
	if delta < 0 {
		delta = -delta
	}
	return delta <= bandBoundaryMarginHz
}

// atBoundaryPreferLowerBand — при волне на границе полос и двух глифах из соседних полос: предпочесть глиф из нижней полосы.
// Возвращает true, если нужно предпочесть a над b (a в нижней полосе). Одна волна (39.9 и 40.1) — всегда в один глиф.
func atBoundaryPreferLowerBand(p *Pattern, a, b *Pattern) bool {
	if a.BandIdx < 0 || b.BandIdx < 0 || a.BandIdx == b.BandIdx {
		return false
	}
	if a.BandIdx-b.BandIdx != 1 && b.BandIdx-a.BandIdx != 1 {
		return false
	}
	if !waveNearBandBoundary(p, a) && !waveNearBandBoundary(p, b) {
		return false
	}
	return a.BandIdx < b.BandIdx
}

// freqCloser возвращает true, если токен a ближе по частоте к pFreq, чем b (для стабильного выбора при похожих D).
func freqCloser(pFreq float64, a, b *Pattern) bool {
	if pFreq <= 0 {
		return false
	}
	da := pFreq - freqCenter(a)
	if da < 0 {
		da = -da
	}
	db := pFreq - freqCenter(b)
	if db < 0 {
		db = -db
	}
	return da < db
}

// ratioClose возвращает true, если RatioXY и RatioXZ пика близки к токену (|log(new/ref)| <= maxLog).
// Разные волны по позе/ориентации (напр. RatioXY 0.8 vs 1.2, RatioXZ 4.5 vs 11) не попадают в одну карточку.
func ratioClose(p, ex *Pattern, maxLog float64) bool {
	if maxLog <= 0 {
		return true
	}
	const eps = 1e-12
	refXY := ex.RatioXY
	if refXY < eps {
		refXY = eps
	}
	refXZ := ex.RatioXZ
	if refXZ < eps {
		refXZ = eps
	}
	newXY := p.RatioXY
	if newXY < eps {
		newXY = eps
	}
	newXZ := p.RatioXZ
	if newXZ < eps {
		newXZ = eps
	}
	if math.Abs(math.Log(newXY/refXY)) > maxLog {
		return false
	}
	if math.Abs(math.Log(newXZ/refXZ)) > maxLog {
		return false
	}
	return true
}

// freqInBand возвращает true, если частота пика pFreq попадает в полосу токена ex с допуском maxDelta Hz (0 = не проверять).
func freqInBand(pFreq float64, ex *Pattern, maxDelta float64) bool {
	if maxDelta <= 0 {
		return true
	}
	if pFreq <= 0 {
		return true
	}
	lo := ex.FreqRangeHz[0]
	hi := ex.FreqRangeHz[1]
	if hi <= 0 && ex.Freq > 0 {
		lo, hi = ex.Freq, ex.Freq
	}
	if hi <= 0 {
		return true
	}
	lo -= maxDelta
	hi += maxDelta
	return pFreq >= lo && pFreq <= hi
}

// freqMergeable возвращает true, если волна p может быть смержена в глиф ex по частоте.
// При валидном ex.BandIdx — допуск из domainsignal.BandFreqToleranceHz. Два условия: |f-center| <= tol И span после merge <= 2*tol (не даём ползти диапазону).
func freqMergeable(p *Pattern, ex *Pattern, maxRatio float64) bool {
	if p.Freq <= 0 {
		return true
	}
	center := freqCenter(ex)
	if center <= 0 {
		return true
	}
	// Приоритет: per-band допуск, если есть band
	if ex.BandIdx >= 0 && ex.BandIdx < len(domainsignal.BandFreqToleranceHz) {
		tol := domainsignal.BandFreqToleranceHz[ex.BandIdx]
		if tol <= 0 {
			return true
		}
		delta := p.Freq - center
		if delta < 0 {
			delta = -delta
		}
		// Простой частотный gate:
		// - обычные полосы: delta <= tol
		// - high-bands (B5+): delta <= 1.4*tol (там f_peak плавает сильнее)
		mul := 1.0
		return delta <= mul*tol
	}
	// Legacy: ratio
	if maxRatio <= 0 {
		return true
	}
	lo, hi := p.Freq, center
	if lo > hi {
		lo, hi = hi, lo
	}
	return hi/lo <= maxRatio
}

func isNoisyMergeBand(band int) bool {
	return band == 1 || band == 2 || band == 5 || band == 6
}

// Безопасный нижний порог косинуса: не даём сливать явно разные формы.
const safeCosFloor = 0.55

const (
	// Valve-style стабильность: established-глифы не должны распадаться от лёгкого jitter.
	valveStableMinOccurrences = 3
	valveSignatureFailOpenD   = 0.12
	valveSignatureFailOpenCos = 0.90
	valveFrequencyFailOpenD   = 0.05
	valveFrequencyFailOpenCos = 0.95
	// В high-bands (B5+) сигнатура дрожит сильнее: даём более широкий fail-open.
	valveHighBandSignatureFailOpenD   = 0.20
	valveHighBandSignatureFailOpenCos = 0.82
	// Probation для B5/B6: если near-duplicate отклонён gate'ом, не плодим новый ID, а прилипаем к ближайшему.
	valveHighBandProbationD   = 0.18
	valveHighBandProbationCos = 0.84
	// Для B1/B2/B4 расширяем anti-fragmentation осторожно (строже, чем в high-bands).
	valveMidBandSignatureFailOpenD   = 0.14
	valveMidBandSignatureFailOpenCos = 0.88
	valveMidBandProbationD           = 0.12
	valveMidBandProbationCos         = 0.90
)

// signatureMergeableSimple — упрощённый merge по сигнатуре:
// 1) одинаковый coarse-код — merge (в тихих полосах с тем же знаком асимметрии),
// 2) в шумных полосах допускаем соседний coarse-код при близкой форме.
func signatureMergeableSimple(pSig, exSig ShapeSignature, band int, shapeDist, shapeCos float64) bool {
	if shapeCos < safeCosFloor {
		return false
	}
	if pSig.CoarseTopologyCode == exSig.CoarseTopologyCode {
		if isNoisyMergeBand(band) {
			// В шумных полосах AsymmetrySign учитываем только при очень хорошем cos.
			if shapeCos < 0.92 {
				return true
			}
			return pSig.AsymmetrySign == exSig.AsymmetrySign
		}
		return pSig.AsymmetrySign == exSig.AsymmetrySign
	}
	if !isNoisyMergeBand(band) {
		return false
	}
	// Соседние coarse-коды больше не считаем вариациями одного класса для повышения разнообразия.
	return false
}

// signatureFailOpenAllowed — мягкий режим для near-miss по сигнатуре:
// разрешаем merge только у очень близкой формы и только при близкой топологии.
func signatureFailOpenAllowed(pSig, exSig ShapeSignature, band int, shapeDist, shapeCos float64) bool {
	maxD := valveSignatureFailOpenD
	minCos := valveSignatureFailOpenCos
	if band >= 5 {
		maxD = valveHighBandSignatureFailOpenD
		minCos = valveHighBandSignatureFailOpenCos
	} else if band == 1 || band == 2 || band == 4 {
		maxD = valveMidBandSignatureFailOpenD
		minCos = valveMidBandSignatureFailOpenCos
	}
	if shapeDist > maxD || shapeCos < minCos {
		return false
	}
	// Безопасный случай: та же coarse-топология, но сигнатура дрогнула по E/A/M.
	if pSig.TopologyOnlyEqual(exSig) {
		return true
	}
	// Допускаем соседнюю coarse-корзину только при очень близкой форме.
	if pSig.NearCoarseEqual(exSig) {
		return shapeDist <= 0.08 && shapeCos >= 0.94
	}
	return false
}

// highBandProbationMergeAllowed — anti-fragmentation для B5/B6:
// если near-duplicate отклонён gate'ом, сливаем в ближайший глиф вместо создания нового.
func highBandProbationMergeAllowed(p *Pattern, ex *Pattern, rejectGate string, shapeDist, shapeCos float64) bool {
	if p.BandIdx < 5 || ex.BandIdx < 5 || p.BandIdx != ex.BandIdx {
		return false
	}
	if rejectGate != "signature" && rejectGate != "frequency" {
		return false
	}
	if shapeDist > valveHighBandProbationD || shapeCos < valveHighBandProbationCos {
		return false
	}
	if p.ShapeSig.TopologyOnlyEqual(ex.ShapeSig) {
		return true
	}
	return p.ShapeSig.NearCoarseEqual(ex.ShapeSig)
}

func midBandProbationMergeAllowed(p *Pattern, ex *Pattern, rejectGate string, shapeDist, shapeCos float64) bool {
	if !(p.BandIdx == 1 || p.BandIdx == 2 || p.BandIdx == 4) {
		return false
	}
	if p.BandIdx != ex.BandIdx {
		return false
	}
	if rejectGate != "signature" && rejectGate != "frequency" {
		return false
	}
	if shapeDist > valveMidBandProbationD || shapeCos < valveMidBandProbationCos {
		return false
	}
	// В mid-bands разрешаем только ту же coarse-топологию (без near-coarse), чтобы не размывать классы.
	return p.ShapeSig.TopologyOnlyEqual(ex.ShapeSig)
}

// frequencyFailOpenAllowed — мягкий режим для near-miss по частоте:
// у уже стабильного глифа и почти идеальной формы разрешаем merge, даже если freq gate не прошёл.
func frequencyFailOpenAllowed(p *Pattern, ex *Pattern, shapeDist, shapeCos float64) bool {
	if ex.Occurrences < valveStableMinOccurrences {
		return false
	}
	if p.BandIdx < 0 || ex.BandIdx < 0 || p.BandIdx != ex.BandIdx {
		return false
	}
	if shapeDist > valveFrequencyFailOpenD || shapeCos < valveFrequencyFailOpenCos {
		return false
	}
	// Не склеиваем разные топологические классы только из-за частоты.
	return p.ShapeSig.TopologyOnlyEqual(ex.ShapeSig)
}

// stickyLastMergeAllowed — если это последний выбранный глиф полосы и форма всё ещё очень близка,
// сохраняем трек даже при minor gate mismatch.
func stickyLastMergeAllowed(p *Pattern, ex *Pattern, shapeDist, shapeCos float64) bool {
	if ex.Occurrences < 2 {
		return false
	}
	if p.BandIdx < 0 || ex.BandIdx < 0 || p.BandIdx != ex.BandIdx {
		return false
	}
	if shapeDist > 0.10 || shapeCos < 0.90 {
		return false
	}
	return p.ShapeSig.TopologyOnlyEqual(ex.ShapeSig)
}

// freqMergeableRelaxedForStrongShape — мягкое расширение частотного gate для почти идеального shape-match.
// Используется только в same-band и только при строгом sig-совпадении (не near-signature), чтобы не терять повторяемость.
func freqMergeableRelaxedForStrongShape(p *Pattern, ex *Pattern, shapeDist, shapeCos float64, sigStrict bool) bool {
	if !sigStrict || p.BandIdx < 0 || ex.BandIdx < 0 || p.BandIdx != ex.BandIdx {
		return false
	}
	if ex.BandIdx < 0 || ex.BandIdx >= len(domainsignal.BandFreqToleranceHz) {
		return false
	}
	relaxedTolMul := 1.2
	relaxedSpanMul := 3.0
	strongEnough := shapeDist <= 0.06 && shapeCos >= 0.96
	// Для высоких полос (B5+) допускаем более мягкий частотный допуск при близкой форме:
	// там f_peak плавает заметно сильнее, а жёсткий freq-gate плодит NEW при почти одинаковом shape.
	if ex.BandIdx >= 5 {
		strongEnough = shapeDist <= 0.12 && shapeCos >= 0.90
		relaxedTolMul = 1.35
		relaxedSpanMul = 3.5
	}
	if !strongEnough {
		return false
	}
	tol := domainsignal.BandFreqToleranceHz[ex.BandIdx]
	if tol <= 0 {
		return false
	}
	center := freqCenter(ex)
	if p.Freq <= 0 || center <= 0 {
		return true
	}
	// Мягкое расширение допуска для близкой формы: убирает ложные NEW при jitter f_peak.
	relaxedTol := relaxedTolMul * tol
	delta := p.Freq - center
	if delta < 0 {
		delta = -delta
	}
	if delta > relaxedTol {
		return false
	}
	// Разрешаем шире span для сильного shape-match, но не даём бесконтрольный дрейф.
	lo, hi := ex.FreqRangeHz[0], ex.FreqRangeHz[1]
	if hi <= 0 && ex.Freq > 0 {
		lo, hi = ex.Freq, ex.Freq
	}
	if hi > 0 {
		newLo := lo
		if p.Freq < newLo {
			newLo = p.Freq
		}
		newHi := hi
		if p.Freq > newHi {
			newHi = p.Freq
		}
		if newHi-newLo > relaxedSpanMul*tol {
			return false
		}
	}
	return true
}

// bandsMergeable возвращает (ok, needStrictDist): ok = можно рассматривать для merge; needStrictDist = для соседних полос нужен строже maxDist.
func bandsMergeable(ex, p *Pattern) (ok bool, needStrictDist bool) {
	if ex.BandIdx < 0 && p.BandIdx < 0 {
		return true, false
	}
	if ex.BandIdx < 0 || p.BandIdx < 0 {
		return false, false
	}
	if ex.BandIdx == p.BandIdx {
		return true, false
	}
	// Склейка соседних полос отключена для повышения разнообразия (high diversity mode).
	return false, false
}

// comparisonShape возвращает форму для сравнения: GlyphShape (прототип). Сравнение с эталоном, а не с последней вариацией — меньше распада по дрейфу.
func comparisonShape(ex *Pattern, _ int64) ShapeVector {
	return ex.GlyphShape
}

// glyphEmaAlpha возвращает alpha для EMA прототипа (доля старого): при сильном матче (низкий d) — больше нового.
func glyphEmaAlpha(bestD float64) float64 {
	if bestD < 0.2 {
		return 0.8 // 20% нового при очень близком матче
	}
	if bestD < 0.35 {
		return 0.9 // 10% нового
	}
	return 0.95 // 5% нового — инерция, меньше дерганья
}

// applyMergeUpdate — общая логика merge. GlyphShape обновляется EMA (инерция), не полной заменой — меньше ping-pong.
func applyMergeUpdate(_ *PatternStore, ex *Pattern, p *Pattern, bestD float64) {
	ex.Amp = 0.5*ex.Amp + 0.5*p.Amp
	ex.RatioXY = 0.5*ex.RatioXY + 0.5*p.RatioXY
	ex.RatioXZ = 0.5*ex.RatioXZ + 0.5*p.RatioXZ
	ex.LastSeenTs = p.Captured
	ex.Occurrences++
	ex.Confidence = 0.5*ex.Confidence + 0.5*p.Confidence
	ex.EnvelopeSlope = 0.5*ex.EnvelopeSlope + 0.5*p.EnvelopeSlope
	ex.PeakProfile = p.PeakProfile
	if len(p.GlyphShape.Points) > 0 {
		ex.LastClassifiedShape = ShapeVector{Points: append([]float64(nil), p.GlyphShape.Points...)}
		ex.LastClassifiedTs = p.Captured
		// Инерционное обновление прототипа — не полная замена, меньше перескоков между близкими глифами.
		if len(ex.GlyphShape.Points) == len(p.GlyphShape.Points) {
			ShapeVectorEMA(&ex.GlyphShape, p.GlyphShape, glyphEmaAlpha(bestD))
		} else {
			ex.GlyphShape = ShapeVector{Points: append([]float64(nil), p.GlyphShape.Points...)}
		}
		ex.LastNShapes = append(ex.LastNShapes, ShapeVector{Points: append([]float64(nil), p.GlyphShape.Points...)})
		if len(ex.LastNShapes) > maxLastNShapes {
			ex.LastNShapes = ex.LastNShapes[len(ex.LastNShapes)-maxLastNShapes:]
		}
	}
	if len(p.GlyphData) >= FrozenGlyphPoints {
		ex.GlyphData = append([]float64(nil), p.GlyphData...)
		ex.LastNGlyphX = append(ex.LastNGlyphX, append([]float64(nil), p.GlyphData...))
		if len(ex.LastNGlyphX) > maxLastNShapes {
			ex.LastNGlyphX = ex.LastNGlyphX[len(ex.LastNGlyphX)-maxLastNShapes:]
		}
		ex.LastNGlyphTs = append(ex.LastNGlyphTs, p.Captured)
		if len(ex.LastNGlyphTs) > maxLastNShapes {
			ex.LastNGlyphTs = ex.LastNGlyphTs[len(ex.LastNGlyphTs)-maxLastNShapes:]
		}
	}
	if len(p.GlyphDataY) >= FrozenGlyphPoints {
		ex.GlyphDataY = append([]float64(nil), p.GlyphDataY...)
		ex.LastNGlyphY = append(ex.LastNGlyphY, append([]float64(nil), p.GlyphDataY...))
		if len(ex.LastNGlyphY) > maxLastNShapes {
			ex.LastNGlyphY = ex.LastNGlyphY[len(ex.LastNGlyphY)-maxLastNShapes:]
		}
	}
	if len(p.GlyphDataZ) >= FrozenGlyphPoints {
		ex.GlyphDataZ = append([]float64(nil), p.GlyphDataZ...)
		ex.LastNGlyphZ = append(ex.LastNGlyphZ, append([]float64(nil), p.GlyphDataZ...))
		if len(ex.LastNGlyphZ) > maxLastNShapes {
			ex.LastNGlyphZ = ex.LastNGlyphZ[len(ex.LastNGlyphZ)-maxLastNShapes:]
		}
	}
	if p.Freq > 0 {
		if ex.FreqRangeHz[0] == 0 || p.Freq < ex.FreqRangeHz[0] {
			ex.FreqRangeHz[0] = p.Freq
		}
		if p.Freq > ex.FreqRangeHz[1] {
			ex.FreqRangeHz[1] = p.Freq
		}
	}
	if p.Amp > 0 {
		if ex.AmpRange[0] == 0 || p.Amp < ex.AmpRange[0] {
			ex.AmpRange[0] = p.Amp
		}
		if p.Amp > ex.AmpRange[1] {
			ex.AmpRange[1] = p.Amp
		}
	}
}

// MergeOrAddByShape — единый алгоритм: same band + ShapeDistance < maxDist + cos >= minCos + PeakProfileSameDominantHalf.
// Глиф при merge перезаписывается новым (эволюция), история с таймингами — LastNGlyphTs.
func (s *PatternStore) MergeOrAddByShape(p Pattern, params ShapeMergeParams) MergeResult {
	res := MergeResult{BestID: -1, DistShapeMin: 1e9, BestRejectedID: -1, BestRejectedDist: 1e9}
	s.mu.Lock()
	defer s.mu.Unlock()
	p.Confidence = clampConfidence(p.Confidence)

	if params.NoMerge {
		s.addNewPatternLocked(&p, &res)
		return res
	}
	if len(p.GlyphShape.Points) == 0 {
		s.addNewPatternLocked(&p, &res)
		res.MergeReason = "new"
		return res
	}

	maxDist := params.MaxDistForMerge
	if maxDist <= 0 {
		maxDist = 0.65
	}
	maxDistAdjacentBand := params.MaxDistAdjacentBand
	if maxDistAdjacentBand <= 0 {
		maxDistAdjacentBand = 0.45
	}
	hysteresisMargin := params.HysteresisMargin
	if hysteresisMargin <= 0 {
		hysteresisMargin = 0.05
	}
	minCos := params.MinCosForMerge
	if minCos > 1 {
		minCos = 1
	}
	if params.MaxFreqRatioForMerge <= 0 {
		params.MaxFreqRatioForMerge = 1.5
	}
	ultraStrongDist := params.UltraStrongDist
	if ultraStrongDist <= 0 {
		ultraStrongDist = 0.01
	}
	var bestD, secondD float64 = 1e9, 1e9
	var bestCos float64
	bestIdx := -1
	bestFallbackIdx := -1
	bestFallbackD := 1e9
	bestFallbackCos := 0.0
	bestFallbackReason := ""
	var lastMergedIdx int = -1
	var lastMergedD float64 = 1e9
	const tieEpsilon = 0.02
	validP := PeakProfileValid(p.PeakProfile)
	band := p.BandIdx
	if band < 0 || band >= 8 {
		band = 0
	}
	lastID := s.lastMergedIDPerBand[band]

	for i := range s.patterns {
		ex := &s.patterns[i]
		if len(ex.GlyphShape.Points) == 0 {
			continue
		}
		bandOk, needStrict := bandsMergeable(ex, &p)
		if !bandOk {
			continue
		}
		distThr := maxDist
		if needStrict {
			distThr = maxDistAdjacentBand
		}
		ref := comparisonShape(ex, p.Captured)
		d := ShapeDistance(p.GlyphShape, ref)

		// Profile не блокирует merge: используем его только как метрику похожести.
		profileOK := true
		cos := ShapeCosineSimilarity(p.GlyphShape, ref)
		safeCosOK := cos >= safeCosFloor
		sigOK := params.AllowCoarseSigMismatch || signatureMergeableSimple(p.ShapeSig, ex.ShapeSig, p.BandIdx, d, cos)
		distOK := d < distThr
		freqOK := freqMergeable(&p, ex, params.MaxFreqRatioForMerge)
		cosOK := cos >= minCos
		res.DistsToAll = append(res.DistsToAll, DistID{ID: ex.ID, D: d})
		res.DistsToAllWithSig = append(res.DistsToAllWithSig, DistIDWithSig{ID: ex.ID, D: d, SigMatch: sigOK})

		if !(profileOK && safeCosOK && sigOK && distOK && freqOK && cosOK) {
			/* Fallback disabled for higher diversity
			fallbackReason := ""
			if profileOK && safeCosOK && distOK && cosOK {
				if !sigOK && signatureFailOpenAllowed(p.ShapeSig, ex.ShapeSig, p.BandIdx, d, cos) && ex.Occurrences >= valveStableMinOccurrences {
					fallbackReason = "signature_failopen"
				} else if !freqOK && frequencyFailOpenAllowed(&p, ex, d, cos) {
					fallbackReason = "frequency_failopen"
				} else if ex.ID == lastID && stickyLastMergeAllowed(&p, ex, d, cos) {
					fallbackReason = "sticky_last"
				}
			}
			if fallbackReason != "" {
				if bestFallbackIdx < 0 || d < bestFallbackD || (bestFallbackIdx >= 0 && d <= bestFallbackD+tieEpsilon && ex.ID == lastID) {
					bestFallbackIdx = i
					bestFallbackD = d
					bestFallbackCos = cos
					bestFallbackReason = fallbackReason
				}
			}
			*/
			if d < res.BestRejectedDist {
				res.BestRejectedDist = d
				res.BestRejectedID = ex.ID
				res.BestRejectedCos = cos
				switch {
				case !profileOK:
					res.RejectGate = "profile"
				case !safeCosOK:
					res.RejectGate = "safe_cos"
				case !sigOK:
					res.RejectGate = "signature"
				case !distOK:
					res.RejectGate = "distance"
				case !freqOK:
					res.RejectGate = "frequency"
				case !cosOK:
					res.RejectGate = "cosine"
				default:
					res.RejectGate = "unknown"
				}
			}
			continue
		}
		if ex.ID == lastID {
			lastMergedIdx = i
			lastMergedD = d
		}
		if d < bestD {
			secondD, bestD = bestD, d
			bestIdx = i
			bestCos = cos
		} else if d < secondD {
			secondD = d
		} else if bestIdx >= 0 && d <= bestD+tieEpsilon && (atBoundaryPreferLowerBand(&p, ex, &s.patterns[bestIdx]) || freqCloser(p.Freq, ex, &s.patterns[bestIdx])) {
			bestIdx = i
			bestD = d
			bestCos = cos
		}
	}

	// Гистерезис: если прошлый глиф для этой полосы ещё проходит и почти не хуже лучшего — остаёмся на нём.
	if bestIdx >= 0 && lastMergedIdx >= 0 && lastMergedD <= bestD+hysteresisMargin {
		bestIdx = lastMergedIdx
		bestD = lastMergedD
	}
	if bestIdx < 0 && bestFallbackIdx >= 0 {
		bestIdx = bestFallbackIdx
		bestD = bestFallbackD
		bestCos = bestFallbackCos
	}
	// Probation: near-duplicate после reject не создаёт новый ID.
	// ОТКЛЮЧЕНО для повышения разнообразия (high diversity mode)
	/*
	if bestIdx < 0 && res.BestRejectedID >= 0 {
		for i := range s.patterns {
			ex := &s.patterns[i]
			if ex.ID != res.BestRejectedID {
				continue
			}
			if highBandProbationMergeAllowed(&p, ex, res.RejectGate, res.BestRejectedDist, res.BestRejectedCos) {
				bestIdx = i
				bestD = res.BestRejectedDist
				bestCos = res.BestRejectedCos
				bestFallbackReason = "probation"
			} else if midBandProbationMergeAllowed(&p, ex, res.RejectGate, res.BestRejectedDist, res.BestRejectedCos) {
				bestIdx = i
				bestD = res.BestRejectedDist
				bestCos = res.BestRejectedCos
				bestFallbackReason = "probation"
			}
			break
		}
	}
	*/

	if bestIdx >= 0 {
		ex := &s.patterns[bestIdx]
		if validP && PeakProfileValid(ex.PeakProfile) {
			res.PeakProfileDist = PeakProfileDistance(p.PeakProfile, ex.PeakProfile)
		} else {
			res.PeakProfileDist = -1
		}
		applyMergeUpdate(nil, ex, &p, bestD)
		res.Merged = true
		res.DistShapeMin = bestD
		res.BestID = ex.ID
		if secondD >= 1e8 {
			res.SecondBestGap = -1
		} else {
			res.SecondBestGap = secondD - bestD
			if res.SecondBestGap < 0 {
				res.SecondBestGap = 0
			}
		}
		if bestFallbackReason != "" {
			res.MergeReason = "fallback"
		} else {
			res.MergeReason = "sig"
		}
		s.lastMergedIDPerBand[band] = ex.ID
		if s.logCtx != nil {
			df := math.Abs(s.logCtx.F1 - s.logCtx.F2)
			s.glyphLogf(
				"glyph_log action=merge glyph_id=%d band=%d f_peak=%.2f amp=%.4f shape_distance=%.4f shape_cos=%.4f second_gap=%.4f profile_dist=%.4f dist_thr=%.4f adjacent_dist_thr=%.4f min_cos=%.4f max_freq_ratio=%.4f hysteresis=%.4f ultra_strong=%.4f shape_sig_coarse=%d shape_sig_topo=%d shape_sig_env=%d shape_sig_asym=%d shape_sig_mod=%d f1=%.2f f2=%.2f delta_f=%.2f phase=%d env_type=%s residual_entropy=%.4f occurrences=%d",
				ex.ID, p.BandIdx, p.Freq, p.Amp, bestD, bestCos, res.SecondBestGap, res.PeakProfileDist,
				maxDist, maxDistAdjacentBand, minCos, params.MaxFreqRatioForMerge, hysteresisMargin, ultraStrongDist,
				p.ShapeSig.CoarseTopologyCode, p.ShapeSig.TopologyCode, p.ShapeSig.EnvelopeClass, p.ShapeSig.AsymmetrySign, p.ShapeSig.ModulationFlag,
				s.logCtx.F1, s.logCtx.F2, df, s.logCtx.PhaseDeg, s.logCtx.EnvType, s.logCtx.HResid, ex.Occurrences,
			)
		}
		return res
	}

	res.DistShapeMin = bestD
	if secondD >= 1e8 {
		res.SecondBestGap = -1
	} else {
		res.SecondBestGap = secondD - bestD
		if res.SecondBestGap < 0 {
			res.SecondBestGap = 0
		}
	}
	s.addNewPatternLocked(&p, &res)
	s.lastMergedIDPerBand[band] = p.ID
	res.MergeReason = "new"
	return res
}

// addNewPatternLocked добавляет новый паттерн. Вызывать только при удержанном s.mu.
func (s *PatternStore) addNewPatternLocked(p *Pattern, res *MergeResult) {
	p.ID = s.nextID
	s.nextID++
	p.Label = fmt.Sprintf("Pattern %02d", p.ID+1)
	p.Occurrences = 1
	p.LastSeenTs = p.Captured
	p.FreqRangeHz = [2]float64{p.Freq, p.Freq}
	p.AmpRange = [2]float64{p.Amp, p.Amp}
	if len(p.GlyphShape.Points) > 0 {
		p.LastClassifiedShape = ShapeVector{Points: append([]float64(nil), p.GlyphShape.Points...)}
		p.LastClassifiedTs = p.Captured
	}
	s.patterns = append(s.patterns, *p)
	res.BestID = p.ID
	if s.logCtx != nil {
		df := math.Abs(s.logCtx.F1 - s.logCtx.F2)
		nearestDist := res.DistShapeMin
		if nearestDist >= 1e8 {
			nearestDist = -1
		}
		rejectDist := res.BestRejectedDist
		if rejectDist >= 1e8 {
			rejectDist = -1
		}
		s.glyphLogf(
			"glyph_log action=new glyph_id=%d band=%d f_peak=%.2f amp=%.4f nearest_shape_distance=%.4f second_gap=%.4f reject_gate=%s reject_id=%d reject_dist=%.4f reject_cos=%.4f shape_sig_coarse=%d shape_sig_topo=%d shape_sig_env=%d shape_sig_asym=%d shape_sig_mod=%d f1=%.2f f2=%.2f delta_f=%.2f phase=%d env_type=%s residual_entropy=%.4f occurrences=1",
			p.ID, p.BandIdx, p.Freq, p.Amp, nearestDist, res.SecondBestGap, res.RejectGate, res.BestRejectedID, rejectDist, res.BestRejectedCos,
			p.ShapeSig.CoarseTopologyCode, p.ShapeSig.TopologyCode, p.ShapeSig.EnvelopeClass, p.ShapeSig.AsymmetrySign, p.ShapeSig.ModulationFlag,
			s.logCtx.F1, s.logCtx.F2, df, s.logCtx.PhaseDeg, s.logCtx.EnvType, s.logCtx.HResid,
		)
	}
}

// ReplaceGlyphByID заменяет GlyphData у паттерна с заданным ID. Только по явному действию (кнопка "Replace glyph").
func (s *PatternStore) ReplaceGlyphByID(id int, glyph []float64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.patterns {
		if s.patterns[i].ID != id {
			continue
		}
		if len(glyph) >= FrozenGlyphPoints {
			s.patterns[i].GlyphData = append([]float64(nil), glyph...)
			return true
		}
		return false
	}
	return false
}

func (s *PatternStore) List() []Pattern {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Pattern, len(s.patterns))
	copy(out, s.patterns)
	return out
}

// ListOrderedByTop возвращает паттерны, упорядоченные по топу: сначала по Occurrences (убыв.), затем по ID (возр.).
// ID как tie-break делает порядок стабильным между кадрами (без прыжков при обновлении LastSeenTs).
func (s *PatternStore) ListOrderedByTop() []Pattern {
	out := s.List()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Occurrences != out[j].Occurrences {
			return out[i].Occurrences > out[j].Occurrences
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// RemoveStale удаляет паттерны, не обновлявшиеся дольше ttlSec секунд (по LastSeenTs).
// nowMs — текущее время unix ms. ttlSec <= 0 — не удалять. Возвращает число удалённых.
func (s *PatternStore) RemoveStale(nowMs int64, ttlSec int) int {
	if ttlSec <= 0 {
		return 0
	}
	ttlMs := int64(ttlSec) * 1000
	s.mu.Lock()
	defer s.mu.Unlock()
	var kept []Pattern
	removed := 0
	for i := range s.patterns {
		if nowMs-s.patterns[i].LastSeenTs > ttlMs {
			removed++
			// Сбросить гистерезис по полосам, если удалённый ID был там
			id := s.patterns[i].ID
			for b := 0; b < 8; b++ {
				if s.lastMergedIDPerBand[b] == id {
					s.lastMergedIDPerBand[b] = -1
				}
			}
			continue
		}
		kept = append(kept, s.patterns[i])
	}
	s.patterns = kept
	return removed
}

func (s *PatternStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.patterns)
}

// CountWithGlyph возвращает число токенов с заполненным GlyphData (сколько «идёт в глифы»).
func (s *PatternStore) CountWithGlyph() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for i := range s.patterns {
		if len(s.patterns[i].GlyphData) >= FrozenGlyphPoints {
			n++
		}
	}
	return n
}

// HasGlyph возвращает true, если паттерн с id имеет устойчивый глиф (GlyphData заполнен).
// Только такие паттерны участвуют в динамике — переходы между событиями, а не между сэмплами.
func (s *PatternStore) HasGlyph(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.patterns {
		if s.patterns[i].ID == id {
			return len(s.patterns[i].GlyphData) >= FrozenGlyphPoints
		}
	}
	return false
}

// Get возвращает копию паттерна по ID.
func (s *PatternStore) Get(id int) (Pattern, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.patterns {
		if s.patterns[i].ID == id {
			return s.patterns[i], true
		}
	}
	return Pattern{}, false
}

// DrawAlphabetScreen рисует вкладку ALPHABET: блок Lock settings, переключатель вида (Combined / 3 оси), сетка токенов (иероглифы), help при пустом списке.
// w уже равна ширине левого блока (до правой панели), двойной вычет pw не нужен.
