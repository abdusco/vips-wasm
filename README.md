# vips-wasm

libvips compiled to standalone WASM for image thumbnailing.
No native dependencies — runs in any WASI-compatible runtime.

## Download

Pre-built `vips-thumbnail.wasm` is available from [GitHub Releases](../../releases).

## WASM Interface

### Protocol

- **Input**: image bytes piped to **stdin**
- **Output**: thumbnail bytes written to **stdout**
- **Errors**: diagnostic messages on **stderr**

### Arguments (argv)

```
argv[0]: "thumbnail"    (program name)
argv[1]: <width>        (pixels, required)
argv[2]: <suffix>       (".jpg", ".png", ".webp", ".avif")
argv[3]: <height>       (pixels, 0 = preserve aspect ratio; must be > 0 for crop/force)
argv[4]: <quality>      (1-100, 0 = encoder default; applies to JPEG/WebP/AVIF)
argv[5]: <strip>        (1 = strip metadata, 0 = keep)
argv[6]: <mode>         (optional: "fit" (default), "crop" or "force")
```

`argv[6]` may be omitted; the 5-argument form behaves as `fit`.

| Mode    | Behaviour | Output size | Upscales |
|---------|-----------|-------------|----------|
| `fit`   | Fit inside the width/height box (or width-only when `height=0`), keeping the aspect ratio. `VIPS_SIZE_DOWN` | at most `width` x `height` | no: smaller inputs are returned unchanged |
| `crop`  | Scale to cover the box, then centre-crop (`VIPS_INTERESTING_CENTRE`). `VIPS_SIZE_BOTH` | exactly `width` x `height` | yes |
| `force` | Stretch to the box, ignoring the aspect ratio. `VIPS_SIZE_FORCE` | exactly `width` x `height` | yes |

`crop` and `force` drop `VIPS_SIZE_DOWN` on purpose: with it, an input smaller
than the box would come out smaller than requested. They exit with an error if
`height` is 0.

### Supported formats

| Format | Read | Write | Suffix |
|--------|------|-------|--------|
| JPEG   | yes  | yes   | `.jpg` |
| PNG    | yes  | yes   | `.png` |
| WebP   | yes  | yes   | `.webp` |
| AVIF   | yes  | yes   | `.avif` |
| HEIF/HEIC | yes | no (decode-only) | n/a |

### Emscripten host imports

The WASM module uses **WASI preview1** syscalls plus Emscripten-specific
imports for setjmp/longjmp error handling:

- `invoke_*` trampolines (signatures auto-detected from the WASM import list)
- `setThrew(threw, value)`
- `_emscripten_throw_longjmp()`
- `emscripten_stack_get_current() → i32`
- `_emscripten_stack_restore(sp)`

Host runtimes must implement these. See the reference implementations below.

## Reference implementations

### Go (wazero)

[`main.go`](main.go) — full wazero host with:
- Automatic `invoke_*` trampoline registration from WASM imports
- setjmp/longjmp via panic/recover with sentinel value
- Persistent compilation cache (`~/.cache/vips-thumb/`)
- Output format detection from file extension

Library package: [`govips`](govips/govips.go)

```go
out, err := govips.Resize(ctx, inputBytes, govips.ResizeOptions{
    Width:        300,
    Height:       0,
    Format:       govips.FormatWebP, // required
    Quality:      80,
    KeepMetadata: false,
    Mode:         govips.ModeFit, // default; or ModeCrop / ModeForce
})

// Exactly 300x200, centre-cropped. Height must be > 0 for crop and force,
// otherwise Resize returns ErrInvalidOptions.
out, err = govips.Resize(ctx, inputBytes, govips.ResizeOptions{
    Width:  300,
    Height: 200,
    Format: govips.FormatWebP,
    Mode:   govips.ModeCrop,
})
```

```
go build -o vips-thumb .
./vips-thumb input.jpg output.jpg 300
./vips-thumb input.jpg output.webp 300 200
./vips-thumb -mode crop input.jpg output.webp 300 200
./vips-thumb -mode force input.jpg output.jpg 300 200
./vips-thumb -q 80 input.jpg output.webp 300
./vips-thumb -keep-metadata input.jpg output.jpg 300
```

### Deno

[`deno/run.ts`](deno/run.ts) — WebAssembly.instantiate with:
- Custom minimal WASI preview1 shim
- Native JS try/catch for setjmp/longjmp
- Proxied late-binding for circular import dependencies

```
deno run -A deno/run.ts input.jpg output.jpg 300
deno run -A deno/run.ts input.jpg output.webp 300 200 80
deno run -A deno/run.ts input.jpg output.jpg 300 0 0 --keep-metadata
deno run -A deno/run.ts input.jpg output.webp 300 200 --mode=crop
```

The runner takes `--mode=fit|crop|force` (default `fit`) and passes it as
`argv[6]`. `crop` and `force` need a height > 0.

## Building from source

### Prerequisites

- Docker

### Build the WASM

```sh
./build.sh
```

Output: `vips-thumbnail.wasm` (~9 MB)

### Build the Go CLI

```sh
go build -o vips-thumb .
```

## Libraries

The following libraries are statically linked into the WASM binary:

| Library | Purpose |
|---------|---------|
| zlib-ng | Compression |
| libffi | Foreign function interface (for GLib) |
| GLib | Core utilities for libvips |
| Expat | XML parsing |
| libexif | EXIF metadata |
| Little-CMS 2 | ICC color management |
| MozJPEG | JPEG codec |
| libpng | PNG codec |
| libwebp | WebP codec |
| libde265 | H.265 decoder (for HEIF) |
| libaom | AV1 codec (for AVIF) |
| libheif | HEIF/AVIF container |
| libvips | Image processing |
