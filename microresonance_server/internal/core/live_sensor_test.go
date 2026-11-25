// Тест пайплайна на реальном сенсоре в реалтайме.
// Запуск (только 20s тест, без бесконечного Loop): go test -v -run '^TestLiveSensorPipeline$' . -timeout 25s
// Протокол как у сервера по умолчанию (TCP). Останови ./server перед запуском.
//
// Архитектура: динамика = грамматика поверх устойчивых глифов, не компенсация DistThr.
//   — Только locked-глифы (HasGlyph) идут в динамику; кластер ≠ глиф.
//   — Событийная модель: сегмент [T_start..T_end, DurationMs] при смене глифа, не каждый тик.
//   — T_MIN_HOLD_MS=250 (переход = Glyph_A активен 200–400ms → Glyph_B), гистерезис 0.65, событийная модель по полосам (DurationMs при смене глифа), DistThr=0.8.
//   — Граф переходов: edge valid if count≥K и P(B|A)≥p (DynamicsFilterParams).
//
// Пошаговая отладка волна→глиф→динамика:
//   DEBUG_WAVES=1 — каждые 4 тика: peaks/rej/cls, по каждому пику: f,amp→id, reason (sig|fallback|new), d=dist до токена, gap; candidates, dominant, tape_last, locked, segment_pushed.
//   DEBUG_WAVES=2 — дополнительно по каждому пику: ShapeSig T/E/A/M, points_len, dists_to_all.
//   DEBUG_CLASSIFIER=1 — полный вывод классификатора: [tokens] id T E A M occ; по каждому пику [peak] sig T E A M → best_id reason | dists: id:d(sig_ok) id:d(sig_no) по возрастанию d.
// Шаги пайплайна: FFT→пики→StationarityOK→форма волны→ShapeSignature→MergeOrAddByShape(sig или fallback по DistThr)→только HasGlyph в кандидаты→доминант по amp, гистерезис 0.75→при смене глифа Push(сегмент с DurationMs)→holds≥T_MIN→лента→FindRepeatedChains.
// Цель: unique_ids не растёт без предела, tape = повторяющиеся цепочки (напр. 1→2→1→2), strong(≥3) растёт.

package core

import (
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
)

// liveParamsFromEnv возвращает params с переопределением из env для итеративного тюнинга.
// Для am-sine: DISTTHR=0.6 помогает схлопывать алфавит (меньше токенов → повторяемые последовательности).
func liveParamsFromEnv() ShapePipelineParams {
	p := defaultShapePipelineParams()
	p.AllowDiscovery = true
	if s := os.Getenv("DISTTHR"); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
			p.DistThr = v
		}
	} else {
		// Динамика = грамматика поверх устойчивых глифов. DistThr выше → меньше токенов, стабильнее счётчики и лента.
		// По умолчанию 0.85 (схлопывание алфавита — одна волна не раскалывается на много ID); DYNAMICS_COLLAPSE=1 → 0.88; AM_SINE=1 → 0.84.
		if os.Getenv("DYNAMICS_COLLAPSE") == "1" {
			if p.DistThr < 0.88 {
				p.DistThr = 0.88
			}
		} else if os.Getenv("AM_SINE") == "1" {
			if p.DistThr < 0.84 {
				p.DistThr = 0.84
			}
		} else {
			if p.DistThr < 0.85 {
				p.DistThr = 0.85
			}
		}
	}
	if s := os.Getenv("MINDELTAH"); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 {
			p.MinDeltaH = v
		}
	}
	if s := os.Getenv("HTHR"); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
			p.HThr = v
		}
	}
	if s := os.Getenv("MAXITERS"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 1 {
			p.MaxIters = v
		}
	}
	// Discovery C
	if s := os.Getenv("THRSCORE"); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
			p.ThrScore = v
		}
	}
	if s := os.Getenv("THRH"); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
			p.ThrH = v
		}
	}
	if s := os.Getenv("DISCOVERYBUFFERSEC"); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
			p.DiscoveryBufferSec = v
		}
	}
	if s := os.Getenv("A_CV"); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
			p.A_cv = v
		}
	}
	if s := os.Getenv("A_SLOPE"); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
			p.A_slope = v
		}
	}
	return p
}

// liveSeqParamsFromEnv — параметры последовательностей для лайв-теста (T_min_hold, SeqMinLen, SeqMaxLen, SeqMinCount).
// В тесте по умолчанию T_MIN=80ms: серия из 2 тиков (250ms) даёт hold, лента заполняется раньше.
func liveSeqParamsFromEnv() (tMinHoldMs int64, seqMinLen, seqMaxLen, seqMinCount int) {
	tMinHoldMs = 250 // переход = Glyph_A (активен 200–400ms) → Glyph_B; не ловим микро-колебания
	seqMinLen = SeqMinLenDefault
	seqMaxLen = SeqMaxLenDefault
	seqMinCount = SeqMinCountDefault
	if s := os.Getenv("T_MIN_HOLD_MS"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil && v >= 0 {
			tMinHoldMs = v
		}
	}
	if s := os.Getenv("SEQ_MIN_LEN"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 2 {
			seqMinLen = v
		}
	}
	if s := os.Getenv("SEQ_MAX_LEN"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= seqMinLen {
			seqMaxLen = v
		}
	}
	if s := os.Getenv("SEQ_MIN_COUNT"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 1 {
			seqMinCount = v
		}
	}
	return tMinHoldMs, seqMinLen, seqMaxLen, seqMinCount
}

// formatSequenceMetrics возвращает строку с метриками последовательностей (holds, tape, отфильтрованные seq, total_count).
// Цепочки из одного глифа [A→A] не считаются (ChainTrivial); только нетривиальные: хотя бы 2 разных ID.
// shortLine: если true, только одна строка для \r-вывода в Loop.
func formatSequenceMetrics(ds *DynamicsStore, tMinHoldMs int64, seqMinLen, seqMaxLen, seqMinCount int, shortLine bool) string {
	if ds == nil {
		return ""
	}
	holds, _ := ds.SnapshotFiltered(tMinHoldMs, -1)
	tape := ds.HoldPatternSequence(tMinHoldMs, -1)
	allSeqs := ds.FindRepeatedChainsFromHolds(tMinHoldMs, -1)
	var filtered []RepeatedChain
	var totalCount int
	for _, seq := range allSeqs {
		if seq.Count < seqMinCount {
			continue
		}
		ln := len(seq.Chain)
		if ln < seqMinLen || ln > seqMaxLen {
			continue
		}
		if ChainTrivial(seq.Chain) {
			continue
		}
		filtered = append(filtered, seq)
		totalCount += seq.Count
	}
	freq := ds.FindFrequentNgrams(tMinHoldMs, -1, 2, 4, 2)
	freqTotal := 0
	for _, seq := range freq {
		freqTotal += seq.Count
	}
	uniqueInTape := 0
	if len(tape) > 0 {
		seen := make(map[int]struct{})
		for _, id := range tape {
			seen[id] = struct{}{}
		}
		uniqueInTape = len(seen)
	}
	// Стабильная динамика = много повторений (count≥3 подряд), не «2 цепочки за 20 секунд».
	strongChains := 0
	for _, seq := range filtered {
		if seq.Count >= 3 {
			strongChains++
		}
	}
	s := fmt.Sprintf("[seq] T_min=%dms len=%d..%d minCount=%d | holds=%d tape=%d unique=%d seqs=%d total_count=%d strong(≥3)=%d freq_total=%d",
		tMinHoldMs, seqMinLen, seqMaxLen, seqMinCount, len(holds), len(tape), uniqueInTape, len(filtered), totalCount, strongChains, freqTotal)
	if shortLine {
		return s
	}
	const tapeSampleLen = 24
	if len(tape) > 0 {
		end := tapeSampleLen
		if end > len(tape) {
			end = len(tape)
		}
		sample := ""
		for i := 0; i < end; i++ {
			if i > 0 {
				sample += "→"
			}
			sample += fmt.Sprintf("%d", tape[i])
		}
		if len(tape) > tapeSampleLen {
			sample += "..."
		}
		s += fmt.Sprintf("\n[seq] tape_sample: [%s]", sample)
	}
	// Сначала жирные (count≥3) — «много повторяются».
	if len(freq) > 0 {
		var fat []RepeatedChain
		for _, seq := range freq {
			if seq.Count >= 3 {
				fat = append(fat, seq)
			}
		}
		if len(fat) > 0 {
			s += fmt.Sprintf("\n[seq] жирные (повтор в ленте ≥3): %d", len(fat))
			for i, seq := range fat {
				if i >= 5 {
					break
				}
				chain := ""
				for j, id := range seq.Chain {
					if j > 0 {
						chain += "→"
					}
					chain += fmt.Sprintf("%d", id)
				}
				s += fmt.Sprintf(" [%s]×%d", chain, seq.Count)
			}
		}
		s += fmt.Sprintf("\n[seq] frequent_ngrams (count≥2): %d, freq_total=%d", len(freq), freqTotal)
		for i, seq := range freq {
			if i >= 5 {
				break
			}
			chain := ""
			for j, id := range seq.Chain {
				if j > 0 {
					chain += "→"
				}
				chain += fmt.Sprintf("%d", id)
			}
			s += fmt.Sprintf(" [%s]×%d", chain, seq.Count)
		}
	}
	for i, seq := range filtered {
		chain := ""
		for j, id := range seq.Chain {
			if j > 0 {
				chain += "→"
			}
			chain += fmt.Sprintf("%d", id)
		}
		mark := ""
		if seq.Count >= 3 {
			mark = " ✓"
		}
		s += fmt.Sprintf("\n[seq]   #%d count=%d: [%s]%s", i+1, seq.Count, chain, mark)
	}
	if strongChains == 0 && len(tape) >= 6 {
		s += "\n[seq] стабильная динамика: нужны цепочки с повтором ≥3 подряд (strong(≥3)>0); freq_total растёт — хорошо"
	}
	if len(filtered) == 0 && len(freq) == 0 && len(tape) >= 4 {
		s += "\n[seq] tip: AM_SINE=1 or DYNAMICS_COLLAPSE=1 → fewer tokens, repeated chains"
	}
	return s
}

// logSequenceMetrics выводит метрики последовательностей в t.Logf.
func logSequenceMetrics(t *testing.T, ds *DynamicsStore, tMinHoldMs int64, seqMinLen, seqMaxLen, seqMinCount int) {
	if ds == nil {
		return
	}
	body := formatSequenceMetrics(ds, tMinHoldMs, seqMinLen, seqMaxLen, seqMinCount, false)
	if body == "" {
		return
	}
	for _, line := range splitLines(body) {
		t.Logf("%s", line)
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// formatDistsToAllWithSig сортирует по D и форматирует "id:d(sig)" для отладки классификатора (sig_ok=y, sig_no=n).
func formatDistsToAllWithSig(dists []DistIDWithSig, maxN int) string {
	if len(dists) == 0 {
		return ""
	}
	sorted := make([]DistIDWithSig, len(dists))
	copy(sorted, dists)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].D < sorted[j].D })
	n := maxN
	if n > len(sorted) {
		n = len(sorted)
	}
	s := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			s += " "
		}
		sig := "n"
		if sorted[i].SigMatch {
			sig = "y"
		}
		s += fmt.Sprintf("%d:%.2f(%s)", sorted[i].ID, sorted[i].D, sig)
	}
	if len(sorted) > n {
		s += "..."
	}
	return s
}

func TestLiveSensorPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("short: skip live sensor (requires ESP32 / port 5555)")
	}
	flag.Parse()
	_ = flag.Set("time", "5s") // 5 сек буфера для Discovery C (T=2..3 сек)

	buf := &buffers{
		sensors: make(map[uint8]*sensorData),
		logs:    make([]string, 0, 64),
	}
	currentIngestCfg := func() ingestConfig {
		return ingestConfig{
			AdxlRate:   *adxlRate,
			AdsRate:    *adsRate,
			TimeWindow: *timeWindow,
		}
	}

	useUDP := *proto == "udp"
	if useUDP {
		pc, err := net.ListenPacket("udp4", *addr)
		if err != nil {
			t.Fatalf("UDP listen %s: %v (останови ./server)", *addr, err)
		}
		defer pc.Close()
		t.Logf("[live] UDP %s, жду пакеты от ESP32...", *addr)
		go receiveUDP(pc, buf, currentIngestCfg)
	} else {
		ln, err := net.Listen("tcp", *addr)
		if err != nil {
			t.Fatalf("TCP listen %s: %v (останови ./server)", *addr, err)
		}
		defer ln.Close()
		t.Logf("[live] TCP %s, жду подключение ESP32...", *addr)
		go acceptTCP(ln, buf, currentIngestCfg)
	}

	params := liveParamsFromEnv()
	runStats := &ShapePipelineRunStats{}
	params.RunStats = runStats
	dynamicsStore := NewDynamicsStore()
	params.DynamicsStore = dynamicsStore
	tMinHold, seqMinLen, seqMaxLen, seqMinCount := liveSeqParamsFromEnv()
	// WaveDebug всегда включён для метрик SigMatchRate и Stability(T); DEBUG_WAVES=1 — дополнительный лог каждые 4 тика.
	waveDebug := &WaveDebugSnapshot{}
	params.WaveDebug = waveDebug
	if os.Getenv("DEBUG_WAVES") == "1" {
		t.Logf("[live] DEBUG_WAVES=1: волны→токен→динамика цифрами каждые 4 тика")
	}
	var totalMerged, totalSigMerge, totalNearSig, totalSoftE, totalFallback int
	var tByFreq []struct {
		freq    float64
		coarseT uint16
	}
	// Динамика по полосам: по одному глифу на полосу (B0..B7), гистерезис 0.65, событийная модель (сегмент с DurationMs при смене глифа).
	lastDynamicsIDPerBand := [8]int{-1, -1, -1, -1, -1, -1, -1, -1}
	params.DynamicsLastIDPerBand = lastDynamicsIDPerBand[:]
	var dynamicsSegmentStartMsPerBand [8]int64
	params.DynamicsSegmentStartMsPerBand = dynamicsSegmentStartMsPerBand[:]
	lastDynamicsID := -1
	params.DynamicsLastID = &lastDynamicsID
	var dynamicsSegmentStartMs int64
	var dynamicsLastAmp float64 = 0.5
	params.DynamicsSegmentStartMs = &dynamicsSegmentStartMs
	params.DynamicsLastAmp = &dynamicsLastAmp
	t.Logf("[live] params: DistThr=%.2f ThrScore=%.2f ThrH=%.2f A_cv=%.2f A_slope=%.2f DiscoveryBuf=%.1fs", params.DistThr, params.ThrScore, params.ThrH, params.A_cv, params.A_slope, params.DiscoveryBufferSec)
	t.Logf("[live] seq: T_MIN=%dms len=%d..%d minCount=%d | только locked глифы, сегменты, гистерезис 0.75 | unique_ids↓ seqs/total_count↑", tMinHold, seqMinLen, seqMaxLen, seqMinCount)
	t.Logf("[live] rejected = пики, не прошедшие StationarityOK. DEBUG_STATIONARITY=1 — причина по каждому. DEBUG_WAVES: reason=sig|fallback|new, d=dist до токена, gap=разрыв до 2-го; unique_ids↓ seqs↑ = хорошо.")
	store := NewShapeClusterStore(params.DistThr)
	decomposer := &ResidualDecomposer{}
	patternStore := &PatternStore{}
	primaryAlphabetStore := NewPrimaryAlphabetStore()
	params.OnPrimaryGlyphBatch = func(exciterID int, bandEnergy []float64, nowMs int64) {
		primaryAlphabetStore.UpdateAll(exciterID, bandEnergy, nowMs)
	}
	params.ExciterID = 0
	metrics := NewPipelineMetrics()
	metrics.OnNew = func(e PipelineEvent) {
		t.Logf("[NEW_PATTERN] reason=%s dist_shape_min=%.3f gap=%.3f conf=%.2f amp_norm=%.4f f0=%.0f best_match_id=%d",
			e.Reason, e.DistMin, e.Dist2Gap, e.Conf, e.AmpNorm, e.F0, e.TokenID)
	}
	var lastSpectrum time.Time
	metrics.OnSpectrum = func(raw, prep, residual []float64, st any, fs float64) {
		if time.Since(lastSpectrum) < 10*time.Second {
			return
		}
		lastSpectrum = time.Now()
		ps, ok := st.(*PatternStore)
		if !ok || ps == nil {
			return
		}
		LogSpectrumPipeline(raw, prep, residual, ps, fs)
	}
	fs := 4000.0
	if *adxlRate > 0 {
		fs = float64(*adxlRate)
	}

	interval := 250 * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	deadline := time.Now().Add(20 * time.Second)
	noDataDeadline := time.Now().Add(8 * time.Second)
	lastKpiLog := time.Now()
	var lastPkts uint64
	var lastClusters, lastLocked int
	var ticksWithData int
	var sumTotal, sumRejected, sumClassified int64

	for time.Now().Before(deadline) {
		<-ticker.C

		buf.mu.Lock()
		pkts := buf.packets
		var x, y, z []float64
		if len(buf.sensors) > 0 {
			var firstID uint8 = 255
			for id := range buf.sensors {
				if id < firstID {
					firstID = id
				}
			}
			if s := buf.sensors[firstID]; s != nil {
				x = copyFloats(s.adxlX, nil)
				y = copyFloats(s.adxlY, nil)
				z = copyFloats(s.adxlZ, nil)
			}
		}
		buf.mu.Unlock()

		if len(x) < 128 {
			if pkts > 0 && pkts != lastPkts {
				t.Logf("[live] pkts=%d буфер=%d, ждём...", pkts, len(x))
				lastPkts = pkts
			}
			if pkts == 0 && time.Now().After(noDataDeadline) {
				t.Logf("[live] нет пакетов за 8 сек — останови ./server, ESP32 подключается к %s?", *addr)
				break
			}
			continue
		}

		UpdateShapePipeline(x, y, z, fs, time.Now(), params, store, decomposer, patternStore, metrics)
		clusters, _ := store.ListClusters()
		clustersCount := len(clusters)
		locked := patternStore.Count()
		lockedGlyph := patternStore.CountWithGlyph()
		lastPkts = pkts
		lastClusters = clustersCount
		lastLocked = locked
		ticksWithData++
		// Раз в ~4 сек логируем энергии полос B0..B7 (Discovery): проверка, что B6 получает энергию при fs=4k
		if ticksWithData%16 == 1 && ticksWithData > 1 {
			var rawB [8]float64
			for b := 0; b < 8; b++ {
				if c := primaryAlphabetStore.Cell(0, b); c != nil {
					rawB[b] = c.RawE
				}
			}
			t.Logf("[live] band RawE B0..B7: %.4f %.4f %.4f %.4f %.4f %.4f %.4f %.4f (B6 при fs=4k должен быть >0 при сигнале 1.5–2k)", rawB[0], rawB[1], rawB[2], rawB[3], rawB[4], rawB[5], rawB[6], rawB[7])
		}

		// Метрики SigMatchRate и Stability(T): накапливаем по каждому классифицированному пику.
		for _, p := range waveDebug.ClassifiedPeaks {
			if p.Merged {
				totalMerged++
				switch p.MergeReason {
				case "sig":
					totalSigMerge++
				case "shape_e":
					totalSoftE++
				case "near_sig":
					totalNearSig++
					totalSigMerge++ // near_sig считаем в SigMatchRate
				case "fallback":
					totalFallback++
				}
			}
			tByFreq = append(tByFreq, struct {
				freq    float64
				coarseT uint16
			}{p.Freq, p.ShapeSig.CoarseTopologyCode})
		}

		total := runStats.PeaksTotal
		rej := runStats.PeaksRejectedStationarity
		cls := runStats.PeaksClassified
		sumTotal += int64(total)
		sumRejected += int64(rej)
		sumClassified += int64(cls)

		classPct := 0.0
		rejPct := 0.0
		if total > 0 {
			classPct = 100 * float64(cls) / float64(total)
			rejPct = 100 * float64(rej) / float64(total)
		}
		// Накопительно за сессию: какой % волны раскладывается, какой не попадает / не дособирается до глифа
		accClassPct := 0.0
		accRejPct := 0.0
		if sumTotal > 0 {
			accClassPct = 100 * float64(sumClassified) / float64(sumTotal)
			accRejPct = 100 * float64(sumRejected) / float64(sumTotal)
		}
		t.Logf("[live] pkts=%d | тик: всего=%d раскл=%d (%.0f%%) не_поп=%d (%.0f%%) | Σ раскл=%.1f%% не_поп=%.1f%% в_глифы=%d clusters=%d locked=%d",
			pkts, total, cls, classPct, rej, rejPct, accClassPct, accRejPct, lockedGlyph, clustersCount, locked)

		// DEBUG_WAVES: пошаговая отладка волна→токен→динамика (каждые 4 тика).
		if (os.Getenv("DEBUG_WAVES") == "1" || os.Getenv("DEBUG_WAVES") == "2") && ticksWithData%4 == 0 && (waveDebug.Classified > 0 || waveDebug.PeaksTotal > 0) {
			tape := dynamicsStore.HoldPatternSequence(tMinHold, 0)
			tapeLast := ""
			if len(tape) > 0 {
				start := len(tape) - 8
				if start < 0 {
					start = 0
				}
				if start > 0 {
					tapeLast = ".."
				}
				for i := start; i < len(tape); i++ {
					if i > start {
						tapeLast += ","
					}
					tapeLast += fmt.Sprintf("%d", tape[i])
				}
			}
			waveLine := fmt.Sprintf("[waves] tick %d: peaks=%d rej=%d cls=%d locked=%d pushed=%v | ",
				ticksWithData, waveDebug.PeaksTotal, waveDebug.Rejected, waveDebug.Classified, waveDebug.LockedCount, waveDebug.SegmentPushed)
			for _, p := range waveDebug.ClassifiedPeaks {
				// f, amp→id, reason, dist_min, gap
				waveLine += fmt.Sprintf(" f=%.0f amp=%.2f→id=%d %s d=%.3f gap=%.3f;", p.Freq, p.Amp, p.BestID, p.MergeReason, p.DistMin, p.SecondGap)
			}
			candLine := ""
			for _, c := range waveDebug.DynamicsCandidates {
				if candLine != "" {
					candLine += " "
				}
				candLine += fmt.Sprintf("%d:%.2f", c.ID, c.Amp)
			}
			t.Logf("%s | candidates [%s] → dominant=%d | tape_last=[%s]", waveLine, candLine, waveDebug.DominantID, tapeLast)
			// DEBUG_WAVES=2: ShapeSig, points_len, dists_to_all
			if os.Getenv("DEBUG_WAVES") == "2" {
				for i, p := range waveDebug.ClassifiedPeaks {
					sig := fmt.Sprintf("T=%d E=%d A=%d M=%d pts=%d", p.ShapeSig.TopologyCode, p.ShapeSig.EnvelopeClass, p.ShapeSig.AsymmetrySign, p.ShapeSig.ModulationFlag, p.PointsLen)
					distsStr := ""
					if len(p.DistsToAll) > 0 {
						n := 6
						if n > len(p.DistsToAll) {
							n = len(p.DistsToAll)
						}
						for j := 0; j < n; j++ {
							if j > 0 {
								distsStr += " "
							}
							distsStr += fmt.Sprintf("%d:%.2f", p.DistsToAll[j].ID, p.DistsToAll[j].D)
						}
						if len(p.DistsToAll) > n {
							distsStr += "..."
						}
					}
					t.Logf("  [peak %d] %s | dists_to_all=[%s]", i+1, sig, distsStr)
				}
			}
			// DEBUG_CLASSIFIER=1: полный вывод классификатора — какие токены (признаки T,E,A,M), куда легла каждая волна, по каким признакам
			if os.Getenv("DEBUG_CLASSIFIER") == "1" {
				list := patternStore.List()
				tokensLine := "[tokens]"
				for _, pat := range list {
					glyph := "—"
					if patternStore.HasGlyph(pat.ID) {
						glyph = "✓"
					}
					tokensLine += fmt.Sprintf(" id=%d T=%d E=%d A=%d M=%d occ=%d %s;", pat.ID, pat.ShapeSig.TopologyCode, pat.ShapeSig.EnvelopeClass, pat.ShapeSig.AsymmetrySign, pat.ShapeSig.ModulationFlag, pat.Occurrences, glyph)
				}
				t.Logf("%s", tokensLine)
				for i, p := range waveDebug.ClassifiedPeaks {
					sig := fmt.Sprintf("T=%d E=%d A=%d M=%d", p.ShapeSig.TopologyCode, p.ShapeSig.EnvelopeClass, p.ShapeSig.AsymmetrySign, p.ShapeSig.ModulationFlag)
					distsStr := formatDistsToAllWithSig(p.DistsToAllWithSig, 8)
					t.Logf("  [peak %d] f=%.0f amp=%.2f sig %s pts=%d → best_id=%d %s d=%.3f gap=%.3f | dists: %s",
						i+1, p.Freq, p.Amp, sig, p.PointsLen, p.BestID, p.MergeReason, p.DistMin, p.SecondGap, distsStr)
				}
			}
		}

		// KPI каждые 10 сек
		if time.Since(lastKpiLog) >= 10*time.Second {
			lastKpiLog = time.Now()
			snap := metrics.Snapshot(locked)
			LogKPI(snap)
			t.Logf("[live] KPI: reuse=%.2f merge=%.2f novelty=%.2f new_10s=%d", snap.ReuseRate, snap.MergeRate, snap.Novelty, snap.NewPer10s)
			t.Logf("[live] волна: раскладывается=%.1f%% (classified) не_попадает/не_дособир=%.1f%% (rejected) в_глифы=%d (locked)", accClassPct, accRejPct, lockedGlyph)
		}
		// Метрики последовательностей каждые ~1 сек (каждые 4 тика), чтобы видеть быстрый рост count повторяющихся паттернов
		if ticksWithData > 0 && ticksWithData%4 == 0 {
			logSequenceMetrics(t, dynamicsStore, tMinHold, seqMinLen, seqMaxLen, seqMinCount)
		}
	}

	accClassPct := 0.0
	accRejPct := 0.0
	if sumTotal > 0 {
		accClassPct = 100 * float64(sumClassified) / float64(sumTotal)
		accRejPct = 100 * float64(sumRejected) / float64(sumTotal)
	}
	t.Logf("[live] итог: раскладывается=%.1f%% не_попадает=%.1f%% в_глифы=%d clusters=%d locked=%d (Σ пиков %d)", accClassPct, accRejPct, patternStore.CountWithGlyph(), lastClusters, lastLocked, sumTotal)
	// SigMatchRate: доля мержей по CoarseSig (sig + shape_e + near_sig), цель 80%+.
	if totalMerged > 0 {
		sigMatchRate := 100 * float64(totalSigMerge+totalSoftE) / float64(totalMerged)
		t.Logf("[live] SigMatchRate: %.1f%% (sig=%d shape_e=%d near_sig=%d fallback=%d total=%d, цель ≥80%%)", sigMatchRate, totalSigMerge-totalNearSig, totalSoftE, totalNearSig, totalFallback, totalMerged)
	}
	// Stability(T): для полосы 280–360 Hz — распределение CoarseTopologyCode; цель 1–2 T доминируют.
	const bandLo, bandHi = 280.0, 360.0
	var bandT []struct {
		freq    float64
		coarseT uint16
	}
	for _, e := range tByFreq {
		if e.freq >= bandLo && e.freq <= bandHi {
			bandT = append(bandT, e)
		}
	}
	if len(bandT) > 0 {
		hist := make(map[uint16]int)
		for _, e := range bandT {
			hist[e.coarseT]++
		}
		var order []uint16
		for k := range hist {
			order = append(order, k)
		}
		sort.Slice(order, func(i, j int) bool { return hist[order[i]] > hist[order[j]] })
		line := "[live] Stability(T) band 280–360Hz:"
		for i, t := range order {
			if i >= 5 {
				line += " ..."
				break
			}
			pct := 100 * float64(hist[t]) / float64(len(bandT))
			line += fmt.Sprintf(" T=%d %.0f%%", t, pct)
		}
		t.Logf("%s (n=%d, цель 1–2 T доминируют)", line, len(bandT))
	}
	logSequenceMetrics(t, dynamicsStore, tMinHold, seqMinLen, seqMaxLen, seqMinCount)
	if ticksWithData == 0 {
		t.Skip("нет данных с сенсора — останови ./server, ESP32 подключается к " + *addr)
	}
	// Итог по полосам Discovery (B0..B7): проверка, что B6 получает энергию при fs=4k
	var rawB [8]float64
	for b := 0; b < 8; b++ {
		if c := primaryAlphabetStore.Cell(0, b); c != nil {
			rawB[b] = c.RawE
		}
	}
	t.Logf("[live] итог band RawE B0..B7: %.4f %.4f %.4f %.4f %.4f %.4f %.4f %.4f", rawB[0], rawB[1], rawB[2], rawB[3], rawB[4], rawB[5], rawB[6], rawB[7])
	sumLow := rawB[0] + rawB[1] + rawB[2] + rawB[3] + rawB[4] + rawB[5]
	if sumLow > 1e-6 && rawB[6] < 1e-9 {
		t.Logf("[live] предупреждение: B0–B5 имеют энергию, B6=0 — при fs=4k B6 должен показывать 1.5–2 kHz; проверь adxlRate и signal.NewBandpassFilter")
	}
}

// TestLiveSensorPipelineLoop — то же, но бесконечно до Ctrl+C (для отладки).
// Запуск: go test -v -run TestLiveSensorPipelineLoop -timeout 0
func TestLiveSensorPipelineLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("short: skip live sensor loop")
	}
	flag.Parse()
	_ = flag.Set("time", "1s")

	buf := &buffers{
		sensors: make(map[uint8]*sensorData),
		logs:    make([]string, 0, 64),
	}
	currentIngestCfg := func() ingestConfig {
		return ingestConfig{
			AdxlRate:   *adxlRate,
			AdsRate:    *adsRate,
			TimeWindow: *timeWindow,
		}
	}

	useUDP := *proto == "udp"
	if useUDP {
		pc, err := net.ListenPacket("udp4", *addr)
		if err != nil {
			t.Fatalf("UDP listen: %v", err)
		}
		defer pc.Close()
		fmt.Printf("UDP %s. ESP32 шлёт сюда. Ctrl+C — выход.\n", *addr)
		go receiveUDP(pc, buf, currentIngestCfg)
	} else {
		ln, err := net.Listen("tcp", *addr)
		if err != nil {
			t.Fatalf("TCP listen: %v", err)
		}
		defer ln.Close()
		fmt.Printf("TCP %s. ESP32 подключается сюда. Ctrl+C — выход.\n", *addr)
		go acceptTCP(ln, buf, currentIngestCfg)
	}

	params := liveParamsFromEnv()
	runStats := &ShapePipelineRunStats{}
	params.RunStats = runStats
	dynamicsStore := NewDynamicsStore()
	params.DynamicsStore = dynamicsStore
	tMinHold, seqMinLen, seqMaxLen, seqMinCount := liveSeqParamsFromEnv()
	lastDynamicsID := -1
	var dynamicsSegmentStartMs int64
	var dynamicsLastAmp float64 = 0.5
	params.DynamicsLastID = &lastDynamicsID
	params.DynamicsSegmentStartMs = &dynamicsSegmentStartMs
	params.DynamicsLastAmp = &dynamicsLastAmp
	store := NewShapeClusterStore(params.DistThr)
	decomposer := &ResidualDecomposer{}
	patternStore := &PatternStore{}
	fs := float64(*adxlRate)
	if fs < 1 {
		fs = 4000
	}

	interval := 250 * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var sumTotal, sumRejected, sumClassified int64
	for range ticker.C {
		buf.mu.Lock()
		pkts := buf.packets
		var x, y, z []float64
		if len(buf.sensors) > 0 {
			var firstID uint8 = 255
			for id := range buf.sensors {
				if id < firstID {
					firstID = id
				}
			}
			if s := buf.sensors[firstID]; s != nil {
				x = copyFloats(s.adxlX, nil)
				y = copyFloats(s.adxlY, nil)
				z = copyFloats(s.adxlZ, nil)
			}
		}
		buf.mu.Unlock()

		if len(x) < 128 {
			if pkts > 0 {
				fmt.Printf("\rpkts=%d буфер=%d (ждём)   ", pkts, len(x))
			}
			continue
		}

		UpdateShapePipeline(x, y, z, fs, time.Now(), params, store, decomposer, patternStore, nil)
		clusters, _ := store.ListClusters()
		locked := patternStore.Count()
		lockedGlyph := patternStore.CountWithGlyph()
		total := runStats.PeaksTotal
		rej, cls := runStats.PeaksRejectedStationarity, runStats.PeaksClassified
		sumTotal += int64(total)
		sumRejected += int64(rej)
		sumClassified += int64(cls)
		accClassPct, accRejPct := 0.0, 0.0
		if sumTotal > 0 {
			accClassPct = 100 * float64(sumClassified) / float64(sumTotal)
			accRejPct = 100 * float64(sumRejected) / float64(sumTotal)
		}
		seqLine := formatSequenceMetrics(dynamicsStore, tMinHold, seqMinLen, seqMaxLen, seqMinCount, true)
		if seqLine != "" {
			// одна строка: holds, tape_len, seqs, total_count
			fmt.Printf("\rpkts=%d Σ раскл=%.1f%% в_глифы=%d locked=%d | %s   ", pkts, accClassPct, lockedGlyph, locked, seqLine)
		} else {
			fmt.Printf("\rpkts=%d Σ раскл=%.1f%% не_поп=%.1f%% в_глифы=%d locked=%d clusters=%d   ",
				pkts, accClassPct, accRejPct, lockedGlyph, locked, len(clusters))
		}
	}
}
