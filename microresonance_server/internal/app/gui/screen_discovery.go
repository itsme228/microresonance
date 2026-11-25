// Экран DISCOVERY: первый алфавит (exciter, band). Ячейки адреса.
// Статический, не ML. Сетка ячеек с E(t), E_rise_slope, E_decay_tau.

package gui

import (
	"fmt"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

const (
	discoveryCardW = 110
	discoveryCardH = 48
	discoveryPad   = 6
)

// DrawDiscoveryScreen рисует вкладку DISCOVERY: первый алфавит (exciter, band).
// w уже равна ширине левого блока (до правой панели).
func DrawDiscoveryScreen(g *gpuGame, screen *ebiten.Image, w, h int) {
	top := headerH + mainTabRowH + contentTopGap
	top = g.drawSensorDropdown(screen, plotPad, top, w) + 4
	rest := h - top
	if rest < 100 {
		return
	}
	contentW := w - 2*plotPad
	if contentW < 200 {
		return
	}

	vector.FillRect(screen, float32(plotPad), float32(top), float32(contentW+plotPad), float32(rest), clrBg, true)

	y := top + 4
	ebitenutil.DebugPrintAt(screen, "DISCOVERY: primary alphabet (exciter, band). Address cells — static, not ML.", plotPad+4, y)
	y += 16

	// Текущий exciter и счётчик ячеек
	currentExciter := ExciterIDFromPresetName(g.protocol.AutoPresetName)
	exciterName := g.protocol.AutoPresetName
	if exciterName == "" {
		exciterName = "idle"
	}
	primaryStore := g.getPrimaryAlphabetStore(int(g.getSelectedSensorID()))
	cellCount := 0
	if primaryStore != nil {
		cellCount = primaryStore.Count()
	}
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Exciter: %s (id=%d)  |  Cells: %d  |  Bands: B0..B7 (unchanged)", exciterName, currentExciter, cellCount), plotPad+4, y)
	y += 18

	if primaryStore == nil {
		ebitenutil.DebugPrintAt(screen, "PrimaryAlphabetStore not initialized.", plotPad+4, y)
		return
	}

	// Сетка: строки = [idle, текущий exciter], столбцы = B0..B7
	exciterRows := []int{0, currentExciter}
	if currentExciter == 0 {
		exciterRows = []int{0}
	}
	nBands := len(BandsFilterbank)
	if nBands > 8 {
		nBands = 8
	}
	cardW := discoveryCardW
	cardH := discoveryCardH
	if (contentW-70)/(nBands) < cardW+discoveryPad {
		cardW = (contentW - 70) / nBands
		if cardW < 50 {
			cardW = 50
		}
	}
	startX := plotPad + 4
	labelW := 58

	for rowIdx, exciterID := range exciterRows {
		rowY := y + rowIdx*(cardH+discoveryPad)
		ebitenutil.DebugPrintAt(screen, ExciterIDToName(exciterID)+":", startX, rowY+cardH/2-5)
		// Макс сырой энергии по строке — чтобы бар B6/B7 был виден при наличии сигнала (E_rel может быть малой)
		var rowMaxRawE float64
		for band := 0; band < nBands; band++ {
			cell := primaryStore.Cell(exciterID, band)
			if cell != nil && cell.RawE > rowMaxRawE {
				rowMaxRawE = cell.RawE
			}
		}
		if rowMaxRawE < 1e-12 {
			rowMaxRawE = 1
		}
		for band := 0; band < nBands; band++ {
			cx := startX + labelW + band*(cardW+discoveryPad)
			cy := rowY
			cell := primaryStore.Cell(exciterID, band)
			vector.StrokeRect(screen, float32(cx), float32(cy), float32(cardW), float32(cardH), 1, clrGrd, true)
			if cell != nil && (cell.RawE > 1e-9 || cell.E > 1e-9) {
				vector.FillRect(screen, float32(cx+1), float32(cy+1), float32(cardW-2), float32(cardH-2), clrPanel, true)
				// Бар по сырой энергии (RawE), масштаб — макс по строке, чтобы B6/B7 были видны при сигнале в RAW
				barW := cardW - 8
				if barW < 2 {
					barW = 2
				}
				barH := int(float64(cardH-12) * (cell.RawE / rowMaxRawE))
				if barH > cardH-12 {
					barH = cardH - 12
				}
				// Минимум 4 px при ненулевой RawE, иначе B6 (малая доля при сильных B4/B5) визуально «нет»
				if barH < 4 && cell.RawE > 1e-6 {
					barH = 4
				} else if barH < 1 && cell.RawE > 0 {
					barH = 1
				}
				vector.FillRect(screen, float32(cx+4), float32(cy+cardH-6-barH), float32(barW), float32(barH), clrAuto, true)
				ebitenutil.DebugPrintAt(screen, fmt.Sprintf("B%d %.3f", band, cell.RawE), cx+4, cy+2)
			} else {
				vector.FillRect(screen, float32(cx+1), float32(cy+1), float32(cardW-2), float32(cardH-2), clrGrd, true)
				ebitenutil.DebugPrintAt(screen, fmt.Sprintf("B%d", band), cx+4, cy+cardH/2-5)
			}
		}
	}

	// Подсказка внизу
	legendY := y + len(exciterRows)*(cardH+discoveryPad) + 8
	if legendY < h-24 {
		ebitenutil.DebugPrintAt(screen, "Bar = RawE (raw band energy), scale per row  |  E_rel for T_on/T_off thresholds", plotPad+4, legendY)
	}
}
