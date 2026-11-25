package classifier

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	ort "github.com/yalue/onnxruntime_go"
)

type ortInterface interface {
	NewSession(onnxPath string, axes int) (*ortRunner, error)
}

type ortRunner struct {
	session  *ort.AdvancedSession
	inShape  *ort.Tensor[float32]
	inX      *ort.Tensor[float32]
	inY      *ort.Tensor[float32]
	inZ      *ort.Tensor[float32]
	inFeat   *ort.Tensor[float32]
	inReact  *ort.Tensor[float32]
	outProbs *ort.Tensor[float32]
}

func (r *ortRunner) Run(shape, x, y, z, feat, react []float64) error {
	copyFloat64To32(r.inShape.GetData(), shape)
	copyFloat64To32(r.inX.GetData(), x)
	copyFloat64To32(r.inY.GetData(), y)
	copyFloat64To32(r.inZ.GetData(), z)
	copyFloat64To32(r.inFeat.GetData(), feat)
	return r.session.Run()
}

func (r *ortRunner) Output() []float32 {
	return r.outProbs.GetData()
}

func (r *ortRunner) Destroy() {
	if r.session != nil {
		r.session.Destroy()
	}
}

func copyFloat64To32(dst []float32, src []float64) {
	for i := 0; i < len(dst) && i < len(src); i++ {
		dst[i] = float32(src[i])
	}
}

type ortContext struct{}

func newORTContext(libPath string) (ortInterface, error) {
	ort.SetSharedLibraryPath(libPath)
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, err
	}
	return &ortContext{}, nil
}

func (ctx *ortContext) NewSession(onnxPath string, axes int) (*ortRunner, error) {
	shapeShape := ort.NewShape(1, 64)
	xShape := ort.NewShape(1, int64(axes))
	featShape := ort.NewShape(1, 17)
	reactShape := ort.NewShape(1, 16)
	outShape := ort.NewShape(1, 3)

	inShape, _ := ort.NewEmptyTensor[float32](shapeShape)
	inX, _ := ort.NewEmptyTensor[float32](xShape)
	inY, _ := ort.NewEmptyTensor[float32](xShape)
	inZ, _ := ort.NewEmptyTensor[float32](xShape)
	inFeat, _ := ort.NewEmptyTensor[float32](featShape)
	inReact, _ := ort.NewEmptyTensor[float32](reactShape)
	outProbs, _ := ort.NewEmptyTensor[float32](outShape)

	// tf2onnx exports Keras inputs as inputs, inputs_1, ...
	inputNames := []string{"inputs", "inputs_1", "inputs_2", "inputs_3", "inputs_4", "inputs_5"}
	outputNames := []string{"output_0"}
	inputs := []ort.Value{inShape, inX, inY, inZ, inFeat, inReact}
	outputs := []ort.Value{outProbs}

	session, err := ort.NewAdvancedSession(onnxPath, inputNames, outputNames, inputs, outputs, nil)
	if err != nil {
		return nil, fmt.Errorf("NewAdvancedSession: %w", err)
	}

	return &ortRunner{
		session:  session,
		inShape:  inShape,
		inX:      inX,
		inY:      inY,
		inZ:      inZ,
		inFeat:   inFeat,
		inReact:  inReact,
		outProbs: outProbs,
	}, nil
}

func getORTLibPath() string {
	if p := os.Getenv("ONNXRUNTIME_SHARED_LIBRARY_PATH"); p != "" {
		return p
	}
	modDir, err := getORTModuleDir()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return filepath.Join(modDir, "test_data", "onnxruntime_arm64.dylib")
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "amd64" {
		return filepath.Join(modDir, "test_data", "onnxruntime_amd64.dylib")
	}
	return ""
}

func getORTModuleDir() (string, error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/yalue/onnxruntime_go")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
