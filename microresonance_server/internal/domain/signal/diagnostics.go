package signal

import (
	"math"
	"time"
)

// DiagSensorSnap is a per-sensor snapshot for diagnostic logging.
type DiagSensorSnap struct {
	ID                 uint8
	SNR                float32
	Magnitude          float32
	PhaseDeg           float32
	DemodFreqHz        float32
	NPeaks             uint8
	Peaks              [8]DspPeak
	TransportLatencyMs float64
	PhaseStabilityDeg  float64
	MagStabilityG      float64
	PacketsTotal       uint64
	PacketAgeMs        int64
	Flags              uint8
	IsCompact          bool
	TsfUs              uint64 // TSF последнего пакета (WiFi beacon мкс)
	Resonance          ResonanceMetrics
	RawZCount          uint8
	RawZTail           [32]float32
	PhaseHistoryLen    int
	RawVelocityDegSec  float64
	TrackReason        string
	ResetReason        string
	PLLWarmupN         int
	PLLActive          bool
	PLLVelocityDegSec  float64
	LockinPackets      int
	LockinMagnitude    float32
	LockinCoherenceDB  float64
	LockinXMag         float32
	LockinXPhaseDeg    float32
	LockinYMag         float32
	LockinYPhaseDeg    float32
	LockinZMag         float32
	LockinZPhaseDeg    float32
	LockinDeltaXY      float32
	LockinDeltaYZ      float32
	LockinDeltaXZ      float32
	LockinHardLock     bool
	LockinState        int     // 0=None, 1=Idle, 2=Data
	PhysioBPM          float64
	PhysioHRV          float64
	PhysioPulseWave    []float32
	LockinPhaseStdDeg  float64 // circular StdDev of last 100 Z-phase results
	LockinApEn         float64 // simplified ApEn proxy (0=locked, 1=chaotic)

	// MIMO: Freq2 lock-in results (zero if not active)
	Lockin2Freq     float64
	Lockin2ZMag     float64
	Lockin2ZPhase   float64
	Lockin2XMag     float64
	Lockin2XPhase   float64
	Lockin2YMag     float64
	Lockin2YPhase   float64
	BeatPhaseStdDeg float64
}

// DiagReport is a full point-in-time snapshot for periodic logging.
type DiagReport struct {
	At           time.Time
	IntervalSec  float64
	SensorCount  int
	TotalPackets uint64
	Sync         SyncMetrics
	Sensors      []DiagSensorSnap
}

// ComputeDiagReport builds a DiagReport. Caller must hold b.Mu.
func (b *Buffers) ComputeDiagReport(windowMs, intervalSec float64) DiagReport {
	r := DiagReport{
		At:           time.Now(),
		IntervalSec:  intervalSec,
		SensorCount:  len(b.Sensors),
		TotalPackets: b.Packets,
		Sync:         b.ComputeSyncMetrics(windowMs),
	}
	nowNs := r.At.UnixNano()
	for _, id := range b.sortedIDs() {
		s := b.Sensors[id]
		if s == nil {
			continue
		}
		snap := DiagSensorSnap{
			ID:           id,
			PacketsTotal: s.Packets,
			PacketAgeMs:  (nowNs - s.LastPacketUnixNano) / 1_000_000,
		}
		if s.LastDsp != nil {
			d := s.LastDsp
			snap.SNR = d.SNR
			snap.Magnitude = d.Magnitude
			snap.PhaseDeg = d.PhaseDeg
			snap.DemodFreqHz = d.DemodFreqHz
			snap.NPeaks = d.NPeaks
			snap.Peaks = d.Peaks
			snap.TransportLatencyMs = d.TransportLatencyMs
			snap.Flags = d.Flags
			snap.IsCompact = true
			snap.TsfUs = d.TsfUs
			snap.Resonance = d.Resonance
			snap.RawZCount = d.RawZCount
			snap.RawZTail = d.RawZTail
		}
		snap.PhaseHistoryLen = len(s.PhaseHistory)
		snap.RawVelocityDegSec = s.lastRawVelocityDegSec
		snap.TrackReason = s.lastTrackReason
		snap.ResetReason = s.lastResetReason
		snap.PLLWarmupN = s.pllWarmupN
		snap.PLLActive = s.pllActive
		snap.PLLVelocityDegSec = s.pllVelocityDegSec
		snap.LockinPackets = s.lastLockinPackets
		snap.LockinMagnitude = s.lastLockinMag
		snap.LockinCoherenceDB = s.lastLockinCoherenceDB
		snap.LockinXMag = float32(s.LockinXMag)
		snap.LockinXPhaseDeg = float32(s.LockinXPhaseDeg)
		snap.LockinYMag = float32(s.LockinYMag)
		snap.LockinYPhaseDeg = float32(s.LockinYPhaseDeg)
		snap.LockinZMag = float32(s.LockinMag)
		snap.LockinZPhaseDeg = float32(s.LockinPhase * 180.0 / math.Pi)
		snap.LockinDeltaXY = float32(s.LockinDeltaXY)
		snap.LockinDeltaYZ = float32(s.LockinDeltaYZ)
		snap.LockinDeltaXZ = float32(s.LockinDeltaXZ)
		snap.LockinHardLock = s.LockinHardLock
		snap.LockinState = int(s.LockinState)
		snap.PhysioBPM = s.PhysioBPM
		snap.PhysioHRV = s.PhysioHRV
		if len(s.PhysioPulseWave) > 0 {
			snap.PhysioPulseWave = make([]float32, len(s.PhysioPulseWave))
			copy(snap.PhysioPulseWave, s.PhysioPulseWave)
		}

		// Phase StdDev and ApEn from rolling window
		winN := s.lockinPhaseWinN
		if winN > 100 {
			winN = 100
		}
		if winN > 1 {
			snap.LockinPhaseStdDeg = circularPhaseStdDev(s.lockinPhaseWinZ[:winN])
			snap.LockinApEn = math.Min(1.0, snap.LockinPhaseStdDeg/90.0)
		}

		// MIMO Freq2 results
		if s.Lockin2Freq > 0 {
			snap.Lockin2Freq = s.Lockin2Freq
			snap.Lockin2ZMag = s.Lockin2Mag
			snap.Lockin2ZPhase = s.Lockin2PhaseDeg
			snap.Lockin2XMag = s.Lockin2XMag
			snap.Lockin2XPhase = s.Lockin2XPhaseDeg
			snap.Lockin2YMag = s.Lockin2YMag
			snap.Lockin2YPhase = s.Lockin2YPhaseDeg
			snap.BeatPhaseStdDeg = s.LockinBeatPhaseStdDeg
		}

		if len(s.PhaseHistory) > 1 {
			snap.PhaseStabilityDeg = phaseStdDev(tailFloat32(s.PhaseHistory, maxSyncEvalSamples))
		}
		if len(s.MagHistory) > 1 {
			snap.MagStabilityG = stdDevFloat32(tailFloat32(s.MagHistory, maxSyncEvalSamples))
		}
		r.Sensors = append(r.Sensors, snap)
	}
	return r
}

func tailFloat32(src []float32, n int) []float32 {
	if len(src) <= n {
		return src
	}
	return src[len(src)-n:]
}

func tailUint64(src []uint64, n int) []uint64 {
	if len(src) <= n {
		return src
	}
	return src[len(src)-n:]
}

func stdDevFloat32(h []float32) float64 {
	n := float64(len(h))
	var sum float64
	for _, v := range h {
		sum += float64(v)
	}
	mean := sum / n
	var vari float64
	for _, v := range h {
		d := float64(v) - mean
		vari += d * d
	}
	return math.Sqrt(vari / n)
}
