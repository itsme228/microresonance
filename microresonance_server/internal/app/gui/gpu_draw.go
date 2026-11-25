package gui

import (
	"fmt"
	"image/color"
	"log"
	"math"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// occurrenceHistogramBuckets: 1, 2-4, 5-9, 10-99, 100+
const occurrenceHistogramBuckets = 5

func occurrenceBucket(occ int) int {
	switch {
	case occ == 1:
		return 0
	case occ <= 4:
		return 1
	case occ <= 9:
		return 2
	case occ <= 99:
		return 3
	default:
		return 4
	}
}

func (g *gpuGame) drawTokenHistogram(screen *ebiten.Image, r uiRect) {
	var hist [occurrenceHistogramBuckets]int
	for _, sid := range g.buf.ListSensors() {
		ps := g.getPatternStore(int(sid))
		if ps == nil {
			continue
		}
		for _, p := range ps.ListOrderedByTop() {
			b := occurrenceBucket(p.Occurrences)
			if b >= 0 && b < occurrenceHistogramBuckets {
				hist[b]++
			}
		}
	}
	labels := []string{"1", "2-4", "5-9", "10-99", "100+"}
	titleH := 10
	barH := r.h - titleH - 12
	if barH < 4 {
		barH = 4
	}
	barY := r.y + titleH
	maxCount := 1
	for _, c := range hist {
		if c > maxCount {
			maxCount = c
		}
	}
	logMax := math.Log10(float64(maxCount) + 1)
	if logMax < 0.1 {
		logMax = 0.1
	}
	barW := (r.w - 4) / occurrenceHistogramBuckets
	if barW < 4 {
		barW = 4
	}
	gap := 2
	ebitenutil.DebugPrintAt(screen, "Tokens x repeats (log)", r.x, r.y)
	for i := 0; i < occurrenceHistogramBuckets; i++ {
		c := hist[i]
		logC := math.Log10(float64(c) + 1)
		h := int(logC / logMax * float64(barH))
		if h < 0 {
			h = 0
		}
		if h > barH {
			h = barH
		}
		x := r.x + 2 + i*(barW+gap)
		y := barY + barH - h
		vector.FillRect(screen, float32(x), float32(y), float32(barW), float32(h), clrAcc, true)
		vector.StrokeRect(screen, float32(x), float32(y), float32(barW), float32(h), 1, clrGrid, true)
		ebitenutil.DebugPrintAt(screen, labels[i], x, barY+barH+1)
		if c > 0 {
			ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%d", c), x+barW/2-4, y+1)
		}
	}
}

func (g *gpuGame) drawTokenTrend(screen *ebiten.Image, r uiRect) {
	ebitenutil.DebugPrintAt(screen, "Tokens over time", r.x, r.y)
	if len(g.tokenHistory) == 0 {
		ebitenutil.DebugPrintAt(screen, "No samples yet", r.x+4, r.y+16)
		return
	}
	plotX := r.x + 2
	plotY := r.y + 12
	plotW := r.w - 4
	plotH := r.h - 16
	if plotW < 4 || plotH < 4 {
		return
	}
	vector.StrokeRect(screen, float32(plotX), float32(plotY), float32(plotW), float32(plotH), 1, clrGrid, true)

	minV := g.tokenHistory[0]
	maxV := g.tokenHistory[0]
	for i := 1; i < len(g.tokenHistory); i++ {
		v := g.tokenHistory[i]
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	span := maxV - minV
	if span < 1 {
		span = 1
	}

	n := len(g.tokenHistory)
	if n == 1 {
		y := plotY + plotH - 1
		vector.FillRect(screen, float32(plotX), float32(y), float32(plotW), 1, clrAcc, true)
	} else {
		stepIdx := 1
		if n-1 > plotW {
			stepIdx = int(math.Ceil(float64(n-1) / float64(plotW)))
		}
		prevI := 0
		prevX := plotX
		prevY := plotY + plotH - 1 - int(float64(g.tokenHistory[0]-minV)/float64(span)*float64(plotH-1))
		for i := stepIdx; i < n; i += stepIdx {
			x := plotX + int(float64(i)/float64(n-1)*float64(plotW-1))
			y := plotY + plotH - 1 - int(float64(g.tokenHistory[i]-minV)/float64(span)*float64(plotH-1))
			if x <= prevX && i != n-1 {
				continue
			}
			vector.StrokeLine(screen, float32(prevX), float32(prevY), float32(x), float32(y), 1.5, clrAcc, true)
			prevX = x
			prevY = y
			prevI = i
		}
		if prevI != n-1 {
			x := plotX + plotW - 1
			y := plotY + plotH - 1 - int(float64(g.tokenHistory[n-1]-minV)/float64(span)*float64(plotH-1))
			vector.StrokeLine(screen, float32(prevX), float32(prevY), float32(x), float32(y), 1.5, clrAcc, true)
		}
	}

	totalSec := 0
	if g.tokenHistoryStrideSec > 0 {
		totalSec = (len(g.tokenHistory) - 1) * g.tokenHistoryStrideSec
	}
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%ds -> now", totalSec), plotX+2, plotY+1)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("N %d..%d", minV, maxV), plotX+plotW-72, plotY+1)
}

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

	// AVN (Acoustic Vector Navigation) Logic
	if s := g.buf.Sensors[sid]; s != nil && s.LastDsp != nil {
		dsp := s.LastDsp
		liveV := [3]float64{s.LockinDeltaXY, s.LockinDeltaYZ, -s.LockinDeltaXZ}
		liveVel := g.cachedSyncMetrics.PhaseVelocityDegSec

		// 1. Record data if recording
		if g.avn.Recording != "" {
			g.avn.RecBuffer = append(g.avn.RecBuffer, liveV)
			g.avn.RecVel = append(g.avn.RecVel, liveVel)
		}

		// 2. Real-time Mapping
		if g.avn.Enabled && g.avn.Calibration.MaxDelta > 0 {
			cal := g.avn.Calibration

			// Quality Gate: If SNR is too low, we can't trust phases. Fade to center.
			snr := dsp.Resonance.CarrierSNR
			trust := 1.0
			if snr < 4.0 {
				trust = 0.0
			} else if snr < 10.0 {
				trust = float64(snr-4.0) / 6.0
			}

			dv := [3]float64{
				(liveV[0] - cal.Rest[0]) * trust,
				(liveV[1] - cal.Rest[1]) * trust,
				(liveV[2] - cal.Rest[2]) * trust,
			}

			dot := func(a, b [3]float64) float64 {
				return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
			}

			vU := [3]float64{cal.Up[0] - cal.Rest[0], cal.Up[1] - cal.Rest[1], cal.Up[2] - cal.Rest[2]}
			vD := [3]float64{cal.Down[0] - cal.Rest[0], cal.Down[1] - cal.Rest[1], cal.Down[2] - cal.Rest[2]}
			vL := [3]float64{cal.Left[0] - cal.Rest[0], cal.Left[1] - cal.Rest[1], cal.Left[2] - cal.Rest[2]}
			vR := [3]float64{cal.Right[0] - cal.Rest[0], cal.Right[1] - cal.Rest[1], cal.Right[2] - cal.Rest[2]}

			rawX := dot(dv, vR) - dot(dv, vL)
			rawY := dot(dv, vU) - dot(dv, vD)

			norm := cal.MaxDelta
			if norm < 0.1 {
				norm = 1.0
			}

			nx := (rawX / norm)
			ny := (rawY / norm)

			tf := func(v float64) float64 {
				sign := 1.0
				if v < 0 {
					sign = -1.0
					v = -v
				}
				if v > 1.0 {
					v = 1.0
				}
				return sign * (v * v) // Exponential transfer
			}

			targetX := tf(nx)
			targetY := tf(ny)

			// EMA Smoothing (alpha=0.15 for smooth drift)
			const alpha = 0.15
			g.avn.Live.SmoothX = g.avn.Live.SmoothX*(1-alpha) + targetX*alpha
			g.avn.Live.SmoothY = g.avn.Live.SmoothY*(1-alpha) + targetY*alpha

			g.avn.Live.X = g.avn.Live.SmoothX
			g.avn.Live.Y = g.avn.Live.SmoothY
			g.avn.Live.RawX = nx
			g.avn.Live.RawY = ny
			g.avn.Live.Vel = liveVel
			g.avn.Live.IsClick = math.Abs(liveVel) > cal.ClickVel && trust > 0.5

			// Update Trail
			g.avn.LiveTrail = append(g.avn.LiveTrail, [2]float64{g.avn.Live.X, g.avn.Live.Y})
			if len(g.avn.LiveTrail) > 30 {
				g.avn.LiveTrail = g.avn.LiveTrail[1:]
			}

			// ... Periodic Logging ...
			if time.Since(g.avn.Live.LastLog) > time.Second {
				log.Printf("[AVN-LIVE] X:%+.2f Y:%+.2f (raw: %+.2f, %+.2f) SNR:%.1f Trust:%.1f",
					g.avn.Live.X, g.avn.Live.Y, nx, ny, snr, trust)
				g.avn.Live.LastLog = time.Now()
			}
		} else {
			g.avn.LiveTrail = nil
			g.avn.Live.X = 0
			g.avn.Live.Y = 0
			g.avn.Live.SmoothX = 0
			g.avn.Live.SmoothY = 0
		}
	}

	// Update TOMO RPG Mechanics
	if g.mainScreen == "TOMO" {
		if g.tomoActiveBuffs == nil {
			g.tomoActiveBuffs = make(map[string]float64)
		}

		// 1. Calculate Charge and Balance (RPG Physics)
		magSum := 0.0
		magDiff := 0.0
		bestSNR := -20.0

		mL := g.buf.Sensors[uint8(g.tomoSensorMap["LeftLeg"])]
		mR := g.buf.Sensors[uint8(g.tomoSensorMap["RightLeg"])]

		if mL != nil && mR != nil {
			magSum = mL.LockinMag + mR.LockinMag
			magDiff = (mL.LockinMag - mR.LockinMag) / (magSum + 1e-9)
			g.tomoLiveTwist = (mL.LockinPhase - mR.LockinPhase) * 180 / math.Pi
		}

		for _, s := range g.buf.Sensors {
			if s.LastDsp != nil && float64(s.LastDsp.Resonance.CarrierSNR) > bestSNR {
				bestSNR = float64(s.LastDsp.Resonance.CarrierSNR)
			}
		}

		// Map to 2D field
		targetX := magDiff * 1.5 // Sensitivity boost
		targetY := (magSum / 0.04) - 0.5

		const alpha = 0.1
		g.tomoLiveSmoothX = g.tomoLiveSmoothX*(1-alpha) + targetX*alpha
		g.tomoLiveSmoothY = g.tomoLiveSmoothY*(1-alpha) + targetY*alpha

		// Fireball casting speed (dependent on power)
		targetCharge := math.Max(0, math.Min(1, magSum/0.05))
		if targetCharge > 0.1 {
			g.tomoCastLevel = g.tomoCastLevel*0.97 + targetCharge*0.03
		} else {
			g.tomoCastLevel *= 0.95 // Decay
		}

		// 2. Assign Buffs
		updateBuff := func(name string, active bool) {
			if active {
				g.tomoActiveBuffs[name] = g.tomoActiveBuffs[name]*0.9 + 0.1
			} else {
				g.tomoActiveBuffs[name] *= 0.95
			}
		}
		updateBuff("POWER", bestSNR > 12.0)
		updateBuff("STABILITY", math.Abs(g.cachedSyncMetrics.PhaseVelocityDegSec) < 30.0)
		updateBuff("SYNC", math.Abs(g.tomoLiveBalanceX) < 15.0 && magSum > 0.01)
		updateBuff("RESONANCE", g.tomoCastLevel > 0.8)
	}

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
	addTab("DISCOVERY", mainTabW+10)
	addTab("ALPHABET", mainTabW+8)
	addTab("TRAINER", 56)
	addTab("MEANING", 52)
	addTab("TOMO", 48)
	addTab("LOCKIN", 56)
	for _, name := range []string{"RAW", "DYN", "BANDS", "XCORR", "DISCOVERY", "ALPHABET", "TRAINER", "MEANING", "TOMO", "LOCKIN"} {
		r, ok := g.mainTabRects[name]
		if !ok {
			continue
		}
		on := g.mainScreen == name || (g.mainScreen == "" && name == "RAW")
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
		if name == "TRAINER" {
			label = "TRAIN"
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
		g.mainScreen = "RAW"
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
	case "DISCOVERY":
		DrawDiscoveryScreen(g, screen, contentW, h)
	case "ALPHABET":
		DrawAlphabetScreen(g, screen, contentW, h)
	case "TRAINER":
		DrawTrainerScreen(g, screen, contentW, h)
	case "MEANING":
		DrawMeaningScreen(g, screen, contentW, h)
	case "TOMO":
		DrawTomographyScreen(g, screen, contentW, h)
	case "LOCKIN":
		DrawLockinScreen(g, screen, contentW, h)
	default:
		DrawScanScreen(g, screen, contentW, h)
		contentScreen = "RAW"
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

		drawEntropySection := func() {
			if b, ok := layout.buttons["entropy_sep"]; ok {
				ebitenutil.DebugPrintAt(screen, "--- Entropy / Alphabet ---", b.x, b.y+2)
			}
			if sl, ok := layout.sliders["h_thr"]; ok {
				drawSlider(sl.rect, fmt.Sprintf("H_thr %.2f", g.shapeParams.HThr), g.shapeParams.HThr, sl.min, sl.max)
			}
			if sl, ok := layout.sliders["min_delta_h"]; ok {
				drawSlider(sl.rect, fmt.Sprintf("minΔH %.4f", g.shapeParams.MinDeltaH), g.shapeParams.MinDeltaH, sl.min, sl.max)
			}
			if sl, ok := layout.sliders["dist_thr"]; ok {
				drawSlider(sl.rect, fmt.Sprintf("dist %.2f", g.distThr), g.distThr, sl.min, sl.max)
			}
			if sl, ok := layout.sliders["min_cos"]; ok {
				drawSlider(sl.rect, fmt.Sprintf("minCos %.2f", g.shapeParams.MinCos), g.shapeParams.MinCos, sl.min, sl.max)
			}
			if sl, ok := layout.sliders["max_freq_ratio"]; ok {
				drawSlider(sl.rect, fmt.Sprintf("fRatio %.2f", g.shapeParams.MaxFreqRatio), g.shapeParams.MaxFreqRatio, sl.min, sl.max)
			}
			if sl, ok := layout.sliders["zoom_x"]; ok {
				drawSlider(sl.rect, fmt.Sprintf("Zoom X %.1f", g.zoomX), g.zoomX, sl.min, sl.max)
			}
			if sl, ok := layout.sliders["zoom_y"]; ok {
				drawSlider(sl.rect, fmt.Sprintf("Zoom Y %.1f", g.zoomY), g.zoomY, sl.min, sl.max)
			}
			if sl, ok := layout.sliders["adjacent_dist_thr"]; ok {
				drawSlider(sl.rect, fmt.Sprintf("adjDist %.2f", g.shapeParams.AdjacentDistThr), g.shapeParams.AdjacentDistThr, sl.min, sl.max)
			}
			if sl, ok := layout.sliders["hysteresis_margin"]; ok {
				drawSlider(sl.rect, fmt.Sprintf("hyst %.2f", g.shapeParams.HysteresisMargin), g.shapeParams.HysteresisMargin, sl.min, sl.max)
			}
			if sl, ok := layout.sliders["ultra_strong_dist"]; ok {
				drawSlider(sl.rect, fmt.Sprintf("ultra %.3f", g.shapeParams.UltraStrongDist), g.shapeParams.UltraStrongDist, sl.min, sl.max)
			}
			if b, ok := layout.buttons["x_ge"]; ok {
				lbl := fmt.Sprintf("x≥%d", g.reactionMinOccurrences)
				drawButton(b, lbl, false)
			}
			if sl, ok := layout.sliders["glyph_ttl_sec"]; ok {
				lbl := "TTL inf"
				val := float64(g.glyphTTLSec)
				if g.glyphTTLSec > 0 {
					lbl = fmt.Sprintf("TTL %dm%02ds", g.glyphTTLSec/60, g.glyphTTLSec%60)
				}
				drawSlider(sl.rect, lbl, val, sl.min, sl.max)
			}
			if b, ok := layout.buttons["entropy_metrics"]; ok {
				_, _, hFinal, _ := g.residualDecomposer.LastResult()
				peaksT, peaksC, peaksR := 0, 0, 0
				if g.runStats != nil {
					peaksT, peaksC, peaksR = g.runStats.PeaksTotal, g.runStats.PeaksClassified, g.runStats.PeaksRejectedStationarity
				}
				tokensTotal := g.totalTokensAcrossSensors()
				ebitenutil.DebugPrintAt(screen, fmt.Sprintf("H=%.2f N=%d P=%d/%d/%d", hFinal, tokensTotal, peaksC, peaksT, peaksR), b.x, b.y+2)
			}
			if b, ok := layout.buttons["token_history_toggle"]; ok {
				label := "Token trend [ ]"
				if g.tokenTrendEnabled {
					label = "Token trend [x]"
				}
				drawButton(b, label, g.tokenTrendEnabled)
			}
			if b, ok := layout.buttons["token_histogram"]; ok {
				if g.tokenTrendEnabled {
					g.drawTokenTrend(screen, b)
				} else {
					g.drawTokenHistogram(screen, b)
				}
			}
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

		case "GLYPHS":
			drawEntropySection()
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
	case "DISCOVERY":
		g.perfDrawDISC += contentDur
	case "ALPHABET":
		g.perfDrawALPHA += contentDur
	case "TRAINER":
		g.perfDrawTRAIN += contentDur
	case "MEANING":
		g.perfDrawMEAN += contentDur
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
