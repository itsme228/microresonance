// ShapeRepresentation: фаза-нормализованная форма цикла (ShapeVector).
// Инвариантность к частоте и амплитуде.

package glyph

import (
	"math"
	"sort"
)

const (
	DefaultPointsPerCycle = 128
	shapeEps              = 1e-12
	// TrainerShapePoints — длина формы, которую пайплайн передаёт в буфер калибровки (BuildShape(...,64), ExtractWaveShapeAtFreq→64). Контракт: только такие глифы попадают в датасет.
	TrainerShapePoints = 64
	// PeaksPerCycle — число вершин (пиков) в одном цикле глифа. Сегмент = от дна до дна с PeaksPerCycle вершинами внутри.
	PeaksPerCycle = 3
)

// ShapeVector — форма одного цикла в нормализованной фазе (0..1).
// Независимость от фазы: один цикл в фазе 0..1, без привязки к времени — ровные с ровными, хвосты с хвостами.
// Points: 64 или 128 значений в [-1, 1].
type ShapeVector struct {
	Points      []float64
	PointsSigma []float64
	Envelope    []float64
}

// GetCycleBounds возвращает границы одного цикла в окне (то же, что использует BuildShape): start, T.
// Нужно, чтобы трёхосевой глиф X/Y/Z использовал тот же сегмент, что и профиль (Shape).
func GetCycleBounds(window []float64) (start, T int) {
	if len(window) < 16 {
		return 0, 0
	}
	T, start = periodFromExtrema(window)
	if T < 4 || start < 0 || start+T > len(window) {
		T, start = periodFromZeroCrossings(window)
	}
	if T < 4 || start < 0 || start+T > len(window) {
		T = estimatePeriodSamples(window)
		if T < 4 {
			T = len(window) / 2
			if T < 4 {
				T = 4
			}
		}
		if T > len(window)/2 {
			T = len(window) / 2
		}
		start = (len(window) - T) / 2
		if start < 0 {
			start = 0
		}
	}
	end := start + T
	if end > len(window) {
		end = len(window)
		T = end - start
	}
	if T < 4 {
		return 0, 0
	}
	return start, T
}

// buildShapeFromSegment строит ShapeVector из уже вырезанного сегмента одного цикла (resample по фазе + тренд + нормализация).
func buildShapeFromSegment(segment []float64, pointsPerCycle int) ShapeVector {
	if len(segment) < 4 || pointsPerCycle < 4 {
		return ShapeVector{}
	}
	points := make([]float64, pointsPerCycle)
	for i := 0; i < pointsPerCycle; i++ {
		phase := float64(i) / float64(pointsPerCycle)
		idx := phase * float64(len(segment)-1)
		if idx < 0 {
			idx = 0
		}
		if idx >= float64(len(segment)-1) {
			idx = float64(len(segment) - 1)
		}
		i0 := int(idx)
		i1 := i0 + 1
		if i1 >= len(segment) {
			i1 = len(segment) - 1
		}
		frac := idx - float64(i0)
		points[i] = segment[i0]*(1-frac) + segment[i1]*frac
	}
	n := len(points)
	if n < 2 {
		return ShapeVector{}
	}
	p0, pN := points[0], points[n-1]
	for i := 0; i < n; i++ {
		t := 0.0
		if n > 1 {
			t = float64(i) / float64(n-1)
		}
		points[i] -= p0 + (pN-p0)*t
	}
	maxAbs := shapeEps
	for _, v := range points {
		a := v
		if a < 0 {
			a = -a
		}
		if a > maxAbs {
			maxAbs = a
		}
	}
	if maxAbs < shapeEps {
		maxAbs = shapeEps
	}
	for i := range points {
		points[i] /= maxAbs
		if points[i] < -1 {
			points[i] = -1
		}
		if points[i] > 1 {
			points[i] = 1
		}
	}
	return ShapeVector{Points: points}
}

// BuildShape строит ShapeVector из окна: один цикл = 2 пересечения нуля (одно направление) → resample по фазе → нормализация.
func BuildShape(window []float64, Fs float64, pointsPerCycle int) ShapeVector {
	shape, _, _ := BuildShapeWithBounds(window, Fs, pointsPerCycle)
	return shape
}

// BuildShapeWithBounds строит Shape и возвращает границы цикла (start, T), чтобы трёхосевой глиф использовал тот же сегмент (HD-версия профиля).
func BuildShapeWithBounds(window []float64, Fs float64, pointsPerCycle int) (ShapeVector, int, int) {
	if len(window) < 16 || Fs <= 0 || pointsPerCycle < 4 {
		return ShapeVector{}, 0, 0
	}
	if pointsPerCycle <= 0 {
		pointsPerCycle = DefaultPointsPerCycle
	}
	start, T := GetCycleBounds(window)
	if T < 4 || start+T > len(window) {
		return ShapeVector{}, 0, 0
	}
	end := start + T
	if end > len(window) {
		end = len(window)
		T = end - start
	}
	segment := window[start:end]
	if len(segment) < 4 {
		return ShapeVector{}, 0, 0
	}
	return buildShapeFromSegment(segment, pointsPerCycle), start, T
}

func PeriodFromExtrema(s []float64) (T int, start int) {
	return periodFromExtrema(s)
}

func PeriodFromZeroCrossings(s []float64) (T int, start int) {
	return periodFromZeroCrossings(s)
}

func EstimatePeriodSamples(s []float64) int {
	return estimatePeriodSamples(s)
}

// findTroughs возвращает индексы локальных минимумов (дна) с минимальным расстоянием minDist между ними.
func findTroughs(s []float64, minDist int) []int {
	if len(s) < 3 || minDist < 1 {
		return nil
	}
	var out []int
	for i := 1; i < len(s)-1; i++ {
		if s[i] > s[i-1] || s[i] > s[i+1] {
			continue
		}
		if len(out) > 0 && i-out[len(out)-1] < minDist {
			if s[i] < s[out[len(out)-1]] {
				out[len(out)-1] = i
			}
			continue
		}
		out = append(out, i)
	}
	return out
}

// findPeaks возвращает индексы локальных максимумов (вершин) с минимальным расстоянием minDist.
func findPeaks(s []float64, minDist int) []int {
	if len(s) < 3 || minDist < 1 {
		return nil
	}
	var out []int
	for i := 1; i < len(s)-1; i++ {
		if s[i] < s[i-1] || s[i] < s[i+1] {
			continue
		}
		if len(out) > 0 && i-out[len(out)-1] < minDist {
			if s[i] > s[out[len(out)-1]] {
				out[len(out)-1] = i
			}
			continue
		}
		out = append(out, i)
	}
	return out
}

// periodFromExtrema возвращает длину сегмента и старт по экстремумам.
// Сегмент = от дна до дна с PeaksPerCycle вершинами внутри (границы в нижней точке).
func periodFromExtrema(s []float64) (T int, start int) {
	if len(s) < 16 {
		return 0, 0
	}
	const minDist = 4
	troughs := findTroughs(s, minDist)
	needTroughs := PeaksPerCycle + 1
	if len(troughs) < needTroughs {
		return 0, 0
	}
	// Берём последние needTroughs доньев — один цикл с PeaksPerCycle вершинами.
	start = troughs[len(troughs)-needTroughs]
	end := troughs[len(troughs)-1]
	T = end - start + 1
	if T < 4 {
		return 0, 0
	}
	if end >= len(s) {
		return 0, 0
	}
	return T, start
}

// stabilizedZero возвращает стабилизированный ноль для подсчёта пересечений.
// При дрейфе волны ноль считаем по последним 10% окна (текущий базовый уровень).
// Короткий хвост лучше при быстром дрейфе — последняя треть ещё смешивала старый и новый уровень.
func stabilizedZero(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	// Последние 10% окна — текущий уровень; минимум 32 сэмпла для устойчивой медианы
	const minTail = 32
	tailStart := len(s) * 9 / 10
	if len(s)-tailStart < minTail {
		tailStart = len(s) - minTail
	}
	if tailStart < 0 {
		tailStart = 0
	}
	tail := s[tailStart:]
	cp := make([]float64, len(tail))
	copy(cp, tail)
	sort.Float64s(cp)
	return cp[len(cp)/2]
}

// periodFromZeroCrossings возвращает длину сегмента в сэмплах и старт.
// Пересечения считаем по стабилизированному нулю (медиана последних 10%), при дрейфе окно не раздувается.
// Сегмент берём по пересечениям во второй половине сигнала — текущий режим, не старый базовый уровень.
// Сегмент [start, start+T] из исходных данных — график рисуем от нормального сигнала.
func periodFromZeroCrossings(s []float64) (T int, start int) {
	if len(s) < 8 {
		return 0, 0
	}
	zero := stabilizedZero(s)
	var crossings []int
	for i := 1; i < len(s); i++ {
		a, b := s[i-1]-zero, s[i]-zero
		if a <= 0 && b > 0 {
			crossings = append(crossings, i)
		}
	}
	if len(crossings) < 2 {
		return 0, 0
	}
	// При дрейфе старые пересечения (в начале окна) могут быть по другому нулю — берём только из второй половины.
	half := len(s) / 2
	var useCrossings []int
	for _, i := range crossings {
		if i >= half {
			useCrossings = append(useCrossings, i)
		}
	}
	if len(useCrossings) < 2 {
		useCrossings = crossings
	}
	// PeaksPerCycle вершин = PeaksPerCycle+1 пересечений нуля вверх.
	nUse := PeaksPerCycle + 1
	if len(useCrossings) < nUse {
		return 0, 0
	}
	start = useCrossings[len(useCrossings)-nUse]
	end := useCrossings[len(useCrossings)-1]
	T = end - start + 1
	if T < 4 {
		return 0, 0
	}
	if end >= len(s) {
		return 0, 0
	}
	return T, start
}

// ShapeZeroCrossingCount возвращает число положительных пересечений нуля в нормализованной форме (Points уже относительно 0).
// Чистый один цикл = 2, два цикла = 4. Сложные/шумные волны дают 6+.
func ShapeZeroCrossingCount(shape ShapeVector) int {
	pts := shape.Points
	if len(pts) < 4 {
		return 0
	}
	n := 0
	for i := 1; i < len(pts); i++ {
		if pts[i-1] <= 0 && pts[i] > 0 {
			n++
		}
	}
	return n
}

// ShapeLooksCleanCycle: true если форма похожа на один–два цикла (2 или 4 пересечения нуля). Сложные/шумные отсекаем.
const maxZeroCrossingsForCleanCycle = 6

func ShapeLooksCleanCycle(shape ShapeVector) bool {
	zc := ShapeZeroCrossingCount(shape)
	return zc >= 2 && zc <= maxZeroCrossingsForCleanCycle
}

// estimatePeriodSamples возвращает оценку периода в сэмплах (autocorrelation peak).
// Ускорение: шаг по лагу 4, maxLag ограничен 512.
func estimatePeriodSamples(s []float64) int {
	n := len(s)
	if n < 16 {
		return 0
	}
	minLag := n / 32
	if minLag < 4 {
		minLag = 4
	}
	maxLag := n / 2
	if maxLag > 512 {
		maxLag = 512
	}
	if maxLag > n-1 {
		maxLag = n - 1
	}
	bestLag := minLag
	bestCorr := -1e20
	const lagStep = 4
	for lag := minLag; lag <= maxLag; lag += lagStep {
		var sum float64
		for i := 0; i+lag < n; i += 2 {
			sum += s[i] * s[i+lag]
		}
		if sum > bestCorr {
			bestCorr = sum
			bestLag = lag
		}
	}
	return bestLag
}

// CosineDistance возвращает 1 - cosine_similarity (0 = идентичны, 2 = противоположны).
// Сравнение только по Points.
func CosineDistance(a, b ShapeVector) float64 {
	pa, pb := a.Points, b.Points
	if len(pa) == 0 || len(pb) == 0 {
		return 1
	}
	n := len(pa)
	if len(pb) < n {
		n = len(pb)
	}
	var dot, normA, normB float64
	for i := 0; i < n; i++ {
		dot += pa[i] * pb[i]
		normA += pa[i] * pa[i]
		normB += pb[i] * pb[i]
	}
	normA = math.Sqrt(normA)
	normB = math.Sqrt(normB)
	if normA < shapeEps || normB < shapeEps {
		return 1
	}
	cos := dot / (normA * normB)
	if cos > 1 {
		cos = 1
	}
	if cos < -1 {
		cos = -1
	}
	return 1 - cos
}

// ShapeDistance — алиас для merge/кластеризации (cosine distance).
func ShapeDistance(a, b ShapeVector) float64 {
	return CosineDistance(a, b)
}

// ShapeCosineSimilarity возвращает cos(angle) между векторами формы: 1 = одинаковые, -1 = инвертированные (убывание vs возрастание).
// Для discovery: не мержить при cos < 0 (разные глифы).
func ShapeCosineSimilarity(a, b ShapeVector) float64 {
	pa, pb := a.Points, b.Points
	if len(pa) == 0 || len(pb) == 0 {
		return 0
	}
	n := len(pa)
	if len(pb) < n {
		n = len(pb)
	}
	var dot, normA, normB float64
	for i := 0; i < n; i++ {
		dot += pa[i] * pb[i]
		normA += pa[i] * pa[i]
		normB += pb[i] * pb[i]
	}
	normA = math.Sqrt(normA)
	normB = math.Sqrt(normB)
	if normA < shapeEps || normB < shapeEps {
		return 0
	}
	cos := dot / (normA * normB)
	if cos > 1 {
		cos = 1
	}
	if cos < -1 {
		cos = -1
	}
	return cos
}

// ShapeVectorEMA обновляет dst экспоненциальным скользящим средним: dst = alpha*dst + (1-alpha)*src (по точкам).
// Стабилизирует матрицу глифов: эталон не прыгает при каждом merge. Если dst пустой — копирует src.
func ShapeVectorEMA(dst *ShapeVector, src ShapeVector, alpha float64) {
	pa, pb := dst.Points, src.Points
	if len(pb) == 0 {
		return
	}
	if len(pa) != len(pb) {
		dst.Points = append([]float64(nil), pb...)
		return
	}
	for i := range pa {
		pa[i] = alpha*pa[i] + (1-alpha)*pb[i]
	}
}

// PeakProfileSize — число бинов профиля пиков (относительность высокие/низкие по фазе).
const PeakProfileSize = 8

// PeakProfileFromShape строит профиль относительности пиков: по сегментам фазы (8 сегментов)
// max |value| в каждом сегменте. Волны с разным чередованием высокие/низкие дают разный профиль.
func PeakProfileFromShape(shape ShapeVector) [PeakProfileSize]float64 {
	var out [PeakProfileSize]float64
	pts := shape.Points
	if len(pts) < PeakProfileSize {
		return out
	}
	segLen := len(pts) / PeakProfileSize
	for i := 0; i < PeakProfileSize; i++ {
		start := i * segLen
		end := start + segLen
		if end > len(pts) {
			end = len(pts)
		}
		maxAbs := 0.0
		for j := start; j < end; j++ {
			a := math.Abs(pts[j])
			if a > maxAbs {
				maxAbs = a
			}
		}
		out[i] = maxAbs
	}
	// Нормализуем: сумма = 1 (чтобы сравнивать относительность, не масштаб).
	sum := 0.0
	for i := range out {
		sum += out[i]
	}
	if sum >= shapeEps {
		for i := range out {
			out[i] /= sum
		}
	}
	return out
}

// PeakProfileValid — true если профиль не нулевой (сумма > eps). Иначе классификация по соотношению пиков невозможна.
func PeakProfileValid(profile [PeakProfileSize]float64) bool {
	sum := 0.0
	for _, v := range profile {
		sum += v
	}
	return sum > 0.01
}

// PeakProfileDistance — L1 расстояние между профилями пиков (0 = одинаковое чередование, до ~2).
// Разное чередование высокие/низкие → большая дистанция → разные глифы.
func PeakProfileDistance(a, b [PeakProfileSize]float64) float64 {
	var d float64
	for i := 0; i < PeakProfileSize; i++ {
		diff := a[i] - b[i]
		if diff < 0 {
			diff = -diff
		}
		d += diff
	}
	return d
}

// PeakProfileDominantConfidence — уверенность в доминирующей половине: |max1 - max2|. Ниже threshold — форма «размазана», признак ненадёжен.
const PeakProfileDominantConfidenceThr = 0.05

// PeakProfileSameDominantHalf: true если у обоих профилей выше первая половина фазы или у обоих вторая.
// Soft gate: если у любой формы уверенность низкая (|max1-max2| < thr) — не запрещаем merge (return true).
func PeakProfileSameDominantHalf(a, b [PeakProfileSize]float64) bool {
	const half = PeakProfileSize / 2
	maxA1, maxA2 := 0.0, 0.0
	for i := 0; i < half; i++ {
		if a[i] > maxA1 {
			maxA1 = a[i]
		}
	}
	for i := half; i < PeakProfileSize; i++ {
		if a[i] > maxA2 {
			maxA2 = a[i]
		}
	}
	maxB1, maxB2 := 0.0, 0.0
	for i := 0; i < half; i++ {
		if b[i] > maxB1 {
			maxB1 = b[i]
		}
	}
	for i := half; i < PeakProfileSize; i++ {
		if b[i] > maxB2 {
			maxB2 = b[i]
		}
	}
	// Мягкий gate: при размытой форме не запрещаем merge.
	confA := maxA1 - maxA2
	if confA < 0 {
		confA = -confA
	}
	confB := maxB1 - maxB2
	if confB < 0 {
		confB = -confB
	}
	if confA < PeakProfileDominantConfidenceThr || confB < PeakProfileDominantConfidenceThr {
		return true
	}
	firstDominantA := maxA1 >= maxA2
	firstDominantB := maxB1 >= maxB2
	return firstDominantA == firstDominantB
}

// --- Shape as foundation: дискретный signature (топология + тренд), не точные числа ---

// EnvelopeClass — тип огибающей (категориально). Не число — класс.
const (
	EnvelopeSymmetric           = 0
	EnvelopeAttackFastDecaySlow = 1
	EnvelopeAttackSlowDecayFast = 2
	EnvelopeStepRing            = 3
	EnvelopeBurst               = 4
	EnvelopeClassMax            = 5
)

// AsymmetrySign — знак асимметрии формы.
const (
	AsymmetryLeft  = -1
	AsymmetryNone  = 0
	AsymmetryRight = 1
)

// ModulationFlag — модуляция (флаг, не коэффициент).
const (
	ModulationCyclic = 0
	ModulationAM     = 1
	ModulationFM     = 2
	ModulationJitter = 3
)

// ShapeSignature — дискретная идентичность формы. Merge по CoarseEqual (устойчивый класс), отображение по TopologyCode.
// Точные амплитуды/частоты/отношения пиков — в metadata экземпляра, не здесь.
type ShapeSignature struct {
	CoarseTopologyCode uint16 // устойчивый класс: N_peaks(2-3-4-5+), zero_cross_rate, symmetry (T меняется редко)
	TopologyCode       uint16 // детальный код (4 сегмента) — для отладки/отображения, может прыгать
	EnvelopeClass      uint8  // symmetric / attack-fast-decay-slow / ...
	AsymmetrySign      int8   // left / symmetric / right
	ModulationFlag     uint8  // cyclic / AM / FM / jitter
}

// Equal — полное совпадение по всем полям (в т.ч. fine TopologyCode).
func (a ShapeSignature) Equal(b ShapeSignature) bool {
	return a.TopologyCode == b.TopologyCode &&
		a.CoarseTopologyCode == b.CoarseTopologyCode &&
		a.EnvelopeClass == b.EnvelopeClass &&
		a.AsymmetrySign == b.AsymmetrySign &&
		a.ModulationFlag == b.ModulationFlag
}

// CoarseEqual — совпадение по грубой форме (CoarseTopologyCode + E + A). Если true → тот же класс, можно мержить даже при разном TopologyCode.
func (a ShapeSignature) CoarseEqual(b ShapeSignature) bool {
	return a.CoarseTopologyCode == b.CoarseTopologyCode &&
		a.EnvelopeClass == b.EnvelopeClass &&
		a.AsymmetrySign == b.AsymmetrySign
}

// SimplifiedCoarseEqual — упрощённая идентичность: только топология (CoarseTopologyCode) и асимметрия (A). E и M не учитываются.
// Одна волна не раскалывается на два токена из-за граничного E (напр. 13/0/0/0 и 13/1/0/0 — один класс).
func (a ShapeSignature) SimplifiedCoarseEqual(b ShapeSignature) bool {
	return a.CoarseTopologyCode == b.CoarseTopologyCode &&
		a.AsymmetrySign == b.AsymmetrySign
}

// TopologyOnlyEqual — только совпадение топологии (CoarseTopologyCode). E, A, M не учитываются.
// Для merge при очень малой дистанции формы: вариации одного глифа (напр. 11/0/-1/0 и 11/0/0/0 — одна волна, A на границе).
func (a ShapeSignature) TopologyOnlyEqual(b ShapeSignature) bool {
	return a.CoarseTopologyCode == b.CoarseTopologyCode
}

// decodeCoarseTopology разбивает CoarseTopologyCode на корзины: nPeaksBin*9 + zcrBin*3 + symBin.
func decodeCoarseTopology(code uint16) (nPeaksBin, zcrBin, symBin uint16) {
	nPeaksBin = code / 9
	rem := code % 9
	zcrBin = rem / 3
	symBin = rem % 3
	return nPeaksBin, zcrBin, symBin
}

// CoarseTopologyEqualAndOnlyEDiffers — та же топология (CoarseTopologyCode) и A, E отличается не более чем на 1 (0 vs 1).
// Для merge «слишком похожих» карточек при малом D и высоком cos (напр. T=9 E=1 vs T=9 E=0).
func (a ShapeSignature) CoarseTopologyEqualAndOnlyEDiffers(b ShapeSignature) bool {
	if a.CoarseTopologyCode != b.CoarseTopologyCode || a.AsymmetrySign != b.AsymmetrySign {
		return false
	}
	diff := int(a.EnvelopeClass) - int(b.EnvelopeClass)
	if diff < 0 {
		diff = -diff
	}
	return diff <= 1
}

// NearCoarseEqual — near-match: E и A совпадают, а CoarseTopology отличается ровно на одну корзину (nPeaks/zcr/sym ±1).
// Разрешает merge при более строгом D, не размывая алфавит (разные формы не сливаются).
func (a ShapeSignature) NearCoarseEqual(b ShapeSignature) bool {
	if a.EnvelopeClass != b.EnvelopeClass || a.AsymmetrySign != b.AsymmetrySign {
		return false
	}
	na, za, sa := decodeCoarseTopology(a.CoarseTopologyCode)
	nb, zb, sb := decodeCoarseTopology(b.CoarseTopologyCode)
	dn := int(na) - int(nb)
	if dn < 0 {
		dn = -dn
	}
	dz := int(za) - int(zb)
	if dz < 0 {
		dz = -dz
	}
	ds := int(sa) - int(sb)
	if ds < 0 {
		ds = -ds
	}
	// Ровно одна компонента отличается на 1, остальные совпадают.
	return (dn+dz+ds == 1) && dn <= 1 && dz <= 1 && ds <= 1
}

// shapeCenteredByAnchor возвращает форму, центрированную по якорю (главный экстремум в начале).
// Сигнатура, построенная по такой форме, инвариантна к сдвигу окна — одна физическая волна даёт один и тот же T в большинстве тиков.
func shapeCenteredByAnchor(shape ShapeVector) ShapeVector {
	pts := shape.Points
	if len(pts) < 8 {
		return shape
	}
	// Якорь = индекс глобального максимума (фаза-инвариантный старт; согласовано с AsymmetryFromShape).
	idxMax := 0
	for i := 1; i < len(pts); i++ {
		if pts[i] > pts[idxMax] {
			idxMax = i
		}
	}
	rotated := make([]float64, len(pts))
	for i := 0; i < len(pts); i++ {
		rotated[i] = pts[(idxMax+i)%len(pts)]
	}
	return ShapeVector{Points: rotated}
}

// smooth3 — скользящее среднее по 3 точкам для снижения чувствительности экстремумов к шуму.
func smooth3(pts []float64) []float64 {
	if len(pts) < 3 {
		return pts
	}
	out := make([]float64, len(pts))
	out[0] = pts[0]
	for i := 1; i < len(pts)-1; i++ {
		out[i] = (pts[i-1] + pts[i] + pts[i+1]) / 3
	}
	out[len(pts)-1] = pts[len(pts)-1]
	return out
}

// CoarseTopologyFromShape возвращает устойчивый класс топологии (N_peaks 2–3–4–5+, zero_cross 3 корзины, symmetry 3 корзины).
// T меняется редко; одна физическая волна даёт один и тот же coarse code в 80–95% тиков.
func CoarseTopologyFromShape(centered ShapeVector) uint16 {
	pts := centered.Points
	if len(pts) < 8 {
		return 0
	}
	sm := smooth3(pts)
	// Диапазон для порога шума: экстремумы ниже k*range игнорируем.
	minP, maxP := sm[0], sm[0]
	for _, v := range sm {
		if v < minP {
			minP = v
		}
		if v > maxP {
			maxP = v
		}
	}
	rangeP := maxP - minP
	if rangeP < 1e-12 {
		rangeP = 1e-12
	}
	noiseFloor := 0.08 * rangeP

	nExtrema := 0
	for i := 1; i < len(sm)-1; i++ {
		isMax := sm[i] >= sm[i-1] && sm[i] >= sm[i+1]
		isMin := sm[i] <= sm[i-1] && sm[i] <= sm[i+1]
		if (isMax || isMin) && math.Abs(sm[i]) >= noiseFloor {
			nExtrema++
			if nExtrema >= 20 {
				break
			}
		}
	}
	// Квантование N_peaks в широкие корзины: 2, 3, 4, 5+.
	var nPeaksBin uint16
	switch {
	case nExtrema <= 2:
		nPeaksBin = 0
	case nExtrema == 3:
		nPeaksBin = 1
	case nExtrema == 4:
		nPeaksBin = 2
	default:
		nPeaksBin = 3
	}

	// Zero-cross rate: число смен знака / (n-1), 3 корзины.
	zeroCrosses := 0
	for i := 1; i < len(pts); i++ {
		if pts[i-1]*pts[i] < 0 {
			zeroCrosses++
		}
	}
	zcr := float64(zeroCrosses) / float64(len(pts)-1)
	var zcrBin uint16
	if zcr < 0.2 {
		zcrBin = 0
	} else if zcr < 0.5 {
		zcrBin = 1
	} else {
		zcrBin = 2
	}

	// Symmetry: |left_mean - right_mean| от центрированной формы, 3 корзины.
	mid := len(pts) / 2
	left := 0.0
	for i := 0; i < mid; i++ {
		left += pts[i]
	}
	left /= float64(mid)
	right := 0.0
	for i := mid; i < len(pts); i++ {
		right += pts[i]
	}
	right /= float64(len(pts) - mid)
	diff := math.Abs(left - right)
	var symBin uint16
	if diff < 0.15 {
		symBin = 0
	} else if diff < 0.35 {
		symBin = 1
	} else {
		symBin = 2
	}

	return nPeaksBin*9 + zcrBin*3 + symBin
}

// TopologyFromShape: fine-топология по 4 сегментам от уже центрированной формы (для отладки/отображения).
// Центрирование вызывающий делает через shapeCenteredByAnchor. Порог 0.06 для устойчивости к шуму.
func TopologyFromShape(centered ShapeVector) uint16 {
	pts := centered.Points
	if len(pts) < 8 {
		return 0
	}
	// Паттерн по 4 сегментам (центр уже в начале).
	segLen := len(pts) / 4
	if segLen < 1 {
		segLen = 1
	}
	var pattern uint16
	const patternThr = 0.06
	for i := 0; i < 4; i++ {
		start := i * segLen
		end := start + segLen
		if end > len(pts) {
			end = len(pts)
		}
		mean := 0.0
		for j := start; j < end; j++ {
			mean += pts[j]
		}
		mean /= float64(end - start)
		if mean > patternThr {
			pattern |= 1 << i
		}
	}
	return pattern & 0xF
}

// EnvelopeClassFromSlope — категория огибающей по наклону (не точное число).
// Порог 0.25: шире нейтральная зона, алфавит схлопывается на реальных данных.
func EnvelopeClassFromSlope(slopePerSec, meanEnv float64) uint8 {
	if meanEnv < 1e-12 {
		return EnvelopeSymmetric
	}
	rel := slopePerSec / meanEnv
	if rel > 0.25 {
		return EnvelopeAttackFastDecaySlow
	}
	if rel < -0.25 {
		return EnvelopeAttackSlowDecayFast
	}
	return EnvelopeSymmetric
}

// AsymmetryFromShape — знак асимметрии: первая vs вторая половина цикла от фаза-инвариантного старта (первый макс).
// Порог 0.25: шире нейтральная зона, алфавит схлопывается.
func AsymmetryFromShape(shape ShapeVector) int8 {
	pts := shape.Points
	if len(pts) < 4 {
		return AsymmetryNone
	}
	idxMax := 0
	for i := 1; i < len(pts); i++ {
		if pts[i] > pts[idxMax] {
			idxMax = i
		}
	}
	rotated := make([]float64, len(pts))
	for i := 0; i < len(pts); i++ {
		rotated[i] = pts[(idxMax+i)%len(pts)]
	}
	mid := len(rotated) / 2
	left := 0.0
	for i := 0; i < mid; i++ {
		left += rotated[i]
	}
	left /= float64(mid)
	right := 0.0
	for i := mid; i < len(rotated); i++ {
		right += rotated[i]
	}
	right /= float64(len(rotated) - mid)
	diff := left - right
	if diff > 0.25 {
		return AsymmetryLeft
	}
	if diff < -0.25 {
		return AsymmetryRight
	}
	return AsymmetryNone
}

// ComputeShapeSignature строит дискретный signature из формы и наклона огибающей.
// Форма сначала центрируется по якорю (главный экстремум), затем считаются coarse (устойчивый класс) и fine (4 сегмента) топологии.
// Модуляция пока всегда Cyclic.
func ComputeShapeSignature(shape ShapeVector, envelopeSlopePerSec, meanEnv float64) ShapeSignature {
	centered := shapeCenteredByAnchor(shape)
	return ShapeSignature{
		CoarseTopologyCode: CoarseTopologyFromShape(centered),
		TopologyCode:       TopologyFromShape(centered),
		EnvelopeClass:      EnvelopeClassFromSlope(envelopeSlopePerSec, meanEnv),
		AsymmetrySign:      AsymmetryFromShape(centered),
		ModulationFlag:     ModulationCyclic,
	}
}
