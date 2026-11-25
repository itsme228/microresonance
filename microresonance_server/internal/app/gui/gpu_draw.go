package gui

import (
	"fmt"
	"image/color"
	"math"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)


func (g *gpuGame) Draw(screen *ebiten.Image) {
	drawStart := time.Now()
	stageStart := drawStart
	b := screen.Bounds()
	w := b.Dx()
	h := b.Dy()
	layout := g.buildPanelLayout(w, h)
	layoutDur := time.Since(stageStart)
	stageStart = time.Now()
	screen.Fill(clrBg)

	sid := g.getSelectedSensorID()
	g.buf.Mu.Lock()
	pkts := g.buf.Packets
	from := g.buf.LastFrom
	g.cachedSyncMetrics = g.buf.ComputeSyncMetrics(float64((*timeWindow).Milliseconds()))

	// Collect dynamics for the DYN screen.
	// Always sample — do NOT gate on PhaseHistory so plots stay live during weak signal.
	if s := g.buf.Sensors[sid]; s != nil && s.LastDsp != nil {
		dsp := s.LastDsp

		// Phase: use carrier phase from resonance analysis (updates per DSP packet).
		rawPhase := float64(dsp.Resonance.CarrierPhaseDeg)
		phaseVal := rawPhase - g.dynPhaseZero
		for phaseVal > 180 {
			phaseVal -= 360
		}
		for phaseVal <= -180 {
			phaseVal += 360
		}
		g.dynHistPhase = append(g.dynHistPhase, phaseVal)

		// SNR and magnitude from latest DSP.
		g.dynHistSNR = append(g.dynHistSNR, float64(dsp.Resonance.CarrierSNR))
		g.dynHistMag = append(g.dynHistMag, float64(dsp.Resonance.CarrierMag))

		// Per-axis lock-in magnitudes and absolute phases.
		g.dynHistMagX = append(g.dynHistMagX, s.LockinXMag)
		g.dynHistMagY = append(g.dynHistMagY, s.LockinYMag)
		phX := s.LockinXPhaseDeg
		phY := s.LockinYPhaseDeg
		phZ := s.LockinPhase * 180.0 / math.Pi
		g.dynHistPhaseX = append(g.dynHistPhaseX, phX)
		g.dynHistPhaseY = append(g.dynHistPhaseY, phY)
		g.dynHistPhaseZ = append(g.dynHistPhaseZ, phZ)

		// All differential phases.
		g.dynHistDeltaXY = append(g.dynHistDeltaXY, s.LockinDeltaXY)
		g.dynHistDeltaYZ = append(g.dynHistDeltaYZ, s.LockinDeltaYZ)
		g.dynHistDeltaZX = append(g.dynHistDeltaZX, -s.LockinDeltaXZ) // Φ_Z − Φ_X

		// Vector magnitude sum.
		magTot := math.Sqrt(s.LockinXMag*s.LockinXMag + s.LockinYMag*s.LockinYMag + s.LockinMag*s.LockinMag)
		g.dynHistMagTotal = append(g.dynHistMagTotal, magTot)

		// Cache hard-lock status for draw (lock-in hard lock ≠ carrier dominant)
		g.dynLockinHardLock = s.LockinHardLock

		// Orbit trails.
		appendOrb := func(sl *[][2]float64, a, b float64) {
			*sl = append(*sl, [2]float64{a, b})
			if len(*sl) > 120 {
				*sl = (*sl)[1:]
			}
		}
		appendOrb(&g.dynOrbitXY, phX, phY)
		appendOrb(&g.dynOrbitYZ, phY, phZ)
		appendOrb(&g.dynOrbitXZ, phX, phZ)
		zPhaseDeg := phZ
		g.dynOrbitTrail = append(g.dynOrbitTrail, struct{ P, M float64 }{zPhaseDeg, s.LockinMag})
		if len(g.dynOrbitTrail) > 180 {
			g.dynOrbitTrail = g.dynOrbitTrail[1:]
		}
	}
	// Velocity and jitter sampled every frame from sync metrics.
	g.dynHistVel = append(g.dynHistVel, g.cachedSyncMetrics.PhaseVelocityDegSec)
	g.dynHistJitter = append(g.dynHistJitter, g.cachedSyncMetrics.JitterUs)

	// Cap history to 5000 points (~83 s at 60fps).
	capHist := func(h *[]float64) {
		if len(*h) > 5000 {
			*h = (*h)[len(*h)-5000:]
		}
	}
	capHist(&g.dynHistPhase)
	capHist(&g.dynHistMag)
	capHist(&g.dynHistMagX)
	capHist(&g.dynHistMagY)
	capHist(&g.dynHistDeltaXY)
	capHist(&g.dynHistDeltaYZ)
	capHist(&g.dynHistDeltaZX)
	capHist(&g.dynHistMagTotal)
	capHist(&g.dynHistPhaseX)
	capHist(&g.dynHistPhaseY)
	capHist(&g.dynHistPhaseZ)
	capHist(&g.dynHistVel)
	capHist(&g.dynHistJitter)
	capHist(&g.dynHistSNR)

	g.buf.Mu.Unlock()
	x, _, _, ads := g.getSelectedSensorData()
	nX := len(x)
	nADS := len(ads)

	fromStr := from
	if fromStr == "" {
		fromStr = "(waiting)"
	}
	ar := *adxlRate
	if ar < 1 {
		ar = 4000
	}
	ad := *adsRate
	if ad < 1 {
		ad = 40000
	}
	aStr := fmt.Sprintf("%d", ar)
	if ar >= 1000 {
		aStr = fmt.Sprintf("%dk", ar/1000)
	}
	dStr := fmt.Sprintf("%d", ad)
	if ad >= 1000 {
		dStr = fmt.Sprintf("%dk", ad/1000)
	}
	effStr := "—"
	latencyStr := ""
	if (nX > 0 || nADS > 0) && (*timeWindow).Seconds() > 0 {
		effStr = fmt.Sprintf("%.0f %.0f", float64(nX)/(*timeWindow).Seconds(), float64(nADS)/(*timeWindow).Seconds())
	} else {
		// Если сырых данных нет, возможно это v2 пакеты — берем задержку оттуда
		sid := g.getSelectedSensorID()
		g.buf.Mu.Lock()
		if s := g.buf.Sensors[sid]; s != nil && s.LastDsp != nil {
			latencyStr = fmt.Sprintf(" | lat=%.0fms", s.LastDsp.TransportLatencyMs)
		}
		g.buf.Mu.Unlock()
	}
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf(" MR | pkts=%d %s%s | ADXL %s ADS %s Hz | receive ≈%s | F Esc ", pkts, fromStr, latencyStr, aStr, dStr, effStr), 4, 2)
	headerDur := time.Since(stageStart)
	stageStart = time.Now()

	mainTabW := 64
	bandsTabW := 56
	xcorrTabW := 52
	mainTabH := mainTabRowH - 2
	tx0 := 4
	ty0 := headerH + 2
	if g.mainTabRects == nil {
		g.mainTabRects = make(map[string]uiRect)
	}
	// Layout tabs with a running x offset for clarity.
	tabX := tx0
	addTab := func(name string, w int) {
		g.mainTabRects[name] = uiRect{tabX, ty0, w, mainTabH}
		tabX += w + 2
	}
	addTab("RAW", mainTabW)
	addTab("DYN", 36)
	addTab("BANDS", bandsTabW)
	addTab("XCORR", xcorrTabW)
	addTab("LOCKIN", 56)
	for _, name := range []string{"RAW", "DYN", "BANDS", "XCORR", "LOCKIN"} {
		r, ok := g.mainTabRects[name]
		if !ok {
			continue
		}
		on := g.mainScreen == name || (g.mainScreen == "" && name == "LOCKIN")
		fill := clrBtn
		if on {
			fill = clrBtnOn
		}
		vector.FillRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), fill, true)
		vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 1, clrGrid, true)
		label := name
		if name == "RAW" {
			label = "FFT"
		}
		if name == "BANDS" {
			label = "RAW"
		}
		if name == "LOCKIN" {
			label = "LOCK-IN"
		}
		ebitenutil.DebugPrintAt(screen, label, r.x+4, r.y+2)
	}
	tabsDur := time.Since(stageStart)
	stageStart = time.Now()

	contentW := w
	if layout.show {
		contentW = layout.x
	}

	if g.mainScreen == "" {
		g.mainScreen = "LOCKIN"
	}
	contentScreen := g.mainScreen
	contentStart := time.Now()
	switch g.mainScreen {
	case "RAW":
		DrawScanScreen(g, screen, contentW, h)
	case "DYN":
		DrawDynamicsScreen(g, screen, contentW, h)
	case "BANDS":
		DrawBandsScreen(g, screen, contentW, h)
	case "XCORR":
		DrawXcorrScreen(g, screen, contentW, h)
	case "LOCKIN":
		DrawLockinScreen(g, screen, contentW, h)
	default:
		DrawLockinScreen(g, screen, contentW, h)
		contentScreen = "LOCKIN"
	}
	contentDur := time.Since(contentStart)
	panelStart := time.Now()

	if layout.show {
		vector.FillRect(screen, float32(layout.x), float32(layout.y), float32(layout.w), float32(layout.h), clrPanel, true)

		drawButton := func(r uiRect, label string, on bool) {
			fill := clrBtn
			if on {
				fill = clrBtnOn
			}
			vector.FillRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), fill, true)
			vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 1, clrGrid, true)
			ebitenutil.DebugPrintAt(screen, label, r.x+4, r.y+3)
		}
		drawSlider := func(r uiRect, label string, v, min, max float64) {
			vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 1, clrGrd, true)
			t := 0.0
			if max > min {
				t = (v - min) / (max - min)
			}
			t = clamp(t, 0, 1)
			knobX := r.x + int(t*float64(r.w-6))
			vector.FillRect(screen, float32(knobX), float32(r.y+2), 6, float32(r.h-4), clrAcc, true)
			ebitenutil.DebugPrintAt(screen, label, r.x+4, r.y-1)
		}

		for name, r := range layout.tabs {
			on := g.panelTab == name || (g.panelTab == "" && name == "EMITTER")
			drawButton(r, name, on)
		}

		switch g.panelTab {
		case "", "EMITTER":
			// Signal type selector
			if b, ok := layout.buttons["signal_sine"]; ok {
				drawButton(b, "SINE", g.protocol.Preset == "manual" || g.protocol.Preset == "")
			}
			if b, ok := layout.buttons["signal_am"]; ok {
				drawButton(b, "AM-SINE", g.protocol.Preset == "am-sine")
			}
			if b, ok := layout.buttons["signal_chirp"]; ok {
				drawButton(b, "CHIRP", g.protocol.Preset == "chirp")
			}
			if b, ok := layout.buttons["freq_extra"]; ok {
				drawButton(b, "Ext Freq", g.manualFreqExtra)
			}
			// Dynamic Tones (up to 10)
			for i := 0; i < len(g.protocol.Tones); i++ {
				freqKey := fmt.Sprintf("freq_%d", i)
				valKey := fmt.Sprintf("freq_val_%d", i)
				delKey := fmt.Sprintf("del_tone_%d", i)
				lEnKey := fmt.Sprintf("tone_l_en_%d", i)
				rEnKey := fmt.Sprintf("tone_r_en_%d", i)
				lLevelKey := fmt.Sprintf("tone_l_level_%d", i)
				rLevelKey := fmt.Sprintf("tone_r_level_%d", i)

				if b, ok := layout.buttons[lEnKey]; ok {
				        drawButton(b, "L", g.protocol.Tones[i].LeftEnabled)
				}
				if b, ok := layout.buttons[rEnKey]; ok {
					drawButton(b, "R", g.protocol.Tones[i].RightEnabled)
				}
				if sl, ok := layout.sliders[freqKey]; ok {
					f := g.protocol.Tones[i].Freq
					drawSlider(sl.rect, fmt.Sprintf("F%d %.2f Hz", i+1, f), f, sl.min, sl.max)
				}
				if r, ok := layout.inputs[valKey]; ok {
					vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 1, clrGrid, true)
					var txt string
					if g.manualFreqInputFocused && g.manualFreqInputIdx == i {
						txt = g.manualFreqInput + "|"
						vector.FillRect(screen, float32(r.x+1), float32(r.y+1), float32(r.w-2), float32(r.h-2), color.RGBA{40, 40, 60, 255}, true)
					} else {
						txt = fmt.Sprintf("%.2f", g.protocol.Tones[i].Freq)
					}
					ebitenutil.DebugPrintAt(screen, txt, r.x+4, r.y+2)
				}
				if b, ok := layout.buttons[delKey]; ok {
					drawButton(b, "DEL", false)
				}
				if sl, ok := layout.sliders[lLevelKey]; ok {
					drawSlider(sl.rect, fmt.Sprintf("L %d%%", int(g.protocol.Tones[i].LeftLevel)), g.protocol.Tones[i].LeftLevel, 0, 100)
				}
				if sl, ok := layout.sliders[rLevelKey]; ok {
					drawSlider(sl.rect, fmt.Sprintf("R %d%%", int(g.protocol.Tones[i].RightLevel)), g.protocol.Tones[i].RightLevel, 0, 100)
				}
			}
			if b, ok := layout.buttons["add_tone"]; ok {
				drawButton(b, "+ ADD TONE", false)
			}
			if sl, ok := layout.sliders["phase_l"]; ok {
				drawSlider(sl.rect, fmt.Sprintf("Ph L %d°", g.protocol.PhaseL), float64(g.protocol.PhaseL), sl.min, sl.max)
			}
			if sl, ok := layout.sliders["phase_r"]; ok {
				drawSlider(sl.rect, fmt.Sprintf("Ph R (Δ) %d°", g.protocol.PhaseOffset), float64(g.protocol.PhaseOffset), sl.min, sl.max)
			}
			if b, ok := layout.buttons["open"]; ok {
				drawButton(b, "ON", g.protocol.SessionState == "running")
			}

		case "RECEIVER":
			if b, ok := layout.buttons["receiver_linked"]; ok {
				drawButton(b, "Linked to Emitter", g.protocol.ReceiverLinked)
			}
			if sl, ok := layout.sliders["receiver_freq"]; ok {
				f := g.protocol.ReceiverFreq
				drawSlider(sl.rect, fmt.Sprintf("RX %.2f Hz", f), f, sl.min, sl.max)
			}
			if r, ok := layout.inputs["receiver_freq_val"]; ok {
				vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 1, clrGrid, true)
				var txt string
				if g.textFocus == "receiver_freq_val" {
					txt = g.manualFreqInput + "|"
					vector.FillRect(screen, float32(r.x+1), float32(r.y+1), float32(r.w-2), float32(r.h-2), color.RGBA{40, 40, 60, 255}, true)
				} else {
					txt = fmt.Sprintf("%.2f", g.protocol.ReceiverFreq)
				}
				ebitenutil.DebugPrintAt(screen, txt, r.x+4, r.y+2)
			}
			g.drawSyncPanel(screen, layout)
			if b, ok := layout.buttons["run_auto_scan"]; ok {
				drawButton(b, "AM-SINE (Discovery)", g.protocol.AutoPresetName == "am-sine" && g.protocol.SessionState == "running")
			}
			if b, ok := layout.buttons["run_burst"]; ok {
				drawButton(b, "BURST", g.protocol.AutoPresetName == "burst" && g.protocol.SessionState == "running")
			}
			if b, ok := layout.buttons["run_chirp"]; ok {
				drawButton(b, "CHIRP", g.protocol.AutoPresetName == "chirp" && g.protocol.SessionState == "running")
			}
			if b, ok := layout.buttons["run_phase_scan"]; ok {
				drawButton(b, "PHASE-SCAN", g.protocol.AutoPresetName == "phase-scan" && g.protocol.SessionState == "running")
			}
			if b, ok := layout.buttons["run_dual_asym"]; ok {
				drawButton(b, "DUAL-ASY", g.protocol.AutoPresetName == "dual-asym" && g.protocol.SessionState == "running")
			}
			if b, ok := layout.buttons["run_fast_scan"]; ok {
				drawButton(b, "FAST-SCAN (debug)", g.protocol.AutoPresetName == "fast-scan" && g.protocol.SessionState == "running")
			}
			if b, ok := layout.buttons["mode_a"]; ok {
				drawButton(b, "A", g.protocol.AutoPresetName == "mode-a" && g.protocol.SessionState == "running")
			}
			if b, ok := layout.buttons["mode_b"]; ok {
				drawButton(b, "B", g.protocol.AutoPresetName == "mode-b" && g.protocol.SessionState == "running")
			}
			if b, ok := layout.buttons["mode_c"]; ok {
				drawButton(b, "C", g.protocol.AutoPresetName == "mode-c" && g.protocol.SessionState == "running")
			}
			if b, ok := layout.buttons["mode_d"]; ok {
				drawButton(b, "D", g.protocol.AutoPresetName == "mode-d" && g.protocol.SessionState == "running")
			}
			if b, ok := layout.buttons["stop"]; ok {
				drawButton(b, "STOP", false)
			}
			if b, ok := layout.buttons["panic"]; ok {
				drawButton(b, "PANIC", g.protocol.PanicStop)
			}
		}
	}
	panelDur := time.Since(panelStart)
	totalDur := time.Since(drawStart)
	_ = stageStart
	g.perfDrawFrames++
	g.perfDrawTotal += totalDur
	g.perfDrawLayout += layoutDur
	g.perfDrawHeader += headerDur
	g.perfDrawTabs += tabsDur
	g.perfDrawContent += contentDur
	g.perfDrawPanel += panelDur
	if totalDur >= 33*time.Millisecond {
		g.perfDrawSlow33++
	}
	switch contentScreen {
	case "RAW":
		g.perfDrawRAW += contentDur
	case "BANDS":
		g.perfDrawBANDS += contentDur
	case "XCORR":
		g.perfDrawXCORR += contentDur
	}
}

// drawSyncPanel рисует содержимое вкладки SYNC: TSF-статус, метрики фазы, кнопки.
func (g *gpuGame) drawSyncPanel(screen *ebiten.Image, layout panelLayout) {
	drawButton := func(r uiRect, label string, on bool) {
		fill := clrBtn
		if on {
			fill = clrBtnOn
		}
		vector.FillRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), fill, true)
		vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 1, clrGrid, true)
		ebitenutil.DebugPrintAt(screen, label, r.x+4, r.y+3)
	}

	if b, ok := layout.buttons["sync_trigger"]; ok {
		drawButton(b, "FIRE SYNC TRIGGER", false)
	}
	if b, ok := layout.buttons["sync_reset_calib"]; ok {
		drawButton(b, "RESET PHASE CALIB", false)
	}

	// TSF-статус
	if b, ok := layout.buttons["sync_tsf_status"]; ok {
		tsfOffset := int64(0)
		if g.store != nil {
			tsfOffset = g.store.GetTsfOffsetUs()
		}
		var line1, line2, line3 string
		if tsfOffset == 0 {
			line1 = "TSF: NO DATA (waiting device poll)"
			line2 = "Connect device + wait for HTTP poll"
			line3 = "X-Device-TSF header needed"
		} else {
			estTsfUs := time.Now().UnixMicro() + tsfOffset
			line1 = fmt.Sprintf("TSF offset: %+.3f s", float64(tsfOffset)/1e6)
			line2 = fmt.Sprintf("Est. TSF:   %.3f s", float64(estTsfUs)/1e6)
			line3 = "WiFi beacon sync (target <2 us)"
		}
		vector.StrokeRect(screen, float32(b.x), float32(b.y), float32(b.w), float32(b.h), 1, clrGrd, true)
		ebitenutil.DebugPrintAt(screen, "--- TSF Status ---", b.x+2, b.y+1)
		ebitenutil.DebugPrintAt(screen, line1, b.x+2, b.y+14)
		ebitenutil.DebugPrintAt(screen, line2, b.x+2, b.y+27)
		ebitenutil.DebugPrintAt(screen, line3, b.x+2, b.y+40)
	}

	// Метрики синхронизации фазы
	if b, ok := layout.buttons["sync_phase_metrics"]; ok {
		m := g.cachedSyncMetrics
		calib := "NO"
		if m.PhaseCalibrated {
			calib = fmt.Sprintf("YES %.1f deg", m.PhaseCalibOffsetDeg)
		}
		stability := "—"
		if m.PhaseStabilityDeg > 0 {
			if m.PhaseStabilityDeg < 5 {
				stability = fmt.Sprintf("%.2f deg STABLE", m.PhaseStabilityDeg)
			} else {
				stability = fmt.Sprintf("%.2f deg UNSTABLE", m.PhaseStabilityDeg)
			}
		}
		vector.StrokeRect(screen, float32(b.x), float32(b.y), float32(b.w), float32(b.h), 1, clrGrd, true)
		ebitenutil.DebugPrintAt(screen, "--- Sync Metrics ---", b.x+2, b.y+1)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("DeltaPhi: %+.1f deg  calib: %s", m.DeltaPhiDeg, calib), b.x+2, b.y+14)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Stability: %s", stability), b.x+2, b.y+27)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Latency:  %.0f ms", m.TransportLatencyMs), b.x+2, b.y+40)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("TSF Drift: %.1f us", m.SyncDriftUs), b.x+2, b.y+53)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("L+D: %.0fms / %.1fus", m.TransportLatencyMs, m.SyncDriftUs), b.x+2, b.y+66)
	}

	// Статус датчиков
	if b, ok := layout.buttons["sync_sensors_disp"]; ok {
		sensors := g.buf.GetActiveSensors()
		vector.StrokeRect(screen, float32(b.x), float32(b.y), float32(b.w), float32(b.h), 1, clrGrd, true)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("--- Sensors (%d) ---", len(sensors)), b.x+2, b.y+1)
		y := b.y + 14
		for i, s := range sensors {
			if i >= 3 {
				break
			}
			ebitenutil.DebugPrintAt(screen,
				fmt.Sprintf("S%d snr=%.1fdB mag=%.4fg age=%dms", s.ID, s.SNR, s.Magnitude, s.AgeMs),
				b.x+2, y)
			y += 13
		}
		if len(sensors) == 0 {
			ebitenutil.DebugPrintAt(screen, "No sensors connected", b.x+2, b.y+14)
		}
	}
}
