// Экран SCAN: осциллограммы X/Y/Z + ADS и FFT-спектры.
// Вкладка для машины и отладки — частоты, оси, спектры.

package gui

import (
	"fmt"
	"image/color"
	"math"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"server/internal/domain/signal"
)

const (
	scanMaxWavePoints     = 240
	scanMaxSpectrumPoints = 220
	scanHeatmapMaxRows    = 80
)

func downSampleScan(d []float64, w int) []float64 {
	if len(d) <= w || w <= 1 {
		return d
	}
	out := make([]float64, w)
	step := float64(len(d)-1) / float64(w-1)
	for i := 0; i < w; i++ {
		idx := int(step * float64(i))
		if idx >= len(d) {
			idx = len(d) - 1
		}
		out[i] = d[idx]
	}
	return out
}

func copyFloatSlice(in []float64) []float64 {
	if len(in) == 0 {
		return nil
	}
	out := make([]float64, len(in))
	copy(out, in)
	return out
}

func maxAbsScan(d []float64) float64 {
	m := 0.0
	for _, v := range d {
		if math.Abs(v) > m {
			m = math.Abs(v)
		}
	}
	return m
}

func heatmapColor(t float64) (uint8, uint8, uint8) {
	type stop struct {
		t       float64
		r, g, b uint8
	}
	stops := []stop{
		{0.0, 0, 0, 0},
		{0.2, 0, 0, 100},
		{0.4, 0, 200, 200},
		{0.6, 0, 255, 0},
		{0.8, 255, 255, 0},
		{1.0, 255, 0, 0},
	}
	if t <= 0 {
		return 0, 0, 0
	}
	if t >= 1 {
		return 255, 255, 255
	}
	for i := 1; i < len(stops); i++ {
		if t <= stops[i].t {
			lo, hi := stops[i-1], stops[i]
			u := float32((t - lo.t) / (hi.t - lo.t))
			lerp := func(x, y uint8) uint8 {
				return uint8(float32(x)*(1-u) + float32(y)*u)
			}
			return lerp(lo.r, hi.r), lerp(lo.g, hi.g), lerp(lo.b, hi.b)
		}
	}
	return 255, 255, 255
}

func (g *gpuGame) refreshScanFFTCache(x, y, z, ads []float64, adxlFs, adsFs float64, currentPkts uint64) {
	now := time.Now()
	fftN := g.fftSize
	sid := int(g.getSelectedSensorID())
	st := g.getSensorState(sid)

	if now.Sub(st.scanFFTCacheAt) < 16*time.Millisecond && st.scanFFTCachePkts == currentPkts {
		return
	}
	if g.scanFFTScratch == nil {
		g.scanFFTScratch = &signal.FFTScratch{}
	}

	magX, freqsAdxl := signal.ComputeFFTScanReuse(g.scanFFTScratch, x, fftN, adxlFs)
	st.scanFFTMagX = copyFloatSlice(magX)
	st.scanFFTFreqAdxl = copyFloatSlice(freqsAdxl)
	magY, _ := signal.ComputeFFTScanReuse(g.scanFFTScratch, y, fftN, adxlFs)
	st.scanFFTMagY = copyFloatSlice(magY)
	magZ, _ := signal.ComputeFFTScanReuse(g.scanFFTScratch, z, fftN, adxlFs)
	st.scanFFTMagZ = copyFloatSlice(magZ)
	magADS, freqsADS := signal.ComputeFFTScanReuse(g.scanFFTScratch, ads, fftN, adsFs)
	st.scanFFTMagADS = copyFloatSlice(magADS)
	st.scanFFTFreqADS = copyFloatSlice(freqsADS)
	st.scanFFTCacheAt = now
	st.scanFFTCachePkts = currentPkts
}

func (g *gpuGame) updateScanHistory(sid int, sensorPkts uint64) {
	st := g.getSensorState(sid)

	if sensorPkts <= st.lastHeatmapPkts {
		return
	}
	st.lastHeatmapPkts = sensorPkts

	if len(st.scanFFTMagX) == 0 {
		x, y, z, ads := g.getSelectedSensorData()
		g.refreshScanFFTCache(x, y, z, ads, 4000.0, 40000.0, sensorPkts)
	}
	n := len(st.scanFFTMagX)
	if n == 0 {
		return
	}

	maxCombined := 1e-12
	for i := 0; i < n; i++ {
		c := math.Sqrt(st.scanFFTMagX[i]*st.scanFFTMagX[i] + st.scanFFTMagY[i]*st.scanFFTMagY[i] + st.scanFFTMagZ[i]*st.scanFFTMagZ[i])
		if c > maxCombined {
			maxCombined = c
		}
	}

	if st.waterfallMaxMag < 1e-12 {
		st.waterfallMaxMag = maxCombined
	} else {
		if maxCombined > st.waterfallMaxMag {
			st.waterfallMaxMag = st.waterfallMaxMag*0.5 + maxCombined*0.5
		} else {
			st.waterfallMaxMag = st.waterfallMaxMag*0.95 + maxCombined*0.05
		}
	}
	dbRef := 20 * math.Log10(st.waterfallMaxMag)

	row := make([]float64, n)
	for i := 0; i < n; i++ {
		c := math.Sqrt(st.scanFFTMagX[i]*st.scanFFTMagX[i] + st.scanFFTMagY[i]*st.scanFFTMagY[i] + st.scanFFTMagZ[i]*st.scanFFTMagZ[i])
		db := 20*math.Log10(c+1e-12) - dbRef
		t := 1.0 + db/60.0
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
		row[i] = t
	}

	if len(st.scanHeatmapBuf) < scanHeatmapMaxRows {
		st.scanHeatmapBuf = append(st.scanHeatmapBuf, row)
		if len(st.scanHeatmapBuf) == scanHeatmapMaxRows {
			st.scanHeatmapHead = 0
		}
	} else {
		st.scanHeatmapBuf[st.scanHeatmapHead] = row
		st.scanHeatmapHead = (st.scanHeatmapHead + 1) % scanHeatmapMaxRows
	}

	g.buf.Mu.Lock()
	if s := g.buf.Sensors[uint8(sid)]; s != nil && s.LastDsp != nil {
		if len(st.scanEdgeHeatmapBuf) < scanHeatmapMaxRows {
			st.scanEdgeHeatmapBuf = append(st.scanEdgeHeatmapBuf, s.LastDsp.Spectrum)
			if len(st.scanEdgeHeatmapBuf) == scanHeatmapMaxRows {
				st.scanEdgeHeatmapHead = 0
			}
		} else {
			st.scanEdgeHeatmapBuf[st.scanEdgeHeatmapHead] = s.LastDsp.Spectrum
			st.scanEdgeHeatmapHead = (st.scanEdgeHeatmapHead + 1) % scanHeatmapMaxRows
		}
	}
	g.buf.Mu.Unlock()
}

func DrawScanScreen(g *gpuGame, screen *ebiten.Image, w, h int) {
	sid := int(g.getSelectedSensorID())
	st := g.getSensorState(sid)

	g.buf.Mu.Lock()
	s := g.buf.Sensors[uint8(sid)]
	var sensorPkts uint64
	if s != nil {
		sensorPkts = s.Packets
	}
	g.buf.Mu.Unlock()

	g.updateScanHistory(sid, sensorPkts)
	g.updateLockinHistory(sid, sensorPkts)

	top := headerH + mainTabRowH + contentTopGap
	top = g.drawSensorDropdown(screen, plotPad, top, w) + 4

	// Dynamic heights to fit screen
	availableH := h - top - 20
	th := availableH / 12 // Oscillogram height
	if th < 20 {
		th = 20
	}
	if th > 50 {
		th = 50
	}
	timeH := th*4 + 10

	sh := availableH - timeH
	if sh < 100 {
		sh = 100
	}

	plotX, plotW := plotPad, w-2*plotPad
	x, y, z, ads := g.getSelectedSensorData()

	var adxlR0, adxlR1, adsR0, adsR1 float64
	if g.vertAuto {
		adxlR0 = math.Max(math.Max(maxAbsScan(x), maxAbsScan(y)), maxAbsScan(z))
		adxlR1 = -adxlR0
		adsR0 = maxAbsScan(ads)
		adsR1 = -adsR0
		m := (adxlR0 - adxlR1) * 0.05
		if m < 0.01 {
			m = 0.01
		}
		adxlR0 += m
		adxlR1 -= m
		m = (adsR0 - adsR1) * 0.05
		if m < 0.01 {
			m = 0.01
		}
		adsR0 += m
		adsR1 -= m
	} else {
		adxlR0, adxlR1 = adxlYMax/g.vertMult, adxlYMin/g.vertMult
		adsR0, adsR1 = adsYMax/g.vertMult, adsYMin/g.vertMult
	}

	drawPlot := func(d []float64, y_pos, ph int, vMin, vMax float64, clr color.Color) {
		targetW := plotW
		if targetW > scanMaxWavePoints {
			targetW = scanMaxWavePoints
		}
		down := downSampleScan(d, targetW)
		if len(down) < 2 {
			return
		}
		vector.StrokeRect(screen, float32(plotX), float32(y_pos), float32(plotW), float32(ph-2), 1, clrGrd, true)
		innerH := float64(ph - plotPad - 2)
		span := vMax - vMin
		if span <= 0 {
			span = 1
		}
		dx := float64(plotW-1) / float64(len(down)-1)
		for i := 0; i < len(down)-1; i++ {
			norm := func(v float64) float32 {
				t := (v - vMin) / span
				if t < 0 {
					t = 0
				} else if t > 1 {
					t = 1
				}
				return float32(float64(y_pos) + 2 + (1-t)*innerH)
			}
			x0, x1 := float32(plotX+int(dx*float64(i))), float32(plotX+int(dx*float64(i+1)))
			vector.StrokeLine(screen, x0, norm(down[i]), x1, norm(down[i+1]), strokeW, clr, false)
		}
	}

	y0 := top
	drawPlot(x, y0, th, adxlR1, adxlR0, clrX)
	drawPlot(y, y0, th, adxlR1, adxlR0, clrY)
	drawPlot(z, y0, th, adxlR1, adxlR0, clrZ)
	ebitenutil.DebugPrintAt(screen, "ADXL355 X/Y/Z", plotX+4, y0+2)

	y0 += th
	
	g.buf.Mu.Lock()
	var emg, lsmAccX, lsmAccY, lsmAccZ, lsmGyrX, lsmGyrY, lsmGyrZ []float64
	if s != nil {
		emg = signal.CopyFloats(s.Emg, nil)
		lsmAccX = signal.CopyFloats(s.LsmAccelX, nil)
		lsmAccY = signal.CopyFloats(s.LsmAccelY, nil)
		lsmAccZ = signal.CopyFloats(s.LsmAccelZ, nil)
		lsmGyrX = signal.CopyFloats(s.LsmGyroX, nil)
		lsmGyrY = signal.CopyFloats(s.LsmGyroY, nil)
		lsmGyrZ = signal.CopyFloats(s.LsmGyroZ, nil)
	}
	g.buf.Mu.Unlock()

	// EMG (centered)
	emgR0 := 2048.0
	if maxAbsScan(emg) > 2048 {
		emgR0 = maxAbsScan(emg)
	}
	drawPlot(emg, y0, th, 0, emgR0*2, clrADS)
	ebitenutil.DebugPrintAt(screen, "EMG (0-4095)", plotX+4, y0+2)

	y0 += th
	// LSM Accel
	lsmAccR0 := math.Max(math.Max(maxAbsScan(lsmAccX), maxAbsScan(lsmAccY)), maxAbsScan(lsmAccZ))
	if lsmAccR0 < 2 { lsmAccR0 = 2 } else { lsmAccR0 *= 1.1 }
	drawPlot(lsmAccX, y0, th, -lsmAccR0, lsmAccR0, clrX)
	drawPlot(lsmAccY, y0, th, -lsmAccR0, lsmAccR0, clrY)
	drawPlot(lsmAccZ, y0, th, -lsmAccR0, lsmAccR0, clrZ)
	ebitenutil.DebugPrintAt(screen, "LSM6DSOX Accel X/Y/Z", plotX+4, y0+2)

	y0 += th
	// LSM Gyro
	lsmR0 := math.Max(math.Max(maxAbsScan(lsmGyrX), maxAbsScan(lsmGyrY)), maxAbsScan(lsmGyrZ))
	if lsmR0 < 10 { lsmR0 = 10 } else { lsmR0 *= 1.1 }
	drawPlot(lsmGyrX, y0, th, -lsmR0, lsmR0, clrX)
	drawPlot(lsmGyrY, y0, th, -lsmR0, lsmR0, clrY)
	drawPlot(lsmGyrZ, y0, th, -lsmR0, lsmR0, clrZ)
	ebitenutil.DebugPrintAt(screen, "LSM6DSOX Gyro X/Y/Z (dps)", plotX+4, y0+2)

	adxlFs := 4000.0
	if g.store != nil {
		adxlFs = float64(g.store.GetReceiver().AdxlRate)
	}
	g.refreshScanFFTCache(x, y, z, ads, adxlFs, 40000.0, sensorPkts)

	sy0 := top + timeH
	blockH := (sh - 10) / 3 // 3 waterfalls (X, Y, Z)
	if blockH < 30 {
		blockH = 30
	}

	carrierHz := 0.0
	if s != nil && s.LastDsp != nil {
		carrierHz = float64(s.LastDsp.DemodFreqHz)
	}

	axes := []struct {
		mag   []float64
		buf   [][]float64
		head  int
		img   **ebiten.Image
		label string
		clr   color.RGBA
	}{
		{st.scanFFTMagX, st.lockinHeatX, st.lockinHeatHead[0], &g.scanAxisImgs[0], "X", clrX},
		{st.scanFFTMagY, st.lockinHeatY, st.lockinHeatHead[1], &g.scanAxisImgs[1], "Y", clrY},
		{st.scanFFTMagZ, st.lockinHeatZ, st.lockinHeatHead[2], &g.scanAxisImgs[2], "Z", clrZ},
	}

	for i, ax := range axes {
		ry := sy0 + i*(blockH+2)
		g.drawScanAxisWaterfall(screen, ax.buf, ax.head, ax.img, st.scanFFTFreqAdxl, adxlFs, carrierHz, ax.clr, plotX, ry, plotW, blockH)
	}

	if s != nil && s.LastDsp != nil {
		p0 := s.LastDsp
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("EDGE Peak: %.1f Hz | Mag: %.3f | SNR: %.1f dB", p0.DemodFreqHz, p0.Magnitude, p0.SNR), plotX+4, h-15)
	}
}

func drawSpectrumScan(screen *ebiten.Image, mag, freqs []float64, fs float64, x0, y0, w, h int, clr color.Color, logFreq bool, label string) {
	if len(mag) < 2 || len(freqs) < 2 {
		return
	}
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)
	if label != "" {
		ebitenutil.DebugPrintAt(screen, label, x0+2, y0+1)
	}
	innerW, innerH := w-4, float64(h-plotPad-2)
	fMax := fs / 2
	fMinPlot := freqMinHz
	logMin, logMax := math.Log10(fMinPlot), math.Log10(fMax)
	maxMag := 1e-12
	for _, v := range mag {
		if v > maxMag {
			maxMag = v
		}
	}
	dbRef := 20 * math.Log10(maxMag)
	toX := func(f float64) float32 {
		t := (math.Log10(f) - logMin) / (logMax - logMin)
		if !logFreq {
			t = (f - fMinPlot) / (fMax - fMinPlot)
		}
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
		return float32(x0) + 2 + float32(t*float64(innerW))
	}
	toY := func(m float64) float32 {
		t := 1.0 + (20*math.Log10(m+1e-12)-dbRef)/60.0
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
		return float32(y0) + float32(h) - 2 - float32(t*innerH)
	}
	for i := 0; i < len(mag)-1; i++ {
		if freqs[i+1] < fMinPlot {
			continue
		}
		vector.StrokeLine(screen, toX(freqs[i]), toY(mag[i]), toX(freqs[i+1]), toY(mag[i+1]), strokeW, clr, false)
	}
}

func (g *gpuGame) drawSpectralHeatmap(screen *ebiten.Image, st *sensorGUIState, freqs []float64, fs float64, x0, y0, w, h int, logFreq bool) {
	innerW, innerH := w-4, h-16
	if innerW < 8 || innerH < 4 {
		return
	}
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)
	ebitenutil.DebugPrintAt(screen, "HEAT X+Y+Z", x0+2, y0+1)
	numRows := len(st.scanHeatmapBuf)
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
		if !logFreq {
			f = fMinPlot + t*(fMax-fMinPlot)
		}
		for bj < len(freqs)-1 && freqs[bj] < f {
			bj++
		}
		binMap[xi] = bj
	}
	needed := innerW * innerH * 4
	if cap(g.scanHeatmapPix) < needed {
		g.scanHeatmapPix = make([]byte, needed)
	}
	pix := g.scanHeatmapPix[:needed]
	for yi := 0; yi < innerH; yi++ {
		rowIdx := int(float64(yi) * float64(numRows) / float64(innerH))
		if rowIdx >= numRows {
			rowIdx = numRows - 1
		}
		bufIdx := rowIdx
		if numRows >= scanHeatmapMaxRows {
			bufIdx = (st.scanHeatmapHead + rowIdx) % scanHeatmapMaxRows
		}
		row := st.scanHeatmapBuf[bufIdx]
		base := yi * innerW * 4
		for xi := 0; xi < innerW; xi++ {
			mag := 0.0
			if binMap[xi] < len(row) {
				mag = row[binMap[xi]]
			}
			cr, cg, cb := heatmapColor(mag)
			off := base + xi*4
			pix[off], pix[off+1], pix[off+2], pix[off+3] = cr, cg, cb, 255
		}
	}
	if g.scanHeatmapImg == nil || g.scanHeatmapImg.Bounds().Dx() != innerW || g.scanHeatmapImg.Bounds().Dy() != innerH {
		if g.scanHeatmapImg != nil {
			g.scanHeatmapImg.Deallocate()
		}
		g.scanHeatmapImg = ebiten.NewImage(innerW, innerH)
	}
	g.scanHeatmapImg.WritePixels(pix)
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Translate(float64(x0+2), float64(y0+3))
	screen.DrawImage(g.scanHeatmapImg, op)

	// Vertical grid
	toX := func(f float64) int {
		t := (math.Log10(f) - logMin) / (logMax - logMin)
		if !logFreq {
			t = (f - fMinPlot) / (fMax - fMinPlot)
		}
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
		return x0 + 2 + int(t*float64(innerW))
	}
	tickFreqs := []float64{0.2, 1, 10, 100, 1000, 2000, 10000, 20000, 40000}
	tickFLbl := []string{"0.2", "1", "10", "100", "1k", "2k", "10k", "20k", "40k"}
	for i, f := range tickFreqs {
		if f < fMinPlot || f > fMax {
			continue
		}
		px := toX(f)
		vector.StrokeLine(screen, float32(px), float32(y0+3), float32(px), float32(y0+3+innerH), 1, color.RGBA{60, 60, 100, 160}, false)
		ebitenutil.DebugPrintAt(screen, tickFLbl[i], px-6, y0+h-12)
	}
	g.drawToneMarkers(screen, g.toneMarkerFreqs(), x0+2, y0+3, innerW, innerH, fMinPlot, fMax, logFreq)
}

func (g *gpuGame) drawEdgeWaterfall(screen *ebiten.Image, st *sensorGUIState, x0, y0, w, h int) {
	innerW, innerH := w-4, h-16
	if innerW < 32 || innerH < 4 {
		return
	}
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)
	ebitenutil.DebugPrintAt(screen, "EDGE WATERFALL", x0+2, y0+1)
	numRows := len(st.scanEdgeHeatmapBuf)
	if numRows == 0 {
		return
	}
	needed := innerW * innerH * 4
	if cap(g.scanHeatmapPix) < needed {
		g.scanHeatmapPix = make([]byte, needed)
	}
	pix := g.scanHeatmapPix[:needed]
	for yi := 0; yi < innerH; yi++ {
		rowIdx := int(float64(yi) * float64(numRows) / float64(innerH))
		if rowIdx >= numRows {
			rowIdx = numRows - 1
		}
		bufIdx := rowIdx
		if numRows >= scanHeatmapMaxRows {
			bufIdx = (st.scanEdgeHeatmapHead + rowIdx) % scanHeatmapMaxRows
		}
		row := st.scanEdgeHeatmapBuf[bufIdx]
		base := yi * innerW * 4
		for xi := 0; xi < innerW; xi++ {
			band := int(float64(xi) * 32.0 / float64(innerW))
			if band >= 32 {
				band = 31
			}
			cr, cg, cb := heatmapColor(float64(row[band]) / 255.0)
			off := base + xi*4
			pix[off], pix[off+1], pix[off+2], pix[off+3] = cr, cg, cb, 255
		}
	}
	if g.scanHeatmapImg == nil || g.scanHeatmapImg.Bounds().Dx() != innerW || g.scanHeatmapImg.Bounds().Dy() != innerH {
		if g.scanHeatmapImg != nil {
			g.scanHeatmapImg.Deallocate()
		}
		g.scanHeatmapImg = ebiten.NewImage(innerW, innerH)
	}
	g.scanHeatmapImg.WritePixels(pix)
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Translate(float64(x0+2), float64(y0+3))
	screen.DrawImage(g.scanHeatmapImg, op)
}

func (g *gpuGame) drawScanAxisWaterfall(screen *ebiten.Image, buf [][]float64, head int, imgPtr **ebiten.Image, freqs []float64, fs, carrierHz float64, clr color.RGBA, x0, y0, w, h int) {
	innerW, innerH := w-4, h-4
	if innerW < 8 || innerH < 2 {
		return
	}
	vector.StrokeRect(screen, float32(x0), float32(y0), float32(w), float32(h), 1, clrGrd, true)
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
	pix := make([]byte, innerW*innerH*4)
	for yi := 0; yi < innerH; yi++ {
		rowIdx := int(float64(yi) * float64(numRows) / float64(innerH))
		if rowIdx >= numRows {
			rowIdx = numRows - 1
		}
		bufIdx := rowIdx
		if numRows >= scanHeatmapMaxRows {
			bufIdx = (head + rowIdx) % scanHeatmapMaxRows
		}
		row := buf[bufIdx]
		base := yi * innerW * 4
		for xi := 0; xi < innerW; xi++ {
			mag := 0.0
			if binMap[xi] < len(row) {
				mag = row[binMap[xi]]
			}
			cr, cg, cb := heatmapColor(mag)
			off := base + xi*4
			pix[off], pix[off+1], pix[off+2], pix[off+3] = cr, cg, cb, 255
		}
	}
	img := *imgPtr
	if img == nil || img.Bounds().Dx() != innerW || img.Bounds().Dy() != innerH {
		if img != nil {
			img.Deallocate()
		}
		img = ebiten.NewImage(innerW, innerH)
		*imgPtr = img
	}
	img.WritePixels(pix)
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Translate(float64(x0+2), float64(y0+2))
	screen.DrawImage(img, op)
	if carrierHz > fMinPlot && carrierHz < fMax {
		t := (math.Log10(carrierHz) - logMin) / (logMax - logMin)
		cx := x0 + 2 + int(t*float64(innerW))
		vector.StrokeLine(screen, float32(cx), float32(y0+2), float32(cx), float32(y0+2+innerH), 1, color.RGBA{clr.R, clr.G, clr.B, 160}, true)
	}
	g.drawToneMarkers(screen, g.toneMarkerFreqs(), x0+2, y0+2, innerW, innerH, fMinPlot, fMax, true)
}
