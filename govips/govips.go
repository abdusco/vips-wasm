package govips

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	tablexp "github.com/tetratelabs/wazero/experimental/table"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

//go:embed thumbnail.wasm
var wasmBin []byte

var (
	ErrInvalidOptions = errors.New("invalid resize options")
)

type Format string

const (
	FormatJPEG Format = ".jpg"
	FormatPNG  Format = ".png"
	FormatWebP Format = ".webp"
	FormatAVIF Format = ".avif"
)

type ResizeOptions struct {
	Width        int
	Height       int
	Format       Format
	Quality      int
	KeepMetadata bool
}

type engine struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	mu       sync.Mutex
}

var (
	defaultEngine   *engine
	defaultEngineMu sync.Mutex
)

func (f Format) Validate() error {
	switch f {
	case FormatJPEG, FormatPNG, FormatWebP, FormatAVIF:
		return nil
	default:
		return errors.New("format must be one of: .jpg, .png, .webp, .avif")
	}
}

func (o ResizeOptions) Validate() error {
	if o.Width <= 0 {
		return errors.New("width must be > 0")
	}
	if o.Height < 0 {
		return errors.New("height must be >= 0")
	}
	if o.Quality < 0 || o.Quality > 100 {
		return errors.New("quality must be between 0 and 100")
	}
	if err := o.Format.Validate(); err != nil {
		return fmt.Errorf("format: %w", err)
	}
	return nil
}

func Resize(ctx context.Context, source []byte, opts ResizeOptions) ([]byte, error) {
	if len(source) == 0 {
		return nil, errors.New("empty source image")
	}
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidOptions, err)
	}

	suffix := string(opts.Format)

	e, err := getDefaultEngine()
	if err != nil {
		return nil, err
	}

	var outBuf bytes.Buffer
	var errBuf bytes.Buffer

	stripStr := "1"
	if opts.KeepMetadata {
		stripStr = "0"
	}

	cfg := wazero.NewModuleConfig().
		WithStdin(bytes.NewReader(source)).
		WithStdout(&outBuf).
		WithStderr(&errBuf).
		WithArgs(
			"thumbnail",
			strconv.Itoa(opts.Width),
			suffix,
			strconv.Itoa(opts.Height),
			strconv.Itoa(opts.Quality),
			stripStr,
		)

	e.mu.Lock()
	_, err = e.rt.InstantiateModule(ctx, e.compiled, cfg)
	e.mu.Unlock()
	if err != nil {
		var exitErr *sys.ExitError
		if errors.As(err, &exitErr) {
			msg := strings.TrimSpace(errBuf.String())
			if msg == "" {
				msg = exitErr.Error()
			}
			return nil, fmt.Errorf("wasm exited with %d: %s", exitErr.ExitCode(), msg)
		}
		return nil, fmt.Errorf("instantiate wasm: %w", err)
	}

	if outBuf.Len() == 0 {
		msg := strings.TrimSpace(errBuf.String())
		if msg != "" {
			return nil, errors.New(msg)
		}
		return nil, errors.New("wasm produced no output")
	}

	return bytes.Clone(outBuf.Bytes()), nil
}

func getDefaultEngine() (*engine, error) {
	defaultEngineMu.Lock()
	defer defaultEngineMu.Unlock()
	if defaultEngine != nil {
		return defaultEngine, nil
	}

	e, err := newEngine(context.Background())
	if err != nil {
		return nil, err
	}
	defaultEngine = e
	return defaultEngine, nil
}

func newEngine(ctx context.Context) (*engine, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	cacheDir = filepath.Join(cacheDir, "vips-wasm")

	cache, err := wazero.NewCompilationCacheWithDir(cacheDir)
	if err != nil {
		cache = wazero.NewCompilationCache()
	}

	rt := wazero.NewRuntimeWithConfig(ctx,
		wazero.NewRuntimeConfig().
			WithCompilationCache(cache).
			WithCoreFeatures(api.CoreFeaturesV2|experimental.CoreFeaturesThreads),
	)

	compiled, err := rt.CompileModule(ctx, wasmBin)
	if err != nil {
		return nil, fmt.Errorf("compile wasm: %w", err)
	}

	if err := buildEnvModule(ctx, rt, compiled); err != nil {
		return nil, err
	}

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return nil, fmt.Errorf("instantiate wasi: %w", err)
	}
	return &engine{rt: rt, compiled: compiled}, nil
}

const longjmpSentinel = "emscripten_longjmp_sentinel"

func isLongjmpErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), longjmpSentinel)
}

func callTableRaw(ctx context.Context, m api.Module, fnIdx uint32, params, results []api.ValueType, args []uint64) ([]uint64, error) {
	fn := tablexp.LookupFunction(m, 0, fnIdx, params, results)
	return fn.Call(ctx, args...)
}

func sjljSave(ctx context.Context, m api.Module) uint32 {
	r, err := m.ExportedFunction("emscripten_stack_get_current").Call(ctx)
	if err != nil || len(r) == 0 {
		return 0
	}
	return uint32(r[0])
}

func sjljRestore(ctx context.Context, m api.Module, sp uint32) {
	m.ExportedFunction("_emscripten_stack_restore").Call(ctx, uint64(sp))
	m.ExportedFunction("setThrew").Call(ctx, 1, 0)
}

func makeInvoke(calledParams, calledResults []api.ValueType) api.GoModuleFunc {
	nArgs := len(calledParams)
	hasResult := len(calledResults) > 0
	return api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		sp := sjljSave(ctx, m)
		r, err := callTableRaw(ctx, m, uint32(stack[0]), calledParams, calledResults, stack[1:1+nArgs])
		if err != nil {
			if isLongjmpErr(err) {
				sjljRestore(ctx, m, sp)
				if hasResult {
					stack[0] = 0
				}
				return
			}
			panic(fmt.Sprintf("invoke table[%d]: %v", uint32(stack[0]), err))
		}
		if hasResult {
			stack[0] = r[0]
		}
	})
}

var invokeTypeMap = map[byte]api.ValueType{
	'i': api.ValueTypeI32,
	'j': api.ValueTypeI64,
	'f': api.ValueTypeF32,
	'd': api.ValueTypeF64,
}

func parseInvokeSignature(name string) ([]api.ValueType, []api.ValueType, bool) {
	sig := strings.TrimPrefix(name, "invoke_")
	if len(sig) == 0 {
		return nil, nil, false
	}

	var calledResults []api.ValueType
	if sig[0] != 'v' {
		t, ok := invokeTypeMap[sig[0]]
		if !ok {
			return nil, nil, false
		}
		calledResults = []api.ValueType{t}
	}

	calledParams := make([]api.ValueType, 0, len(sig)-1)
	for i := 1; i < len(sig); i++ {
		t, ok := invokeTypeMap[sig[i]]
		if !ok {
			return nil, nil, false
		}
		calledParams = append(calledParams, t)
	}
	return calledParams, calledResults, true
}

func registerInvoke(b wazero.HostModuleBuilder, name string, importParams, importResults []api.ValueType) error {
	calledParams, calledResults, ok := parseInvokeSignature(name)
	if !ok {
		return fmt.Errorf("cannot parse invoke signature: %s", name)
	}
	b.NewFunctionBuilder().
		WithGoModuleFunction(makeInvoke(calledParams, calledResults), importParams, importResults).
		Export(name)
	return nil
}

func buildEnvModule(ctx context.Context, rt wazero.Runtime, compiled wazero.CompiledModule) error {
	b := rt.NewHostModuleBuilder("env")
	b.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, _ []uint64) {
			panic(longjmpSentinel)
		}), nil, nil).
		Export("_emscripten_throw_longjmp")

	for _, fn := range compiled.ImportedFunctions() {
		mod, name, _ := fn.Import()
		if mod != "env" || !strings.HasPrefix(name, "invoke_") {
			continue
		}
		if err := registerInvoke(b, name, fn.ParamTypes(), fn.ResultTypes()); err != nil {
			return err
		}
	}

	if _, err := b.Instantiate(ctx); err != nil {
		return fmt.Errorf("build env module: %w", err)
	}
	return nil
}
