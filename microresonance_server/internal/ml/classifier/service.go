// glyph_classifier.go — нативный инференс glyph_classifier.
// Использует model.onnx (onnxruntime) при наличии, иначе run_glyph_model.py.
// Предобработка совпадает с train_glyph_model / run_glyph_model.

package classifier

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	mlqueue "server/internal/ml/queue"
)

const (
	shapeDim    = 64
	axesDim     = 32
	featuresDim = 17
	reactionDim = 16
)

// GlyphClassifier загружает модель и выполняет инференс.
type GlyphClassifier struct {
	mu         sync.Mutex
	modelDir   string
	meta       glyphMeta
	usePython  bool
	pythonCmd  *exec.Cmd
	pythonIn   *bufio.Writer
	pythonOut  *bufio.Reader
	ortSession *ortRunner // nil = use Python
}

type glyphMeta struct {
	Format       string            `json:"format"`
	NumClasses   int               `json:"num_classes"`
	IdxToID      []int             `json:"idx_to_id"`
	IDToName     map[string]string `json:"id_to_name"`
	FeaturesMean []float64         `json:"features_mean"`
	FeaturesStd  []float64         `json:"features_std"`
	AxesDim      int               `json:"axes_dim"`
}

// GlyphPrediction — результат классификации.
type GlyphPrediction struct {
	ActionID   int
	ActionName string
	Confidence float64
}

// NewGlyphClassifier загружает модель из modelDir. Использует model.onnx если есть и ORT доступен, иначе Python.
func NewGlyphClassifier(modelDir string) (*GlyphClassifier, error) {
	metaPath := filepath.Join(modelDir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("meta.json: %w", err)
	}
	var meta glyphMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("meta.json parse: %w", err)
	}
	if meta.Format != "glyph_classifier" {
		return nil, fmt.Errorf("format %q != glyph_classifier", meta.Format)
	}
	if len(meta.FeaturesMean) < featuresDim || len(meta.FeaturesStd) < featuresDim {
		return nil, fmt.Errorf("meta features_mean/std too short")
	}
	axes := meta.AxesDim
	if axes <= 0 {
		axes = axesDim
	}
	meta.AxesDim = axes

	c := &GlyphClassifier{modelDir: modelDir, meta: meta}

	onnxPath := filepath.Join(modelDir, "model.onnx")
	if _, err := os.Stat(onnxPath); err == nil {
		if runner, err := c.initONNX(onnxPath, axes); err == nil {
			c.ortSession = runner
			return c, nil
		}
	}
	// Fallback на Python
	c.usePython = true
	if err := c.startPython(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *GlyphClassifier) initONNX(onnxPath string, axes int) (*ortRunner, error) {
	libPath := getORTLibPath()
	if libPath == "" {
		return nil, fmt.Errorf("ONNXRUNTIME_SHARED_LIBRARY_PATH not set, run from module dir or set env")
	}
	if _, err := os.Stat(libPath); err != nil {
		return nil, fmt.Errorf("onnxruntime lib not found: %s", libPath)
	}
	ctx, err := newORTContext(libPath)
	if err != nil {
		return nil, err
	}
	return ctx.NewSession(onnxPath, axes)
}

func (c *GlyphClassifier) startPython() error {
	scriptPath := "scripts/infer/run_glyph_model.py"
	if _, err := os.Stat(scriptPath); err != nil {
		return err
	}
	absDir, _ := filepath.Abs(c.modelDir)
	c.pythonCmd = exec.Command("python3", scriptPath, absDir)
	c.pythonCmd.Stderr = os.Stderr
	stdin, err := c.pythonCmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := c.pythonCmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return err
	}
	if err := c.pythonCmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return err
	}
	c.pythonIn = bufio.NewWriter(stdin)
	c.pythonOut = bufio.NewReader(stdout)
	return nil
}

// Predict классифицирует EventGlyph.
func (c *GlyphClassifier) Predict(e *mlqueue.EventGlyph) (GlyphPrediction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ortSession != nil {
		return c.predictNative(e)
	}
	return c.predictPython(e)
}

func (c *GlyphClassifier) predictNative(e *mlqueue.EventGlyph) (GlyphPrediction, error) {
	if c.ortSession == nil {
		return GlyphPrediction{}, fmt.Errorf("ORT not initialized")
	}
	ort := c.ortSession
	shape := padTrunc(e.Shape, shapeDim)
	x := padTruncSlice(e.Gx, c.meta.AxesDim)
	y := padTruncSlice(e.Gy, c.meta.AxesDim)
	z := padTruncSlice(e.Gz, c.meta.AxesDim)
	feat := padTruncSlice(e.Features, featuresDim)
	react := make([]float64, reactionDim)

	// Features 14-16: AgeSincePrevEvent, EventIndexInRun, RunLength.
	// Подставляем только если пришли нули (production: Go их не задаёт).
	// Иначе используем реальные данные из датасета/JSON.
	if len(feat) >= 17 && feat[14] == 0 && feat[15] == 0 && feat[16] == 0 {
		feat[14] = math.Log1p(e.Dt * 1000)
		feat[15] = 1
		feat[16] = 1
	}

	// Z-score normalize
	for i := 0; i < featuresDim && i < len(feat); i++ {
		std := c.meta.FeaturesStd[i]
		if std < 1e-8 {
			std = 1e-8
		}
		feat[i] = (feat[i] - c.meta.FeaturesMean[i]) / std
	}

	if err := ort.Run(shape, x, y, z, feat, react); err != nil {
		return GlyphPrediction{}, err
	}
	probs := ort.Output()
	if len(probs) < 3 {
		return GlyphPrediction{}, fmt.Errorf("output dim %d < 3", len(probs))
	}
	idx := 0
	maxP := probs[0]
	for i := 1; i < len(probs); i++ {
		if probs[i] > maxP {
			maxP = probs[i]
			idx = i
		}
	}
	actionID := idx + 1
	if idx < len(c.meta.IdxToID) {
		actionID = c.meta.IdxToID[idx]
	}
	name := fmt.Sprintf("action_%d", actionID)
	if n, ok := c.meta.IDToName[fmt.Sprintf("%d", actionID)]; ok {
		name = n
	}
	return GlyphPrediction{
		ActionID:   actionID,
		ActionName: name,
		Confidence: float64(maxP),
	}, nil
}

func (c *GlyphClassifier) predictPython(e *mlqueue.EventGlyph) (GlyphPrediction, error) {
	msg, err := e.ToJSON()
	if err != nil {
		return GlyphPrediction{}, err
	}
	c.pythonIn.Write(msg)
	c.pythonIn.WriteByte('\n')
	if err := c.pythonIn.Flush(); err != nil {
		return GlyphPrediction{}, err
	}
	line, err := c.pythonOut.ReadString('\n')
	if err != nil {
		return GlyphPrediction{}, err
	}
	var out struct {
		ActionID   int     `json:"action_id"`
		ActionName string  `json:"action_name"`
		Confidence float64 `json:"confidence"`
		Error      string  `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		return GlyphPrediction{}, fmt.Errorf("parse output: %w", err)
	}
	if out.Error != "" {
		return GlyphPrediction{}, fmt.Errorf("python: %s", out.Error)
	}
	return GlyphPrediction{
		ActionID:   out.ActionID,
		ActionName: out.ActionName,
		Confidence: out.Confidence,
	}, nil
}

// PredictFromArrays — инференс из сырых массивов (для тестов).
func (c *GlyphClassifier) PredictFromArrays(shape, x, y, z, features []float64) (GlyphPrediction, error) {
	e := &mlqueue.EventGlyph{
		Shape:    padTrunc(shape, shapeDim),
		Gx:       padTruncSlice(x, axesDim),
		Gy:       padTruncSlice(y, axesDim),
		Gz:       padTruncSlice(z, axesDim),
		Features: padTruncSlice(features, featuresDim),
	}
	return c.Predict(e)
}

func padTrunc(arr []float64, size int) []float64 {
	out := make([]float64, size)
	n := len(arr)
	if n > size {
		n = size
	}
	if n > 0 {
		copy(out, arr[:n])
	}
	return out
}

func padTruncSlice(arr []float64, size int) []float64 {
	if len(arr) >= size {
		return arr[:size]
	}
	out := make([]float64, size)
	copy(out, arr)
	return out
}

// Close освобождает ресурсы.
func (c *GlyphClassifier) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ortSession != nil {
		c.ortSession.Destroy()
	}
	if c.pythonCmd != nil && c.pythonCmd.Process != nil {
		c.pythonCmd.Process.Kill()
	}
	return nil
}
