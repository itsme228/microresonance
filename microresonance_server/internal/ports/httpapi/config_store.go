// HTTP API конфигов для излучателя (transmitter) и приёмника (receiver).
// Оба устройства периодически опрашивают GET /api/transmitter и GET /api/receiver.

package httpapi

import (
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"server/internal/domain/signal"
)

const rawSyncWindowSamples = 120

func snapFrequencyToReceiverBin(freqHz float64, adxlRate int) float64 {
	if freqHz <= 0 {
		return freqHz
	}
	if adxlRate <= 0 {
		adxlRate = 4000
	}
	binHz := float64(adxlRate) / rawSyncWindowSamples
	if binHz <= 0 {
		return freqHz
	}
	snapped := math.Round(freqHz/binHz) * binHz
	if snapped < 0.2 {
		snapped = 0.2
	}
	return snapped
}

func SnapProtocolFrequency(freqHz float64, adxlRate int) float64 {
	return snapFrequencyToReceiverBin(freqHz, adxlRate)
}

func snapProtocolFrequencies(c *ProtocolConfig, adxlRate int) {
	c.Frequency = snapFrequencyToReceiverBin(c.Frequency, adxlRate)
	c.FrequencyR = snapFrequencyToReceiverBin(c.FrequencyR, adxlRate)
	c.Freq1 = snapFrequencyToReceiverBin(c.Freq1, adxlRate)
	c.Freq2 = snapFrequencyToReceiverBin(c.Freq2, adxlRate)
	// ReceiverFreq must also be snapped: when ReceiverLinked=false the lock-in demodulates
	// at ReceiverFreq, which must match the emitter's actual bin (Frequency is already snapped).
	c.ReceiverFreq = snapFrequencyToReceiverBin(c.ReceiverFreq, adxlRate)
	// Snap individual tone frequencies so the emitter plays on exact FFT bin boundaries
	// and the tone markers in the waterfall align with the actual signal peaks.
	for i := range c.Tones {
		c.Tones[i].Freq = snapFrequencyToReceiverBin(c.Tones[i].Freq, adxlRate)
	}
}

// TransmitterChannel — параметры одного канала (left/right) излучателя.
type TransmitterChannel struct {
	Freq    float64 `json:"freq"`  // 1–2000 Hz
	Wave    string  `json:"wave"`  // "sine", "triangle", "square"
	Phase   float64 `json:"phase"` // 0–360°
	Enabled bool    `json:"enabled"`
}

// TransmitterConfig — конфиг для ESP32-S3-CAM излучателя (два AD9833).
type TransmitterConfig struct {
	Mode  string             `json:"mode"` // "independent", "stereo", "phase"
	Left  TransmitterChannel `json:"left"`
	Right TransmitterChannel `json:"right"`
}

// ReceiverConfig — конфиг для приёмника (куда слать поток, параметры сенсоров).
type ReceiverConfig struct {
	Server   string `json:"server"`   // IP или host для TCP/UDP
	Port     int    `json:"port"`     // порт, обычно 5555
	Proto    string `json:"proto"`    // "tcp" or "udp"
	AdxlRate int    `json:"adxlRate"` // частота ADXL, Гц (опционально)
	AdsRate  int    `json:"adsRate"`  // частота ADS, Гц (опционально)
}

// Marker — метка состояния, выставленная оператором.
type Marker struct {
	Label string `json:"label"`
	At    int64  `json:"at"` // unix ms
}

// IntentConfig — параметры "намерения" (пинги).
type IntentConfig struct {
	PingIntervalMs int  `json:"pingIntervalMs"`
	PingLengthMs   int  `json:"pingLengthMs"`
	Randomize      bool `json:"randomize"`
	Cue            bool `json:"cue"`
}

// SessionMeta — метаданные сессии.
type SessionMeta struct {
	SubjectID     string `json:"subjectId"`
	Posture       string `json:"posture"`       // sitting/standing/lying
	BeltTightness string `json:"beltTightness"` // loose/med/tight
	Placement     string `json:"placement"`     // neck/occiput
	Notes         string `json:"notes"`
	SavedAt       int64  `json:"savedAt"` // unix ms
}

// QualityMetrics — метрики качества сигнала (обновляются по мере необходимости).
type QualityMetrics struct {
	SNR       float64 `json:"snr"`
	Coherence float64 `json:"coherence"`
	Clipping  bool    `json:"clipping"`
	Motion    bool    `json:"motion"`
}

// SyncTrigger — команда синхронного запуска для всех устройств.
// Сервер вычисляет targetTsfUs на основе последнего X-Device-TSF заголовка.
// Устройства исполняют когда esp_wifi_get_tsf64() >= targetTsfUs.
type SyncTrigger struct {
	TargetTsfUs uint64  `json:"targetTsfUs,omitempty"`
	FreqHz      float64 `json:"freqHz,omitempty"`
	SessionID   uint32  `json:"sessionId,omitempty"`
}

type Tone struct {
	Freq         float64 `json:"freq"`
	LeftLevel    float64 `json:"leftLevel"`
	RightLevel   float64 `json:"rightLevel"`
	LeftEnabled  bool    `json:"leftEnabled"`
	RightEnabled bool    `json:"rightEnabled"`
}

// ProtocolConfig — операторский протокол (full set).
// UI: только MANUAL + AUTO SCAN (2 вкладки). Остальное — backend/logging.
type ProtocolConfig struct {
	SessionState string `json:"sessionState"` // idle/running/stopped
	PanicStop    bool   `json:"panicStop"`
	Preset       string `json:"preset"` // manual | am-sine | burst | chirp

	// MANUAL: Linked=true — одна ручка, оба канала повторяют; Linked=false — L и R отдельно
	Linked bool `json:"linked"` // по умолчанию true: двигаешь один — второй повторяет

	ReceiverLinked bool    `json:"receiverLinked"` // true: приемник использует Frequency
	ReceiverFreq   float64 `json:"receiverFreq"`   // частота приемника (если ReceiverLinked = false)

	Intensity  int    `json:"intensity"`  // Level 1
	IntensityR int    `json:"intensityR"` // Level 2
	Mode       string `json:"mode"`
	Channel    string `json:"channel"` // L / R / Both
	MuteLeft   bool   `json:"muteLeft"`
	MuteRight  bool   `json:"muteRight"`

	// MIMO / Multi-tone
	Freq1      float64 `json:"freq1"`
	Freq2      float64 `json:"freq2"`
	Level1     int     `json:"level1"`
	Level2     int     `json:"level2"`
	MimoLocked bool    `json:"mimoLocked"`
	Tones      []Tone  `json:"tones"` // Dynamic up to 10 tones

	// Live Metrics for Logging/ITR
	LiveITR     float64 `json:"liveItr"`
	LiveEntropy float64 `json:"liveEntropy"`

	WindowMs    int     `json:"windowMs"`    // фикс 450 ms для AUTO; для chirp — длительность прохода (10–20 с)
	RampMs      int     `json:"rampMs"`      // фикс 20 ms
	Frequency   float64 `json:"frequency"`   // L (или общая при Linked); для AM-sine — carrier 200–400 Гц
	FrequencyR  float64 `json:"frequencyR"`  // R при unlinked
	StreamMode  string  `json:"streamMode"`  // "compact" | "raw"
	FreqStep    float64 `json:"freqStep"`    // для dual: freq_R = Frequency + FreqStep; для chirp — диапазон
	PhaseOffset int     `json:"phaseOffset"` // 0 или 180 (R при unlinked)
	PhaseL      int     `json:"phaseL"`      // 0 или 180 (L при unlinked)

	// AM-sine (основной режим): carrier + огибающая 0.5–1.5 Гц, глубина 60–80%
	AmFreqHz   float64 `json:"amFreqHz,omitempty"`   // 1 Гц по умолчанию
	AmDepthPct int     `json:"amDepthPct,omitempty"` // 70 по умолчанию
	// Burst (диагностика): ON 100–200 мс, OFF 300–500 мс, серия 10–20, пауза 5–10 с
	BurstOnMs    int `json:"burstOnMs,omitempty"`
	BurstOffMs   int `json:"burstOffMs,omitempty"`
	BurstCount   int `json:"burstCount,omitempty"`
	BurstPauseMs int `json:"burstPauseMs,omitempty"`

	// Двухэксайдерные режимы зондирования A/B/C/D. Envelope обязательна.
	ProbingMode     string  `json:"probingMode,omitempty"`     // "A"|"B"|"C"|"D"
	EnvType         string  `json:"envType,omitempty"`         // "sine"|"asymmetric"|"stepped"|"sawtooth"
	EnvFreqHz       float64 `json:"envFreqHz,omitempty"`       // 0.3–6 Гц
	EnvDepth        float64 `json:"envDepth,omitempty"`        // 0.3–0.6
	EnvCounterPhase bool    `json:"envCounterPhase,omitempty"` // D: противофазная огибающая L/R

	// Только для отображения/лога при AUTO
	AutoPresetName string `json:"autoPresetName,omitempty"`
	StepIndex      int    `json:"stepIndex,omitempty"`

	// TOMO Binding persistence
	TomoSensorMap map[string]int `json:"tomoSensorMap,omitempty"`

	// Скрыто из UI (backend/logging)
	Intent      IntentConfig   `json:"intent,omitempty"`
	Markers     []Marker       `json:"markers,omitempty"`
	Session     SessionMeta    `json:"session,omitempty"`
	Quality     QualityMetrics `json:"quality,omitempty"`
	SyncTrigger SyncTrigger    `json:"syncTrigger,omitempty"`
}

// SensorStatus — краткий статус одного датчика для OLED трансивера.
type SensorStatus struct {
	ID     uint8   `json:"id"`
	SNR    float32 `json:"snr"`   // dB
	Mag    float32 `json:"mag"`   // g
	FreqHz float32 `json:"freq"`  // Hz
	AgeMs  int64   `json:"ageMs"` // мс с последнего пакета
}

// ConfigStore — хранилище конфигов, общее для HTTP и (при необходимости) TUI.
type ConfigStore struct {
	mu                   sync.RWMutex
	Transmitter          TransmitterConfig
	Receiver             ReceiverConfig
	Protocol             ProtocolConfig
	statePath            string
	Version              uint64
	UpdatedAtMs          int64

	// TSF-оффсет: вычисляется из заголовка X-Device-TSF в HTTP-запросах устройств.
	// tsfOffsetUs = deviceTsfUs - serverUnixUs; позволяет оценить текущий TSF как:
	//   estimatedTsfUs = time.Now().UnixMicro() + tsfOffsetUs
	tsfOffsetUs         int64 // EMA-filtered offset (используется для SyncTrigger)
	tsfOffsetRaw        int64 // последнее сырое значение (для диагностики)
	tsfOffsetUpdated    int64 // server time.UnixNano() последнего обновления (0 = не получали)

	// Провайдер активных датчиков (устанавливается через RegisterHTTPAPI).
	getSensors func() []SensorStatus
}

type persistedState struct {
	Transmitter TransmitterConfig `json:"transmitter"`
	Receiver    ReceiverConfig    `json:"receiver"`
	Protocol    ProtocolConfig    `json:"protocol"`
	Version     uint64            `json:"version"`
	UpdatedAtMs int64             `json:"updatedAtMs"`
}

// defaultTransmitterConfig возвращает конфиг по умолчанию для излучателя.
func defaultTransmitterConfig() TransmitterConfig {
	return TransmitterConfig{
		Mode: "stereo",
		Left: TransmitterChannel{
			Freq:    311.0,
			Wave:    "sine",
			Phase:   0,
			Enabled: true,
		},
		Right: TransmitterChannel{
			Freq:    401.0,
			Wave:    "sine",
			Phase:   180,
			Enabled: true,
		},
	}
}

// defaultReceiverConfig возвращает конфиг по умолчанию для приёмника.
func defaultReceiverConfig() ReceiverConfig {
	return ReceiverConfig{
		Server:   "192.168.200.10",
		Port:     5555,
		Proto:    "udp",
		AdxlRate: 4000,
		AdsRate:  40000,
	}
}

func defaultProtocolConfig() ProtocolConfig {
	return ProtocolConfig{
		SessionState:    "idle",
		PanicStop:       false,
		Preset:          "manual",
		Linked:          true,
		ReceiverLinked:  true,
		ReceiverFreq:    311.0,
		Intensity:       20,
		IntensityR:      20,
		Mode:            "independent",
		WindowMs:        450,
		RampMs:          20,
		Frequency:       300.0,
		FrequencyR:      300.0,
		FreqStep:        0,
		StreamMode:      "raw",
		AmFreqHz:        0.7,
		AmDepthPct:      65,
		BurstOnMs:       150,
		BurstOffMs:      400,
		BurstCount:      12,
		BurstPauseMs:    8000,
		ProbingMode:     "",
		EnvType:         "sine",
		EnvFreqHz:       1.0,
		EnvDepth:        0.5,
		EnvCounterPhase: false,
		PhaseOffset:     0,
		PhaseL:          0,
		Channel:         "Both",
		MuteLeft:        false,
		MuteRight:       false,
		Intent: IntentConfig{
			PingIntervalMs: 2000,
			PingLengthMs:   100,
			Randomize:      true,
			Cue:            false,
		},
		Markers: []Marker{},
		Session: SessionMeta{
			SubjectID:     "",
			Posture:       "sitting",
			BeltTightness: "med",
			Placement:     "neck",
			Notes:         "",
			SavedAt:       0,
		},
		Quality: QualityMetrics{},
	}
}

// GetTransmitter возвращает копию конфига излучателя.
func (s *ConfigStore) GetTransmitter() TransmitterConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Transmitter
}

// SetTransmitter атомарно задаёт конфиг излучателя.
func (s *ConfigStore) SetTransmitter(c TransmitterConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Transmitter = c
	syncProtocolFromTransmitterLocked(&s.Protocol, &s.Transmitter)
	s.Version++
	s.UpdatedAtMs = time.Now().UnixMilli()
	s.persistLocked()
}

// GetReceiver возвращает копию конфига приёмника.
func (s *ConfigStore) GetReceiver() ReceiverConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Receiver
}

// SetReceiver атомарно задаёт конфиг приёмника.
func (s *ConfigStore) SetReceiver(c ReceiverConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Receiver = c
	s.Version++
	s.UpdatedAtMs = time.Now().UnixMilli()
	s.persistLocked()
}

// GetProtocol возвращает копию протокола.
func (s *ConfigStore) GetProtocol() ProtocolConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Protocol
}

// SetProtocol атомарно задаёт протокол.
func (s *ConfigStore) SetProtocol(c ProtocolConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapProtocolFrequencies(&c, s.Receiver.AdxlRate)
	syncLegacyToneFields(&c)
	s.Protocol = c
	syncTransmitterFromProtocolLocked(&s.Protocol, &s.Transmitter)
	s.Version++
	s.UpdatedAtMs = time.Now().UnixMilli()
	s.persistLocked()
}

// AddMarker добавляет метку в протокол (с ограничением длины).
func (s *ConfigStore) AddMarker(label string) {
	if label == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := Marker{Label: label, At: time.Now().UnixMilli()}
	s.Protocol.Markers = append(s.Protocol.Markers, m)
	if len(s.Protocol.Markers) > 128 {
		s.Protocol.Markers = s.Protocol.Markers[len(s.Protocol.Markers)-128:]
	}
	s.Version++
	s.UpdatedAtMs = time.Now().UnixMilli()
	s.persistLocked()
}

func defaultStatePath() string {
	return filepath.Join(".", "config_state.json")
}

func (s *ConfigStore) persistLocked() {
	if s.statePath == "" {
		return
	}
	ps := persistedState{
		Transmitter: s.Transmitter,
		Receiver:    s.Receiver,
		Protocol:    s.Protocol,
		Version:     s.Version,
		UpdatedAtMs: s.UpdatedAtMs,
	}
	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		log.Printf("[config] marshal state: %v", err)
		return
	}
	tmp := s.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("[config] write temp state: %v", err)
		return
	}
	if err := os.Rename(tmp, s.statePath); err != nil {
		log.Printf("[config] replace state: %v", err)
	}
}

func (s *ConfigStore) load() {
	if s.statePath == "" {
		return
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[config] read state: %v", err)
		}
		return
	}
	var ps persistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		log.Printf("[config] parse state: %v", err)
		return
	}
	s.Transmitter = ps.Transmitter
	s.Receiver = ps.Receiver
	s.Protocol = ps.Protocol
	s.Version = ps.Version
	s.UpdatedAtMs = ps.UpdatedAtMs
	if s.Receiver.AdxlRate <= 0 {
		s.Receiver.AdxlRate = 4000
	}
	if s.Receiver.AdsRate <= 0 {
		s.Receiver.AdsRate = 40000
	}
	snapProtocolFrequencies(&s.Protocol, s.Receiver.AdxlRate)
	syncLegacyToneFields(&s.Protocol)
	syncTransmitterFromProtocolLocked(&s.Protocol, &s.Transmitter)
}

// UpdateTsfOffset сохраняет TSF-оффсет из заголовка X-Device-TSF.
// Применяет EMA (α=0.2) для подавления ±50мс HTTP round-trip джиттера.
func (s *ConfigStore) UpdateTsfOffset(deviceTsfUs uint64, serverNs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw := int64(deviceTsfUs) - serverNs/1000
	s.tsfOffsetRaw = raw
	if s.tsfOffsetUpdated == 0 {
		// Первое измерение — сразу задаём без фильтрации.
		s.tsfOffsetUs = raw
	} else {
		const alpha = 0.2
		s.tsfOffsetUs = int64(alpha*float64(raw) + (1-alpha)*float64(s.tsfOffsetUs))
	}
	s.tsfOffsetUpdated = serverNs
	log.Printf("[TSF-OFFSET] raw=%dµs filtered=%dµs delta=%dµs",
		raw, s.tsfOffsetUs, raw-s.tsfOffsetUs)
}

// GetTsfOffsetUs возвращает tsfOffsetUs = deviceTsfUs - serverUnixUs (отрицателен).
// Возвращает 0 если X-Device-TSF ещё не получали.
func (s *ConfigStore) GetTsfOffsetUs() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.tsfOffsetUpdated == 0 {
		return 0
	}
	return s.tsfOffsetUs
}

// DispatchSyncTrigger выставляет SyncTrigger с targetTsfUs = estimatedCurrentTsf + lookAheadUs.
// Требует хотя бы одного X-Device-TSF заголовка (возвращает false если TSF не получали).
// Устройства сравнивают его с esp_wifi_get_tsf64().
func (s *ConfigStore) DispatchSyncTrigger(lookAheadUs uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tsfOffsetUpdated == 0 {
		return false // ещё нет TSF от устройств
	}
	estimatedTsfUs := time.Now().UnixMicro() + s.tsfOffsetUs
	s.Protocol.SyncTrigger = SyncTrigger{
		TargetTsfUs: uint64(estimatedTsfUs) + lookAheadUs,
		FreqHz:      s.Protocol.Frequency,
		SessionID:   s.Protocol.SyncTrigger.SessionID + 1,
	}
	s.Version++
	s.UpdatedAtMs = time.Now().UnixMilli()
	log.Printf("[SYNC-DISPATCH] session=%d targetTsf=%d us freq=%.1fHz (tsfOffset=%d lookAhead=%d)",
		s.Protocol.SyncTrigger.SessionID,
		s.Protocol.SyncTrigger.TargetTsfUs,
		s.Protocol.SyncTrigger.FreqHz,
		s.tsfOffsetUs,
		lookAheadUs)
	return true
}

func syncProtocolFromTransmitterLocked(p *ProtocolConfig, t *TransmitterConfig) {
	if p == nil || t == nil {
		return
	}
	p.Linked = (t.Mode == "stereo")
	p.Frequency = t.Left.Freq
	p.FrequencyR = t.Right.Freq
	p.PhaseL = int(t.Left.Phase)
	p.PhaseOffset = int(t.Right.Phase)
	if t.Mode == "independent" && t.Right.Freq != t.Left.Freq {
		p.FreqStep = t.Right.Freq - t.Left.Freq
	}
	p.MuteLeft = !t.Left.Enabled
	p.MuteRight = !t.Right.Enabled
	if p.MuteLeft && p.MuteRight {
		p.SessionState = "stopped"
	}
}

func syncTransmitterFromProtocolLocked(p *ProtocolConfig, t *TransmitterConfig) {
	if p == nil || t == nil {
		return
	}
	leftToneOn, rightToneOn := false, false
	leftFreq := p.Frequency
	rightFreq := p.FrequencyR
	for _, tone := range p.Tones {
		if toneEnabledLeft(tone) {
			leftToneOn = true
			leftFreq = tone.Freq
		}
		if toneEnabledRight(tone) {
			rightToneOn = true
			rightFreq = tone.Freq
		}
	}
	if p.Linked {
		t.Mode = "stereo"
	} else {
		t.Mode = "independent"
	}
	if !leftToneOn && !rightToneOn {
		leftFreq = p.Frequency
		rightFreq = p.FrequencyR
	}
	if !p.Linked && !rightToneOn {
		rightFreq = p.FrequencyR
	} else if p.Linked && p.Mode == "dual-tone" && !rightToneOn {
		rightFreq = p.Frequency + p.FreqStep
	}
	t.Left.Freq = leftFreq
	t.Right.Freq = rightFreq
	// When tones override the base frequency, keep Frequency/FrequencyR in sync so that
	// ingestCfgFn and the firmware HTTP response both see the actual transmitted frequency.
	if leftToneOn {
		p.Frequency = leftFreq
	}
	if rightToneOn {
		p.FrequencyR = rightFreq
	}
	if p.Linked {
		t.Left.Phase = 0
		t.Right.Phase = float64(p.PhaseOffset)
	} else {
		t.Left.Phase = float64(p.PhaseL)
		t.Right.Phase = float64(p.PhaseOffset)
	}

	enL := true
	enR := true
	switch p.Channel {
	case "L":
		enR = false
	case "R":
		enL = false
	}
	if len(p.Tones) > 0 {
		enL = enL && leftToneOn
		enR = enR && rightToneOn
	}
	if p.MuteLeft {
		enL = false
	}
	if p.MuteRight {
		enR = false
	}
	if p.PanicStop || p.SessionState == "stopped" {
		enL = false
		enR = false
	}
	t.Left.Enabled = enL
	t.Right.Enabled = enR
}

// UnifiedProtocol — единый источник правды для устройств.
type UnifiedProtocol struct {
	Version     uint64            `json:"version"`
	UpdatedAtMs int64             `json:"updatedAtMs"`
	Transmitter TransmitterConfig `json:"transmitter"`
	Receiver    ReceiverConfig    `json:"receiver"`
	Protocol    ProtocolConfig    `json:"protocol"`
	Sensors     []SensorStatus    `json:"sensors,omitempty"`
}

func (s *ConfigStore) snapshotUnified() UnifiedProtocol {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := UnifiedProtocol{
		Version:     s.Version,
		UpdatedAtMs: s.UpdatedAtMs,
		Transmitter: s.Transmitter,
		Receiver:    s.Receiver,
		Protocol:    s.Protocol,
	}
	if s.getSensors != nil {
		u.Sensors = s.getSensors()
	}
	return u
}

const jsonCT = "application/json; charset=utf-8"

// writeJSON helper sends JSON with Content-Length to prevent HTTP clients from blocking when keep-alive is active
func writeJSON(w http.ResponseWriter, data interface{}) {
	b, err := json.Marshal(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", jsonCT)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Write(b)
}

func (s *ConfigStore) serveGetTransmitter(w http.ResponseWriter, _ *http.Request) {
	c := s.GetTransmitter()
	writeJSON(w, c)
}

func (s *ConfigStore) servePutTransmitter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var c TransmitterConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.SetTransmitter(c)
	writeJSON(w, c)
}

func (s *ConfigStore) serveGetReceiver(w http.ResponseWriter, _ *http.Request) {
	c := s.GetReceiver()
	writeJSON(w, c)
}

func (s *ConfigStore) servePutReceiver(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var c ReceiverConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.SetReceiver(c)
	writeJSON(w, c)
}

func (s *ConfigStore) serveGetProtocol(w http.ResponseWriter, _ *http.Request) {
	c := s.snapshotUnified()
	writeJSON(w, c)
}

func (s *ConfigStore) servePutProtocol(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error: "+err.Error(), http.StatusBadRequest)
		return
	}
	var wrapper struct {
		Transmitter *TransmitterConfig `json:"transmitter"`
		Receiver    *ReceiverConfig    `json:"receiver"`
		Protocol    *ProtocolConfig    `json:"protocol"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if wrapper.Transmitter == nil && wrapper.Receiver == nil && wrapper.Protocol == nil {
		var p ProtocolConfig
		if err := json.Unmarshal(body, &p); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.SetProtocol(p)
	} else {
		if wrapper.Transmitter != nil {
			s.SetTransmitter(*wrapper.Transmitter)
		}
		if wrapper.Receiver != nil {
			s.SetReceiver(*wrapper.Receiver)
		}
		if wrapper.Protocol != nil {
			s.SetProtocol(*wrapper.Protocol)
		}
	}
	writeJSON(w, s.snapshotUnified())
}

// tsfMiddleware читает X-Device-TSF и обновляет TSF-оффсет в store.
func tsfMiddleware(store *ConfigStore, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hdr := r.Header.Get("X-Device-TSF"); hdr != "" {
			if v, err := strconv.ParseUint(hdr, 10, 64); err == nil {
				store.UpdateTsfOffset(v, time.Now().UnixNano())
			}
		}
		next(w, r)
	}
}

// RegisterHTTPAPI вешает эндпоинты на mux. Если mux == nil, используется http.DefaultServeMux.
// bufs используется для включения статуса датчиков в ответ GET /api/protocol.
func RegisterHTTPAPI(mux *http.ServeMux, store *ConfigStore, bufs *signal.Buffers) {
	if bufs != nil {
		store.getSensors = func() []SensorStatus {
			summaries := bufs.GetActiveSensors()
			out := make([]SensorStatus, len(summaries))
			for i, s := range summaries {
				out[i] = SensorStatus{
					ID:     s.ID,
					SNR:    s.SNR,
					Mag:    s.Magnitude,
					FreqHz: s.FreqHz,
					AgeMs:  s.AgeMs,
				}
			}
			return out
		}
	}
	if mux == nil {
		mux = http.DefaultServeMux
	}
	// /api/logs: прошивки отправляют накопленные логи раз в N секунд (POST text/plain)
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil || len(body) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		deviceID := r.Header.Get("X-Device-ID")
		if deviceID == "" {
			deviceID = r.RemoteAddr
		}
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				log.Printf("[FW-HTTP:%s] %s", deviceID, line)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/sync_trigger", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !store.DispatchSyncTrigger(800_000) {
			http.Error(w, "no device TSF available: wait for first /api/protocol poll", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, store.snapshotUnified())
	})

	mux.HandleFunc("/api/transmitter", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/transmitter" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			store.serveGetTransmitter(w, r)
		case http.MethodPut, http.MethodPost:
			store.servePutTransmitter(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/receiver", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/receiver" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			store.serveGetReceiver(w, r)
		case http.MethodPut, http.MethodPost:
			store.servePutReceiver(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/protocol", tsfMiddleware(store, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/protocol" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			store.serveGetProtocol(w, r)
		case http.MethodPut, http.MethodPost:
			store.servePutProtocol(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
}

// NewConfigStore создаёт хранилище с дефолтными конфигами.
func NewConfigStore() *ConfigStore {
	s := &ConfigStore{
		Transmitter: defaultTransmitterConfig(),
		Receiver:    defaultReceiverConfig(),
		Protocol:    defaultProtocolConfig(),
		statePath:   defaultStatePath(),
		Version:     1,
		UpdatedAtMs: time.Now().UnixMilli(),
	}
	s.load()
	return s
}
