// Экран ALPHABET: отрисовка карточек и реакций.

package gui

import (
	"fmt"
	"image/color"
	"sort"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"server/internal/domain/alphabet"
)

type alphabetBandGroup struct {
	bandIdx int
	label   string
	items   []alphabet.Pattern
}

const (
	alphabetTopOrderRefreshInterval = 3 * time.Second
	alphabetCardAnimDurationMs      = int64(420)
	alphabetCardAnimCleanupInterval = 5 * time.Second
)

func sortPatternsForAlphabet(items []alphabet.Pattern, orderByTop bool) {
	sort.Slice(items, func(i, j int) bool {
		if orderByTop {
			if items[i].Occurrences != items[j].Occurrences {
				return items[i].Occurrences > items[j].Occurrences
			}
		}
		return items[i].ID < items[j].ID
	})
}

func buildAlphabetBandGroups(list []alphabet.Pattern, orderByTop bool) []alphabetBandGroup {
	groupMap := make(map[int][]alphabet.Pattern, len(BandsFilterbank)+1)
	for _, p := range list {
		key := p.BandIdx
		if key < 0 || key >= len(BandsFilterbank) {
			key = -1
		}
		groupMap[key] = append(groupMap[key], p)
	}
	out := make([]alphabetBandGroup, 0, len(groupMap))
	for b := 0; b < len(BandsFilterbank); b++ {
		items := groupMap[b]
		if len(items) == 0 {
			continue
		}
		sortPatternsForAlphabet(items, orderByTop)
		out = append(out, alphabetBandGroup{
			bandIdx: b,
			label:   BandsFilterbank[b].Label,
			items:   items,
		})
	}
	if other := groupMap[-1]; len(other) > 0 {
		sortPatternsForAlphabet(other, orderByTop)
		out = append(out, alphabetBandGroup{
			bandIdx: -1,
			label:   "Other",
			items:   other,
		})
	}
	return out
}

func (g *gpuGame) alphabetDisplayGroups(list []alphabet.Pattern, now time.Time) []alphabetBandGroup {
	if !g.alphabetOrderByTop {
		g.alphabetTopOrderWasEnabled = false
		g.alphabetTopOrderNextRefresh = time.Time{}
		return buildAlphabetBandGroups(list, false)
	}
	fresh := buildAlphabetBandGroups(list, true)
	if !g.alphabetTopOrderWasEnabled {
		g.alphabetTopOrderByBand = make(map[int][]int, len(BandsFilterbank)+1)
		g.alphabetTopOrderNextRefresh = time.Time{}
	}
	g.alphabetTopOrderWasEnabled = true

	needRefresh := g.alphabetTopOrderNextRefresh.IsZero() || !now.Before(g.alphabetTopOrderNextRefresh)
	if needRefresh {
		seenBands := make(map[int]struct{}, len(fresh))
		for _, grp := range fresh {
			seenBands[grp.bandIdx] = struct{}{}
			ids := make([]int, 0, len(grp.items))
			for _, p := range grp.items {
				ids = append(ids, p.ID)
			}
			g.alphabetTopOrderByBand[grp.bandIdx] = ids
		}
		for bandIdx := range g.alphabetTopOrderByBand {
			if _, ok := seenBands[bandIdx]; !ok {
				delete(g.alphabetTopOrderByBand, bandIdx)
			}
		}
		g.alphabetTopOrderNextRefresh = now.Add(alphabetTopOrderRefreshInterval)
	}

	freshByBand := make(map[int]alphabetBandGroup, len(fresh))
	for _, grp := range fresh {
		freshByBand[grp.bandIdx] = grp
	}
	out := make([]alphabetBandGroup, 0, len(fresh))
	appendBand := func(bandIdx int, fallbackLabel string) {
		grp, ok := freshByBand[bandIdx]
		if !ok || len(grp.items) == 0 {
			return
		}
		label := grp.label
		if label == "" {
			label = fallbackLabel
		}
		order := g.alphabetTopOrderByBand[bandIdx]
		byID := make(map[int]alphabet.Pattern, len(grp.items))
		for _, p := range grp.items {
			byID[p.ID] = p
		}
		items := make([]alphabet.Pattern, 0, len(grp.items))
		nextOrder := make([]int, 0, len(grp.items))
		for _, id := range order {
			if p, ok := byID[id]; ok {
				items = append(items, p)
				nextOrder = append(nextOrder, id)
				delete(byID, id)
			}
		}
		for _, p := range grp.items {
			if _, ok := byID[p.ID]; ok {
				items = append(items, p)
				nextOrder = append(nextOrder, p.ID)
				delete(byID, p.ID)
			}
		}
		g.alphabetTopOrderByBand[bandIdx] = nextOrder
		out = append(out, alphabetBandGroup{bandIdx: bandIdx, label: label, items: items})
	}
	for b := 0; b < len(BandsFilterbank); b++ {
		appendBand(b, BandsFilterbank[b].Label)
	}
	appendBand(-1, "Other")
	return out
}

func easeInOutCubic01(t float64) float64 {
	if t <= 0 { return 0 }
	if t >= 1 { return 1 }
	if t < 0.5 { return 4 * t * t * t }
	u := -2*t + 2
	return 1 - (u*u*u)/2
}

func (g *gpuGame) animatedAlphabetCardPosition(id int, targetX, targetY float64, nowMs int64, gen uint32, animate bool) (float64, float64) {
	state, ok := g.alphabetCardAnimStates[id]
	if !ok {
		state = alphabetCardAnimState{ FromX: targetX, FromY: targetY, ToX: targetX, ToY: targetY, StartMs: nowMs, EndMs: nowMs }
	}
	curX, curY := state.ToX, state.ToY
	if state.EndMs > state.StartMs {
		t := float64(nowMs-state.StartMs) / float64(state.EndMs-state.StartMs)
		tt := easeInOutCubic01(t)
		curX = state.FromX + (state.ToX-state.FromX)*tt
		curY = state.FromY + (state.ToY-state.FromY)*tt
	}
	if state.ToX != targetX || state.ToY != targetY {
		if animate {
			state.FromX, state.FromY, state.ToX, state.ToY = curX, curY, targetX, targetY
			state.StartMs, state.EndMs = nowMs, nowMs+alphabetCardAnimDurationMs
		} else {
			state.FromX, state.FromY, state.ToX, state.ToY = targetX, targetY, targetX, targetY
			state.StartMs, state.EndMs = nowMs, nowMs
			curX, curY = targetX, targetY
		}
	}
	if nowMs >= state.EndMs {
		state.FromX, state.FromY, state.StartMs, state.EndMs = state.ToX, state.ToY, nowMs, nowMs
		curX, curY = state.ToX, state.ToY
	}
	state.SeenGen = gen
	g.alphabetCardAnimStates[id] = state
	return curX, curY
}

func DrawAlphabetScreen(g *gpuGame, screen *ebiten.Image, w, h int) {
	top := headerH + mainTabRowH + contentTopGap
	top = g.drawSensorDropdown(screen, plotPad, top, w) + 4
	rest := h - top
	if rest < 100 { return }
	contentW := w - 2*plotPad
	const sbReserve = 14
	contentAreaForCards := contentW - sbReserve

	vector.FillRect(screen, float32(plotPad), float32(top), float32(contentW+plotPad), float32(rest), clrBg, true)

	lockRowH := 18
	orderCheckW, orderCheckH := 140, 16
	contentStartY := top + 4
	visibleHeight := rest - 8
	cardW, cardH := 120, 28
	reactionCols := (contentAreaForCards + 4) / (cardW + 4)
	if reactionCols < 1 { reactionCols = 1 }

	reactionHeight := 0
	var reactions []alphabet.ReactionPattern
	reactionStore := g.getReactionAlphabetStore(int(g.getSelectedSensorID()))
	if reactionStore != nil && reactionStore.Count() > 0 {
		all := reactionStore.ListAll()
		for _, r := range all {
			if r.Occurrences >= g.reactionMinOccurrences {
				reactions = append(reactions, r)
			}
		}
		sort.Slice(reactions, func(i, j int) bool {
			if reactions[i].Occurrences != reactions[j].Occurrences {
				return reactions[i].Occurrences > reactions[j].Occurrences
			}
			return reactions[i].ID < reactions[j].ID
		})
		reactionRows := (len(reactions) + reactionCols - 1) / reactionCols
		reactionHeight = 16 + reactionRows*(cardH+4) + 8
	}
	ps := g.getPatternStore(int(g.getSelectedSensorID()))
	list := ps.List()
	now := time.Now()
	nowMs := now.UnixMilli()
	groups := g.alphabetDisplayGroups(list, now)
	cellSize := 80
	if g.alphabetViewAxes && g.alphabetAxesPhasePortrait { cellSize = 112 }
	pad := 8
	cellStep := cellSize + pad
	patternCols := (contentAreaForCards + pad) / cellStep
	if patternCols < 1 { patternCols = 1 }
	const groupHeaderH, groupGap = 16, 8
	patternGridHeight := pad
	for _, grp := range groups {
		rows := (len(grp.items) + patternCols - 1) / patternCols
		patternGridHeight += groupHeaderH + rows*cellStep + groupGap
	}
	_, lockBlockHeight := orderCheckH+4, orderCheckH+4+lockRowH+16
	totalContentHeight := reactionHeight + lockBlockHeight + patternGridHeight
	maxScroll := totalContentHeight - visibleHeight
	if maxScroll < 0 { maxScroll = 0 }
	scrollY := g.lockedScrollY
	if scrollY > maxScroll { g.lockedScrollY, scrollY = maxScroll, maxScroll }
	if scrollY < 0 { g.lockedScrollY, scrollY = 0, 0 }
	baseY := contentStartY - scrollY

	g.reactionCardRects = g.reactionCardRects[:0]
	g.reactionFilterRect = uiRect{}
	if len(reactions) > 0 {
		if baseY+reactionHeight > top && baseY < top+rest {
			header := fmt.Sprintf("Reaction alphabet: %d patterns", len(reactions))
			if g.reactionMinOccurrences > 0 { header += fmt.Sprintf(" x≥%d", g.reactionMinOccurrences) }
			ebitenutil.DebugPrintAt(screen, header+" — click card", plotPad+4, baseY)
			filterW, filterH := 36, 14
			filterX, filterY := plotPad+contentAreaForCards-filterW-4, baseY
			g.reactionFilterRect = uiRect{x: filterX, y: filterY, w: filterW, h: filterH}
			filterLabel := "x≥0"
			if g.reactionMinOccurrences > 0 { filterLabel = fmt.Sprintf("x≥%d", g.reactionMinOccurrences) }
			vector.FillRect(screen, float32(filterX), float32(filterY), float32(filterW), float32(filterH), clrBtn, true)
			vector.StrokeRect(screen, float32(filterX), float32(filterY), float32(filterW), float32(filterH), 1, clrGrid, true)
			ebitenutil.DebugPrintAt(screen, filterLabel, filterX+2, filterY+1)
			for i, r := range reactions {
				col, row := i%reactionCols, i/reactionCols
				cx, cy := plotPad+4+col*(cardW+4), baseY+16+row*(cardH+4)
				g.reactionCardRects = append(g.reactionCardRects, reactionCardRect{rect: uiRect{x: cx, y: cy, w: cardW, h: cardH}, index: i})
				if cy+cardH < top || cy > top+rest { continue }
				vector.StrokeRect(screen, float32(cx), float32(cy), float32(cardW), float32(cardH), 1, clrGrd, true)
				vector.FillRect(screen, float32(cx+1), float32(cy+1), float32(cardW-2), float32(cardH-2), clrPanel, true)
				ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%s B%d #%d x%d E%.1f", alphabet.ExciterIDToName(r.ExciterID), r.BandID, r.ID, r.Occurrences, r.TotalEnergy), cx+4, cy+4)
				barH, nBars := 8, alphabet.ReactionPhaseBins
				if len(reactions) > 16 { nBars = 8 }
				barW := (cardW - 16) / nBars
				if barW < 2 { barW = 2 }
				maxV := 1e-12
				for _, v := range r.Vector { if v > maxV { maxV = v } }
				for b := 0; b < nBars && b*barW < cardW-16; b++ {
					idx := b * alphabet.ReactionPhaseBins / nBars
					if idx >= alphabet.ReactionPhaseBins { idx = alphabet.ReactionPhaseBins - 1 }
					v := r.Vector[idx]
					h := int(float64(barH) * (v / maxV))
					if h < 1 && v > 0 { h = 1 }
					vector.FillRect(screen, float32(cx+8+b*barW), float32(cy+cardH-4-h), float32(barW-1), float32(h), clrAuto, true)
				}
			}
		}
		baseY += reactionHeight
	}

	orderRowY := baseY
	g.alphabetOrderByTopRect = uiRect{x: plotPad + 4, y: orderRowY, w: orderCheckW, h: orderCheckH}
	if g.alphabetOrderByTop {
		vector.FillRect(screen, float32(plotPad+4), float32(orderRowY), 12, 12, clrAuto, true)
		vector.StrokeRect(screen, float32(plotPad+4), float32(orderRowY), 12, 12, 1, clrGrid, true)
		ebitenutil.DebugPrintAt(screen, "[x] Dynamic top order", plotPad+18, orderRowY)
	} else {
		vector.StrokeRect(screen, float32(plotPad+4), float32(orderRowY), 12, 12, 1, clrGrd, true)
		ebitenutil.DebugPrintAt(screen, "[ ] Stable rows by band", plotPad+18, orderRowY)
	}
	lockY := orderRowY + orderCheckH + 4
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("S%d View:", g.getSelectedSensorID()), plotPad+4, lockY+2)
	toggleX, toggleW, toggleH := plotPad+60, 72, 18
	g.alphabetViewCombinedRect = uiRect{x: toggleX, y: lockY, w: toggleW, h: toggleH}
	g.alphabetViewAxesRect = uiRect{x: toggleX + toggleW, y: lockY, w: toggleW, h: toggleH}
	cvFill, axFill := clrBtnOn, clrBtn
	if g.alphabetViewAxes { cvFill, axFill = clrBtn, clrBtnOn }
	vector.FillRect(screen, float32(toggleX), float32(lockY), float32(toggleW), float32(toggleH), cvFill, true)
	vector.FillRect(screen, float32(toggleX+toggleW), float32(lockY), float32(toggleW), float32(toggleH), axFill, true)
	vector.StrokeRect(screen, float32(toggleX), float32(lockY), float32(toggleW*2), float32(toggleH), 1, clrGrid, true)
	ebitenutil.DebugPrintAt(screen, "Combined", toggleX+4, lockY+2)
	ebitenutil.DebugPrintAt(screen, "3 axes", toggleX+toggleW+4, lockY+2)

	modeX, modeW := toggleX+toggleW*2+8, 92
	g.alphabetAxesPortraitRect = uiRect{x: modeX, y: lockY, w: modeW, h: toggleH}
	g.alphabetAxesWaveformRect = uiRect{x: modeX + modeW, y: lockY, w: modeW, h: toggleH}
	if g.alphabetViewAxes {
		ebitenutil.DebugPrintAt(screen, "Axes:", modeX-34, lockY)
		pFill, wfFill := clrBtnOn, clrBtn
		if !g.alphabetAxesPhasePortrait { pFill, wfFill = clrBtn, clrBtnOn }
		vector.FillRect(screen, float32(modeX), float32(lockY), float32(modeW), float32(toggleH), pFill, true)
		vector.FillRect(screen, float32(modeX+modeW), float32(lockY), float32(modeW), float32(toggleH), wfFill, true)
		vector.StrokeRect(screen, float32(modeX), float32(lockY), float32(modeW*2), float32(toggleH), 1, clrGrid, true)
		ebitenutil.DebugPrintAt(screen, "Phase Portrait", modeX+4, lockY+2)
		ebitenutil.DebugPrintAt(screen, "Waveform", modeX+modeW+4, lockY+2)
	}

	glyphW, glyphH := cellSize-4, cellSize-34
	if g.alphabetViewAxes && g.alphabetAxesPhasePortrait { glyphH = cellSize - 48 }
	startX := plotPad + (contentAreaForCards-(patternCols*cellStep-pad))/2
	if startX < plotPad { startX = plotPad }
	g.alphabetCardAnimGen++
	if g.alphabetCardAnimGen == 0 { g.alphabetCardAnimGen = 1 }
	animGen := g.alphabetCardAnimGen

	g.alphabetScrollbarRect = uiRect{}
	g.lockedCardRects = g.lockedCardRects[:0]

	groupYNoScroll := lockY + lockRowH + 16
	for _, grp := range groups {
		gy := groupYNoScroll - scrollY
		if gy+groupHeaderH >= contentStartY && gy <= contentStartY+visibleHeight {
			ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%s  (%d)", grp.label, len(grp.items)), plotPad+4, gy)
		}
		for i, p := range grp.items {
			col, row := i%patternCols, i/patternCols
			tx, ty := float64(startX+col*cellStep), float64(groupYNoScroll+groupHeaderH+row*cellStep)
			ax, ay := g.animatedAlphabetCardPosition(p.ID, tx, ty, nowMs, animGen, true)
			cx, cy := int(ax+0.5), int(ay+0.5)-scrollY
			if cy+cellSize < contentStartY || cy > contentStartY+visibleHeight { continue }
			g.lockedCardRects = append(g.lockedCardRects, lockedCardRect{patternID: p.ID, rect: uiRect{x: cx, y: cy, w: cellSize, h: cellSize}})
			fill := clrPanel
			if (g.lockedCompareIDs[0] >= 0 && p.ID == g.lockedCompareIDs[0]) || (g.lockedCompareIDs[1] >= 0 && p.ID == g.lockedCompareIDs[1]) {
				vector.StrokeRect(screen, float32(cx), float32(cy), float32(cellSize), float32(cellSize), 2.5, clrBtnOn, true)
			}
			vector.StrokeRect(screen, float32(cx), float32(cy), float32(cellSize), float32(cellSize), 1.5, clrGrd, true)
			vector.FillRect(screen, float32(cx+1), float32(cy+1), float32(cellSize-2), float32(cellSize-2), fill, true)
			ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%d", p.ID), cx+4, cy+2)
			if p.Occurrences > 1 { ebitenutil.DebugPrintAt(screen, fmt.Sprintf("x%d", p.Occurrences), cx+cellSize-20, cy+2) }
			
			// Глиф
			if g.alphabetViewAxes {
				if g.alphabetAxesPhasePortrait {
					draw3AxesPhasePortraitTriplet(screen, cx+2, cy+14, glyphW, glyphH-12, p.GlyphData, p.GlyphDataY, p.GlyphDataZ)
				} else {
					drawOscillo3AxesMidlineShared(screen, cx+2, cy+2, glyphW, glyphH, p.GlyphData, p.GlyphDataY, p.GlyphDataZ, clrX, clrY, clrZ)
				}
			} else {
				if len(p.GlyphShape.Points) > 0 {
					drawShapeVectorGlyphFast(screen, cx+2, cy+2, glyphW, glyphH, p.GlyphShape.Points)
				} else if len(p.GlyphData) >= FrozenGlyphPoints {
					drawOscilloGlyphFrozenMidline(screen, cx+2, cy+2, glyphW, glyphH, p.GlyphData, clrY)
				}
			}
			if p.LastSeenTs > 0 {
				ls := (nowMs - p.LastSeenTs) / 1000
				ebitenutil.DebugPrintAt(screen, fmt.Sprintf("last: %ds", ls), cx+4, cy+cellSize-30)
			}
			ebitenutil.DebugPrintAt(screen, formatFreqForCard(p), cx+4, cy+cellSize-16)
		}
		groupYNoScroll += groupHeaderH + ((len(grp.items)+patternCols-1)/patternCols)*cellStep + groupGap
	}

	if maxScroll > 0 {
		sbW, sbX, sbH := 8, plotPad+contentAreaForCards+2, visibleHeight
		thumbH := int(float64(sbH) * float64(visibleHeight) / float64(totalContentHeight))
		if thumbH < 12 { thumbH = 12 }
		thumbY := contentStartY + int(float64(sbH-thumbH)*float64(scrollY)/float64(maxScroll))
		g.alphabetScrollbarRect = uiRect{x: sbX, y: contentStartY, w: sbW, h: sbH}
		g.alphabetScrollbarMaxScroll = maxScroll
		vector.FillRect(screen, float32(sbX), float32(contentStartY), float32(sbW), float32(sbH), clrPanel, true)
		vector.FillRect(screen, float32(sbX+1), float32(thumbY), float32(sbW-2), float32(thumbH), clrBtnOn, true)
	}

	if g.lockedSelectedPatternID >= 0 {
		DrawGlyphPopup(g, screen, w, h)
	}
}

func DrawGlyphPopup(g *gpuGame, screen *ebiten.Image, w, h int) {
	ps := g.getPatternStore(int(g.getSelectedSensorID()))
	if ps == nil { return }
	p, ok := ps.Get(g.lockedSelectedPatternID)
	if !ok { return }

	popW, popH := 480, 420
	px, py := (w-popW)/2, (h-popH)/2
	// Shadow / Dim
	vector.FillRect(screen, 0, 0, float32(w), float32(h), color.RGBA{0, 0, 0, 0x80}, true)
	// Popup box
	vector.FillRect(screen, float32(px), float32(py), float32(popW), float32(popH), clrPanel, true)
	vector.StrokeRect(screen, float32(px), float32(py), float32(popW), float32(popH), 2, clrAcc, true)

	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Pattern %d: %s", p.ID, p.Label), px+10, py+10)
	ebitenutil.DebugPrintAt(screen, formatFreqForCard(p), px+10, py+26)

	// Buttons: Close and Play/Pause
	g.lockedPopupCloseRect = uiRect{x: px + popW - 60, y: py + 8, w: 52, h: 18}
	vector.FillRect(screen, float32(g.lockedPopupCloseRect.x), float32(g.lockedPopupCloseRect.y), float32(g.lockedPopupCloseRect.w), float32(g.lockedPopupCloseRect.h), clrBtn, true)
	ebitenutil.DebugPrintAt(screen, " CLOSE ", g.lockedPopupCloseRect.x+4, g.lockedPopupCloseRect.y+1)

	g.lockedPopupStopRect = uiRect{x: px + popW - 130, y: py + 8, w: 62, h: 18}
	vector.FillRect(screen, float32(g.lockedPopupStopRect.x), float32(g.lockedPopupStopRect.y), float32(g.lockedPopupStopRect.w), float32(g.lockedPopupStopRect.h), clrBtn, true)
	stopLabel := " PAUSE "
	if g.lockedPopupAnimStop { stopLabel = " PLAY  " }
	ebitenutil.DebugPrintAt(screen, stopLabel, g.lockedPopupStopRect.x+4, g.lockedPopupStopRect.y+1)

	// Animation area
	animW, animH := popW-40, popH-140
	ax, ay := px+20, py+60
	vector.FillRect(screen, float32(ax), float32(ay), float32(animW), float32(animH), clrBg, true)
	vector.StrokeRect(screen, float32(ax), float32(ay), float32(animW), float32(animH), 1, clrGrd, true)

	nFramesRequested := g.lockedPopupFrames
	if nFramesRequested < 1 { nFramesRequested = 1 }
	if nFramesRequested > maxLastNShapes { nFramesRequested = maxLastNShapes }

	// History count
	hCount := len(p.LastNGlyphX)
	if !g.alphabetViewAxes && len(p.LastNShapes) > 0 { hCount = len(p.LastNShapes) }
	
	nFrames := nFramesRequested
	if nFrames > hCount { nFrames = hCount }
	if nFrames < 1 { nFrames = 1 }

	// Looping animation logic
	frameIdx := 0
	if nFrames > 1 && !g.lockedPopupAnimStop {
		frameSpeed := 100 * time.Millisecond // 10 FPS
		frameIdx = int((time.Now().UnixMilli() / frameSpeed.Milliseconds()) % int64(nFrames))
	}

	// Drawing animation
	if g.alphabetViewAxes {
		if g.alphabetAxesPhasePortrait {
			// Phase Portrait Animation
			if len(p.LastNGlyphX) > 0 {
				startIdx := len(p.LastNGlyphX) - nFrames
				if startIdx < 0 { startIdx = 0 }
				
				// Actual frame index in history array
				i := startIdx + frameIdx
				if i >= len(p.LastNGlyphX) { i = len(p.LastNGlyphX) - 1 }
				if i < 0 { i = 0 }

				mx := frozenMidlineCycle(p.LastNGlyphX[i])
				my := frozenMidlineCycle(p.LastNGlyphY[i])
				mz := frozenMidlineCycle(p.LastNGlyphZ[i])
				if len(mx) >= 2 {
					// Calculate min/max for all 3 axes in this frame
					minV, maxV := mx[0], mx[0]
					for _, v := range mx { if v < minV { minV = v }; if v > maxV { maxV = v } }
					for _, v := range my { if v < minV { minV = v }; if v > maxV { maxV = v } }
					for _, v := range mz { if v < minV { minV = v }; if v > maxV { maxV = v } }
					span := maxV - minV
					if span < 1e-12 { span = 1 }
					minV -= span * 0.05
					maxV += span * 0.05
					
					gap := 8
					pw := (animW - 2*gap) / 3
					drawPhasePortraitPairShared(screen, ax, ay+10, pw, animH-20, mx, my, minV, maxV, color.RGBA{R: 0xe0, G: 0x7a, B: 0x5f, A: 0xff})
					drawPhasePortraitPairShared(screen, ax+pw+gap, ay+10, pw, animH-20, my, mz, minV, maxV, color.RGBA{R: 0x58, G: 0xb7, B: 0x7f, A: 0xff})
					drawPhasePortraitPairShared(screen, ax+2*(pw+gap), ay+10, pw, animH-20, mx, mz, minV, maxV, color.RGBA{R: 0x5c, G: 0x89, B: 0xd8, A: 0xff})
				}
			}
		} else {
			// Waveform Animation (3 axes)
			if len(p.LastNGlyphX) > 0 {
				startIdx := len(p.LastNGlyphX) - nFrames
				if startIdx < 0 { startIdx = 0 }
				
				i := startIdx + frameIdx
				if i >= len(p.LastNGlyphX) { i = len(p.LastNGlyphX) - 1 }
				if i < 0 { i = 0 }

				drawOscillo3AxesMidlineShared(screen, ax, ay, animW, animH, p.LastNGlyphX[i], p.LastNGlyphY[i], p.LastNGlyphZ[i], clrX, clrY, clrZ)
			}
		}
	} else {
		// Combined Shape Animation
		if len(p.LastNShapes) > 0 {
			startIdx := len(p.LastNShapes) - nFrames
			if startIdx < 0 { startIdx = 0 }
			
			i := startIdx + frameIdx
			if i >= len(p.LastNShapes) { i = len(p.LastNShapes) - 1 }
			if i < 0 { i = 0 }

			drawShapeVectorGlyphColored(screen, ax, ay, animW, animH, p.LastNShapes[i].Points, clrY)
		} else if len(p.LastNGlyphX) > 0 {
			// Fallback to X axis if no combined shape
			startIdx := len(p.LastNGlyphX) - nFrames
			if startIdx < 0 { startIdx = 0 }
			
			i := startIdx + frameIdx
			if i >= len(p.LastNGlyphX) { i = len(p.LastNGlyphX) - 1 }
			if i < 0 { i = 0 }

			drawOscilloGlyphFrozenColored(screen, ax, ay, animW, animH, p.LastNGlyphX[i], clrY)
		}
	}

	// Slider
	sy := ay + animH + 20
	sw := animW
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Animation: %d/%d frames (looping) collected: %d", nFrames, nFramesRequested, hCount), ax, sy-16)
	g.lockedPopupSliderRect = uiRect{x: ax, y: sy, w: sw, h: 20}
	vector.FillRect(screen, float32(ax), float32(sy+8), float32(sw), 4, clrGrd, true)
	thumbX := ax + int(g.lockedPopupFrameSlider*float64(sw-10))
	vector.FillRect(screen, float32(thumbX), float32(sy), 10, 20, clrAcc, true)
}


