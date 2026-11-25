package gui

import (
	"fmt"
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

func (g *gpuGame) drawToneMarkers(screen *ebiten.Image, freqs []float64, x0, y0, w, h int, fMinPlot, fMax float64, logFreq bool) {
	if w <= 0 || h <= 0 || fMax <= fMinPlot {
		return
	}
	logMin, logMax := math.Log10(fMinPlot), math.Log10(fMax)
	for _, f := range freqs {
		if f < fMinPlot || f > fMax {
			continue
		}
		var t float64
		if logFreq {
			t = (math.Log10(f) - logMin) / (logMax - logMin)
		} else {
			t = (f - fMinPlot) / (fMax - fMinPlot)
		}
		if t < 0 || t > 1 {
			continue
		}
		px := x0 + int(t*float64(w-1))
		marker := color.RGBA{255, 220, 80, 220}
		vector.StrokeLine(screen, float32(px), float32(y0), float32(px), float32(y0+h), 1, marker, true)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%.0f", f), px-10, y0+2)
	}
}
