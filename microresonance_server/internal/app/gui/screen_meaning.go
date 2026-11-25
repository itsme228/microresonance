// MEANING screen: token table + occurrence counter (TF model), reset button.

package gui

import (
	"fmt"
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

const (
	meaningTableRowH = 22
	meaningBtnH      = 26
)

var (
	clrConfHigh = color.RGBA{50, 200, 50, 255} // green (>90%)
	clrConfLow  = color.RGBA{200, 80, 80, 255} // red (<90%)
)

// actionNamesFromStore returns action names from trainer.
func actionNamesFromStore(g *gpuGame) []string {
	if g.trainerActionStore == nil {
		return nil
	}
	list := g.trainerActionStore.List()
	names := make([]string, 0, len(list))
	for _, a := range list {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	return names
}

// DrawMeaningScreen draws MEANING tab: table (token | counter) + Reset button.
func DrawMeaningScreen(g *gpuGame, screen *ebiten.Image, w, h int) {
	top := headerH + mainTabRowH
	rest := h - top
	if rest < 80 {
		return
	}
	contentW := w - 2*plotPad
	if contentW < 120 {
		contentW = w - 2*plotPad
	}

	g.meaningContentTop = top
	g.meaningContentH = rest

	vector.FillRect(screen, float32(plotPad), float32(top), float32(contentW+plotPad), float32(rest), clrBg, true)

	x := plotPad + 4
	y := top + 4

	actions := actionNamesFromStore(g)
	name, conf := g.mlLastPrediction.Get()

	// Model status
	if g.mlWorker == nil {
		if MLModelExists() {
			ebitenutil.DebugPrintAt(screen, "MEANING: saved_model exists, loading…", x, y)
		} else {
			ebitenutil.DebugPrintAt(screen, "MEANING: train model (scripts/train/train_glyph_model.py), create actions in TRAIN.", x, y)
		}
	} else if name == "" && conf == 0 {
		ebitenutil.DebugPrintAt(screen, "MEANING: model loaded, waiting for events…", x, y)
	} else {
		ebitenutil.DebugPrintAt(screen, "Model running. Recognition below.", x, y)
	}
	y += meaningTableRowH

	// Confident predictions (>90%) — green on left
	confY := y
	if g.mlWorker != nil && name != "" && conf >= 0.90 {
		vector.FillRect(screen, float32(x-2), float32(confY-2), 180, float32(meaningTableRowH), clrConfHigh, true)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("OK: %s (%.0f%%)", name, conf*100), x, confY)
	}

	// Low-confidence predictions (<90%) — red on right
	lowConfX := x + 200
	if g.mlWorker != nil && name != "" && conf < 0.90 {
		vector.FillRect(screen, float32(lowConfX-2), float32(confY-2), 180, float32(meaningTableRowH), clrConfLow, true)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("?: %s (%.0f%%)", name, conf*100), lowConfX, confY)
	}
	y += meaningTableRowH

	ebitenutil.DebugPrintAt(screen, "For recognition: enable DISCOVERY (tab or am-sine mode).", x, y)
	y += meaningTableRowH + 4

	if len(actions) == 0 {
		ebitenutil.DebugPrintAt(screen, "Create actions in TRAIN tab (Actions).", x, y)
		return
	}

	// Table header
	ebitenutil.DebugPrintAt(screen, "Token", x, y)
	ebitenutil.DebugPrintAt(screen, "Counter", x+180, y)
	y += meaningTableRowH

	// Rows: action name | counter
	g.meaningCountersMu.Lock()
	counters := make(map[string]int)
	for k, v := range g.meaningCounters {
		counters[k] = v
	}
	g.meaningCountersMu.Unlock()

	for _, name := range actions {
		cnt := counters[name]
		ebitenutil.DebugPrintAt(screen, name, x, y)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%d", cnt), x+180, y)
		y += meaningTableRowH
	}

	// Reset button
	btnW := 90
	btnY := y + 8
	resetRect := uiRect{x: x, y: btnY, w: btnW, h: meaningBtnH}
	if g.meaningRects == nil {
		g.meaningRects = make(map[string]uiRect)
	}
	g.meaningRects["meaningReset"] = resetRect
	vector.FillRect(screen, float32(resetRect.x), float32(resetRect.y), float32(resetRect.w), float32(resetRect.h), clrBtn, true)
	vector.StrokeRect(screen, float32(resetRect.x), float32(resetRect.y), float32(resetRect.w), float32(resetRect.h), 1, clrGrid, true)
	ebitenutil.DebugPrintAt(screen, "Reset", resetRect.x+20, resetRect.y+5)
}
