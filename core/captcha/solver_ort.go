package captcha

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

var (
	ortOnce sync.Once
	ortErr  error
)

// InitORT 装载 onnxruntime 动态库并初始化环境(进程级一次)。
// libPath 为空时依次取 LOVEACE_ORT_LIB 环境变量、third_party 内的平台默认路径。
func InitORT(libPath string) error {
	ortOnce.Do(func() {
		if libPath == "" {
			libPath = os.Getenv("LOVEACE_ORT_LIB")
		}
		if libPath == "" {
			libPath = bundledLibPath()
		}
		if libPath == "" {
			ortErr = fmt.Errorf("onnxruntime 动态库未找到(设 LOVEACE_ORT_LIB)")
			return
		}
		ort.SetSharedLibraryPath(libPath)
		ortErr = ort.InitializeEnvironment()
	})
	return ortErr
}

// bundledLibPath 定位仓库内 third_party 的平台库(开发/测试用;生产由
// loveaced 显式传 env 路径)。
func bundledLibPath() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "third_party", "onnxruntime")
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return filepath.Join(root, "osx-arm64", "lib", "libonnxruntime.dylib")
		}
	case "linux":
		if runtime.GOARCH == "amd64" {
			return filepath.Join(root, "linux-x64", "lib", "libonnxruntime.so")
		}
	}
	return ""
}

// OrtSolver 是 Solver 的 onnxruntime 实现:单会话、受限解码、P 模式 GIF 预处理。
type OrtSolver struct {
	mu   sync.Mutex
	sess *ort.DynamicAdvancedSession
}

// New 构建求解器。modelPath 指 beta 的 common.onnx(见 core/captcha/model/)。
func New(modelPath, libPath string) (*OrtSolver, error) {
	if err := InitORT(libPath); err != nil {
		return nil, fmt.Errorf("ort init: %w", err)
	}
	ins, outs, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		return nil, fmt.Errorf("ort GetInputOutputInfo: %w", err)
	}
	if len(ins) != 1 {
		return nil, fmt.Errorf("期望 1 个模型输入,实际 %d", len(ins))
	}
	inNames := make([]string, 0, len(ins))
	for _, io := range ins {
		inNames = append(inNames, io.Name)
	}
	outNames := make([]string, 0, len(outs))
	for _, io := range outs {
		outNames = append(outNames, io.Name)
	}
	sess, err := ort.NewDynamicAdvancedSession(modelPath, inNames, outNames, nil)
	if err != nil {
		return nil, fmt.Errorf("ort session: %w", err)
	}
	return &OrtSolver{sess: sess}, nil
}

// Solve 实现 Solver。
func (s *OrtSolver) Solve(imageData []byte) (string, bool, error) {
	data, w, err := preprocessGIF(imageData)
	if err != nil {
		return "", false, err
	}
	in, err := ort.NewTensor(ort.NewShape(1, 1, targetH, int64(w)), data)
	if err != nil {
		return "", false, fmt.Errorf("ort input tensor: %w", err)
	}
	defer in.Destroy()

	s.mu.Lock()
	outs := []ort.Value{nil} // nil 输出由 Run 自动分配(动态 T)
	err = s.sess.Run([]ort.Value{in}, outs)
	s.mu.Unlock()
	if err != nil {
		return "", false, fmt.Errorf("ort run: %w", err)
	}
	outT, ok := outs[0].(*ort.Tensor[float32])
	if !ok || outT == nil {
		return "", false, fmt.Errorf("ort 输出类型异常 %T", outs[0])
	}
	shape := outT.GetShape()
	k := int(shape[len(shape)-1])
	total := 1
	for _, d := range shape {
		total *= int(d)
	}
	code, okCode := decode(outT.GetData(), total/k, k)
	_ = outT.Destroy()
	return code, okCode, nil
}

// Close 释放会话(环境保持初始化,供后续重建)。
func (s *OrtSolver) Close() error {
	return s.sess.Destroy()
}
