package signal

import (
	"math"
)

type ResonanceMetrics struct {
	CarrierFreqHz    float32
	CarrierMag       float32
	CarrierPhaseDeg  float32
	CarrierSNR       float32
	CarrierFFTMag    float32
	NoiseFloorMag    float32
	WaterfallSNRDB   float32
	AxisMeanG        float32
	AxisACRMSG       float32
	CarrierToACDB    float32
	SubFreqHz        float32
	SubMag           float32
	SubPhaseDeg      float32
	SubSNR           float32
	H2FreqHz         float32
	H2Mag            float32
	H2PhaseDeg       float32
	H2SNR            float32
	H3FreqHz         float32
	H3Mag            float32
	H3PhaseDeg       float32
	H3SNR            float32
	DominantFreqHz   float32
	DominantMag      float32
	DominantPhaseDeg float32
	DominantSNR      float32
	PhaseDeltaDeg    float32
	FreqOffsetHz     float32
	PeakSpreadHz     float32
	AxisCode         uint8
}

// DemodulateIQ выполняет когерентную IQ-демодуляцию сырых данных.
// z — сырые сэмплы (обычно ось Z), tsfUs — TSF начала окна,
// freq — частота несущей (Гц), fs — частота дискретизации (Гц).
// Возвращает амплитуду (g), фазу (градусы 0–360) и SNR (дБ).
func DemodulateIQ(z []float64, tsfUs uint64, freq float64, fs float64) (mag float64, phase float64, snr float64) {
	n := len(z)
	if n == 0 || freq <= 0 || fs <= 0 {
		return 0, 0, 0
	}

	var mean float64
	for _, v := range z {
		mean += v
	}
	mean /= float64(n)

	// Формула фазы: phi = frac(f * t) * 2π
	phiStart := math.Mod(freq*(float64(tsfUs)*1e-6), 1.0) * (2.0 * math.Pi)
	dphiRad := 2.0 * math.Pi * freq / fs
	cRef, sRef := math.Cos(phiStart), math.Sin(phiStart)
	dcRot, dsRot := math.Cos(dphiRad), math.Sin(dphiRad)

	var iAcc, qAcc, totalPwr float64
	for i := 0; i < n; i++ {
		zd := z[i] - mean
		iAcc += zd * cRef
		qAcc += zd * sRef
		totalPwr += zd * zd

		// Rotating phasor: (c + i*s) * (dc + i*ds) = (c*dc - s*ds) + i*(c*ds + s*dc)
		cNext := cRef*dcRot - sRef*dsRot
		sRef = cRef*dsRot + sRef*dcRot
		cRef = cNext
	}

	iIQ := iAcc / float64(n)
	qIQ := qAcc / float64(n)
	totalPwr /= float64(n)

	sigPwr := iIQ*iIQ + qIQ*qIQ
	noisePwr := totalPwr - 2.0*sigPwr
	if noisePwr < 1e-15 {
		noisePwr = 1e-15
	}

	mag = 2.0 * math.Sqrt(sigPwr)
	phase = math.Atan2(qIQ, iIQ) * (180.0 / math.Pi)
	if phase < 0 {
		phase += 360
	}
	snr = 10.0 * math.Log10(sigPwr/noisePwr)
	if snr > 99.0 {
		snr = 99.0
	}

	return mag, phase, snr
}

func normalizeDeg180(v float64) float64 {
	for v <= -180 {
		v += 360
	}
	for v > 180 {
		v -= 360
	}
	return v
}

func chooseDominantPeak(carrierHz, carMag, carPhase, carSNR float64, peaks []DspPeak) (freq, mag, phase, snr float64) {
	freq = carrierHz
	mag = carMag
	phase = carPhase
	snr = carSNR
	if len(peaks) == 0 {
		return
	}

	best := peaks[0]
	bestDF := math.Abs(float64(best.FreqHz) - carrierHz)
	carrierIsUsable := carMag > 1e-9 && carSNR > -18.0
	offCarrierNeedsLift := 1.35
	if carSNR > -10.0 {
		offCarrierNeedsLift = 1.6
	}

	for _, p := range peaks {
		if p.FreqHz <= 0 {
			continue
		}
		df := math.Abs(float64(p.FreqHz) - carrierHz)
		if df <= 20 {
			return float64(p.FreqHz), float64(p.Magnitude), float64(p.PhaseDeg), float64(p.SNR)
		}
		if df < bestDF || (math.Abs(df-bestDF) < 1e-6 && p.Magnitude > best.Magnitude) {
			best = p
			bestDF = df
		}
	}

	if carrierIsUsable && float64(best.Magnitude) < carMag*offCarrierNeedsLift {
		return
	}
	return float64(best.FreqHz), float64(best.Magnitude), float64(best.PhaseDeg), float64(best.SNR)
}

func buildAxisCombined(x, y, z []float64) []float64 {
	n := len(x)
	if len(y) < n {
		n = len(y)
	}
	if len(z) < n {
		n = len(z)
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = math.Sqrt(x[i]*x[i] + y[i]*y[i] + z[i]*z[i])
	}
	return out
}

func dominantAxis(x, y, z []float64) ([]float64, uint8) {
	rmsAxis := func(v []float64) float64 {
		var s float64
		for _, x := range v {
			s += x * x
		}
		if len(v) == 0 {
			return 0
		}
		return math.Sqrt(s / float64(len(v)))
	}
	rx, ry, rz := rmsAxis(x), rmsAxis(y), rmsAxis(z)
	if rx >= ry && rx >= rz {
		return x, 1
	}
	if ry >= rz {
		return y, 2
	}
	return z, 3
}

// AnalyzeResonantResponse performs server-side demodulation for RAW_SYNC:
// exact carrier IQ plus a small top-N resonant peak search around the carrier.
func AnalyzeResonantResponse(x, y, z []float64, tsfUs uint64, carrierHz, fs float64) (primary DspPeak, peaks [8]DspPeak, nPeaks uint8, m ResonanceMetrics) {
	axis, axisCode := dominantAxis(x, y, z)
	if len(axis) == 0 {
		return
	}
	var axisMean, axisPwr float64
	for _, v := range axis {
		axisMean += v
	}
	axisMean /= float64(len(axis))
	for _, v := range axis {
		d := v - axisMean
		axisPwr += d * d
	}
	axisAcRms := math.Sqrt(axisPwr / float64(len(axis)))

	carMag, carPhase, carSNR := DemodulateIQ(axis, tsfUs, carrierHz, fs)
	primary = DspPeak{
		FreqHz:    float32(carrierHz),
		Magnitude: float32(carMag),
		PhaseDeg:  float32(carPhase),
		SNR:       float32(carSNR),
	}
	peaks[0] = primary
	nPeaks = 1

	m = ResonanceMetrics{
		CarrierFreqHz:   float32(carrierHz),
		CarrierMag:      float32(carMag),
		CarrierPhaseDeg: float32(carPhase),
		CarrierSNR:      float32(carSNR),
		AxisMeanG:       float32(axisMean),
		AxisACRMSG:      float32(axisAcRms),
		AxisCode:        axisCode,
	}
	if axisAcRms > 1e-12 && carMag > 1e-12 {
		m.CarrierToACDB = float32(20.0 * math.Log10(carMag/axisAcRms))
	}
	if carrierHz*0.5 >= 1 {
		subF := carrierHz * 0.5
		subMag, subPhase, subSNR := DemodulateIQ(axis, tsfUs, subF, fs)
		m.SubFreqHz = float32(subF)
		m.SubMag = float32(subMag)
		m.SubPhaseDeg = float32(subPhase)
		m.SubSNR = float32(subSNR)
	}
	if carrierHz*2 < fs*0.5 {
		h2F := carrierHz * 2
		h2Mag, h2Phase, h2SNR := DemodulateIQ(axis, tsfUs, h2F, fs)
		m.H2FreqHz = float32(h2F)
		m.H2Mag = float32(h2Mag)
		m.H2PhaseDeg = float32(h2Phase)
		m.H2SNR = float32(h2SNR)
	}
	if carrierHz*3 < fs*0.5 {
		h3F := carrierHz * 3
		h3Mag, h3Phase, h3SNR := DemodulateIQ(axis, tsfUs, h3F, fs)
		m.H3FreqHz = float32(h3F)
		m.H3Mag = float32(h3Mag)
		m.H3PhaseDeg = float32(h3Phase)
		m.H3SNR = float32(h3SNR)
	}

	combined := buildAxisCombined(x, y, z)
	scratch := &FFTScratch{}
	mag, freqs := ComputeFFTScanReuse(scratch, combined, len(combined), fs)
	if len(mag) < 8 || len(freqs) != len(mag) {
		m.DominantFreqHz = float32(carrierHz)
		m.DominantMag = float32(carMag)
		m.DominantPhaseDeg = float32(carPhase)
		m.DominantSNR = float32(carSNR)
		return
	}

	type cand struct {
		idx int
		mag float64
	}
	cands := make([]cand, 0, 7)
	loHz := carrierHz - 50
	hiHz := carrierHz + 50
	if loHz < 1 {
		loHz = 1
	}
	for i := 2; i < len(mag)-1; i++ {
		f := freqs[i]
		if f < loHz || f > hiHz {
			continue
		}
		if mag[i] <= mag[i-1] || mag[i] < mag[i+1] {
			continue
		}
		cands = append(cands, cand{idx: i, mag: mag[i]})
	}
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if cands[j].mag > cands[i].mag {
				cands[i], cands[j] = cands[j], cands[i]
			}
		}
	}
	if len(cands) == 0 {
		m.DominantFreqHz = float32(carrierHz)
		m.DominantMag = float32(carMag)
		m.DominantPhaseDeg = float32(carPhase)
		m.DominantSNR = float32(carSNR)
		return
	}

	maxPeaks := 8
	if len(cands) < maxPeaks {
		maxPeaks = len(cands)
	}
	minF, maxF := freqs[cands[0].idx], freqs[cands[0].idx]
	detected := make([]DspPeak, 0, maxPeaks)
	for i := 0; i < maxPeaks; i++ {
		f := freqs[cands[i].idx]
		pm, pp, ps := DemodulateIQ(axis, tsfUs, f, fs)
		pk := DspPeak{
			FreqHz:    float32(f),
			Magnitude: float32(pm),
			PhaseDeg:  float32(pp),
			SNR:       float32(ps),
		}
		peaks[i] = pk
		detected = append(detected, pk)
		if f < minF {
			minF = f
		}
		if f > maxF {
			maxF = f
		}
		nPeaks++
	}
	if nPeaks > uint8(maxPeaks) {
		nPeaks = uint8(maxPeaks)
	}
	carrierBinIdx := -1
	carrierBinErr := math.MaxFloat64
	for i, f := range freqs {
		err := math.Abs(f - carrierHz)
		if err < carrierBinErr {
			carrierBinErr = err
			carrierBinIdx = i
		}
	}
	if carrierBinIdx >= 0 && carrierBinIdx < len(mag) {
		m.CarrierFFTMag = float32(mag[carrierBinIdx])
	}
	var noiseSum float64
	var noiseN int
	for i, f := range freqs {
		if f < 5 {
			continue
		}
		if math.Abs(f-carrierHz) <= 20 || math.Abs(f-carrierHz*0.5) <= 20 || math.Abs(f-carrierHz*2) <= 20 || math.Abs(f-carrierHz*3) <= 20 {
			continue
		}
		noiseSum += mag[i]
		noiseN++
	}
	if noiseN > 0 {
		noiseFloor := noiseSum / float64(noiseN)
		m.NoiseFloorMag = float32(noiseFloor)
		if noiseFloor > 1e-12 && m.CarrierFFTMag > 0 {
			m.WaterfallSNRDB = float32(20.0 * math.Log10(float64(m.CarrierFFTMag)/noiseFloor))
		}
	}
	domF, domMag, domPhase, domSNR := chooseDominantPeak(carrierHz, carMag, carPhase, carSNR, detected)
	m.DominantFreqHz = float32(domF)
	m.DominantMag = float32(domMag)
	m.DominantPhaseDeg = float32(domPhase)
	m.DominantSNR = float32(domSNR)
	m.PhaseDeltaDeg = float32(normalizeDeg180(domPhase - carPhase))
	m.FreqOffsetHz = float32(domF - carrierHz)
	m.PeakSpreadHz = float32(maxF - minF)
	return
}
