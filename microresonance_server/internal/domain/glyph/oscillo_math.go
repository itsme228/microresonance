package glyph

import "math"

const (
	// Frozen glyph contract: envelope glyph is 16 segments * 2 (min+max) = 32 points.
	FrozenGlyphSegments = 16
	FrozenGlyphPoints   = 32
	// Stationarity defaults for lock windows.
	DefaultACv    = 0.15
	DefaultASlope = 0.25
	fcLpfEnvHz    = 15
)

// ComputeFrozenGlyph computes glyph once at lock time from current buffer (legacy, no explicit window).
func ComputeFrozenGlyph(x []float64, Fs, f0 float64) []float64 {
	return ComputeFrozenGlyphFromWindow(x, Fs, f0, 0)
}

// ComputeFrozenGlyphFromWindow computes envelope glyph from latest windowSec of x.
func ComputeFrozenGlyphFromWindow(x []float64, Fs, f0 float64, windowSec float64) []float64 {
	if Fs <= 0 || f0 <= 0 || len(x) < 32 {
		return nil
	}
	if windowSec <= 0 {
		windowSec = 0.6
	}
	N := int(Fs * windowSec)
	if N < 128 {
		N = 128
	}
	if N > len(x) {
		N = len(x)
	}
	start := len(x) - N
	return ComputeFrozenGlyphFromSegment(x, start, N, Fs, f0)
}

const MinSamplesForGlyph = 4

// ComputeFrozenGlyphFromSegment computes envelope glyph over [start:start+n].
func ComputeFrozenGlyphFromSegment(x []float64, start, n int, Fs, f0 float64) []float64 {
	if Fs <= 0 || f0 <= 0 || n < MinSamplesForGlyph || start < 0 || start+n > len(x) {
		return nil
	}
	seg := x[start : start+n]
	env := computeEnvelope(seg, 0, len(seg), Fs, f0)
	if len(env) < MinSamplesForGlyph {
		return nil
	}
	out := make([]float64, FrozenGlyphPoints)
	for j := 0; j < FrozenGlyphSegments; j++ {
		a := j * len(env) / FrozenGlyphSegments
		b := (j + 1) * len(env) / FrozenGlyphSegments
		if b <= a {
			b = a + 1
			if b > len(env) {
				b = len(env)
				a = b - 1
			}
		}
		mn, mx := env[a], env[a]
		for i := a + 1; i < b; i++ {
			if env[i] < mn {
				mn = env[i]
			}
			if env[i] > mx {
				mx = env[i]
			}
		}
		out[j] = clampNorm(mn)
		out[FrozenGlyphSegments+j] = clampNorm(mx)
	}
	return out
}

// ExtractWaveShapeAtFreq extracts one-cycle shape around target f0.
func ExtractWaveShapeAtFreq(x []float64, start, n int, fs, f0 float64, minCycles int) ShapeVector {
	shape, _, _ := ExtractWaveShapeAtFreqWithBounds(x, start, n, fs, f0, minCycles)
	return shape
}

// ExtractWaveShapeAtFreqWithBounds returns shape and source bounds.
func ExtractWaveShapeAtFreqWithBounds(x []float64, start, n int, fs, f0 float64, minCycles int) (ShapeVector, int, int) {
	if fs <= 0 || f0 <= 0 || n < MinSamplesForGlyph || start < 0 || start+n > len(x) {
		return ShapeVector{}, 0, 0
	}
	seg := x[start : start+n]
	env := computeEnvelope(seg, 0, len(seg), fs, f0)
	if len(env) < MinSamplesForGlyph {
		return ShapeVector{}, 0, 0
	}

	T, s0 := periodFromExtrema(env)
	if T < 4 || s0 < 0 || s0+T > len(env) {
		T, s0 = periodFromZeroCrossings(env)
	}
	if T < 4 || s0 < 0 || s0+T > len(env) {
		T = estimatePeriodSamples(env)
		if T < 4 {
			return ShapeVector{}, 0, 0
		}
		s0 = len(env) - T
		if s0 < 0 {
			s0 = 0
		}
	}
	if minCycles <= 0 {
		minCycles = 1
	}
	if len(env) < minCycles*T {
		return ShapeVector{}, 0, 0
	}
	startEnv := len(env) - minCycles*T
	if startEnv < 0 {
		startEnv = 0
	}
	shape, localStart, localT := BuildShapeWithBounds(env[startEnv:], fs, 64)
	if len(shape.Points) == 0 {
		return ShapeVector{}, 0, 0
	}
	globalStart := start + startEnv + localStart
	return shape, globalStart, localT
}

func computeEnvelope(x []float64, start, n int, Fs, f0 float64) []float64 {
	if n < MinSamplesForGlyph || start < 0 || start+n > len(x) {
		return nil
	}
	seg := x[start : start+n]
	alpha := 1.0 - math.Exp(-2*math.Pi*float64(fcLpfEnvHz)/Fs)
	I := 0.0
	Q := 0.0
	env := make([]float64, n)
	for i := 0; i < n; i++ {
		t := float64(i) / Fs
		c := math.Cos(2 * math.Pi * f0 * t)
		s := math.Sin(2 * math.Pi * f0 * t)
		Iraw := seg[i] * c
		Qraw := seg[i] * s
		I = I + alpha*(Iraw-I)
		Q = Q + alpha*(Qraw-Q)
		env[i] = math.Sqrt(I*I + Q*Q)
	}
	return env
}

func ComputeEnvelope(x []float64, start, n int, Fs, f0 float64) []float64 {
	return computeEnvelope(x, start, n, Fs, f0)
}

func EnvelopeSlopePerSec(env []float64, Fs float64) float64 {
	if len(env) < MinSamplesForGlyph || Fs <= 0 {
		return 0
	}
	var sx, sy, sxx, sxy float64
	n := float64(len(env))
	for i, v := range env {
		x := float64(i) / Fs
		sx += x
		sy += v
		sxx += x * x
		sxy += x * v
	}
	den := n*sxx - sx*sx
	if math.Abs(den) < 1e-12 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}

func EnvelopeCVAndSlope(env []float64, Fs float64) (cv, slopeRel float64) {
	if len(env) < MinSamplesForGlyph || Fs <= 0 {
		return 0, 0
	}
	mu := 0.0
	for _, v := range env {
		mu += v
	}
	mu /= float64(len(env))
	if mu < 1e-12 {
		return 0, 0
	}
	varsum := 0.0
	for _, v := range env {
		d := v - mu
		varsum += d * d
	}
	std := math.Sqrt(varsum / float64(len(env)))
	cv = std / mu
	slope := EnvelopeSlopePerSec(env, Fs)
	slopeRel = math.Abs(slope) / mu
	return cv, slopeRel
}

func StationarityOK(env []float64, Fs float64, A_cv, A_slope float64) bool {
	if len(env) < MinSamplesForGlyph || Fs <= 0 {
		return false
	}
	if A_cv <= 0 {
		A_cv = DefaultACv
	}
	if A_slope <= 0 {
		A_slope = DefaultASlope
	}
	cv, slopeRel := EnvelopeCVAndSlope(env, Fs)
	return cv < A_cv && slopeRel < A_slope
}

func clampNorm(v float64) float64 {
	if v < -1 {
		return -1
	}
	if v > 1 {
		return 1
	}
	return v
}

func rms(a []float64) float64 {
	if len(a) == 0 {
		return 0
	}
	s := 0.0
	for _, v := range a {
		s += v * v
	}
	return math.Sqrt(s / float64(len(a)))
}

func RMS(a []float64) float64 {
	return rms(a)
}
