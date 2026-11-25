// glyph_event.go — единица датасета = один glyph-event: один цикл волны (как в графиках, одна вершина), 3 экстремума внутри (min–max–min или max–min–max).
// Сохраняем инварианты формы, энергии и геометрии, без сырых X/Y/Z и без таймстепов.

package trainer

import (
	"math"
)

// TrainerEventPoints — shape length for event (same as TrainerShapePoints).
const TrainerEventPoints = TrainerShapePoints

// TrainerEvent — one "gesture" (glyph-event): one wave cycle (same window as on graphs), 3 shape extrema inside.
// Вектор ~16–18 чисел для нейросети. Порядок полей = порядок в Vector().
type TrainerEvent struct {
	// A. Базовая геометрия волны (один цикл, 3 экстремума)
	A1       float64 // амплитуды 3 экстремумов (нормированные -1..1)
	A2       float64
	A3       float64
	DeltaA12 float64 // A2 - A1
	DeltaA23 float64 // A3 - A2
	T12      float64 // расстояние в сэмплах между экстремумами 1→2
	T23      float64 // между 2→3
	Symmetry float64 // T12 / (T12 + T23) — асимметрия жеста

	// B. Частотная идентичность
	FreqCenter float64 // Гц
	Bandwidth  float64 // Гц (ширина полосы или 0.1*freq)

	// C. Трёхосность: ориентация энергии (u1, u2; Ez = 1-u1-u2)
	U1 float64 // Ex / (Ex+Ey+Ez)
	U2 float64 // Ey / (Ex+Ey+Ez)
	// Геометрия движения: PCA собственные значения λ2/λ1, λ3/λ1
	E1 float64 // λ2/λ1
	E2 float64 // λ3/λ1

	// D. Жизнь глифа (заменяет таймстепы)
	AgeSincePrevEvent float64 // мс или сэмплы, log1p
	EventIndexInRun   int     // порядковый номер в серии подряд
	RunLength         int     // сколько событий подряд этого глифа

	// E. Контекст (опционально)
	ActiveGlyphCount int     // сколько глифов сейчас активны (0 = не задано)
	RankInEnergy     float64 // 0..1, насколько доминирует (0 = не задано)

	// Полная информация для датасета: общий график глифа и каждая ось отдельно точками
	Shape []float64 // общий график глифа (нормализованная форма, 64 точки)
	Gx    []float64 // ось X точками (64)
	Gy    []float64 // ось Y точками (64)
	Gz    []float64 // ось Z точками (64)

	// Мета для сохранения
	GlyphID int   `json:"glyph_id"` // токен алфавита (PatternStore ID)
	BandIdx int   `json:"band_idx"`
	Ts      int64 `json:"ts"`
}

// Invariants возвращает инварианты формы/энергии/геометрии для ML (17 float64).
func (e *TrainerEvent) Invariants() []float64 {
	return []float64{
		e.A1, e.A2, e.A3,
		e.DeltaA12, e.DeltaA23,
		e.T12, e.T23, e.Symmetry,
		e.FreqCenter, e.Bandwidth,
		e.U1, e.U2,
		e.E1, e.E2,
		e.AgeSincePrevEvent,
		float64(e.EventIndexInRun),
		float64(e.RunLength),
	}
}

// Meta возвращает метаинформацию события для хранения в JSON (в модель не подаётся).
// Часть полей дублирует features: freq_center, bandwidth, age → уже в Invariants(); держать в одном месте при изменении.
func (e *TrainerEvent) Meta() map[string]interface{} {
	return map[string]interface{}{
		"glyph_id":           e.GlyphID,
		"band_idx":           e.BandIdx,
		"ts":                 e.Ts,
		"freq_center":        e.FreqCenter,
		"bandwidth":          e.Bandwidth,
		"age_since_prev_ms":  e.AgeSincePrevEvent,
		"event_index_in_run": e.EventIndexInRun,
		"run_length":         e.RunLength,
		"active_glyph_count": e.ActiveGlyphCount,
		"rank_in_energy":     e.RankInEnergy,
	}
}

// ExtractTrainerEvent builds one glyph-event from one cycle shape (3 extrema inside), period in samples and 3-axis data.
// shapePoints — 64 точки одного цикла (то же окно, что на графиках); periodSamples — длина сегмента в сэмплах;
// gx, gy, gz — 64 точки по осям X,Y,Z по тому же окну (ComputeFrozenGlyphFromSegment).
// freqHz, bandwidthHz — частотная идентичность.
// Возвращает (event, true) при успехе.
func ExtractTrainerEvent(
	shapePoints []float64,
	periodSamples int,
	gx, gy, gz []float64,
	freqHz, bandwidthHz float64,
) (TrainerEvent, bool) {
	var ev TrainerEvent
	if len(shapePoints) < 8 || periodSamples < 4 {
		return ev, false
	}
	n := len(shapePoints)
	// Найти 3 экстремума внутри одного цикла: trough–peak–trough или peak–trough–peak
	i1, i2, i3 := findThreeExtrema(shapePoints)
	if i1 < 0 || i2 < 0 || i3 < 0 {
		return ev, false
	}
	v1, v2, v3 := shapePoints[i1], shapePoints[i2], shapePoints[i3]
	ev.A1, ev.A2, ev.A3 = v1, v2, v3
	ev.DeltaA12 = v2 - v1
	ev.DeltaA23 = v3 - v2
	// T12, T23 в сэмплах (фаза 0..1 → период)
	phaseScale := float64(periodSamples) / float64(n-1)
	ev.T12 = float64(i2-i1) * phaseScale
	ev.T23 = float64(i3-i2) * phaseScale
	sumT := ev.T12 + ev.T23
	if sumT < 1e-9 {
		sumT = 1e-9
	}
	ev.Symmetry = ev.T12 / sumT
	ev.FreqCenter = freqHz
	ev.Bandwidth = bandwidthHz

	// u1, u2 — энергия по осям (Ex, Ey, Ez)
	if len(gx) >= n && len(gy) >= n && len(gz) >= n {
		var ex, ey, ez float64
		for i := 0; i < n; i++ {
			ex += gx[i] * gx[i]
			ey += gy[i] * gy[i]
			ez += gz[i] * gz[i]
		}
		s := ex + ey + ez
		if s < 1e-20 {
			s = 1e-20
		}
		ev.U1 = ex / s
		ev.U2 = ey / s
		// e1, e2 — PCA на (gx, gy, gz)
		e1, e2 := pcaEigenRatios(gx[:n], gy[:n], gz[:n])
		ev.E1 = e1
		ev.E2 = e2
	} else {
		ev.U1, ev.U2 = 1.0/3.0, 1.0/3.0
		ev.E1, ev.E2 = 0.5, 0.25
	}
	return ev, true
}

// findThreeExtrema находит индексы 3 экстремумов (trough–peak–trough или peak–trough–peak) в форме одного цикла.
func findThreeExtrema(s []float64) (i1, i2, i3 int) {
	if len(s) < 8 {
		return -1, -1, -1
	}
	troughs := findTroughs(s, 3)
	peaks := findPeaks(s, 3)
	if len(troughs) >= 2 && len(peaks) >= 1 {
		// trough – peak – trough: берём последние 2 дна и пик между ними
		t0 := troughs[len(troughs)-2]
		t1 := troughs[len(troughs)-1]
		var p int
		for _, pk := range peaks {
			if pk > t0 && pk < t1 {
				p = pk
				break
			}
		}
		if p > 0 {
			return t0, p, t1
		}
	}
	if len(peaks) >= 2 && len(troughs) >= 1 {
		p0 := peaks[len(peaks)-2]
		p1 := peaks[len(peaks)-1]
		var t int
		for _, tr := range troughs {
			if tr > p0 && tr < p1 {
				t = tr
				break
			}
		}
		if t > 0 {
			return p0, t, p1
		}
	}
	// Fallback: минимум, максимум, минимум по всей форме
	mini, maxi := 0, 0
	for i := 1; i < len(s); i++ {
		if s[i] < s[mini] {
			mini = i
		}
		if s[i] > s[maxi] {
			maxi = i
		}
	}
	if mini < maxi {
		return mini, maxi, len(s) - 1
	}
	return 0, maxi, mini
}

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

// AxisSigFromGlyphData вычисляет u1, u2, e1, e2 из данных по осям (gx, gy, gz).
// Used for ML queue when ExtractTrainerEvent failed (no 3 extrema).
// Возвращает nil при недостатке данных.
func AxisSigFromGlyphData(gx, gy, gz []float64) []float64 {
	n := len(gx)
	if len(gy) < n || len(gz) < n || n < 4 {
		return nil
	}
	var ex, ey, ez float64
	for i := 0; i < n; i++ {
		ex += gx[i] * gx[i]
		ey += gy[i] * gy[i]
		ez += gz[i] * gz[i]
	}
	s := ex + ey + ez
	if s < 1e-20 {
		s = 1e-20
	}
	u1 := ex / s
	u2 := ey / s
	e1, e2 := pcaEigenRatios(gx[:n], gy[:n], gz[:n])
	return []float64{u1, u2, e1, e2}
}

// pcaEigenRatios: по 3 осям (x,y,z) считаем PCA, возвращаем e1=λ2/λ1, e2=λ3/λ1.
func pcaEigenRatios(x, y, z []float64) (e1, e2 float64) {
	n := len(x)
	if len(y) < n || len(z) < n || n < 4 {
		return 0.5, 0.25
	}
	var mx, my, mz float64
	for i := 0; i < n; i++ {
		mx += x[i]
		my += y[i]
		mz += z[i]
	}
	mx /= float64(n)
	my /= float64(n)
	mz /= float64(n)
	var cxx, cyy, czz, cxy, cxz, cyz float64
	for i := 0; i < n; i++ {
		dx := x[i] - mx
		dy := y[i] - my
		dz := z[i] - mz
		cxx += dx * dx
		cyy += dy * dy
		czz += dz * dz
		cxy += dx * dy
		cxz += dx * dz
		cyz += dy * dz
	}
	inv := 1.0 / float64(n)
	cxx *= inv
	cyy *= inv
	czz *= inv
	cxy *= inv
	cxz *= inv
	cyz *= inv
	// Собственные значения 3x3 симметричной матрицы (упрощённо — итерация или явная формула)
	// Используем степенной метод для наибольшего, затем дефляцию для двух других.
	// Для 3x3 можно решить характеристический многочлен. Здесь — приближение через trace и детерминант.
	// Упрощение: считаем λ1 ≈ max диагонали + поправка, λ2, λ3 из оставшейся части.
	// Более надёжно: Jacobi для 3x3.
	lambda1, lambda2, lambda3 := eigenvalues3x3Sym(cxx, cyy, czz, cxy, cxz, cyz)
	if math.Abs(lambda1) < 1e-20 {
		lambda1 = 1e-20
	}
	e1 = lambda2 / lambda1
	e2 = lambda3 / lambda1
	if e1 > 1 {
		e1 = 1
	}
	if e2 > 1 {
		e2 = 1
	}
	return e1, e2
}

// eigenvalues3x3Sym возвращает собственные значения симметричной 3x3 матрицы (по убыванию).
// Степенной метод + дефляция.
func eigenvalues3x3Sym(cxx, cyy, czz, cxy, cxz, cyz float64) (l1, l2, l3 float64) {
	// Наибольшее по модулю — степенной метод
	v := []float64{1.0, 1.0, 1.0}
	for iter := 0; iter < 30; iter++ {
		w0 := cxx*v[0] + cxy*v[1] + cxz*v[2]
		w1 := cxy*v[0] + cyy*v[1] + cyz*v[2]
		w2 := cxz*v[0] + cyz*v[1] + czz*v[2]
		norm := math.Sqrt(w0*w0 + w1*w1 + w2*w2)
		if norm < 1e-20 {
			break
		}
		v[0], v[1], v[2] = w0/norm, w1/norm, w2/norm
	}
	l1 = v[0]*(cxx*v[0]+cxy*v[1]+cxz*v[2]) + v[1]*(cxy*v[0]+cyy*v[1]+cyz*v[2]) + v[2]*(cxz*v[0]+cyz*v[1]+czz*v[2])
	// Дефляция: B = A - λ1*v*v'
	cxx -= l1 * v[0] * v[0]
	cyy -= l1 * v[1] * v[1]
	czz -= l1 * v[2] * v[2]
	cxy -= l1 * v[0] * v[1]
	cxz -= l1 * v[0] * v[2]
	cyz -= l1 * v[1] * v[2]
	tr := cxx + cyy + czz
	// Второе собственное значение — степенной метод по дефлированной матрице
	v2 := []float64{1.0, -0.5, -0.5}
	for iter := 0; iter < 25; iter++ {
		w0 := cxx*v2[0] + cxy*v2[1] + cxz*v2[2]
		w1 := cxy*v2[0] + cyy*v2[1] + cyz*v2[2]
		w2 := cxz*v2[0] + cyz*v2[1] + czz*v2[2]
		norm := math.Sqrt(w0*w0 + w1*w1 + w2*w2)
		if norm < 1e-20 {
			break
		}
		v2[0], v2[1], v2[2] = w0/norm, w1/norm, w2/norm
	}
	l2 = v2[0]*(cxx*v2[0]+cxy*v2[1]+cxz*v2[2]) + v2[1]*(cxy*v2[0]+cyy*v2[1]+cyz*v2[2]) + v2[2]*(cxz*v2[0]+cyz*v2[1]+czz*v2[2])
	l3 = tr - l2
	if l2 < l3 {
		l2, l3 = l3, l2
	}
	if l1 < 1e-20 {
		l1 = 1e-20
	}
	return l1, l2, l3
}
