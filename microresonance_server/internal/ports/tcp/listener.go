package tcp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"server/internal/domain/ingest"
	"server/internal/domain/signal"
	"strings"
	"time"
)

const readDeadline = 800 * time.Millisecond

type Listener struct {
	Addr        string
	Buffers     *signal.Buffers
	CfgFn       func() ingest.Config
	TsfOffsetFn func() int64 // deviceTsfUs - serverUnixUs (отрицателен); nil → не используется
}

func NewListener(addr string, b *signal.Buffers, cfgFn func() ingest.Config) *Listener {
	return &Listener{Addr: addr, Buffers: b, CfgFn: cfgFn}
}

func (l *Listener) Listen() error {
	ln, err := net.Listen("tcp", l.Addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	fmt.Fprintf(os.Stderr, "TCP listening on %s\n", l.Addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		log.Printf("[ingest/tcp] accepted %s", conn.RemoteAddr())
		go l.handleConn(conn)
	}
}

func (l *Listener) handleConn(conn net.Conn) {
	from := conn.RemoteAddr().String()
	defer func() {
		log.Printf("[ingest/tcp] closed %s", from)
		conn.Close()
	}()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	hdr := make([]byte, ingest.HeaderLen)
	logBuf := make([]byte, 256)
	dataBuf := make([]byte, ingest.PacketLen)
	dspBuf := make([]byte, ingest.DspRawPacketLen)
	decoder := ingest.NewDecoder(l.CfgFn())
	streak := 0
	type rawFlowState struct {
		arrival        time.Time
		tsfUs          uint64
		summaryAt      time.Time
		count          int
		bursts         int
		maxArrivalMs   float64
		maxTSFMs       float64
		maxAbsJitterMs float64
	}
	rawFlowBySensor := make(map[uint8]rawFlowState)

	for {
		if err := readExact(conn, hdr[:2], &streak); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				log.Printf("[ingest/tcp] %s read error: %v", from, err)
			}
			return
		}
		streak = 0

		magic := binary.LittleEndian.Uint16(hdr[:2])

		switch magic {

		// --- v1: лог-пакет от прошивки ---
		case ingest.MagicLog:
			if err := readExact(conn, hdr[2:3], &streak); err != nil {
				return
			}
			L := int(hdr[2])
			if L <= 0 || L > len(logBuf) {
				return
			}
			if err := readExact(conn, logBuf[:L], &streak); err != nil {
				return
			}
			text := strings.TrimSpace(string(logBuf[:L]))
			// Выводим каждую строку прошивки в серверный лог с пометкой [FW]
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					log.Printf("[FW:%s] %s", from, line)
				}
			}
			l.Buffers.Mu.Lock()
			l.Buffers.LogCount++
			l.Buffers.LastFrom = from
			l.Buffers.Logs = append(l.Buffers.Logs, text)
			if len(l.Buffers.Logs) > 64 {
				l.Buffers.Logs = l.Buffers.Logs[len(l.Buffers.Logs)-64:]
			}
			l.Buffers.Mu.Unlock()

		// --- v1: сырые сэмплы ADXL355 ---
		case ingest.Magic:
			if err := readExact(conn, hdr[2:8], &streak); err != nil {
				return
			}
			na := int(hdr[4])
			nad := int(binary.LittleEndian.Uint16(hdr[5:7]))
			bodyLen := na*12 + nad*2
			if bodyLen < 0 || ingest.HeaderLen+bodyLen > len(dataBuf) {
				return
			}
			copy(dataBuf, hdr)
			if err := readExact(conn, dataBuf[ingest.HeaderLen:ingest.HeaderLen+bodyLen], &streak); err != nil {
				return
			}
			cfg := l.CfgFn()
			decoder.Config = cfg
			packet := dataBuf[:ingest.HeaderLen+bodyLen]
			sensorID, adxlX, adxlY, adxlZ, ads, err := decoder.DecodeDataPacket(packet)
			if err != nil || adxlX == nil {
				continue
			}
			nowNs := time.Now().UnixNano()
			l.Buffers.Mu.Lock()
			l.Buffers.Packets++
			l.Buffers.LastFrom = from
			s := l.Buffers.GetOrCreateSensor(sensorID)
			decoder.TrimAndAppend(s, adxlX, adxlY, adxlZ, ads, nowNs)
			l.Buffers.Mu.Unlock()
		// --- v2: Edge-DSP результат (DspPacket) ---
		case ingest.MagicDsp, ingest.MagicDspRaw:
			readLen := ingest.DspHeaderLen
			mode := "COMPACT"
			if magic == ingest.MagicDspRaw {
				readLen = ingest.DspRawPacketLen
				mode = "RAW_SYNC"
			}
			l.Buffers.Mu.Lock()
			pkts := l.Buffers.Packets
			l.Buffers.Mu.Unlock()

			// Дочитываем оставшиеся байты
			copy(dspBuf[:2], hdr[:2])
			if err := readExact(conn, dspBuf[2:readLen], &streak); err != nil {
				return
			}
			res, err := ingest.DecodeDspPacket(dspBuf[:readLen])
			if err != nil {
				log.Printf("[ingest/tcp] dsp decode from=%s: %v", from, err)
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
				arrivalNow := time.Now()
				if prev.summaryAt.IsZero() {
					prev.summaryAt = arrivalNow
				}
				arrivalDeltaMs := 0.0
				tsfDeltaMs := 0.0
				jitterMs := 0.0
				if !prev.arrival.IsZero() {
					arrivalDeltaMs = float64(arrivalNow.Sub(prev.arrival).Microseconds()) / 1000.0
				}
				if prev.tsfUs != 0 && res.TsfUs >= prev.tsfUs {
					tsfDeltaMs = float64(res.TsfUs-prev.tsfUs) / 1000.0
				}
				if arrivalDeltaMs > 0 && tsfDeltaMs > 0 {
					jitterMs = arrivalDeltaMs - tsfDeltaMs
				}
				prev.count++
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
					log.Printf("[RAW-FLOW-TCP-SLOW] from=%s sensor=%d arrival_dt=%.1fms tsf_dt=%.1fms jitter=%.1fms qhint=server_read_delay_or_sender_gap",
						from, res.SensorID, arrivalDeltaMs, tsfDeltaMs, jitterMs)
				}
				if arrivalNow.Sub(prev.summaryAt) >= time.Second {
					log.Printf("[RAW-FLOW-TCP-SUM] sensor=%d count=%d max_arrival=%.1fms max_tsf=%.1fms max_abs_jitter=%.1fms bursts=%d",
						res.SensorID, prev.count, prev.maxArrivalMs, prev.maxTSFMs, prev.maxAbsJitterMs, prev.bursts)
					prev.summaryAt = arrivalNow
					prev.count = 0
					prev.bursts = 0
					prev.maxArrivalMs = 0
					prev.maxTSFMs = 0
					prev.maxAbsJitterMs = 0
				}
				prev.arrival = arrivalNow
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
			// TSF → Unix µs: unixUs = tsfUs - tsfOffsetUs (offset отрицателен, поэтому вычитание = прибавление Unix-базы)
			tsfUnixMs := float64(int64(res.TsfUs)-tsfOffsetUs) / 1000.0
			windowMs := 0.0
			if adxlRate > 0 {
				windowMs = float64(res.WindowSamples) * 1000.0 / adxlRate
			}
			latMs := float64(res.ServerArrival)/1e6 - tsfUnixMs - windowMs
			if latMs < -1000 || latMs > 60_000 {
				latMs = 0
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
			l.Buffers.Mu.Lock()
			l.Buffers.Packets++
			l.Buffers.LastFrom = from
			s := l.Buffers.GetOrCreateSensor(res.SensorID)
			s.AdxlX = append(s.AdxlX, res.RawX...)
			s.AdxlY = append(s.AdxlY, res.RawY...)
			s.AdxlZ = append(s.AdxlZ, res.RawZ...)

			// Ограничиваем буфер
			maxAdxl := int(cfg.TimeWindow.Seconds() * adxlRate)
			if maxAdxl < 100 {
				maxAdxl = 4000
			}
			if len(s.AdxlX) > maxAdxl {
				s.AdxlX = s.AdxlX[len(s.AdxlX)-maxAdxl:]
			}
			if len(s.AdxlY) > maxAdxl {
				s.AdxlY = s.AdxlY[len(s.AdxlY)-maxAdxl:]
			}
			if len(s.AdxlZ) > maxAdxl {
				s.AdxlZ = s.AdxlZ[len(s.AdxlZ)-maxAdxl:]
			}

			l.Buffers.StoreDsp(res.SensorID, snap)
			l.Buffers.Mu.Unlock()

			// Подробный лог каждые 50 пакетов (~12 с при 250 мс/окно)
			if pkts%50 == 0 {
				log.Printf("[DSP:%s] sensor=%d tsf=%.3fs freq=%.1fHz φ=%.1f° snr=%.1fdB mag=%.4fg lat=%.0fms mode=%s",
					from, res.SensorID,
					float64(res.TsfUs)/1e6,
					res.DemodFreqHz,
					res.Peaks[0].PhaseDeg,
					res.Peaks[0].SNR,
					res.Peaks[0].Magnitude,
					latMs,
					mode)
			}

		default:
			log.Printf("[ingest/tcp] bad magic from=%s: 0x%04X", from, magic)
			return
		}
	}
}

func readExact(conn net.Conn, buf []byte, streak *int) error {
	off := 0
	for off < len(buf) {
		if err := conn.SetReadDeadline(time.Now().Add(readDeadline)); err != nil {
			return err
		}
		n, err := conn.Read(buf[off:])
		if n > 0 {
			off += n
			*streak = 0
		}
		if err == nil {
			continue
		}
		if isTimeout(err) {
			*streak++
			if *streak > 5 {
				log.Printf("[ingest/tcp] read stall streak=%d from %s (read %d/%d bytes)", *streak, conn.RemoteAddr(), off, len(buf))
			}
			continue
		}
		if errors.Is(err, io.EOF) && off > 0 {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
