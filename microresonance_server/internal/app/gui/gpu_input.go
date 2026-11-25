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
				if name == "ALPHABET" {
					g.textFocus = ""
					g.reactionSelectedIndex = -1
					g.lockedSelectedPatternID = -1
					g.lockedCompareIDs = [2]int{-1, -1}
				}
				return // Обработали клик по вкладке
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
	case "ALPHABET":
		if g.lockedSelectedPatternID >= 0 {
			if clicked {
				if g.lockedPopupCloseRect.contains(mx, my) {
					g.lockedSelectedPatternID = -1
					return
				}
				if g.lockedPopupStopRect.contains(mx, my) {
					g.lockedPopupAnimStop = !g.lockedPopupAnimStop
					return
				}
			}
			if mouseDown && g.lockedPopupSliderRect.contains(mx, my) {
				r := g.lockedPopupSliderRect
				v := float64(mx-r.x) / float64(r.w)
				if v < 0 {
					v = 0
				}
				if v > 1 {
					v = 1
				}
				g.lockedPopupFrameSlider = v
				g.lockedPopupFrames = 1 + int(v*float64(maxLastNShapes-1)) // 1..256
			}
			// If popup is open, don't process other ALPHABET clicks
			if clicked || mouseDown {
				return
			}
		}
		if clicked {
			if g.alphabetScrollbarWasDrag {
				g.alphabetScrollbarWasDrag = false
			} else {
				ctrlHeld := ebiten.IsKeyPressed(ebiten.KeyControl) || ebiten.IsKeyPressed(ebiten.KeyControlLeft)
				cardHit := false
				// Реакции
				for _, rc := range g.reactionCardRects {
					if rc.rect.contains(mx, my) {
						cardHit = true
						if g.reactionSelectedIndex == rc.index {
							g.reactionSelectedIndex = -1
						} else {
							g.reactionSelectedIndex = rc.index
							g.reactionPopupX, g.reactionPopupY = mx, my
						}
						g.lockedSelectedPatternID = -1
						g.lockedCompareIDs[0], g.lockedCompareIDs[1] = -1, -1
						break
					}
				}
				if !cardHit {
					// Токены
					for _, cr := range g.lockedCardRects {
						if cr.rect.contains(mx, my) {
							cardHit = true
							g.reactionSelectedIndex = -1
							if ctrlHeld {
								// Compare mode
								// ... simplify for now, can restore later
							} else {
								if cr.patternID == g.lockedSelectedPatternID {
									g.lockedSelectedPatternID = -1
								} else {
									g.lockedSelectedPatternID = cr.patternID
								}
								g.lockedCompareIDs[0], g.lockedCompareIDs[1] = -1, -1
							}
							break
						}
					}
				}
				if !cardHit {
					// Checkboxes and toggles
					if g.alphabetOrderByTopRect.w > 0 && g.alphabetOrderByTopRect.contains(mx, my) {
						g.alphabetOrderByTop = !g.alphabetOrderByTop
						g.alphabetTopOrderNextRefresh = time.Time{}
					} else if g.reactionFilterRect.w > 0 && g.reactionFilterRect.contains(mx, my) {
						// x>=N filter cycle
						switch g.reactionMinOccurrences {
						case 0:
							g.reactionMinOccurrences = 3
						case 3:
							g.reactionMinOccurrences = 5
						case 5:
							g.reactionMinOccurrences = 10
						default:
							g.reactionMinOccurrences = 0
						}
					} else if g.alphabetViewCombinedRect.contains(mx, my) {
						g.alphabetViewAxes = false
					} else if g.alphabetViewAxesRect.contains(mx, my) {
						g.alphabetViewAxes = true
						g.alphabetAxesPhasePortrait = true
					} else if g.alphabetAxesPortraitRect.contains(mx, my) {
						g.alphabetAxesPhasePortrait = true
					} else if g.alphabetAxesWaveformRect.contains(mx, my) {
						g.alphabetAxesPhasePortrait = false
					}
				}
			}
		}
		// Scrolling
		const scrollStep = 48
		if _, wy := ebiten.Wheel(); wy != 0 {
			g.lockedScrollY += int(wy * scrollStep)
		}
		if g.lockedScrollY < 0 {
			g.lockedScrollY = 0
		}

		// Scrollbar drag
		if g.alphabetScrollbarRect.w > 0 {
			if !mouseDown {
				if g.alphabetScrollbarDrag {
					g.alphabetScrollbarWasDrag = true
				}
				g.alphabetScrollbarDrag = false
			} else if g.alphabetScrollbarDrag {
				r := g.alphabetScrollbarRect
				scrollRatio := float64(my-r.y) / float64(r.h)
				if scrollRatio < 0 {
					scrollRatio = 0
				}
				if scrollRatio > 1 {
					scrollRatio = 1
				}
				maxSc := g.alphabetScrollbarMaxScroll
				if maxSc > 0 {
					g.lockedScrollY = int(scrollRatio * float64(maxSc))
				}
			} else if g.alphabetScrollbarRect.contains(mx, my) && mouseDown {
				g.alphabetScrollbarDrag = true
			}
		}
	case "TRAINER":
		if clicked {
			for _, ar := range g.trainerActionRects {
				if ar.rect.contains(mx, my) {
					g.trainerSelectedActionID = ar.actionID
					g.inputCooldown = 15
					// Add to sequence if not calibrating
					if g.trainerCalibrateLoop == nil && g.trainerActionStore != nil {
						seq := g.trainerPlannedSequence
						if len(seq) == 0 || seq[len(seq)-1] != ar.actionID {
							g.trainerPlannedSequence = append(append([]int(nil), seq...), ar.actionID)
						}
					}
					break
				}
			}
			// Buttons
			if g.trainerRects != nil {
				if r, ok := g.trainerRects["newAction"]; ok && r.contains(mx, my) {
					g.inputCooldown = 20
					if g.trainerActionStore != nil {
						n := len(g.trainerActionStore.List()) + 1
						name := fmt.Sprintf("action_%d", n)
						a := g.trainerActionStore.Add(name, "")
						g.trainerSelectedActionID = a.ID
					}
				} else if r, ok := g.trainerRects["clearSequence"]; ok && r.contains(mx, my) {
					g.inputCooldown = 15
					g.trainerPlannedSequence = nil
				} else if r, ok := g.trainerRects["stop"]; ok && r.contains(mx, my) {
					g.inputCooldown = 15
					g.requestStopRunner()
				} else if r, ok := g.trainerRects["avn_toggle"]; ok && r.contains(mx, my) {
					g.inputCooldown = 15
					g.avn.Enabled = !g.avn.Enabled
				} else if r, ok := g.trainerRects["avn_reset"]; ok && r.contains(mx, my) {
					g.inputCooldown = 20
					g.avn.Calibration = avnCalibration{}
					g.avn.Counts = make(map[string]int)
					g.avn.Enabled = false
					g.avn.Recording = ""
					log.Printf("[AVN] Calibration reset by user")
				} else {
					// AVN Recording Steps
					steps := []string{"REST", "UP", "DOWN", "LEFT", "RIGHT"}
					for _, step := range steps {
						if r, ok := g.trainerRects["avn_rec_"+step]; ok && r.contains(mx, my) {
							g.inputCooldown = 20
							if g.avn.Recording == step {
								g.avnProcessRecording(step)
								g.avn.Recording = ""
							} else {
								g.avn.Recording = step
								g.avn.RecBuffer = nil
								g.avn.RecVel = nil
							}
							return
						}
					}
				}
				// ... more trainer buttons if needed
			}
		}
	case "MEANING":
		if clicked && g.meaningRects != nil {
			if r, ok := g.meaningRects["meaningReset"]; ok && r.contains(mx, my) {
				g.meaningResetCounters()
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

	case "TOMO":
		if clicked {
			for partName, r := range g.tomoPartsRects {
				if r.contains(mx, my) {
					g.inputCooldown = 15

					if _, ok := g.tomoSensorMap[partName]; ok {
						// Already bound -> Unbind
						delete(g.tomoSensorMap, partName)
						log.Printf("[TOMO] Unbound %s", partName)
					} else {
						// Not bound -> Find first available unassigned sensor
						assigned := make(map[int]bool)
						for _, s := range g.tomoSensorMap {
							assigned[s] = true
						}

						ids := g.buf.ListSensors()
						boundAny := false
						for _, sid := range ids {
							if !assigned[int(sid)] {
								g.tomoSensorMap[partName] = int(sid)
								log.Printf("[TOMO] Auto-bound S%d to %s", sid, partName)
								boundAny = true
								break
							}
						}
						if !boundAny {
							log.Printf("[TOMO] No free sensors to bind to %s", partName)
						}
					}

					// Persist
					p := g.store.GetProtocol()
					p.TomoSensorMap = make(map[string]int)
					for k, v := range g.tomoSensorMap {
						p.TomoSensorMap[k] = v
					}
					g.store.SetProtocol(p)
					break
				}
			}
			if modeRect, ok := g.tomoPartsRects["mode_toggle"]; ok && modeRect.contains(mx, my) {
				g.inputCooldown = 15
				if g.tomoViewMode == "Quantum" {
					g.tomoViewMode = "Circular"
				} else {
					g.tomoViewMode = "Quantum"
				}
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
	case "h_thr":
		g.shapeParams.HThr = val
	case "min_delta_h":
		g.shapeParams.MinDeltaH = val
	case "dist_thr":
		g.distThr = val
		g.shapeParams.DistThr = val
		if g.shapeClusterStore != nil {
			g.shapeClusterStore.SetDistThr(val)
		}
	case "min_cos":
		g.shapeParams.MinCos = val
	case "max_freq_ratio":
		g.shapeParams.MaxFreqRatio = val
	case "adjacent_dist_thr":
		g.shapeParams.AdjacentDistThr = val
	case "hysteresis_margin":
		g.shapeParams.HysteresisMargin = val
	case "ultra_strong_dist":
		g.shapeParams.UltraStrongDist = val
	case "glyph_ttl_sec":
		g.glyphTTLSec = int(val)
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
	case "x_ge":
		switch g.reactionMinOccurrences {
		case 0:
			g.reactionMinOccurrences = 3
		case 3:
			g.reactionMinOccurrences = 5
		case 5:
			g.reactionMinOccurrences = 10
		default:
			g.reactionMinOccurrences = 0
		}
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
	case "token_history_toggle":
		g.tokenTrendEnabled = !g.tokenTrendEnabled
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
