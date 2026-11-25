package logging

import (
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"strings"
	"time"

	"server/internal/domain/signal"
)

func wrapDeg180(v float64) float64 {
	for v <= -180 {
		v += 360
	}
	for v > 180 {
		v -= 360
	}
	return v
}

func resonanceVerdict(r signal.ResonanceMetrics) string {
	car := float64(r.CarrierMag)
	sub := float64(r.SubMag)
	h2 := float64(r.H2Mag)
	h3 := float64(r.H3Mag)
	dom := float64(r.DominantMag)
	if car <= 0 && dom <= 0 {
		return "NO_SIGNAL"
	}
	maxH := sub
	if h2 > maxH {
		maxH = h2
	}
	if h3 > maxH {
		maxH = h3
	}
	df := r.FreqOffsetHz
	if df < 0 {
		df = -df
	}
	switch {
	case maxH > car*1.5:
		return "HARMONIC_DOMINANT"
	case dom > car*1.5 && df > 20:
		return "RESONANCE_DOMINANT"
	case car >= dom*0.8 && maxH <= car*0.8:
		return "CARRIER_DOMINANT"
	default:
		return "MIXED"
	}
}

func syncLockTier(rep signal.DiagReport, tsfKnown bool) string {
	s := rep.Sync
	if !tsfKnown {
		return "NO_TSF"
	}
	if rep.SensorCount < 1 {
		return "NO_SENSOR"
	}
	if !s.CarrierDominant {
		return "NOT_CARRIER"
	}
	if len(rep.Sensors) == 0 {
		return "NO_SNAPSHOT"
	}
	sn := rep.Sensors[0]
	singleSensor := rep.SensorCount <= 1
	if sn.PacketAgeMs > 2000 {
		return "STALE"
	}
	if s.HarmonicDistanceDB < 15 && !singleSensor {
		return "HARMONIC_LEAK"
	}

	// Use ApEn for high-level state detection
	if sn.LockinHardLock {
		if sn.LockinApEn < 0.2 {
			return "IDLE"
		}
		if sn.LockinApEn > 0.8 && s.HarmonicDistanceDB > 20 {
			return "DATA"
		}
	}

	if s.PhaseVelocityDegSec < -15 || s.PhaseVelocityDegSec > 15 {
		return "PHASE_VELOCITY"
	}
	if !hasUsableCarrierEnergy(sn) {
		return "LOW_SNR"
	}
	if s.PhaseStabilityDeg <= 0 {
		if singleSensor {
			return "SOFT_LOCK"
		}
		return "NO_PHASE"
	}
	if s.PhaseStabilityDeg < 5 {
		return "HARD_LOCK"
	}
	if s.PhaseStabilityDeg < 15 {
		return "SOFT_LOCK"
	}
	return "PHASE_STDDEV"
}

func hasUsableCarrierEnergy(sn signal.DiagSensorSnap) bool {
	// Lock-in hard lock means coherent averaging already recovered the carrier —
	// waterfall SNR being low is expected and not a disqualifier.
	if sn.LockinHardLock {
		return true
	}
	carSNR := sn.SNR
	if sn.Resonance.CarrierSNR != 0 {
		carSNR = sn.Resonance.CarrierSNR
	}
	if sn.Resonance.WaterfallSNRDB >= 7.0 {
		return true
	}
	if carSNR > 6 {
		return true
	}
	if carSNR >= 0 && sn.Resonance.WaterfallSNRDB >= 10 {
		return true
	}
	if sn.Resonance.WaterfallSNRDB >= 8 && sn.Resonance.CarrierToACDB >= -10 {
		return true
	}
	if sn.Resonance.WaterfallSNRDB >= 12 && sn.Resonance.CarrierToACDB >= -6 {
		return true
	}
	return false
}

func syncGateStatus(rep signal.DiagReport, tsfKnown bool) (bool, string) {
	tier := syncLockTier(rep, tsfKnown)
	switch tier {
	case "HARD_LOCK", "SOFT_LOCK", "IDLE", "DATA":
		return true, tier
	default:
		return false, tier
	}
}

// DiagLogger periodically writes diagnostic snapshots.
type DiagLogger struct {
	bufs          *signal.Buffers
	interval      time.Duration
	windowMs      float64
	lg            *log.Logger
	file          *os.File
	TsfOffsetFn   func() int64 // deviceTsfUs - serverUnixUs; nil → не задан
	prevTsfOffset int64
	prevTsfTime   time.Time
}

// NewDiagLogger creates a DiagLogger that writes to path (or stderr if path is empty).
func NewDiagLogger(bufs *signal.Buffers, interval time.Duration, windowMs float64, path string) *DiagLogger {
	var out io.Writer = os.Stderr
	var fh *os.File
	if path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.Printf("[diag] cannot open %s: %v, using stderr", path, err)
		} else {
			out = f
			fh = f
		}
	}
	return &DiagLogger{
		bufs:     bufs,
		interval: interval,
		windowMs: windowMs,
		lg:       log.New(out, "", log.LstdFlags|log.Lmicroseconds),
		file:     fh,
	}
}

// Run starts the periodic log loop. Blocks; run in a goroutine.
func (d *DiagLogger) Run() {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for range ticker.C {
		d.emit()
	}
}

func (d *DiagLogger) emit() {
	d.bufs.Mu.Lock()
	rep := d.bufs.ComputeDiagReport(d.windowMs, d.interval.Seconds())
	d.bufs.Mu.Unlock()

	// TSF-оффсет: показываем статус и оценочный текущий TSF на сервере
	tsfStatus := "NO_TSF"
	if d.TsfOffsetFn != nil {
		offset := d.TsfOffsetFn()
		if offset != 0 {
			estTsfUs := time.Now().UnixMicro() + offset
			tsfStatus = fmt.Sprintf("offset=%+.3fs estTSF=%.3fs",
				float64(offset)/1e6, float64(estTsfUs)/1e6)
		}
	}

	d.lg.Printf("══ DIAG every=%.0fs sensors=%d packets=%d tsf=%s ══",
		rep.IntervalSec, rep.SensorCount, rep.TotalPackets, tsfStatus)

	// Sync line (only meaningful with 2+ sensors)
	s := rep.Sync
	if rep.SensorCount >= 2 {
		calib := "NO"
		if s.PhaseCalibrated {
			calib = fmt.Sprintf("YES offset=%.1f°", s.PhaseCalibOffsetDeg)
		}
		d.lg.Printf("[SYNC   ] Drift: %6.1f µs | ΔΦ: %+6.1f° | Calib: %s",
			s.SyncDriftUs, s.DeltaPhiDeg, calib)
	}
	d.lg.Printf("[LATENCY] Transport: %5.0f ms | PhaseStdDev: %.2f° (sensor[0])",
		s.TransportLatencyMs, s.PhaseStabilityDeg)
	pllStr := "off"
	if s.PLLActive {
		pllStr = fmt.Sprintf("on(%.1f°/s)", s.PLLVelocityDegSec)
	}
	d.lg.Printf("[LOCK   ] Vel:%6.2f°/s | dF:%+7.3fHz | Hdist:%5.1fdB | Jitter:%7.1fµs | Coh:%5.2fs | Carrier:%v | PLL:%s",
		s.PhaseVelocityDegSec, s.FreqErrorHz, s.HarmonicDistanceDB, s.JitterUs, s.CoherenceTimeSec, s.CarrierDominant, pllStr)

	for _, sn := range rep.Sensors {
		mode := "V1"
		if sn.IsCompact {
			mode = "COMPACT"
		}
		qual := ""
		if sn.SNR < 6 {
			qual = " LOW_SNR"
		}
		if sn.Flags&0x02 != 0 {
			qual += " SAT"
		}
		if sn.PacketAgeMs > 2000 {
			qual += " STALE"
		}
		// TsfUs → секунды для читаемости (WiFi TSF с момента ассоциации)
		tsfSec := float64(sn.TsfUs) / 1e6
		d.lg.Printf("[SENSOR%02d] MODE:%-7s SNR:%5.1fdB Mag:%7.4fg Phase:%6.1f° Lat:%5.0fms TSF=%.3fs Peaks:%d Age:%dms%s",
			sn.ID, mode, sn.SNR, sn.Magnitude, sn.PhaseDeg,
			sn.TransportLatencyMs, tsfSec, sn.NPeaks, sn.PacketAgeMs, qual)
		d.lg.Printf("          SENSOR sync tsf=%.3fs age=%dms lat=%.1fms flags=0x%02X",
			tsfSec, sn.PacketAgeMs, sn.TransportLatencyMs, sn.Flags)

		for i := 0; i < int(sn.NPeaks) && i < 8; i++ {
			p := sn.Peaks[i]
			if p.FreqHz < 1 {
				continue
			}
			d.lg.Printf("          Peak[%d] %7.1fHz %6.3fg %6.1f° SNR:%5.1fdB",
				i, p.FreqHz, p.Magnitude, p.PhaseDeg, p.SNR)
		}
		if sn.Resonance.CarrierFreqHz > 0 {
			axis := "?"
			switch sn.Resonance.AxisCode {
			case 1:
				axis = "X"
			case 2:
				axis = "Y"
			case 3:
				axis = "Z"
			}
			d.lg.Printf("          Resonance axis=%s carrier=%.1fHz %.3fg %.1f° %.1fdB | dom=%.1fHz %.3fg %.1f° %.1fdB | dPhi=%+.1f° dF=%+.1fHz span=%.1fHz",
				axis,
				sn.Resonance.CarrierFreqHz, sn.Resonance.CarrierMag, sn.Resonance.CarrierPhaseDeg, sn.Resonance.CarrierSNR,
				sn.Resonance.DominantFreqHz, sn.Resonance.DominantMag, sn.Resonance.DominantPhaseDeg, sn.Resonance.DominantSNR,
				sn.Resonance.PhaseDeltaDeg, sn.Resonance.FreqOffsetHz, sn.Resonance.PeakSpreadHz)
			d.lg.Printf("          FREQ target=%.1fHz measured=%.1fHz",
				sn.DemodFreqHz, sn.Resonance.DominantFreqHz)
			d.lg.Printf("          ENERGY dc=%.4fg ac_rms=%.4fg iq/ac=%+.1fdB fft=%.4fg floor=%.4fg wf_snr=%+.1fdB",
				sn.Resonance.AxisMeanG,
				sn.Resonance.AxisACRMSG,
				sn.Resonance.CarrierToACDB,
				sn.Resonance.CarrierFFTMag,
				sn.Resonance.NoiseFloorMag,
				sn.Resonance.WaterfallSNRDB)
			if sn.Resonance.SubFreqHz > 0 || sn.Resonance.H2FreqHz > 0 || sn.Resonance.H3FreqHz > 0 {
				d.lg.Printf("          Harmonics sub=%.1fHz %.3fg %.1fdB | h2=%.1fHz %.3fg %.1fdB | h3=%.1fHz %.3fg %.1fdB",
					sn.Resonance.SubFreqHz, sn.Resonance.SubMag, sn.Resonance.SubSNR,
					sn.Resonance.H2FreqHz, sn.Resonance.H2Mag, sn.Resonance.H2SNR,
					sn.Resonance.H3FreqHz, sn.Resonance.H3Mag, sn.Resonance.H3SNR)
			}
			d.lg.Printf("          Verdict %s", resonanceVerdict(sn.Resonance))
		}
		if sn.RawZCount > 0 {
			vals := make([]string, 0, sn.RawZCount)
			for i := 0; i < int(sn.RawZCount); i++ {
				vals = append(vals, fmt.Sprintf("%.5f", sn.RawZTail[i]))
			}
			d.lg.Printf("          WAVE_DUMP Z[%d]=[%s]", sn.RawZCount, strings.Join(vals, ", "))
		}

		if sn.PhaseStabilityDeg > 0 || sn.MagStabilityG > 0 {
			stability := "HARD_LOCK"
			if sn.PhaseStabilityDeg >= 15 {
				stability = "UNSTABLE"
			} else if sn.PhaseStabilityDeg >= 5 {
				stability = "SOFT_LOCK"
			}
			d.lg.Printf("          Stability phase=%.2f° mag=%.5fg [%s]",
				sn.PhaseStabilityDeg, sn.MagStabilityG, stability)
		}
		d.lg.Printf("          LOCKIN pkts=%d coh_mag=%.5fg coh=%+.1fdB hard_lock=%v Φ_std=%.2f° ApEn=%.2f",
			sn.LockinPackets, sn.LockinMagnitude, sn.LockinCoherenceDB, sn.LockinHardLock,
			sn.LockinPhaseStdDeg, sn.LockinApEn)
		d.lg.Printf("          LOCKIN_3AXIS X:mag=%.6fg φ=%.1f° | Y:mag=%.6fg φ=%.1f° | Z:mag=%.6fg φ=%.1f° | ΔXY=%+.1f° ΔYZ=%+.1f° ΔXZ=%+.1f°",
			sn.LockinXMag, sn.LockinXPhaseDeg,
			sn.LockinYMag, sn.LockinYPhaseDeg,
			sn.LockinZMag, sn.LockinZPhaseDeg,
			sn.LockinDeltaXY, sn.LockinDeltaYZ, sn.LockinDeltaXZ)
		if sn.Lockin2Freq > 0 {
			xTalkDB := 0.0
			mainMag := math.Max(1e-12, float64(sn.LockinZMag))
			crossMag := math.Max(1e-12, sn.Lockin2ZMag)
			if mainMag >= crossMag {
				xTalkDB = 20 * math.Log10(mainMag/crossMag)
			} else {
				xTalkDB = -20 * math.Log10(crossMag/mainMag)
			}
			beatPhase := wrapDeg180(float64(sn.LockinZPhaseDeg) - sn.Lockin2ZPhase)
			beatHz := math.Abs(float64(sn.DemodFreqHz) - sn.Lockin2Freq)
			beatCycles := beatHz * (d.windowMs / 1000.0)
			beatCycleErr := math.Abs(beatCycles - math.Round(beatCycles))
			correctness := "OK"
			if sn.Flags&0x02 != 0 {
				correctness = "SAT"
			} else if beatHz > 0.01 && beatCycleErr > 0.15 {
				correctness = "LEAKAGE"
			} else if xTalkDB < 12.0 {
				correctness = "INTERMOD"
			} else if sn.LockinPhaseStdDeg > 5 {
				correctness = "PHASE_STD"
			} else if sn.LockinApEn > 0.2 {
				correctness = "ENTROPY"
			} else if sn.SNR < 6 {
				correctness = "LOW_SNR"
			}
			d.lg.Printf("[MIMO-SDR] S%d | F1:%.1fHz Φ=%.2f°±%.2f° Mag=%.6fg | F2:%.1fHz Φ=%.2f° Mag=%.6fg | dΦ_beat=%+.2f°±%.2f° | Xtalk=%+.1fdB | Beat=%.2fHz cyc=%.2f err=%.3f | ApEn=%.2f | Check=%s",
				sn.ID,
				sn.DemodFreqHz, sn.LockinZPhaseDeg, sn.LockinPhaseStdDeg, sn.LockinZMag,
				sn.Lockin2Freq, sn.Lockin2ZPhase, sn.Lockin2ZMag,
				beatPhase, sn.BeatPhaseStdDeg, xTalkDB, beatHz, beatCycles, beatCycleErr, sn.LockinApEn, correctness)
			d.lg.Printf("          LOCKIN_F2 freq=%.1fHz | X:mag=%.6fg φ=%.1f° | Y:mag=%.6fg φ=%.1f° | Z:mag=%.6fg φ=%.1f°",
				sn.Lockin2Freq,
				sn.Lockin2XMag, sn.Lockin2XPhase,
				sn.Lockin2YMag, sn.Lockin2YPhase,
				sn.Lockin2ZMag, sn.Lockin2ZPhase)
		}
		d.lg.Printf("          TRACK hist=%d raw_vel=%+.2f°/s pll_vel=%+.2f°/s warmup=%d active=%v reason=%s reset=%s",
			sn.PhaseHistoryLen, sn.RawVelocityDegSec, sn.PLLVelocityDegSec, sn.PLLWarmupN, sn.PLLActive, sn.TrackReason, sn.ResetReason)
		if sn.PhysioBPM > 0 {
			d.lg.Printf("[BIO-MON] S%02d HeartRate: %.1f bpm | HRV: %.1fms | PulseWave size: %d",
				sn.ID, sn.PhysioBPM, sn.PhysioHRV, len(sn.PhysioPulseWave))
		}
	}

	// One-line quality summary + ITR estimate
	tsfKnown := d.TsfOffsetFn != nil && d.TsfOffsetFn() != 0
	tsfDriftStr := "—"
	dTsfDtStr := "—"
	if d.TsfOffsetFn != nil {
		offset := d.TsfOffsetFn()
		if offset != 0 {
			tsfDriftStr = fmt.Sprintf("%+.0fµs", float64(offset%1_000_000)) // sub-second component
			now := time.Now()
			if d.prevTsfOffset != 0 && !d.prevTsfTime.IsZero() {
				elapsed := now.Sub(d.prevTsfTime).Seconds()
				if elapsed > 0.1 {
					rate := float64(offset-d.prevTsfOffset) / elapsed // µs/s
					dTsfDtStr = fmt.Sprintf("%+.2fµs/s", rate)
				}
			}
			d.prevTsfOffset = offset
			d.prevTsfTime = now
		}
	}
	syncOK, reason := syncGateStatus(rep, tsfKnown)

	// ITR estimate: N=2 commands (locked/unlocked), P=accuracy inferred from PhaseStdDev
	itrBps := 0.0
	if rep.SensorCount > 0 && s.PhaseStabilityDeg > 0 && s.PhaseStabilityDeg < 180 {
		P := 1.0 - s.PhaseStabilityDeg/180.0 // rough accuracy estimate
		if P > 0.5 && P < 1.0 {
			const N = 2.0
			itrBps = (math.Log2(N) + P*math.Log2(P) + (1-P)*math.Log2((1-P)/(N-1))) / (d.interval.Seconds())
			if itrBps < 0 {
				itrBps = 0
			}
		}
	}

	apEnStr := "—"
	if len(rep.Sensors) > 0 && rep.Sensors[0].LockinPhaseStdDeg > 0 {
		apEnStr = fmt.Sprintf("%.2f", rep.Sensors[0].LockinApEn)
	}

	d.lg.Printf("[QUALITY] SyncOK:%v Mode:%s Sensors:%d ΔΦ:%+.1f° StdDev:%.2f° Vel:%+.2f°/s Hdist:%.1fdB TSF:%v PLL:%v TSFdrift:%s dTSF/dt:%s ITR:%.2fbps ApEn:%s\n",
		syncOK, reason, rep.SensorCount, s.DeltaPhiDeg, s.PhaseStabilityDeg, s.PhaseVelocityDegSec, s.HarmonicDistanceDB, tsfKnown, s.PLLActive, tsfDriftStr, dTsfDtStr, itrBps, apEnStr)
	d.lg.Printf("[CHECK ] Target SAT:false | Jitter<5us:%v(%.1f) | PhaseStd<0.1°:%v(%.2f) | ApEn<0.2:%v(%s) | TSFdrift:%s | dTSF/dt:%s\n",
		s.JitterUs < 5.0, s.JitterUs,
		s.PhaseStabilityDeg < 0.1 && s.PhaseStabilityDeg > 0, s.PhaseStabilityDeg,
		len(rep.Sensors) > 0 && rep.Sensors[0].LockinApEn < 0.2, apEnStr,
		tsfDriftStr, dTsfDtStr)
}
