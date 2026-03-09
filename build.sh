#!/usr/bin/env bash
# Builds vips-thumbnail.wasm using the pre-compiled WASM libraries in Docker.
set -e
cd "$(dirname "$0")"

IMAGE_TAG="${VIPS_WASM_IMAGE:-vips-wasm}"
DOCKER_BUILD_FLAGS="${DOCKER_BUILD_FLAGS:-}"

if [ -n "$DOCKER_BUILD_FLAGS" ]; then
  docker buildx build $DOCKER_BUILD_FLAGS -t "$IMAGE_TAG" .
else
  docker build -t "$IMAGE_TAG" .
fi

docker run --rm -v "$(pwd)/wasm:/src/wasm" "$IMAGE_TAG" -c '
  VIPS_CFLAGS=$(pkg-config --static --cflags vips | sed "s/-pthread//g")
  VIPS_LIBS=$(pkg-config --static --libs vips | sed "s/-pthread//g")
  emcc /src/wasm/thumbnail.c /src/wasm/stubs.c \
    -O3 $VIPS_CFLAGS $VIPS_LIBS -lc++ \
    -sSTANDALONE_WASM=1 -sUSE_PTHREADS=0 \
    -sALLOW_MEMORY_GROWTH=1 -sSTACK_SIZE=2097152 -sASSERTIONS=0 \
    -o /src/wasm/thumbnail.wasm
'

cp wasm/thumbnail.wasm vips-thumbnail.wasm
cp wasm/thumbnail.wasm govips/thumbnail.wasm
echo "Built: vips-thumbnail.wasm ($(du -sh vips-thumbnail.wasm | cut -f1))"
