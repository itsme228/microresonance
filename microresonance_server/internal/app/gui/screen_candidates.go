// Экран CANDIDATES: список кандидатов (пики/кластеры), прогресс к lock.

package gui

import (
	"fmt"
	"math"
	"sort"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

const (
	candidateCardW    = 180
	candidateCardH    = 72
	candidatePad      = 8
	candidatesMaxShow = 12
)

// DrawCandidatesScreen рисует вкладку CANDIDATES: карточки кандидатов с f_est, amp_norm_est, shape_xy/xz, progress bar.
func DrawCandidatesScreen(g *gpuGame, screen *ebiten.Image, w, h int) {
	if g.candidateStore == nil {
		return
	}
	top := headerH + mainTabRowH
	rest := h - top
	if rest < 100 { return }
	contentW := w - 2*plotPad
	if contentW < 200 { return }

	vector.FillRect(screen, float32(plotPad), float32(top), float32(contentW+plotPad), float32(rest), clrBg, true)

	debugY := top + 4
	ebitenutil.DebugPrintAt(screen, "Detection (FFT X 0.2-2k Hz):", plotPad+4, debugY)
	debugY += 16
	if g.lastRawDetection.Valid {
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("peak_freq: %.1f Hz  peak_amp: %.4f  noise_floor: %.4f  amp_norm: %.2f",
			g.lastRawDetection.PeakFreq, g.lastRawDetection.PeakAmp, g.lastRawDetection.NoiseFloor, g.lastRawDetection.AmpNorm),
			plotPad+4, debugY)
	} else {
		ebitenutil.DebugPrintAt(screen, "peak_freq: —  peak_amp: —  noise_floor: —  amp_norm: —", plotPad+4, debugY)
	}
	debugY += 18
	ebitenutil.DebugPrintAt(screen, "Candidates (amp_norm > 0.8):", plotPad+4, debugY)
	debugY += 18

	list := g.candidateStore.List()
	sort.Slice(list, func(i, j int) bool {
		if list[i].AmpNormEst != list[j].AmpNormEst {
			return list[i].AmpNormEst > list[j].AmpNormEst
		}
		return list[i].HitCount > list[j].HitCount
	})
	if len(list) > candidatesMaxShow {
		list = list[:candidatesMaxShow]
	}

	if len(list) == 0 {
		ebitenutil.DebugPrintAt(screen, "No peaks above threshold (amp_norm > 0.8). See numbers above.", plotPad+4, debugY+8)
		return
	}

	fs := 4000.0
	x, _, _, _ := g.getSelectedSensorData()

	glyphW, glyphH := 100, 50
	cols := (contentW + candidatePad) / (candidateCardW + candidatePad)
	if cols < 1 { cols = 1 }
	startX := plotPad + (contentW-(cols*(candidateCardW+candidatePad)-candidatePad))/2
	if startX < plotPad { startX = plotPad }
	startY := debugY + 8

	for i, c := range list {
		col, row := i % cols, i / cols
		cx, cy := startX + col*(candidateCardW+candidatePad), startY + row*(candidateCardH+candidatePad)
		vector.StrokeRect(screen, float32(cx), float32(cy), float32(candidateCardW), float32(candidateCardH), 1.5, clrGrd, true)
		vector.FillRect(screen, float32(cx+1), float32(cy+1), float32(candidateCardW-2), float32(candidateCardH-2), clrPanel, true)
		status := "unstable"
		if c.StabilityProgress >= 1 { status = "locked" }
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("CANDIDATE (%s)", status), cx+6, cy+4)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("f0=%.1f Hz amp=%.2f hits=%d", c.FEst, c.AmpNormEst, c.HitCount), cx+6, cy+18)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("hold: %.1f s  XY=%.2f XZ=%.2f", c.StableTime, c.ShapeXYEst, c.ShapeXZEst), cx+6, cy+32)
		gx := cx + candidateCardW - glyphW - 6
		points, lowAmp := GetOscilloPointsForCandidate(g, c.ID, c.FEst, x, fs)
		drawOscilloGlyph(screen, gx, cy+4, glyphW, glyphH, points, c.FEst, c.AmpNormEst, c.StabilityProgress >= 1, lowAmp)
		barY, barW, barH := cy + candidateCardH - 14, candidateCardW - 12, 8.0
		vector.StrokeRect(screen, float32(cx+6), float32(barY), float32(barW), float32(barH), 1, clrGrd, true)
		prog := c.StabilityProgress
		if math.IsNaN(prog) || prog < 0 { prog = 0 }
		if prog > 1 { prog = 1 }
		fillW := int(float64(barW-2) * prog)
		if fillW > 0 {
			vector.FillRect(screen, float32(cx+7), float32(barY+1), float32(fillW), float32(barH-2), clrAuto, true)
		}
		ebitenutil.DebugPrintAt(screen, "lock", cx+6, barY-10)
	}
}
