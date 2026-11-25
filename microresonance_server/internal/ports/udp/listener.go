package udp

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"runtime"
	"server/internal/domain/ingest"
	"server/internal/domain/signal"
	"strings"
	"time"
)

type Listener struct {
	Addr        string
	Buffers     *signal.Buffers
	CfgFn       func() ingest.Config
	TsfOffsetFn func() int64
}

func NewListener(addr string, b *signal.Buffers, cfgFn func() ingest.Config) *Listener {
	return &Listener{
		Addr:    addr,
		Buffers: b,
		CfgFn:   cfgFn,
	}
}

func (l *Listener) Listen() error {
	pc, err := net.ListenPacket("udp4", l.Addr)
	if err != nil {
		return err
	}
	defer pc.Close()
	if uc, ok := pc.(*net.UDPConn); ok {
		_ = uc.SetReadBuffer(8 << 20) // 8MB buffer to handle bursts
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	fmt.Fprintf(os.Stderr, "UDP listening on %s\n", l.Addr)

	buf := make([]byte, 4096)
	var lastReadAt time.Time
	_ = lastReadAt
	type rawFlowState struct {
		arrival        time.Time
		tsfUs          uint64
		summaryAt      time.Time
		count          int
		bursts         int
		maxArrivalMs   float64
		maxTSFMs       float64
		maxAbsJitterMs float64
		lastPktBytes   int
	}
	rawFlowBySensor := make(map[uint8]rawFlowState)

	decoder := ingest.NewDecoder(l.CfgFn())

	// Jitter buffer for raw data to smooth out A-MPDU bursts
	type rawPacket struct {
		isDsp    bool
		sensorID uint8
		adxlX    []float64
		adxlY    []float64
		adxlZ    []float64
		ads      []float64
		emg      []float64
		lsmAccX  []float64
		lsmAccY  []float64
		lsmAccZ  []float64
		lsmGyrX  []float64
		lsmGyrY  []float64
		lsmGyrZ  []float64
		lsmTs    []uint32
		dspSnap  *signal.DspSnapshot
		fromStr  string
	}
	rawBufCh := make(chan rawPacket, 2000)

	go func() {
		lastProc := time.Now()
		for pkt := range rawBufCh {
			qLen := len(rawBufCh)

			// Adaptive smoothing delay:
			// - If buffer is low, limit processing to ~100Hz (10ms) to avoid UI flicker.
			// - If buffer starts piling up, increase speed to 500Hz (2ms).
			// - If buffer is critically full, drain at maximum speed (no delay).
			var delay time.Duration
			if qLen < 10 {
				delay = 10 * time.Millisecond
			} else if qLen < 50 {
				delay = 2 * time.Millisecond
			}

			if delay > 0 {
				since := time.Since(lastProc)
				if since < delay {
					time.Sleep(delay - since)
				}
			}
			lastProc = time.Now()

			nowNs := time.Now().UnixNano()
			l.Buffers.Mu.Lock()
			l.Buffers.Packets++
			l.Buffers.LastFrom = pkt.fromStr
			s := l.Buffers.GetOrCreateSensor(pkt.sensorID)
			
			if pkt.isDsp {
				s.AdxlX = append(s.AdxlX, pkt.adxlX...)
				s.AdxlY = append(s.AdxlY, pkt.adxlY...)
				s.AdxlZ = append(s.AdxlZ, pkt.adxlZ...)
				s.Emg = append(s.Emg, pkt.emg...)
				s.LsmAccelX = append(s.LsmAccelX, pkt.lsmAccX...)
				s.LsmAccelY = append(s.LsmAccelY, pkt.lsmAccY...)
				s.LsmAccelZ = append(s.LsmAccelZ, pkt.lsmAccZ...)
				s.LsmGyroX = append(s.LsmGyroX, pkt.lsmGyrX...)
				s.LsmGyroY = append(s.LsmGyroY, pkt.lsmGyrY...)
				s.LsmGyroZ = append(s.LsmGyroZ, pkt.lsmGyrZ...)
				s.LsmTs = append(s.LsmTs, pkt.lsmTs...)

				cfg := l.CfgFn()
				adxlRate := float64(cfg.AdxlRate)
				if adxlRate < 1 {
					adxlRate = 4000
				}
				maxAdxl := int(cfg.TimeWindow.Seconds() * adxlRate)
				if maxAdxl < 100 {
					maxAdxl = 4000
				}
				if len(s.AdxlX) > maxAdxl {
					s.AdxlX = s.AdxlX[len(s.AdxlX)-maxAdxl:]
					s.AdxlY = s.AdxlY[len(s.AdxlY)-maxAdxl:]
					s.AdxlZ = s.AdxlZ[len(s.AdxlZ)-maxAdxl:]
				}
				if len(s.Emg) > maxAdxl {
					s.Emg = s.Emg[len(s.Emg)-maxAdxl:]
				}
				lsmRate := 1660.0
				maxLsm := int(cfg.TimeWindow.Seconds() * lsmRate)
				if maxLsm < 50 {
					maxLsm = 1660
				}
				if len(s.LsmAccelX) > maxLsm {
					s.LsmAccelX = s.LsmAccelX[len(s.LsmAccelX)-maxLsm:]
					s.LsmAccelY = s.LsmAccelY[len(s.LsmAccelY)-maxLsm:]
					s.LsmAccelZ = s.LsmAccelZ[len(s.LsmAccelZ)-maxLsm:]
					s.LsmGyroX = s.LsmGyroX[len(s.LsmGyroX)-maxLsm:]
					s.LsmGyroY = s.LsmGyroY[len(s.LsmGyroY)-maxLsm:]
					s.LsmGyroZ = s.LsmGyroZ[len(s.LsmGyroZ)-maxLsm:]
					s.LsmTs = s.LsmTs[len(s.LsmTs)-maxLsm:]
				}

				l.Buffers.StoreDsp(pkt.sensorID, pkt.dspSnap)
			} else {
				decoder.TrimAndAppend(s, pkt.adxlX, pkt.adxlY, pkt.adxlZ, pkt.ads, nowNs)
			}
			l.Buffers.Mu.Unlock()
		}
	}()

	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		now := time.Now()
		lastReadAt = now

		// Log packet handling
		if n >= 3 && buf[0] == 0x4C && buf[1] == 0x4D {
			L := int(buf[2])
			if 3+L <= n && L > 0 {
				s := string(buf[3 : 3+L])
				for _, line := range strings.Split(s, "\n") {
					line = strings.TrimSpace(line)
					if line != "" {
						log.Printf("[FW-UDP:%s] %s", from.String(), line)
					}
				}
				l.Buffers.Mu.Lock()
				l.Buffers.LogCount++
				l.Buffers.Logs = append(l.Buffers.Logs, s)
				l.Buffers.LastFrom = from.String()
				if len(l.Buffers.Logs) > 64 {
					l.Buffers.Logs = l.Buffers.Logs[len(l.Buffers.Logs)-64:]
				}
				l.Buffers.Mu.Unlock()
			}
			continue
		}

		if n < 2 {
			continue
		}
		magic := binary.LittleEndian.Uint16(buf[0:2])

		// --- v2: Edge-DSP результат (DspPacket) ---
		if magic == ingest.MagicDsp || magic == ingest.MagicDspRaw {
			readLen := ingest.DspHeaderLen
			mode := "COMPACT"
			if magic == ingest.MagicDspRaw {
				readLen = ingest.DspRawPacketLen
				mode = "RAW_SYNC"
			}
			if n < readLen {
				log.Printf("[UDP] Short packet for magic 0x%04X: got %d, expected %d", magic, n, readLen)
				continue
			}

			res, err := ingest.DecodeDspPacket(buf[:readLen])
			if err != nil {
				continue
			}
			cfg := l.CfgFn()

			// Если это RAW пакет, выполняем демодуляцию на стороне сервера
			var resonance signal.ResonanceMetrics
			if magic == ingest.MagicDspRaw {
				res.DemodFreqHz = float32(cfg.DemodFreqHz)
				p0, peaks, nPeaks, metrics := signal.AnalyzeResonantResponse(res.RawX, res.RawY, res.RawZ, res.TsfUs, cfg.DemodFreqHz, float64(cfg.AdxlRate))
				res.Peaks[0] = ingest.DspPeak(p0)
				for i := 0; i < int(nPeaks) && i < len(peaks); i++ {
					res.Peaks[i] = ingest.DspPeak(peaks[i])
				}
				res.NPeaks = nPeaks
				resonance = metrics

				prev := rawFlowBySensor[res.SensorID]
				if prev.summaryAt.IsZero() {
					prev.summaryAt = now
				}
				arrivalDeltaMs := 0.0
				tsfDeltaMs := 0.0
				jitterMs := 0.0
				if !prev.arrival.IsZero() {
					arrivalDeltaMs = float64(now.Sub(prev.arrival).Microseconds()) / 1000.0
				}
				if prev.tsfUs != 0 && res.TsfUs >= prev.tsfUs {
					tsfDeltaMs = float64(res.TsfUs-prev.tsfUs) / 1000.0
				}
				if arrivalDeltaMs > 0 && tsfDeltaMs > 0 {
					jitterMs = arrivalDeltaMs - tsfDeltaMs
				}
				// Reference phase at window start (same formula as DemodulateIQ)
				phiDeg := math.Mod(cfg.DemodFreqHz*float64(res.TsfUs)*1e-6, 1.0) * 360.0

				// Expected TSF delta: windowSamples / adxlRate seconds
				localAdxlRate := float64(cfg.AdxlRate)
				if localAdxlRate < 1 {
					localAdxlRate = 4000
				}
				expectedTsfDeltaMs := float64(res.WindowSamples) / localAdxlRate * 1000.0

				// Detect TSF anomalies
				tsfNonMono := prev.tsfUs != 0 && res.TsfUs < prev.tsfUs
				tsfGapLarge := tsfDeltaMs > 0 && expectedTsfDeltaMs > 0 && tsfDeltaMs > expectedTsfDeltaMs*1.8
				tsfGapSmall := tsfDeltaMs > 0 && expectedTsfDeltaMs > 0 && tsfDeltaMs < expectedTsfDeltaMs*0.5
				if tsfNonMono || tsfGapLarge || tsfGapSmall {
					reason := "large-gap"
					if tsfNonMono {
						reason = "non-monotonic"
					} else if tsfGapSmall {
						reason = "small-gap"
					}
					log.Printf("[TSF-ANOMALY] sensor=%d tsf_dt=%.1fms expected=%.1fms reason=%s tsf=%d prev_tsf=%d",
						res.SensorID, tsfDeltaMs, expectedTsfDeltaMs, reason, res.TsfUs, prev.tsfUs)
				}

				log.Printf("[RAW-PKT] sensor=%d tsf=%dµs phi=%.1f° n=%d arrival_dt=%.1fms tsf_dt=%.1fms jitter=%.1fms pkt=%d",
					res.SensorID, res.TsfUs, phiDeg, res.WindowSamples, arrivalDeltaMs, tsfDeltaMs, jitterMs, n)

				prev.count++
				prev.lastPktBytes = n
				if arrivalDeltaMs > prev.maxArrivalMs {
					prev.maxArrivalMs = arrivalDeltaMs
				}
				if tsfDeltaMs > prev.maxTSFMs {
					prev.maxTSFMs = tsfDeltaMs
				}
				absJitterMs := mathAbs(jitterMs)
				if absJitterMs > prev.maxAbsJitterMs {
					prev.maxAbsJitterMs = absJitterMs
				}
				if arrivalDeltaMs >= 80 || tsfDeltaMs >= 45 {
					prev.bursts++
				}
				if now.Sub(prev.summaryAt) >= 10*time.Second {
					log.Printf("[RAW-FLOW-SUM] sensor=%d count=%d max_arrival=%.1fms max_tsf=%.1fms max_abs_jitter=%.1fms bursts=%d pkt=%d",
						res.SensorID, prev.count, prev.maxArrivalMs, prev.maxTSFMs, prev.maxAbsJitterMs, prev.bursts, prev.lastPktBytes)
					prev.summaryAt = now
					prev.count = 0
					prev.bursts = 0
					prev.maxArrivalMs = 0
					prev.maxTSFMs = 0
					prev.maxAbsJitterMs = 0
				}
				prev.arrival = now
				prev.tsfUs = res.TsfUs
				rawFlowBySensor[res.SensorID] = prev
			}

			adxlRate := float64(cfg.AdxlRate)
			if adxlRate < 1 {
				adxlRate = 4000
			}

			var tsfOffsetUs int64
			if l.TsfOffsetFn != nil {
				tsfOffsetUs = l.TsfOffsetFn()
			}
			latMs := 0.0
			if tsfOffsetUs != 0 {
				tsfUnixMs := float64(int64(res.TsfUs)-tsfOffsetUs) / 1000.0
				windowMs := 0.0
				if adxlRate > 0 {
					windowMs = float64(res.WindowSamples) * 1000.0 / adxlRate
				}
				latMs = float64(res.ServerArrival)/1e6 - tsfUnixMs - windowMs
				if latMs < -1000 || latMs > 60_000 {
					latMs = 0
				}
			}

			snap := &signal.DspSnapshot{
				TsfUs:              res.TsfUs,
				DemodFreqHz:        res.DemodFreqHz,
				PhaseDeg:           res.Peaks[0].PhaseDeg,
				Magnitude:          res.Peaks[0].Magnitude,
				SNR:                res.Peaks[0].SNR,
				NPeaks:             res.NPeaks,
				Spectrum:           res.Spectrum,
				Flags:              res.Flags,
				ServerArrival:      res.ServerArrival,
				TransportLatencyMs: latMs,
				Resonance:          resonance,
				AdxlRate:           adxlRate,
			}
			copyRawTail := func(src []float64, dst *[32]float32, cnt *uint8) {
				start := len(src) - 32
				if start < 0 {
					start = 0
				}
				*cnt = uint8(len(src) - start)
				for i := start; i < len(src) && i-start < 32; i++ {
					dst[i-start] = float32(src[i])
				}
			}
			copyRawTail(res.RawX, &snap.RawXTail, &snap.RawXCount)
			copyRawTail(res.RawY, &snap.RawYTail, &snap.RawYCount)
			copyRawTail(res.RawZ, &snap.RawZTail, &snap.RawZCount)
			for i := 0; i < 8; i++ {
				snap.Peaks[i] = signal.DspPeak{
					FreqHz:    res.Peaks[i].FreqHz,
					Magnitude: res.Peaks[i].Magnitude,
					PhaseDeg:  res.Peaks[i].PhaseDeg,
					SNR:       res.Peaks[i].SNR,
				}
			}

			select {
			case rawBufCh <- rawPacket{
				isDsp:    true,
				sensorID: res.SensorID,
				adxlX:    res.RawX,
				adxlY:    res.RawY,
				adxlZ:    res.RawZ,
				emg:      res.Emg,
				lsmAccX:  res.LsmAccelX,
				lsmAccY:  res.LsmAccelY,
				lsmAccZ:  res.LsmAccelZ,
				lsmGyrX:  res.LsmGyroX,
				lsmGyrY:  res.LsmGyroY,
				lsmGyrZ:  res.LsmGyroZ,
				lsmTs:    res.LsmTs,
				dspSnap:  snap,
				fromStr:  from.String(),
			}:
			default:
				log.Printf("[DSP-UDP] Jitter buffer full, dropping packet from %s", from.String())
			}

			_ = mode // suppress unused warning after removing debug fprintf
			continue
		}

		if n < ingest.HeaderLen || binary.BigEndian.Uint16(buf[0:2]) != ingest.Magic {
			continue
		}

		cfg := l.CfgFn()
		decoder.Config = cfg

		sensorID, adxlX, adxlY, adxlZ, ads, err := decoder.DecodeDataPacket(buf[:n])
		if err != nil || adxlX == nil {
			continue
		}

		select {
		case rawBufCh <- rawPacket{
			sensorID: sensorID,
			adxlX:    adxlX,
			adxlY:    adxlY,
			adxlZ:    adxlZ,
			ads:      ads,
			fromStr:  from.String(),
		}:
		default:
			// Buffer full, drop packet
			log.Printf("[RAW-UDP] Jitter buffer full, dropping packet from %s", from.String())
		}
	}
}

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
