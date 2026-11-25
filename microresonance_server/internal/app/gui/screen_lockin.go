// LOCKIN screen: 3-axis independent lock-in amplifier dashboard.
// Shows per-axis FFT waterfalls, lock-in magnitudes/phases, differential phases,
// and synchronization parameter log for BCI signal diagnostics.

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

const (
	lockinHeatMaxRows = 80
	lockinHistMaxLen  = 120
)

// updateLockinHistory refreshes per-axis FFT waterfalls and lock-in result histories for the selected sensor.
func (g *gpuGame) updateLockinHistory(sid int, sensorPkts uint64) {
	st := g.getSensorState(sid)

	if sensorPkts <= st.lockinLastPkts {
		return
	}
	st.lockinLastPkts = sensorPkts

	// Ensure FFT cache is fresh for this sensor.
	if len(st.scanFFTMagX) == 0 {
		x, y, z, ads := g.getSelectedSensorData()
		g.refreshScanFFTCache(x, y, z, ads, 4000.0, 40000.0, sensorPkts)
	}

	n := len(st.scanFFTMagX)
	if n == 0 {
		return
	}

	// Helper: append one FFT row into a per-axis ring buffer.
	appendHeatRow := func(buf *[][]float64, head *int, mag []float64) {
		if len(mag) != n {
			return
		}
		maxMag := 1e-12
		for _, v := range mag {
			if v > maxMag {
				maxMag = v
			}
		}

		if st.waterfallMaxMag < 1e-12 {
			st.waterfallMaxMag = maxMag
		} else {
			if maxMag > st.waterfallMaxMag {
				st.waterfallMaxMag = st.waterfallMaxMag*0.5 + maxMag*0.5
			} else {
				st.waterfallMaxMag = st.waterfallMaxMag*0.95 + maxMag*0.05
			}
		}

		dbRef := 20 * math.Log10(st.waterfallMaxMag)
		row := make([]float64, n)
		for i, v := range mag {
			db := 20*math.Log10(v+1e-12) - dbRef
			t := 1.0 + db/60.0
			if t < 0 {
				t = 0
			} else if t > 1 {
				t = 1
			}
			row[i] = t
		}
		if len(*buf) < lockinHeatMaxRows {
			*buf = append(*buf, row)
			if len(*buf) == lockinHeatMaxRows {
				*head = 0
			}
		} else {
			(*buf)[*head] = row
			*head = (*head + 1) % lockinHeatMaxRows
		}
	}

	appendHeatRow(&st.lockinHeatX, &st.lockinHeatHead[0], st.scanFFTMagX)
	appendHeatRow(&st.lockinHeatY, &st.lockinHeatHead[1], st.scanFFTMagY)
	appendHeatRow(&st.lockinHeatZ, &st.lockinHeatHead[2], st.scanFFTMagZ)

	// Update live numeric histories (selected sensor only).
	g.buf.Mu.Lock()
	s := g.buf.Sensors[uint8(sid)]
	if s != nil {
		appendBoundedHist := func(h *[]float64, v float64) {
			*h = append(*h, v)
			if len(*h) > lockinHistMaxLen {
				*h = (*h)[len(*h)-lockinHistMaxLen:]
			}
		}
		appendBoundedHist(&g.lockinHistMagX, s.LockinXMag)
		appendBoundedHist(&g.lockinHistMagY, s.LockinYMag)
		appendBoundedHist(&g.lockinHistMagZ, s.LockinMag)
		appendBoundedHist(&g.lockinHistDXY, s.LockinDeltaXY)
		appendBoundedHist(&g.lockinHistDYZ, s.LockinDeltaYZ)
		appendBoundedHist(&g.lockinHistDXZ, s.LockinDeltaXZ)
	}
	g.buf.Mu.Unlock()
}

func DrawLockinScreen(g *gpuGame, screen *ebiten.Image, w, h int) {
	sid := int(g.getSelectedSensorID())
	st := g.getSensorState(sid)

	g.buf.Mu.Lock()
	s := g.buf.Sensors[uint8(sid)]
	var sensorPkts uint64
	if s != nil {
		sensorPkts = s.Packets
	}
	g.buf.Mu.Unlock()

	g.updateLockinHistory(sid, sensorPkts)

	top := headerH + mainTabRowH + contentTopGap
	top = g.drawSensorDropdown(screen, plotPad, top, w) + 4
	rest := h - top - 4
	if rest < 80 {
		return
	}

	// Layout
	splitX := w * 65 / 100
	leftW := splitX - plotPad*2
	rightX := splitX + plotPad
	rightW := w - rightX - plotPad

	vector.FillRect(screen, float32(plotPad), float32(top), float32(w-2*plotPad), float32(rest+4), clrBg, true)

	waterfallH := (rest - 8) / 3
	if waterfallH < 40 {
		waterfallH = 40
	}

	fs := 4000.0
	if g.store != nil {
		fs = float64(g.store.GetReceiver().AdxlRate)
	}

	g.buf.Mu.Lock()
	metrics := g.buf.ComputeSyncMetrics(450.0)
	var sCopy *signal.SensorData
	if s != nil {
		cp := *s
		sCopy = &cp
	}
	g.buf.Mu.Unlock()

	// emitterHz: where the physical signal actually appears in the FFT spectrum.
	// demodHz: where the server lock-in is computing IQ (may differ when ReceiverLinked=false).
	emitterHz := g.protocol.Frequency // snapped emitter frequency
	demodHz := 0.0
	if sCopy != nil && sCopy.LastDsp != nil {
		demodHz = float64(sCopy.LastDsp.DemodFreqHz)
	}
	if math.Abs(demodHz-emitterHz) < 0.1 {
		demodHz = 0 // same freq — no second marker needed
	}

	// Draw waterfalls
	g.drawLockinWaterfall(screen, st.lockinHeatX, st.lockinHeatHead[0], st.scanFFTFreqAdxl, fs, emitterHz, demodHz, plotPad, top, leftW, waterfallH, 0, "X", clrX)
	g.drawLockinWaterfall(screen, st.lockinHeatY, st.lockinHeatHead[1], st.scanFFTFreqAdxl, fs, emitterHz, demodHz, plotPad, top+(waterfallH+4), leftW, waterfallH, 1, "Y", clrY)
	g.drawLockinWaterfall(screen, st.lockinHeatZ, st.lockinHeatHead[2], st.scanFFTFreqAdxl, fs, emitterHz, demodHz, plotPad, top+2*(waterfallH+4), leftW, waterfallH, 2, "Z", clrZ)

	// Metrics panels
	panelY := top
	magH := 80
	if sCopy != nil {
		drawLockinMagBars(screen, sCopy.LockinXMag, sCopy.LockinYMag, sCopy.LockinMag, rightX, panelY, rightW, magH)
	}
	panelY += magH + 4

	tableH := 75
	drawLockinPhaseTable(screen, sCopy, rightX, panelY, rightW, tableH)
	panelY += tableH + 4

	deltaH := 120
	drawLockinDeltaPhase(screen, sCopy, g.lockinHistDXY, g.lockinHistDYZ, g.lockinHistDXZ, rightX, panelY, rightW, deltaH)
	panelY += deltaH + 4

	syncH := h - panelY - 15
	if syncH > 40 {
		drawLockinSyncLog(screen, metrics, sCopy, rightX, panelY, rightW, syncH)
	}

	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("pkts=%d S%d", sensorPkts, sid), plotPad+2, h-12)
}

func (g *gpuGame) drawLockinWaterfall(screen *ebiten.Image, buf [][]float64, head int, freqs []float64, fs, emitterHz, demodHz float64, x0, y0, w, h int, axisIdx int, label string, clr color.RGBA) {
	innerW, innerH := w-4, h-16
	if innerW < 8 || innerH < 4 {
		return
	}
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)
	ebitenutil.DebugPrintAt(screen, "WATERFALL "+label, x0+2, y0+1)
	numRows := len(buf)
	if numRows == 0 || len(freqs) < 2 {
		return
	}

	fMax := fs / 2
	fMinPlot := freqMinHz
	logMin, logMax := math.Log10(fMinPlot), math.Log10(fMax)

	binMap := make([]int, innerW)
	bj := 1
	for xi := 0; xi < innerW; xi++ {
		t := float64(xi) / float64(innerW-1)
		f := math.Pow(10, logMin+t*(logMax-logMin))
		for bj < len(freqs)-1 && freqs[bj] < f {
			bj++
		}
		binMap[xi] = bj
	}

	needed := innerW * innerH * 4
	if cap(g.lockinHeatPix) < needed {
		g.lockinHeatPix = make([]byte, needed)
	}
	pix := g.lockinHeatPix[:needed]

	maxDisp := numRows
	for yi := 0; yi < innerH; yi++ {
		rowIdx := int(float64(yi) * float64(maxDisp) / float64(innerH))
		if rowIdx >= maxDisp {
			rowIdx = maxDisp - 1
		}
		var bufIdx int
		if numRows < lockinHeatMaxRows {
			bufIdx = rowIdx
		} else {
			bufIdx = (head + rowIdx) % lockinHeatMaxRows
		}
		row := buf[bufIdx]
		base := yi * innerW * 4
		for xi := 0; xi < innerW; xi++ {
			b := binMap[xi]
			mag := 0.0
			if b < len(row) {
				mag = row[b]
			}
			cr, cg, cb := heatmapColor(mag)
			off := base + xi*4
			pix[off], pix[off+1], pix[off+2], pix[off+3] = cr, cg, cb, 255
		}
	}
	img := g.lockinHeatImgs[axisIdx]
	if img == nil || img.Bounds().Dx() != innerW || img.Bounds().Dy() != innerH {
		if img != nil {
			img.Deallocate()
		}
		img = ebiten.NewImage(innerW, innerH)
		g.lockinHeatImgs[axisIdx] = img
	}
	img.WritePixels(pix)
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Translate(float64(x0+2), float64(y0+14))
	screen.DrawImage(img, op)

	// Emitter frequency marker (bright): where the signal peak should appear in the waterfall.
	if emitterHz > fMinPlot && emitterHz < fMax {
		t := (math.Log10(emitterHz) - logMin) / (logMax - logMin)
		cx := x0 + 2 + int(t*float64(innerW))
		vector.StrokeLine(screen, float32(cx), float32(y0+14), float32(cx), float32(y0+14+innerH), 1.5, color.RGBA{clr.R, clr.G, clr.B, 200}, true)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%.0f", emitterHz), cx-12, y0+14)
	}
	// Demod frequency marker (dim cyan): where the lock-in demodulates — shown only when different from emitter.
	if demodHz > fMinPlot && demodHz < fMax {
		t := (math.Log10(demodHz) - logMin) / (logMax - logMin)
		cx := x0 + 2 + int(t*float64(innerW))
		vector.StrokeLine(screen, float32(cx), float32(y0+14), float32(cx), float32(y0+14+innerH), 1.0, color.RGBA{80, 220, 220, 160}, true)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("D%.0f", demodHz), cx-12, y0+14+8)
	}
	g.drawToneMarkers(screen, g.toneMarkerFreqs(), x0+2, y0+14, innerW, innerH, fMinPlot, fMax, true)
}

func drawLockinMagBars(screen *ebiten.Image, magX, magY, magZ float64, x0, y0, w, h int) {
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)
	ebitenutil.DebugPrintAt(screen, "MAGNITUDE (g)", x0+2, y0+1)
	barH := (h - 20) / 3
	maxMag := math.Max(magX, math.Max(magY, magZ))
	if maxMag < 1e-9 {
		maxMag = 1e-9
	}
	axes := []struct {
		label string
		mag   float64
		clr   color.RGBA
	}{
		{"X", magX, clrX}, {"Y", magY, clrY}, {"Z", magZ, clrZ},
	}
	barW := w - 80
	for i, ax := range axes {
		by := y0 + 18 + i*(barH+4)
		fill := int((ax.mag / maxMag) * float64(barW))
		if fill < 0 {
			fill = 0
		} else if fill > barW {
			fill = barW
		}
		vector.StrokeRect(screen, float32(x0+30), float32(by), float32(barW), float32(barH-2), 1, clrGrid, true)
		if fill > 0 {
			vector.FillRect(screen, float32(x0+30), float32(by), float32(fill), float32(barH-2), ax.clr, true)
		}
		ebitenutil.DebugPrintAt(screen, ax.label, x0+2, by)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%.4f", ax.mag), x0+30+barW+4, by)
	}
}

func drawLockinPhaseTable(screen *ebiten.Image, s *signal.SensorData, x0, y0, w, h int) {
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)
	ebitenutil.DebugPrintAt(screen, "PHASE DATA", x0+2, y0+1)
	if s == nil {
		return
	}
	type row struct {
		label string
		mag   float64
		ph    float64
		clr   color.RGBA
	}
	rows := []row{
		{"X", s.LockinXMag, s.LockinXPhaseDeg, clrX},
		{"Y", s.LockinYMag, s.LockinYPhaseDeg, clrY},
		{"Z", s.LockinMag, s.LockinPhase * 180 / math.Pi, clrZ},
	}
	for i, r := range rows {
		ly := y0 + 16 + i*14
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%s: %6.1f deg", r.label, r.ph), x0+4, ly)
	}
}

func drawLockinDeltaPhase(screen *ebiten.Image, s *signal.SensorData, hX, hY, hZ []float64, x0, y0, w, h int) {
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)
	ebitenutil.DebugPrintAt(screen, "DELTA PHASE", x0+2, y0+1)
	if s == nil {
		return
	}
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("XY: %+7.1f YZ: %+7.1f", s.LockinDeltaXY, s.LockinDeltaYZ), x0+4, y0+16)
}

func drawLockinSyncLog(screen *ebiten.Image, m signal.SyncMetrics, s *signal.SensorData, x0, y0, w, h int) {
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)
	ebitenutil.DebugPrintAt(screen, "SYNC LOG", x0+2, y0+1)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("PLL: %v Vel: %.1f", m.PLLActive, m.PhaseVelocityDegSec), x0+4, y0+16)
}
