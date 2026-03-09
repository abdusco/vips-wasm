/*
 * stubs.c — stubs for standalone WASM/WASI build (no pthreads, no filesystem).
 *
 * Defining these here means Emscripten links them directly into the WASM
 * binary instead of generating "env" imports that the host must provide.
 * This keeps the host code (Go/Deno) minimal: only invoke_* trampolines
 * and _emscripten_throw_longjmp remain as env imports.
 *
 * Patches applied to the libvips source (deps/vips/libvips/iofuncs/):
 *   threadset.c  — vips_threadset_run runs the task inline (synchronously).
 *   sinkdisc.c   — wbuffer_flush writes the buffer synchronously inline.
 */

#include <pthread.h>
#include <stdlib.h>

/* ── pthread stub ──────────────────────────────────────────────────────────── */

/* pthread_setattr_default_np is a GNU extension present in the pthreads
 * sysroot but absent from the single-threaded one; stub it out.
 */
int pthread_setattr_default_np(const pthread_attr_t *attr)
{
    (void)attr;
    return 0;
}

/* ── Emscripten syscall stubs ──────────────────────────────────────────────── */
/* libvips / GLib call these but we have no real filesystem. */

long __syscall_getcwd(long buf, long size)
{
    /* Write "/" as the cwd. */
    if (size >= 2) {
        char *p = (char *)buf;
        p[0] = '/';
        p[1] = '\0';
        return buf;
    }
    return -22; /* -EINVAL */
}

long __syscall_faccessat(long dirfd, long path, long mode, long flags)
{
    (void)dirfd; (void)path; (void)mode; (void)flags;
    return 0;
}

long __syscall_ftruncate64(long fd, long long length)
{
    (void)fd; (void)length;
    return 0;
}

long __syscall_poll(long fds, long nfds, long timeout)
{
    (void)fds; (void)nfds; (void)timeout;
    return 0;
}

long __syscall_rmdir(long path)
{
    (void)path;
    return 0;
}

long __syscall_unlinkat(long dirfd, long path, long flags)
{
    (void)dirfd; (void)path; (void)flags;
    return 0;
}

/* ── Emscripten runtime stubs ──────────────────────────────────────────────── */

void emscripten_notify_memory_growth(int idx)
{
    (void)idx;
}

/* libffi dynamic dispatch — used by GLib GObject signals.
 * Thumbnail operations don't trigger this path; abort if reached. */
void ffi_call_js(long cif, long fn, long rvalue, long avalue)
{
    (void)cif; (void)fn; (void)rvalue; (void)avalue;
    abort();
}
