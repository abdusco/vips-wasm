/*
 * thumbnail.c — minimal libvips thumbnail via stdin/stdout (standalone WASM).
 *
 * Usage: thumbnail <width> <suffix> <height> <quality> <strip>
 *   width   — target width in pixels
 *   suffix  — output format hint: ".jpg", ".png", ".webp", etc.
 *   height  — max height (0 = preserve aspect ratio)
 *   quality — 1-100 output quality (0 = encoder default); applies to JPEG/WebP/AVIF
 *   strip   — 1 = strip metadata, 0 = keep
 *
 * Reads the source image from stdin, writes the thumbnail to stdout.
 * Emscripten -sSTANDALONE_WASM=1 stubs out path_open, so file I/O must
 * go through pre-opened descriptors (stdin=0, stdout=1, stderr=2).
 */
#include <vips/vips.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* Read all bytes from fp into a heap buffer.  Returns NULL on OOM. */
static void *read_all(FILE *fp, size_t *out_len) {
    size_t cap = 65536, len = 0;
    unsigned char *buf = malloc(cap);
    if (!buf) return NULL;
    size_t n;
    while ((n = fread(buf + len, 1, cap - len, fp)) > 0) {
        len += n;
        if (len == cap) {
            cap *= 2;
            unsigned char *nb = realloc(buf, cap);
            if (!nb) { free(buf); return NULL; }
            buf = nb;
        }
    }
    *out_len = len;
    return buf;
}

int main(int argc, char *argv[]) {
    if (argc < 6) {
        fprintf(stderr, "usage: thumbnail <width> <suffix> <height> <quality> <strip>\n");
        fprintf(stderr, "  reads image from stdin, writes thumbnail to stdout\n");
        return 1;
    }

    int width  = atoi(argv[1]);
    const char *suffix = argv[2]; /* e.g. ".jpg", ".png", ".webp" */
    int height  = atoi(argv[3]);
    int quality = atoi(argv[4]); /* 0 = encoder default */
    int strip   = atoi(argv[5]); /* 1 = strip metadata */

    if (width <= 0) {
        fprintf(stderr, "width must be > 0\n");
        return 1;
    }

    /* Prevent libvips from writing disc-backed temp images.  Without this,
     * vips_thumbnail_buffer on a large JPEG may exceed the default 100 MB
     * threshold and trigger vips_sink_disc — which spawns a "wbuffer" thread
     * that needs to run concurrently with the main thread.  10 GB is large
     * enough to keep all intermediates in memory for any realistic thumbnail. */
    g_setenv("VIPS_DISC_THRESHOLD", "10737418240", TRUE);

    if (vips_init(argv[0])) {
        fprintf(stderr, "vips_init: %s\n", vips_error_buffer());
        return 1;
    }
    vips_concurrency_set(1);
    vips_cache_set_max(0);

    size_t in_len = 0;
    void *in_buf = read_all(stdin, &in_len);
    if (!in_buf || in_len == 0) {
        fprintf(stderr, "failed to read stdin\n");
        vips_shutdown();
        return 1;
    }

    VipsImage *out = NULL;
    int r;
    if (height > 0) {
        r = vips_thumbnail_buffer(in_buf, in_len, &out, width,
            "height", height, "size", VIPS_SIZE_DOWN, NULL);
    } else {
        r = vips_thumbnail_buffer(in_buf, in_len, &out, width,
            "size", VIPS_SIZE_DOWN, NULL);
    }

    if (r) {
        free(in_buf);
        fprintf(stderr, "vips_thumbnail_buffer: %s\n", vips_error_buffer());
        vips_shutdown();
        return 1;
    }

    /* Keep in_buf alive until after write_to_buffer — libvips builds a lazy
     * pipeline that references the blob's memory until evaluation completes. */
    void *out_buf = NULL;
    size_t out_len = 0;
    int w;
    if (quality > 0 && strip) {
        w = vips_image_write_to_buffer(out, suffix, &out_buf, &out_len,
            "Q", quality, "strip", TRUE, NULL);
    } else if (quality > 0) {
        w = vips_image_write_to_buffer(out, suffix, &out_buf, &out_len,
            "Q", quality, NULL);
    } else if (strip) {
        w = vips_image_write_to_buffer(out, suffix, &out_buf, &out_len,
            "strip", TRUE, NULL);
    } else {
        w = vips_image_write_to_buffer(out, suffix, &out_buf, &out_len, NULL);
    }
    if (w) {
        free(in_buf);
        fprintf(stderr, "vips_image_write_to_buffer: %s\n", vips_error_buffer());
        g_object_unref(out);
        vips_shutdown();
        return 1;
    }
    g_object_unref(out);
    free(in_buf);

    fwrite(out_buf, 1, out_len, stdout);
    g_free(out_buf);

    vips_shutdown();
    return 0;
}
