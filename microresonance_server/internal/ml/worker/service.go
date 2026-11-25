// ml_worker.go — ML-воркер: читает из очередей (FrameSummary, EventGlyph, EventReaction),
// пересылает в Python run_ml_queue_model.py, обновляет предсказание.

package worker

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	mlclassifier "server/internal/ml/classifier"
	mlqueue "server/internal/ml/queue"
)

// mlMetaFormat читает format из data/saved_model/meta.json.
func mlMetaFormat() string {
	data, err := os.ReadFile(filepath.Join(mlWorkerModelDir, "meta.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Format string `json:"format"`
	}
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	return m.Format
}

// MLPrediction — последний результат модели (для вкладки MEANING).
type MLPrediction struct {
	mu         sync.Mutex
	ActionName string
	Confidence float64
}

func (p *MLPrediction) Get() (name string, confidence float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ActionName, p.Confidence
}

func (p *MLPrediction) set(name string, confidence float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ActionName = name
	p.Confidence = confidence
}

// MLWorker — читает из MLQueue, обновляет MLPrediction.
// Для glyph_classifier использует GlyphClassifier (нативно или через Python).
// Для ml_queue — subprocess run_ml_queue_model.py.
type MLWorker struct {
	mu           sync.Mutex
	cmd          *exec.Cmd
	stdin        *bufio.Writer
	lastPred     *MLPrediction
	queue        *mlqueue.MLQueueImpl
	stopCh       chan struct{}
	running      bool
	onPrediction func(actionName string)
	// glyph_classifier: нативный инференс (без Python)
	glyphClassifier *mlclassifier.GlyphClassifier
	useGlyphFormat  bool // format == glyph_classifier
}

const mlWorkerModelDir = "data/saved_model"
const mlWorkerScriptQueue = "scripts/infer/run_ml_queue_model.py"
const mlWorkerScriptGlyph = "scripts/infer/run_glyph_model.py"

// MLModelExists проверяет наличие модели (data/saved_model/ + meta.json).
func MLModelExists() bool {
	dir := mlWorkerModelDir
	if _, err := os.Stat(filepath.Join(dir, "saved_model.pb")); err != nil {
		if _, err := os.Stat(filepath.Join(dir, "keras_metadata.pb")); err != nil {
			if _, err := os.Stat(filepath.Join(dir, "model.keras")); err != nil {
				return false
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		return false
	}
	return true
}

// NewMLWorker создаёт воркер; не запускает. Start() при переходе на MEANING.
func NewMLWorker(lastPred *MLPrediction, queue *mlqueue.MLQueueImpl) *MLWorker {
	return &MLWorker{
		lastPred: lastPred,
		queue:    queue,
		stopCh:   make(chan struct{}),
	}
}

// SetOnPrediction — колбэк при каждом предсказании.
func (w *MLWorker) SetOnPrediction(f func(actionName string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onPrediction = f
}

// Start запускает воркер. Для glyph_classifier — GlyphClassifier; для ml_queue — Python subprocess.
func (w *MLWorker) Start() bool {
	w.mu.Lock()
	if w.running || w.queue == nil {
		w.mu.Unlock()
		return false
	}
	format := mlMetaFormat()
	w.useGlyphFormat = (format == "glyph_classifier")

	if w.useGlyphFormat {
		cl, err := mlclassifier.NewGlyphClassifier(mlWorkerModelDir)
		if err != nil {
			w.mu.Unlock()
			return false
		}
		w.glyphClassifier = cl
		w.running = true
		w.mu.Unlock()
		go w.runLoopGlyph()
		return true
	}

	// ml_queue: Python subprocess
	scriptPath := mlWorkerScriptQueue
	if _, err := os.Stat(scriptPath); err != nil {
		w.mu.Unlock()
		return false
	}
	absDir, _ := filepath.Abs(mlWorkerModelDir)
	if _, err := os.Stat(filepath.Join(absDir, "meta.json")); err != nil {
		absDir = mlWorkerModelDir
	}
	w.cmd = exec.Command("python3", scriptPath, absDir)
	w.cmd.Stderr = os.Stderr
	stdinPipe, err := w.cmd.StdinPipe()
	if err != nil {
		w.mu.Unlock()
		return false
	}
	stdoutPipe, err := w.cmd.StdoutPipe()
	if err != nil {
		stdinPipe.Close()
		w.mu.Unlock()
		return false
	}
	if err := w.cmd.Start(); err != nil {
		stdinPipe.Close()
		stdoutPipe.Close()
		w.mu.Unlock()
		return false
	}
	w.stdin = bufio.NewWriter(stdinPipe)
	w.running = true
	w.mu.Unlock()

	go w.run(stdoutPipe)
	go w.runLoop()
	return true
}

func (w *MLWorker) run(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		var out struct {
			ActionID   int     `json:"action_id"`
			Confidence float64 `json:"confidence"`
			ActionName string  `json:"action_name"`
		}
		if err := json.Unmarshal([]byte(line), &out); err != nil {
			continue
		}
		w.lastPred.set(out.ActionName, out.Confidence)
		w.mu.Lock()
		cb := w.onPrediction
		w.mu.Unlock()
		if cb != nil && out.ActionName != "" {
			cb(out.ActionName)
		}
	}
}

// runLoopGlyph — для glyph_classifier: читает EventGlyph, вызывает Predict нативно.
func (w *MLWorker) runLoopGlyph() {
	glyphCh := w.queue.GlyphChan()
	for {
		select {
		case <-w.stopCh:
			return
		case e, ok := <-glyphCh:
			if !ok {
				return
			}
			if len(e.Shape) < 10 || len(e.Features) < 10 {
				continue
			}
			pred, err := w.glyphClassifier.Predict(&e)
			if err != nil {
				continue
			}
			w.lastPred.set(pred.ActionName, pred.Confidence)
			w.mu.Lock()
			cb := w.onPrediction
			w.mu.Unlock()
			if cb != nil && pred.ActionName != "" {
				cb(pred.ActionName)
			}
		}
	}
}

// runLoop читает из очередей и пишет JSON в stdin Python (ml_queue format).
func (w *MLWorker) runLoop() {
	frameCh := w.queue.FrameChan()
	glyphCh := w.queue.GlyphChan()
	reactionCh := w.queue.ReactionChan()
	for {
		select {
		case <-w.stopCh:
			return
		case f, ok := <-frameCh:
			if !ok {
				return
			}
			w.sendMessage(&f)
		case e, ok := <-glyphCh:
			if !ok {
				return
			}
			w.sendMessage(&e)
		case r, ok := <-reactionCh:
			if !ok {
				return
			}
			w.sendMessage(&r)
		}
	}
}

func (w *MLWorker) sendMessage(msg interface{}) {
	w.mu.Lock()
	wr := w.stdin
	running := w.running
	useGlyph := w.useGlyphFormat
	w.mu.Unlock()
	if !running || wr == nil {
		return
	}
	if useGlyph {
		// glyph_classifier: только EventGlyph с полными данными (shape, features)
		e, ok := msg.(*mlqueue.EventGlyph)
		if !ok || len(e.Shape) < 10 || len(e.Features) < 10 {
			return
		}
	}
	var b []byte
	var err error
	switch m := msg.(type) {
	case *mlqueue.FrameSummary:
		if useGlyph {
			return // glyph_classifier не использует FrameSummary
		}
		b, err = m.ToJSON()
	case *mlqueue.EventGlyph:
		b, err = m.ToJSON()
	case *mlqueue.EventReaction:
		if useGlyph {
			return // glyph_classifier не использует EventReaction (reaction=zeros)
		}
		b, err = m.ToJSON()
	default:
		return
	}
	if err != nil {
		return
	}
	wr.Write(b)
	wr.WriteByte('\n')
	wr.Flush()
}

// Stop останавливает процесс.
func (w *MLWorker) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	close(w.stopCh)
	if w.glyphClassifier != nil {
		w.glyphClassifier.Close()
		w.glyphClassifier = nil
	}
	if w.cmd != nil && w.cmd.Process != nil {
		w.cmd.Process.Kill()
	}
	w.mu.Unlock()
}
