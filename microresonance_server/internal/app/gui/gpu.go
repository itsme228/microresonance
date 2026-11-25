// GPU mode: ebiten real-time signal acquisition, FFT spectra, IIR bands,
// cross-correlation, dynamics, and TSF-coherent 3-axis lock-in dashboard.
// Run: ./receiverd -gpu -addr=:5555

package gui

import (
	"fmt"
	"image/color"
	"log"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"
	"server/internal/domain/signal"
	"server/internal/ports/httpapi"
)

const (
	headerH       = 20
	mainTabRowH   = 22
	contentTopGap = 4
	sensorRowH    = 20
	sliderH       = 22
	fftLabelWidth = 56 // label column width (Sensor:, t:, Y:, FFT:)
	plotPad       = 4
	strokeW       = 1.5
	keyRepeat     = 6
	thumbW        = 10
	panelMinW     = 240
	panelMaxW     = 320
	panelPad      = 10
	tabH          = 20
	rowH          = 22
	btnH          = 18
	freqMinHz     = 0.2
	freqMaxHz     = 40000.0
	adxlYMin      = -1.0
	adxlYMax      = 1.0
	adsYMin       = -2.56
	adsYMax       = 2.56
)

var (
	clrX     = color.RGBA{0xf4, 0x4e, 0x4e, 0xff}
	clrY     = color.RGBA{0x66, 0xbb, 0x6a, 0xff}
	clrZ     = color.RGBA{0x42, 0xa5, 0xf5, 0xff}
	clrADS   = color.RGBA{0xff, 0xca, 0x28, 0xff}
	clrBg    = color.RGBA{0x20, 0x20, 0x30, 0xff}
	clrGrd   = color.RGBA{0x2a, 0x2a, 0x38, 0xff}
	clrGrid  = color.RGBA{0x1e, 0x1e, 0x28, 0xff}
	clrAcc   = color.RGBA{0x5c, 0x6b, 0xce, 0xff}
	clrAuto  = color.RGBA{0x66, 0xbb, 0x6a, 0xff}
	clrPanel = color.RGBA{0x16, 0x18, 0x24, 0xff}
	clrBtn   = color.RGBA{0x24, 0x29, 0x3a, 0xff}
	clrBtnOn = color.RGBA{0x3a, 0x53, 0x9f, 0xff}
	clrTxt   = color.RGBA{0xd8, 0xe2, 0xff, 0xff}
)

type uiRect struct {
	x int
	y int
	w int
	h int
}

func (r uiRect) contains(mx, my int) bool {
	return mx >= r.x && mx <= r.x+r.w && my >= r.y && my <= r.y+r.h
}

type xcorrRectItem struct {
	key        string
	rect       uiRect
	isCheckbox bool
}

type rawPopupBandData struct {
	xB, yB, zB, squared []float64
}

type sliderDef struct {
	rect uiRect
	min  float64
	max  float64
	step float64
}

type panelLayout struct {
	show    bool
	x       int
	y       int
	w       int
	h       int
	tabs    map[string]uiRect
	buttons map[string]uiRect
	sliders map[string]sliderDef
	inputs  map[string]uiRect
}

type sensorGUIState struct {
	// SCAN: FFT cache for Draw
	scanFFTCacheAt   time.Time
	scanFFTCachePkts uint64
	scanFFTMagX      []float64
	scanFFTMagY      []float64
	scanFFTMagZ      []float64
	scanFFTFreqAdxl  []float64
	scanFFTMagADS    []float64
	scanFFTFreqADS   []float64

	// SCAN: spectral heatmap (waterfall) - combined FFT X+Y+Z
	scanHeatmapBuf      [][]float64
	scanHeatmapHead     int
	scanEdgeHeatmapBuf  [][32]uint8
	scanEdgeHeatmapHead int
	lastHeatmapPkts     uint64
	waterfallMaxMag     float64

	// Per-axis FFT waterfalls (X, Y, Z) - shared between SCAN and LOCKIN screens
	lockinHeatX    [][]float64
	lockinHeatY    [][]float64
	lockinHeatZ    [][]float64
	lockinHeatHead [3]int
	lockinLastPkts uint64
}

type gpuGame struct {
	buf         *signal.Buffers
	keyCooldown int
	store       *httpapi.ConfigStore
	protocol    httpapi.ProtocolConfig

	vertAuto      bool
	vertMult      float64
	fftSize       int
	freqLog       bool
	dragSlider    string
	dragValue     float64
	mousePrev     bool
	panelTab      string // "EMITTER" | "RECEIVER"
	textFocus     string
	textCooldown  int
	inputCooldown int
	syncTick      int

	// Per-sensor UI state (isolates waterfalls and FFT cache)
	sensorStates map[int]*sensorGUIState

	// Active main screen: "RAW" (FFT) | "DYN" | "BANDS" (RAW) | "XCORR" | "LOCKIN"
	mainScreen   string
	mainTabRects map[string]uiRect

	// Selected sensor for plots (-1 = first available)
	selectedSensorID int
	sensorDropdownRects []struct {
		id   int
		rect uiRect
	}
	scanDumpBtnRect      uiRect
	scanSessionName      string
	scanSessionInputRect uiRect
	streamCompactRect    uiRect
	streamRawRect        uiRect

	// XCORR screen state
	xcorrScrollY        int
	xcorrUseForAlphabet map[string]bool
	xcorrRects          []xcorrRectItem

	// Performance counters
	lastDebugLogTs  time.Time
	perfFrames      int64
	perfUpdateTotal time.Duration
	perfDrawFrames  int64
	perfDrawTotal   time.Duration
	perfDrawLayout  time.Duration
	perfDrawHeader  time.Duration
	perfDrawTabs    time.Duration
	perfDrawContent time.Duration
	perfDrawPanel   time.Duration
	perfDrawRAW     time.Duration
	perfDrawBANDS   time.Duration
	perfDrawXCORR   time.Duration
	perfDrawSlow33  int64

	// BANDS: causal time-domain IIR filterbank
	bandFilters       []signal.BandpassFilter
	bandFiltersFs     float64
	bandsPlotRects    []uiRect
	rawPopupOpen      bool
	rawPopupBandIndex int
	rawPopupHold      bool
	rawPopupSnapshot  *rawPopupBandData
	rawPopupHoldRect  uiRect
	rawPopupCloseRect uiRect
	rawPopupBandData  *rawPopupBandData

	// Emitter manual frequency input state
	manualEditChannel       string // "L" | "R"
	manualFreqExtra         bool
	manualFreqInput         string
	manualFreqInputIdx      int
	manualFreqInputFocused  bool
	manualFreq2Input        string
	manualFreq2InputFocused bool

	// Runner: STOP = graceful (finish step); PANIC = instant mute
	runnerMu            sync.Mutex
	runnerStopRequested bool

	// Reusable scratch buffers
	fftScratch        *signal.FFTScratch
	scanFFTScratch    *signal.FFTScratch
	downSampleScratch *signal.DownSampleScratch
	sensorScratchX    []float64
	sensorScratchY    []float64
	sensorScratchZ    []float64
	sensorScratchADS  []float64
	panelLayoutReuse  panelLayout
	cachedSyncMetrics signal.SyncMetrics

	// SCAN / LOCKIN waterfall GPU textures
	scanHeatmapImg *ebiten.Image
	scanHeatmapPix []byte
	lockinHeatImgs [3]*ebiten.Image
	scanAxisImgs   [3]*ebiten.Image
	lockinHeatPix  []byte

	// LOCKIN: live result history
	lockinHistDXY  []float64
	lockinHistDYZ  []float64
	lockinHistDXZ  []float64
	lockinHistMagX []float64
	lockinHistMagY []float64
	lockinHistMagZ []float64

	// DYNAMICS screen history (sampled at Draw rate, capped at 5000 pts)
	dynHistPhase      []float64
	dynHistMag        []float64
	dynHistMagX       []float64
	dynHistMagY       []float64
	dynHistDeltaXY    []float64
	dynHistDeltaYZ    []float64
	dynHistDeltaZX    []float64
	dynHistMagTotal   []float64
	dynHistPhaseX     []float64
	dynHistPhaseY     []float64
	dynHistPhaseZ     []float64
	dynHistVel        []float64
	dynHistJitter     []float64
	dynHistTsf        []float64
	dynHistSNR        []float64
	dynPhaseZero      float64
	dynLastDspTsf     uint64
	dynLockinHardLock bool

	// Orbit trail for DYN screen: polar Z, and phase-phase Lissajous
	dynOrbitTrail []struct {
		P float64
		M float64
	}
	dynOrbitXY [][2]float64
	dynOrbitYZ [][2]float64
	dynOrbitXZ [][2]float64

	// DYN plot checkbox state (key -> enabled)
	dynPlotEnabled   map[string]bool
	dynCheckboxRects []struct {
		key  string
		rect uiRect
	}
	dynVoiceBtnRect uiRect

	// Manual Zoom
	zoomX float64
	zoomY float64
}

func (g *gpuGame) Layout(outsideWidth, outsideHeight int) (int, int) {
	return outsideWidth, outsideHeight
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func panelWidth(w int) int {
	pw := panelMaxW
	if w < 900 {
		pw = panelMinW
	}
	if w < 760 {
		pw = 0
	}
	return pw
}

func (g *gpuGame) buildPanelLayout(w, h int) panelLayout {
	pw := panelWidth(w)
	if pw == 0 {
		return panelLayout{show: false}
	}
	x0 := w - pw
	y0 := headerH + mainTabRowH + 6
	switch g.mainScreen {
	case "RAW":
		y0 += contentTopGap + sensorRowH + 4 + sliderH
	case "BANDS":
		y0 += contentTopGap + sensorRowH + 4
	}
	hh := h - y0 - 6
	lay := &g.panelLayoutReuse
	if lay.tabs == nil {
		lay.tabs = make(map[string]uiRect)
		lay.buttons = make(map[string]uiRect)
		lay.sliders = make(map[string]sliderDef)
		lay.inputs = make(map[string]uiRect)
	}
	for k := range lay.tabs {
		delete(lay.tabs, k)
	}
	for k := range lay.buttons {
		delete(lay.buttons, k)
	}
	for k := range lay.sliders {
		delete(lay.sliders, k)
	}
	for k := range lay.inputs {
		delete(lay.inputs, k)
	}
	lay.show = true
	lay.x = x0
	lay.y = y0
	lay.w = pw
	lay.h = hh

	tabNames := []string{"EMITTER", "RECEIVER"}
	tabW := (pw - 2*panelPad) / len(tabNames)
	tx := x0 + panelPad
	ty := y0
	for i, name := range tabNames {
		lay.tabs[name] = uiRect{x: tx + i*tabW, y: ty, w: tabW - 2, h: tabH}
	}
	cursorY := y0 + tabH + 6
	row := func(height int) uiRect {
		r := uiRect{x: x0 + panelPad, y: cursorY, w: pw - 2*panelPad, h: height}
		cursorY += height + 6
		return r
	}
	split := func(r uiRect, cols int) []uiRect {
		out := make([]uiRect, cols)
		gap := 6
		wc := (r.w - gap*(cols-1)) / cols
		for i := 0; i < cols; i++ {
			out[i] = uiRect{x: r.x + i*(wc+gap), y: r.y, w: wc, h: r.h}
		}
		return out
	}

	tab := g.panelTab
	if tab == "" || (tab != "EMITTER" && tab != "RECEIVER") {
		tab = "EMITTER"
	}

	switch tab {
	case "EMITTER":
		// Signal type: SINE / AM-SINE / CHIRP
		r := row(btnH)
		bs := split(r, 3)
		lay.buttons["signal_sine"] = bs[0]
		lay.buttons["signal_am"] = bs[1]
		lay.buttons["signal_chirp"] = bs[2]

		// External Freq + ON toggle on the same row
		r = row(btnH)
		bs2 := split(r, 2)
		lay.buttons["freq_extra"] = bs2[0]
		lay.buttons["open"] = bs2[1]
		freqMin, freqMax := 66.67, 2000.0
		if g.manualFreqExtra {
			freqMin, freqMax = 1.0, 12000.0
		}
		// Dynamic Tones (up to 10)
		if len(g.protocol.Tones) == 0 {
			g.protocol.Tones = append(g.protocol.Tones, httpapi.Tone{
				Freq:         g.protocol.Frequency,
				LeftEnabled:  true,
				RightEnabled: true,
				LeftLevel:    float64(g.protocol.Intensity),
				RightLevel:   float64(g.protocol.Intensity),
			})
			if g.protocol.Tones[0].Freq <= 0 {
				g.protocol.Tones[0].Freq = 500.0
			}
			if g.protocol.Tones[0].LeftLevel <= 0 {
				g.protocol.Tones[0].LeftLevel = 50.0
			}
			if g.protocol.Tones[0].RightLevel <= 0 {
				g.protocol.Tones[0].RightLevel = 50.0
			}
		}
		for i := 0; i < len(g.protocol.Tones); i++ {
			r = row(rowH)
			bs := split(r, 12)
			lay.buttons[fmt.Sprintf("tone_l_en_%d", i)] = bs[0]
			lay.buttons[fmt.Sprintf("tone_r_en_%d", i)] = bs[1]
			sliderR := uiRect{bs[2].x, bs[2].y, bs[2].w + bs[3].w + bs[4].w + bs[5].w + bs[6].w + bs[7].w + bs[8].w + 36, bs[2].h}
			lay.sliders[fmt.Sprintf("freq_%d", i)] = sliderDef{rect: sliderR, min: freqMin, max: freqMax, step: 0.01}
			inputR := uiRect{bs[10].x, bs[10].y, bs[10].w + bs[11].w + 6, bs[10].h}
			lay.inputs[fmt.Sprintf("freq_val_%d", i)] = inputR
			if i > 0 {
				lay.buttons[fmt.Sprintf("del_tone_%d", i)] = bs[9]
			}

			r = row(rowH)
			bs = split(r, 2)
			lay.sliders[fmt.Sprintf("tone_l_level_%d", i)] = sliderDef{rect: bs[0], min: 0, max: 100, step: 1}
			lay.sliders[fmt.Sprintf("tone_r_level_%d", i)] = sliderDef{rect: bs[1], min: 0, max: 100, step: 1}
		}
		r = row(btnH)
		lay.buttons["add_tone"] = r

		r = row(rowH)
		lay.sliders["phase_r"] = sliderDef{rect: r, min: 0, max: 360, step: 1}

	case "RECEIVER":
		r := row(btnH)
		lay.buttons["receiver_linked"] = r
		if !g.protocol.ReceiverLinked {
			r = row(rowH)
			sliderR := uiRect{r.x, r.y, r.w - 50, r.h}
			inputR := uiRect{r.x + r.w - 44, r.y, 44, r.h}
			lay.sliders["receiver_freq"] = sliderDef{rect: sliderR, min: 66.67, max: 2000.0, step: 0.01}
			lay.inputs["receiver_freq_val"] = inputR
		}

		r = row(btnH)
		lay.buttons["run_auto_scan"] = r
		r = row(btnH)
		bs := split(r, 2)
		lay.buttons["run_burst"] = bs[0]
		lay.buttons["run_chirp"] = bs[1]
		r = row(btnH)
		bs = split(r, 2)
		lay.buttons["run_phase_scan"] = bs[0]
		lay.buttons["run_dual_asym"] = bs[1]
		r = row(btnH)
		lay.buttons["run_fast_scan"] = r
		r = row(btnH)
		bs = split(r, 4)
		lay.buttons["mode_a"] = bs[0]
		lay.buttons["mode_b"] = bs[1]
		lay.buttons["mode_c"] = bs[2]
		lay.buttons["mode_d"] = bs[3]
		r = row(btnH)
		bs = split(r, 2)
		lay.buttons["stop"] = bs[0]
		lay.buttons["panic"] = bs[1]

		r = row(btnH)
		lay.buttons["sync_trigger"] = r
		r = row(btnH)
		lay.buttons["sync_reset_calib"] = r
		r = row(52)
		lay.buttons["sync_tsf_status"] = r
		r = row(76)
		lay.buttons["sync_phase_metrics"] = r
		r = row(60)
		lay.buttons["sync_sensors_disp"] = r
	}
	return *lay
}

const (
	rampMs   = 20
	windowMs = 450
)

var fastScanFreqs = []float64{80, 100, 125, 160, 200, 250, 315, 400, 500, 630, 800, 1000, 1200}
var phaseScanFreqs = []float64{200, 400, 800}
var dualAsymPairs = [][2]float64{{311, 401}, {250, 315}, {400, 500}, {500, 630}}
var probingPhaseDegs = []int{0, 90, 120, 180}
var probingBeatsPairs = [][2]float64{{300, 304}, {400, 406}, {250, 256}, {500, 505}}
var probingDualPairs = [][2]float64{{200, 650}, {250, 700}, {280, 800}}

func (g *gpuGame) shouldStopRunner() bool {
	g.runnerMu.Lock()
	defer g.runnerMu.Unlock()
	if g.runnerStopRequested {
		return true
	}
	if g.store != nil && g.store.GetProtocol().PanicStop {
		return true
	}
	return false
}

func (g *gpuGame) requestStopRunner() {
	g.runnerMu.Lock()
	g.runnerStopRequested = true
	g.runnerMu.Unlock()
}

func (g *gpuGame) runPresetSteps(presetName string) {
	g.runnerMu.Lock()
	g.runnerStopRequested = false
	g.runnerMu.Unlock()

	p := g.store.GetProtocol()
	p.Preset = "manual"
	p.SessionState = "running"
	p.PanicStop = false
	p.RampMs = rampMs
	p.WindowMs = windowMs
	p.AutoPresetName = presetName
	p.StepIndex = 0
	p.MuteLeft = false
	p.MuteRight = false
	g.store.SetProtocol(p)

	applyStep := func(ch string, freqL, freqR float64, phase int, muteL, muteR bool, stepIdx int) {
		cfg := g.store.GetProtocol()
		cfg.Channel = ch
		cfg.Frequency = freqL
		cfg.FreqStep = freqR - freqL
		cfg.PhaseOffset = phase
		cfg.MuteLeft = muteL
		cfg.MuteRight = muteR
		cfg.StepIndex = stepIdx
		g.store.SetProtocol(cfg)
		ts := time.Now().UnixMilli()
		log.Printf("preset=%s step=%d ts=%d channel=%s freq_L=%.0f freq_R=%.0f phase=%d intensity=%d",
			presetName, stepIdx, ts, ch, freqL, freqR, phase, cfg.Intensity)
	}

	muteBoth := func() {
		cfg := g.store.GetProtocol()
		cfg.MuteLeft = true
		cfg.MuteRight = true
		g.store.SetProtocol(cfg)
	}

	switch presetName {
	case "fast-scan":
		channels := []struct {
			ch    string
			muteL bool
			muteR bool
		}{{"L", false, true}, {"R", true, false}, {"Both", false, false}}
		stepIdx := 0
		for _, c := range channels {
			if g.shouldStopRunner() {
				break
			}
			for _, freq := range fastScanFreqs {
				if g.shouldStopRunner() {
					break
				}
				applyStep(c.ch, freq, freq, 0, c.muteL, c.muteR, stepIdx)
				stepIdx++
				time.Sleep(windowMs * time.Millisecond)
			}
			if g.shouldStopRunner() {
				break
			}
			muteBoth()
			time.Sleep(300 * time.Millisecond)
		}
	case "phase-scan":
		stepIdx := 0
		for _, freq := range phaseScanFreqs {
			if g.shouldStopRunner() {
				break
			}
			applyStep("Both", freq, freq, 0, false, false, stepIdx)
			stepIdx++
			time.Sleep(2000 * time.Millisecond)
			if g.shouldStopRunner() {
				break
			}
			muteBoth()
			time.Sleep(300 * time.Millisecond)
			applyStep("Both", freq, freq, 180, false, false, stepIdx)
			stepIdx++
			time.Sleep(2000 * time.Millisecond)
			if g.shouldStopRunner() {
				break
			}
			muteBoth()
			time.Sleep(500 * time.Millisecond)
		}
	case "dual-asym":
		cfg := g.store.GetProtocol()
		cfg.Mode = "dual-tone"
		g.store.SetProtocol(cfg)
		stepIdx := 0
		for _, pair := range dualAsymPairs {
			if g.shouldStopRunner() {
				break
			}
			applyStep("Both", pair[0], pair[1], 0, false, false, stepIdx)
			stepIdx++
			time.Sleep(2000 * time.Millisecond)
			if g.shouldStopRunner() {
				break
			}
			muteBoth()
			time.Sleep(500 * time.Millisecond)
		}
	}

	cfg := g.store.GetProtocol()
	cfg.SessionState = "stopped"
	cfg.AutoPresetName = ""
	cfg.MuteLeft = true
	cfg.MuteRight = true
	g.store.SetProtocol(cfg)
	log.Printf("preset=%s finished sessionState=stopped", presetName)
}

func (g *gpuGame) runProbingMode(mode string) {
	g.runnerMu.Lock()
	g.runnerStopRequested = false
	g.runnerMu.Unlock()

	p := g.store.GetProtocol()
	p.Preset = "manual"
	p.SessionState = "running"
	p.PanicStop = false
	p.RampMs = rampMs
	p.WindowMs = windowMs
	p.ProbingMode = mode
	p.EnvType = "sine"
	p.EnvFreqHz = 1.0
	p.EnvDepth = 0.5
	p.MuteLeft = false
	p.MuteRight = false
	p.Channel = "Both"
	switch mode {
	case "A":
		p.EnvCounterPhase = false
		p.Mode = "stereo"
		p.AutoPresetName = "mode-a"
	case "B":
		p.EnvCounterPhase = false
		p.Mode = "dual-tone"
		p.AutoPresetName = "mode-b"
	case "C":
		p.EnvCounterPhase = false
		p.Mode = "dual-tone"
		p.AutoPresetName = "mode-c"
	case "D":
		p.EnvCounterPhase = true
		p.Mode = "stereo"
		p.AutoPresetName = "mode-d"
	default:
		p.AutoPresetName = ""
		g.store.SetProtocol(p)
		return
	}
	g.store.SetProtocol(p)

	apply := func(freqL, freqR float64, phase int) {
		cfg := g.store.GetProtocol()
		cfg.Frequency = freqL
		cfg.FreqStep = freqR - freqL
		cfg.PhaseOffset = phase
		cfg.MuteLeft = false
		cfg.MuteRight = false
		g.store.SetProtocol(cfg)
		log.Printf("probing=%s f1=%.0f f2=%.0f phase=%d", mode, freqL, freqR, phase)
	}
	muteBoth := func() {
		cfg := g.store.GetProtocol()
		cfg.MuteLeft = true
		cfg.MuteRight = true
		g.store.SetProtocol(cfg)
	}

	holdMs := 2000
	pauseMs := 300

	for !g.shouldStopRunner() {
		switch mode {
		case "A":
			for _, ph := range probingPhaseDegs {
				if g.shouldStopRunner() {
					break
				}
				apply(300, 300, ph)
				time.Sleep(time.Duration(holdMs) * time.Millisecond)
				if g.shouldStopRunner() {
					break
				}
				muteBoth()
				time.Sleep(time.Duration(pauseMs) * time.Millisecond)
			}
		case "B":
			for _, pair := range probingBeatsPairs {
				if g.shouldStopRunner() {
					break
				}
				apply(pair[0], pair[1], 0)
				time.Sleep(time.Duration(holdMs) * time.Millisecond)
				if g.shouldStopRunner() {
					break
				}
				muteBoth()
				time.Sleep(time.Duration(pauseMs) * time.Millisecond)
			}
		case "C":
			for _, pair := range probingDualPairs {
				if g.shouldStopRunner() {
					break
				}
				apply(pair[0], pair[1], 0)
				time.Sleep(time.Duration(holdMs) * time.Millisecond)
				if g.shouldStopRunner() {
					break
				}
				muteBoth()
				time.Sleep(time.Duration(pauseMs) * time.Millisecond)
			}
		case "D":
			apply(300, 300, 0)
			if g.shouldStopRunner() {
				break
			}
			time.Sleep(time.Duration(holdMs) * time.Millisecond)
			if g.shouldStopRunner() {
				break
			}
			muteBoth()
			time.Sleep(time.Duration(pauseMs) * time.Millisecond)
			apply(300, 300, 180)
			if g.shouldStopRunner() {
				break
			}
			time.Sleep(time.Duration(holdMs) * time.Millisecond)
			if g.shouldStopRunner() {
				break
			}
			muteBoth()
			time.Sleep(time.Duration(pauseMs) * time.Millisecond)
		}
	}

	cfg := g.store.GetProtocol()
	cfg.SessionState = "stopped"
	cfg.AutoPresetName = ""
	cfg.ProbingMode = ""
	cfg.MuteLeft = true
	cfg.MuteRight = true
	g.store.SetProtocol(cfg)
	log.Printf("probing=%s finished sessionState=stopped", mode)
}

func dropLastRune(s string) string {
	if s == "" {
		return s
	}
	_, size := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-size]
}

func (g *gpuGame) getSensorState(id int) *sensorGUIState {
	if g.sensorStates == nil {
		g.sensorStates = make(map[int]*sensorGUIState)
	}
	if g.sensorStates[id] == nil {
		g.sensorStates[id] = &sensorGUIState{
			lockinHeatX:        make([][]float64, 0),
			lockinHeatY:        make([][]float64, 0),
			lockinHeatZ:        make([][]float64, 0),
			scanHeatmapBuf:     make([][]float64, 0),
			scanEdgeHeatmapBuf: make([][32]uint8, 0),
		}
	}
	return g.sensorStates[id]
}

func (g *gpuGame) getSelectedSensorID() uint8 {
	ids := g.buf.ListSensors()
	if len(ids) == 0 {
		return 0
	}
	if g.selectedSensorID < 0 {
		return ids[0]
	}
	for _, id := range ids {
		if int(id) == g.selectedSensorID {
			return id
		}
	}
	return ids[0]
}

func (g *gpuGame) getSelectedSensorData() (x, y, z, ads []float64) {
	id := g.getSelectedSensorID()
	g.buf.GetSensorDataInto(id, &g.sensorScratchX, &g.sensorScratchY, &g.sensorScratchZ, &g.sensorScratchADS)
	return g.sensorScratchX, g.sensorScratchY, g.sensorScratchZ, g.sensorScratchADS
}
