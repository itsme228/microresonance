package gui

import (
	"fmt"
	"image"
	"log"
	"math"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"server/internal/domain/signal"
	"server/internal/ports/httpapi"
	"strings"
)

func (g *gpuGame) handleInput() {
	if g.inputCooldown > 0 {
		g.inputCooldown--
	}
	mx, my := ebiten.CursorPosition()
	mouseDown := ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft)
	clicked := mouseDown && !g.mousePrev && g.inputCooldown <= 0

	// Клик по вкладкам главного экрана: RAW | ПОЛОСЫ | DISCOVERY | ALPHABET | TRAINER | MEANING
	if clicked {
		for name, r := range g.mainTabRects {
			if r.contains(mx, my) {
				g.mainScreen = name
				g.inputCooldown = 10 // Prevent bounce when switching tabs
				g.textFocus = ""
				return
			}
		}

		// Выбор сенсора (dropdown)
		for _, sr := range g.sensorDropdownRects {
			if sr.rect.contains(mx, my) {
				g.selectedSensorID = sr.id
				return
			}
		}
	}

	// Screen-specific input
	switch g.mainScreen {
	case "DYN":
		if clicked {
			if g.dynVoiceBtnRect.contains(mx, my) {
				signal.ToggleVoiceRecording()
				g.inputCooldown = 15
				return
			}
			for _, cb := range g.dynCheckboxRects {
				if cb.rect.contains(mx, my) {
					if g.dynPlotEnabled == nil {
						g.dynPlotEnabled = dynDefaultPlotEnabled()
					}
					g.dynPlotEnabled[cb.key] = !g.dynPlotEnabled[cb.key]
					saveDynPlotConfig(g.dynPlotEnabled)
					return
				}
			}
		}
	case "RAW":
		if clicked {
			if g.scanDumpBtnRect.contains(mx, my) {
				g.textFocus = ""
				signal.ToggleDump(g.scanSessionName)
			} else if g.scanSessionInputRect.contains(mx, my) && !signal.IsDumping() {
				g.textFocus = "scanSessionName"
				g.textCooldown = 15
			} else {
				g.textFocus = ""
			}
		}
	case "BANDS":
		if clicked {
			if g.rawPopupOpen {
				if g.rawPopupHoldRect.contains(mx, my) {
					g.rawPopupHold = !g.rawPopupHold
					if g.rawPopupHold && g.rawPopupBandData != nil {
						// Snapshot for hold mode
						// ... logic omitted for brevity if not critical, but let's keep it if possible
					}
				} else if g.rawPopupCloseRect.contains(mx, my) {
					g.rawPopupOpen = false
					g.rawPopupHold = false
				}
			} else {
				for i, r := range g.bandsPlotRects {
					if r.contains(mx, my) {
						g.rawPopupOpen = true
						g.rawPopupBandIndex = i
						g.rawPopupHold = false
						break
					}
				}
			}
		}
	case "XCORR":
		if clicked {
			for _, xr := range g.xcorrRects {
				if xr.rect.contains(mx, my) && xr.isCheckbox {
					if g.xcorrUseForAlphabet == nil {
						g.xcorrUseForAlphabet = make(map[string]bool)
					}
					g.xcorrUseForAlphabet[xr.key] = !g.xcorrUseForAlphabet[xr.key]
					break
				}
			}
		}
		if _, wy := ebiten.Wheel(); wy != 0 {
			g.xcorrScrollY += int(wy * 48)
			if g.xcorrScrollY < 0 {
				g.xcorrScrollY = 0
			}
		}
	}

	// Panel input
	sw, sh := ebiten.WindowSize()
	layout := g.buildPanelLayout(sw, sh)
	if layout.show {
		// Panel Tabs
		if clicked {
			for name, r := range layout.tabs {
				if r.contains(mx, my) {
					g.panelTab = name
					g.textFocus = ""
					return
				}
			}
		}

		// Panel Sliders
		if mouseDown && g.dragSlider == "" {
			for id, sl := range layout.sliders {
				if sl.rect.contains(mx, my) {
					g.dragSlider = id
				}
			}
		}

		if mouseDown && g.dragSlider != "" && g.dragSlider != "1" && g.dragSlider != "2" && g.dragSlider != "3" && g.dragSlider != "4" {
			sliderName := g.dragSlider
			if sl, ok2 := layout.sliders[sliderName]; ok2 {
				t := (float64(mx) - float64(sl.rect.x)) / float64(sl.rect.w)
				t = clamp(t, 0, 1)
				val := sl.min + t*(sl.max-sl.min)
				if sl.step > 0 {
					val = math.Round(val/sl.step) * sl.step
				}
				g.applySliderValue(sliderName, val)
			}
		}
		if !mouseDown {
			g.dragSlider = ""
		}

		// Panel Buttons
		if clicked {
			for id, r := range layout.buttons {
				if r.contains(mx, my) {
					g.handlePanelButton(id)
					g.inputCooldown = 10
					break
				}
			}
		}

		// Panel Inputs
		if clicked {
			hitInput := false
			for id, r := range layout.inputs {
				if r.contains(mx, my) {
					g.textFocus = id
					if strings.HasPrefix(id, "freq_val_") {
						var idx int
						fmt.Sscanf(id, "freq_val_%d", &idx)
						if g.manualFreqInputFocused && g.manualFreqInputIdx != idx {
							g.applyFreqInput()
						}
						g.manualFreqInputFocused = true
						g.manualFreqInputIdx = idx
						g.manualFreqInput = ""
					}
					hitInput = true
					break
				}
			}
			if !hitInput {
				if g.manualFreqInputFocused {
					g.applyFreqInput()
					g.manualFreqInputFocused = false
				}
			}
		}
	}

	// Keyboard Input for Freq1 field
	if g.manualFreqInputFocused {
		chars := ebiten.AppendInputChars(nil)
		for _, c := range chars {
			if (c >= '0' && c <= '9') || c == '.' {
				g.manualFreqInput += string(c)
			}
		}
		if ebiten.IsKeyPressed(ebiten.KeyBackspace) && g.textCooldown <= 0 {
			if len(g.manualFreqInput) > 0 {
				g.manualFreqInput = g.manualFreqInput[:len(g.manualFreqInput)-1]
				g.textCooldown = 6
			}
		}
		if ebiten.IsKeyPressed(ebiten.KeyEnter) {
			g.applyFreqInput()
			g.manualFreqInputFocused = false
			g.textFocus = ""
		}
		if ebiten.IsKeyPressed(ebiten.KeyEscape) {
			g.manualFreqInputFocused = false
			g.textFocus = ""
		}
	}

	// Keyboard Input for Freq2 field
	if g.manualFreq2InputFocused {
		chars := ebiten.AppendInputChars(nil)
		for _, c := range chars {
			if (c >= '0' && c <= '9') || c == '.' {
				g.manualFreq2Input += string(c)
			}
		}
		if ebiten.IsKeyPressed(ebiten.KeyBackspace) && g.textCooldown <= 0 {
			if len(g.manualFreq2Input) > 0 {
				g.manualFreq2Input = g.manualFreq2Input[:len(g.manualFreq2Input)-1]
				g.textCooldown = 6
			}
		}
		if ebiten.IsKeyPressed(ebiten.KeyEnter) {
			g.applyFreq2Input()
			g.manualFreq2InputFocused = false
			g.textFocus = ""
		}
		if ebiten.IsKeyPressed(ebiten.KeyEscape) {
			g.manualFreq2InputFocused = false
			g.textFocus = ""
		}
	}
	if g.textCooldown > 0 {
		g.textCooldown--
	}

	// RAW Screen FFT Sliders
	if g.mainScreen == "RAW" {
		x0 := plotPad
		gap := 8
		autoW := 36
		autoGap := 4
		trackW := (sw - x0 - 3*fftLabelWidth - autoW - autoGap - 2*gap) / 3
		if trackW < 60 {
			trackW = 60
		}
		if trackW > 180 {
			trackW = 180
		}
		sySliders := headerH + mainTabRowH + contentTopGap + sensorRowH + 4
		ySx := x0 + fftLabelWidth + trackW + gap
		autoX := ySx + fftLabelWidth
		yTrackX := autoX + autoW + autoGap
		fftSx := ySx + fftLabelWidth + autoW + autoGap + trackW + gap

		timeR := image.Rect(x0+fftLabelWidth, sySliders, x0+fftLabelWidth+trackW, sySliders+16)
		vertR := image.Rect(yTrackX, sySliders, yTrackX+trackW, sySliders+16)
		autoR := image.Rect(autoX, sySliders-1, autoX+autoW, sySliders+11)
		fftR := image.Rect(fftSx+fftLabelWidth, sySliders, fftSx+fftLabelWidth+trackW, sySliders+16)

		if mouseDown {
			if g.dragSlider == "" {
				if autoR.Min.X <= mx && mx <= autoR.Max.X && autoR.Min.Y <= my && my <= autoR.Max.Y {
					g.vertAuto = !g.vertAuto
					g.dragSlider = "4"
				} else if timeR.Min.X <= mx && mx <= timeR.Max.X && timeR.Min.Y <= my && my <= timeR.Max.Y {
					g.dragSlider = "1"
				} else if vertR.Min.X <= mx && mx <= vertR.Max.X && vertR.Min.Y <= my && my <= vertR.Max.Y && !g.vertAuto {
					g.dragSlider = "2"
				} else if fftR.Min.X <= mx && mx <= fftR.Max.X && fftR.Min.Y <= my && my <= fftR.Max.Y {
					g.dragSlider = "3"
				}
			}

			v := (float64(mx) - float64(timeR.Min.X)) / float64(timeR.Dx())
			if g.dragSlider == "1" {
				v = clamp(v, 0, 1)
				*timeWindow = time.Duration(50+4950*v) * time.Millisecond
			} else if g.dragSlider == "2" {
				v = (float64(mx) - float64(vertR.Min.X)) / float64(vertR.Dx())
				v = clamp(v, 0, 1)
				g.vertMult = 0.25 + 3.75*v
			} else if g.dragSlider == "3" {
				v = (float64(mx) - float64(fftR.Min.X)) / float64(fftR.Dx())
				v = clamp(v, 0, 1)
				if v < 0.2 {
					g.fftSize = 512
				} else if v < 0.5 {
					g.fftSize = 1024
				} else if v < 0.8 {
					g.fftSize = 2048
				} else {
					g.fftSize = 4096
				}
			}
		}
	}

	g.mousePrev = mouseDown
}

func (g *gpuGame) applySliderValue(id string, val float64) {
	if strings.HasPrefix(id, "freq_") && !strings.HasPrefix(id, "freq_val_") {
		var idx int
		fmt.Sscanf(id, "freq_%d", &idx)
		if idx >= 0 && idx < len(g.protocol.Tones) {
			g.protocol.Tones[idx].Freq = val
			if g.store != nil {
				g.store.SetProtocol(g.protocol)
			}
		}
		return
	}
	if strings.HasPrefix(id, "tone_l_level_") {
		var idx int
		fmt.Sscanf(id, "tone_l_level_%d", &idx)
		if idx >= 0 && idx < len(g.protocol.Tones) {
			g.protocol.Tones[idx].LeftLevel = val
			if g.store != nil {
				g.store.SetProtocol(g.protocol)
			}
		}
		return
	}
	if strings.HasPrefix(id, "tone_r_level_") {
		var idx int
		fmt.Sscanf(id, "tone_r_level_%d", &idx)
		if idx >= 0 && idx < len(g.protocol.Tones) {
			g.protocol.Tones[idx].RightLevel = val
			if g.store != nil {
				g.store.SetProtocol(g.protocol)
			}
		}
		return
	}
	
	switch id {
	case "zoom_x":
		g.zoomX = val
	case "zoom_y":
		g.zoomY = val
	case "receiver_freq":
		g.protocol.ReceiverFreq = val
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "intensity":
		if g.protocol.Linked {
			g.protocol.Intensity = int(val)
			g.protocol.IntensityR = int(val)
		} else if g.manualEditChannel == "R" {
			g.protocol.IntensityR = int(val)
		} else {
			g.protocol.Intensity = int(val)
		}
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}

	case "freq":
		adxlRate := 4000
		if g.store != nil {
			adxlRate = g.store.GetReceiver().AdxlRate
		}
		val = httpapi.SnapProtocolFrequency(val, adxlRate)
		if g.protocol.Linked {
			g.protocol.Frequency = val
			g.protocol.FrequencyR = val
		} else if g.manualEditChannel == "R" {
			g.protocol.FrequencyR = val
		} else {
			g.protocol.Frequency = val
		}
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "phase_l":
		g.protocol.PhaseL = int(val)
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "phase_r":
		g.protocol.PhaseOffset = int(val)
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	}
}

func (g *gpuGame) handlePanelButton(id string) {
	if id == "add_tone" {
		if len(g.protocol.Tones) < 10 {
			f := 311.0
			if len(g.protocol.Tones) > 0 {
				f = g.protocol.Tones[len(g.protocol.Tones)-1].Freq + 10.0
			}
			g.protocol.Tones = append(g.protocol.Tones, httpapi.Tone{
				Freq:         f,
				LeftEnabled:  true,
				RightEnabled: true,
				LeftLevel:    50.0,
				RightLevel:   50.0,
			})
			if g.store != nil {
				g.store.SetProtocol(g.protocol)
			}
		}
		return
	}
	if strings.HasPrefix(id, "tone_l_en_") {
		var idx int
		fmt.Sscanf(id, "tone_l_en_%d", &idx)
		if idx >= 0 && idx < len(g.protocol.Tones) {
			g.protocol.Tones[idx].LeftEnabled = !g.protocol.Tones[idx].LeftEnabled
			if g.store != nil {
				g.store.SetProtocol(g.protocol)
			}
		}
		return
	}
	if strings.HasPrefix(id, "tone_r_en_") {
		var idx int
		fmt.Sscanf(id, "tone_r_en_%d", &idx)
		if idx >= 0 && idx < len(g.protocol.Tones) {
			g.protocol.Tones[idx].RightEnabled = !g.protocol.Tones[idx].RightEnabled
			if g.store != nil {
				g.store.SetProtocol(g.protocol)
			}
		}
		return
	}
	if strings.HasPrefix(id, "del_tone_") {
		var idx int
		fmt.Sscanf(id, "del_tone_%d", &idx)
		if idx >= 0 && idx < len(g.protocol.Tones) {
			g.protocol.Tones = append(g.protocol.Tones[:idx], g.protocol.Tones[idx+1:]...)
			sw, sh := ebiten.WindowSize(); g.buildPanelLayout(sw, sh)
			if g.store != nil {
				g.store.SetProtocol(g.protocol)
			}
		}
		return
	}
	switch id {
	case "open":
		if g.protocol.SessionState == "running" {
			g.protocol.SessionState = "stopped"
			g.protocol.MuteLeft = true
			g.protocol.MuteRight = true
		} else {
			g.protocol.SessionState = "running"
			g.protocol.PanicStop = false
			g.protocol.MuteLeft = (g.protocol.Channel == "R")
			g.protocol.MuteRight = (g.protocol.Channel == "L")
		}
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "panic":
		g.protocol.PanicStop = true
		g.protocol.SessionState = "stopped"
		g.protocol.MuteLeft = true
		g.protocol.MuteRight = true
		g.requestStopRunner()
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "ch_l":
		g.protocol.Channel = "L"
		g.protocol.MuteLeft = false
		g.protocol.MuteRight = true
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "ch_r":
		g.protocol.Channel = "R"
		g.protocol.MuteLeft = true
		g.protocol.MuteRight = false
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "ch_both":
		g.protocol.Channel = "Both"
		g.protocol.MuteLeft = false
		g.protocol.MuteRight = false
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "signal_sine":
		g.protocol.Preset = "manual"
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
		log.Printf("[SIGNAL] Waveform=SINE")
	case "signal_am":
		g.protocol.Preset = "am-sine"
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
		log.Printf("[SIGNAL] Waveform=AM-SINE amFreq=%.2fHz depth=%d%%", g.protocol.AmFreqHz, g.protocol.AmDepthPct)
	case "signal_chirp":
		g.protocol.Preset = "chirp"
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
		log.Printf("[SIGNAL] Waveform=CHIRP")
	case "mimo_locked":
		g.protocol.MimoLocked = !g.protocol.MimoLocked
		if g.protocol.MimoLocked {
			f1 := g.protocol.Freq1
			if f1 <= 0 {
				f1 = g.protocol.Frequency
			}
			g.protocol.Freq2 = f1
			g.protocol.FrequencyR = f1
			g.protocol.Level2 = g.protocol.Level1
			g.protocol.IntensityR = g.protocol.Intensity
		}
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
		if g.buf != nil {
			g.buf.Mu.Lock()
			if g.protocol.MimoLocked {
				g.buf.MimoFreq2Hz = 0 // same frequency = no second lock-in needed
			} else {
				g.buf.MimoFreq2Hz = g.protocol.Freq2
			}
			g.buf.Mu.Unlock()
		}
		log.Printf("[MIMO] locked=%v f1=%.2fHz f2=%.2fHz", g.protocol.MimoLocked, g.protocol.Freq1, g.protocol.Freq2)
	case "linked":
		g.protocol.Linked = !g.protocol.Linked
		if g.protocol.Linked {
			g.protocol.FrequencyR = g.protocol.Frequency
			g.protocol.Freq2 = g.protocol.Freq1
			g.protocol.IntensityR = g.protocol.Intensity
			g.protocol.Level2 = g.protocol.Level1
			g.protocol.PhaseL = 0
		}
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "receiver_linked":
		g.protocol.ReceiverLinked = !g.protocol.ReceiverLinked
		if g.protocol.ReceiverLinked {
			g.protocol.ReceiverFreq = g.protocol.Frequency
		}
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "freq_extra":
		g.manualFreqExtra = !g.manualFreqExtra
	case "phase_l_0":
		g.protocol.PhaseL = 0
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "phase_l_180":
		g.protocol.PhaseL = 180
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "phase_r_0":
		g.protocol.PhaseOffset = 0
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "phase_r_180":
		g.protocol.PhaseOffset = 180
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "stream_compact":
		g.protocol.StreamMode = "compact"
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "stream_raw":
		g.protocol.StreamMode = "raw"
		if g.store != nil {
			g.store.SetProtocol(g.protocol)
		}
	case "edit_l":
		g.manualEditChannel = "L"
	case "edit_r":
		g.manualEditChannel = "R"
	case "run_auto_scan":
		go g.runPresetSteps("am-sine")
	case "run_burst":
		go g.runPresetSteps("burst")
	case "run_chirp":
		go g.runPresetSteps("chirp")
	case "run_phase_scan":
		go g.runPresetSteps("phase-scan")
	case "run_dual_asym":
		go g.runPresetSteps("dual-asym")
	case "run_fast_scan":
		go g.runPresetSteps("fast-scan")
	case "mode_a":
		go g.runProbingMode("A")
	case "mode_b":
		go g.runProbingMode("B")
	case "mode_c":
		go g.runProbingMode("C")
	case "mode_d":
		go g.runProbingMode("D")
	case "stop":
		g.requestStopRunner()
	case "sync_trigger":
		if g.store != nil {
			if !g.store.DispatchSyncTrigger(800_000) {
				log.Printf("[SYNC] Cannot dispatch: no TSF data from devices yet")
			} else {
				g.protocol = g.store.GetProtocol()
				log.Printf("[SYNC] Trigger dispatched: session=%d targetTsf=%d",
					g.protocol.SyncTrigger.SessionID,
					g.protocol.SyncTrigger.TargetTsfUs)
			}
		}
	case "sync_reset_calib":
		if g.buf != nil {
			g.buf.Mu.Lock()
			g.buf.ResetPhaseCalib()
			g.buf.Mu.Unlock()
			log.Printf("[SYNC] Phase calibration reset")
		}
	}
}
