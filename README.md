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
argv[2]: <suffix>       (".jpg", ".png", ".webp", ".avif", ".heif")
argv[3]: [height]       (pixels, optional — 0 or omit to preserve aspect ratio)
```

Images are always scaled **down** (`VIPS_SIZE_DOWN`) — inputs smaller than the
target dimensions are returned unchanged.

### Supported formats

| Format | Read | Write | Suffix |
|--------|------|-------|--------|
| JPEG   | yes  | yes   | `.jpg` |
| PNG    | yes  | yes   | `.png` |
| WebP   | yes  | yes   | `.webp` |
| AVIF   | yes  | yes   | `.avif` |
| HEIF   | yes  | yes   | `.heif` |

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

```
go build -o vips-thumb .
./vips-thumb input.jpg output.jpg 300
./vips-thumb input.jpg output.webp 300 200
```

### Deno

[`deno/run.ts`](deno/run.ts) — WebAssembly.instantiate with:
- Custom minimal WASI preview1 shim
- Native JS try/catch for setjmp/longjmp
- Proxied late-binding for circular import dependencies

```
deno run -A deno/run.ts input.jpg output.jpg 300
```

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
