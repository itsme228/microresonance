package trainer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestActionStoreAddListDelete(t *testing.T) {
	s := NewActionStore()
	a1 := s.Add("jaw_clench", "clench jaw")
	if a1 == nil || a1.ID != 1 || a1.Name != "jaw_clench" {
		t.Errorf("Add: got %+v", a1)
	}
	a2 := s.Add("exhale", "")
	if a2.ID != 2 {
		t.Errorf("Add second: got ID=%d", a2.ID)
	}
	list := s.List()
	if len(list) != 2 {
		t.Errorf("List: want 2, got %d", len(list))
	}
	s.Delete(1)
	list = s.List()
	if len(list) != 1 || list[0].ID != 2 {
		t.Errorf("After Delete(1): want [a2], got %+v", list)
	}
}

func TestGlyphShapeBuffer(t *testing.T) {
	b := NewGlyphShapeBuffer(5)
	// Контракт: в буфер попадают только глифы длины TrainerShapePoints (64) из пайплайна
	validPts := make([]float64, TrainerShapePoints)
	for i := range validPts {
		validPts[i] = float64(i) / float64(TrainerShapePoints)
	}
	b.Add(validPts, 0, 100)
	b.Add(validPts, 1, 200)
	samples := b.GetInWindow(50, 250)
	if len(samples) != 2 {
		t.Errorf("GetInWindow: want 2, got %d", len(samples))
	}
	if len(samples) >= 1 && (samples[0].Ts != 100 || samples[0].BandIdx != 0 || len(samples[0].Points) != TrainerShapePoints) {
		t.Errorf("first sample: Ts=%d BandIdx=%d len(Points)=%d", samples[0].Ts, samples[0].BandIdx, len(samples[0].Points))
	}
	b.Clear()
	samples = b.GetInWindow(0, 999)
	if len(samples) != 0 {
		t.Errorf("after Clear: want 0, got %d", len(samples))
	}
}

// TestSaveGlyphSamplesToDataset проверяет, что сэмплы из пайплайна (всегда 64 точки) сохраняются в датасет с правильными полями.
func TestSaveGlyphSamplesToDataset(t *testing.T) {
	store := NewActionStore()
	a := store.Add("test_save", "")
	if a == nil {
		t.Fatal("Add failed")
	}
	pts := make([]float64, TrainerShapePoints)
	for i := range pts {
		pts[i] = float64(i) / float64(TrainerShapePoints)
	}
	glyphSamplesPerAction := map[int][]GlyphSample{
		a.ID: {
			{Points: pts, BandIdx: 0, Ts: 100},
			{Points: pts, BandIdx: 1, Ts: 200},
		},
	}
	SaveGlyphSamplesToDataset(glyphSamplesPerAction, store)
	actionDir := filepath.Join(trainerDatasetDir, actionDirName(a.ID, a.Name))
	defer os.RemoveAll(actionDir)
	entries, err := os.ReadDir(actionDir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", actionDir, err)
	}
	var jsonFiles []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		jsonFiles = append(jsonFiles, e.Name())
	}
	sort.Strings(jsonFiles)
	if len(jsonFiles) != 2 {
		t.Fatalf("want 2 json files, got %d", len(jsonFiles))
	}
	wantSamples := []struct {
		BandIdx int
		Ts      int64
	}{
		{0, 100},
		{1, 200},
	}
	for i, fname := range jsonFiles {
		body, err := os.ReadFile(filepath.Join(actionDir, fname))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", fname, err)
		}
		var sample struct {
			Points  []float64 `json:"points"`
			BandIdx int       `json:"band_idx"`
			Ts      int64     `json:"ts"`
		}
		if err := json.Unmarshal(body, &sample); err != nil {
			t.Fatalf("%s Unmarshal: %v", fname, err)
		}
		if len(sample.Points) != TrainerShapePoints {
			t.Errorf("%s: len(points)=%d, want %d", fname, len(sample.Points), TrainerShapePoints)
		}
		if sample.BandIdx != wantSamples[i].BandIdx {
			t.Errorf("%s: band_idx=%d, want %d", fname, sample.BandIdx, wantSamples[i].BandIdx)
		}
		if sample.Ts != wantSamples[i].Ts {
			t.Errorf("%s: ts=%d, want %d", fname, sample.Ts, wantSamples[i].Ts)
		}
		// Значения points как при записи: pts[i] = i/64
		for j := range sample.Points {
			want := float64(j) / float64(TrainerShapePoints)
			if sample.Points[j] != want {
				t.Errorf("%s: points[%d]=%g, want %g", fname, j, sample.Points[j], want)
				break
			}
		}
	}
}

// TestSaveEventsToDataset проверяет, что события сохраняются отсортированными по Ts внутри каждого глифа.
func TestSaveEventsToDataset(t *testing.T) {
	store := NewActionStore()
	a := store.Add("test_events", "")
	if a == nil {
		t.Fatal("Add failed")
	}
	shape64 := make([]float64, 64)
	for i := range shape64 {
		shape64[i] = 0.1 * float64(i)
	}
	// Создаём события глифа 5 в неправильном порядке (Ts: 3000, 1000, 5000, 2000, 4000)
	events := []TrainerEvent{
		{A1: 1.0, Ts: 3000, GlyphID: 5, BandIdx: 1, Shape: shape64, Gx: shape64, Gy: shape64, Gz: shape64},
		{A1: 0.9, Ts: 1000, GlyphID: 5, BandIdx: 1, Shape: shape64, Gx: shape64, Gy: shape64, Gz: shape64},
		{A1: 1.1, Ts: 5000, GlyphID: 5, BandIdx: 1, Shape: shape64, Gx: shape64, Gy: shape64, Gz: shape64},
		{A1: 1.0, Ts: 2000, GlyphID: 5, BandIdx: 1, Shape: shape64, Gx: shape64, Gy: shape64, Gz: shape64},
		{A1: 1.05, Ts: 4000, GlyphID: 5, BandIdx: 1, Shape: shape64, Gx: shape64, Gy: shape64, Gz: shape64},
	}
	eventsPerAction := map[int][]TrainerEvent{a.ID: events}
	SaveEventsToDataset(eventsPerAction, store)
	actionDir := filepath.Join(trainerDatasetDir, actionDirName(a.ID, a.Name))
	defer os.RemoveAll(actionDir)
	glyphDir := filepath.Join(actionDir, "5")
	entries, err := os.ReadDir(glyphDir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", glyphDir, err)
	}
	var jsonFiles []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		jsonFiles = append(jsonFiles, e.Name())
	}
	sort.Strings(jsonFiles)
	if len(jsonFiles) != 5 {
		t.Fatalf("want 5 json files, got %d", len(jsonFiles))
	}
	// Проверяем, что файлы сохранены в порядке возрастания Ts (1000, 2000, 3000, 4000, 5000)
	expectedTs := []int64{1000, 2000, 3000, 4000, 5000}
	for i, fname := range jsonFiles {
		body, err := os.ReadFile(filepath.Join(glyphDir, fname))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", fname, err)
		}
		var ev struct {
			Shape []float64              `json:"shape"`
			Meta  map[string]interface{} `json:"meta"`
		}
		if err := json.Unmarshal(body, &ev); err != nil {
			t.Fatalf("%s Unmarshal: %v", fname, err)
		}
		ts := int64(0)
		glyphID := 0
		if ev.Meta != nil {
			if v, ok := ev.Meta["ts"].(float64); ok {
				ts = int64(v)
			}
			if v, ok := ev.Meta["glyph_id"].(float64); ok {
				glyphID = int(v)
			}
		}
		if ts != expectedTs[i] {
			t.Errorf("%s: ts=%d, want %d (последовательность эволюции нарушена)", fname, ts, expectedTs[i])
		}
		if glyphID != 5 {
			t.Errorf("%s: glyph_id=%d, want 5", fname, glyphID)
		}
		if len(ev.Shape) < 1 {
			t.Errorf("%s: empty shape", fname)
		}
	}
	t.Logf("✓ События глифа 5 сохранены в правильном порядке по Ts")
}

// TestSaveRunToDataset проверяет, что один запуск сохраняется в папку run_001: класс/запуск/глиф/variation.json.
func TestSaveRunToDataset(t *testing.T) {
	store := NewActionStore()
	a := store.Add("test_run", "")
	if a == nil {
		t.Fatal("Add failed")
	}
	actionDir := filepath.Join(trainerDatasetDir, actionDirName(a.ID, a.Name))
	defer os.RemoveAll(actionDir)
	// События с Shape/Gx/Gy/Gz (требуются для записи)
	shape64 := make([]float64, 64)
	for i := range shape64 {
		shape64[i] = 0.1 * float64(i)
	}
	events := []TrainerEvent{
		{A1: 1.0, Ts: 3000, GlyphID: 5, BandIdx: 1, Shape: shape64, Gx: shape64, Gy: shape64, Gz: shape64},
		{A1: 0.9, Ts: 1000, GlyphID: 5, BandIdx: 1, Shape: shape64, Gx: shape64, Gy: shape64, Gz: shape64},
		{A1: 1.1, Ts: 5000, GlyphID: 7, BandIdx: 0, Shape: shape64, Gx: shape64, Gy: shape64, Gz: shape64},
	}
	SaveRunToDataset(a.ID, events, store, nil, 0, 0, 0)
	runDir := filepath.Join(actionDir, "run_001")
	glyphDir5 := filepath.Join(runDir, "5")
	glyphDir7 := filepath.Join(runDir, "7")
	for _, dir := range []string{glyphDir5, glyphDir7} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir %s: %v", dir, err)
		}
		var jsons int
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				jsons++
			}
		}
		if jsons < 1 {
			t.Errorf("want at least 1 json in %s", dir)
		}
	}
	// В run_001/5 должны быть 2 файла (два события glyph 5), в run_001/7 — 1
	e5, _ := os.ReadDir(glyphDir5)
	e7, _ := os.ReadDir(glyphDir7)
	n5 := 0
	n7 := 0
	for _, e := range e5 {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n5++
		}
	}
	for _, e := range e7 {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n7++
		}
	}
	if n5 != 2 {
		t.Errorf("run_001/5: want 2 json files, got %d", n5)
	}
	if n7 != 1 {
		t.Errorf("run_001/7: want 1 json file, got %d", n7)
	}
	t.Logf("✓ SaveRunToDataset: класс/run_001/глиф/variation.json")
}
