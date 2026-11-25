package signal

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

type MimoFreqStats struct {
	Freq  float32 `json:"freq"`
	Phase float32 `json:"phase"`
	Mag   float32 `json:"mag"`
	SNR   float32 `json:"snr"`
}

type PhysioMetricsDump struct {
	TSFUs     uint64 `json:"tsf_us"`
	SensorID  uint8  `json:"sensor_id"`
	Label     string `json:"label,omitempty"`
	Recording bool   `json:"recording,omitempty"`

	// Primary Carrier (F1)
	F1 MimoFreqStats `json:"f1"`

	// MIMO (F2) if active
	F2      MimoFreqStats `json:"f2,omitempty"`
	DPhiBeat float32       `json:"d_phi_beat,omitempty"`

	// Bio-metrics
	BPM float64 `json:"bpm"`
	HRV float64 `json:"hrv"`

	// DSP Metrics
	ApEn     float64 `json:"ap_en"`
	PhaseVel float64 `json:"phase_vel"`
	HardLock bool    `json:"hard_lock"`

	// System Health
	JitterUs  float64 `json:"jitter_us"`
	DriftRate float64 `json:"drift_rate"`

	// Raw Data (tails or samples)
	EmgTail       []float64 `json:"emg_tail,omitempty"`
	LsmAccelXTail []float64 `json:"lsm_accel_x_tail,omitempty"`
	LsmAccelYTail []float64 `json:"lsm_accel_y_tail,omitempty"`
	LsmAccelZTail []float64 `json:"lsm_accel_z_tail,omitempty"`
	LsmGyroXTail  []float64 `json:"lsm_gyro_x_tail,omitempty"`
	LsmGyroYTail  []float64 `json:"lsm_gyro_y_tail,omitempty"`
	LsmGyroZTail  []float64 `json:"lsm_gyro_z_tail,omitempty"`
}

type PhysioLogger struct {
	ch   chan PhysioMetricsDump
	file *os.File
}

var globalPhysioLogger *PhysioLogger

type VoiceState struct {
	IsRecording bool
	StartTSF    uint64
	EndTSF      uint64
	AudioFile   string
}

var globalVoiceState VoiceState

func ToggleVoiceRecording() bool {
	if globalVoiceState.IsRecording {
		globalVoiceState.IsRecording = false
		globalVoiceState.EndTSF = 0 // To be filled by next packet
		log.Printf("[VOICE] Stopped recording")
		return false
	}
	globalVoiceState.IsRecording = true
	log.Printf("[VOICE] Started recording (Sync marked in JSON)")
	return true
}

func IsVoiceRecording() bool {
	return globalVoiceState.IsRecording
}

func init() {
	globalPhysioLogger = &PhysioLogger{
		ch: make(chan PhysioMetricsDump, 8192),
	}
	dir := "data/physio"
	_ = os.MkdirAll(dir, 0755)
	name := fmt.Sprintf("%s/physio_raw_%s.jsonl", dir, time.Now().Format("20060102_150405"))
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("[PHYSIO_LOG] failed to open file: %v", err)
	} else {
		globalPhysioLogger.file = f
		go globalPhysioLogger.run()
	}
}

func (l *PhysioLogger) run() {
	encoder := json.NewEncoder(l.file)
	for r := range l.ch {
		if l.file != nil {
			err := encoder.Encode(r)
			if err != nil {
				log.Printf("[PHYSIO_LOG] encode error: %v", err)
			}
		}
	}
}

func LogPhysioMetrics(r PhysioMetricsDump) {
	if globalPhysioLogger != nil && globalPhysioLogger.file != nil {
		select {
		case globalPhysioLogger.ch <- r:
		default:
		}
	}
}
