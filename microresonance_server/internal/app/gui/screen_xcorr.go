// Экран XCORR: кросскорреляции между сенсорами, галочки «использовать для алфавита».

package gui

import (
	"fmt"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

const (
	xcorrMaxLag    = 256 // макс лаг в сэмплах
	xcorrSegLen    = 512 // длина сегмента для NCC
	xcorrNSegments = 3   // число участков для усреднения CCF
	xcorrPlotH     = 60  // высота графика пары
	xcorrCheckboxW = 16
)

// CrossCorrelationAtLag вычисляет NCC между a и b при лаге lag (b сдвинут вперёд на lag).
func CrossCorrelationAtLag(a, b []float64, lag int) float64 {
	n := len(a)
	if len(b) < n+lag || n < 16 {
		return 0
	}
	if lag < 0 {
		lag = 0
	}
	if lag+n > len(b) {
		n = len(b) - lag
	}
	if n < 16 {
		return 0
	}
	return NCC(a[:n], b[lag:lag+n])
}

// ComputeCrossCorrelation возвращает CCF для пары сигналов (лаг 0..maxLag).
// Берёт xcorrNSegments участков, усредняет CCF по ним.
func ComputeCrossCorrelation(a, b []float64, maxLag int) []float64 {
	if maxLag <= 0 {
		maxLag = xcorrMaxLag
	}
	na, nb := len(a), len(b)
	minLen := maxLag + xcorrNSegments*xcorrSegLen
	if na < minLen || nb < minLen {
		return nil
	}
	out := make([]float64, maxLag+1)
	for seg := 0; seg < xcorrNSegments; seg++ {
		off := seg * xcorrSegLen
		startA := na - maxLag - xcorrSegLen - off
		startB := nb - maxLag - xcorrSegLen - off
		segA := a[startA : startA+xcorrSegLen]
		for lag := 0; lag <= maxLag; lag++ {
			segB := b[startB+lag : startB+lag+xcorrSegLen]
			v := NCC(segA, segB)
			if seg == 0 {
				out[lag] = v
			} else {
				out[lag] += v
			}
		}
	}
	for i := range out {
		out[i] /= float64(xcorrNSegments)
	}
	return out
}

// DrawXcorrScreen рисует вкладку XCORR: пары сенсоров, графики CCF, галочки.
func DrawXcorrScreen(g *gpuGame, screen *ebiten.Image, w, h int) {
	top := headerH + mainTabRowH
	rest := h - top - 8
	if rest < 80 {
		ebitenutil.DebugPrintAt(screen, "XCORR: cross-correlations between sensors", plotPad, top+8)
		return
	}

	ids := g.buf.ListSensors()
	if len(ids) < 2 {
		ebitenutil.DebugPrintAt(screen, "XCORR: need at least 2 sensors for cross-correlation", plotPad, top+8)
		return
	}

	fs := float64(*adxlRate)
	if fs < 100 {
		fs = 4000
	}

	// Пары (i, j) где i < j
	type pair struct {
		a, b uint8
	}
	keep := xcorrMaxLag + xcorrNSegments*xcorrSegLen
	tails := g.buf.GetAllSensorADXLTails(keep)
	var pairs []pair
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			pairs = append(pairs, pair{ids[i], ids[j]})
		}
	}

	if g.xcorrUseForAlphabet == nil {
		g.xcorrUseForAlphabet = make(map[string]bool)
	}

	left := plotPad + 4
	plotW := w - 2*left - xcorrCheckboxW - 20
	if plotW < 100 {
		plotW = 100
	}

	itemH := xcorrPlotH + 8
	contentH := len(pairs) * itemH
	visibleH := rest - 24
	scrollY := g.xcorrScrollY
	if scrollY > contentH-visibleH {
		scrollY = contentH - visibleH
	}
	if scrollY < 0 {
		scrollY = 0
	}
	g.xcorrScrollY = scrollY

	baseY := top - scrollY

	ebitenutil.DebugPrintAt(screen, "Cross-correlations between sensors. Checkbox = use for alphabet. Scroll: mouse wheel", left, top+4)

	// Очищаем rects для кликов
	g.xcorrRects = g.xcorrRects[:0]

	for idx, p := range pairs {
		itemY := baseY + 24 + idx*itemH
		if itemY+xcorrPlotH < top+20 || itemY > top+rest {
			continue
		}

		key := fmt.Sprintf("%d-%d", p.a, p.b)
		useForAlphabet := g.xcorrUseForAlphabet[key]

		ta, oka := tails[p.a]
		tb, okb := tails[p.b]
		if !oka || !okb {
			ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%d–%d: no data", p.a, p.b), left, itemY)
			continue
		}
		xa, ya, za := ta.X, ta.Y, ta.Z
		xb, yb, zb := tb.X, tb.Y, tb.Z

		sigA := Build1DInvariantMag2(xa, ya, za)
		sigB := Build1DInvariantMag2(xb, yb, zb)
		if len(sigA) < xcorrMaxLag+xcorrNSegments*xcorrSegLen || len(sigB) < xcorrMaxLag+xcorrNSegments*xcorrSegLen {
			ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%d–%d: no data", p.a, p.b), left, itemY)
			continue
		}

		ccf := ComputeCrossCorrelation(sigA, sigB, xcorrMaxLag)
		if len(ccf) < 2 {
			continue
		}

		// Подпись пары
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%d–%d", p.a, p.b), left, itemY)

		// Чекбокс «использовать для алфавита»
		cbX := left
		cbY := itemY + 16
		cbRect := uiRect{x: cbX, y: cbY, w: xcorrCheckboxW, h: xcorrCheckboxW}
		g.xcorrRects = append(g.xcorrRects, xcorrRectItem{key: key, rect: cbRect, isCheckbox: true})
		if useForAlphabet {
			vector.FillRect(screen, float32(cbX), float32(cbY), float32(xcorrCheckboxW), float32(xcorrCheckboxW), clrBtnOn, true)
		}
		vector.StrokeRect(screen, float32(cbX), float32(cbY), float32(xcorrCheckboxW), float32(xcorrCheckboxW), 1, clrGrd, true)
		if useForAlphabet {
			ebitenutil.DebugPrintAt(screen, "x", cbX+4, cbY+1)
		}
		ebitenutil.DebugPrintAt(screen, "alph.", cbX+xcorrCheckboxW+2, cbY+2)

		// График CCF
		graphX := left + 90
		graphY := itemY + 4
		graphW := plotW - 90
		graphH := xcorrPlotH - 8
		if graphW < 4 || graphH < 4 {
			continue
		}
		g.xcorrRects = append(g.xcorrRects, xcorrRectItem{key: key, rect: uiRect{x: graphX, y: graphY, w: graphW, h: graphH}, isCheckbox: false})

		vector.StrokeRect(screen, float32(graphX), float32(graphY), float32(graphW), float32(graphH), 1, clrGrd, true)

		maxCCF := 1e-12
		for _, v := range ccf {
			if v > maxCCF {
				maxCCF = v
			}
		}
		if maxCCF < 1e-12 {
			maxCCF = 1
		}
		innerW := float64(graphW - 4)
		innerH := float64(graphH - 4)
		normY := func(v float64) float32 {
			t := (v - 0) / (maxCCF - 0)
			if t < 0 {
				t = 0
			}
			if t > 1 {
				t = 1
			}
			return float32(graphY+2) + float32((1-t)*innerH)
		}
		stepX := innerW / float64(len(ccf)-1)
		px := float32(graphX + 2)
		py := normY(ccf[0])
		for i := 1; i < len(ccf); i++ {
			x1 := float32(graphX + 2 + int(stepX*float64(i)))
			y1 := normY(ccf[i])
			vector.StrokeLine(screen, px, py, x1, y1, strokeW, clrAcc, true)
			px, py = x1, y1
		}
	}

	// Скроллбар
	if contentH > visibleH {
		sbW := 8
		sbX := w - left - sbW - 8
		sbY := top + 24
		sbH := visibleH
		vector.FillRect(screen, float32(sbX), float32(sbY), float32(sbW), float32(sbH), clrPanel, true)
		thumbRatio := float64(visibleH) / float64(contentH)
		if thumbRatio > 1 {
			thumbRatio = 1
		}
		thumbH := int(float64(sbH) * thumbRatio)
		if thumbH < 12 {
			thumbH = 12
		}
		scrollRatio := 0.0
		if contentH > visibleH {
			scrollRatio = float64(scrollY) / float64(contentH-visibleH)
		}
		thumbY := sbY + int(float64(sbH-thumbH)*scrollRatio)
		vector.FillRect(screen, float32(sbX+1), float32(thumbY), float32(sbW-2), float32(thumbH), clrBtnOn, true)
	}
}
