// VR API: /api/glyphs и /api/glyphs/{id}/examples — данные из trainer_dataset для VR Glyph Browser.
// Использует PeakProfileFromShape (shapes.go) — тот же proto_sig, что в GUI и ML.

package httpapi

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	domainglyph "server/internal/domain/glyph"
)

const vrAPIDatasetDir = "data/trainer_dataset"

// vrGlyphEntry — одна запись каталога глифов для VR.
type vrGlyphEntry struct {
	ID             string    `json:"id"` // "1_LEFT_5" — action_dir + glyph_id
	ActionID       int       `json:"action_id"`
	ActionName     string    `json:"action_name"`
	GlyphID        int       `json:"glyph_id"`
	Count          int       `json:"count"`
	ProtoSig       []float64 `json:"proto_sig"` // 8 чисел (PeakProfile)
	FreqCenterMean float64   `json:"freq_center_mean"`
	BandIdxMode    int       `json:"band_idx_mode"`
	FeaturesMean   []float64 `json:"features_mean,omitempty"` // 17, опционально
}

// vrGlyphExample — один пример глифа (shape, xyz, reaction).
type vrGlyphExample struct {
	Shape         []float64 `json:"shape"`
	X             []float64 `json:"x"`
	Y             []float64 `json:"y"`
	Z             []float64 `json:"z"`
	Features      []float64 `json:"features"`
	Reaction      []float64 `json:"reaction"`
	ReactionValid bool      `json:"reaction_valid"`
	BandIdx       int       `json:"band_idx"`
	FreqCenter    float64   `json:"freq_center"`
}

// vrGlyphCatalog — каталог глифов, перестраивается при каждом GET /api/glyphs.
type vrGlyphCatalog struct {
	Glyphs []vrGlyphEntry `json:"glyphs"`
}

type trainerEventJSON struct {
	Features      []float64              `json:"features"`
	Shape         []float64              `json:"shape"`
	X             []float64              `json:"x"`
	Y             []float64              `json:"y"`
	Z             []float64              `json:"z"`
	Reaction      []float64              `json:"reaction,omitempty"`
	ReactionValid bool                   `json:"reaction_valid"`
	ReactionTs    int64                  `json:"reaction_ts,omitempty"`
	Meta          map[string]interface{} `json:"meta"`
}

// shapeToProtoSig — обёртка над PeakProfileFromShape для []float64. Совпадает с GUI/ML.
func shapeToProtoSig(pts []float64) []float64 {
	if len(pts) < domainglyph.PeakProfileSize {
		return make([]float64, domainglyph.PeakProfileSize)
	}
	sv := domainglyph.ShapeVector{Points: pts}
	prof := domainglyph.PeakProfileFromShape(sv)
	out := make([]float64, domainglyph.PeakProfileSize)
	for i := 0; i < domainglyph.PeakProfileSize; i++ {
		out[i] = prof[i]
	}
	return out
}

// normalizeXYZ — центрирует и масштабирует P := (P - mean) / max(|P|) для читаемой 3D-спирали.
func normalizeXYZ(x, y, z []float64) ([]float64, []float64, []float64) {
	n := len(x)
	if n == 0 || len(y) < n || len(z) < n {
		return x, y, z
	}
	cx, cy, cz := 0.0, 0.0, 0.0
	for i := 0; i < n; i++ {
		cx += x[i]
		cy += y[i]
		cz += z[i]
	}
	cx /= float64(n)
	cy /= float64(n)
	cz /= float64(n)
	maxAbs := 1e-12
	outX := make([]float64, n)
	outY := make([]float64, n)
	outZ := make([]float64, n)
	for i := 0; i < n; i++ {
		ox := x[i] - cx
		oy := y[i] - cy
		oz := z[i] - cz
		outX[i] = ox
		outY[i] = oy
		outZ[i] = oz
		a := math.Sqrt(ox*ox + oy*oy + oz*oz)
		if a > maxAbs {
			maxAbs = a
		}
	}
	for i := 0; i < n; i++ {
		outX[i] /= maxAbs
		outY[i] /= maxAbs
		outZ[i] /= maxAbs
	}
	return outX, outY, outZ
}

func loadGlyphCatalog() vrGlyphCatalog {
	var cat vrGlyphCatalog
	cat.Glyphs = []vrGlyphEntry{}

	entries, err := os.ReadDir(vrAPIDatasetDir)
	if err != nil {
		return cat
	}

	type key struct {
		actionDir string
		glyphID   int
	}

	byKey := make(map[key]*vrGlyphEntry)
	freqSum := make(map[key]float64)
	bandCounts := make(map[key]map[int]int)

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		actionDir := e.Name()
		actionPath := filepath.Join(vrAPIDatasetDir, actionDir)
		actionID, actionName := parseActionDir(actionDir)
		if actionID < 0 {
			continue
		}

		_ = filepath.Walk(actionPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".json") || filepath.Base(path) == "meta.json" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			var ev trainerEventJSON
			if json.Unmarshal(data, &ev) != nil {
				return nil
			}
			if len(ev.Shape) < 8 {
				return nil
			}
			glyphID := 0
			if m, ok := ev.Meta["glyph_id"]; ok {
				switch v := m.(type) {
				case float64:
					glyphID = int(v)
				case int:
					glyphID = v
				}
			}
			if glyphID == 0 {
				parent := filepath.Base(filepath.Dir(path))
				if n, err := strconv.Atoi(parent); err == nil {
					glyphID = n
				}
			}

			k := key{actionDir: actionDir, glyphID: glyphID}
			if byKey[k] == nil {
				proto := shapeToProtoSig(ev.Shape)
				byKey[k] = &vrGlyphEntry{
					ID:         actionDir + "_" + strconv.Itoa(glyphID),
					ActionID:   actionID,
					ActionName: actionName,
					GlyphID:    glyphID,
					ProtoSig:   proto,
				}
				freqSum[k] = 0
				bandCounts[k] = make(map[int]int)
			}
			ent := byKey[k]
			ent.Count++
			if fc, ok := ev.Meta["freq_center"].(float64); ok {
				freqSum[k] += fc
			}
			if bi, ok := ev.Meta["band_idx"].(float64); ok {
				bandCounts[k][int(bi)]++
			} else if bi, ok := ev.Meta["band_idx"].(int); ok {
				bandCounts[k][bi]++
			}

			// усредняем features и proto по первому примеру (для mean можно усложнить)
			if len(ent.FeaturesMean) == 0 && len(ev.Features) >= 17 {
				ent.FeaturesMean = make([]float64, 17)
				copy(ent.FeaturesMean, ev.Features[:17])
			}

			return nil
		})
	}

	for k, ent := range byKey {
		if freqSum[k] > 0 {
			ent.FreqCenterMean = freqSum[k] / float64(ent.Count)
		}
		mode, maxCnt := 0, 0
		for b, c := range bandCounts[k] {
			if c > maxCnt {
				maxCnt = c
				mode = b
			}
		}
		ent.BandIdxMode = mode
		cat.Glyphs = append(cat.Glyphs, *ent)
	}

	sort.Slice(cat.Glyphs, func(i, j int) bool {
		if cat.Glyphs[i].ActionID != cat.Glyphs[j].ActionID {
			return cat.Glyphs[i].ActionID < cat.Glyphs[j].ActionID
		}
		if cat.Glyphs[i].GlyphID != cat.Glyphs[j].GlyphID {
			return cat.Glyphs[i].GlyphID < cat.Glyphs[j].GlyphID
		}
		return cat.Glyphs[i].Count > cat.Glyphs[j].Count
	})

	return cat
}

func parseActionDir(dir string) (id int, name string) {
	parts := strings.SplitN(dir, "_", 2)
	if len(parts) < 2 {
		return -1, ""
	}
	var err error
	id, err = strconv.Atoi(parts[0])
	if err != nil {
		return -1, ""
	}
	name = strings.TrimSpace(parts[1])
	if name == "" {
		name = "action"
	}
	return id, name
}

func findGlyphExamples(id string, limit int) []vrGlyphExample {
	// id = "1_LEFT_5" → actionDir="1_LEFT", glyphID=5
	lastUnderscore := strings.LastIndex(id, "_")
	if lastUnderscore < 0 {
		return nil
	}
	glyphID, _ := strconv.Atoi(id[lastUnderscore+1:])
	actionDir := id[:lastUnderscore]
	actionPath := filepath.Join(vrAPIDatasetDir, actionDir)
	if _, err := os.Stat(actionPath); err != nil {
		return nil
	}

	var examples []vrGlyphExample
	_ = filepath.Walk(actionPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".json") || filepath.Base(path) == "meta.json" {
			return nil
		}
		parent := filepath.Base(filepath.Dir(path))
		pgid, err := strconv.Atoi(parent)
		if err != nil || pgid != glyphID {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var ev trainerEventJSON
		if json.Unmarshal(data, &ev) != nil {
			return nil
		}
		if len(ev.Shape) < 8 || len(ev.X) < 8 || len(ev.Y) < 8 || len(ev.Z) < 8 {
			return nil
		}

		ex := vrGlyphExample{
			Shape:         ev.Shape,
			X:             ev.X,
			Y:             ev.Y,
			Z:             ev.Z,
			Features:      ev.Features,
			Reaction:      ev.Reaction,
			ReactionValid: ev.ReactionValid,
		}
		if m, ok := ev.Meta["band_idx"]; ok {
			switch v := m.(type) {
			case float64:
				ex.BandIdx = int(v)
			case int:
				ex.BandIdx = v
			}
		}
		if m, ok := ev.Meta["freq_center"]; ok {
			if fc, ok := m.(float64); ok {
				ex.FreqCenter = fc
			}
		}
		// нормализуем xyz для читаемой 3D-спирали
		ex.X, ex.Y, ex.Z = normalizeXYZ(ev.X, ev.Y, ev.Z)

		examples = append(examples, ex)
		if limit > 0 && len(examples) >= limit {
			return filepath.SkipAll
		}
		return nil
	})

	return examples
}

func serveGlyphs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	cat := loadGlyphCatalog()
	_ = json.NewEncoder(w).Encode(cat)
}

func serveGlyphExamples(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 20
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	examples := findGlyphExamples(id, limit)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"examples": examples})
}

// RegisterVRAPI регистрирует /api/glyphs и /api/glyphs/{id}/examples.
func RegisterVRAPI(mux *http.ServeMux) {
	mux.HandleFunc("/api/glyphs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/glyphs" {
			http.NotFound(w, r)
			return
		}
		serveGlyphs(w, r)
	})
	mux.HandleFunc("/api/glyphs/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/glyphs/")
		if path == "" {
			http.Redirect(w, r, "/api/glyphs", http.StatusFound)
			return
		}
		if strings.HasSuffix(path, "/examples") {
			id := strings.TrimSuffix(path, "/examples")
			serveGlyphExamples(w, r, id)
			return
		}
		http.NotFound(w, r)
	})
}
