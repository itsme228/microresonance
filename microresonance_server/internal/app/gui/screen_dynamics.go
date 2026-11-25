package gui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"

	"server/internal/domain/signal"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// ─── plot registry ───────────────────────────────────────────────────────────

type dynPlotDef struct {
	key   string
	label string
	clr   color.RGBA
	multi bool // true → draws multiple series (Mag X/Y/Z)
}

var dynPlotDefs = []dynPlotDef{
	{"delta_phase", "ΔΦ XY/YZ/ZX", color.RGBA{}, true},
	{"mag",         "Mag X/Y/Z",   color.RGBA{}, true},
	{"phase_xyz",   "Phase X/Y/Z", color.RGBA{}, true},
	{"mag_total",   "Mag Σ",       color.RGBA{0xff, 0xff, 0xff, 0xcc}, false},
	{"vel",         "Velocity",    color.RGBA{0xff, 0x80, 0x00, 0xff}, false},
	{"snr",         "SNR",         color.RGBA{0xaa, 0x00, 0xff, 0xff}, false},
	{"jitter",      "Jitter",      color.RGBA{0xff, 0xeb, 0x3b, 0xff}, false},
}

func dynDefaultPlotEnabled() map[string]bool {
	return map[string]bool{
		"delta_phase": true,
		"mag":         true,
		"phase_xyz":   false,
		"mag_total":   false,
		"vel":         true,
		"snr":         true,
		"jitter":      false,
	}
}

// ─── config persistence ───────────────────────────────────────────────────────

func dynPlotConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "dyn_plots.json"
	}
	dir := filepath.Join(home, ".config", "microresonance")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "dyn_plots.json")
}

func loadDynPlotConfig() map[string]bool {
	cfg := dynDefaultPlotEnabled()
	data, err := os.ReadFile(dynPlotConfigPath())
	if err != nil {
		return cfg
	}
	loaded := map[string]bool{}
	if json.Unmarshal(data, &loaded) == nil {
		for k, v := range loaded {
			cfg[k] = v
		}
	}
	return cfg
}

func saveDynPlotConfig(cfg map[string]bool) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	_ = os.WriteFile(dynPlotConfigPath(), data, 0o644)
}

// ─── main draw ───────────────────────────────────────────────────────────────

func DrawDynamicsScreen(g *gpuGame, screen *ebiten.Image, w, h int) {
	// Lazy-init plot enable state from persisted config.
	if g.dynPlotEnabled == nil {
		g.dynPlotEnabled = loadDynPlotConfig()
	}

	top := headerH + mainTabRowH + contentTopGap
	top = g.drawSensorDropdown(screen, plotPad, top, w) + 4
	rest := h - top
	if rest < 60 {
		return
	}

	// Layout: plots on left (70%), Lissajous + status on right (30%)
	const sidebarW = 250
	splitX := w - sidebarW
	contentW := splitX - plotPad - 4

	vector.FillRect(screen, float32(plotPad), float32(top), float32(w-2*plotPad), float32(rest), clrBg, true)

	// ── checkbox row ─────────────────────────────────────────────────────────
	cbH := 14
	drawDynCheckboxes(g, screen, plotPad+4, top, contentW, cbH)
	plotTop := top + cbH + 3

	// ── dynamic plot list ─────────────────────────────────────────────────────
	var enabledKeys []string
	for _, def := range dynPlotDefs {
		if g.dynPlotEnabled[def.key] {
			enabledKeys = append(enabledKeys, def.key)
		}
	}
	if len(enabledKeys) > 0 {
		plotAreaH := rest - cbH - 4
		plotH := (plotAreaH - 4) / len(enabledKeys)
		if plotH < 20 {
			plotH = 20
		}
		for i, key := range enabledKeys {
			def := dynPlotDefByKey(key)
			if def == nil {
				continue
			}
			py := plotTop + i*(plotH+3)
			if def.multi {
				series := [][]float64{g.dynHistMagX, g.dynHistMagY, g.dynHistMag}
				clrs := []color.RGBA{clrX, clrY, clrZ}
				label := "Mag (g)  X=red  Y=grn  Z=blue"

				if key == "delta_phase" {
					series = [][]float64{g.dynHistDeltaXY, g.dynHistDeltaYZ, g.dynHistDeltaZX}
					clrs = []color.RGBA{
						{0xff, 0xeb, 0x3b, 0xff}, // Yellow (XY)
						{0xff, 0x40, 0x81, 0xff}, // Magenta (YZ)
						{0x00, 0xff, 0xff, 0xff}, // Cyan (ZX) - making sure it's distinct
					}
					label = "ΔΦ (deg)  XY=ylw  YZ=mag  ZX=cyan"
				} else if key == "phase_xyz" {
					series = [][]float64{g.dynHistPhaseX, g.dynHistPhaseY, g.dynHistPhaseZ}
					clrs = []color.RGBA{clrX, clrY, clrZ}
					label = "Phase (deg)  X=red  Y=grn  Z=blue"
				}

				drawDynamicsMultiPlot(g, screen, plotPad+4, py, contentW, plotH, series, clrs, label)
			} else {
				data := dynPlotData(g, key)
				drawDynamicsPlot(g, screen, plotPad+4, py, contentW, plotH, data, def.clr, def.label)
			}
		}
	}

	// ── right sidebar ─────────────────────────────────────────────────────────
	sx := splitX + 6
	sw := w - sx - plotPad

	// Lock status chip
	lockLabel := "NO LOCK"
	lockClr := color.RGBA{80, 40, 40, 200}
	if g.dynLockinHardLock {
		lockLabel = "HARD LOCK"
		lockClr = color.RGBA{20, 100, 40, 200}
	} else if g.cachedSyncMetrics.CarrierDominant {
		lockLabel = "CARRIER LOCK"
		lockClr = color.RGBA{30, 80, 30, 200}
	}
	vector.FillRect(screen, float32(sx), float32(top+2), float32(sw), 20, lockClr, true)
	ebitenutil.DebugPrintAt(screen, lockLabel, sx+sw/2-len(lockLabel)*3, top+7)

	// Event detector chip
	lastVel := 0.0
	if len(g.dynHistVel) > 0 {
		lastVel = math.Abs(g.dynHistVel[len(g.dynHistVel)-1])
	}
	evClr := color.RGBA{40, 40, 50, 200}
	evLabel := "EVENT DETECTOR"
	if lastVel > 200 {
		evClr = color.RGBA{220, 50, 50, 220}
		evLabel = "! MUSCLE CLICK !"
	}
	vector.FillRect(screen, float32(sx), float32(top+26), float32(sw), 20, evClr, true)
	vector.StrokeRect(screen, float32(sx), float32(top+26), float32(sw), 20, 1, clrGrid, true)
	ebitenutil.DebugPrintAt(screen, evLabel, sx+4, top+32)

	// 2×2 Lissajous grid
	gridTop := top + 50
	halfW := sw / 2
	halfH := halfW // square cells
	drawPhasePhasePlot(screen, g.dynOrbitXY, sx, gridTop, halfW, halfH, "XY")
	drawPhasePhasePlot(screen, g.dynOrbitYZ, sx+halfW+2, gridTop, halfW, halfH, "YZ")
	drawPhasePhasePlot(screen, g.dynOrbitXZ, sx, gridTop+halfH+2, halfW, halfH, "XZ")

	// Polar Z orbit (bottom-right cell)
	orbitX := sx + halfW + 2
	orbitY := gridTop + halfH + 2
	drawOrbitDynamics(g, screen, orbitX, orbitY, halfW)

	// Phasor Diagram below grid
	phasorY := gridTop + 2*halfH + 8
	drawPhasorDiagram(g, screen, sx, phasorY, sw)

	// Magnitude bars + ΔΦ readout below phasor
	barsY := phasorY + sw + 8
	drawLockinAxisBars(g, screen, sx, barsY, sw, h-(barsY+4))
}

// drawDynCheckboxes renders compact checkboxes and stores their rects.
func drawDynCheckboxes(g *gpuGame, screen *ebiten.Image, x0, y, totalW, h int) {
	g.dynCheckboxRects = g.dynCheckboxRects[:0]

	const cbSize = 10
	const padX = 2
	px := x0
	for _, def := range dynPlotDefs {
		label := def.label
		if def.multi {
			if def.key == "mag" {
				label = "MagXYZ"
			} else if def.key == "delta_phase" {
				label = "ΔΦXYZ"
			} else if def.key == "phase_xyz" {
				label = "ΦXYZ"
			}
		}
		boxW := cbSize + 4 + len(label)*6 + padX
		if px+boxW > x0+totalW {
			break
		}
		rect := uiRect{px, y + (h-cbSize)/2, cbSize, cbSize}
		g.dynCheckboxRects = append(g.dynCheckboxRects, struct {
			key  string
			rect uiRect
		}{def.key, rect})

		on := g.dynPlotEnabled[def.key]
		fill := color.RGBA{30, 30, 40, 255}
		if on {
			fill = color.RGBA{0x00, 0xcc, 0x66, 255}
		}
		vector.FillRect(screen, float32(rect.x), float32(rect.y), float32(cbSize), float32(cbSize), fill, true)
		vector.StrokeRect(screen, float32(rect.x), float32(rect.y), float32(cbSize), float32(cbSize), 1, clrGrid, true)
		clr := color.RGBA{120, 120, 140, 255}
		if on {
			clr = color.RGBA{230, 230, 230, 255}
		}
		_ = clr
		ebitenutil.DebugPrintAt(screen, label, rect.x+cbSize+2, rect.y)
		px += boxW + 4
	}

	// VOICE REC button
	btnW := 90
	if px+btnW <= x0+totalW {
		btnRect := uiRect{px, y + (h-cbSize-4)/2, btnW, cbSize + 4}
		g.dynVoiceBtnRect = btnRect
		
		isRec := signal.IsVoiceRecording()
		fill := color.RGBA{40, 40, 50, 255}
		if isRec {
			fill = color.RGBA{255, 50, 50, 255}
		}
		vector.FillRect(screen, float32(btnRect.x), float32(btnRect.y), float32(btnRect.w), float32(btnRect.h), fill, true)
		vector.StrokeRect(screen, float32(btnRect.x), float32(btnRect.y), float32(btnRect.w), float32(btnRect.h), 1, clrGrid, true)
		lbl := "VOICE REC [ ]"
		if isRec {
			lbl = "VOICE REC [x]"
		}
		ebitenutil.DebugPrintAt(screen, lbl, btnRect.x+4, btnRect.y)
	}
}

// drawPhasePhasePlot draws a phase-phase scatter/trail for two axes.
func drawPhasePhasePlot(screen *ebiten.Image, trail [][2]float64, x, y, w, h int, label string) {
	vector.StrokeRect(screen, float32(x), float32(y), float32(w), float32(h), 1, clrGrid, true)
	ebitenutil.DebugPrintAt(screen, label, x+2, y+1)
	if len(trail) < 2 {
		return
	}
	cx, cy := float32(x+w/2), float32(y+h/2)
	scaleX := float32(w/2-6) / 180.0
	scaleY := float32(h/2-6) / 180.0

	normAngle := func(a float64) float64 {
		for a > 180 {
			a -= 360
		}
		for a <= -180 {
			a += 360
		}
		return a
	}

	n := len(trail)
	for i := 0; i < n-1; i++ {
		a0 := normAngle(trail[i][0])
		b0 := normAngle(trail[i][1])
		a1 := normAngle(trail[i+1][0])
		b1 := normAngle(trail[i+1][1])
		x0 := cx + float32(a0)*scaleX
		y0 := cy - float32(b0)*scaleY
		x1 := cx + float32(a1)*scaleX
		y1 := cy - float32(b1)*scaleY
		alpha := uint8(60 + int(float32(i)/float32(n)*195))
		vector.StrokeLine(screen, x0, y0, x1, y1, 1, color.RGBA{0x00, 0xcc, 0xff, alpha}, true)
	}
	// Newest point
	last := trail[n-1]
	lx := cx + float32(normAngle(last[0]))*scaleX
	ly := cy - float32(normAngle(last[1]))*scaleY
	vector.DrawFilledCircle(screen, lx, ly, 3, color.RGBA{0xff, 0xff, 0x00, 0xff}, true)
}

func dynPlotDefByKey(key string) *dynPlotDef {
	for i := range dynPlotDefs {
		if dynPlotDefs[i].key == key {
			return &dynPlotDefs[i]
		}
	}
	return nil
}

func dynPlotData(g *gpuGame, key string) []float64 {
	switch key {
	case "mag_total":
		return g.dynHistMagTotal
	case "vel":
		return g.dynHistVel
	case "snr":
		return g.dynHistSNR
	case "jitter":
		return g.dynHistJitter
	case "phase_x":
		return g.dynHistPhaseX
	case "phase_y":
		return g.dynHistPhaseY
	case "phase_z":
		return g.dynHistPhaseZ
	}
	return nil
}

// ─── right-panel helpers ──────────────────────────────────────────────────────

func drawPhasorDiagram(g *gpuGame, screen *ebiten.Image, x, y, size int) {
	vector.StrokeRect(screen, float32(x), float32(y), float32(size), float32(size), 1, clrGrid, true)
	ebitenutil.DebugPrintAt(screen, "PHASE VECTORS (deg)", x+2, y+1)

	cx, cy := float32(x+size/2), float32(y+size/2)
	r := float32(size/2) - 24

	// Circle outline
	vector.StrokeCircle(screen, cx, cy, r, 1, color.RGBA{60, 60, 80, 255}, true)

	// Markers
	for a := 0; a < 360; a += 45 {
		rad := float64(a) * math.Pi / 180.0
		x1 := cx + r*float32(math.Cos(rad))
		y1 := cy + r*float32(math.Sin(rad))
		x2 := cx + (r+5)*float32(math.Cos(rad))
		y2 := cy + (r+5)*float32(math.Sin(rad))
		vector.StrokeLine(screen, x1, y1, x2, y2, 1, color.RGBA{100, 100, 120, 255}, true)
	}

	// Current phases
	px, py, pz := 0.0, 0.0, 0.0
	if len(g.dynHistPhaseX) > 0 {
		px = g.dynHistPhaseX[len(g.dynHistPhaseX)-1]
	}
	if len(g.dynHistPhaseY) > 0 {
		py = g.dynHistPhaseY[len(g.dynHistPhaseY)-1]
	}
	if len(g.dynHistPhaseZ) > 0 {
		pz = g.dynHistPhaseZ[len(g.dynHistPhaseZ)-1]
	}

	phases := []float64{px, py, pz}
	labels := []string{"X", "Y", "Z"}
	clrs := []color.RGBA{clrX, clrY, clrZ}

	for i, p := range phases {
		rad := p * math.Pi / 180.0
		tx := cx + r*float32(math.Cos(rad))
		ty := cy + r*float32(math.Sin(rad))

		// Vector line
		vector.StrokeLine(screen, cx, cy, tx, ty, 2, clrs[i], true)
		vector.DrawFilledCircle(screen, tx, ty, 3, clrs[i], true)

		// Label
		lx := cx + (r+14)*float32(math.Cos(rad)) - 4
		ly := cy + (r+14)*float32(math.Sin(rad)) - 6
		ebitenutil.DebugPrintAt(screen, labels[i], int(lx), int(ly))
	}

	// Show values
	dxy, dyz, dzx := 0.0, 0.0, 0.0
	if len(g.dynHistDeltaXY) > 0 {
		dxy = g.dynHistDeltaXY[len(g.dynHistDeltaXY)-1]
	}
	if len(g.dynHistDeltaYZ) > 0 {
		dyz = g.dynHistDeltaYZ[len(g.dynHistDeltaYZ)-1]
	}
	if len(g.dynHistDeltaZX) > 0 {
		dzx = g.dynHistDeltaZX[len(g.dynHistDeltaZX)-1]
	}

	msg := fmt.Sprintf("XY:%+.1f YZ:%+.1f ZX:%+.1f", dxy, dyz, dzx)
	ebitenutil.DebugPrintAt(screen, msg, x+size/2-len(msg)*3, y+size-14)
}

func drawLockinAxisBars(g *gpuGame, screen *ebiten.Image, x, y, w, maxH int) {
	if maxH < 60 {
		return
	}
	labels := []string{"X", "Y", "Z"}
	clrs := []color.RGBA{clrX, clrY, clrZ}
	mags := [3]float64{}
	if len(g.dynHistMagX) > 0 {
		mags[0] = g.dynHistMagX[len(g.dynHistMagX)-1]
	}
	if len(g.dynHistMagY) > 0 {
		mags[1] = g.dynHistMagY[len(g.dynHistMagY)-1]
	}
	if len(g.dynHistMag) > 0 {
		mags[2] = g.dynHistMag[len(g.dynHistMag)-1]
	}
	peak := mags[0]
	for _, m := range mags[1:] {
		if m > peak {
			peak = m
		}
	}
	if peak < 1e-9 {
		peak = 1e-9
	}

	rowH := 16
	for i, lbl := range labels {
		ry := y + i*rowH
		ratio := float32(mags[i] / peak)
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		barW := float32(w-2) * ratio
		vector.StrokeRect(screen, float32(x), float32(ry), float32(w), float32(rowH-2), 1, clrGrid, true)
		if barW > 1 {
			c := clrs[i]
			c.A = 160
			vector.FillRect(screen, float32(x+1), float32(ry+1), barW, float32(rowH-4), c, true)
		}
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%s %.5fg", lbl, mags[i]), x+2, ry+3)
	}

	// Differential phase readouts
	oy := y + 3*rowH + 2
	dxy := 0.0
	dyz := 0.0
	dzx := 0.0
	if len(g.dynHistDeltaXY) > 0 {
		dxy = g.dynHistDeltaXY[len(g.dynHistDeltaXY)-1]
	}
	if len(g.dynHistDeltaYZ) > 0 {
		dyz = g.dynHistDeltaYZ[len(g.dynHistDeltaYZ)-1]
	}
	if len(g.dynHistDeltaZX) > 0 {
		dzx = g.dynHistDeltaZX[len(g.dynHistDeltaZX)-1]
	}
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("ΔXY %+.1f°", dxy), x, oy)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("ΔYZ %+.1f°", dyz), x, oy+13)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("ΔZX %+.1f°", dzx), x, oy+26)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Stab %.2f°", g.cachedSyncMetrics.PhaseStabilityDeg), x, oy+42)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("PLL %+.1f°/s", g.cachedSyncMetrics.PLLVelocityDegSec), x, oy+55)
}

func drawOrbitDynamics(g *gpuGame, screen *ebiten.Image, x, y, size int) {
	vector.StrokeRect(screen, float32(x), float32(y), float32(size), float32(size), 1, clrGrid, true)
	ebitenutil.DebugPrintAt(screen, "Z POLAR", x+2, y+1)

	cx, cy := float32(x+size/2), float32(y+size/2)
	r := float32(size/2) - 8

	vector.StrokeCircle(screen, cx, cy, r, 1, color.RGBA{60, 60, 80, 255}, true)

	if len(g.dynOrbitTrail) > 1 {
		for i := 0; i < len(g.dynOrbitTrail)-1; i++ {
			p0 := g.dynOrbitTrail[i].P * math.Pi / 180.0
			m0 := float32(g.dynOrbitTrail[i].M * 100 * g.zoomY)
			if m0 > r {
				m0 = r
			}
			p1 := g.dynOrbitTrail[i+1].P * math.Pi / 180.0
			m1 := float32(g.dynOrbitTrail[i+1].M * 100 * g.zoomY)
			if m1 > r {
				m1 = r
			}
			x0 := cx + m0*float32(math.Cos(p0))
			y0 := cy + m0*float32(math.Sin(p0))
			x1 := cx + m1*float32(math.Cos(p1))
			y1 := cy + m1*float32(math.Sin(p1))
			alpha := uint8(float32(i) / float32(len(g.dynOrbitTrail)) * 255)
			vector.StrokeLine(screen, x0, y0, x1, y1, 1, color.RGBA{0x4e, 0xc4, 0x50, alpha}, true)
		}
	}

	phaseRad := 0.0
	magVal := 0.0
	if len(g.dynOrbitTrail) > 0 {
		last := g.dynOrbitTrail[len(g.dynOrbitTrail)-1]
		phaseRad = last.P * math.Pi / 180.0
		magVal = last.M
	}
	magR := float32(magVal * 100 * g.zoomY)
	if magR > r {
		magR = r
	}
	targetX := cx + magR*float32(math.Cos(phaseRad))
	targetY := cy + magR*float32(math.Sin(phaseRad))
	vector.StrokeLine(screen, cx, cy, targetX, targetY, 2, color.RGBA{0x4e, 0xc4, 0x50, 0xff}, true)
	vector.DrawFilledCircle(screen, targetX, targetY, 4, color.RGBA{0x4e, 0xc4, 0x50, 0xff}, true)
}

// ─── plot primitives ──────────────────────────────────────────────────────────

func drawDynamicsPlot(g *gpuGame, screen *ebiten.Image, x0, y0, w, h int, data []float64, clr color.RGBA, label string) {
	if w < 4 || h < 4 {
		return
	}
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)

	if len(data) < 2 {
		ebitenutil.DebugPrintAt(screen, label+" (waiting...)", x0+2, y0+1)
		return
	}

	vMin, vMax := data[0], data[0]
	for _, v := range data {
		if v < vMin {
			vMin = v
		}
		if v > vMax {
			vMax = v
		}
	}
	span := vMax - vMin
	if span < 1e-9 {
		span = 1.0
		vMin -= 0.5
		vMax += 0.5
	}
	center := (vMax + vMin) / 2
	span = span / g.zoomY
	vMin = center - span/2
	vMax = center + span/2

	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%s [%.2f/%.2f]", label, vMin, vMax), x0+2, y0+1)

	n := len(data)
	pointsToShow := int(float64(w) / g.zoomX)
	if pointsToShow < 2 {
		pointsToShow = 2
	}
	startIndex := n - pointsToShow
	if startIndex < 0 {
		startIndex = 0
	}
	nVisible := n - startIndex
	drawW := float64(w - 2)
	drawH := float64(h - 2)

	normY := func(v float64) float32 {
		if span == 0 {
			return float32(y0 + h/2)
		}
		return float32(y0+h-1) - float32((v-vMin)/span*drawH)
	}

	var prevX, prevY float32
	first := true
	for i := startIndex; i < n; i++ {
		px := float32(x0+1) + float32(float64(i-startIndex)/float64(nVisible-1)*drawW)
		py := normY(data[i])
		if !first {
			vector.StrokeLine(screen, prevX, prevY, px, py, 1.2, clr, true)
		}
		prevX, prevY = px, py
		first = false
	}
}

func drawDynamicsMultiPlot(g *gpuGame, screen *ebiten.Image, x0, y0, w, h int, series [][]float64, clrs []color.RGBA, label string) {
	if w < 4 || h < 4 {
		return
	}
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)

	vMin, vMax := math.MaxFloat64, -math.MaxFloat64
	anyData := false
	for _, data := range series {
		for _, v := range data {
			if v < vMin {
				vMin = v
			}
			if v > vMax {
				vMax = v
			}
			anyData = true
		}
	}
	if !anyData {
		ebitenutil.DebugPrintAt(screen, label+" (waiting...)", x0+2, y0+1)
		return
	}
	span := vMax - vMin
	if span < 1e-9 {
		span = 1.0
		vMin -= 0.5
		vMax += 0.5
	}
	center := (vMax + vMin) / 2
	span = span / g.zoomY
	vMin = center - span/2
	vMax = center + span/2

	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%s [%.5f/%.5f]", label, vMin, vMax), x0+2, y0+1)

	pointsToShow := int(float64(w) / g.zoomX)
	if pointsToShow < 2 {
		pointsToShow = 2
	}
	drawW := float64(w - 2)
	drawH := float64(h - 2)

	normY := func(v float64) float32 {
		if span == 0 {
			return float32(y0 + h/2)
		}
		return float32(y0+h-1) - float32((v-vMin)/span*drawH)
	}

	for si, data := range series {
		n := len(data)
		if n < 2 {
			continue
		}
		startIndex := n - pointsToShow
		if startIndex < 0 {
			startIndex = 0
		}
		nVisible := n - startIndex
		clr := clrs[si]
		var prevX, prevY float32
		first := true
		for i := startIndex; i < n; i++ {
			px := float32(x0+1) + float32(float64(i-startIndex)/float64(nVisible-1)*drawW)
			py := normY(data[i])
			if !first {
				vector.StrokeLine(screen, prevX, prevY, px, py, 1.2, clr, true)
			}
			prevX, prevY = px, py
			first = false
		}
	}
}
