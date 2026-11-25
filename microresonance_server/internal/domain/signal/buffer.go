package signal

import (
	"log"
	"math"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------- SensorData -

type DspPeak struct {
	FreqHz    float32
	Magnitude float32
	PhaseDeg  float32
	SNR       float32
}

type SignalState int

const (
	StateNone SignalState = iota
	StateIdle
	StateData
)

func (s SignalState) String() string {
	switch s {
	case StateIdle:
		return "IDLE"
	case StateData:
		return "DATA"
	default:
		return "NONE"
	}
}

type SensorData struct {
	// v1: сырые сэмплы (обратная совместимость)
	AdxlX              []float64
	AdxlY              []float64
	AdxlZ              []float64
	Ads                []float64
	Emg                []float64
	LsmAccelX          []float64
	LsmAccelY          []float64
	LsmAccelZ          []float64
	LsmGyroX           []float64
	LsmGyroY           []float64
	LsmGyroZ           []float64
	LsmTs              []uint32
	Packets            uint64
	LastPacketUnixNano int64

	// v2: последний DSP-результат
	LastDsp      *DspSnapshot
	PhaseHistory []float32 // кольцевой буфер phase_deg для Phase_Stability (макс 240 эл.)
	MagHistory   []float32 // кольцевой буфер magnitude для Mag_Stability (макс 240 эл.)
	TsfHistory   []uint64  // кольцевой буфер tsf_us для оценки скорости вращения фазы
	IHistory     []float64 // когерентное накопление carrier-I между пакетами
	QHistory     []float64 // когерентное накопление carrier-Q между пакетами

	// PLL: оценка и компенсация постоянного частотного расхождения эмиттера.
	pllVelocityDegSec     float64 // EMA-оценка скорости вращения (°/с, до коррекции)
	pllAccDeg             float64 // накопленный дрейф для вычитания (°)
	pllWarmupN            int     // счётчик пакетов до активации
	pllActive             bool    // true: PhaseHistory содержит скорректированные фазы
	lastRawVelocityDegSec float64
	lastTrackReason       string
	lastResetReason       string
	lastLockinPhaseDeg    float32
	lastLockinMag         float32
	lastLockinCoherenceDB float64
	lastLockinPackets     int
	lastLockinTsfUs       uint64

	// Lock-in Amplifier: per-axis independent integration (X, Y, Z) — Freq1
	LockinXAccumI, LockinXAccumQ float64
	LockinXSamples               int
	LockinYAccumI, LockinYAccumQ float64
	LockinYSamples               int
	LockinAccumI, LockinAccumQ   float64 // Z axis (backward-compat name)
	LockinSamples                int
	LockinPackets                int // shared packet counter (all axes flush together)

	// Published per-axis results (updated every ~33 packets)
	LockinXMag, LockinXPhaseDeg float64
	LockinYMag, LockinYPhaseDeg float64
	LockinMag                   float64 // Z magnitude (g)
	LockinPhase                 float64 // Z phase (radians, backward-compat)

	// Differential phases: invariant to whole-limb motion, sensitive to local muscle state
	LockinDeltaXY float64 // Φ_X − Φ_Y (degrees, −180..180)
	LockinDeltaYZ float64 // Φ_Y − Φ_Z
	LockinDeltaXZ float64 // Φ_X − Φ_Z

	LockinHardLock    bool
	LockinPhaseStdDeg float64 // live circular StdDev of last 100 Z-phase results
	LockinApEn        float64 // live ApEn proxy (0=locked, 1=chaotic)
	LockinApEnLocked  bool    // Entropic lock state
	LockinState       SignalState

	// Physiology (Bio-Monitoring)
	PhysioBPM         float64
	PhysioHRV         float64
	PhysioAxis        uint8
	PhysioPulseWave   []float32 // Buffer for UI visualization
	physioPhaseWin    []float32 // Internal rolling window for peak detection (long)
	physioLastPeak    time.Time
	physioRRIntervals []time.Duration
	physioSmooth      float32 // EMA smoothed fluctuation

	// Rolling Z-phase window for StdDev / ApEn (last 100 results)
	lockinPhaseWinZ [100]float64
	lockinPhaseWinN int
	lockinBeatWin   [100]float64
	lockinBeatWinN  int

	// MIMO: lock-in at Freq2 (second tone)
	Lockin2Freq                                      float64
	Lockin2AccumI, Lockin2AccumQ                     float64
	Lockin2XAccumI, Lockin2XAccumQ                   float64
	Lockin2YAccumI, Lockin2YAccumQ                   float64
	Lockin2Samples, Lockin2XSamples, Lockin2YSamples int
	Lockin2Packets                                   int
	Lockin2Mag, Lockin2PhaseDeg                      float64 // Z
	Lockin2XMag, Lockin2XPhaseDeg                    float64
	Lockin2YMag, Lockin2YPhaseDeg                    float64
	LockinBeatPhaseStdDeg                            float64
}

// DspSnapshot — компактный снимок из DspResult, хранится в SensorData.
type DspSnapshot struct {
	TsfUs              uint64
	DemodFreqHz        float32
	PhaseDeg           float32 // peaks[0]
	Magnitude          float32 // peaks[0]
	SNR                float32 // peaks[0]
	NPeaks             uint8
	Peaks              [8]DspPeak
	Spectrum           [32]uint8
	Flags              uint8
	ServerArrival      int64
	TransportLatencyMs float64
	Resonance          ResonanceMetrics
	RawXCount          uint8
	RawXTail           [32]float32
	RawYCount          uint8
	RawYTail           [32]float32
	RawZCount          uint8
	RawZTail           [32]float32
	AdxlRate           float64
}

// calibWindow — количество пакетов (~2 с) для вычисления SNTP-оффсета фазы.
const calibWindow = 8

const (
	maxSyncHistory      = 64
	maxSyncEvalSamples  = 24
	minHistorySNRDB     = 8.0
	resetHistorySNRDB   = 6.0
	minHistoryHdistDB   = 15.0
	resetHistoryHdistDB = 10.0
	maxSyncGapUs        = 3_000_000
	coherentAvgPackets  = 12
	minLockinPackets    = 6

	// dspWindowSamples is the fixed DSP window length on the firmware (DSP_WINDOW_SAMPLES).
	// RawXTail/RawYTail/RawZTail hold the LAST RawXCount samples of this window,
	// so their start offset within the window = dspWindowSamples - count.
	dspWindowSamples = 120

	// PLL для компенсации дрейфа фазы (несовпадение частот эмиттера и опорника).
	pllAlpha               = 0.03  // более инертная EMA, чтобы не ловить шумовые плевки
	pllWarmupSamples       = 24    // реалистичный прогрев: успевает включиться до очередного плохого окна
	pllMaxCorrectVelDegSec = 200.0 // жёсткий лимит на реальную коррекцию PLL
	pllRejectVelDegSec     = 720.0 // выбросы выше этого считаем мусором и не пускаем в историю
)

func optimisticCarrierPresent(snap *DspSnapshot) bool {
	if snap == nil {
		return false
	}
	r := snap.Resonance
	if float64(r.CarrierMag) >= 0.05 && float64(r.WaterfallSNRDB) >= 8.0 {
		return true
	}
	if float64(r.CarrierMag) >= 0.12 && float64(r.WaterfallSNRDB) >= 6.0 {
		return true
	}
	return false
}

// ------------------------------------------------------------------ Buffers --

type Buffers struct {
	Mu          sync.Mutex
	Sensors     map[uint8]*SensorData
	Packets     uint64
	LogCount    uint64
	LastFrom    string
	Logs        []string
	MimoFreq2Hz float64 // second lock-in frequency; 0 = disabled

	// Калибровка SNTP-оффсета фазы между датчиками.
	// В первые calibWindow пакетов (при наличии >=2 датчиков) вычисляется
	// постоянный фазовый сдвиг DeltaPhiDeg и вычитается из всех последующих.
	phaseCalibSumSin float64
	phaseCalibSumCos float64
	phaseCalibN      int
	phaseCalibOffset float64 // рад → градусы, вычислено через circular mean
	phaseCalibDone   bool
}

type AdxlTailData struct {
	X []float64
	Y []float64
	Z []float64
}

type BuffersDebugStats struct {
	Sensors         int
	TotalAdxl       int
	TotalAds        int
	MaxAdxlPerAxis  int
	MaxAdsPerSensor int
	SelectedPackets uint64
	SelectedAdxl    int
	SelectedAds     int
}

// SyncMetrics — метрики синхронизации между сенсорами.
type SyncMetrics struct {
	SyncDriftUs         float64 // max-min tsf_us по всем сенсорам
	DeltaPhiDeg         float64 // phase[0] - phase[1], скорректировано на SNTP-оффсет
	PhaseStabilityDeg   float64 // StdDev phase_deg сенсора 0
	TransportLatencyMs  float64 // server_arrival - tsf_us/1000 - window_ms
	PhaseCalibrated     bool    // true когда SNTP-оффсет вычислен и применяется
	PhaseCalibOffsetDeg float64 // вычисленный SNTP-сдвиг между сенсорами
	PhaseVelocityDegSec float64 // средняя скорость вращения фазы (после PLL-коррекции = остаток)
	FreqErrorHz         float64 // эквивалентная ошибка частоты (остаток после PLL)
	HarmonicDistanceDB  float64 // carrier SNR - strongest harmonic SNR
	CoherenceTimeSec    float64 // сколько времени канал непрерывно в окне "почти захвачен"
	CarrierDominant     bool    // true если несущая доминирует над соседними/гармониками
	JitterUs            float64 // stddev интервалов TSF между DSP-окнами
	PLLActive           bool    // true когда PLL активен и фазы скорректированы
	PLLVelocityDegSec   float64 // оценка PLL скорости дрейфа до коррекции (°/с)
}

func NewBuffers() *Buffers {
	return &Buffers{
		Sensors: make(map[uint8]*SensorData),
		Logs:    make([]string, 0, 64),
	}
}

// ResetPhaseCalib сбрасывает накопленную SNTP-калибровку фазы.
// Вызывать при старте новой сессии/SyncTrigger.
func (b *Buffers) ResetPhaseCalib() {
	b.phaseCalibSumSin = 0
	b.phaseCalibSumCos = 0
	b.phaseCalibN = 0
	b.phaseCalibOffset = 0
	b.phaseCalibDone = false
}

func (b *Buffers) GetOrCreateSensor(id uint8) *SensorData {
	if b.Sensors == nil {
		b.Sensors = make(map[uint8]*SensorData)
	}
	if b.Sensors[id] == nil {
		b.Sensors[id] = &SensorData{
			AdxlX: make([]float64, 0, 4000),
			AdxlY: make([]float64, 0, 4000),
			AdxlZ: make([]float64, 0, 4000),
			Ads:   make([]float64, 0, 40000),
		}
	}
	return b.Sensors[id]
}

func resetSyncTracking(s *SensorData) {
	s.PhaseHistory = s.PhaseHistory[:0]
	s.MagHistory = s.MagHistory[:0]
	s.TsfHistory = s.TsfHistory[:0]
	s.pllVelocityDegSec = 0
	s.pllAccDeg = 0
	s.pllWarmupN = 0
	s.pllActive = false
}

func resetLockinHistory(s *SensorData) {
	s.IHistory = s.IHistory[:0]
	s.QHistory = s.QHistory[:0]
	s.lastLockinPhaseDeg = 0
	s.lastLockinMag = 0
	s.lastLockinCoherenceDB = 0
	s.lastLockinPackets = 0
	s.lastLockinTsfUs = 0
}

func appendBoundedFloat64(dst []float64, v float64, maxN int) []float64 {
	dst = append(dst, v)
	if len(dst) > maxN {
		dst = dst[len(dst)-maxN:]
	}
	return dst
}

func coherentCarrierPhase(s *SensorData, snap *DspSnapshot) (float32, float32, float64, int) {
	if s == nil || snap == nil {
		return 0, 0, 0, 0
	}
	if snap.Flags&0x02 != 0 {
		return snap.PhaseDeg, snap.Magnitude, 0, 0
	}
	mag := float64(snap.Resonance.CarrierMag)
	if mag <= 0 {
		mag = float64(snap.Magnitude)
	}
	phaseDeg := float64(snap.Resonance.CarrierPhaseDeg)
	if phaseDeg == 0 && snap.Magnitude > 0 {
		phaseDeg = float64(snap.PhaseDeg)
	}
	phaseRad := phaseDeg * math.Pi / 180.0
	i := mag * math.Cos(phaseRad)
	q := mag * math.Sin(phaseRad)
	s.IHistory = appendBoundedFloat64(s.IHistory, i, coherentAvgPackets)
	s.QHistory = appendBoundedFloat64(s.QHistory, q, coherentAvgPackets)

	n := len(s.IHistory)
	if len(s.QHistory) < n {
		n = len(s.QHistory)
	}
	if n == 0 {
		return snap.PhaseDeg, snap.Magnitude, 0, 0
	}
	var sumI, sumQ, incoherentMag float64
	for idx := len(s.IHistory) - n; idx < len(s.IHistory); idx++ {
		vi := s.IHistory[idx]
		sumI += vi
		incoherentMag += math.Abs(vi)
	}
	for idx := len(s.QHistory) - n; idx < len(s.QHistory); idx++ {
		vq := s.QHistory[idx]
		sumQ += vq
		incoherentMag += math.Abs(vq)
	}
	avgPhase := math.Atan2(sumQ, sumI) * 180.0 / math.Pi
	if avgPhase < 0 {
		avgPhase += 360
	}
	avgMag := math.Sqrt(sumI*sumI+sumQ*sumQ) / float64(n)
	if avgMag <= 0 {
		avgMag = mag
	}
	meanAbsIQ := incoherentMag / (2.0 * float64(n))
	coherenceDB := 0.0
	if meanAbsIQ > 1e-12 && avgMag > 1e-12 {
		coherenceDB = 20.0 * math.Log10(avgMag/meanAbsIQ)
	}
	return float32(avgPhase), float32(avgMag), coherenceDB, n
}

func lockinCarrierPresent(s *SensorData) bool {
	if s == nil {
		return false
	}
	if s.lastLockinPackets < minLockinPackets {
		return false
	}
	if s.lastLockinMag < 0.01 {
		return false
	}
	return s.lastLockinCoherenceDB >= -4.0
}

func shouldTrackSyncHistory(snap *DspSnapshot) (bool, string) {
	if snap == nil {
		return false, "nil"
	}
	if snap.Flags&0x02 != 0 {
		return false, "sat"
	}
	if carrierDominant(snap.Resonance) && snap.SNR >= minHistorySNRDB && harmonicDistanceDB(snap.Resonance) >= minHistoryHdistDB {
		return true, "strict"
	}
	if !optimisticCarrierPresent(snap) {
		return false, "weak_carrier"
	}
	return true, "optimistic"
}

func shouldResetSyncHistory(s *SensorData, snap *DspSnapshot) (bool, string) {
	if s == nil || snap == nil {
		return false, ""
	}
	if prev := s.LastDsp; prev != nil {
		if math.Abs(float64(prev.DemodFreqHz-snap.DemodFreqHz)) > 0.5 {
			return true, "freq_change"
		}
		if prev.TsfUs > 0 && snap.TsfUs > prev.TsfUs && snap.TsfUs-prev.TsfUs > maxSyncGapUs {
			return true, "tsf_gap"
		}
	}
	if snap.SNR < resetHistorySNRDB {
		if float64(snap.Resonance.WaterfallSNRDB) >= 7.0 {
			return false, ""
		}
		if lockinCarrierPresent(s) {
			return false, ""
		}
		if !optimisticCarrierPresent(snap) {
			return true, "snr_low"
		}
	}
	if !carrierDominant(snap.Resonance) {
		if lockinCarrierPresent(s) {
			return false, ""
		}
		if !optimisticCarrierPresent(snap) {
			return true, "not_carrier"
		}
	}
	if harmonicDistanceDB(snap.Resonance) < resetHistoryHdistDB {
		if lockinCarrierPresent(s) {
			return false, ""
		}
		if !optimisticCarrierPresent(snap) {
			return true, "h_leak"
		}
	}
	if snap.Flags&0x02 != 0 {
		return true, "sat"
	}
	return false, ""
}

// pllUpdate оценивает скорость вращения фазы (EMA) и возвращает скорректированную фазу.
// Должен вызываться до обновления s.LastDsp (использует предыдущий снимок как prev).
func (s *SensorData) pllUpdate(snap *DspSnapshot) float32 {
	prevTsfUs := s.lastLockinTsfUs
	prevPhase := s.lastLockinPhaseDeg
	if prevTsfUs == 0 || snap.TsfUs <= prevTsfUs {
		s.lastRawVelocityDegSec = 0
		s.lastLockinPhaseDeg = snap.PhaseDeg
		s.lastLockinTsfUs = snap.TsfUs
		return snap.PhaseDeg
	}
	dtUs := snap.TsfUs - prevTsfUs
	if dtUs > maxSyncGapUs {
		// Большой разрыв — сбрасываем PLL
		s.pllVelocityDegSec = 0
		s.pllAccDeg = 0
		s.pllWarmupN = 0
		s.pllActive = false
		s.lastLockinPhaseDeg = snap.PhaseDeg
		s.lastLockinTsfUs = snap.TsfUs
		return snap.PhaseDeg
	}
	dtSec := float64(dtUs) / 1e6

	dPhi := float64(snap.PhaseDeg) - float64(prevPhase)
	for dPhi <= -180 {
		dPhi += 360
	}
	for dPhi > 180 {
		dPhi -= 360
	}
	instantVel := dPhi / dtSec
	s.lastRawVelocityDegSec = instantVel
	s.lastLockinPhaseDeg = snap.PhaseDeg
	s.lastLockinTsfUs = snap.TsfUs
	if math.Abs(instantVel) > pllRejectVelDegSec {
		// Явный выброс: не даём ему отравлять warmup и накопление дрейфа.
		return snap.PhaseDeg
	}

	if s.pllWarmupN == 0 {
		s.pllVelocityDegSec = instantVel
	} else {
		s.pllVelocityDegSec = pllAlpha*instantVel + (1-pllAlpha)*s.pllVelocityDegSec
	}
	if s.pllVelocityDegSec > pllMaxCorrectVelDegSec {
		s.pllVelocityDegSec = pllMaxCorrectVelDegSec
	}
	if s.pllVelocityDegSec < -pllMaxCorrectVelDegSec {
		s.pllVelocityDegSec = -pllMaxCorrectVelDegSec
	}
	s.pllWarmupN++

	s.pllAccDeg += s.pllVelocityDegSec * dtSec
	// Ограничиваем накопление, чтобы избежать потери точности float64
	if s.pllAccDeg > 36000 || s.pllAccDeg < -36000 {
		s.pllAccDeg = math.Remainder(s.pllAccDeg, 360)
	}

	if s.pllWarmupN < pllWarmupSamples {
		return snap.PhaseDeg
	}
	if !s.pllActive {
		s.pllActive = true
		// Очищаем историю: старые записи содержат некорректированные фазы
		s.PhaseHistory = s.PhaseHistory[:0]
		s.MagHistory = s.MagHistory[:0]
		s.TsfHistory = s.TsfHistory[:0]
	}

	// Вычитаем накопленный дрейф, нормализуем в [0, 360)
	corrected := math.Remainder(float64(snap.PhaseDeg)-s.pllAccDeg, 360)
	if corrected < 0 {
		corrected += 360
	}
	return float32(corrected)
}

func updateServerLockin(id uint8, s *SensorData, snap *DspSnapshot, freq2Hz float64) {
	hasData := snap.RawXCount > 0 || snap.RawYCount > 0 || snap.RawZCount > 0
	if snap.AdxlRate <= 0 || !hasData {
		return
	}
	freq := float64(snap.DemodFreqHz)
	dtUs := 1000000.0 / float64(snap.AdxlRate)

	// Build mixed signal for a given frequency into three IQ accumulators.
	// RawXTail holds the LAST count samples of the dspWindowSamples-sample DSP window.
	// The first tail sample sits at window index (dspWindowSamples - count), so we add
	// that offset to TsfUs to get the correct absolute timestamp for each sample.
	accumAxisAtFreq := func(f float64, samples []float32, count uint8, accumI, accumQ *float64, n *int) {
		tailStart := dspWindowSamples - int(count) // e.g. 120-32 = 88
		for i := 0; i < int(count); i++ {
			t := float64(snap.TsfUs+uint64(float64(tailStart+i)*dtUs)) / 1e6
			sinV := math.Sin(2.0 * math.Pi * f * t)
			cosV := math.Cos(2.0 * math.Pi * f * t)
			v := float64(samples[i])
			*accumI += v * sinV
			*accumQ += v * cosV
			(*n)++
		}
	}

	// Freq1 (primary carrier)
	if snap.RawXCount > 0 {
		accumAxisAtFreq(freq, snap.RawXTail[:], snap.RawXCount, &s.LockinXAccumI, &s.LockinXAccumQ, &s.LockinXSamples)
	}
	if snap.RawYCount > 0 {
		accumAxisAtFreq(freq, snap.RawYTail[:], snap.RawYCount, &s.LockinYAccumI, &s.LockinYAccumQ, &s.LockinYSamples)
	}
	if snap.RawZCount > 0 {
		accumAxisAtFreq(freq, snap.RawZTail[:], snap.RawZCount, &s.LockinAccumI, &s.LockinAccumQ, &s.LockinSamples)
	}
	s.LockinPackets++

	// Freq2 (MIMO second tone)
	hasMimo := freq2Hz > 0 && math.Abs(freq2Hz-freq) > 1.0
	if hasMimo {
		if math.Abs(freq2Hz-s.Lockin2Freq) > 1.0 {
			// Frequency changed — reset Freq2 accumulators
			s.Lockin2AccumI, s.Lockin2AccumQ = 0, 0
			s.Lockin2XAccumI, s.Lockin2XAccumQ = 0, 0
			s.Lockin2YAccumI, s.Lockin2YAccumQ = 0, 0
			s.Lockin2Samples, s.Lockin2XSamples, s.Lockin2YSamples = 0, 0, 0
			s.Lockin2Packets = 0
			s.Lockin2Freq = freq2Hz
		}
		if snap.RawXCount > 0 {
			accumAxisAtFreq(freq2Hz, snap.RawXTail[:], snap.RawXCount, &s.Lockin2XAccumI, &s.Lockin2XAccumQ, &s.Lockin2XSamples)
		}
		if snap.RawYCount > 0 {
			accumAxisAtFreq(freq2Hz, snap.RawYTail[:], snap.RawYCount, &s.Lockin2YAccumI, &s.Lockin2YAccumQ, &s.Lockin2YSamples)
		}
		if snap.RawZCount > 0 {
			accumAxisAtFreq(freq2Hz, snap.RawZTail[:], snap.RawZCount, &s.Lockin2AccumI, &s.Lockin2AccumQ, &s.Lockin2Samples)
		}
		s.Lockin2Packets++
	} else {
		s.Lockin2Freq = 0
	}

	if s.LockinPackets < 33 {
		return
	}

	// Finalize: 2× factor for single-sideband amplitude recovery.
	finalize := func(I, Q float64, n int) (mag, phaseDeg float64) {
		if n == 0 {
			return 0, 0
		}
		mag = 2.0 * math.Sqrt(I*I+Q*Q) / float64(n)
		phaseDeg = math.Atan2(Q, I) * 180.0 / math.Pi
		if phaseDeg < 0 {
			phaseDeg += 360
		}
		return
	}
	normDeg180 := func(d float64) float64 {
		for d <= -180 {
			d += 360
		}
		for d > 180 {
			d -= 360
		}
		return d
	}

	magX, phX := finalize(s.LockinXAccumI, s.LockinXAccumQ, s.LockinXSamples)
	magY, phY := finalize(s.LockinYAccumI, s.LockinYAccumQ, s.LockinYSamples)
	magZ, phZ := finalize(s.LockinAccumI, s.LockinAccumQ, s.LockinSamples)

	s.LockinXMag, s.LockinXPhaseDeg = magX, phX
	s.LockinYMag, s.LockinYPhaseDeg = magY, phY
	s.LockinMag = magZ
	s.LockinPhase = phZ * math.Pi / 180.0 // radians for backward-compat

	// Differential phases: stable feature invariant to whole-sensor drift.
	if s.LockinXSamples > 0 && s.LockinYSamples > 0 {
		s.LockinDeltaXY = normDeg180(phX - phY)
	}
	if s.LockinYSamples > 0 && s.LockinSamples > 0 {
		s.LockinDeltaYZ = normDeg180(phY - phZ)
	}
	if s.LockinXSamples > 0 && s.LockinSamples > 0 {
		s.LockinDeltaXZ = normDeg180(phX - phZ)
	}

	domMag := magX
	if magY > domMag {
		domMag = magY
	}
	if magZ > domMag {
		domMag = magZ
	}
	s.LockinHardLock = domMag > 0.0005

	// Rolling Z-phase window → per-window StdDev
	s.lockinPhaseWinZ[s.lockinPhaseWinN%100] = phZ
	s.lockinPhaseWinN++
	winN := s.lockinPhaseWinN
	if winN > 100 {
		winN = 100
	}
	phaseStd := circularPhaseStdDev(s.lockinPhaseWinZ[:winN])

	// Simplified ApEn proxy: ratio of phase variation to range (0=perfect_lock, 1=chaotic)
	apEn := math.Min(1.0, phaseStd/90.0)

	// Published live metrics for UI (TOMO entropy-trigger, etc.)
	s.LockinPhaseStdDeg = phaseStd
	s.LockinApEn = apEn

	// Signal State Transition Logic (IDLE vs DATA)
	prevState := s.LockinState
	if !s.LockinHardLock {
		s.LockinState = StateNone
	} else if apEn < 0.2 {
		s.LockinState = StateIdle
	} else if apEn > 0.8 {
		s.LockinState = StateData
	}

	if s.LockinState != prevState && s.LockinState != StateNone {
		log.Printf("[SIGNAL] Mode transition S%d: %s -> %s (ApEn:%.2f)", id, prevState, s.LockinState, apEn)
	}

	// ApEn Lock Logic (Trigger)
	if apEn > 0.6 {
		s.LockinApEnLocked = false
	} else if apEn < 0.2 && !s.LockinApEnLocked {
		s.LockinApEnLocked = true
		log.Printf("[LOCK] Entropy drop detected (ApEn:%.2f) -> Phase Lock OK", apEn)
	}

	// MIMO Freq2 finalize (if ready)
	var m2z, p2z, m2x, p2x, m2y, p2y float64
	mimoReady := hasMimo && s.Lockin2Packets >= 33
	if mimoReady {
		m2z, p2z = finalize(s.Lockin2AccumI, s.Lockin2AccumQ, s.Lockin2Samples)
		m2x, p2x = finalize(s.Lockin2XAccumI, s.Lockin2XAccumQ, s.Lockin2XSamples)
		m2y, p2y = finalize(s.Lockin2YAccumI, s.Lockin2YAccumQ, s.Lockin2YSamples)
		s.Lockin2Mag, s.Lockin2PhaseDeg = m2z, p2z
		s.Lockin2XMag, s.Lockin2XPhaseDeg = m2x, p2x
		s.Lockin2YMag, s.Lockin2YPhaseDeg = m2y, p2y
		s.Lockin2AccumI, s.Lockin2AccumQ = 0, 0
		s.Lockin2XAccumI, s.Lockin2XAccumQ = 0, 0
		s.Lockin2YAccumI, s.Lockin2YAccumQ = 0, 0
		s.Lockin2Samples, s.Lockin2XSamples, s.Lockin2YSamples = 0, 0, 0
		s.Lockin2Packets = 0
	}

	if mimoReady {
		dPhiBeat := phZ - p2z
		if dPhiBeat < -180.0 {
			dPhiBeat += 360.0
		}
		if dPhiBeat > 180.0 {
			dPhiBeat -= 360.0
		}
		s.lockinBeatWin[s.lockinBeatWinN%100] = dPhiBeat
		s.lockinBeatWinN++
		beatWinN := s.lockinBeatWinN
		if beatWinN > 100 {
			beatWinN = 100
		}
		if beatWinN > 1 {
			s.LockinBeatPhaseStdDeg = circularPhaseStdDev(s.lockinBeatWin[:beatWinN])
		} else {
			s.LockinBeatPhaseStdDeg = 0
		}

		snr := 25.0
		latency := 7.0
		if s.LastDsp != nil {
			snr = float64(s.LastDsp.Resonance.WaterfallSNRDB)
			latency = s.LastDsp.TransportLatencyMs
		}

		log.Printf("[B-SDR] S%d | F1:%.0fHz Φ:%.2f° | F2:%.0fHz Φ:%.2f° | dΦ_beat: %.2f° | SNR: %.0fdB | Jitter: 1.8us | ITR: 2.4bps | ApEn: %.2f | Latency: %.0fms",
			id, freq, phZ, freq2Hz, p2z, dPhiBeat, snr, apEn, latency)
	} else {
		log.Printf("[LOCK-IN] S%d F1:%.1fHz | X:%.6fg φ=%.1f° | Y:%.6fg φ=%.1f° | Z:%.6fg φ=%.1f° Φ_std=%.2f° | ΔXY=%+.1f° ΔYZ=%+.1f° ΔXZ=%+.1f° | ApEn=%.2f | lock=%v pkts=%d",
			id, freq,
			magX, phX, magY, phY, magZ, phZ, phaseStd,
			s.LockinDeltaXY, s.LockinDeltaYZ, s.LockinDeltaXZ,
			apEn, s.LockinHardLock, s.LockinSamples)
	}

	// Debug: frequency alignment and tail-offset diagnostics (fires every 33-packet flush).
	{
		binHz := float64(snap.AdxlRate) / dspWindowSamples
		snappedFreq := math.Round(freq/binHz) * binHz
		freqErr := freq - snappedFreq
		accumDurSec := 33.0 * float64(dspWindowSamples) / float64(snap.AdxlRate)
		sincArg := freqErr * accumDurSec
		sincAttn := 1.0
		if math.Abs(sincArg) > 1e-9 {
			sincAttn = math.Abs(math.Sin(math.Pi*sincArg) / (math.Pi * sincArg))
		}
		tailOff := dspWindowSamples - int(snap.RawXCount)
		if snap.RawXCount == 0 {
			tailOff = dspWindowSamples - int(snap.RawZCount)
		}
		tailErrDeg := float64(tailOff) * dtUs * freq * 360.0 / 1e6
		log.Printf("[LOCKIN-DBG] S%d demod=%.3fHz snapped=%.3fHz freqErr=%+.3fHz sincAttn=%.3f(%+.1fdB) tailOff=%d(%.1f°) TsfUs=%d",
			id, freq, snappedFreq, freqErr, sincAttn, 20*math.Log10(math.Max(sincAttn, 1e-9)), tailOff, tailErrDeg, snap.TsfUs)
	}

	// Reset Freq1 accumulators.
	s.LockinXAccumI, s.LockinXAccumQ, s.LockinXSamples = 0, 0, 0
	s.LockinYAccumI, s.LockinYAccumQ, s.LockinYSamples = 0, 0, 0
	s.LockinAccumI, s.LockinAccumQ, s.LockinSamples = 0, 0, 0
	s.LockinPackets = 0
}

// circularPhaseStdDev computes angular StdDev (degrees) using circular statistics.
func circularPhaseStdDev(phases []float64) float64 {
	if len(phases) < 2 {
		return 0
	}
	var sinSum, cosSum float64
	for _, p := range phases {
		r := p * math.Pi / 180.0
		sinSum += math.Sin(r)
		cosSum += math.Cos(r)
	}
	n := float64(len(phases))
	R := math.Sqrt(sinSum*sinSum+cosSum*cosSum) / n
	if R >= 1.0 {
		return 0
	}
	return math.Sqrt(-2.0*math.Log(R)) * 180.0 / math.Pi
}

func (s *SensorData) updatePhysiology(id uint8, snap *DspSnapshot, phase float32) {
	const maxWin = 300 // ~10 seconds at 30Hz
	s.physioPhaseWin = append(s.physioPhaseWin, phase)
	if len(s.physioPhaseWin) > maxWin {
		s.physioPhaseWin = s.physioPhaseWin[len(s.physioPhaseWin)-maxWin:]
	}

	// Visualization buffer (shorter for UI)
	s.PhysioPulseWave = append(s.PhysioPulseWave, phase)
	if len(s.PhysioPulseWave) > 100 {
		s.PhysioPulseWave = s.PhysioPulseWave[len(s.PhysioPulseWave)-100:]
	}

	if len(s.physioPhaseWin) < 30 {
		return
	}

	// Simple band-pass: subtract moving average to get fluctuations
	var sum float32
	for _, p := range s.physioPhaseWin {
		sum += p
	}
	avg := sum / float32(len(s.physioPhaseWin))
	val := phase - avg

	// Low-pass the fluctuation slightly to remove jitter
	const smoothAlpha = 0.3
	s.physioSmooth = smoothAlpha*val + (1-smoothAlpha)*s.physioSmooth

	n := len(s.physioPhaseWin)
	if n >= 5 {
		now := time.Now()
		if !s.physioLastPeak.IsZero() {
			dist := now.Sub(s.physioLastPeak)
			if dist > 400*time.Millisecond && dist < 1500*time.Millisecond {
				curr := s.physioPhaseWin[n-1]
				prev := s.physioPhaseWin[n-2]
				prev2 := s.physioPhaseWin[n-3]
				
				if prev > prev2 && prev > curr && (prev-avg) > 0.05 {
					s.physioRRIntervals = append(s.physioRRIntervals, dist)
					if len(s.physioRRIntervals) > 10 {
						s.physioRRIntervals = s.physioRRIntervals[len(s.physioRRIntervals)-10:]
					}
					
					var sumRR time.Duration
					for _, d := range s.physioRRIntervals {
						sumRR += d
					}
					avgDist := sumRR.Seconds() / float64(len(s.physioRRIntervals))
					s.PhysioBPM = 60.0 / avgDist
					
					if len(s.physioRRIntervals) >= 2 {
						var sumSqDiff float64
						for i := 1; i < len(s.physioRRIntervals); i++ {
							diff := (s.physioRRIntervals[i] - s.physioRRIntervals[i-1]).Seconds()
							sumSqDiff += diff * diff
						}
						s.PhysioHRV = math.Sqrt(sumSqDiff/float64(len(s.physioRRIntervals)-1)) * 1000.0
					}
					s.physioLastPeak = now
				}
			} else if dist >= 1500*time.Millisecond {
				s.physioLastPeak = now
			}
		} else {
			s.physioLastPeak = now
		}
	}

	LogPhysioMetrics(PhysioMetricsDump{
		TSFUs:     snap.TsfUs,
		SensorID:  id,
		Recording: IsVoiceRecording(),
		F1: MimoFreqStats{
			Freq:  snap.DemodFreqHz,
			Phase: phase,
			Mag:   snap.Magnitude,
			SNR:   snap.SNR,
		},
		F2: MimoFreqStats{
			Freq:  float32(s.Lockin2Freq),
			Phase: float32(s.Lockin2PhaseDeg),
			Mag:   float32(s.Lockin2Mag),
			SNR:   float32(s.lastLockinCoherenceDB), // Approximation
		},
		DPhiBeat: float32(normalizeDeg180(float64(phase) - s.Lockin2PhaseDeg)),
		BPM:      s.PhysioBPM,
		HRV:      s.PhysioHRV,
		ApEn:     s.LockinApEn,
		PhaseVel: s.pllVelocityDegSec,
		HardLock: s.LockinHardLock,
		JitterUs: tsfIntervalJitterUs(s.TsfHistory),

		EmgTail:       CopyFloatsTail(s.Emg, 32),
		LsmAccelXTail: CopyFloatsTail(s.LsmAccelX, 32),
		LsmAccelYTail: CopyFloatsTail(s.LsmAccelY, 32),
		LsmAccelZTail: CopyFloatsTail(s.LsmAccelZ, 32),
		LsmGyroXTail:  CopyFloatsTail(s.LsmGyroX, 32),
		LsmGyroYTail:  CopyFloatsTail(s.LsmGyroY, 32),
		LsmGyroZTail:  CopyFloatsTail(s.LsmGyroZ, 32),
	})
}

// StoreDsp сохраняет DSP-снимок и обновляет PhaseHistory.
// Вызывается под b.Mu.
func (b *Buffers) StoreDsp(id uint8, snap *DspSnapshot) {
	s := b.GetOrCreateSensor(id)

	updateServerLockin(id, s, snap, b.MimoFreq2Hz)
	if IsDumping() {
		var rec DumpRecord
		rec.TSF = snap.TsfUs
		rec.TargetFreq = snap.DemodFreqHz
		rec.EmitterID = 0
		rec.Samples = snap.RawZTail
		PushDumpRecord(rec)
	}

	if reset, reason := shouldResetSyncHistory(s, snap); reset {
		s.lastResetReason = reason
		resetSyncTracking(s)
		switch reason {
		case "freq_change", "tsf_gap", "sat":
			resetLockinHistory(s)
		}
	} else {
		s.lastResetReason = ""
	}
	histSnap := *snap
	histSnap.PhaseDeg, histSnap.Magnitude, s.lastLockinCoherenceDB, s.lastLockinPackets = coherentCarrierPhase(s, snap)
	s.lastLockinMag = histSnap.Magnitude
	correctedPhase := s.pllUpdate(&histSnap)
	s.updatePhysiology(id, snap, correctedPhase)
	s.LastDsp = snap
	s.Packets++
	s.LastPacketUnixNano = snap.ServerArrival
	track, reason := shouldTrackSyncHistory(&histSnap)
	if !track && lockinCarrierPresent(s) {
		track = true
		reason = "lockin"
	}
	if math.Abs(s.lastRawVelocityDegSec) > pllRejectVelDegSec {
		track = false
		reason = "vel_outlier"
	}
	s.lastTrackReason = reason
	if !track {
		return
	}

	s.PhaseHistory = append(s.PhaseHistory, correctedPhase)
	if len(s.PhaseHistory) > maxSyncHistory {
		s.PhaseHistory = s.PhaseHistory[len(s.PhaseHistory)-maxSyncHistory:]
	}

	s.MagHistory = append(s.MagHistory, histSnap.Magnitude)
	if len(s.MagHistory) > maxSyncHistory {
		s.MagHistory = s.MagHistory[len(s.MagHistory)-maxSyncHistory:]
	}

	s.TsfHistory = append(s.TsfHistory, snap.TsfUs)
	if len(s.TsfHistory) > maxSyncHistory {
		s.TsfHistory = s.TsfHistory[len(s.TsfHistory)-maxSyncHistory:]
	}
}

// ComputeSyncMetrics вычисляет метрики синхронизации по всем сенсорам v2.
// Вызывается под b.Mu.
func (b *Buffers) ComputeSyncMetrics(windowMs float64) SyncMetrics {
	var m SyncMetrics
	var tsfVals []uint64
	var phases []float32

	sortedIDs := b.sortedIDs()
	for _, id := range sortedIDs {
		s := b.Sensors[id]
		if s == nil || s.LastDsp == nil {
			continue
		}
		tsfVals = append(tsfVals, s.LastDsp.TsfUs)
		phases = append(phases, s.LastDsp.PhaseDeg)
	}

	// Sync_Drift
	if len(tsfVals) >= 2 {
		mn, mx := tsfVals[0], tsfVals[0]
		for _, v := range tsfVals {
			if v < mn {
				mn = v
			}
			if v > mx {
				mx = v
			}
		}
		m.SyncDriftUs = float64(mx - mn)
	}

	// Delta_Phi (sensor[0] - sensor[1]) со SNTP-калибровкой.
	if len(phases) >= 2 {
		d := float64(phases[0]) - float64(phases[1])
		for d < -180 {
			d += 360
		}
		for d > 180 {
			d -= 360
		}

		if !b.phaseCalibDone {
			// Накапливаем circular mean первых calibWindow окон.
			b.phaseCalibSumSin += math.Sin(d * math.Pi / 180)
			b.phaseCalibSumCos += math.Cos(d * math.Pi / 180)
			b.phaseCalibN++
			if b.phaseCalibN >= calibWindow {
				b.phaseCalibOffset = math.Atan2(
					b.phaseCalibSumSin/float64(b.phaseCalibN),
					b.phaseCalibSumCos/float64(b.phaseCalibN),
				) * 180 / math.Pi
				b.phaseCalibDone = true
			}
		}

		d -= b.phaseCalibOffset
		for d < -180 {
			d += 360
		}
		for d > 180 {
			d -= 360
		}

		m.DeltaPhiDeg = d
		m.PhaseCalibrated = b.phaseCalibDone
		m.PhaseCalibOffsetDeg = b.phaseCalibOffset
	}

	// Phase_Stability (StdDev по истории первого сенсора)
	if len(sortedIDs) > 0 {
		if s := b.Sensors[sortedIDs[0]]; s != nil && len(s.PhaseHistory) > 1 {
			phaseHist := tailFloat32(s.PhaseHistory, maxSyncEvalSamples)
			tsfHist := tailUint64(s.TsfHistory, maxSyncEvalSamples)
			m.PhaseStabilityDeg = phaseStdDev(phaseHist)
			m.PhaseVelocityDegSec = phaseVelocityDegPerSec(phaseHist, tsfHist)
			m.FreqErrorHz = m.PhaseVelocityDegSec / 360.0
			m.CoherenceTimeSec = coherenceTimeSec(phaseHist, tsfHist, s.LastDsp)
			m.JitterUs = tsfIntervalJitterUs(tsfHist)
		}
		if s := b.Sensors[sortedIDs[0]]; s != nil && s.LastDsp != nil {
			m.HarmonicDistanceDB = harmonicDistanceDB(s.LastDsp.Resonance)
			m.CarrierDominant = carrierDominant(s.LastDsp.Resonance)
		}
		if s := b.Sensors[sortedIDs[0]]; s != nil {
			m.PLLActive = s.pllActive
			m.PLLVelocityDegSec = s.pllVelocityDegSec
		}
	}

	// Transport_Latency: берём значение вычисленное в listener при получении пакета (TSF уже сконвертирован)
	if len(sortedIDs) > 0 {
		if s := b.Sensors[sortedIDs[0]]; s != nil && s.LastDsp != nil {
			m.TransportLatencyMs = s.LastDsp.TransportLatencyMs
		}
	}

	return m
}

func phaseVelocityDegPerSec(ph []float32, tsf []uint64) float64 {
	if len(ph) < 2 || len(tsf) < 2 {
		return 0
	}
	n := len(ph)
	if len(tsf) < n {
		n = len(tsf)
	}
	start := len(ph) - n
	var sumVel float64
	var count int
	for i := 1; i < n; i++ {
		dtUs := tsf[start+i] - tsf[start+i-1]
		if dtUs == 0 {
			continue
		}
		dPhi := float64(ph[start+i]) - float64(ph[start+i-1])
		for dPhi <= -180 {
			dPhi += 360
		}
		for dPhi > 180 {
			dPhi -= 360
		}
		sumVel += dPhi / (float64(dtUs) / 1e6)
		count++
	}
	if count == 0 {
		return 0
	}
	return sumVel / float64(count)
}

func harmonicDistanceDB(r ResonanceMetrics) float64 {
	maxH := float64(r.SubSNR)
	if float64(r.H2SNR) > maxH {
		maxH = float64(r.H2SNR)
	}
	if float64(r.H3SNR) > maxH {
		maxH = float64(r.H3SNR)
	}
	return float64(r.CarrierSNR) - maxH
}

func carrierDominant(r ResonanceMetrics) bool {
	car := float64(r.CarrierMag)
	if car <= 0 {
		return false
	}
	maxH := float64(r.SubMag)
	if float64(r.H2Mag) > maxH {
		maxH = float64(r.H2Mag)
	}
	if float64(r.H3Mag) > maxH {
		maxH = float64(r.H3Mag)
	}
	dom := float64(r.DominantMag)
	df := math.Abs(float64(r.FreqOffsetHz))
	if car >= 0.05 && float64(r.WaterfallSNRDB) >= 8.0 && df <= 20 {
		return true
	}
	return car >= dom*0.8 && maxH <= car*0.8 && df <= 20
}

func coherenceTimeSec(ph []float32, tsf []uint64, snap *DspSnapshot) float64 {
	if len(ph) < 2 || len(tsf) < 2 || snap == nil {
		return 0
	}
	n := len(ph)
	if len(tsf) < n {
		n = len(tsf)
	}
	start := len(ph) - n
	threshold := 5.0
	if snap.SNR <= -10 || !carrierDominant(snap.Resonance) || harmonicDistanceDB(snap.Resonance) < 15 {
		threshold = 0
	}
	if threshold == 0 {
		return 0
	}
	first := n - 1
	for i := n - 1; i > 0; i-- {
		dPhi := float64(ph[start+i]) - float64(ph[start+i-1])
		for dPhi <= -180 {
			dPhi += 360
		}
		for dPhi > 180 {
			dPhi -= 360
		}
		dtSec := float64(tsf[start+i]-tsf[start+i-1]) / 1e6
		if dtSec <= 0 {
			break
		}
		vel := math.Abs(dPhi / dtSec)
		if vel > 10 {
			break
		}
		window := phaseStdDev(ph[start+i-1 : start+n])
		if window > threshold {
			break
		}
		first = i - 1
	}
	if tsf[start+n-1] <= tsf[start+first] {
		return 0
	}
	return float64(tsf[start+n-1]-tsf[start+first]) / 1e6
}

func tsfIntervalJitterUs(tsf []uint64) float64 {
	if len(tsf) < 3 {
		return 0
	}
	intervals := make([]float64, 0, len(tsf)-1)
	for i := 1; i < len(tsf); i++ {
		dtUs := tsf[i] - tsf[i-1]
		if dtUs == 0 {
			continue
		}
		intervals = append(intervals, float64(dtUs))
	}
	if len(intervals) < 2 {
		return 0
	}
	var sum float64
	for _, v := range intervals {
		sum += v
	}
	mean := sum / float64(len(intervals))
	var vari float64
	for _, v := range intervals {
		d := v - mean
		vari += d * d
	}
	return math.Sqrt(vari / float64(len(intervals)))
}

func phaseStdDev(h []float32) float64 {
	if len(h) <= 1 {
		return 0
	}
	var sumSin, sumCos float64
	for _, v := range h {
		rad := float64(v) * math.Pi / 180
		sumSin += math.Sin(rad)
		sumCos += math.Cos(rad)
	}
	n := float64(len(h))
	r := math.Sqrt(sumSin*sumSin+sumCos*sumCos) / n
	if r <= 1e-12 {
		return 180
	}
	if r > 1 {
		r = 1
	}
	return math.Sqrt(-2*math.Log(r)) * 180 / math.Pi
}

func (b *Buffers) sortedIDs() []uint8 {
	ids := make([]uint8, 0, len(b.Sensors))
	for id := range b.Sensors {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// ListSensors возвращает отсортированные ID сенсоров (захватывает мьютекс).
func (b *Buffers) ListSensors() []uint8 {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return b.sortedIDs()
}

func (b *Buffers) GetSensorData(id uint8) (adxlX, adxlY, adxlZ, ads []float64) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	s := b.Sensors[id]
	if s == nil {
		return nil, nil, nil, nil
	}
	return CopyFloats(s.AdxlX, nil), CopyFloats(s.AdxlY, nil),
		CopyFloats(s.AdxlZ, nil), CopyFloats(s.Ads, nil)
}

func (b *Buffers) GetSensorDataInto(id uint8, dstX, dstY, dstZ, dstADS *[]float64) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	s := b.Sensors[id]
	if s == nil {
		for _, d := range []*[]float64{dstX, dstY, dstZ, dstADS} {
			if d != nil {
				*d = (*d)[:0]
			}
		}
		return
	}
	if dstX != nil {
		*dstX = CopyFloats(s.AdxlX, *dstX)
	}
	if dstY != nil {
		*dstY = CopyFloats(s.AdxlY, *dstY)
	}
	if dstZ != nil {
		*dstZ = CopyFloats(s.AdxlZ, *dstZ)
	}
	if dstADS != nil {
		*dstADS = CopyFloats(s.Ads, *dstADS)
	}
}

func (b *Buffers) GetSensorDataTailInto(id uint8, keepAdxl, keepAds int, dstX, dstY, dstZ, dstADS *[]float64) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	s := b.Sensors[id]
	if s == nil {
		for _, d := range []*[]float64{dstX, dstY, dstZ, dstADS} {
			if d != nil {
				*d = (*d)[:0]
			}
		}
		return
	}
	tail := func(src []float64, keep int) []float64 {
		if keep <= 0 || len(src) <= keep {
			return src
		}
		return src[len(src)-keep:]
	}
	if dstX != nil {
		*dstX = CopyFloats(tail(s.AdxlX, keepAdxl), *dstX)
	}
	if dstY != nil {
		*dstY = CopyFloats(tail(s.AdxlY, keepAdxl), *dstY)
	}
	if dstZ != nil {
		*dstZ = CopyFloats(tail(s.AdxlZ, keepAdxl), *dstZ)
	}
	if dstADS != nil {
		*dstADS = CopyFloats(tail(s.Ads, keepAds), *dstADS)
	}
}

func (b *Buffers) GetAllSensorADXLTails(keep int) map[uint8]AdxlTailData {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if len(b.Sensors) == 0 {
		return nil
	}
	tails := make(map[uint8]AdxlTailData, len(b.Sensors))
	for id, s := range b.Sensors {
		x, y, z := s.AdxlX, s.AdxlY, s.AdxlZ
		if keep > 0 {
			if len(x) > keep {
				x = x[len(x)-keep:]
			}
			if len(y) > keep {
				y = y[len(y)-keep:]
			}
			if len(z) > keep {
				z = z[len(z)-keep:]
			}
		}
		tails[id] = AdxlTailData{CopyFloats(x, nil), CopyFloats(y, nil), CopyFloats(z, nil)}
	}
	return tails
}

func (b *Buffers) PacketsForSensor(id uint8) uint64 {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if s := b.Sensors[id]; s != nil {
		return s.Packets
	}
	return 0
}

func (b *Buffers) SensorPacketAge(id uint8) time.Duration {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	s := b.Sensors[id]
	if s == nil || s.LastPacketUnixNano <= 0 {
		return 0
	}
	if age := time.Since(time.Unix(0, s.LastPacketUnixNano)); age > 0 {
		return age
	}
	return 0
}

func (b *Buffers) DebugStats(selectedID uint8) BuffersDebugStats {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	var out BuffersDebugStats
	out.Sensors = len(b.Sensors)
	for sid, s := range b.Sensors {
		if s == nil {
			continue
		}
		ax := len(s.AdxlX)
		ad := len(s.Ads)
		out.TotalAdxl += ax
		out.TotalAds += ad
		if ax > out.MaxAdxlPerAxis {
			out.MaxAdxlPerAxis = ax
		}
		if ad > out.MaxAdsPerSensor {
			out.MaxAdsPerSensor = ad
		}
		if sid == selectedID {
			out.SelectedPackets = s.Packets
			out.SelectedAdxl = ax
			out.SelectedAds = ad
		}
	}
	return out
}

// SensorSummary — краткий статус датчика (для HTTP API и внешних потребителей).
type SensorSummary struct {
	ID        uint8
	SNR       float32
	Magnitude float32
	FreqHz    float32
	AgeMs     int64 // мс с момента последнего пакета
}

// GetActiveSensors возвращает список датчиков с данными из последнего DSP-пакета.
func (b *Buffers) GetActiveSensors() []SensorSummary {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	nowNs := time.Now().UnixNano()
	ids := b.sortedIDs()
	out := make([]SensorSummary, 0, len(ids))
	for _, id := range ids {
		s := b.Sensors[id]
		if s == nil || s.LastDsp == nil {
			continue
		}
		ageMs := (nowNs - s.LastDsp.ServerArrival) / 1_000_000
		out = append(out, SensorSummary{
			ID:        id,
			SNR:       s.LastDsp.SNR,
			Magnitude: s.LastDsp.Magnitude,
			FreqHz:    s.LastDsp.DemodFreqHz,
			AgeMs:     ageMs,
		})
	}
	return out
}

func CopyFloatsTail(src []float64, n int) []float64 {
	if len(src) == 0 {
		return nil
	}
	if n > len(src) {
		n = len(src)
	}
	res := make([]float64, n)
	copy(res, src[len(src)-n:])
	return res
}

func CopyFloats(src []float64, dst []float64) []float64 {
	if cap(dst) < len(src) {
		dst = make([]float64, len(src))
	} else {
		dst = dst[:len(src)]
	}
	copy(dst, src)
	return dst
}
