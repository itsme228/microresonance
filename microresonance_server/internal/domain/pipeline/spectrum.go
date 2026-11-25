// PipelineSpectrum: спектры и слепые зоны для отладки пайплайна.
// Что теряется, где слепо, как тюнить допуски.

package pipeline

import (
	"fmt"
	"log"
	"math"
	"sort"
)

const (
	spectrumBands   = 16
	spectrumFFTSize = 1024
)

// LogSpectrumPipeline выводит спектры raw/preprocessed/residual и матрицу расстояний токенов.
func LogSpectrumPipeline(raw, preprocessed, residual []float64, store *PatternStore, fs float64) {
	if len(raw) < 32 {
		return
	}

	// 1) Энергия по бинам (временная область)
	featRaw := ToFeatures(raw, spectrumBands)
	featPrep := ToFeatures(preprocessed, spectrumBands)
	featRes := ToFeatures(residual, spectrumBands)
	Hraw := Entropy(featRaw)
	Hprep := Entropy(featPrep)
	Hres := Entropy(featRes)

	log.Printf("[SPECTRUM] H_raw=%.3f H_prep=%.3f H_res=%.3f (потеря H_raw→prep=%.3f prep→res=%.3f)",
		Hraw, Hprep, Hres, Hraw-Hprep, Hprep-Hres)

	// 2) FFT-спектр (частотная область) — raw и preprocessed
	nFFT := spectrumFFTSize
	if nFFT > len(raw) {
		nFFT = len(raw)
	}
	if nFFT >= 16 && fs > 0 {
		magRaw, freqs := computeFFTScan(raw, nFFT, fs)
		magPrep, _ := computeFFTScan(preprocessed, nFFT, fs)
		if len(magRaw) > 0 && len(magPrep) > 0 {
			bandsRaw := downsampleSpectrum(magRaw, freqs, 8)
			bandsPrep := downsampleSpectrum(magPrep, freqs, 8)
			freqLabels := bandFreqLabels(freqs, 8)
			log.Printf("[SPECTRUM] FFT_raw  (8 bands Hz): %s", freqLabels)
			log.Printf("[SPECTRUM] FFT_raw  (8 bands):    %s", formatBands(bandsRaw))
			log.Printf("[SPECTRUM] FFT_prep (8 bands):    %s", formatBands(bandsPrep))
			log.Printf("[SPECTRUM] FFT_ratio prep/raw:    %s (1=сохранено, <1=потеря)", formatLoss(bandsRaw, bandsPrep))
		}
	}

	// 3) Матрица расстояний токенов (слепые зоны, дубликаты, подбор DistThr)
	tokens := store.List()
	if len(tokens) > 0 {
		dists := tokenDistanceMatrix(tokens)
		log.Printf("[SPECTRUM] token_dist min=%.3f max=%.3f mean=%.3f p50=%.3f (n=%d)",
			dists.min, dists.max, dists.mean, dists.p50, len(tokens))
		log.Printf("[SPECTRUM] token_dist matrix (ближайшие пары): %s", formatClosestPairs(tokens, 5))
		log.Printf("[SPECTRUM] token_dist gaps (мин до другого): %s", formatGaps(tokens))
		if dists.min < 1.5 {
			log.Printf("[SPECTRUM] DistThr hint: thr>=%.2f схлопнет ближайшие пары; thr>=%.2f схлопнет все", dists.min, dists.max)
		}
		LogTokenMap(tokens)
	}
}

func downsampleSpectrum(mag, freqs []float64, nBands int) []float64 {
	if len(mag) == 0 || nBands <= 0 {
		return nil
	}
	bands := make([]float64, nBands)
	step := float64(len(mag)) / float64(nBands)
	for i := 0; i < len(mag); i++ {
		bin := int(float64(i) / step)
		if bin >= nBands {
			bin = nBands - 1
		}
		bands[bin] += mag[i] * mag[i]
	}
	for i := range bands {
		bands[i] = math.Sqrt(bands[i])
	}
	return bands
}

func formatBands(b []float64) string {
	if len(b) == 0 {
		return ""
	}
	s := ""
	for i, v := range b {
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("%.2e", v)
	}
	return s
}

func formatLoss(raw, prep []float64) string {
	if len(raw) != len(prep) || len(raw) == 0 {
		return ""
	}
	s := ""
	for i := range raw {
		if i > 0 {
			s += " "
		}
		ratio := 0.0
		if raw[i] > 1e-20 {
			ratio = prep[i] / raw[i]
		}
		s += fmt.Sprintf("%.2f", ratio)
	}
	return s
}

func bandFreqLabels(freqs []float64, nBands int) string {
	if len(freqs) == 0 || nBands <= 0 {
		return ""
	}
	step := float64(len(freqs)) / float64(nBands)
	s := ""
	for i := 0; i < nBands; i++ {
		if i > 0 {
			s += " "
		}
		idx := int(float64(i) * step)
		if idx >= len(freqs) {
			idx = len(freqs) - 1
		}
		s += fmt.Sprintf("%.0f", freqs[idx])
	}
	return s
}

type distStats struct {
	min, max, mean, p50 float64
	values              []float64
}

func tokenDistanceMatrix(tokens []Pattern) distStats {
	var vals []float64
	for i := 0; i < len(tokens); i++ {
		for j := i + 1; j < len(tokens); j++ {
			if len(tokens[i].GlyphShape.Points) > 0 && len(tokens[j].GlyphShape.Points) > 0 {
				d := ShapeDistance(tokens[i].GlyphShape, tokens[j].GlyphShape)
				vals = append(vals, d)
			}
		}
	}
	if len(vals) == 0 {
		return distStats{}
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	var sum float64
	for _, v := range vals {
		sum += v
	}
	minV, maxV := vals[0], vals[0]
	for _, v := range vals {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	p50 := sorted[len(sorted)/2]
	return distStats{
		min: minV, max: maxV, mean: sum / float64(len(vals)), p50: p50,
		values: vals,
	}
}

func formatClosestPairs(tokens []Pattern, topK int) string {
	type pair struct {
		i, j int
		d    float64
	}
	var pairs []pair
	for i := 0; i < len(tokens); i++ {
		for j := i + 1; j < len(tokens); j++ {
			if len(tokens[i].GlyphShape.Points) > 0 && len(tokens[j].GlyphShape.Points) > 0 {
				d := ShapeDistance(tokens[i].GlyphShape, tokens[j].GlyphShape)
				pairs = append(pairs, pair{i, j, d})
			}
		}
	}
	sort.Slice(pairs, func(a, b int) bool { return pairs[a].d < pairs[b].d })
	if topK > len(pairs) {
		topK = len(pairs)
	}
	s := ""
	for k := 0; k < topK; k++ {
		if k > 0 {
			s += " "
		}
		s += fmt.Sprintf("(%d-%d:%.2f)", pairs[k].i+1, pairs[k].j+1, pairs[k].d)
	}
	return s
}

func formatGaps(tokens []Pattern) string {
	// мин расстояние от каждого токена до ближайшего другого
	s := ""
	for i := 0; i < len(tokens) && i < 12; i++ {
		if len(tokens[i].GlyphShape.Points) == 0 {
			continue
		}
		minD := 2.0
		for j := 0; j < len(tokens); j++ {
			if i == j || len(tokens[j].GlyphShape.Points) == 0 {
				continue
			}
			d := ShapeDistance(tokens[i].GlyphShape, tokens[j].GlyphShape)
			if d < minD {
				minD = d
			}
		}
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("%d:%.2f", i+1, minD)
	}
	return s
}

// LogTokenMap выводит полную карту расстояний: матрица N×N + соседи по каждому токену.
// Позволяет видеть, кто рядом с кем, и тюнить DistThr по матрице.
func LogTokenMap(tokens []Pattern) {
	n := len(tokens)
	if n == 0 {
		return
	}
	// Собираем только токены с формой
	type idxShape struct {
		i int
		p Pattern
	}
	var valid []idxShape
	for i := range tokens {
		if len(tokens[i].GlyphShape.Points) > 0 {
			valid = append(valid, idxShape{i, tokens[i]})
		}
	}
	if len(valid) == 0 {
		return
	}
	m := len(valid)
	// Матрица [i][j] = dist(valid[i], valid[j])
	mat := make([][]float64, m)
	for i := 0; i < m; i++ {
		mat[i] = make([]float64, m)
		for j := 0; j < m; j++ {
			if i == j {
				mat[i][j] = 0
			} else {
				mat[i][j] = ShapeDistance(valid[i].p.GlyphShape, valid[j].p.GlyphShape)
			}
		}
	}
	ids := make([]int, m)
	for i := range valid {
		ids[i] = valid[i].p.ID
	}
	log.Printf("[MAP] n=%d ids=%v", m, ids)
	// Заголовок матрицы
	head := "     |"
	for j := 0; j < m; j++ {
		head += fmt.Sprintf(" %5d", ids[j])
	}
	log.Printf("[MAP] matrix (row i = dist to each):\n[MAP] %s", head)
	for i := 0; i < m; i++ {
		row := fmt.Sprintf("%4d |", ids[i])
		for j := 0; j < m; j++ {
			row += fmt.Sprintf(" %5.2f", mat[i][j])
		}
		log.Printf("[MAP] %s", row)
	}
	// Соседи: для каждого токена — остальные по возрастанию dist
	log.Printf("[MAP] neighbors (ближайшие → дальние):")
	for i := 0; i < m; i++ {
		type jd struct {
			j int
			d float64
		}
		var nn []jd
		for j := 0; j < m; j++ {
			if i == j {
				continue
			}
			nn = append(nn, jd{j, mat[i][j]})
		}
		sort.Slice(nn, func(a, b int) bool { return nn[a].d < nn[b].d })
		s := fmt.Sprintf("[MAP]   %d:", ids[i])
		for k := 0; k < len(nn) && k < 12; k++ {
			s += fmt.Sprintf(" %d(%.2f)", ids[nn[k].j], nn[k].d)
		}
		log.Printf("%s", s)
	}
}

// LogNewPatternBlindSpotFromMergeResult при NEW: логирует по mr (dists до add). Почему не merge.
func LogNewPatternBlindSpotFromMergeResult(mr MergeResult, distThr float64) {
	if len(mr.DistsToAll) == 0 {
		log.Printf("[BLIND] NEW: store empty, first token")
		return
	}
	dists := append([]DistID(nil), mr.DistsToAll...)
	sort.Slice(dists, func(a, b int) bool { return dists[a].D < dists[b].D })
	best := dists[0]
	gap := 0.0
	if len(dists) > 1 {
		gap = dists[1].D - dists[0].D
		if gap < 0 {
			gap = 0
		}
	}
	thrHint := ""
	if best.D >= distThr && best.D < 1.5 {
		thrHint = fmt.Sprintf(" [нужен thr>=%.2f чтобы merge]", best.D)
	}
	log.Printf("[BLIND] NEW: best_match=%d dist=%.3f (thr=%.2f) gap=%.3f | dists_to_all=%s%s",
		mr.BestID, mr.DistShapeMin, distThr, gap, formatDistIDs(dists, 8), thrHint)
	if mr.RejectGate != "" {
		log.Printf("[BLIND] NEW reject: gate=%s reject_id=%d reject_dist=%.3f reject_cos=%.3f",
			mr.RejectGate, mr.BestRejectedID, mr.BestRejectedDist, mr.BestRejectedCos)
	}
}

func formatDistIDs(dists []DistID, maxN int) string {
	s := ""
	n := maxN
	if n > len(dists) {
		n = len(dists)
	}
	for i := 0; i < n; i++ {
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("%d:%.2f", dists[i].ID, dists[i].D)
	}
	return s
}
