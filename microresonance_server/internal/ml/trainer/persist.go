// Сохранение и загрузка ActionStore.
// Данные хранятся в data/trainer_dataset/actions.json (список действий).
// Сэмплы глифов — в data/trainer_dataset/<id>_<name>/*.json (см. trainer_tf.go).

package trainer

import (
	"encoding/json"
	"os"
	"time"
)

const (
	trainerDatasetDir     = "data/trainer_dataset"
	trainerActionsFile    = "data/trainer_dataset/actions.json"
	trainerDataFileLegacy = "trainer_data.json" // обратная совместимость
)

// actionPersist — сериализуемая версия Action для JSON.
type actionPersist struct {
	ID            int       `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	Samples       int       `json:"samples"`
	Quality       float64   `json:"quality"`
	LastTrainedMs int64     `json:"lastTrainedMs"`
	StateContext  string    `json:"stateContext,omitempty"`
	Prototype     []float64 `json:"prototype,omitempty"`
	RestPrototype []float64 `json:"restPrototype,omitempty"`
}

// trainerDataPersist — корневая структура для файла.
type trainerDataPersist struct {
	NextID  int             `json:"nextId"`
	Actions []actionPersist `json:"actions"`
}

// Save сохраняет ActionStore в data/trainer_dataset/actions.json.
func (s *ActionStore) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out trainerDataPersist
	out.NextID = s.nextID
	out.Actions = make([]actionPersist, 0, len(s.actions))
	for _, a := range s.actions {
		ap := actionPersist{
			ID:           a.ID,
			Name:         a.Name,
			Description:  a.Description,
			Samples:      a.Samples,
			Quality:      a.Quality,
			StateContext: a.StateContext,
		}
		if !a.LastTrained.IsZero() {
			ap.LastTrainedMs = a.LastTrained.UnixMilli()
		}
		if a.Prototype != nil {
			ap.Prototype = a.Prototype.Bands[:]
		}
		if a.RestPrototype != nil {
			ap.RestPrototype = a.RestPrototype.Bands[:]
		}
		out.Actions = append(out.Actions, ap)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(trainerDatasetDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(trainerActionsFile, data, 0644)
}

// Load загружает ActionStore из data/trainer_dataset/actions.json или trainer_data.json (legacy).
func (s *ActionStore) Load() error {
	path := trainerActionsFile
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			path = trainerDataFileLegacy
			data, err = os.ReadFile(path)
		}
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
	}
	if path == trainerDataFileLegacy {
		_ = os.MkdirAll(trainerDatasetDir, 0755)
	}
	// Удалить старый монолитный датасет, чтобы не было мусора
	_ = os.Remove("trainer_glyphs.json")
	var in trainerDataPersist
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = make([]*Action, 0, len(in.Actions))
	s.nextID = in.NextID
	if s.nextID < 1 {
		s.nextID = 1
	}
	for _, ap := range in.Actions {
		a := &Action{
			ID:           ap.ID,
			Name:         ap.Name,
			Description:  ap.Description,
			Samples:      ap.Samples,
			Quality:      ap.Quality,
			StateContext: ap.StateContext,
		}
		if ap.LastTrainedMs != 0 {
			a.LastTrained = time.UnixMilli(ap.LastTrainedMs)
		}
		if len(ap.Prototype) >= TrainerBandsN {
			sig := EventSignature{}
			copy(sig.Bands[:], ap.Prototype[:TrainerBandsN])
			a.Prototype = &sig
		}
		if len(ap.RestPrototype) >= TrainerBandsN {
			sig := EventSignature{}
			copy(sig.Bands[:], ap.RestPrototype[:TrainerBandsN])
			a.RestPrototype = &sig
		}
		s.actions = append(s.actions, a)
	}
	return nil
}
