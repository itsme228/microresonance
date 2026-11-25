// IIR bandpass в time-domain: каузальный, с минимальной задержкой.
// Не FFT/IFFT — каждый band = отдельный канал наблюдения для realtime и событий.

package signal

import "math"

// Biquad — один biquad (Direct Form II), состояние v1,v2.
type Biquad struct {
	B0, B1, B2, A1, A2 float64
	V1, V2             float64
}

// Filter обрабатывает один сэмпл, возвращает один сэмпл (каузально).
func (b *Biquad) Filter(x float64) float64 {
	v0 := x - b.A1*b.V1 - b.A2*b.V2
	y := b.B0*v0 + b.B1*b.V1 + b.B2*b.V2
	b.V2, b.V1 = b.V1, v0
	return y
}

// Reset сбрасывает состояние (например в начале буфера при отрисовке).
func (b *Biquad) Reset() {
	b.V1, b.V2 = 0, 0
}

// NewBiquadBandpass создаёт biquad bandpass (cookbook, constant 0 dB peak).
// f0Hz — центр полосы, bwHz — ширина (hi-lo), fs — частота дискретизации.
// Q = f0/bw.
func NewBiquadBandpass(f0Hz, bwHz, fs float64) Biquad {
	if fs <= 0 || f0Hz <= 0 || bwHz <= 0 {
		return Biquad{}
	}
	if f0Hz >= fs/2 {
		f0Hz = fs/2 - 1
	}
	w0 := 2 * math.Pi * f0Hz / fs
	Q := f0Hz / bwHz
	if Q < 0.5 {
		Q = 0.5
	}
	sinW0 := math.Sin(w0)
	cosW0 := math.Cos(w0)
	alpha := sinW0 / (2 * Q)
	a0 := 1 + alpha
	return Biquad{
		B0: alpha / a0,
		B1: 0,
		B2: -alpha / a0,
		A1: -2 * cosW0 / a0,
		A2: (1 - alpha) / a0,
	}
}

// BandpassFilter — один каузальный bandpass по полосе [loHz, hiHz].
type BandpassFilter struct {
	X, Y, Z Biquad
}

// NewBandpassFilter создаёт фильтр для полосы [loHz, hiHz] при fs.
// Если полоса выше Найквиста (hiHz > fs/2), верхняя граница зажимается до fs/2-0.5,
// чтобы biquad не уходил в sin(w0)≈0 и не давал нулевой выход (B6/B7 «молчали» при fs=4k).
// Если после зажима полоса пустая (loHz >= hiClamp), возвращается фильтр с нулевым откликом.
func NewBandpassFilter(loHz, hiHz, fs float64) BandpassFilter {
	if fs <= 0 {
		return BandpassFilter{}
	}
	nyq := fs/2 - 0.5
	if nyq < 1 {
		nyq = 1
	}
	hiClamp := hiHz
	if hiClamp > nyq {
		hiClamp = nyq
	}
	if loHz >= hiClamp {
		return BandpassFilter{} // полоса целиком выше Найквиста — нулевой отклик
	}
	f0 := math.Sqrt(loHz * hiClamp)
	bw := hiClamp - loHz
	if bw <= 0 {
		bw = 0.1
	}
	return BandpassFilter{
		X: NewBiquadBandpass(f0, bw, fs),
		Y: NewBiquadBandpass(f0, bw, fs),
		Z: NewBiquadBandpass(f0, bw, fs),
	}
}

// Reset сбрасывает состояние всех осей.
func (b *BandpassFilter) Reset() {
	b.X.Reset()
	b.Y.Reset()
	b.Z.Reset()
}

// Filter обрабатывает три канала, возвращает отфильтрованные x,y,z и квадраты x²+y²+z².
func (b *BandpassFilter) Filter(x, y, z []float64) (xB, yB, zB, squared []float64) {
	n := len(x)
	if len(y) < n {
		n = len(y)
	}
	if len(z) < n {
		n = len(z)
	}
	if n == 0 {
		return nil, nil, nil, nil
	}
	xB = make([]float64, n)
	yB = make([]float64, n)
	zB = make([]float64, n)
	squared = make([]float64, n)
	for i := 0; i < n; i++ {
		xB[i] = b.X.Filter(x[i])
		yB[i] = b.Y.Filter(y[i])
		zB[i] = b.Z.Filter(z[i])
		squared[i] = xB[i]*xB[i] + yB[i]*yB[i] + zB[i]*zB[i]
	}
	return xB, yB, zB, squared
}
