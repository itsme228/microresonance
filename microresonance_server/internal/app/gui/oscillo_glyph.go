// Глиф «реальная волна» (осциллограмма) для карточек CANDIDATES и LOCKED.
// I/Q демодуляция + LPF вокруг f0, нормализация, envelope min/max, отрисовка без FFT.

package gui

import (
	"fmt"
	"image/color"
	"math"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"server/internal/domain/glyph"
	"server/internal/ml/trainer"
)

const (
	twindowMs       = 120  // окно по умолчанию, мс
	fcLpfHz         = 25   // частота среза LPF после I/Q, Гц
	oscilloM        = 128  // число точек по ширине глифа (живой глиф)
	oscilloNMin     = 128  // минимум сэмплов
	oscilloNMax     = 512  // максимум сэмплов
	oscilloUpdateMs = 100  // пересчёт не чаще, мс (~10 Hz на элемент)
	lowAmpThreshold = 1e-8 // порог «почти тишина» по RMS
	// Замороженный глиф LOCKED: envelope glyph — 16 сегментов × 2 (min+max) = 32 точки
	FrozenGlyphSegments = 16
	FrozenGlyphPoints   = 32 // 16×yMin + 16×yMax (было 64)
	// Стационарность окна: cv = std(env)/mean(env) < A_cv, |slope(env)|/mean(env) < A_slope (1/s)
	DefaultACv    = 0.15 // дефолт: коэффициент вариации огибающей
	DefaultASlope = 0.25 // дефолт: относительный наклон огибающей (1/с)
)

// oscilloGlyphPoints строит нормализованные точки глифа из последних сэмплов X.
func oscilloGlyphPoints(x []float64, Fs, f0 float64, lastScale float64) (points []float64, newScale float64, lowAmp bool) {
	if Fs <= 0 || len(x) < 16 {
		return nil, lastScale, true
	}
	twindowSec := float64(twindowMs) / 1000.0
	if f0 > 0 && f0 < 2 {
		twindowSec = 0.5
		availSec := float64(len(x)) / Fs
		if availSec < twindowSec {
			twindowSec = availSec
		}
	}
	N := int(Fs * twindowSec)
	if N < oscilloNMin { N = oscilloNMin }
	if N > oscilloNMax { N = oscilloNMax }
	if N > len(x) { N = len(x) }
	if N < 16 { return nil, lastScale, true }
	start := len(x) - N
	seg := x[start:]

	alpha := 1.0 - math.Exp(-2*math.Pi*float64(fcLpfHz)/Fs)
	I, Q := 0.0, 0.0
	y := make([]float64, N)
	for i := 0; i < N; i++ {
		t := float64(i) / Fs
		c := math.Cos(2 * math.Pi * f0 * t)
		s := math.Sin(2 * math.Pi * f0 * t)
		Iraw := seg[i] * c
		Qraw := seg[i] * s
		I = I + alpha*(Iraw-I)
		Q = Q + alpha*(Qraw-Q)
		y[i] = I*c + Q*s
	}

	amp := glyph.RMS(y)
	if amp < lowAmpThreshold {
		return nil, lastScale, true
	}
	eps := 1e-12
	targetScale := 1.0 / (amp + eps)
	if lastScale <= 0 {
		lastScale = targetScale
	}
	scale := lastScale*0.8 + targetScale*0.2
	for i := range y {
		v := y[i] * scale
		if v < -1 { v = -1 }
		if v > 1 { v = 1 }
		y[i] = v
	}

	points = make([]float64, 2*oscilloM)
	for j := 0; j < oscilloM; j++ {
		segStart := j * N / oscilloM
		segEnd := (j + 1) * N / oscilloM
		if segEnd > N { segEnd = N }
		if segStart >= segEnd {
			segEnd = segStart + 1
			if segEnd > N {
				segEnd = N
				segStart = N - 1
			}
		}
		ymin, ymax := y[segStart], y[segStart]
		for k := segStart; k < segEnd; k++ {
			if y[k] < ymin { ymin = y[k] }
			if y[k] > ymax { ymax = y[k] }
		}
		points[j] = ymin
		points[oscilloM+j] = ymax
	}
	return points, scale, false
}

type OscilloCacheEntry struct {
	LastUpdate   time.Time
	Points       []float64
	DisplayScale float64
	LowAmp       bool
}

const maxOscilloCacheSize = 128

func evictOscilloCache(cache map[int]OscilloCacheEntry) {
	for len(cache) > maxOscilloCacheSize {
		var oldest int
		var oldestT time.Time
		first := true
		for id, ent := range cache {
			if first || ent.LastUpdate.Before(oldestT) {
				oldest = id
				oldestT = ent.LastUpdate
				first = false
			}
		}
		delete(cache, oldest)
	}
}

func GetOscilloPointsForPattern(g *gpuGame, id int, f0 float64, x []float64, Fs float64) (points []float64, lowAmp bool) {
	now := time.Now()
	if g.patternOscilloCache == nil {
		g.patternOscilloCache = make(map[int]OscilloCacheEntry)
	}
	ent := g.patternOscilloCache[id]
	if now.Sub(ent.LastUpdate) < oscilloUpdateMs*time.Millisecond && len(ent.Points) > 0 {
		return ent.Points, ent.LowAmp
	}
	points, newScale, lowAmp := oscilloGlyphPoints(x, Fs, f0, ent.DisplayScale)
	g.patternOscilloCache[id] = OscilloCacheEntry{LastUpdate: now, Points: points, DisplayScale: newScale, LowAmp: lowAmp}
	evictOscilloCache(g.patternOscilloCache)
	return points, lowAmp
}

func GetOscilloPointsForCandidate(g *gpuGame, id int, f0 float64, x []float64, Fs float64) (points []float64, lowAmp bool) {
	now := time.Now()
	if g.candidateOscilloCache == nil {
		g.candidateOscilloCache = make(map[int]OscilloCacheEntry)
	}
	ent := g.candidateOscilloCache[id]
	if now.Sub(ent.LastUpdate) < oscilloUpdateMs*time.Millisecond && len(ent.Points) > 0 {
		return ent.Points, ent.LowAmp
	}
	points, newScale, lowAmp := oscilloGlyphPoints(x, Fs, f0, ent.DisplayScale)
	g.candidateOscilloCache[id] = OscilloCacheEntry{LastUpdate: now, Points: points, DisplayScale: newScale, LowAmp: lowAmp}
	evictOscilloCache(g.candidateOscilloCache)
	return points, lowAmp
}

func drawOscilloGlyph(screen *ebiten.Image, cx, cy, w, h int, points []float64, f0, ampNorm float64, isLocked, lowAmp bool) {
	midY := float32(cy + h/2)
	height := float32(h) * 0.45
	clrZero := color.RGBA{R: 0xd8, G: 0xe2, B: 0xff, A: 0x88}
	vector.StrokeLine(screen, float32(cx), midY, float32(cx+w), midY, 1, clrZero, true)
	if lowAmp || len(points) < 2*oscilloM {
		ebitenutil.DebugPrintAt(screen, "low amp", cx+w/2-18, cy+h/2-6)
		return
	}
	alpha := uint8(0xc0)
	if isLocked { alpha = 0xff }
	clrWave := color.RGBA{R: 0x66, G: 0xbb, B: 0x6a, A: alpha}
	for j := 0; j < oscilloM-1; j++ {
		x0 := float32(cx) + float32(j)*float32(w)/float32(oscilloM-1)
		x1 := float32(cx) + float32(j+1)*float32(w)/float32(oscilloM-1)
		yMin0 := midY - float32(points[j])*height
		yMax0 := midY - float32(points[oscilloM+j])*height
		yMin1 := midY - float32(points[j+1])*height
		yMax1 := midY - float32(points[oscilloM+j+1])*height
		vector.StrokeLine(screen, x0, yMax0, x1, yMax1, 1.5, clrWave, true)
		vector.StrokeLine(screen, x0, yMin0, x1, yMin1, 1.5, clrWave, true)
	}
	midAlpha := uint8(0xa0)
	if isLocked { midAlpha = 0xcc }
	clrMid := color.RGBA{R: 0x66, G: 0xbb, B: 0x6a, A: midAlpha}
	for j := 0; j < oscilloM-1; j++ {
		x0 := float32(cx) + float32(j)*float32(w)/float32(oscilloM-1)
		x1 := float32(cx) + float32(j+1)*float32(w)/float32(oscilloM-1)
		mid0 := (points[j] + points[oscilloM+j]) * 0.5
		mid1 := (points[j+1] + points[oscilloM+j+1]) * 0.5
		y0 := midY - float32(mid0)*height
		y1 := midY - float32(mid1)*height
		vector.StrokeLine(screen, x0, y0, x1, y1, 1, clrMid, true)
	}
}

func drawOscilloGlyphFrozen(screen *ebiten.Image, cx, cy, w, h int, glyphData []float64) {
	midY := float32(cy + h/2)
	height := float32(h) * 0.45
	clrZero := color.RGBA{R: 0xd8, G: 0xe2, B: 0xff, A: 0x88}
	vector.StrokeLine(screen, float32(cx), midY, float32(cx+w), midY, 1, clrZero, true)
	if len(glyphData) < FrozenGlyphPoints {
		ebitenutil.DebugPrintAt(screen, "no glyph", cx+w/2-24, cy+h/2-6)
		return
	}
	K := FrozenGlyphSegments
	clrWave := color.RGBA{R: 0x66, G: 0xbb, B: 0x6a, A: 0xff}
	clrMid := color.RGBA{R: 0x66, G: 0xbb, B: 0x6a, A: 0xcc}
	for j := 0; j < K-1; j++ {
		x0 := float32(cx) + float32(j)*float32(w)/float32(K-1)
		x1 := float32(cx) + float32(j+1)*float32(w)/float32(K-1)
		yMin0 := midY - float32(glyphData[j])*height
		yMax0 := midY - float32(glyphData[K+j])*height
		yMin1 := midY - float32(glyphData[j+1])*height
		yMax1 := midY - float32(glyphData[j+1+K])*height
		vector.StrokeLine(screen, x0, yMax0, x1, yMax1, 1.5, clrWave, true)
		vector.StrokeLine(screen, x0, yMin0, x1, yMin1, 1.5, clrWave, true)
		mid0 := (glyphData[j] + glyphData[K+j]) * 0.5
		mid1 := (glyphData[j+1] + glyphData[K+j+1]) * 0.5
		y0 := midY - float32(mid0)*height
		y1 := midY - float32(mid1)*height
		vector.StrokeLine(screen, x0, y0, x1, y1, 1, clrMid, true)
	}
}

func drawShapeVectorGlyph(screen *ebiten.Image, cx, cy, w, h int, points []float64) {
	drawShapeVectorGlyphColored(screen, cx, cy, w, h, points, color.RGBA{R: 0x66, G: 0xbb, B: 0x6a, A: 0xff})
}

const shapeFastMaxSeg = 16

func drawShapeVectorGlyphFast(screen *ebiten.Image, cx, cy, w, h int, points []float64) {
	if len(points) < 2 { return }
	midY := float32(cy + h/2)
	height := float32(h) * 0.45
	clrZero := color.RGBA{R: 0xd8, G: 0xe2, B: 0xff, A: 0x88}
	vector.StrokeLine(screen, float32(cx), midY, float32(cx+w), midY, 1, clrZero, true)
	n := len(points)
	seg := n - 1
	if seg > shapeFastMaxSeg { seg = shapeFastMaxSeg }
	clr := color.RGBA{R: 0x66, G: 0xbb, B: 0x6a, A: 0xff}
	for i := 0; i < seg; i++ {
		idx0 := i * (n - 1) / seg
		idx1 := (i + 1) * (n - 1) / seg
		if idx1 >= n { idx1 = n - 1 }
		x0 := float32(cx) + float32(idx0)*float32(w)/float32(n-1)
		x1 := float32(cx) + float32(idx1)*float32(w)/float32(n-1)
		y0 := midY - float32(points[idx0])*height
		y1 := midY - float32(points[idx1])*height
		vector.StrokeLine(screen, x0, y0, x1, y1, 1.5, clr, true)
	}
}

func drawShapeVectorGlyphColored(screen *ebiten.Image, cx, cy, w, h int, points []float64, clr color.RGBA) {
	midY := float32(cy + h/2)
	height := float32(h) * 0.45
	clrZero := color.RGBA{R: 0xd8, G: 0xe2, B: 0xff, A: 0x88}
	vector.StrokeLine(screen, float32(cx), midY, float32(cx+w), midY, 1, clrZero, true)
	if len(points) < 2 { return }
	n := len(points)
	for i := 0; i < n-1; i++ {
		x0 := float32(cx) + float32(i)*float32(w)/float32(n-1)
		x1 := float32(cx) + float32(i+1)*float32(w)/float32(n-1)
		y0 := midY - float32(points[i])*height
		y1 := midY - float32(points[i+1])*height
		vector.StrokeLine(screen, x0, y0, x1, y1, 1.5, clr, true)
	}
}

func drawOscilloGlyphFrozenColored(screen *ebiten.Image, cx, cy, w, h int, glyphData []float64, clr color.RGBA) {
	midY := float32(cy + h/2)
	height := float32(h) * 0.45
	clrZero := color.RGBA{R: 0xd8, G: 0xe2, B: 0xff, A: 0x88}
	vector.StrokeLine(screen, float32(cx), midY, float32(cx+w), midY, 1, clrZero, true)
	if len(glyphData) < FrozenGlyphPoints { return }
	K := FrozenGlyphSegments
	for j := 0; j < K-1; j++ {
		x0 := float32(cx) + float32(j)*float32(w)/float32(K-1)
		x1 := float32(cx) + float32(j+1)*float32(w)/float32(K-1)
		yMin0 := midY - float32(glyphData[j])*height
		yMax0 := midY - float32(glyphData[K+j])*height
		yMin1 := midY - float32(glyphData[j+1])*height
		yMax1 := midY - float32(glyphData[K+j+1])*height
		vector.StrokeLine(screen, x0, yMax0, x1, yMax1, 1.5, clr, true)
		vector.StrokeLine(screen, x0, yMin0, x1, yMin1, 1.5, clr, true)
		mid0 := (glyphData[j] + glyphData[K+j]) * 0.5
		mid1 := (glyphData[j+1] + glyphData[K+j+1]) * 0.5
		y0 := midY - float32(mid0)*height
		y1 := midY - float32(mid1)*height
		vector.StrokeLine(screen, x0, y0, x1, y1, 1, clr, true)
	}
}

func drawAlphabet3AxesZeroLine(screen *ebiten.Image, cx, cy, w, h int) {
	midY := float32(cy + h/2)
	clrZero := color.RGBA{R: 0xd8, G: 0xe2, B: 0xff, A: 0x88}
	vector.StrokeLine(screen, float32(cx), midY, float32(cx+w), midY, 1, clrZero, true)
}

func drawOscilloGlyphFrozenMidline(screen *ebiten.Image, cx, cy, w, h int, glyphData []float64, clr color.RGBA) {
	if len(glyphData) < FrozenGlyphPoints { return }
	midY := float32(cy + h/2)
	height := float32(h) * 0.45
	K := FrozenGlyphSegments
	for j := 0; j < K-1; j++ {
		x0 := float32(cx) + float32(j)*float32(w)/float32(K-1)
		x1 := float32(cx) + float32(j+1)*float32(w)/float32(K-1)
		mid0 := (glyphData[j] + glyphData[K+j]) * 0.5
		mid1 := (glyphData[j+1] + glyphData[K+j+1]) * 0.5
		y0 := midY - float32(mid0)*height
		y1 := midY - float32(mid1)*height
		vector.StrokeLine(screen, x0, y0, x1, y1, 1.5, clr, true)
	}
}

func frozenMidline(glyphData []float64) []float64 {
	if len(glyphData) < FrozenGlyphPoints { return nil }
	K := FrozenGlyphSegments
	mid := make([]float64, K)
	for j := 0; j < K; j++ {
		mid[j] = (glyphData[j] + glyphData[K+j]) * 0.5
	}
	return mid
}

func frozenMidlineCycle(glyphData []float64) []float64 {
	mid := frozenMidline(glyphData)
	if mid == nil { return nil }
	K := len(mid)
	if K > 4 {
		avg := (mid[0] + mid[K-1]) * 0.5
		mid[0] = avg
		mid[K-1] = avg
		mid[1] = mid[1]*0.6 + avg*0.4
		mid[K-2] = mid[K-2]*0.6 + avg*0.4
	}
	return mid
}

func drawOscillo3AxesMidlineShared(screen *ebiten.Image, cx, cy, w, h int, dataX, dataY, dataZ []float64, clrX, clrY, clrZ color.RGBA) {
	K := FrozenGlyphSegments
	midY := float32(cy + h/2)
	height := float32(h) * 0.45
	clrZero := color.RGBA{R: 0xd8, G: 0xe2, B: 0xff, A: 0x88}
	vector.StrokeLine(screen, float32(cx), midY, float32(cx+w), midY, 1, clrZero, true)

	midlines := [3][]float64{nil, nil, nil}
	midlines[0] = frozenMidline(dataX)
	midlines[1] = frozenMidline(dataY)
	midlines[2] = frozenMidline(dataZ)
	minV, maxV := 0.0, 0.0
	first := true
	for _, mid := range midlines {
		if mid == nil { continue }
		for _, v := range mid {
			if first { minV, maxV = v, v; first = false } else {
				if v < minV { minV = v }
				if v > maxV { maxV = v }
			}
		}
	}
	if first { return }
	rangeV := maxV - minV
	if rangeV < 1e-12 { rangeV = 1 }
	colors := [3]color.RGBA{clrX, clrY, clrZ}
	for ai, mid := range midlines {
		if mid == nil { continue }
		clr := colors[ai]
		for j := 0; j < K-1; j++ {
			norm0 := (mid[j]-minV)/rangeV*2 - 1
			norm1 := (mid[j+1]-minV)/rangeV*2 - 1
			x0 := float32(cx) + float32(j)*float32(w)/float32(K-1)
			x1 := float32(cx) + float32(j+1)*float32(w)/float32(K-1)
			y0 := midY - float32(norm0)*height
			y1 := midY - float32(norm1)*height
			vector.StrokeLine(screen, x0, y0, x1, y1, 1.5, clr, true)
		}
	}
}

func drawPhasePortraitPairShared(screen *ebiten.Image, cx, cy, w, h int, x, y []float64, minV, maxV float64, clr color.RGBA) {
	if w < 8 || h < 8 { return }
	vector.StrokeRect(screen, float32(cx), float32(cy), float32(w), float32(h), 1, clrGrd, true)
	axisClr := color.RGBA{R: 0x33, G: 0x36, B: 0x46, A: 0xff}
	vector.StrokeLine(screen, float32(cx+w/2), float32(cy), float32(cx+w/2), float32(cy+h), 1, axisClr, true)
	vector.StrokeLine(screen, float32(cx), float32(cy+h/2), float32(cx+w), float32(cy+h/2), 1, axisClr, true)

	n := len(x)
	if len(y) < n { n = len(y) }
	if n < 2 { return }
	span := maxV - minV
	if math.Abs(span) < 1e-12 { span = 1 }
	mapPt := func(i int) (float32, float32) {
		px := (x[i] - minV) / span
		py := (y[i] - minV) / span
		dx := float32(cx) + 2 + float32(px)*float32(w-4)
		dy := float32(cy+h) - 2 - float32(py)*float32(h-4)
		return dx, dy
	}
	x0, y0 := mapPt(0)
	prevX, prevY := x0, y0
	for i := 1; i < n; i++ {
		x1, y1 := mapPt(i)
		vector.StrokeLine(screen, prevX, prevY, x1, y1, 1.2, clr, true)
		prevX, prevY = x1, y1
	}
	distSq := (prevX-x0)*(prevX-x0) + (prevY-y0)*(prevY-y0)
	if distSq < 10000 { vector.StrokeLine(screen, prevX, prevY, x0, y0, 1.2, clr, true) }
	vector.DrawFilledCircle(screen, x0, y0, 1.6, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, true)
}

func draw3AxesPhasePortraitTriplet(screen *ebiten.Image, cx, cy, w, h int, dataX, dataY, dataZ []float64) {
	mx := frozenMidlineCycle(dataX)
	my := frozenMidlineCycle(dataY)
	mz := frozenMidlineCycle(dataZ)
	if len(mx) < 2 { return }
	if len(my) < 2 { my = mx }
	if len(mz) < 2 { mz = mx }
	minV, maxV := mx[0], mx[0]
	for _, v := range mx { if v < minV { minV = v }; if v > maxV { maxV = v } }
	for _, v := range my { if v < minV { minV = v }; if v > maxV { maxV = v } }
	for _, v := range mz { if v < minV { minV = v }; if v > maxV { maxV = v } }
	rangeV := maxV - minV
	if rangeV < 1e-12 { rangeV = 1 }
	minV -= rangeV * 0.05
	maxV += rangeV * 0.05
	panelGap := 4
	panelW := (w - 2*panelGap) / 3
	if panelW < 12 { return }
	plotH := h - 16
	if plotH < 12 { plotH = h }
	x0, x1, x2 := cx, cx + panelW + panelGap, cx + 2*(panelW+panelGap)
	drawPhasePortraitPairShared(screen, x0, cy, panelW, plotH, mx, my, minV, maxV, color.RGBA{R: 0xe0, G: 0x7a, B: 0x5f, A: 0xff})
	drawPhasePortraitPairShared(screen, x1, cy, panelW, plotH, my, mz, minV, maxV, color.RGBA{R: 0x58, G: 0xb7, B: 0x7f, A: 0xff})
	drawPhasePortraitPairShared(screen, x2, cy, panelW, plotH, mx, mz, minV, maxV, color.RGBA{R: 0x5c, G: 0x89, B: 0xd8, A: 0xff})
	ebitenutil.DebugPrintAt(screen, "X-Y", x0+2, cy+plotH+1)
	ebitenutil.DebugPrintAt(screen, "Y-Z", x1+2, cy+plotH+1)
	ebitenutil.DebugPrintAt(screen, "X-Z", x2+2, cy+plotH+1)
	if sig := trainer.AxisSigFromGlyphData(mx, my, mz); len(sig) >= 4 {
		ebitenutil.DebugPrintAt(screen, "e1 "+fmt.Sprintf("%.2f", sig[2])+"  e2 "+fmt.Sprintf("%.2f", sig[3]), cx+2, cy+1)
	}
}
