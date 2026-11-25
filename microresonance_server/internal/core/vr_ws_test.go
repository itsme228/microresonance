// Тест: экраны алфавитов в VR получают глифы из реальных данных сенсора.
// Проверяем, что buildAlphabetSnapshotPayload отдаёт непустой glyphs при паттернах
// с минимальными данными (только GlyphData 8+ точек, без полного GlyphShape).

package core

import (
	"encoding/json"
	"testing"
)

func TestBuildAlphabetSnapshotPayload_WithMinimalGlyphData(t *testing.T) {
	// Паттерн как от сенсора: пустой GlyphShape.Points, но GlyphData с 10 точками
	// (minimalShapeFromData даст 8 точек и карточка попадёт в рассылку).
	glyphData := make([]float64, 10)
	for i := range glyphData {
		glyphData[i] = float64(i) * 0.1
	}
	patterns := []Pattern{
		{
			ID:          1,
			BandIdx:     0,
			Occurrences: 5,
			Freq:        200,
			GlyphShape:  ShapeVector{Points: nil},
			GlyphData:   glyphData,
		},
	}
	var reactions []ReactionPattern

	b, err := buildAlphabetSnapshotPayload(patterns, reactions, 0)
	if err != nil {
		t.Fatalf("buildAlphabetSnapshotPayload: %v", err)
	}
	var msg struct {
		Type   string `json:"type"`
		Glyphs []struct {
			ID          int       `json:"id"`
			BandIdx     int       `json:"band_idx"`
			Occurrences int       `json:"occurrences"`
			Freq        float64   `json:"freq"`
			Shape       []float64 `json:"shape"`
		} `json:"glyphs"`
		Reactions []interface{} `json:"reactions"`
	}
	if err := json.Unmarshal(b, &msg); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if msg.Type != "alphabet_snapshot" {
		t.Errorf("type = %q, want alphabet_snapshot", msg.Type)
	}
	if len(msg.Glyphs) == 0 {
		t.Error("glyphs empty: VR экран алфавита останется «Ожидание данных»; нужен хотя бы один глиф из паттерна с GlyphData 8+ точек")
	}
	if len(msg.Glyphs) > 0 {
		if len(msg.Glyphs[0].Shape) < 8 {
			t.Errorf("glyphs[0].shape length = %d, need >= 8 for VR", len(msg.Glyphs[0].Shape))
		}
		if msg.Glyphs[0].ID != 1 || msg.Glyphs[0].BandIdx != 0 || msg.Glyphs[0].Occurrences != 5 {
			t.Errorf("glyphs[0] id/band/occurrences = %d/%d/%d", msg.Glyphs[0].ID, msg.Glyphs[0].BandIdx, msg.Glyphs[0].Occurrences)
		}
	}
}

func TestBuildFullSnapshotPayload(t *testing.T) {
	glyphData := make([]float64, 10)
	for i := range glyphData {
		glyphData[i] = float64(i) * 0.1
	}
	patterns := []Pattern{
		{
			ID: 1, BandIdx: 0, Occurrences: 5, Freq: 200,
			GlyphShape: ShapeVector{Points: nil},
			GlyphData:  glyphData,
		},
	}
	reactions := []ReactionPattern{
		{
			ID: 1, ExciterID: 1, BandID: 2, Occurrences: 3, TotalEnergy: 0.5,
		},
	}
	input := vrFullSnapshotInput{
		Patterns:  patterns,
		Reactions: reactions,
		Discovery: []vrDiscoveryCell{
			{ExciterID: 0, ExciterName: "idle", BandIdx: 4, RawE: 0.123},
			{ExciterID: 1, ExciterName: "am-sine", BandIdx: 5, RawE: 0.456},
		},
		Trainer: vrTrainerSnapshot{
			Actions:     []vrTrainerAction{{ID: 1, Name: "LEFT", Samples: 10, Quality: 0.9}},
			Calibrating: true,
			Phase:       "do",
		},
		Meaning: vrMeaningSnapshot{
			ModelLoaded:    true,
			PredictionName: "LEFT",
			Confidence:     0.95,
			Counters:       map[string]int{"LEFT": 5, "RIGHT": 2},
			Actions:        []string{"LEFT", "RIGHT"},
		},
		Header:     vrHeaderInfo{Packets: 100, SensorID: 1, AdxlRate: 4000, AdsRate: 40000},
		BandEnergy: [8]float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8},
	}
	b, err := buildFullSnapshotPayload(input)
	if err != nil {
		t.Fatalf("buildFullSnapshotPayload: %v", err)
	}
	var msg map[string]interface{}
	if err := json.Unmarshal(b, &msg); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if msg["type"] != "full_snapshot" {
		t.Errorf("type = %q, want full_snapshot", msg["type"])
	}
	glyphs, _ := msg["glyphs"].([]interface{})
	if len(glyphs) == 0 {
		t.Error("glyphs empty in full_snapshot")
	}
	reactions2, _ := msg["reactions"].([]interface{})
	if len(reactions2) == 0 {
		t.Error("reactions empty in full_snapshot")
	}
	discovery, _ := msg["discovery"].([]interface{})
	if len(discovery) != 2 {
		t.Errorf("discovery len = %d, want 2", len(discovery))
	}
	trainer, ok := msg["trainer"].(map[string]interface{})
	if !ok || trainer["calibrating"] != true {
		t.Errorf("trainer.calibrating = %v, want true", trainer["calibrating"])
	}
	meaning, ok := msg["meaning"].(map[string]interface{})
	if !ok || meaning["prediction_name"] != "LEFT" {
		t.Errorf("meaning.prediction_name = %v, want LEFT", meaning["prediction_name"])
	}
	header, ok := msg["header"].(map[string]interface{})
	if !ok {
		t.Fatal("header missing")
	}
	if header["packets"] != float64(100) {
		t.Errorf("header.packets = %v, want 100", header["packets"])
	}
	bandEnergy, _ := msg["band_energy"].([]interface{})
	if len(bandEnergy) != 8 {
		t.Errorf("band_energy len = %d, want 8", len(bandEnergy))
	}
	bandLabels, _ := msg["band_labels"].([]interface{})
	if len(bandLabels) < 8 {
		t.Errorf("band_labels len = %d, want >= 8", len(bandLabels))
	}
}

func TestBuildAlphabetSnapshotPayload_WithFullGlyphShape(t *testing.T) {
	// Паттерн с заполненным GlyphShape.Points (как после мержа в алфавите).
	shapePoints := make([]float64, 32)
	for i := range shapePoints {
		shapePoints[i] = float64(i) * 0.02
	}
	patterns := []Pattern{
		{
			ID:          2,
			BandIdx:     1,
			Occurrences: 10,
			Freq:        400,
			GlyphShape:  ShapeVector{Points: shapePoints},
		},
	}

	b, err := buildAlphabetSnapshotPayload(patterns, nil, 0)
	if err != nil {
		t.Fatalf("buildAlphabetSnapshotPayload: %v", err)
	}
	var msg struct {
		Type   string          `json:"type"`
		Glyphs json.RawMessage `json:"glyphs"`
	}
	if err := json.Unmarshal(b, &msg); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if msg.Type != "alphabet_snapshot" {
		t.Errorf("type = %q", msg.Type)
	}
	var glyphs []map[string]interface{}
	if err := json.Unmarshal(msg.Glyphs, &glyphs); err != nil {
		t.Fatalf("glyphs unmarshal: %v", err)
	}
	if len(glyphs) != 1 {
		t.Errorf("len(glyphs) = %d, want 1", len(glyphs))
	}
	shape, _ := glyphs[0]["shape"].([]interface{})
	if len(shape) < 8 {
		t.Errorf("shape length = %d, need >= 8", len(shape))
	}
}
