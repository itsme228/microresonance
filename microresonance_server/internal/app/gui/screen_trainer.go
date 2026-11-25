// TRAINER screen: trainer-metronome + hit progress.
// Action = user label for repeatable physical scenario.
// Calibration = REST/DO/RELAX by prompts; Hit/Near/Miss by bands×glyph signature.

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

const (
	trainerPanelW     = 200
	trainerBtnH       = 20
	trainerActionRowH = 36
	trainerContentPad = 8
)

var (
	clrHit  = color.RGBA{0x4c, 0xaf, 0x50, 0xff} // green
	clrNear = color.RGBA{0xff, 0x98, 0x00, 0xff} // orange
	clrMiss = color.RGBA{0x75, 0x75, 0x75, 0xff} // gray
)

// trainerActionRect — clickable action row in list.
type trainerActionRect struct {
	actionID int
	rect     uiRect
}

// DrawTrainerScreen draws TRAINER tab: Actions panel left, center — Calibrate/Live/Export.
func DrawTrainerScreen(g *gpuGame, screen *ebiten.Image, w, h int) {
	top := headerH + mainTabRowH
	rest := h - top
	if rest < 100 {
		return
	}
	contentW := w - 2*plotPad - trainerPanelW
	if contentW < 240 {
		contentW = w - 2*plotPad - trainerPanelW
	}

	// Background
	vector.FillRect(screen, float32(plotPad), float32(top), float32(w-2*plotPad), float32(rest), clrBg, true)

	leftX := plotPad + 4
	panelTop := top + 4

	// === Actions panel (left) ===
	vector.FillRect(screen, float32(leftX), float32(panelTop), float32(trainerPanelW), float32(rest-8), clrPanel, true)
	vector.StrokeRect(screen, float32(leftX), float32(panelTop), float32(trainerPanelW), float32(rest-8), 1, clrGrid, true)
	ebitenutil.DebugPrintAt(screen, "Actions (Binds)", leftX+4, panelTop+2)

	store := g.trainerActionStore
	if store == nil {
		ebitenutil.DebugPrintAt(screen, "store=nil", leftX+4, panelTop+24)
		return
	}

	actions := store.List()
	btnY := panelTop + 22
	g.trainerActionRects = g.trainerActionRects[:0]

	// Mandatory actions (REST, UP, DOWN, LEFT, RIGHT) are always shown and non-deletable
	mandatory := []string{"REST", "UP", "DOWN", "LEFT", "RIGHT"}
	isMandatory := func(name string) bool {
		for _, m := range mandatory {
			if m == name {
				return true
			}
		}
		return false
	}

	// [+ New Action]
	newBtn := uiRect{leftX + 4, btnY, trainerPanelW - 8, trainerBtnH}
	drawTrainerButton(screen, newBtn, "+ New Custom Bind", g.textFocus == "newAction")
	g.trainerRects["newAction"] = newBtn
	btnY += trainerBtnH + 4

	// Action list
	for _, a := range actions {
		rowRect := uiRect{leftX + 4, btnY, trainerPanelW - 8, trainerActionRowH}
		on := g.trainerSelectedActionID == a.ID
		if on {
			vector.FillRect(screen, float32(rowRect.x), float32(rowRect.y), float32(rowRect.w), float32(rowRect.h), clrBtnOn, true)
		}
		vector.StrokeRect(screen, float32(rowRect.x), float32(rowRect.y), float32(rowRect.w), float32(rowRect.h), 1, clrGrid, true)
		
		nameTxt := a.Name
		if isMandatory(a.Name) {
			nameTxt = "[M] " + a.Name
		}
		ebitenutil.DebugPrintAt(screen, nameTxt, rowRect.x+4, rowRect.y+2)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("samples:%d", a.Samples), rowRect.x+4, rowRect.y+14)
		if !a.LastTrained.IsZero() {
			ago := time.Since(a.LastTrained)
			ebitenutil.DebugPrintAt(screen, fmt.Sprintf("trained: %ds ago", int(ago.Seconds())), rowRect.x+4, rowRect.y+24)
		}
		g.trainerActionRects = append(g.trainerActionRects, trainerActionRect{actionID: a.ID, rect: rowRect})
		btnY += trainerActionRowH + 2
	}

	// === Center: AVN / Training ===
	centerX := leftX + trainerPanelW + trainerContentPad
	centerW := contentW - trainerContentPad
	centerY := panelTop

	// Draw AVN Panel
	avnY := drawAVNPanel(g, screen, centerX, centerY, centerW)
	centerY = avnY + 12

	if g.trainerCalibrateLoop != nil {
		drawTrainingView(g, screen, centerX, centerY, centerW, rest-(centerY-top)-8, leftX, panelTop, trainerPanelW, actions)
		return
	}

	// Selected Action Detail
	sel := store.ByID(g.trainerSelectedActionID)
	if sel != nil {
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Selected: %s", sel.Name), centerX, centerY)
		centerY += 20
		
		btnW := 80
		if !isMandatory(sel.Name) {
			renameRect := uiRect{centerX, centerY, btnW, trainerBtnH}
			drawTrainerButton(screen, renameRect, "Rename", g.textFocus == "trainerRename")
			g.trainerRects["rename"] = renameRect
			
			deleteRect := uiRect{centerX + btnW + 4, centerY, btnW, trainerBtnH}
			drawTrainerButton(screen, deleteRect, "Delete", false)
			g.trainerRects["delete"] = deleteRect
			centerY += trainerBtnH + 12
		} else {
			ebitenutil.DebugPrintAt(screen, "(Mandatory bind - cannot rename/delete)", centerX, centerY)
			delete(g.trainerRects, "rename")
			delete(g.trainerRects, "delete")
			centerY += 20
		}
	}

	// Start sequence training
	ebitenutil.DebugPrintAt(screen, "Sequence (click actions to add):", centerX, centerY)
	centerY += 18
	seq := g.trainerPlannedSequence
	if len(seq) > 0 {
		seqStr := ""
		for i, aid := range seq {
			if i > 0 { seqStr += " -> " }
			if a := store.ByID(aid); a != nil { seqStr += a.Name } else { seqStr += "?" }
		}
		ebitenutil.DebugPrintAt(screen, seqStr, centerX, centerY)
		centerY += 18
		clearRect := uiRect{centerX, centerY, 80, trainerBtnH}
		drawTrainerButton(screen, clearRect, "Clear Seq", false)
		g.trainerRects["clearSequence"] = clearRect
		centerY += trainerBtnH + 4
	}
	
	trainBtnW := 120
	trainRect := uiRect{centerX, centerY, trainBtnW, trainerBtnH + 4}
	drawTrainerButton(screen, trainRect, "Start Training", false)
	g.trainerRects["calibrate"] = trainRect
}

func drawTrainerButton(screen *ebiten.Image, r uiRect, label string, active bool) {
	fill := clrBtn
	if active {
		fill = clrBtnOn
	}
	vector.FillRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), fill, true)
	vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 1, clrGrid, true)
	ebitenutil.DebugPrintAt(screen, label, r.x+4, r.y+3)
}

func drawAVNPanel(g *gpuGame, screen *ebiten.Image, x, y, w int) int {
	panelH := 180
	vector.FillRect(screen, float32(x), float32(y), float32(w), float32(panelH), clrPanel, true)
	vector.StrokeRect(screen, float32(x), float32(y), float32(w), float32(panelH), 1, clrGrid, true)
	ebitenutil.DebugPrintAt(screen, "ACOUSTIC VECTOR NAVIGATION (AVN)", x+8, y+4)

	if g.avn.Counts == nil {
		g.avn.Counts = make(map[string]int)
	}

	// Live Indicator
	indicatorSize := 130
	ix := x + w - indicatorSize - 12
	iy := y + 24
	drawAVNLiveIndicator(g, screen, ix, iy, indicatorSize)

	// Status & Calibration Buttons
	curY := y + 24
	ebitenutil.DebugPrintAt(screen, "Status:", x+8, curY)
	statusTxt := "NOT CALIBRATED"
	statusClr := color.RGBA{200, 100, 100, 255}
	if g.avn.Calibration.MaxDelta > 0 {
		statusTxt = "READY"
		statusClr = clrHit
	}
	if g.avn.Enabled {
		statusTxt = "ACTIVE"
		statusClr = color.RGBA{0, 255, 255, 255}
	}
	ebitenutil.DebugPrintAt(screen, statusTxt, x+60, curY)
	_ = statusClr

	// Reset Button
	resetRect := uiRect{x + w - indicatorSize - 100, y + 4, 80, 16}
	drawTrainerButton(screen, resetRect, "RESET ALL", false)
	g.trainerRects["avn_reset"] = resetRect

	curY += 20
	btnW := 74
	btnH := 24
	steps := []string{"REST", "UP", "DOWN", "LEFT", "RIGHT"}
	for i, step := range steps {
		bx := x + 8 + i*(btnW+4)
		rect := uiRect{bx, curY, btnW, btnH}
		label := step
		if g.avn.Recording == step {
			label = "STOP"
		}

		// Button color reflects recording state
		fill := clrBtn
		if g.avn.Recording == step { fill = color.RGBA{200, 50, 50, 255} }
		drawTrainerButtonWithColor(screen, rect, label, fill)

		g.trainerRects["avn_rec_"+step] = rect

		count := g.avn.Counts[step]
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("n:%d", count), bx+4, curY+btnH+2)
	}

	curY += 44
	toggleRect := uiRect{x + 8, curY, 120, 28}
	toggleLabel := "Enable AVN"
	if g.avn.Enabled {
		toggleLabel = "Disable AVN"
	}
	drawTrainerButton(screen, toggleRect, toggleLabel, g.avn.Enabled)
	g.trainerRects["avn_toggle"] = toggleRect

	// Metrics
	curY += 36
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Live ΔX: %+.2f", g.avn.Live.RawX), x+8, curY)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Live ΔY: %+.2f", g.avn.Live.RawY), x+8, curY+14)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("SpeedX: %+.2f", g.avn.Live.X), x+140, curY)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("SpeedY: %+.2f", g.avn.Live.Y), x+140, curY+14)

	if g.avn.Live.IsClick {
		vector.DrawFilledCircle(screen, float32(x+w-20), float32(curY+10), 8, color.RGBA{255, 0, 0, 255}, true)
		ebitenutil.DebugPrintAt(screen, "CLICK!", x+w-70, curY+4)
	}

	return y + panelH
}

func drawTrainerButtonWithColor(screen *ebiten.Image, r uiRect, label string, fill color.RGBA) {
	vector.FillRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), fill, true)
	vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 1, clrGrid, true)
	ebitenutil.DebugPrintAt(screen, label, r.x+4, r.y+5)
}

func drawAVNLiveIndicator(g *gpuGame, screen *ebiten.Image, x, y, size int) {
	// Background and Box
	vector.FillRect(screen, float32(x), float32(y), float32(size), float32(size), color.RGBA{10, 10, 15, 255}, true)
	vector.StrokeRect(screen, float32(x), float32(y), float32(size), float32(size), 1, clrGrid, true)

	cx, cy := float32(x+size/2), float32(y+size/2)

	// Axis Labels
	ebitenutil.DebugPrintAt(screen, "U", int(cx)-4, y+2)
	ebitenutil.DebugPrintAt(screen, "D", int(cx)-4, y+size-14)
	ebitenutil.DebugPrintAt(screen, "L", x+2, int(cy)-6)
	ebitenutil.DebugPrintAt(screen, "R", x+size-10, int(cy)-6)

	// Crosshair (faded)
	crossClr := color.RGBA{40, 40, 50, 255}
	vector.StrokeLine(screen, cx, float32(y), cx, float32(y+size), 1, crossClr, true)
	vector.StrokeLine(screen, float32(x), cy, float32(x+size), cy, 1, crossClr, true)

	// Trail
	if len(g.avn.LiveTrail) > 1 {
		for i := 0; i < len(g.avn.LiveTrail)-1; i++ {
			p0 := g.avn.LiveTrail[i]
			p1 := g.avn.LiveTrail[i+1]

			x0 := cx + float32(p0[0]*float64(size/2))
			y0 := cy - float32(p0[1]*float64(size/2))
			x1 := cx + float32(p1[0]*float64(size/2))
			y1 := cy - float32(p1[1]*float64(size/2))

			alpha := uint8(float32(i) / float32(len(g.avn.LiveTrail)) * 150)
			vector.StrokeLine(screen, x0, y0, x1, y1, 1.5, color.RGBA{0, 200, 255, alpha}, true)
		}
	}

	// Live point
	px := cx + float32(g.avn.Live.X*float64(size/2))
	py := cy - float32(g.avn.Live.Y*float64(size/2))

	clr := color.RGBA{0, 255, 255, 255}
	if g.avn.Live.IsClick {
		clr = color.RGBA{255, 50, 50, 255}
	}
	vector.DrawFilledCircle(screen, px, py, 6, clr, true)
	vector.StrokeCircle(screen, px, py, 8, 1, color.RGBA{255, 255, 255, 100}, true)
}

func (g *gpuGame) avnProcessRecording(step string) {
	if len(g.avn.RecBuffer) == 0 {
		log.Printf("[AVN] Recording empty for %s", step)
		return
	}

	// Calculate Centroid of the session
	var sum [3]float64
	for _, v := range g.avn.RecBuffer {
		sum[0] += v[0]
		sum[1] += v[1]
		sum[2] += v[2]
	}
	n := float64(len(g.avn.RecBuffer))
	sessionMean := [3]float64{sum[0] / n, sum[1] / n, sum[2] / n}

	if g.avn.Counts == nil { g.avn.Counts = make(map[string]int) }

	// Cumulative Update: Weighted mean
	updateMean := func(old [3]float64, oldCount int, session [3]float64, sessionCount int) [3]float64 {
		if oldCount == 0 { return session }
		total := float64(oldCount + sessionCount)
		return [3]float64{
			(old[0]*float64(oldCount) + session[0]*float64(sessionCount)) / total,
			(old[1]*float64(oldCount) + session[1]*float64(sessionCount)) / total,
			(old[2]*float64(oldCount) + session[2]*float64(sessionCount)) / total,
		}
	}

	oldCount := g.avn.Counts[step]
	sessionCount := len(g.avn.RecBuffer)

	switch step {
	case "REST":
		g.avn.Calibration.Rest = updateMean(g.avn.Calibration.Rest, oldCount, sessionMean, sessionCount)
	case "UP":
		g.avn.Calibration.Up = updateMean(g.avn.Calibration.Up, oldCount, sessionMean, sessionCount)
	case "DOWN":
		g.avn.Calibration.Down = updateMean(g.avn.Calibration.Down, oldCount, sessionMean, sessionCount)
	case "LEFT":
		g.avn.Calibration.Left = updateMean(g.avn.Calibration.Left, oldCount, sessionMean, sessionCount)
	case "RIGHT":
		g.avn.Calibration.Right = updateMean(g.avn.Calibration.Right, oldCount, sessionMean, sessionCount)
	}

	g.avn.Counts[step] += sessionCount

	// Discovery for Clicks
	maxVel := 0.0
	for _, v := range g.avn.RecVel {
		if math.Abs(v) > maxVel { maxVel = math.Abs(v) }
	}

	log.Printf("[AVN] Processed %s: %d new samples (total %d). MaxVel:%.1f", 
		step, sessionCount, g.avn.Counts[step], maxVel)

	// Update MaxDelta for normalization
	g.avn.Calibration.MaxDelta = g.avnCalcMaxDelta()

	if g.avn.Calibration.ClickVel < 150 {
		g.avn.Calibration.ClickVel = 200
	}

	g.avn.RecBuffer = nil
	g.avn.RecVel = nil
}

func (g *gpuGame) avnCalcMaxDelta() float64 {
	dot := func(a, b [3]float64) float64 {
		return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
	}
	cal := g.avn.Calibration
	vU := [3]float64{cal.Up[0]-cal.Rest[0], cal.Up[1]-cal.Rest[1], cal.Up[2]-cal.Rest[2]}
	vD := [3]float64{cal.Down[0]-cal.Rest[0], cal.Down[1]-cal.Rest[1], cal.Down[2]-cal.Rest[2]}
	vL := [3]float64{cal.Left[0]-cal.Rest[0], cal.Left[1]-cal.Rest[1], cal.Left[2]-cal.Rest[2]}
	vR := [3]float64{cal.Right[0]-cal.Rest[0], cal.Right[1]-cal.Rest[1], cal.Right[2]-cal.Rest[2]}

	maxP := 0.0
	for _, v := range [][3]float64{vU, vD, vL, vR} {
		d := math.Sqrt(dot(v, v))
		if d > maxP { maxP = d }
	}
	return maxP
}

// drawTrainingView draws training screen: instruction + confirm button. Manual mode — no timers.
func drawTrainingView(g *gpuGame, screen *ebiten.Image, x, y, w, h int, leftX, panelTop, panelW int, actions []*Action) {
	loop := g.trainerCalibrateLoop
	if loop == nil {
		return
	}
	idToName := make(map[int]string)
	for _, a := range actions {
		idToName[a.ID] = a.Name
	}

	curName := ""
	if loop.CurrentActionID != 0 {
		curName = idToName[loop.CurrentActionID]
	}
	if curName == "" {
		curName = idToName[g.trainerSelectedActionID]
	}
	if curName == "" {
		curName = "?"
	}

	// Popup dimensions
	popupW := 420
	popupH := h
	if popupW > w-16 {
		popupW = w - 16
	}
	popupX := x + (w-popupW)/2
	popupY := y

	// Popup background
	vector.FillRect(screen, float32(popupX), float32(popupY), float32(popupW), float32(popupH), clrPanel, true)
	vector.StrokeRect(screen, float32(popupX), float32(popupY), float32(popupW), float32(popupH), 2, clrGrid, true)

	// Title
	titleY := popupY + 24
	ebitenutil.DebugPrintAt(screen, "ML DATASET TRAINING", popupX+popupW/2-60, titleY-16)

	// Main text
	textX := popupX + 24
	textY := titleY + 20
	lineH := 22

	var mainText, subText, confirmLabel string
	switch loop.Phase {
	case PhaseRest:
		mainText = fmt.Sprintf("Get ready: %s", curName)
		subText = "Press Ready when prepared"
		confirmLabel = "Ready"
	case PhaseDo:
		mainText = fmt.Sprintf("Perform: %s", curName)
		subText = "Press Done when finished"
		confirmLabel = "Done"
	case PhaseResult:
		mainText = fmt.Sprintf("Recorded: %s — %d events", loop.LastResult, int(loop.LastConf))
		subText = "Press Next to continue"
		confirmLabel = "Next"
	}

	ebitenutil.DebugPrintAt(screen, mainText, textX, textY)
	ebitenutil.DebugPrintAt(screen, subText, textX, textY+lineH)

	// Progress
	if len(loop.PlannedSequence) > 0 {
		seqIdx := len(loop.Sequence)
		seqLen := len(loop.PlannedSequence)
		progress := fmt.Sprintf("Segments: %d / %d", seqIdx, seqLen)
		ebitenutil.DebugPrintAt(screen, progress, textX, textY+lineH*2+8)
	}

	// Buttons row
	btnW := 120
	btnH := 32
	btnY := popupY + popupH - btnH - 20
	confirmRect := uiRect{popupX + (popupW-btnW*2-12)/2, btnY, btnW, btnH}
	cancelRect := uiRect{confirmRect.x + btnW + 12, btnY, btnW, btnH}
	drawTrainerButton(screen, confirmRect, confirmLabel, false)
	drawTrainerButton(screen, cancelRect, "Cancel", false)
	g.trainerRects["calibrateConfirm"] = confirmRect
	g.trainerRects["calibrateCancel"] = cancelRect
}

