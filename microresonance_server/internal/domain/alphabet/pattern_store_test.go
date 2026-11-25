package alphabet

import "testing"

func testShapeVector64() ShapeVector {
	pts := make([]float64, 64)
	for i := 0; i < 64; i++ {
		x := float64(i) / 63.0
		// Плавная "телесная" форма: один основной бугор + лёгкий хвост.
		pts[i] = 1.2*x*(1.0-x) - 0.15*x
	}
	return ShapeVector{Points: pts}
}

func TestPatternStoreEstablishedGlyphSurvivesFreqGate(t *testing.T) {
	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}
	shape := testShapeVector64()
	sig := ShapeSignature{CoarseTopologyCode: 27, TopologyCode: 15, EnvelopeClass: 0, AsymmetrySign: 0, ModulationFlag: 0}

	// Стабилизируем глиф в B5.
	for i := 0; i < 3; i++ {
		p := Pattern{
			Freq:       760,
			BandIdx:    5,
			Amp:        1,
			GlyphShape: shape,
			ShapeSig:   sig,
			Captured:   int64(1000 + i*1000),
			Confidence: 0.9,
		}
		mr := store.MergeOrAddByShape(p, params)
		if i == 0 && mr.Merged {
			t.Fatal("first add must not merge")
		}
	}
	if store.Count() != 1 {
		t.Fatalf("after stabilization: count=%d want 1", store.Count())
	}

	// |1000-760|=240: для B5 это за пределами расширенного high-band freq gate; fallback должен удержать established-глиф.
	pJitter := Pattern{
		Freq:       1000,
		BandIdx:    5,
		Amp:        1,
		GlyphShape: shape,
		ShapeSig:   sig,
		Captured:   5000,
		Confidence: 0.9,
	}
	mr := store.MergeOrAddByShape(pJitter, params)
	if !mr.Merged {
		t.Fatalf("established glyph should merge under strong shape-match despite freq gate")
	}
	if mr.MergeReason != "fallback" {
		t.Fatalf("merge reason = %q, want fallback", mr.MergeReason)
	}
	if store.Count() != 1 {
		t.Fatalf("after freq fallback merge: count=%d want 1", store.Count())
	}
}

func TestPatternStoreEstablishedGlyphSurvivesSignatureGate(t *testing.T) {
	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}
	shape := testShapeVector64()
	baseSig := ShapeSignature{CoarseTopologyCode: 10, TopologyCode: 11, EnvelopeClass: 0, AsymmetrySign: -1, ModulationFlag: 0}

	// Стабилизируем глиф в B4 (не noisy band).
	for i := 0; i < 3; i++ {
		p := Pattern{
			Freq:       286,
			BandIdx:    4,
			Amp:        1,
			GlyphShape: shape,
			ShapeSig:   baseSig,
			Captured:   int64(1000 + i*1000),
			Confidence: 0.9,
		}
		store.MergeOrAddByShape(p, params)
	}
	if store.Count() != 1 {
		t.Fatalf("after stabilization: count=%d want 1", store.Count())
	}

	// В B4 сигнатурный gate обычно отклоняет другой AsymmetrySign, fallback должен удержать ID.
	jitterSig := baseSig
	jitterSig.AsymmetrySign = 1
	pJitter := Pattern{
		Freq:       288,
		BandIdx:    4,
		Amp:        1,
		GlyphShape: shape,
		ShapeSig:   jitterSig,
		Captured:   5000,
		Confidence: 0.9,
	}
	mr := store.MergeOrAddByShape(pJitter, params)
	if !mr.Merged {
		t.Fatalf("established glyph should merge under strong shape-match despite signature gate")
	}
	if mr.MergeReason != "fallback" {
		t.Fatalf("merge reason = %q, want fallback", mr.MergeReason)
	}
	if store.Count() != 1 {
		t.Fatalf("after signature fallback merge: count=%d want 1", store.Count())
	}
}

func TestPatternStoreHighBandProbationPreventsNewOnNearDuplicate(t *testing.T) {
	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}
	shape := testShapeVector64()
	baseSig := ShapeSignature{CoarseTopologyCode: 27, TopologyCode: 15, EnvelopeClass: 0, AsymmetrySign: 0, ModulationFlag: 0}

	// Первый глиф в B5.
	p1 := Pattern{
		Freq:       700,
		BandIdx:    5,
		Amp:        1,
		GlyphShape: shape,
		ShapeSig:   baseSig,
		Captured:   1000,
		Confidence: 0.9,
	}
	mr1 := store.MergeOrAddByShape(p1, params)
	if mr1.Merged {
		t.Fatal("first add must not merge")
	}
	if store.Count() != 1 {
		t.Fatalf("after first add: count=%d want 1", store.Count())
	}

	// Почти тот же сигнал, но с дрожанием AsymmetrySign:
	// strict signature gate отклонит, high-band probation должен смержить.
	jitterSig := baseSig
	jitterSig.AsymmetrySign = -1
	p2 := Pattern{
		Freq:       704,
		BandIdx:    5,
		Amp:        1,
		GlyphShape: shape,
		ShapeSig:   jitterSig,
		Captured:   2000,
		Confidence: 0.9,
	}
	mr2 := store.MergeOrAddByShape(p2, params)
	if !mr2.Merged {
		t.Fatalf("high-band probation should merge near-duplicate instead of creating new")
	}
	if mr2.MergeReason != "fallback" {
		t.Fatalf("merge reason = %q, want fallback", mr2.MergeReason)
	}
	if store.Count() != 1 {
		t.Fatalf("after probation merge: count=%d want 1", store.Count())
	}
}

func TestPatternStoreMidBandProbationPreventsNewOnNearDuplicate(t *testing.T) {
	store := &PatternStore{}
	params := ShapeMergeParams{MinCosForMerge: 0}
	shape := testShapeVector64()
	baseSig := ShapeSignature{CoarseTopologyCode: 10, TopologyCode: 9, EnvelopeClass: 0, AsymmetrySign: 1, ModulationFlag: 0}

	// Первый глиф в B2.
	p1 := Pattern{
		Freq:       16,
		BandIdx:    2,
		Amp:        1,
		GlyphShape: shape,
		ShapeSig:   baseSig,
		Captured:   1000,
		Confidence: 0.9,
	}
	mr1 := store.MergeOrAddByShape(p1, params)
	if mr1.Merged {
		t.Fatal("first add must not merge")
	}
	if store.Count() != 1 {
		t.Fatalf("after first add: count=%d want 1", store.Count())
	}

	// Дрожание AsymmetrySign в той же coarse-топологии: strict signature может отклонить,
	// mid-band probation должен удержать один ID.
	jitterSig := baseSig
	jitterSig.AsymmetrySign = -1
	p2 := Pattern{
		Freq:       18,
		BandIdx:    2,
		Amp:        1,
		GlyphShape: shape,
		ShapeSig:   jitterSig,
		Captured:   2000,
		Confidence: 0.9,
	}
	mr2 := store.MergeOrAddByShape(p2, params)
	if !mr2.Merged {
		t.Fatalf("mid-band probation should merge near-duplicate instead of creating new")
	}
	if mr2.MergeReason != "fallback" {
		t.Fatalf("merge reason = %q, want fallback", mr2.MergeReason)
	}
	if store.Count() != 1 {
		t.Fatalf("after probation merge: count=%d want 1", store.Count())
	}
}
