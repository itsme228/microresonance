package ingest

import (
	"encoding/binary"
	"fmt"
	"math"
	"server/internal/domain/signal"
	"time"
)

// ------------------------------------------------------------- constants ----

const (
	// v1 — сырые сэмплы ADXL355
	Magic     = 0x4D52
	MagicLog  = 0x4D4C
	HeaderLen = 8
	NADXL     = 64
	NADS      = 320
	AdxlBytes = NADXL * 12
	AdsBytes  = NADS * 2
	PacketLen = HeaderLen + AdxlBytes + AdsBytes

	AdxlLSBperG = 256000
	AdsMax      = 65536
	AdsVref     = 2.56

	// v2 — Edge-DSP результат
	MagicDsp        = 0x4D53 // 'MS' - Compact (183 байта)
	MagicDspRaw     = 0x4D58 // 'MX' - Raw Sync (distinguish from old MT)
	DspMaxPeaks     = 8
	DspSpecBands    = 32
	DspRawSamples   = 120
	DspRawQG        = 16384.0
	DspHeaderLen    = 183
	DspLsmSlots     = 30                                                               // DSP_LSM_SLOTS в прошивке; 60 → 1940 байт (>MTU), 30 → 1460 байт (<MTU)
	DspRawPacketLen = 16 + DspRawSamples*6 + 2 + DspLsmSlots*12 + DspLsmSlots*4 + 2 + DspRawSamples*2
	rawSatAbsG      = 1.95
	rawSatCountMin  = 2
)

// ----------------------------------------------------------------- config ---

type Config struct {
	AdxlRate    int
	AdsRate     int
	DemodFreqHz float64
	TimeWindow  time.Duration
}

// ---------------------------------------------------------------- v1 decoder -

type Decoder struct {
	Config Config
}

func NewDecoder(cfg Config) *Decoder { return &Decoder{Config: cfg} }

func (d *Decoder) DecodeDataPacket(p []byte) (sensorID uint8, adxlX, adxlY, adxlZ, ads []float64, err error) {
	if len(p) < HeaderLen {
		return 0, nil, nil, nil, nil, nil
	}
	na := int(p[4])
	nad := int(binary.LittleEndian.Uint16(p[5:7]))
	sensorID = p[7]
	need := HeaderLen + na*12 + nad*2
	if na != NADXL || nad < 0 || nad > NADS || len(p) < need {
		return 0, nil, nil, nil, nil, nil
	}
	adxlX = make([]float64, na)
	adxlY = make([]float64, na)
	adxlZ = make([]float64, na)
	ads = make([]float64, nad)
	off := HeaderLen
	for i := 0; i < na; i++ {
		x := int32(binary.LittleEndian.Uint32(p[off:]))
		y := int32(binary.LittleEndian.Uint32(p[off+4:]))
		z := int32(binary.LittleEndian.Uint32(p[off+8:]))
		off += 12
		adxlX[i] = float64(x) / float64(AdxlLSBperG)
		adxlY[i] = float64(y) / float64(AdxlLSBperG)
		adxlZ[i] = float64(z) / float64(AdxlLSBperG)
	}
	for i := 0; i < nad; i++ {
		v := binary.LittleEndian.Uint16(p[off : off+2])
		off += 2
		ads[i] = (float64(v) - AdsMax/2) / (AdsMax / 2) * AdsVref
	}
	return sensorID, adxlX, adxlY, adxlZ, ads, nil
}

func (d *Decoder) TrimAndAppend(s *signal.SensorData, adxlX, adxlY, adxlZ, ads []float64, nowNs int64) {
	ar := d.Config.AdxlRate
	if ar < 1 {
		ar = 4000
	}
	ad := d.Config.AdsRate
	if ad < 1 {
		ad = 20000
	}
	window := d.Config.TimeWindow
	if window <= 0 {
		window = 500 * time.Millisecond
	}
	maxAdxl := int(window.Seconds() * float64(ar))
	maxAds := int(window.Seconds() * float64(ad))

	s.Packets++
	s.LastPacketUnixNano = nowNs
	s.AdxlX = append(s.AdxlX, adxlX...)
	s.AdxlY = append(s.AdxlY, adxlY...)
	s.AdxlZ = append(s.AdxlZ, adxlZ...)
	s.Ads = append(s.Ads, ads...)

	if len(s.AdxlX) > maxAdxl {
		s.AdxlX = trimCap(s.AdxlX, maxAdxl)
		s.AdxlY = trimCap(s.AdxlY, maxAdxl)
		s.AdxlZ = trimCap(s.AdxlZ, maxAdxl)
	}
	if len(s.Ads) > maxAds {
		s.Ads = trimCap(s.Ads, maxAds)
	}
}

func trimCap(slice []float64, keep int) []float64 {
	if len(slice) <= keep {
		return slice
	}
	slice = slice[len(slice)-keep:]
	if cap(slice) > keep*2 {
		tmp := make([]float64, keep)
		copy(tmp, slice)
		return tmp
	}
	return slice
}

// --------------------------------------------------------------- v2 decoder -

// DspPeak — один спектральный пик из DspPacket.
type DspPeak struct {
	FreqHz    float32
	Magnitude float32
	PhaseDeg  float32
	SNR       float32
}

// DspResult — декодированный DspPacket (v2).
type DspResult struct {
	SensorID      uint8
	NPeaks        uint8
	TsfUs         uint64
	DemodFreqHz   float32
	WindowSamples uint32
	Peaks         [DspMaxPeaks]DspPeak
	Spectrum      [DspSpecBands]uint8
	RawX          []float64
	RawY          []float64
	RawZ          []float64
	Emg           []float64
	LsmAccelX     []float64
	LsmAccelY     []float64
	LsmAccelZ     []float64
	LsmGyroX      []float64
	LsmGyroY      []float64
	LsmGyroZ      []float64
	LsmTs         []uint32
	Flags         uint8
	BatteryMV     uint16
	ServerArrival int64 // time.Now().UnixNano()
}

// DecodeDspPacket разбирает пакет MAGIC 0x4D53 или 0x4D54.
func DecodeDspPacket(p []byte) (*DspResult, error) {
	if len(p) < 16 {
		return nil, fmt.Errorf("dsp: short packet %d", len(p))
	}
	magic := binary.LittleEndian.Uint16(p[0:2])

	if magic == MagicDspRaw {
		if len(p) < DspRawPacketLen {
			return nil, fmt.Errorf("dsp: short raw packet %d < %d", len(p), DspRawPacketLen)
		}
		windowSamples := binary.LittleEndian.Uint32(p[12:16])
		rawN := int(windowSamples)
		if rawN < 0 {
			rawN = 0
		}
		if rawN > DspRawSamples {
			rawN = DspRawSamples
		}
		r := &DspResult{
			SensorID:      p[2],
			TsfUs:         binary.LittleEndian.Uint64(p[4:12]),
			WindowSamples: windowSamples,
			ServerArrival: time.Now().UnixNano(),
		}
		r.RawX = make([]float64, rawN)
		r.RawY = make([]float64, rawN)
		r.RawZ = make([]float64, rawN)
		off := 16
		for i := 0; i < DspRawSamples; i++ {
			v := float64(int16(binary.LittleEndian.Uint16(p[off:off+2]))) / DspRawQG
			off += 2
			if i < rawN {
				r.RawX[i] = v
				if math.Abs(v) >= rawSatAbsG {
					r.Flags |= 0x02
				}
			}
		}
		rawSatCount := 0
		for i := 0; i < DspRawSamples; i++ {
			v := float64(int16(binary.LittleEndian.Uint16(p[off:off+2]))) / DspRawQG
			off += 2
			if i < rawN {
				r.RawY[i] = v
				if math.Abs(v) >= rawSatAbsG {
					r.Flags |= 0x02
				}
			}
		}
		for i := 0; i < DspRawSamples; i++ {
			v := float64(int16(binary.LittleEndian.Uint16(p[off:off+2]))) / DspRawQG
			off += 2
			if i < rawN {
				r.RawZ[i] = v
				if math.Abs(v) >= rawSatAbsG {
					rawSatCount++
				}
			}
		}
		if rawSatCount >= rawSatCountMin {
			r.Flags |= 0x02
		}
		
		if len(p) >= DspRawPacketLen {
			lsmSamples := int(binary.LittleEndian.Uint16(p[off : off+2]))
			off += 2
			if lsmSamples > DspLsmSlots {
				lsmSamples = DspLsmSlots
			}

			r.LsmAccelX = make([]float64, lsmSamples)
			r.LsmAccelY = make([]float64, lsmSamples)
			r.LsmAccelZ = make([]float64, lsmSamples)
			r.LsmGyroX = make([]float64, lsmSamples)
			r.LsmGyroY = make([]float64, lsmSamples)
			r.LsmGyroZ = make([]float64, lsmSamples)

			for i := 0; i < DspLsmSlots; i++ {
				v := float64(int16(binary.LittleEndian.Uint16(p[off:off+2]))) / DspRawQG
				off += 2
				if i < lsmSamples {
					r.LsmAccelX[i] = v
				}
			}
			for i := 0; i < DspLsmSlots; i++ {
				v := float64(int16(binary.LittleEndian.Uint16(p[off:off+2]))) / DspRawQG
				off += 2
				if i < lsmSamples {
					r.LsmAccelY[i] = v
				}
			}
			for i := 0; i < DspLsmSlots; i++ {
				v := float64(int16(binary.LittleEndian.Uint16(p[off:off+2]))) / DspRawQG
				off += 2
				if i < lsmSamples {
					r.LsmAccelZ[i] = v
				}
			}
			for i := 0; i < DspLsmSlots; i++ {
				v := float64(int16(binary.LittleEndian.Uint16(p[off:off+2]))) / 114.28 // 250 dps, 8.75 mdps/LSB
				off += 2
				if i < lsmSamples {
					r.LsmGyroX[i] = v
				}
			}
			for i := 0; i < DspLsmSlots; i++ {
				v := float64(int16(binary.LittleEndian.Uint16(p[off:off+2]))) / 114.28
				off += 2
				if i < lsmSamples {
					r.LsmGyroY[i] = v
				}
			}
			for i := 0; i < DspLsmSlots; i++ {
				v := float64(int16(binary.LittleEndian.Uint16(p[off:off+2]))) / 114.28
				off += 2
				if i < lsmSamples {
					r.LsmGyroZ[i] = v
				}
			}

			r.LsmTs = make([]uint32, lsmSamples)
			for i := 0; i < DspLsmSlots; i++ {
				v := binary.LittleEndian.Uint32(p[off : off+4])
				off += 4
				if i < lsmSamples {
					r.LsmTs[i] = v
				}
			}
			
			emgSamples := int(binary.LittleEndian.Uint16(p[off : off+2]))
			off += 2
			if emgSamples > DspRawSamples { emgSamples = DspRawSamples }
			r.Emg = make([]float64, emgSamples)
			for i := 0; i < DspRawSamples; i++ {
				v := float64(binary.LittleEndian.Uint16(p[off : off+2]))
				off += 2
				if i < emgSamples { r.Emg[i] = v }
			}
		}

		return r, nil
	} else if magic == MagicDsp {
		if len(p) < DspHeaderLen {
			return nil, fmt.Errorf("dsp: short compact packet %d < %d", len(p), DspHeaderLen)
		}
		r := &DspResult{
			SensorID:      p[2],
			NPeaks:        p[3],
			TsfUs:         binary.LittleEndian.Uint64(p[4:12]),
			DemodFreqHz:   math.Float32frombits(binary.LittleEndian.Uint32(p[12:16])),
			WindowSamples: binary.LittleEndian.Uint32(p[16:20]),
			Flags:         p[180],
			BatteryMV:     binary.LittleEndian.Uint16(p[181:183]),
			ServerArrival: time.Now().UnixNano(),
		}
		if r.NPeaks > DspMaxPeaks {
			r.NPeaks = DspMaxPeaks
		}
		for i := 0; i < DspMaxPeaks; i++ {
			off := 20 + i*16
			r.Peaks[i] = DspPeak{
				FreqHz:    math.Float32frombits(binary.LittleEndian.Uint32(p[off : off+4])),
				Magnitude: math.Float32frombits(binary.LittleEndian.Uint32(p[off+4 : off+8])),
				PhaseDeg:  math.Float32frombits(binary.LittleEndian.Uint32(p[off+8 : off+12])),
				SNR:       math.Float32frombits(binary.LittleEndian.Uint32(p[off+12 : off+16])),
			}
		}
		copy(r.Spectrum[:], p[148:180])
		return r, nil
	}

	return nil, fmt.Errorf("dsp: bad magic 0x%04X", magic)
}
