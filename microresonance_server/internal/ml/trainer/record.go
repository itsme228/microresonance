// Запись потока глифов в папку: X/Y/Z по полосам B0..B7, порядок по времени.
// Формат для тренировки нейросети — все 3 оси (не квадраты).

package trainer

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	domainsignal "server/internal/domain/signal"
	platformsignal "server/internal/platform/signal"
	"strconv"
	"sync"
	"time"
)

const trainerRecordsDir = "data/trainer_records"

// GlyphStreamRecorder записывает сегменты (X,Y,Z по полосам) в папку.
type GlyphStreamRecorder struct {
	mu         sync.Mutex
	sessionDir string
	segmentIdx int
	startedAt  time.Time
}

// StartSession создаёт папку сессии и записывает meta.json.
func (r *GlyphStreamRecorder) StartSession(actions []struct {
	ID   int
	Name string
}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := time.Now()
	r.sessionDir = filepath.Join(trainerRecordsDir, fmt.Sprintf("session_%s", t.Format("20060102_150405")))
	if err := os.MkdirAll(r.sessionDir, 0755); err != nil {
		return err
	}
	r.segmentIdx = 0
	r.startedAt = t
	// meta.json
	metaPath := filepath.Join(r.sessionDir, "meta.json")
	f, err := os.Create(metaPath)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(f, `{"startedAt":"%s","sampleRate":0,"actions":[`, t.Format(time.RFC3339))
	for i, a := range actions {
		if i > 0 {
			_, _ = f.WriteString(",")
		}
		_, _ = fmt.Fprintf(f, `{"id":%d,"name":"%s"}`, a.ID, a.Name)
	}
	_, _ = fmt.Fprintf(f, "]}\n")
	f.Close()
	// Папки по полосам
	for b := 0; b < TrainerBandsN; b++ {
		bandDir := filepath.Join(r.sessionDir, fmt.Sprintf("B%d", b))
		_ = os.MkdirAll(bandDir, 0755)
	}
	return nil
}

// RecordSegment записывает один сегмент (Start–End): для каждой полосы B0..B7 — X,Y,Z.
// x,y,z — срезы буфера за окно [startMs..endMs]; fs — частота.
func (r *GlyphStreamRecorder) RecordSegment(x, y, z []float64, fs float64, startMs, endMs int64, actionID int, actionName string) error {
	r.mu.Lock()
	if r.sessionDir == "" {
		r.mu.Unlock()
		return nil
	}
	r.segmentIdx++
	idx := r.segmentIdx
	sessionDir := r.sessionDir
	r.mu.Unlock()

	if len(x) < 16 || len(y) < 16 || len(z) < 16 || fs <= 0 {
		return nil
	}

	n := len(x)
	if len(y) < n {
		n = len(y)
	}
	if len(z) < n {
		n = len(z)
	}

	for band := 0; band < TrainerBandsN && band < len(domainsignal.BandsFilterbank); band++ {
		b := &domainsignal.BandsFilterbank[band]
		f := platformsignal.NewBandpassFilter(b.LoHz, b.HiHz, fs) // свежий фильтр на сегмент
		xB, yB, zB, _ := f.Filter(x[:n], y[:n], z[:n])
		if len(xB) < 8 {
			continue
		}

		fname := filepath.Join(sessionDir, fmt.Sprintf("B%d", band), fmt.Sprintf("seg_%06d.csv", idx))
		fh, err := os.Create(fname)
		if err != nil {
			continue
		}
		w := csv.NewWriter(fh)
		// Комментарий: sample_rate
		_ = w.Write([]string{"# fs", strconv.FormatFloat(fs, 'g', -1, 64)})
		// Заголовок: ts_start, ts_end, duration_ms, action_id, action_name, n_samples
		_ = w.Write([]string{
			strconv.FormatInt(startMs, 10),
			strconv.FormatInt(endMs, 10),
			strconv.FormatInt(endMs-startMs, 10),
			strconv.Itoa(actionID),
			actionName,
			strconv.Itoa(len(xB)),
		})
		// Строки: sample_idx, x, y, z (все 3 оси)
		for i := 0; i < len(xB); i++ {
			_ = w.Write([]string{
				strconv.Itoa(i),
				strconv.FormatFloat(xB[i], 'g', -1, 64),
				strconv.FormatFloat(yB[i], 'g', -1, 64),
				strconv.FormatFloat(zB[i], 'g', -1, 64),
			})
		}
		w.Flush()
		fh.Close()
	}
	return nil
}
