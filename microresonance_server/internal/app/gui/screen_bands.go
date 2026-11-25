// Экран ПОЛОСЫ: разбор по частотным диапазонам (SDR-стиль).
// IIR bandpass в time-domain — каузальный, без FFT/IFFT. Каждый band = канал наблюдения.
// Реалтайм: по одному графику на полосу, X/Y/Z разными цветами + по квадратам (X²+Y²+Z²) отдельным цветом.

package gui

import (
	"fmt"
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"server/internal/domain/signal"
)

type BandDef = signal.BandDef

var BandsFilterbank = signal.BandsFilterbank

var BandFreqToleranceHz = signal.BandFreqToleranceHz

var (
	clrBandX = color.RGBA{0xf4, 0x4e, 0x4e, 0xff}
	clrBandY = color.RGBA{0x4e, 0xc4, 0x50, 0xff}
	clrBandZ = color.RGBA{0x42, 0xa5, 0xf5, 0xff}
	clrSq    = color.RGBA{0xff, 0xeb, 0x3b, 0xff}
)

const bandsMaxDrawPoints = 192

func drawBandPlot(screen *ebiten.Image, x0, y0, w, h int, xB, yB, zB, squared []float64, label string) {
	if w < 4 || h < 4 { return }
	pw := w - 4
	if pw < 2 { pw = 2 }
	if pw > bandsMaxDrawPoints { pw = bandsMaxDrawPoints }
	xD := downSampleScan(xB, pw)
	yD := downSampleScan(yB, pw)
	zD := downSampleScan(zB, pw)
	sqD := downSampleScan(squared, pw)
	if len(xD) < 2 { return }
	vMin, vMax := xD[0], xD[0]
	for _, v := range xD { if v < vMin { vMin = v }; if v > vMax { vMax = v } }
	for _, v := range yD { if v < vMin { vMin = v }; if v > vMax { vMax = v } }
	for _, v := range zD { if v < vMin { vMin = v }; if v > vMax { vMax = v } }
	for _, v := range sqD { if v < vMin { vMin = v }; if v > vMax { vMax = v } }
	span := vMax - vMin
	if span < 1e-12 { span = 1 }
	innerH := float64(h - 6)
	innerW := float64(w - 4)
	if innerW < 2 { innerW = 2 }
	dxPx := innerW / float64(len(xD)-1)
	normY := func(v float64) float32 {
		t := (v - vMin) / span
		if t < 0 { t = 0 }
		if t > 1 { t = 1 }
		return float32(y0) + 3 + float32((1-t)*innerH)
	}
	drawTrace := func(d []float64, clr color.Color) {
		if len(d) < 2 { return }
		for i := 0; i < len(d)-1; i++ {
			px0 := float32(x0 + 2 + int(dxPx*float64(i)))
			px1 := float32(x0 + 2 + int(dxPx*float64(i+1)))
			vector.StrokeLine(screen, px0, normY(d[i]), px1, normY(d[i+1]), strokeW, clr, true)
		}
	}
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)
	ebitenutil.DebugPrintAt(screen, label, x0+2, y0+1)
	drawTrace(xD, clrBandX)
	drawTrace(yD, clrBandY)
	drawTrace(zD, clrBandZ)
	drawTrace(sqD, clrSq)
}

func drawBandPlotZoomed(screen *ebiten.Image, x0, y0, w, h int, xB, yB, zB, squared []float64, label string, vertScale float64) {
	if w < 4 || h < 4 || vertScale < 1e-6 { return }
	pw := w - 4
	if pw < 2 { pw = 2 }
	if pw > bandsMaxDrawPoints { pw = bandsMaxDrawPoints }
	xD := downSampleScan(xB, pw)
	yD := downSampleScan(yB, pw)
	zD := downSampleScan(zB, pw)
	sqD := downSampleScan(squared, pw)
	if len(xD) < 2 { return }
	vMin, vMax := xD[0], xD[0]
	for _, v := range xD { if v < vMin { vMin = v }; if v > vMax { vMax = v } }
	for _, v := range yD { if v < vMin { vMin = v }; if v > vMax { vMax = v } }
	for _, v := range zD { if v < vMin { vMin = v }; if v > vMax { vMax = v } }
	for _, v := range sqD { if v < vMin { vMin = v }; if v > vMax { vMax = v } }
	fullSpan := vMax - vMin
	if fullSpan < 1e-12 { fullSpan = 1 }
	var sum float64
	n := 0
	for _, v := range xD { sum += v; n++ }
	for _, v := range yD { sum += v; n++ }
	for _, v := range zD { sum += v; n++ }
	for _, v := range sqD { sum += v; n++ }
	center := sum / float64(n)
	span := fullSpan / vertScale
	if span < 1e-12 { span = 1 }
	vMin = center - span/2
	vMax = center + span/2
	span = vMax - vMin
	if span < 1e-12 { span = 1 }
	innerH := float64(h - 6)
	innerW := float64(w - 4)
	if innerW < 2 { innerW = 2 }
	dxPx := innerW / float64(len(xD)-1)
	normY := func(v float64) float32 {
		t := (v - vMin) / span
		if t < 0 { t = 0 }
		if t > 1 { t = 1 }
		return float32(y0) + 3 + float32((1-t)*innerH)
	}
	drawTrace := func(d []float64, clr color.Color) {
		if len(d) < 2 { return }
		for i := 0; i < len(d)-1; i++ {
			px0 := float32(x0 + 2 + int(dxPx*float64(i)))
			px1 := float32(x0 + 2 + int(dxPx*float64(i+1)))
			vector.StrokeLine(screen, px0, normY(d[i]), px1, normY(d[i+1]), strokeW, clr, true)
		}
	}
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)
	ebitenutil.DebugPrintAt(screen, label, x0+2, y0+1)
	drawTrace(xD, clrBandX)
	drawTrace(yD, clrBandY)
	drawTrace(zD, clrBandZ)
	drawTrace(sqD, clrSq)
}

func DrawBandsScreen(g *gpuGame, screen *ebiten.Image, w, h int) {
	top := headerH + mainTabRowH + contentTopGap
	top = g.drawSensorDropdown(screen, 8, top, w) + 4
	left := 8
	restH := h - top - 8
	if restH < 80 {
		ebitenutil.DebugPrintAt(screen, "BANDS: IIR bandpass (time-domain). Limited space for charts.", left, top+8)
		return
	}
	fs := 4000.0
	x, y, z, _ := g.getSelectedSensorData()
	nBands := len(BandsFilterbank)
	plotH := restH / nBands
	if plotH < 32 { plotH = 32 }
	plotW := w - 2*left
	if plotW < 100 { plotW = 100 }
	if g.bandFilters == nil || math.Abs(g.bandFiltersFs-fs) > 1 {
		g.bandFilters = make([]signal.BandpassFilter, nBands)
		for i := range BandsFilterbank {
			b := &BandsFilterbank[i]
			g.bandFilters[i] = signal.NewBandpassFilter(b.LoHz, b.HiHz, fs)
		}
		g.bandFiltersFs = fs
	}
	if len(x) < 16 || len(y) < 16 || len(z) < 16 {
		ebitenutil.DebugPrintAt(screen, "BANDS: no data (buffer empty).", left, top+8)
		return
	}
	const bandsMaxSamples = 4096
	n := len(x)
	if len(y) < n { n = len(y) }
	if len(z) < n { n = len(z) }
	if n > bandsMaxSamples {
		off := n - bandsMaxSamples
		x, y, z = x[off:], y[off:], z[off:]
		n = bandsMaxSamples
	}
	if cap(g.bandsPlotRects) < nBands {
		g.bandsPlotRects = make([]uiRect, nBands)
	}
	g.bandsPlotRects = g.bandsPlotRects[:nBands]
	for i := 0; i < nBands; i++ {
		g.bandFilters[i].Reset()
		xB, yB, zB, squared := g.bandFilters[i].Filter(x, y, z)
		g.bandsPlotRects[i] = uiRect{x: left, y: top + 8 + i*plotH, w: plotW, h: plotH}
		if g.rawPopupOpen && g.rawPopupBandIndex == i {
			if g.rawPopupBandData == nil {
				g.rawPopupBandData = &rawPopupBandData{}
			}
			g.rawPopupBandData.xB = signal.CopyFloats(xB, g.rawPopupBandData.xB)
			g.rawPopupBandData.yB = signal.CopyFloats(yB, g.rawPopupBandData.yB)
			g.rawPopupBandData.zB = signal.CopyFloats(zB, g.rawPopupBandData.zB)
			g.rawPopupBandData.squared = signal.CopyFloats(squared, g.rawPopupBandData.squared)
		}
		label := fmt.Sprintf("%s %.1f–%.0f Hz", BandsFilterbank[i].Label, BandsFilterbank[i].LoHz, BandsFilterbank[i].HiHz)
		drawBandPlot(screen, left, top+8+i*plotH, plotW, plotH, xB, yB, zB, squared, label)
	}
	if g.rawPopupOpen && g.rawPopupBandIndex >= 0 && g.rawPopupBandIndex < nBands {
		popupW := w - 80
		if popupW > 900 { popupW = 900 }
		popupH := restH
		if popupH > 400 { popupH = 400 }
		popupX, popupY := (w-popupW)/2, (h-popupH)/2
		vector.FillRect(screen, float32(popupX), float32(popupY), float32(popupW), float32(popupH), clrPanel, true)
		vector.StrokeRect(screen, float32(popupX), float32(popupY), float32(popupW), float32(popupH), 2, clrAcc, true)
		graphPad, btnRowH := 8, 24
		graphH, graphW := popupH-40-btnRowH, popupW-2*graphPad
		graphX, graphY := popupX+graphPad, popupY+20
		bandIdx := g.rawPopupBandIndex
		label := fmt.Sprintf("%s %.1f–%.0f Hz (zoom x3)", BandsFilterbank[bandIdx].Label, BandsFilterbank[bandIdx].LoHz, BandsFilterbank[bandIdx].HiHz)
		ebitenutil.DebugPrintAt(screen, label, graphX, popupY+4)
		var xB, yB, zB, sq []float64
		if g.rawPopupHold && g.rawPopupSnapshot != nil && len(g.rawPopupSnapshot.xB) > 0 {
			xB, yB, zB, sq = g.rawPopupSnapshot.xB, g.rawPopupSnapshot.yB, g.rawPopupSnapshot.zB, g.rawPopupSnapshot.squared
		} else if g.rawPopupBandData != nil {
			xB, yB, zB, sq = g.rawPopupBandData.xB, g.rawPopupBandData.yB, g.rawPopupBandData.zB, g.rawPopupBandData.squared
		}
		if len(xB) > 0 {
			drawBandPlotZoomed(screen, graphX, graphY, graphW, graphH, xB, yB, zB, sq, "", 3.0)
		}
		btnY := popupY + popupH - btnRowH - 4
		holdW, holdH, closeW := 56, 18, 48
		g.rawPopupHoldRect = uiRect{x: popupX + graphPad, y: btnY, w: holdW, h: holdH}
		g.rawPopupCloseRect = uiRect{x: popupX + popupW - graphPad - closeW, y: btnY, w: closeW, h: holdH}
		holdFill := clrBtn
		if g.rawPopupHold { holdFill = clrBtnOn }
		vector.FillRect(screen, float32(g.rawPopupHoldRect.x), float32(g.rawPopupHoldRect.y), float32(holdW), float32(holdH), holdFill, true)
		vector.StrokeRect(screen, float32(g.rawPopupHoldRect.x), float32(g.rawPopupHoldRect.y), float32(holdW), float32(holdH), 1, clrGrid, true)
		ebitenutil.DebugPrintAt(screen, "Hold", g.rawPopupHoldRect.x+4, g.rawPopupHoldRect.y+2)
		vector.FillRect(screen, float32(g.rawPopupCloseRect.x), float32(g.rawPopupCloseRect.y), float32(closeW), float32(holdH), clrBtn, true)
		vector.StrokeRect(screen, float32(g.rawPopupCloseRect.x), float32(g.rawPopupCloseRect.y), float32(closeW), float32(holdH), 1, clrGrid, true)
		ebitenutil.DebugPrintAt(screen, "Close", g.rawPopupCloseRect.x+4, g.rawPopupCloseRect.y+2)
	}
	ly := top + 8 + nBands*plotH
	if ly < h-20 {
		ebitenutil.DebugPrintAt(screen, "X red  Y green  Z blue  X²+Y²+Z² yellow  | IIR bandpass, time-domain | click chart to zoom", left, ly)
	}
}
