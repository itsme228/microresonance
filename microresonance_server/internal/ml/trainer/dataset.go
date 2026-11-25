// Training dataset: data/trainer_dataset/<id>_<name>/ — folder per action, each sample — separate JSON.
// Python train_ml_queue_model.py reads from data/trainer_dataset/ folder.

package trainer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	domainalphabet "server/internal/domain/alphabet"
	domainglyph "server/internal/domain/glyph"
)

// Dataset only contains glyphs of length TrainerShapePoints (64) — pipeline contract (shapes.TrainerShapePoints).

// One glyph sample in file (legacy).
type trainerGlyphSampleJSON struct {
	Points  []float64 `json:"points"`
	BandIdx int       `json:"band_idx"`
	Ts      int64     `json:"ts"`
}

// One event in file: invariants (features), glyph variation coords (shape, x, y, z), reaction (16 AM phases), reaction_valid/reaction_ts and meta.
// meta — for storage/debug only; not fed to model (some fields duplicate features: freq_center, bandwidth etc.).
type trainerEventJSON struct {
	Features      []float64              `json:"features"`              // 17 invariants (main training signal)
	Shape         []float64              `json:"shape"`                 // glyph envelope (64 points)
	X             []float64              `json:"x"`                     // X axis (64)
	Y             []float64              `json:"y"`                     // Y axis (64)
	Z             []float64              `json:"z"`                     // Z axis (64)
	Reaction      []float64              `json:"reaction,omitempty"`    // 16 AM phases; if absent — zeros, reaction_valid=false
	ReactionValid bool                   `json:"reaction_valid"`        // true = data present; false = "no data" (model does not interpret zeros as "silence")
	ReactionTs    int64                  `json:"reaction_ts,omitempty"` // reaction emission time (ms); for event matching rule (nearest/within window)
	Meta          map[string]interface{} `json:"meta"`                  // glyph_id, band_idx, ts, ... — storage, not fed to model
}

var sanitizeNameRe = regexp.MustCompile(`[^a-zA-Z0-9_\p{L}]+`)

// actionDirName returns folder name for action: <id>_<sanitized_name>.
func actionDirName(id int, name string) string {
	sanitized := strings.TrimSpace(sanitizeNameRe.ReplaceAllString(name, "_"))
	sanitized = regexp.MustCompile(`_+`).ReplaceAllString(sanitized, "_")
	if sanitized == "" {
		sanitized = "action"
	}
	return fmt.Sprintf("%d_%s", id, sanitized)
}

// SaveGlyphSamplesToDataset saves samples to data/trainer_dataset/<id>_<name>/NNNNNN.json.
// Old trainer_glyphs.json removed on first save to new format.
func SaveGlyphSamplesToDataset(glyphSamplesPerAction map[int][]GlyphSample, store *ActionStore) {
	if store == nil {
		return
	}
	_ = os.MkdirAll(trainerDatasetDir, 0755)
	// Удалить старый монолитный файл, чтобы не было мусора
	_ = os.Remove("trainer_glyphs.json")

	for actionID, samples := range glyphSamplesPerAction {
		a := store.ByID(actionID)
		name := ""
		if a != nil {
			name = a.Name
		}
		dirName := actionDirName(actionID, name)
		actionDir := filepath.Join(trainerDatasetDir, dirName)
		if err := os.MkdirAll(actionDir, 0755); err != nil {
			continue
		}
		nextIdx := nextSampleIndex(actionDir)
		for _, s := range samples {
			if len(s.Points) != domainglyph.TrainerShapePoints {
				continue // контракт: пайплайн передаёт только 64 точки
			}
			fname := filepath.Join(actionDir, fmt.Sprintf("%06d.json", nextIdx))
			obj := trainerGlyphSampleJSON{Points: s.Points, BandIdx: s.BandIdx, Ts: s.Ts}
			data, err := json.Marshal(obj)
			if err != nil {
				continue
			}
			if err := os.WriteFile(fname, data, 0644); err != nil {
				continue
			}
			nextIdx++
		}
	}
}

// runMetaJSON — мета одного run для обучения с переходами (prev_action_id, sequence_index).
type runMetaJSON struct {
	ActionID       int `json:"action_id"`
	PrevActionID   int `json:"prev_action_id"`  // 0 = первый в цепочке
	SequenceIndex  int `json:"sequence_index"`  // segment index in chain (0-based)
	SequenceLength int `json:"sequence_length"` // chain length; 0 = no transition
}

// SaveRunToDataset saves one run (one Start–End press) to data/trainer_dataset/<action_id>_<name>/run_<N>/<glyph_id>/NNNNNN.json.
// run_<N>/ contains meta.json with transitions: prev_action_id, sequence_index, sequence_length — for transition training.
// runReactionBuffer — reaction vector buffer for segment [Start..End]; each event gets reaction by (0, ev.BandIdx). May be nil.
func SaveRunToDataset(actionID int, events []TrainerEvent, store *ActionStore, runReactionBuffer *RunReactionBuffer, prevActionID, sequenceIndex, sequenceLength int) {
	if store == nil || len(events) == 0 {
		return
	}
	a := store.ByID(actionID)
	name := ""
	if a != nil {
		name = a.Name
	}
	dirName := actionDirName(actionID, name)
	actionDir := filepath.Join(trainerDatasetDir, dirName)
	if err := os.MkdirAll(actionDir, 0755); err != nil {
		return
	}
	runN := nextRunIndex(actionDir)
	runDir := filepath.Join(actionDir, runDirName(runN))
	_ = os.MkdirAll(runDir, 0755)
	// Transition meta for A→B→C transition training.
	meta := runMetaJSON{ActionID: actionID, PrevActionID: prevActionID, SequenceIndex: sequenceIndex, SequenceLength: sequenceLength}
	if metaData, err := json.Marshal(meta); err == nil {
		_ = os.WriteFile(filepath.Join(runDir, "meta.json"), metaData, 0644)
	}
	byGlyph := make(map[int][]TrainerEvent)
	for _, ev := range events {
		byGlyph[ev.GlyphID] = append(byGlyph[ev.GlyphID], ev)
	}
	for glyphID, glyphEvents := range byGlyph {
		sortTrainerEventsByTs(glyphEvents)
		glyphDir := filepath.Join(runDir, fmt.Sprintf("%d", glyphID))
		if err := os.MkdirAll(glyphDir, 0755); err != nil {
			continue
		}
		nextIdx := 1
		for _, ev := range glyphEvents {
			if len(ev.Shape) < 10 || len(ev.Gx) < 10 || len(ev.Gy) < 10 || len(ev.Gz) < 10 {
				continue
			}
			var reaction []float64
			var reactionTs int64
			reactionValid := false
			if runReactionBuffer != nil {
				reaction, reactionTs, reactionValid = runReactionBuffer.Get(0, ev.BandIdx)
			}
			obj := trainerEventJSON{
				Features:      ev.Invariants(),
				Shape:         ev.Shape,
				X:             ev.Gx,
				Y:             ev.Gy,
				Z:             ev.Gz,
				ReactionValid: reactionValid && len(reaction) == domainalphabet.ReactionPhaseBins,
				Meta:          ev.Meta(),
			}
			if obj.ReactionValid {
				obj.Reaction = reaction
				obj.ReactionTs = reactionTs
			}
			fname := filepath.Join(glyphDir, fmt.Sprintf("%06d.json", nextIdx))
			data, err := json.Marshal(obj)
			if err != nil {
				continue
			}
			if err := os.WriteFile(fname, data, 0644); err != nil {
				continue
			}
			nextIdx++
		}
	}
}

// SaveEventsToDataset сохраняет event-векторы в data/trainer_dataset/<action_id>_<name>/<glyph_id>/NNNNNN.json.
// Legacy: один общий свал без разделения по запускам. Для нового кода используется SaveRunToDataset при каждом End.
func SaveEventsToDataset(eventsPerAction map[int][]TrainerEvent, store *ActionStore) {
	if store == nil {
		return
	}
	_ = os.MkdirAll(trainerDatasetDir, 0755)
	_ = os.Remove("trainer_glyphs.json")
	for actionID, events := range eventsPerAction {
		a := store.ByID(actionID)
		name := ""
		if a != nil {
			name = a.Name
		}
		dirName := actionDirName(actionID, name)
		actionDir := filepath.Join(trainerDatasetDir, dirName)
		byGlyph := make(map[int][]TrainerEvent)
		for _, ev := range events {
			byGlyph[ev.GlyphID] = append(byGlyph[ev.GlyphID], ev)
		}
		for glyphID, glyphEvents := range byGlyph {
			glyphDir := filepath.Join(actionDir, fmt.Sprintf("%d", glyphID))
			if err := os.MkdirAll(glyphDir, 0755); err != nil {
				continue
			}
			sortTrainerEventsByTs(glyphEvents)
			nextIdx := nextSampleIndex(glyphDir)
			for _, ev := range glyphEvents {
				if len(ev.Shape) < 10 || len(ev.Gx) < 10 || len(ev.Gy) < 10 || len(ev.Gz) < 10 {
					continue
				}
				fname := filepath.Join(glyphDir, fmt.Sprintf("%06d.json", nextIdx))
				obj := trainerEventJSON{
					Features: ev.Invariants(),
					Shape:    ev.Shape,
					X:        ev.Gx,
					Y:        ev.Gy,
					Z:        ev.Gz,
					Meta:     ev.Meta(),
				}
				data, err := json.Marshal(obj)
				if err != nil {
					continue
				}
				if err := os.WriteFile(fname, data, 0644); err != nil {
					continue
				}
				nextIdx++
			}
		}
	}
}

// sortTrainerEventsByTs sorts events by Ts ascending (in-place).
func sortTrainerEventsByTs(events []TrainerEvent) {
	for i := 0; i < len(events)-1; i++ {
		for j := i + 1; j < len(events); j++ {
			if events[j].Ts < events[i].Ts {
				events[i], events[j] = events[j], events[i]
			}
		}
	}
}

// runDirName возвращает имя папки запуска: run_001, run_002, ...
func runDirName(runIndex int) string {
	return fmt.Sprintf("run_%03d", runIndex)
}

// nextRunIndex возвращает следующий номер запуска в папке действия (сканирует run_001, run_002, ...).
func nextRunIndex(actionDir string) int {
	entries, err := os.ReadDir(actionDir)
	if err != nil {
		return 1
	}
	max := 0
	prefix := "run_"
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(e.Name(), prefix))
		if err != nil || n < 1 {
			continue
		}
		if n > max {
			max = n
		}
	}
	return max + 1
}

// nextSampleIndex возвращает следующий свободный индекс в папке (по именам NNNNNN.json).
func nextSampleIndex(actionDir string) int {
	entries, err := os.ReadDir(actionDir)
	if err != nil {
		return 1
	}
	max := -1
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".json")
		n, err := strconv.Atoi(base)
		if err != nil || n < 0 {
			continue
		}
		if n > max {
			max = n
		}
	}
	return max + 1
}
