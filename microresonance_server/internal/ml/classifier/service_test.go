// Тест glyph_classifier: загружает модель, по 10 случайных ранов из каждого класса
// датасета, проверяет соответствие предсказания ожидаемому классу.

package classifier

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

const samplesPerClass = 10

func TestGlyphClassifierPredict(t *testing.T) {
	modelDir := "data/saved_model"
	metaPath := filepath.Join(modelDir, "meta.json")
	if _, err := os.Stat(metaPath); err != nil {
		t.Skip("data/saved_model/meta.json not found, skip")
	}
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Format != "glyph_classifier" {
		t.Skipf("format %q != glyph_classifier", meta.Format)
	}

	cl, err := NewGlyphClassifier(modelDir)
	if err != nil {
		t.Fatalf("NewGlyphClassifier: %v", err)
	}
	defer cl.Close()

	rand.Seed(42)

	// Классы: 1_LEFT -> action_id 1, 2_RIGHT -> 2, 3_BOTH -> 3
	classes := []struct {
		dir    string
		action int
	}{
		{"1_LEFT", 1},
		{"2_RIGHT", 2},
		{"3_BOTH", 3},
	}

	datasetDir := "data/trainer_dataset"
	var totalCorrect, totalCount int

	for _, c := range classes {
		classDir := filepath.Join(datasetDir, c.dir)
		matches, _ := filepath.Glob(filepath.Join(classDir, "run_*", "*", "*.json"))
		if len(matches) == 0 {
			t.Logf("class %s: no samples, skip", c.dir)
			continue
		}

		// Перемешиваем и берём до samplesPerClass
		rand.Shuffle(len(matches), func(i, j int) { matches[i], matches[j] = matches[j], matches[i] })
		n := samplesPerClass
		if len(matches) < n {
			n = len(matches)
		}
		samples := matches[:n]

		correct := 0
		for _, path := range samples {
			sampleData, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("read %s: %v", path, err)
				continue
			}
			var ev struct {
				Shape    []float64 `json:"shape"`
				X        []float64 `json:"x"`
				Y        []float64 `json:"y"`
				Z        []float64 `json:"z"`
				Features []float64 `json:"features"`
			}
			if err := json.Unmarshal(sampleData, &ev); err != nil {
				t.Errorf("parse %s: %v", path, err)
				continue
			}
			pred, err := cl.PredictFromArrays(ev.Shape, ev.X, ev.Y, ev.Z, ev.Features)
			if err != nil {
				t.Errorf("Predict %s: %v", path, err)
				continue
			}
			if pred.ActionID == c.action {
				correct++
			}
			totalCount++
		}
		totalCorrect += correct
		t.Logf("%s: %d/%d correct", c.dir, correct, len(samples))
	}

	if totalCount == 0 {
		t.Skip("no trainer_dataset samples")
	}
	acc := float64(totalCorrect) / float64(totalCount)
	t.Logf("total: %d/%d correct (%.1f%%)", totalCorrect, totalCount, acc*100)
	if acc < 0.5 {
		t.Errorf("accuracy %.1f%% too low", acc*100)
	}
}
