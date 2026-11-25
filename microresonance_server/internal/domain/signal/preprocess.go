// Preprocessing: нормализация по окну, mag2, axis_signature.
// Инвариантность к мощности и ориентации датчика.

package signal

import (
	"math"
)

const (
	preprocessHighpassHz = 0.5  // highpass 0.2–1 Hz, по умолчанию 0.5
	preprocessSilence    = 1e-9 // порог тишины (RMS)
)

// AxisSignature — метаданные осей (XY/XZ/YZ), не ключ токена.
type AxisSignature struct {
	RatioXY   float64
	RatioXZ   float64
	RatioYZ   float64
	Coherence float64
}

// PreprocessWindow нормализует окно: remove DC, highpass, robust_scale (median или RMS), silence gate.
// x, y, z — срезы одного размера; useMag2 — использовать mag2 как мастер-канал.
// Возвращает s_norm, axis_signature, ampNorm (RMS до норм), noiseFloor (порог тишины).
func PreprocessWindow(x, y, z []float64, fs float64, useMedian bool, useMag2 bool) (sNorm []float64, axis AxisSignature, ampNorm, noiseFloor float64) {
	n := len(x)
	if n == 0 {
		return nil, axis, 0, 0
	}
	if len(y) < n {
		n = len(y)
	}
	if len(z) < n {
		n = len(z)
	}
	if n < 8 {
		return nil, axis, 0, 0
	}

	// Мастер-канал: X или mag2
	s := make([]float64, n)
	if useMag2 {
		for i := 0; i < n; i++ {
			xi, yi, zi := x[i], y[i], z[i]
			if i < len(y) {
				yi = y[i]
			}
			if i < len(z) {
				zi = z[i]
			}
			s[i] = xi*xi + yi*yi + zi*zi
		}
		// sqrt(mag2) для масштаба, как "амплитуда"
		for i := 0; i < n; i++ {
			if s[i] > 0 {
				s[i] = math.Sqrt(s[i])
			}
		}
	} else {
		copy(s, x[:n])
	}

	// DC remove
	dc := 0.0
	for _, v := range s {
		dc += v
	}
	dc /= float64(n)
	for i := range s {
		s[i] -= dc
	}

	// Highpass: один проход IIR, O(n)
	if fs > 0 && n > 1 {
		fc := preprocessHighpassHz
		if fc > fs/4 {
			fc = fs / 4
		}
		alpha := 1.0 / (1.0 + 1.0/(2*math.Pi*fc/fs))
		yp := 0.0
		xp := s[0]
		for i := 1; i < n; i++ {
			nextX := s[i]
			yp = alpha * (yp + nextX - xp)
			s[i] = yp
			xp = nextX
		}
		s[0] = 0
	}

	// robust_scale: RMS (быстро); median отключён — O(n²) тормозит
	ampNorm = rms(s)
	noiseFloor = preprocessSilence
	if ampNorm < preprocessSilence {
		ampNorm = preprocessSilence
	}
	for i := range s {
		s[i] /= ampNorm
	}

	// Axis signature (метаданные): отношения X/Y, X/Z, Y/Z по RMS
	rmsX := rms(x[:n])
	rmsY := rms(y[:n])
	rmsZ := rms(z[:n])
	eps := 1e-12
	if rmsX < eps {
		rmsX = eps
	}
	if rmsY < eps {
		rmsY = eps
	}
	if rmsZ < eps {
		rmsZ = eps
	}
	axis.RatioXY = rmsX / rmsY
	axis.RatioXZ = rmsX / rmsZ
	axis.RatioYZ = rmsY / rmsZ
	// Coherence: корреляция X с Y (упрощённо)
	axis.Coherence = 0
	if n > 1 {
		var sumXY, sumX2, sumY2 float64
		for i := 0; i < n; i++ {
			sumXY += x[i] * y[i]
			sumX2 += x[i] * x[i]
			sumY2 += y[i] * y[i]
		}
		if sumX2 > eps && sumY2 > eps {
			axis.Coherence = sumXY / (math.Sqrt(sumX2) * math.Sqrt(sumY2))
			if axis.Coherence < 0 {
				axis.Coherence = -axis.Coherence
			}
		}
	}

	return s, axis, ampNorm, noiseFloor
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
