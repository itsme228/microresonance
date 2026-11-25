// GPU runtime: interactive update/draw and pipeline ticks.

package gui

import (
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"server/internal/domain/alphabet"
	"server/internal/domain/dynamics"
	"server/internal/domain/pipeline"
	"server/internal/domain/signal"
	"server/internal/ports/httpapi"
	"server/internal/ports/vrws"
)

const (
	webSnapshotsEnabled          = false
	webSnapshotFastInterval      = 80 * time.Millisecond  // full_snapshot ~12 FPS
	webSnapshotHeavyInterval     = 250 * time.Millisecond // ALPHABET/MEANING/TRAINER ~4 FPS
	webSnapshotIdleInterval      = 1 * time.Second        // when GUI is inactive
	webGraphInterval             = 120 * time.Millisecond // графики в вебе: ~8 FPS
	webXcorrInterval             = 1 * time.Second        // XCORR дороже, реже
	webIncludeGraphsInSnapshot   = false                  // графики в web-snapshot отключены (native GUI рисует сам)
	webRawPoints                 = 180
	webFFTPoints                 = 256
	webBandsMaxSamples           = 2048
	webBandsDownsample           = 64
	webMaxGlyphCardsPerSensor    = 40
	webMaxReactionCardsPerSensor = 20
	webGlyphPointsMax            = 24
	webGlyphAxesPointsMax        = 16
)

func (g *gpuGame) drawFFTSliders(screen *ebiten.Image, sy0, w int) {
	sy := sy0 + 2
	x0 := plotPad
	gap := 8
	autoW := 36
	autoGap := 4
	// Три слота: [t:][track], [Y:][Auto][track], [FFT:][track]
	trackW := (w - x0 - 3*fftLabelWidth - autoW - autoGap - 2*gap) / 3
	if trackW < 40 {
		trackW = 40
	}
	if trackW > 180 {
		trackW = 180
	}
	drawSl := func(sx int, label string, val float64, manual string) {
		tx := sx + fftLabelWidth
		vector.FillRect(screen, float32(tx), float32(sy), float32(trackW), 14, clrGrd, true)
		thx := tx + int((val)*(float64(trackW)-float64(thumbW)))
		if thx < tx {
			thx = tx
		}
		if thx > tx+trackW-thumbW {
			thx = tx + trackW - thumbW
		}
		vector.FillRect(screen, float32(thx), float32(sy+1), float32(thumbW), 12, clrAcc, true)
		ebitenutil.DebugPrintAt(screen, label+manual, sx, sy-1)
	}

	tv := (float64((*timeWindow).Milliseconds()) - 50) / 4950
	if tv < 0 {
		tv = 0
	}
	if tv > 1 {
		tv = 1
	}
	if g.dragSlider == "1" {
		tv = g.dragValue
	}
	drawSl(x0, "t:", tv, fmt.Sprintf("%dms", (*timeWindow).Milliseconds()))

	// Y: второй слот — label, Auto (сразу после подписи), track (после Auto)
	ySx := x0 + fftLabelWidth + trackW + gap
	autoX := ySx + fftLabelWidth
	yTrackX := autoX + autoW + autoGap
	slClrBtn := clrGrd
	if g.vertAuto {
		slClrBtn = clrAuto
	}
	vector.StrokeRect(screen, float32(autoX), float32(sy-2), float32(autoW), 14, 1, slClrBtn, true)

	var vv float64
	if g.vertAuto {
		vv = 1
		ebitenutil.DebugPrintAt(screen, "Y:", ySx, sy-1)
		ebitenutil.DebugPrintAt(screen, "Auto", autoX+4, sy-1)
	} else {
		vv = (g.vertMult - 0.25) / 3.75
		if vv < 0 {
			vv = 0
		}
		if vv > 1 {
			vv = 1
		}
		if g.dragSlider == "2" {
			vv = g.dragValue
		}
		ebitenutil.DebugPrintAt(screen, "Y:"+fmt.Sprintf("x%.2g", g.vertMult), ySx, sy-1)
		vector.FillRect(screen, float32(yTrackX), float32(sy), float32(trackW), 14, clrGrd, true)
		thx := yTrackX + int((vv)*(float64(trackW)-float64(thumbW)))
		if thx < yTrackX {
			thx = yTrackX
		}
		if thx > yTrackX+trackW-thumbW {
			thx = yTrackX + trackW - thumbW
		}
		vector.FillRect(screen, float32(thx), float32(sy+1), float32(thumbW), 12, clrAcc, true)
	}

	// FFT: третий слот
	fftSx := ySx + fftLabelWidth + autoW + autoGap + trackW + gap
	fsv := 0.0
	switch g.fftSize {
	case 512:
		fsv = 0.05
	case 1024:
		fsv = 0.35
	case 2048:
		fsv = 0.65
	case 4096:
		fsv = 0.95
	default:
		g.fftSize = 2048
		fsv = 0.65
	}
	if g.dragSlider == "3" {
		fsv = g.dragValue
	}
	drawSl(fftSx, "FFT:", fsv, fmt.Sprintf("%d", g.fftSize))
}

// drawSensorDropdown рисует ряд кнопок выбора сенсора, возвращает Y после dropdown.
func (g *gpuGame) drawSensorDropdown(screen *ebiten.Image, x0, y0, w int) int {
	ids := g.buf.ListSensors()
	g.sensorDropdownRects = g.sensorDropdownRects[:0]
	rowH := 20
	ebitenutil.DebugPrintAt(screen, "Sensor:", x0, y0+2)
	btnX := x0 + fftLabelWidth
	btnW := 28
	btnH := 18
	for _, id := range ids {
		sid := int(id)
		on := g.selectedSensorID < 0 && sid == int(g.getSelectedSensorID()) || g.selectedSensorID == sid
		fill := clrBtn
		if on {
			fill = clrBtnOn
		}
		r := uiRect{x: btnX, y: y0, w: btnW, h: btnH}
		vector.FillRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), fill, true)
		vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 1, clrGrid, true)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%d", sid), r.x+6, r.y+2)
		g.sensorDropdownRects = append(g.sensorDropdownRects, struct {
			id   int
			rect uiRect
		}{sid, r})
		btnX += btnW + 4
	}
	return y0 + rowH
}

// UpdateDetectionPipeline выполняет FFT по X/Y/Z и обновляет кандидатов и locked по T_lock, Δf, ΔA.
func (g *gpuGame) UpdateDetectionPipeline() {
	// MEANING: запустить ML-воркер (читает из mlQueue, вызывает Python)
	if g.mainScreen == "MEANING" && g.mlWorker == nil && MLModelExists() {
		g.mlWorker = NewMLWorker(&g.mlLastPrediction, g.mlQueue)
		g.mlWorker.SetOnPrediction(g.meaningIncrCounter)
		g.mlWorker.Start()
	}

	now := time.Now()
	sensorID := int(g.getSelectedSensorID())
	patternStore := g.getPatternStore(sensorID)
	packetsNow := g.buf.PacketsForSensor(uint8(sensorID))
	lastProcessed := uint64(0)
	if g.lastProcessedPacketsBySensor != nil {
		lastProcessed = g.lastProcessedPacketsBySensor[sensorID]
	}
	hasNewData := packetsNow > lastProcessed
	pipelineDue := now.Sub(g.lastDetectionTs) >= g.detectionInterval
	// age := g.buf.SensorPacketAge(uint8(sensorID))

	/*
		flowState := "idle"
		if packetsNow > 0 {
			if hasNewData {
				flowState = "active"
			} else if age >= 2*time.Second {
				flowState = "stalled"
			} else {
				flowState = "waiting"
			}
		}
		if flowState != g.flowLastState && now.Sub(g.flowLastStateLogTs) >= time.Second {
			g.flowLastState = flowState
			g.flowLastStateLogTs = now
			log.Printf("[flow] state=%s sensor=%d pkts=%d last_proc=%d age=%.0fms due=%t", flowState, sensorID, packetsNow, lastProcessed, float64(age.Milliseconds()), pipelineDue)
		}
	*/

	if g.shapeClusterStore != nil && g.residualDecomposer != nil && patternStore != nil && hasNewData && pipelineDue {
		g.flowPipeRuns++
		pipelineStart := time.Now()
		g.lastDetectionTs = now
		if g.lastProcessedPacketsBySensor == nil {
			g.lastProcessedPacketsBySensor = make(map[int]uint64)
		}
		g.lastProcessedPacketsBySensor[sensorID] = packetsNow
		x, y, z, _ := g.getSelectedSensorData()
		fs := float64(*adxlRate)
		if fs < 100 {
			fs = 4000
		}
		params := g.shapeParams
		params.WaveFFTSize = g.fftSize
		params.RunStats = g.runStats
		params.DynamicsStore = g.dynamicsStore
		params.TrainerGlyphBuffer = g.trainerGlyphBuffer
		params.TrainerEventBuffer = g.trainerEventBuffer
		params.OnReactionGlyph = func(exciterID, bandID, _ int, vec [alphabet.ReactionPhaseBins]float64, totalEnergy float64, nowMs int64) {
			if bandID >= 0 && bandID < 8 {
				g.meaningReactionByBand[bandID] = vec
				g.meaningReactionTsByBand[bandID] = nowMs
				g.meaningReactionValidByBand[bandID] = true
			}
			if g.trainerReactionBuffer != nil {
				g.trainerReactionBuffer.Add(exciterID, bandID, vec, nowMs)
			}
		}
		params.DynamicsLastID = &g.dynamicsLastID
		params.DynamicsLastIDPerBand = g.dynamicsLastIDPerBand[:]
		params.DynamicsSegmentStartMsPerBand = g.dynamicsSegmentStartMsPerBand[:]
		params.ExciterID = alphabet.ExciterIDFromPresetName(g.protocol.AutoPresetName)
		params.MLQueue = g.mlQueue
		params.AmFreqHz = g.protocol.AmFreqHz
		if params.AmFreqHz <= 0 {
			params.AmFreqHz = 0.7
		}
		primaryStore := g.getPrimaryAlphabetStore(sensorID)
		reactionStore := g.getReactionAlphabetStore(sensorID)
		if primaryStore != nil {
			amFreq := params.AmFreqHz
			onReaction := params.OnReactionGlyph
			params.OnPrimaryGlyphBatch = func(exciterID int, bandEnergy []float64, nowMs int64) {
				primaryStore.UpdateAll(exciterID, bandEnergy, nowMs)
				if g.mlQueue != nil && nowMs-g.lastFrameSummaryMs >= 1000 {
					g.lastFrameSummaryMs = nowMs
					var be [8]float64
					for i := 0; i < 8 && i < len(bandEnergy); i++ {
						be[i] = bandEnergy[i]
					}
					var totalE float64
					for _, e := range bandEnergy {
						totalE += e
					}
					totalE += 1e-12
					g.mlQueue.PushFrameSummary(pipeline.FrameSummary{
						T0: nowMs - 1000, T1: nowMs,
						BandEnergy:   be,
						TotalEnergy:  totalE,
						ExciterState: pipeline.ExciterState{ID: exciterID, AmFreqHz: amFreq},
						Quality:      1.0,
					})
				}
				if reactionStore != nil && amFreq > 0 {
					var sumE float64
					for _, e := range bandEnergy {
						sumE += e
					}
					sumE += 1e-12
					phase := math.Mod(float64(nowMs)/1000*amFreq, 1.0)
					if phase < 0 {
						phase += 1
					}
					for b := 0; b < len(bandEnergy) && b < 8; b++ {
						E_rel := bandEnergy[b] / sumE
						if E_rel > alphabet.PrimaryTOn {
							vec, totalEnergy, emit := reactionStore.Accumulate(exciterID, b, E_rel, phase, nowMs)
							if emit {
								mr := reactionStore.MergeOrAddByReaction(exciterID, b, vec, totalEnergy, nowMs)
								if onReaction != nil && mr.ReactionID >= 0 {
									onReaction(exciterID, b, mr.ReactionID, vec, totalEnergy, nowMs)
								}
								if g.mlQueue != nil && mr.ReactionID >= 0 {
									var phaseVec [16]float64
									copy(phaseVec[:], vec[:])
									g.mlQueue.PushEventReaction(pipeline.EventReaction{
										TCycleEnd:   nowMs,
										ExciterID:   exciterID,
										BandIdx:     b,
										ReactionID:  mr.ReactionID,
										PhaseVec16:  phaseVec,
										TotalEnergy: totalEnergy,
										IsNewID:     !mr.Merged,
									})
								}
							}
						}
					}
				}
			}
		}
		params.AllowDiscovery = true
		f1 := g.protocol.Frequency
		f2 := g.protocol.Frequency
		if g.protocol.Mode == "dual-tone" {
			f2 = g.protocol.Frequency + g.protocol.FreqStep
		}
		envType := g.protocol.EnvType
		if envType == "" {
			envType = "sine"
		}
		g.glyphLogCtx = alphabet.GlyphLogContext{F1: f1, F2: f2, PhaseDeg: g.protocol.PhaseOffset, EnvType: envType}
		patternStore.SetLogContext(&g.glyphLogCtx)
		pipeline.UpdateShapePipeline(x, y, z, fs, now, params, g.shapeClusterStore, g.residualDecomposer, patternStore, nil)
		if g.runStats != nil {
			g.sumTotal += int64(g.runStats.PeaksTotal)
			g.sumRejected += int64(g.runStats.PeaksRejectedStationarity)
			g.sumClassified += int64(g.runStats.PeaksClassified)
		}
		g.perfPipeline += time.Since(pipelineStart)
	} else if !hasNewData {
		g.flowPipeSkipNoData++
	} else if !pipelineDue {
		g.flowPipeSkipInterval++
	}
}

// updateLastRawDetection сохраняет сырой результат детекции: max_bin в 0.2–2 kHz, peak_amp, noise_floor (медиана), amp_norm.
// Всегда показываем во вкладке CANDIDATES, чтобы видеть, что алгоритм считает пиком.
func (g *gpuGame) updateLastRawDetection(magX, freqs []float64) {
	if len(magX) < 2 || len(freqs) < 2 {
		g.lastRawDetection.Valid = false
		return
	}
	const freqMin, freqMax = 0.2, 2000.0
	minBin := 1
	for i := 1; i < len(freqs); i++ {
		if freqs[i] >= freqMin {
			minBin = i
			break
		}
	}
	maxBin := len(magX) - 1
	for i := len(freqs) - 1; i >= 0; i-- {
		if freqs[i] <= freqMax {
			maxBin = i
			break
		}
	}
	if minBin >= maxBin {
		g.lastRawDetection.Valid = false
		return
	}
	maxIdx := minBin
	for i := minBin + 1; i <= maxBin; i++ {
		if magX[i] > magX[maxIdx] {
			maxIdx = i
		}
	}
	peakAmp := magX[maxIdx]
	size := maxBin - minBin + 1
	if g.rawDetectionScratch == nil {
		g.rawDetectionScratch = &signal.RawDetectionScratch{}
	}
	g.rawDetectionScratch.Ensure(size)
	sliceMag := g.rawDetectionScratch.SliceMag()[:size]
	copy(sliceMag, magX[minBin:maxBin+1])
	sort.Float64s(sliceMag)
	medianAmp := 1e-12
	if len(sliceMag) > 0 {
		medianAmp = sliceMag[len(sliceMag)/2]
	}
	if medianAmp < 1e-12 {
		medianAmp = 1e-12
	}
	ampNorm := peakAmp / medianAmp
	g.lastRawDetection.PeakFreq = freqs[maxIdx]
	g.lastRawDetection.PeakAmp = peakAmp
	g.lastRawDetection.NoiseFloor = medianAmp
	g.lastRawDetection.AmpNorm = ampNorm
	g.lastRawDetection.Valid = true
}

// logDetectionStats пишет раз в 5 сек в консоль: shape clusters, locked tokens.
func (g *gpuGame) logDetectionStats() {
	patternStore := g.getPatternStore(int(g.getSelectedSensorID()))
	if patternStore == nil {
		return
	}
	if g.shapeClusterStore != nil {
		clusters, cands := g.shapeClusterStore.ListClusters()
		log.Printf("[shape] clusters=%d candidates=%d", len(clusters), len(cands))
	}
	locked := patternStore.Count()
	log.Printf("[shape] locked tokens=%d", locked)
}

// refreshHeavyWebSnapshot обновляет редкие/тяжёлые части full_snapshot.
func (g *gpuGame) refreshHeavyWebSnapshot(sensorID int) {
	var allGlyphCards []vrws.VRPatternCard
	var allReactionCards []vrws.VRReactionCard
	for _, sid := range g.buf.ListSensors() {
		sidInt := int(sid)
		ps := g.getPatternStore(sidInt)
		if ps != nil {
			var glyphList []alphabet.Pattern
			if g.alphabetOrderByTop {
				glyphList = ps.ListOrderedByTop()
			} else {
				glyphList = ps.List()
			}
			if len(glyphList) > webMaxGlyphCardsPerSensor {
				glyphList = glyphList[:webMaxGlyphCardsPerSensor]
			}
			cards := buildGlyphCards(glyphList, sidInt)
			compressGlyphCardsForWeb(cards)
			allGlyphCards = append(allGlyphCards, cards...)
		}
		rs := g.getReactionAlphabetStore(sidInt)
		if rs != nil {
			var reactions []alphabet.ReactionPattern
			for _, r := range rs.ListAll() {
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
			if len(reactions) > webMaxReactionCardsPerSensor {
				reactions = reactions[:webMaxReactionCardsPerSensor]
			}
			allReactionCards = append(allReactionCards, buildReactionCards(reactions, sidInt)...)
		}
	}
	g.cachedGlyphCards = allGlyphCards
	g.cachedReactionCards = allReactionCards

	var discoveryCells []vrws.VRDiscoveryCell
	if ps := g.getPrimaryAlphabetStore(sensorID); ps != nil {
		currentExciter := alphabet.ExciterIDFromPresetName(g.protocol.AutoPresetName)
		for _, eid := range []int{0, currentExciter} {
			for band := 0; band < 8; band++ {
				cell := ps.Cell(eid, band)
				if cell != nil && cell.RawE > 1e-9 {
					discoveryCells = append(discoveryCells, vrws.VRDiscoveryCell{
						ExciterID:   eid,
						ExciterName: alphabet.ExciterIDToName(eid),
						BandIdx:     band,
						RawE:        cell.RawE,
					})
				}
			}
		}
	}
	g.cachedDiscovery = discoveryCells

	var trainerSnap vrws.VRTrainerSnapshot
	if g.trainerActionStore != nil {
		for _, a := range g.trainerActionStore.List() {
			trainerSnap.Actions = append(trainerSnap.Actions, vrws.VRTrainerAction{
				ID: a.ID, Name: a.Name, Samples: a.Samples, Quality: a.Quality,
			})
		}
		trainerSnap.SelectedActionID = g.trainerSelectedActionID
		if g.trainerCalibrateLoop != nil {
			trainerSnap.Calibrating = true
			switch g.trainerCalibrateLoop.Phase {
			case PhaseRest:
				trainerSnap.Phase = "rest"
			case PhaseDo:
				trainerSnap.Phase = "do"
			case PhaseResult:
				trainerSnap.Phase = "result"
			}
			if a := g.trainerActionStore.ByID(g.trainerCalibrateLoop.CurrentActionID); a != nil {
				trainerSnap.CurrentAction = a.Name
			}
			trainerSnap.SegmentsDone = len(g.trainerCalibrateLoop.Sequence)
			trainerSnap.SegmentsTotal = len(g.trainerCalibrateLoop.PlannedSequence)
		}
	}
	g.cachedTrainer = trainerSnap

	var meaningSnap vrws.VRMeaningSnapshot
	meaningSnap.ModelLoaded = g.mlWorker != nil
	meaningSnap.PredictionName, meaningSnap.Confidence = g.mlLastPrediction.Get()
	g.meaningCountersMu.Lock()
	meaningSnap.Counters = make(map[string]int)
	for k, v := range g.meaningCounters {
		meaningSnap.Counters[k] = v
	}
	g.meaningCountersMu.Unlock()
	meaningSnap.Actions = actionNamesFromStore(g)
	g.cachedMeaning = meaningSnap

	var bandEnergy [8]float64
	if ps := g.getPrimaryAlphabetStore(sensorID); ps != nil {
		currentExciter := alphabet.ExciterIDFromPresetName(g.protocol.AutoPresetName)
		for band := 0; band < 8; band++ {
			cell := ps.Cell(currentExciter, band)
			if cell != nil {
				bandEnergy[band] = cell.RawE
			}
		}
	}
	g.cachedBandEnergy = bandEnergy

	_, _, hFinal, _ := g.residualDecomposer.LastResult()
	peaksTotal, peaksClassified, peaksRejected := 0, 0, 0
	if g.runStats != nil {
		peaksTotal = g.runStats.PeaksTotal
		peaksClassified = g.runStats.PeaksClassified
		peaksRejected = g.runStats.PeaksRejectedStationarity
	}
	g.cachedShapeMetrics = vrws.VRShapeMetrics{
		HFinal:          hFinal,
		TokensTotal:     len(allGlyphCards),
		PeaksTotal:      peaksTotal,
		PeaksClassified: peaksClassified,
		PeaksRejected:   peaksRejected,
	}
}

func downsampleCopy(src []float64, maxN int) []float64 {
	if len(src) == 0 {
		return nil
	}
	if maxN <= 0 || len(src) <= maxN {
		return signal.CopyFloats(src, nil)
	}
	return signal.CopyFloats(downSampleScan(src, maxN), nil)
}

func compressGlyphCardsForWeb(cards []vrws.VRPatternCard) {
	for i := range cards {
		cards[i].PeakProfile = nil // GUI this field does not use; saves payload.
		cards[i].Shape = downsampleCopy(cards[i].Shape, webGlyphPointsMax)
		cards[i].ShapeX = downsampleCopy(cards[i].ShapeX, webGlyphAxesPointsMax)
		cards[i].ShapeY = downsampleCopy(cards[i].ShapeY, webGlyphAxesPointsMax)
		cards[i].ShapeZ = downsampleCopy(cards[i].ShapeZ, webGlyphAxesPointsMax)
	}
}

// refreshGraphWebSnapshot обновляет графические данные для веб-GUI (RAW/FFT/BANDS/XCORR) с агрессивным downsample.
func (g *gpuGame) refreshGraphWebSnapshot(fsAdxl, fsAds float64) {
	keepAdxl := webBandsMaxSamples
	if g.fftSize > keepAdxl {
		keepAdxl = g.fftSize
	}
	keepAds := webRawPoints
	if g.fftSize > keepAds {
		keepAds = g.fftSize
	}
	id := g.getSelectedSensorID()
	g.buf.GetSensorDataTailInto(id, keepAdxl, keepAds, &g.sensorScratchX, &g.sensorScratchY, &g.sensorScratchZ, &g.sensorScratchADS)
	sx, sy, sz, sads := g.sensorScratchX, g.sensorScratchY, g.sensorScratchZ, g.sensorScratchADS
	if g.fftScratch == nil {
		g.fftScratch = &signal.FFTScratch{}
	}
	if g.downSampleScratch == nil {
		g.downSampleScratch = &signal.DownSampleScratch{}
	}
	g.cachedRawX = signal.CopyFloats(signal.DownSampleScanReuse(g.downSampleScratch, 0, sx, webRawPoints), g.cachedRawX)
	g.cachedRawY = signal.CopyFloats(signal.DownSampleScanReuse(g.downSampleScratch, 1, sy, webRawPoints), g.cachedRawY)
	g.cachedRawZ = signal.CopyFloats(signal.DownSampleScanReuse(g.downSampleScratch, 2, sz, webRawPoints), g.cachedRawZ)
	g.cachedRawADS = signal.CopyFloats(signal.DownSampleScanReuse(g.downSampleScratch, 3, sads, webRawPoints), g.cachedRawADS)

	copyDownFFT := func(src, dst []float64) []float64 {
		return signal.CopyFloats(downSampleScan(src, webFFTPoints), dst)
	}
	fftMagX, fftFreqs := signal.ComputeFFTScanReuse(g.fftScratch, sx, g.fftSize, fsAdxl)
	g.cachedFftFreqs = copyDownFFT(fftFreqs, g.cachedFftFreqs)
	g.cachedFftMagX = copyDownFFT(fftMagX, g.cachedFftMagX)
	fftMagY, _ := signal.ComputeFFTScanReuse(g.fftScratch, sy, g.fftSize, fsAdxl)
	g.cachedFftMagY = copyDownFFT(fftMagY, g.cachedFftMagY)
	fftMagZ, _ := signal.ComputeFFTScanReuse(g.fftScratch, sz, g.fftSize, fsAdxl)
	g.cachedFftMagZ = copyDownFFT(fftMagZ, g.cachedFftMagZ)
	fftMagADS, _ := signal.ComputeFFTScanReuse(g.fftScratch, sads, g.fftSize, fsAds)
	g.cachedFftMagADS = copyDownFFT(fftMagADS, g.cachedFftMagADS)

	var bandsData []vrws.VRBandWaveform
	nBands := len(BandsFilterbank)
	if g.bandFilters == nil || math.Abs(g.bandFiltersFs-fsAdxl) > 1 {
		g.bandFilters = make([]signal.BandpassFilter, nBands)
		for i := range BandsFilterbank {
			b := &BandsFilterbank[i]
			g.bandFilters[i] = signal.NewBandpassFilter(b.LoHz, b.HiHz, fsAdxl)
		}
		g.bandFiltersFs = fsAdxl
	}
	bx, by, bz := sx, sy, sz
	if len(bx) > webBandsMaxSamples {
		off := len(bx) - webBandsMaxSamples
		bx, by, bz = bx[off:], by[off:], bz[off:]
	}
	if len(bx) >= 16 && len(by) >= 16 && len(bz) >= 16 {
		for i := 0; i < nBands; i++ {
			g.bandFilters[i].Reset()
			xB, yB, zB, squared := g.bandFilters[i].Filter(bx, by, bz)
			b := &BandsFilterbank[i]
			label := fmt.Sprintf("%s %.1f–%.0f Hz", b.Label, b.LoHz, b.HiHz)
			bandsData = append(bandsData, vrws.VRBandWaveform{
				Label:   label,
				XB:      signal.CopyFloats(downSampleScan(xB, webBandsDownsample), nil),
				YB:      signal.CopyFloats(downSampleScan(yB, webBandsDownsample), nil),
				ZB:      signal.CopyFloats(downSampleScan(zB, webBandsDownsample), nil),
				Squared: signal.CopyFloats(downSampleScan(squared, webBandsDownsample), nil),
			})
		}
	}
	g.cachedBandsData = bandsData
}

// refreshXcorrWebSnapshot обновляет CCF для веба (редко, т.к. дорого).
func (g *gpuGame) refreshXcorrWebSnapshot() {
	var xcorrPairs []vrws.VRXcorrPair
	ids := g.buf.ListSensors()
	keep := xcorrMaxLag + xcorrNSegments*xcorrSegLen
	tails := g.buf.GetAllSensorADXLTails(keep)
	if len(tails) < 2 {
		g.cachedXcorrPairs = nil
		return
	}
	minLen := xcorrMaxLag + xcorrNSegments*xcorrSegLen
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			ta, oka := tails[ids[i]]
			tb, okb := tails[ids[j]]
			if !oka || !okb {
				continue
			}
			xa, ya, za := ta.X, ta.Y, ta.Z
			xb, yb, zb := tb.X, tb.Y, tb.Z
			sigA := pipeline.Build1DInvariantMag2(xa, ya, za)
			sigB := pipeline.Build1DInvariantMag2(xb, yb, zb)
			if len(sigA) < minLen || len(sigB) < minLen {
				continue
			}
			ccf := ComputeCrossCorrelation(sigA, sigB, xcorrMaxLag)
			if len(ccf) < 2 {
				continue
			}
			xcorrPairs = append(xcorrPairs, vrws.VRXcorrPair{
				Key: fmt.Sprintf("%d-%d", ids[i], ids[j]),
				CCF: signal.CopyFloats(downSampleScan(ccf, 48), nil),
			})
		}
	}
	g.cachedXcorrPairs = xcorrPairs
}

func (g *gpuGame) Update() error {
	updateStart := time.Now()
	if ebiten.IsKeyPressed(ebiten.KeyEscape) {
		if g.lockedSelectedPatternID >= 0 {
			g.lockedSelectedPatternID = -1
		}
	} else if ebiten.IsKeyPressed(ebiten.KeyQ) {
		return ebiten.Termination
	} else if ebiten.IsKeyPressed(ebiten.KeyZ) {
		sid := g.getSelectedSensorID()
		g.buf.Mu.Lock()
		if s := g.buf.Sensors[sid]; s != nil && len(s.PhaseHistory) > 0 {
			g.dynPhaseZero = float64(s.PhaseHistory[len(s.PhaseHistory)-1])
		}
		g.buf.Mu.Unlock()
	}
	g.handleInput()

	if g.textFocus != "" {
		if g.textCooldown > 0 {
			g.textCooldown--
		}
		var chars []rune
		chars = ebiten.AppendInputChars(chars)
		if len(chars) > 0 {
			for _, r := range chars {
				if r >= 32 && r <= 126 {
					if g.textFocus == "scanSessionName" {
						g.scanSessionName += string(r)
					} else if g.textFocus == "trainerRename" {
						g.trainerRenameInput += string(r)
					} else if g.textFocus == "newAction" {
						g.trainerNewActionName += string(r)
					}
				}
			}
		}
		if ebiten.IsKeyPressed(ebiten.KeyBackspace) && g.textCooldown == 0 {
			if g.textFocus == "scanSessionName" {
				g.scanSessionName = dropLastRune(g.scanSessionName)
			} else if g.textFocus == "trainerRename" {
				g.trainerRenameInput = dropLastRune(g.trainerRenameInput)
			} else if g.textFocus == "newAction" {
				g.trainerNewActionName = dropLastRune(g.trainerNewActionName)
			}
			g.textCooldown = 8
		}
		if ebiten.IsKeyPressed(ebiten.KeyEnter) && g.textCooldown == 0 {
			if g.textFocus == "scanSessionName" {
				g.textFocus = ""
			}
			g.textCooldown = 8
		}
	}

	if g.store != nil && g.syncTick%6 == 0 && g.dragSlider == "" && g.textFocus == "" {
		g.protocol = g.store.GetProtocol()

		// Sync Lock-In targets (Freq1 and MimoFreq2Hz) from Tones array
		var enabledF []float64
		for _, t := range g.protocol.Tones {
			if t.LeftEnabled || t.RightEnabled {
				enabledF = append(enabledF, t.Freq)
			}
		}

		if len(enabledF) > 0 {
			g.protocol.Freq1 = enabledF[0]
			g.protocol.Frequency = enabledF[0]
		}

		if g.buf != nil {
			g.buf.Mu.Lock()
			if len(enabledF) >= 2 {
				g.buf.MimoFreq2Hz = enabledF[1]
				g.protocol.Freq2 = enabledF[1]
			} else {
				g.buf.MimoFreq2Hz = 0
				g.protocol.Freq2 = 0
			}
			g.buf.Mu.Unlock()
		}
	}
	if g.store != nil {
		if p := g.store.GetPendingShapeParams(); p != nil {
			g.shapeParams.HThr = p.HThr
			g.shapeParams.MinDeltaH = p.MinDeltaH
			g.distThr = p.DistThr
			g.shapeParams.DistThr = p.DistThr
			g.shapeParams.MinCos = p.MinCos
			g.shapeParams.MaxFreqRatio = p.MaxFreqRatio
			g.shapeParams.AdjacentDistThr = p.AdjacentDistThr
			g.shapeParams.HysteresisMargin = p.HysteresisMargin
			g.shapeParams.UltraStrongDist = p.UltraStrongDist
			g.reactionMinOccurrences = p.ReactionMinOccurrences
			if p.GlyphTTLSec >= 0 {
				g.glyphTTLSec = p.GlyphTTLSec
			}
			if g.shapeClusterStore != nil {
				g.shapeClusterStore.SetDistThr(p.DistThr)
			}
		}
	}
	g.UpdateDetectionPipeline()

	now := time.Now()
	sensorID := int(g.getSelectedSensorID())
	// Удаление глифов старше glyphTTLSec (раз в 2 сек)
	if g.glyphTTLSec > 0 && now.Sub(g.lastPruneStaleTs) >= 2*time.Second {
		g.lastPruneStaleTs = now
		nowMs := now.UnixMilli()
		for _, sid := range g.buf.ListSensors() {
			ps := g.getPatternStore(int(sid))
			if ps != nil {
				ps.RemoveStale(nowMs, g.glyphTTLSec)
			}
		}
	}
	g.sampleTokenHistory(now)

	if webSnapshotsEnabled {
		hasWSClients := g.vrHub != nil && g.vrHub.ClientCount() > 0
		httpDemand := isGUISnapshotDemandActive(2 * time.Second)
		webActive := hasWSClients || httpDemand
		webInterval := webSnapshotIdleInterval
		if webActive {
			webInterval = webSnapshotFastInterval
		}
		// Полный снапшот GUI: быстрый цикл при активном GUI, редкий — если GUI не запрашивает данные.
		if now.Sub(g.lastWebSnapshotTime) >= webInterval {
			snapshotStart := time.Now()
			// Header
			g.buf.Mu.Lock()
			_ = g.buf.Packets
			g.buf.Mu.Unlock()
			fsAdxl := float64(*adxlRate)
			if fsAdxl < 100 {
				fsAdxl = 4000
			}
			fsAds := float64(*adsRate)
			if fsAds < 100 {
				fsAds = 40000
			}

			heavyDue := now.Sub(g.lastWebHeavySnapshotTime) >= webSnapshotHeavyInterval || g.cachedMeaning.Counters == nil
			if heavyDue {
				heavyStart := time.Now()
				g.refreshHeavyWebSnapshot(sensorID)
				g.lastWebHeavySnapshotTime = now
				g.perfSnapshotHv += time.Since(heavyStart)
			}
			if webIncludeGraphsInSnapshot {
				graphDue := webActive && (now.Sub(g.lastWebGraphSnapshotTime) >= webGraphInterval || len(g.cachedRawX) == 0)
				if graphDue {
					graphStart := time.Now()
					g.refreshGraphWebSnapshot(fsAdxl, fsAds)
					g.lastWebGraphSnapshotTime = now
					g.perfSnapshotGraph += time.Since(graphStart)
				}
				xcorrDue := webActive && now.Sub(g.lastWebXcorrSnapshotTime) >= webXcorrInterval
				if xcorrDue {
					xcorrStart := time.Now()
					g.refreshXcorrWebSnapshot()
					g.lastWebXcorrSnapshotTime = now
					g.perfSnapshotXcorr += time.Since(xcorrStart)
				}
			}

			buildStart := time.Now()
			input := vrws.VRFullSnapshotInput{
				Patterns:    g.cachedGlyphCards,
				Reactions:   g.cachedReactionCards,
				Protocol:    g.protocol,
				Transmitter: g.store.GetTransmitter(),
				Receiver:    g.store.GetReceiver(),
			}
			// ... input setup truncated to match hub.go minimal impl
			g.perfSnapshotBuild += time.Since(buildStart)
			wsStart := time.Now()
			if g.vrHub != nil {
				g.vrHub.BroadcastFullSnapshot(input)
			}
			g.perfSnapshotWS += time.Since(wsStart)
			g.lastWebSnapshotTime = now
			g.perfSnapshot += time.Since(snapshotStart)
		}
	}

	if now.Sub(g.lastDebugLogTs) >= 5*time.Second {
		fps := ebiten.ActualFPS()
		tps := ebiten.ActualTPS()
		avgDraw := 0.0
		if g.perfDrawFrames > 0 {
			avgDraw = float64(g.perfDrawTotal.Milliseconds()) / float64(g.perfDrawFrames)
		}
		log.Printf("[UI-PERF] FPS: %.1f, TPS: %.1f, AvgDraw: %.2fms, SlowDraws: %d, PipeRuns: %d",
			fps, tps, avgDraw, g.perfDrawSlow33, g.flowPipeRuns)

		g.perfDrawTotal = 0
		g.perfDrawFrames = 0
		g.perfDrawSlow33 = 0
		g.flowPipeRuns = 0
		g.lastDebugLogTs = now
	}

	updateDur := time.Since(updateStart)
	g.perfFrames++
	g.perfUpdateTotal += updateDur
	return nil
}

func Run(buf *signal.Buffers, store *httpapi.ConfigStore, glyphLogger *log.Logger) {
	ebiten.SetWindowTitle("MR scope — vibrations 0.2–40k Hz")
	ebiten.SetWindowSize(1200, 750) // влезает на Mac M1 (13"/14")
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	params := pipeline.DefaultShapeParams()
	g := &gpuGame{
		buf:                           buf,
		store:                         store,
		protocol:                      store.GetProtocol(),
		vertAuto:                      true,
		vertMult:                      1,
		fftSize:                       2048,
		freqLog:                       true,
		panelTab:                      "EMITTER",
		mainScreen:                    "TOMO",
		tomoSensorMap:                 make(map[string]int),
		tomoPartsRects:                make(map[string]uiRect),
		tomoMode:                      "Absolute",
		tomoViewMode:                  "Quantum",
		tomoMaxAmp:                    make(map[string]float64),
		tomoMaxRMS:                    make(map[string]float64),
		shapeClusterStore:             pipeline.NewShapeClusterStore(pipeline.DefaultDistThr),
		residualDecomposer:            &pipeline.ResidualDecomposer{},
		shapeParams:                   params,
		glyphLogger:                   glyphLogger,
		patternStoreBySensor:          make(map[int]*alphabet.PatternStore),
		reactionStoreBySensor:         make(map[int]*alphabet.ReactionAlphabetStore),
		primaryStoreBySensor:          make(map[int]*alphabet.PrimaryAlphabetStore),
		dynamicsStore:                 dynamics.NewDynamicsStore(),
		dynamicsLastID:                -1,
		dynamicsLastIDPerBand:         [8]int{-1, -1, -1, -1, -1, -1, -1, -1},
		dynamicsSegmentStartMsPerBand: [8]int64{0, 0, 0, 0, 0, 0, 0, 0},
		runStats:                      &pipeline.ShapePipelineRunStats{},
		lastDetectionTs:               time.Now(),
		lastPruneStaleTs:              time.Now(),
		lastDebugLogTs:                time.Now(),
		detectionInterval:             50 * time.Millisecond, // 20 Hz makes graphs feel live without overwhelming CPU
		tLockSec:                      0.5,
		deltaFBins:                    3,
		deltaARel:                     0.20,
		windowWSec:                    0.6,
		aCv:                           0.15,
		aSlope:                        0.25,
		distThr:                       params.DistThr,
		glyphTTLSec:                   0, // бесконечно по дефолту
		lockedPopupFrames:             100,
		lockedPopupFrameSlider:        0.39,
		alphabetOrderByTop:            false,
		alphabetAxesPhasePortrait:     true,
		tShape:                        0.35,
		tGlyph:                        0.20,
		zoomX:                         1.0,
		zoomY:                         1.0,
		textFocus:                     "",
		meaningCounters:               make(map[string]int),
		manualEditChannel:             "L",
		discoveryInputRects:           make(map[string]uiRect),
		patternOscilloCache:           make(map[int]OscilloCacheEntry),
		candidateOscilloCache:         make(map[int]OscilloCacheEntry),
		trainerActionStore:            NewActionStore(),
		trainerRects:                  make(map[string]uiRect),
		trainerGlyphRecorder:          &GlyphStreamRecorder{},
		trainerGlyphBuffer:            NewGlyphShapeBuffer(500),
		trainerEventBuffer:            NewTrainerEventBuffer(),
		trainerReactionBuffer:         NewRunReactionBuffer(),
		mlQueue:                       pipeline.NewMLQueue(),
		meaningRects:                  make(map[string]uiRect),
		lockedSelectedPatternID:       -1,
		lockedCompareIDs:              [2]int{-1, -1},
		lockedLastStrCache:            make(map[int]string),
		lockedLastSecCache:            make(map[int]int64),
		alphabetTopOrderByBand:        make(map[int][]int),
		alphabetCardAnimStates:        make(map[int]alphabetCardAnimState),
		alphabetCardAnimLastCleanup:   time.Now(),
		tokenHistoryStrideSec:         1,
		tokenHistoryStart:             time.Now(),
	}
	g.tokenHistoryNextSample = g.tokenHistoryStart
	if g.protocol.TomoSensorMap != nil {
		for k, v := range g.protocol.TomoSensorMap {
			g.tomoSensorMap[k] = v
		}
	}
	_ = g.trainerActionStore.Load()
	g.ensureMandatoryActions()

	if err := ebiten.RunGame(g); err != nil && !errors.Is(err, ebiten.Termination) {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func (g *gpuGame) applyFreqInput() {
	if g.manualFreqInput == "" {
		return
	}
	var val float64
	_, err := fmt.Sscanf(g.manualFreqInput, "%f", &val)
	if err != nil {
		return
	}
	minF, maxF := 66.67, 2000.0
	if g.manualFreqExtra {
		minF, maxF = 1.0, 12000.0
	}
	if val < minF {
		val = minF
	}
	if val > maxF {
		val = maxF
	}

	if g.textFocus == "receiver_freq_val" {
		p := g.store.GetProtocol()
		p.ReceiverFreq = val
		g.store.SetProtocol(p)
		g.protocol = p
		g.manualFreqInput = ""
		return
	}

	if g.manualFreqInputIdx >= 0 && g.manualFreqInputIdx < len(g.protocol.Tones) {
		p := g.store.GetProtocol()
		if g.manualFreqInputIdx >= 0 && g.manualFreqInputIdx < len(p.Tones) {
			p.Tones[g.manualFreqInputIdx].Freq = val
			g.store.SetProtocol(p)
			g.protocol = p
		}
		g.manualFreqInput = ""
		return
	}

	p := g.store.GetProtocol()
	p.Freq1 = val
	p.Frequency = val
	if p.Linked || p.MimoLocked {
		p.Freq2 = val
		p.FrequencyR = val
	}
	g.store.SetProtocol(p)
	g.protocol = p
	g.manualFreqInput = ""
}

func (g *gpuGame) applyFreq2Input() {
	if g.manualFreq2Input == "" {
		return
	}
	if g.protocol.MimoLocked {
		g.manualFreq2Input = ""
		return
	}
	var val float64
	_, err := fmt.Sscanf(g.manualFreq2Input, "%f", &val)
	if err != nil {
		return
	}
	minF, maxF := 66.67, 2000.0
	if g.manualFreqExtra {
		minF, maxF = 1.0, 12000.0
	}
	if val < minF {
		val = minF
	}
	if val > maxF {
		val = maxF
	}

	p := g.store.GetProtocol()
	p.Freq2 = val
	p.FrequencyR = val
	g.store.SetProtocol(p)
	g.protocol = p
	g.manualFreq2Input = ""
	if g.buf != nil {
		g.buf.Mu.Lock()
		g.buf.MimoFreq2Hz = val
		g.buf.Mu.Unlock()
	}
}

func (g *gpuGame) ensureMandatoryActions() {
	if g.trainerActionStore == nil {
		return
	}
	mandatory := []string{"REST", "UP", "DOWN", "LEFT", "RIGHT"}
	existing := make(map[string]bool)
	for _, a := range g.trainerActionStore.List() {
		existing[a.Name] = true
	}
	for _, name := range mandatory {
		if !existing[name] {
			g.trainerActionStore.Add(name, "AVN Mandatory Bind")
		}
	}
}
