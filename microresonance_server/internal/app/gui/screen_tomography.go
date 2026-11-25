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

type tomoSensorMetrics struct {
	SNRdB     float64
	Mag       float64
	Phase     float64
	DeltaXY   float64
	DeltaYZ   float64
	DeltaZX   float64
	Valid     bool
	SID       int
	PhysioBPM float64
	PhysioHRV float64
	PulseWave []float32
}

func DrawTomographyScreen(g *gpuGame, screen *ebiten.Image, w, h int) {
	margin := 10
	top := headerH + mainTabRowH + 10
	
	// Reset hitboxes
	for k := range g.tomoPartsRects { delete(g.tomoPartsRects, k) }

	mainTop := top
	bottomH := 200
	mainH := h - mainTop - bottomH - 20
	sideW := 280 
	centerW := w - 2*margin - 2*sideW - 20

	vector.FillRect(screen, float32(margin), float32(mainTop), float32(w-2*margin), float32(h-mainTop-10), clrPanel, true)
	vector.StrokeRect(screen, float32(margin), float32(mainTop), float32(w-2*margin), float32(h-mainTop-10), 1, clrGrid, true)

	metrics := make(map[string]tomoSensorMetrics)
	parts := []string{"Head", "Chest", "LeftArm", "RightArm", "LeftLeg", "RightLeg"}
	for _, name := range parts {
		sid, bound := g.tomoSensorMap[name]
		if !bound { continue }
		g.buf.Mu.Lock()
		s := g.buf.Sensors[uint8(sid)]
		if s != nil {
			m := tomoSensorMetrics{
				SNRdB:   -20,
				Mag:     s.LockinMag,
				Phase:   s.LockinPhase * 180 / math.Pi,
				DeltaXY: s.LockinDeltaXY,
				DeltaYZ: s.LockinDeltaYZ,
				DeltaZX: -s.LockinDeltaXZ,
				Valid:     true,
				SID:       sid,
				PhysioBPM: s.PhysioBPM,
				PhysioHRV: s.PhysioHRV,
				}
				if len(s.PhysioPulseWave) > 0 {
				m.PulseWave = make([]float32, len(s.PhysioPulseWave))
				copy(m.PulseWave, s.PhysioPulseWave)
				}
				if s.LastDsp != nil { m.SNRdB = float64(s.LastDsp.Resonance.CarrierSNR) }

			metrics[name] = m
		}
		g.buf.Mu.Unlock()
	}

	t := float64(time.Now().UnixMilli()%60000) / 1000.0

	// --- Left Panel: SNR + Left Limbs ---
	leftX := margin + 8
	drawSNRStack(screen, leftX, mainTop+10, 30, mainH, parts, metrics)
	
	triLX := leftX + 40
	drawPhaseQuantumMap(screen, float32(triLX+70), float32(mainTop+mainH/4), 60, "LEFT ARM", metrics["LeftArm"], t)
	drawPhaseQuantumMap(screen, float32(triLX+70), float32(mainTop+3*mainH/4), 60, "LEFT LEG", metrics["LeftLeg"], t)

	// --- Center: Anatomical ---
	skellyX := triLX + 140
	drawAnatomicalControl(g, screen, skellyX, mainTop+10, centerW, mainH, metrics, t)

	// --- Right Panel ---
	rightX := skellyX + centerW + 10
	drawPhaseQuantumMap(screen, float32(rightX+60), float32(mainTop+mainH/6), 50, "HEAD", metrics["Head"], t)
	drawPhaseQuantumMap(screen, float32(rightX+185), float32(mainTop+mainH/6), 50, "CHEST", metrics["Chest"], t)
	drawPhaseQuantumMap(screen, float32(rightX+120), float32(mainTop+mainH/2), 60, "RIGHT ARM", metrics["RightArm"], t)
	drawPhaseQuantumMap(screen, float32(rightX+120), float32(mainTop+3*mainH/4+20), 60, "RIGHT LEG", metrics["RightLeg"], t)

	// --- Bottom: Fireball Balance & Technicals ---
	botY := mainTop + mainH + 20
	drawFireballBCI(g, screen, margin+8, botY, w/2, bottomH-10, metrics, t)
	
	// Central Physiomon: use Chest if available, else Head
	physio := metrics["Chest"]
	if !physio.Valid { physio = metrics["Head"] }
	
	drawTechnicalDiagnostics(g, screen, margin+8+w/2+10, botY, w/2-26, bottomH-10, physio)
}

func drawAnatomicalControl(g *gpuGame, screen *ebiten.Image, x, y, w, h int, metrics map[string]tomoSensorMetrics, t float64) {
	cx, cy := float32(x+w/2), float32(y+h/2-20)
	joints := map[string][2]float32{
		"Head": {cx, cy - 160}, "Neck": {cx, cy - 130}, "Pelvis": {cx, cy + 10},
		"ShoulderL": {cx - 45, cy - 110}, "ShoulderR": {cx + 45, cy - 110},
		"ElbowL": {cx - 75, cy - 40}, "ElbowR": {cx + 75, cy - 40},
		"WristL": {cx - 95, cy + 30}, "WristR": {cx + 95, cy + 30},
		"KneeL": {cx - 35, cy + 85}, "KneeR": {cx + 35, cy + 85},
		"AnkleL": {cx - 40, cy + 160}, "AnkleR": {cx + 40, cy + 160},
	}
	hitSize := 60
	g.tomoPartsRects["Head"] = uiRect{int(joints["Head"][0]) - hitSize/2, int(joints["Head"][1]) - hitSize/2, hitSize, hitSize}
	g.tomoPartsRects["Chest"] = uiRect{int(joints["Neck"][0]) - hitSize/2, int(joints["Neck"][1]) - hitSize/2, hitSize, hitSize}
	g.tomoPartsRects["LeftArm"] = uiRect{int(joints["WristL"][0]) - hitSize/2, int(joints["WristL"][1]) - hitSize/2, hitSize, hitSize}
	g.tomoPartsRects["RightArm"] = uiRect{int(joints["WristR"][0]) - hitSize/2, int(joints["WristR"][1]) - hitSize/2, hitSize, hitSize}
	g.tomoPartsRects["LeftLeg"] = uiRect{int(joints["AnkleL"][0]) - hitSize/2, int(joints["AnkleL"][1]) - hitSize/2, hitSize, hitSize}
	g.tomoPartsRects["RightLeg"] = uiRect{int(joints["AnkleR"][0]) - hitSize/2, int(joints["AnkleR"][1]) - hitSize/2, hitSize, hitSize}

	drawBone := func(a, b [2]float32, active bool) {
		vector.StrokeLine(screen, a[0], a[1], b[0], b[1], 2, color.RGBA{50, 60, 80, 200}, true)
		if active {
			for i := 0; i < 3; i++ {
				pos := math.Mod(t*1.5+float64(i)*0.33, 1.0)
				px := a[0] + float32(pos)*(b[0]-a[0])
				py := a[1] + float32(pos)*(b[1]-a[1])
				vector.DrawFilledCircle(screen, px, py, 2.5, color.RGBA{0, 200, 255, 180}, true)
			}
		}
	}
	drawBone(joints["Pelvis"], joints["Neck"], true)
	drawBone(joints["Neck"], joints["Head"], true)
	drawBone(joints["Neck"], joints["ShoulderL"], metrics["LeftArm"].Valid)
	drawBone(joints["Neck"], joints["ShoulderR"], metrics["RightArm"].Valid)
	drawBone(joints["ShoulderL"], joints["ElbowL"], metrics["LeftArm"].Valid)
	drawBone(joints["ElbowL"], joints["WristL"], metrics["LeftArm"].Valid)
	drawBone(joints["ShoulderR"], joints["ElbowR"], metrics["RightArm"].Valid)
	drawBone(joints["ElbowR"], joints["WristR"], metrics["RightArm"].Valid)
	drawBone(joints["Pelvis"], joints["KneeL"], metrics["LeftLeg"].Valid)
	drawBone(joints["KneeL"], joints["AnkleL"], metrics["LeftLeg"].Valid)
	drawBone(joints["Pelvis"], joints["KneeR"], metrics["RightLeg"].Valid)
	drawBone(joints["KneeR"], joints["AnkleR"], metrics["RightLeg"].Valid)

	nodeMap := map[string][2]float32{
		"Head": joints["Head"], "Chest": joints["Neck"],
		"LeftArm": joints["WristL"], "RightArm": joints["WristR"],
		"LeftLeg": joints["AnkleL"], "RightLeg": joints["AnkleR"],
	}
	for name, pt := range nodeMap {
		m, active := metrics[name]
		drawPolarRose(screen, pt[0], pt[1], 28, active, m)
	}
	drawTomoEmitter(screen, joints["Pelvis"][0]-15, joints["Pelvis"][1], float64(g.protocol.Intensity)/100.0, t)
	drawTomoEmitter(screen, joints["Pelvis"][0]+15, joints["Pelvis"][1], float64(g.protocol.Intensity)/100.0, t)
}

func drawPhaseQuantumMap(screen *ebiten.Image, cx, cy, size float32, name string, m tomoSensorMetrics, t float64) {
	// Cyber Badge
	vector.FillRect(screen, cx-size, cy-size-15, size*2, size*2+55, color.RGBA{20, 22, 30, 200}, true)
	vector.StrokeRect(screen, cx-size, cy-size-15, size*2, size*2+55, 1, clrGrid, true)
	vector.FillRect(screen, cx-size, cy-size-15, size*2, 14, color.RGBA{40, 50, 80, 255}, true)
	ebitenutil.DebugPrintAt(screen, name, int(cx-size+4), int(cy-size-16))

	if !m.Valid {
		ebitenutil.DebugPrintAt(screen, "NO SENSOR", int(cx-25), int(cy-4))
		return
	}

	// CIRCULAR diagram in the corner (top-right of the badge)
	crX, crY, crR := cx+size-18, cy-size+12, float32(12)
	drawMiniCircular(screen, crX, crY, crR, m)

	// Real-time Lissajous Simulation (High-resolution "Twist")
	points := 120
	var px, py float32
	dXY := m.DeltaXY * math.Pi / 180.0
	dYZ := m.DeltaYZ * math.Pi / 180.0
	
	for i := 0; i <= points; i++ {
		angle := float64(i) / float64(points) * 2 * math.Pi
		// We use t*4 for faster spinning and dXY/dYZ for the 3D twist
		tx := float32(math.Cos(angle*1 + t*4)) * (size * 0.7)
		ty := float32(math.Sin(angle*1 + dXY + t*4)) * (size * 0.7)
		tz := float32(math.Sin(angle*2 + dYZ)) * (size * 0.2)
		nx := cx + tx + tz
		ny := cy + ty
		if i > 0 {
			alpha := uint8(100 + 155*math.Max(0, math.Sin(angle*2-t*8)))
			clr := color.RGBA{0, 255, 255, alpha}
			if m.SNRdB < 5 { clr = color.RGBA{255, 50, 50, alpha} }
			vector.StrokeLine(screen, px, py, nx, ny, 1.5, clr, true)
		}
		px, py = nx, ny
	}

	curY := int(cy + size*0.8)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("S%d  SNR:%5.1f", m.SID, m.SNRdB), int(cx-size+4), curY)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Mag:%7.4f", m.Mag), int(cx-size+4), curY+12)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("D: %+.0f %+.0f %+.0f", m.DeltaXY, m.DeltaYZ, m.DeltaZX), int(cx-size+4), curY+24)

	// Physiology Info
	if m.PhysioBPM > 0 {
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("BPM: %.1f", m.PhysioBPM), int(cx-size+4), curY+40)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("HRV: %.0fms", m.PhysioHRV), int(cx+10), curY+40)
		
		// Draw Mini Pulse Wave
		if len(m.PulseWave) > 1 {
			pwY := float32(curY + 65)
			pwH := float32(25)
			pwW := size * 1.8
			pwX := cx - size + 4
			
			// Find min/max for scaling
			mn, mx := m.PulseWave[0], m.PulseWave[0]
			for _, v := range m.PulseWave {
				if v < mn { mn = v }
				if v > mx { mx = v }
			}
			span := mx - mn
			if span < 0.1 { span = 0.1 }
			
			for i := 0; i < len(m.PulseWave)-1; i++ {
				x0 := pwX + float32(i)/float32(len(m.PulseWave))*pwW
				x1 := pwX + float32(i+1)/float32(len(m.PulseWave))*pwW
				y0 := pwY + pwH - (m.PulseWave[i]-mn)/span*pwH
				y1 := pwY + pwH - (m.PulseWave[i+1]-mn)/span*pwH
				vector.StrokeLine(screen, x0, y0, x1, y1, 1.5, color.RGBA{0, 255, 100, 200}, true)
			}
		}
	}
}

func drawMiniCircular(screen *ebiten.Image, cx, cy, r float32, m tomoSensorMetrics) {
	vector.StrokeCircle(screen, cx, cy, r, 1, color.RGBA{100, 100, 120, 150}, true)
	drawV := func(phDeg float64, clr color.RGBA) {
		rad := phDeg * math.Pi / 180.0
		tx := cx + r*float32(math.Cos(rad))
		ty := cy + r*float32(math.Sin(rad))
		vector.StrokeLine(screen, cx, cy, tx, ty, 1.5, clr, true)
	}
	drawV(0, clrX)
	drawV(m.DeltaXY, clrY)
	drawV(m.DeltaXY+m.DeltaYZ, clrZ)
}

func drawPolarRose(screen *ebiten.Image, cx, cy, r float32, active bool, m tomoSensorMetrics) {
	vector.StrokeCircle(screen, cx, cy, r, 1, color.RGBA{40, 45, 60, 255}, true)
	if !active { return }
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("S%d", m.SID), int(cx-10), int(cy-r-14))
	clr := clrNear
	if m.SNRdB > 10 { clr = clrHit } else if m.SNRdB < 0 { clr = color.RGBA{255, 50, 50, 255} }
	rad := m.Phase * math.Pi / 180.0
	tx := cx + r*float32(math.Cos(rad))
	ty := cy + r*float32(math.Sin(rad))
	vector.StrokeLine(screen, cx, cy, tx, ty, 2.5, clr, true)
	vector.DrawFilledCircle(screen, tx, ty, 4, clr, true)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%.0f", m.Phase), int(cx-10), int(cy+r+2))
}

func drawFireballBCI(g *gpuGame, screen *ebiten.Image, x, y, w, h int, metrics map[string]tomoSensorMetrics, t float64) {
	// Collect live ApEn from any bound sensor; use minimum (most locked)
	liveApEn := 1.0
	g.buf.Mu.Lock()
	for _, s := range g.buf.Sensors {
		// lockinPhaseWinN > 0 means we have at least one completed integration window
		if s != nil && s.Packets > 33 && s.LockinApEn < liveApEn {
			liveApEn = s.LockinApEn
		}
	}
	g.buf.Mu.Unlock()
	// intentGlow ∈ [0,1]: 1 = full intent lock (ApEn→0), 0 = chaos (ApEn≥0.5)
	intentGlow := 0.0
	if liveApEn < 0.5 {
		intentGlow = (0.5 - liveApEn) / 0.5
	}

	vector.StrokeRect(screen, float32(x), float32(y), float32(w), float32(h), 1, clrGrid, true)
	sealTitle := "GREAT RESONANCE SEAL (BCI Balance)"
	if intentGlow > 0.3 {
		sealTitle = "GREAT RESONANCE SEAL *** INTENT LOCK ***"
	}
	ebitenutil.DebugPrintAt(screen, sealTitle, x+5, y+2)
	buffs := []struct{ name, icon string; clr color.RGBA }{
		{"POWER", "PWR", color.RGBA{255, 200, 0, 255}},
		{"STABILITY", "STB", color.RGBA{0, 255, 100, 255}},
		{"SYNC", "SNC", color.RGBA{0, 200, 255, 255}},
		{"RESONANCE", "RES", color.RGBA{255, 50, 255, 255}},
	}
	for i, b := range buffs {
		level := g.tomoActiveBuffs[b.name]
		bx := x + w - 40 - i*38
		by := y + 4
		alpha := uint8(50 + 205*level)
		vector.StrokeRect(screen, float32(bx), float32(by), 32, 14, 1, color.RGBA{b.clr.R, b.clr.G, b.clr.B, alpha}, true)
		ebitenutil.DebugPrintAt(screen, b.icon, bx+4, by-1)
	}
	cx, cy := float32(x+w/2), float32(y+h/2)
	mL, okL := metrics["LeftLeg"]
	mR, okR := metrics["RightLeg"]
	if okL && okR {
		rangeX, rangeY := float32(w/2)-50, float32(h/2)-50
		px := cx + float32(g.tomoLiveSmoothX)*rangeX
		py := cy - float32(g.tomoLiveSmoothY)*rangeY
		radius := 20.0 + g.tomoCastLevel*40.0 + intentGlow*25.0
		if g.tomoCastLevel > 0.01 || intentGlow > 0.1 {
			drawPolygon := func(pcx, pcy, r float32, sides int, rot float64, thickness float32, clr color.RGBA) {
				var lastX, lastY float32
				for i := 0; i <= sides; i++ {
					angle := float64(i)*2*math.Pi/float64(sides) + rot
					nx := pcx + r*float32(math.Cos(angle))
					ny := pcy + r*float32(math.Sin(angle))
					if i > 0 { vector.StrokeLine(screen, lastX, lastY, nx, ny, thickness, clr, true) }
					lastX, lastY = nx, ny
				}
			}
			vel := g.cachedSyncMetrics.PhaseVelocityDegSec
			heat := math.Min(1, math.Abs(vel)/200.0)
			// ApEn < 0.5 shifts color toward gold and boosts alpha
			glowAlpha := math.Min(1.0, float64(g.tomoCastLevel)+intentGlow*0.8)
			intentR := uint8(255*heat + 255*intentGlow*(1-heat))
			intentG := uint8(200*(1-heat) + 180*intentGlow*(1-heat))
			intentB := uint8(255*(1-heat) - 200*intentGlow*(1-heat))
			manaClr := color.RGBA{intentR, intentG, intentB, uint8(220 * glowAlpha)}
			snr := mL.SNRdB
			if mR.SNRdB > snr { snr = mR.SNRdB }
			sides := 3 + int((snr+10)/3)
			if sides < 3 { sides = 3 } else if sides > 16 { sides = 16 }
			twistRot := g.tomoLiveTwist * math.Pi / 180.0
			drawPolygon(px, py, float32(radius), sides, t*0.5+twistRot, 2.0, manaClr)
			drawPolygon(px, py, float32(radius*0.75), 3, t*1.2+twistRot, 1.5, manaClr)
			if g.tomoCastLevel > 0.3 { drawPolygon(px, py, float32(radius*0.75), 3, -t*1.2+twistRot, 1.5, manaClr) }
			for i := 0; i < 4; i++ {
				ra := t*3 + float64(i)*math.Pi/2 + twistRot
				rx := px + float32(radius)*float32(math.Cos(ra))
				ry := py + float32(radius)*float32(math.Sin(ra))
				vector.DrawFilledCircle(screen, rx, ry, 3*float32(g.tomoCastLevel), color.RGBA{255, 255, 255, uint8(200 * g.tomoCastLevel)}, true)
			}
			if g.tomoCastLevel > 0.8 || intentGlow > 0.6 {
				coreR := float32(radius * 0.15)
				if intentGlow > 0.6 { coreR = float32(radius * (0.15 + 0.1*intentGlow)) }
				vector.DrawFilledCircle(screen, px, py, coreR, color.RGBA{255, 255, uint8(200 * (1 - intentGlow)), 255}, true)
			}
		}
		g.tomoBalanceTrail = append(g.tomoBalanceTrail, [2]float64{float64(px), float64(py)})
		if len(g.tomoBalanceTrail) > 25 { g.tomoBalanceTrail = g.tomoBalanceTrail[1:] }
		for i := 0; i < len(g.tomoBalanceTrail)-1; i++ {
			p0, p1 := g.tomoBalanceTrail[i], g.tomoBalanceTrail[i+1]
			alpha := uint8(float64(i) * 6 * g.tomoCastLevel)
			trailClr := color.RGBA{uint8(i * 10), 150, 255, alpha}
			if g.tomoCastLevel > 0.4 { trailClr = color.RGBA{255, uint8(100 + i*5), 0, alpha} }
			vector.StrokeLine(screen, float32(p0[0]), float32(p0[1]), float32(p1[0]), float32(p1[1]), 2, trailClr, true)
		}
	}
}

func drawSNRStack(screen *ebiten.Image, x, y, w, h int, parts []string, metrics map[string]tomoSensorMetrics) {
	barH := (h - 20) / len(parts)
	for i, name := range parts {
		by := y + i*(barH+4)
		m, ok := metrics[name]
		snr := -30.0
		if ok { snr = m.SNRdB }
		fillW := float32((snr + 30) / 60.0) * float32(w)
		clr := color.RGBA{200, 50, 50, 150}
		if snr > 10 { clr = clrHit } else if snr > 0 { clr = clrNear }
		vector.FillRect(screen, float32(x), float32(by), fillW, float32(barH), clr, true)
		vector.StrokeRect(screen, float32(x), float32(by), float32(w), float32(barH), 1, clrGrid, true)
		ebitenutil.DebugPrintAt(screen, name[:1], x-12, by)
	}
}

func drawTechnicalDiagnostics(g *gpuGame, screen *ebiten.Image, x, y, w, h int, physio tomoSensorMetrics) {
	vector.StrokeRect(screen, float32(x), float32(y), float32(w), float32(h), 1, clrGrid, true)
	ebitenutil.DebugPrintAt(screen, "MISSION DIAGNOSTICS & PHYSIOLOGY", x+5, y+2)
	
	jitter := g.cachedSyncMetrics.JitterUs
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Jitter: %.1f us", jitter), x+10, y+24)
	fillW := float32(jitter / 1000.0 * float64(w/2-20))
	vector.FillRect(screen, float32(x+10), float32(y+40), fillW, 12, color.RGBA{100, 100, 250, 200}, true)
	vector.StrokeRect(screen, float32(x+10), float32(y+40), float32(w/2-20), 12, 1, clrGrid, true)
	
	vel := g.cachedSyncMetrics.PhaseVelocityDegSec
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Phase Vel: %.1f deg/s", vel), x+10, y+64)
	
	// BPM and HRV display
	if physio.Valid && physio.PhysioBPM > 0 {
		ebitenutil.DebugPrintAt(screen, "HEART RATE MONITOR", x+w/2+10, y+24)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("BPM: %.1f", physio.PhysioBPM), x+w/2+10, y+44)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("HRV: %.1f ms", physio.PhysioHRV), x+w/2+10, y+64)
		
		// Large Cardiogram
		if len(physio.PulseWave) > 1 {
			pwX := float32(x + 10)
			pwY := float32(y + 90)
			pwW := float32(w - 20)
			pwH := float32(h - 100)
			vector.StrokeRect(screen, pwX, pwY, pwW, pwH, 1, color.RGBA{40, 60, 40, 255}, true)
			
			mn, mx := physio.PulseWave[0], physio.PulseWave[0]
			for _, v := range physio.PulseWave {
				if v < mn { mn = v }
				if v > mx { mx = v }
			}
			span := mx - mn
			if span < 0.1 { span = 0.1 }
			
			for i := 0; i < len(physio.PulseWave)-1; i++ {
				x0 := pwX + float32(i)/float32(len(physio.PulseWave))*pwW
				x1 := pwX + float32(i+1)/float32(len(physio.PulseWave))*pwW
				y0 := pwY + pwH - (physio.PulseWave[i]-mn)/span*pwH
				y1 := pwY + pwH - (physio.PulseWave[i+1]-mn)/span*pwH
				vector.StrokeLine(screen, x0, y0, x1, y1, 2, color.RGBA{0, 255, 100, 255}, true)
			}
		}
	} else {
		ebitenutil.DebugPrintAt(screen, "(waiting for chest/head heart capture)", x+w/2+10, y+44)
	}

	isClick := math.Abs(vel) > 200
	if isClick {
		vector.DrawFilledCircle(screen, float32(x+w-40), float32(y+40), 20, color.RGBA{255, 50, 50, 255}, true)
		ebitenutil.DebugPrintAt(screen, "CLICK", x+w-55, y+65)
	}
}

func drawTomoEmitter(screen *ebiten.Image, cx, cy float32, power float64, t float64) {
	if power < 0.01 {
		vector.DrawFilledCircle(screen, cx, cy, 5, color.RGBA{60, 70, 90, 120}, true)
		return
	}
	pulse := float32(0.8 + 0.2*math.Sin(t*math.Pi*2))
	vector.StrokeCircle(screen, cx, cy, 12*pulse, 1, color.RGBA{255, 80, 20, uint8(40 * power)}, true)
	vector.DrawFilledCircle(screen, cx, cy, 5*pulse, color.RGBA{255, 160, 60, uint8(220 * power)}, true)
}
