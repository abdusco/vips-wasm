// vips-thumb — thumbnail images using libvips compiled to standalone WASM.
//
// Usage:
//   vips-thumb <input> <output> <width> [height]
//
// The embedded wasm/thumbnail.wasm is built by wasm/build.sh using Emscripten
// -sSTANDALONE_WASM=1.  It is run by wazero (pure Go, no CGo).
//
// I/O design: Emscripten standalone WASM stubs out path_open, so the WASM
// binary cannot open files by path.  Instead, the Go host reads the input
// file, pipes it to WASM stdin, and captures WASM stdout as the output image.
package main

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

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	tablexp "github.com/tetratelabs/wazero/experimental/table"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

//go:embed wasm/thumbnail.wasm
var wasmBin []byte

// ── Emscripten setjmp/longjmp support ────────────────────────────────────────
//
// Emscripten compiles setjmp/longjmp to the "invoke_*" + "__THREW__" protocol:
//
//   1. setjmp(env) is lowered to: sp = saveStack(); result = invoke_XYZ(fn, ...);
//      if (__THREW__) { setThrew(0,0); stackRestore(sp); handle longjmp }
//
//   2. longjmp(env, val) calls _emscripten_throw_longjmp(), which in JS mode
//      throws a JS exception caught by the surrounding invoke_* try/catch.
//
//   3. invoke_XYZ(fn, a, b, ...): saves stack, calls table[fn](a,b,...) in a
//      try/catch; on longjmp exception: restores stack, calls setThrew(1,0),
//      returns 0.  The WASM caller then checks __THREW__ to detect the throw.
//
// In wazero we implement this by:
//   - panicking a known sentinel in _emscripten_throw_longjmp
//   - catching that sentinel in every invoke_* trampoline
//   - calling the WASM-exported setThrew / _emscripten_stack_restore on catch

// longjmpSentinel is the unique panic value thrown by _emscripten_throw_longjmp.
const longjmpSentinel = "emscripten_longjmp_sentinel"

func isLongjmpErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), longjmpSentinel)
}

// callTableRaw calls a WASM indirect-call table function, returning its error.
func callTableRaw(ctx context.Context, m api.Module, fnIdx uint32, params, results []api.ValueType, args []uint64) ([]uint64, error) {
	fn := tablexp.LookupFunction(m, 0, fnIdx, params, results)
	return fn.Call(ctx, args...)
}

// sjljSave saves the Emscripten shadow stack pointer before an invoke_* call.
func sjljSave(ctx context.Context, m api.Module) uint32 {
	r, err := m.ExportedFunction("emscripten_stack_get_current").Call(ctx)
	if err != nil || len(r) == 0 {
		return 0
	}
	return uint32(r[0])
}

// sjljRestore restores the shadow stack and marks the throw on longjmp catch.
func sjljRestore(ctx context.Context, m api.Module, sp uint32) {
	m.ExportedFunction("_emscripten_stack_restore").Call(ctx, uint64(sp))
	m.ExportedFunction("setThrew").Call(ctx, 1, 0)
}

// makeInvoke returns a GoModuleFunc for any Emscripten invoke_* trampoline.
// calledParams/calledResults describe the signature of the tabled function
// (i.e. the function being indirectly called, without the leading fn_idx).
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

// invokeParamTypes maps Emscripten type chars to WASM value types.
var invokeTypeMap = map[byte]api.ValueType{
	'i': api.ValueTypeI32,
	'j': api.ValueTypeI64,
	'f': api.ValueTypeF32,
	'd': api.ValueTypeF64,
}

// parseInvokeSignature parses an invoke_* name into the called function's
// param/result types. The name format is "invoke_" + result + params, where
// 'v' means void, 'i' = i32, 'j' = i64, 'f' = f32, 'd' = f64.
// Returns (calledParams, calledResults, ok).
func parseInvokeSignature(name string) ([]api.ValueType, []api.ValueType, bool) {
	sig := strings.TrimPrefix(name, "invoke_")
	if len(sig) == 0 {
		return nil, nil, false
	}

	// First char is the result type of the called function.
	var calledResults []api.ValueType
	if sig[0] != 'v' {
		t, ok := invokeTypeMap[sig[0]]
		if !ok {
			return nil, nil, false
		}
		calledResults = []api.ValueType{t}
	}

	// Remaining chars are the parameter types of the called function.
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

// registerInvoke registers a single invoke_* trampoline on the host module builder.
func registerInvoke(b wazero.HostModuleBuilder, name string, importParams, importResults []api.ValueType) {
	calledParams, calledResults, ok := parseInvokeSignature(name)
	if !ok {
		panic(fmt.Sprintf("cannot parse invoke signature: %s", name))
	}
	b.NewFunctionBuilder().
		WithGoModuleFunction(makeInvoke(calledParams, calledResults), importParams, importResults).
		Export(name)
}

// ── env host module ───────────────────────────────────────────────────────────
//
// Syscall stubs, emscripten_notify_memory_growth, and ffi_call_js are now
// compiled directly into the WASM via stubs.c.  The only remaining env
// imports are invoke_* trampolines and _emscripten_throw_longjmp.

func buildEnvModule(ctx context.Context, rt wazero.Runtime, compiled wazero.CompiledModule) {
	b := rt.NewHostModuleBuilder("env")

	// _emscripten_throw_longjmp: triggered by longjmp inside an invoke_*.
	// Panic with the sentinel so the surrounding invoke_* trampoline can catch
	// it, restore the shadow stack, and call setThrew.
	b.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, _ []uint64) {
			panic(longjmpSentinel)
		}), nil, nil).
		Export("_emscripten_throw_longjmp")

	// Auto-register all invoke_* trampolines from the WASM's import list.
	for _, fn := range compiled.ImportedFunctions() {
		mod, name, _ := fn.Import()
		if mod != "env" || !strings.HasPrefix(name, "invoke_") {
			continue
		}
		registerInvoke(b, name, fn.ParamTypes(), fn.ResultTypes())
	}

	if _, err := b.Instantiate(ctx); err != nil {
		panic(fmt.Sprintf("failed to build env module: %v", err))
	}
}

// extToSuffix maps common image extensions to the vips suffix used by
// vips_image_write_to_buffer (which selects the encoder by extension).
func extToSuffix(ext string) string {
	switch ext {
	case ".jpg", ".jpeg":
		return ".jpg"
	case ".png":
		return ".png"
	case ".webp":
		return ".webp"
	case ".avif":
		return ".avif"
	case ".heic", ".heif":
		// No HEIC encoder (no x265), fall back to AVIF via libaom.
		return ".avif"
	default:
		return ".jpg"
	}
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: vips-thumb <input> <output> <width> [height]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  input   — source image (JPEG, PNG, WebP, …)")
		fmt.Fprintln(os.Stderr, "  output  — destination path (format from extension)")
		fmt.Fprintln(os.Stderr, "  width   — target width in pixels")
		fmt.Fprintln(os.Stderr, "  height  — optional max height (0 = preserve aspect ratio)")
		os.Exit(1)
	}

	inputPath := os.Args[1]
	outputPath := os.Args[2]
	widthStr := os.Args[3]

	if w, err := strconv.Atoi(widthStr); err != nil || w <= 0 {
		fmt.Fprintf(os.Stderr, "error: width must be a positive integer, got %q\n", widthStr)
		os.Exit(1)
	}

	heightStr := "0"
	if len(os.Args) >= 5 {
		if h, err := strconv.Atoi(os.Args[4]); err != nil || h < 0 {
			fmt.Fprintf(os.Stderr, "error: height must be a non-negative integer, got %q\n", os.Args[4])
			os.Exit(1)
		}
		heightStr = os.Args[4]
	}

	inputData, err := os.ReadFile(inputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading input: %v\n", err)
		os.Exit(1)
	}

	suffix := extToSuffix(filepath.Ext(outputPath))

	ctx := context.Background()

	// Persistent compilation cache: wazero JIT-compiles the 4 MB WASM on the
	// first run and caches the result so subsequent runs start instantly.
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	cacheDir = filepath.Join(cacheDir, "vips-thumb")
	cache, err := wazero.NewCompilationCacheWithDir(cacheDir)
	if err != nil {
		cache = wazero.NewCompilationCache() // in-memory fallback
	}
	defer cache.Close(ctx)

	// Enable WASM threads proposal: glib's GObject uses i32.atomic.* even in
	// single-threaded builds.  Our binary has no shared memory, so atomics
	// behave as regular loads/stores at runtime.
	rt := wazero.NewRuntimeWithConfig(ctx,
		wazero.NewRuntimeConfig().
			WithCompilationCache(cache).
			WithCoreFeatures(api.CoreFeaturesV2|experimental.CoreFeaturesThreads),
	)
	defer rt.Close(ctx)

	// Compile first so we can introspect imports for invoke_* auto-detection.
	compiled, err := rt.CompileModule(ctx, wasmBin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wasm compile error: %v\n", err)
		os.Exit(1)
	}
	defer compiled.Close(ctx)

	// Register Emscripten env.* imports (invoke_* + longjmp) before instantiating.
	buildEnvModule(ctx, rt, compiled)

	// WASI provides args, env, clocks, and fd I/O (fd_read/write/seek/…).
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)

	var outBuf bytes.Buffer

	cfg := wazero.NewModuleConfig().
		WithStdin(bytes.NewReader(inputData)).
		WithStdout(&outBuf).
		WithStderr(os.Stderr).
		WithArgs("thumbnail", widthStr, suffix, heightStr)

	_, err = rt.InstantiateModule(ctx, compiled, cfg)
	if err != nil {
		var exitErr *sys.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() != 0 {
			os.Exit(int(exitErr.ExitCode()))
		}
		if !errors.As(err, &exitErr) {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}

	if outBuf.Len() == 0 {
		fmt.Fprintln(os.Stderr, "error: WASM produced no output")
		os.Exit(1)
	}

	if err := os.WriteFile(outputPath, outBuf.Bytes(), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing output: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("wrote %s (%d bytes)\n", outputPath, outBuf.Len())
}
