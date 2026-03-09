#!/usr/bin/env bash
# Builds vips-thumbnail.wasm using the pre-compiled WASM libraries in Docker.
set -e
cd "$(dirname "$0")"

docker build -t vips-wasm .
docker run --rm -v "$(pwd)/wasm:/src/wasm" vips-wasm -c '
  VIPS_CFLAGS=$(pkg-config --static --cflags vips | sed "s/-pthread//g")
  VIPS_LIBS=$(pkg-config --static --libs vips | sed "s/-pthread//g")
  emcc /src/wasm/thumbnail.c /src/wasm/stubs.c \
    -O3 $VIPS_CFLAGS $VIPS_LIBS -lc++ \
    -sSTANDALONE_WASM=1 -sUSE_PTHREADS=0 \
    -sALLOW_MEMORY_GROWTH=1 -sSTACK_SIZE=2097152 -sASSERTIONS=0 \
    -o /src/wasm/thumbnail.wasm
'

cp wasm/thumbnail.wasm vips-thumbnail.wasm
echo "Built: vips-thumbnail.wasm ($(du -sh vips-thumbnail.wasm | cut -f1))"
