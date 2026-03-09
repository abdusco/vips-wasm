#!/usr/bin/env -S deno run --allow-read --allow-write
/**
 * run.ts — thumbnail images using libvips compiled to standalone WASM.
 *
 * Usage:
 *   deno run --allow-read --allow-write run.ts <input> <output> <width> [height] [quality] [--keep-metadata]
 *
 * Runs the same thumbnail.wasm built by ../wasm/build.sh.  Uses Deno's WASI
 * support for fd_read/fd_write/etc. and native JS try/catch for the Emscripten
 * setjmp/longjmp invoke_* protocol.
 */

const WASM_PATH = new URL("../wasm/thumbnail.wasm", import.meta.url).pathname;

// ── Emscripten invoke_* protocol ────────────────────────────────────────────
//
// _emscripten_throw_longjmp throws a JS error; each invoke_* wraps a table
// call in try/catch.  On catch: restore shadow stack + setThrew(1, 0).

const longjmpSentinel = Symbol("emscripten_longjmp");

class LongjmpError extends Error {
  readonly tag = longjmpSentinel;
}

function isLongjmpError(e: unknown): boolean {
  return e instanceof LongjmpError;
}

type WasmExports = {
  memory: WebAssembly.Memory;
  __indirect_function_table: WebAssembly.Table;
  emscripten_stack_get_current: () => number;
  _emscripten_stack_restore: (sp: number) => void;
  setThrew: (threw: number, value: number) => void;
  _start: () => void;
};

function makeInvoke(exports: WasmExports) {
  const table = exports.__indirect_function_table;
  return (idx: number, ...args: number[]): number => {
    const sp = exports.emscripten_stack_get_current();
    try {
      return (table.get(idx) as CallableFunction)(...args) ?? 0;
    } catch (e) {
      if (isLongjmpError(e)) {
        exports._emscripten_stack_restore(sp);
        exports.setThrew(1, 0);
        return 0;
      }
      throw e;
    }
  };
}

// ── WASI context ────────────────────────────────────────────────────────────

// Minimal WASI "preview1" implementation: just enough for Emscripten
// standalone WASM (stdin/stdout/stderr via fd_read/fd_write, args, clock).

function buildWasi(
  args: string[],
  stdinBuf: Uint8Array,
  stdoutChunks: Uint8Array[],
) {
  let stdinOffset = 0;
  let memory: WebAssembly.Memory;

  function setMemory(m: WebAssembly.Memory) {
    memory = m;
  }

  function mem8(): Uint8Array {
    return new Uint8Array(memory.buffer);
  }
  function mem32(): Uint32Array {
    return new Uint32Array(memory.buffer);
  }

  // Encode args as null-terminated strings.
  const encodedArgs = args.map((a) => new TextEncoder().encode(a + "\0"));

  const wasi: Record<string, CallableFunction> = {
    // ── args ───────────────────────────────────────────────────────────
    args_sizes_get(countPtr: number, sizePtr: number): number {
      const v = mem32();
      v[countPtr >> 2] = encodedArgs.length;
      v[sizePtr >> 2] = encodedArgs.reduce((s, a) => s + a.length, 0);
      return 0;
    },
    args_get(argvPtr: number, bufPtr: number): number {
      const v = mem32();
      const m = mem8();
      for (const arg of encodedArgs) {
        v[argvPtr >> 2] = bufPtr;
        argvPtr += 4;
        m.set(arg, bufPtr);
        bufPtr += arg.length;
      }
      return 0;
    },

    // ── environ (empty) ───────────────────────────────────────────────
    environ_sizes_get(countPtr: number, sizePtr: number): number {
      const v = mem32();
      v[countPtr >> 2] = 0;
      v[sizePtr >> 2] = 0;
      return 0;
    },
    environ_get(_envPtr: number, _bufPtr: number): number {
      return 0;
    },

    // ── clock ─────────────────────────────────────────────────────────
    clock_time_get(
      _id: number,
      _precision: bigint,
      outPtr: number,
    ): number {
      const ns = BigInt(Date.now()) * 1_000_000n;
      new DataView(memory.buffer).setBigUint64(outPtr, ns, true);
      return 0;
    },

    // ── fd_write (stdout=1, stderr=2) ─────────────────────────────────
    fd_write(
      fd: number,
      iovsPtr: number,
      iovsLen: number,
      nwrittenPtr: number,
    ): number {
      const v = mem32();
      const m = mem8();
      let written = 0;
      for (let i = 0; i < iovsLen; i++) {
        const ptr = v[(iovsPtr >> 2) + i * 2];
        const len = v[(iovsPtr >> 2) + i * 2 + 1];
        const chunk = m.slice(ptr, ptr + len);
        if (fd === 1) {
          stdoutChunks.push(chunk);
        } else if (fd === 2) {
          Deno.stderr.writeSync(chunk);
        }
        written += len;
      }
      v[nwrittenPtr >> 2] = written;
      return 0;
    },

    // ── fd_read (stdin=0) ─────────────────────────────────────────────
    fd_read(
      fd: number,
      iovsPtr: number,
      iovsLen: number,
      nreadPtr: number,
    ): number {
      if (fd !== 0) {
        mem32()[nreadPtr >> 2] = 0;
        return 8; // EBADF
      }
      const v = mem32();
      const m = mem8();
      let totalRead = 0;
      for (let i = 0; i < iovsLen; i++) {
        const ptr = v[(iovsPtr >> 2) + i * 2];
        const len = v[(iovsPtr >> 2) + i * 2 + 1];
        const avail = Math.min(len, stdinBuf.length - stdinOffset);
        if (avail > 0) {
          m.set(stdinBuf.subarray(stdinOffset, stdinOffset + avail), ptr);
          stdinOffset += avail;
          totalRead += avail;
        }
        if (avail < len) break; // EOF
      }
      v[nreadPtr >> 2] = totalRead;
      return 0;
    },

    // ── fd stubs ──────────────────────────────────────────────────────
    fd_close(_fd: number): number {
      return 0;
    },
    fd_seek(
      _fd: number,
      _offset: bigint,
      _whence: number,
      _newOffsetPtr: number,
    ): number {
      return 0;
    },
    fd_fdstat_get(fd: number, bufPtr: number): number {
      // Return a minimal fdstat: filetype + flags.
      const v = mem8();
      // Zero the 24-byte struct.
      v.fill(0, bufPtr, bufPtr + 24);
      // filetype: 2 = CHARACTER_DEVICE for stdio fds.
      if (fd <= 2) v[bufPtr] = 2;
      return 0;
    },
    fd_prestat_get(_fd: number, _bufPtr: number): number {
      return 8; // EBADF — no preopened dirs
    },
    fd_prestat_dir_name(_fd: number, _pathPtr: number, _pathLen: number): number {
      return 8; // EBADF
    },

    // ── proc ──────────────────────────────────────────────────────────
    proc_exit(code: number): never {
      if (code !== 0) {
        Deno.exit(code);
      }
      // For code 0, throw to unwind cleanly.
      throw new Error(`proc_exit(${code})`);
    },
  };

  return { wasi, setMemory };
}

// ── Main ────────────────────────────────────────────────────────────────────

async function main() {
  // Parse --keep-metadata flag
  const keepMetadata = Deno.args.includes("--keep-metadata");
  const positionalArgs = Deno.args.filter((a) => !a.startsWith("--"));

  const [input, output, widthStr, heightStr, qualityStr] = positionalArgs;
  if (!input || !output || !widthStr) {
    console.error(
      "usage: run.ts <input> <output> <width> [height] [quality] [--keep-metadata]",
    );
    Deno.exit(1);
  }

  const width = parseInt(widthStr);
  if (isNaN(width) || width <= 0) {
    console.error(`error: width must be a positive integer, got "${widthStr}"`);
    Deno.exit(1);
  }

  const height = heightStr ? parseInt(heightStr) : 0;
  const quality = qualityStr ? parseInt(qualityStr) : 0;
  const strip = keepMetadata ? 0 : 1;

  // Determine output format from extension.
  const ext = output.match(/\.[^.]+$/)?.[0]?.toLowerCase() ?? ".jpg";
  const suffixMap: Record<string, string> = {
    ".jpg": ".jpg", ".jpeg": ".jpg", ".png": ".png", ".webp": ".webp",
    ".avif": ".avif", ".heic": ".avif", ".heif": ".avif",
  };
  const suffix = suffixMap[ext] ?? ".jpg";

  const inputData = await Deno.readFile(input);
  const stdoutChunks: Uint8Array[] = [];

  const { wasi, setMemory } = buildWasi(
    ["thumbnail", String(width), suffix, String(height), String(quality), String(strip)],
    inputData,
    stdoutChunks,
  );

  // Compile and instantiate.
  const wasmBytes = await Deno.readFile(WASM_PATH);
  const wasmModule = await WebAssembly.compile(wasmBytes);

  // Build env imports: _emscripten_throw_longjmp + all invoke_* detected
  // from the module's imports.
  const envImports: Record<string, CallableFunction> = {
    _emscripten_throw_longjmp() {
      throw new LongjmpError();
    },
  };

  // Detect invoke_* imports from the compiled module.
  for (const imp of WebAssembly.Module.imports(wasmModule)) {
    if (imp.module === "env" && imp.name.startsWith("invoke_")) {
      // All invoke_* share the same try/catch logic; the WASM type system
      // handles argument/result counts automatically.
      envImports[imp.name] = null!; // placeholder, filled after instantiation
    }
  }

  // We need the instance to build invoke (for table + exports access),
  // but the instance needs env imports.  Use a late-binding proxy.
  let exports: WasmExports;
  let invoke: ReturnType<typeof makeInvoke>;

  // Create proxied env: invoke_* calls are forwarded through `invoke`.
  const envProxy: Record<string, CallableFunction> = {
    _emscripten_throw_longjmp: envImports._emscripten_throw_longjmp,
  };
  for (const name of Object.keys(envImports)) {
    if (name.startsWith("invoke_")) {
      envProxy[name] = (...args: number[]) => invoke(...args);
    }
  }

  const instance = await WebAssembly.instantiate(wasmModule, {
    env: envProxy,
    wasi_snapshot_preview1: wasi,
  });

  exports = instance.exports as unknown as WasmExports;
  setMemory(exports.memory);
  invoke = makeInvoke(exports);

  // Run.
  try {
    exports._start();
  } catch (e) {
    if (e instanceof Error && e.message.startsWith("proc_exit(0)")) {
      // Clean exit.
    } else {
      throw e;
    }
  }

  // Collect output.
  const totalLen = stdoutChunks.reduce((s, c) => s + c.length, 0);
  if (totalLen === 0) {
    console.error("error: WASM produced no output");
    Deno.exit(1);
  }

  const outBuf = new Uint8Array(totalLen);
  let offset = 0;
  for (const chunk of stdoutChunks) {
    outBuf.set(chunk, offset);
    offset += chunk.length;
  }

  await Deno.writeFile(output, outBuf);
  console.log(`wrote ${output} (${totalLen} bytes)`);
}

await main();
