# Builds all libvips WASM dependencies as cached Docker layers.
# The final link step (thumbnail.c → thumbnail.wasm) runs at container runtime
# with the source mounted, so C source changes don't require rebuilding the image.
FROM docker.io/emscripten/emsdk:5.0.2

ENV PATH="/root/.local/bin:$PATH"

RUN \
  apt-get update && \
  apt-get install -qqy \
    build-essential \
    libglib2.0-dev \
    pkgconf \
    ninja-build \
    pipx && \
  pipx install meson

# Emscripten patches
RUN \
  curl -Ls https://github.com/emscripten-core/emscripten/compare/5.0.2...kleisauke:wasm-vips-5.0.2.patch | patch -p1 -d $EMSDK/upstream/emscripten && \
  curl -Ls https://github.com/emscripten-core/emscripten/compare/be68a76...kleisauke:mimalloc-update-3.2.8.patch | patch -p1 -d $EMSDK/upstream/emscripten && \
  emcc --clear-cache && embuilder build sysroot --force

# ── Build environment ────────────────────────────────────────────────────────
ENV TARGET="/opt/vips"
ENV CFLAGS="-O3 -fvisibility=hidden -msimd128 -DWASM_SIMD_COMPAT_SLOW"
ENV CXXFLAGS="$CFLAGS"
ENV LDFLAGS="-O3 -L/opt/vips/lib -sAUTO_JS_LIBRARIES=0 -sAUTO_NATIVE_LIBRARIES=0"
ENV CPATH="/opt/vips/include"
ENV PKG_CONFIG_PATH="/opt/vips/lib/pkgconfig"
ENV EM_PKG_CONFIG_PATH="/opt/vips/lib/pkgconfig"
ENV PKG_CONFIG="pkg-config --static"
ENV CHOST="wasm32-unknown-linux"
ENV CPP="emcc -E"

# Files needed during dep builds (rarely change → good cache locality)
COPY wasm/emscripten-cross.ini /build/emscripten-cross.ini
COPY wasm/patches/ /build/patches/

ENV MESON_CROSS="--cross-file=/build/emscripten-cross.ini"

# Helper to download and extract source tarballs
# Usage: fresh_src <dir> <url> [tar-flags]
# Each RUN below uses this inline.

# ── zlib-ng 2.3.3 ────────────────────────────────────────────────────────────
RUN \
  mkdir -p /tmp/zlib-ng && \
  curl -Ls https://github.com/zlib-ng/zlib-ng/archive/refs/tags/2.3.3.tar.gz | tar -xzC /tmp/zlib-ng --strip-components=1 && \
  cd /tmp/zlib-ng && \
  sed -i 's/BASEARCH_X86_FOUND/& OR BASEARCH_WASM32_FOUND/g' CMakeLists.txt && \
  emcmake cmake -B_build -S. \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$TARGET" \
    -DBUILD_SHARED_LIBS=FALSE -DBUILD_TESTING=FALSE \
    -DWITH_RUNTIME_CPU_DETECTION=FALSE -DZLIB_COMPAT=TRUE && \
  make -C _build -j$(nproc) install && \
  rm -rf /tmp/zlib-ng

# ── libffi 3.5.2 ─────────────────────────────────────────────────────────────
RUN \
  mkdir -p /tmp/ffi && \
  curl -Ls https://github.com/libffi/libffi/releases/download/v3.5.2/libffi-3.5.2.tar.gz | tar -xzC /tmp/ffi --strip-components=1 && \
  cd /tmp/ffi && \
  sed -i 's/ -fexceptions//g' configure && \
  emconfigure ./configure \
    --host=$CHOST --prefix="$TARGET" \
    --enable-static --disable-shared --disable-dependency-tracking \
    --disable-builddir --disable-multi-os-directory \
    --disable-raw-api --disable-structs --disable-docs && \
  make -j$(nproc) install && \
  rm -rf /tmp/ffi

# ── glib 2.87.3 ──────────────────────────────────────────────────────────────
RUN \
  mkdir -p /tmp/glib && \
  curl -Ls https://download.gnome.org/sources/glib/2.87/glib-2.87.3.tar.xz | tar -xJC /tmp/glib --strip-components=1 && \
  cd /tmp/glib && \
  curl -Ls "https://github.com/GNOME/glib/compare/2.87.3...kleisauke:wasm-vips-2.87.3.patch" | patch -p1 && \
  meson setup _build --prefix="$TARGET" $MESON_CROSS \
    --default-library=static --buildtype=release \
    --force-fallback-for=gvdb \
    -Dintrospection=disabled -Dselinux=disabled -Dxattr=false \
    -Dlibmount=disabled -Dsysprof=disabled -Dnls=disabled \
    -Dglib_debug=disabled -Dtests=false \
    -Dglib_assert=false -Dglib_checks=false && \
  meson install -C _build --tag devel && \
  rm -rf /tmp/glib

# ── expat 2.7.4 ──────────────────────────────────────────────────────────────
RUN \
  mkdir -p /tmp/expat && \
  curl -Ls https://github.com/libexpat/libexpat/releases/download/R_2_7_4/expat-2.7.4.tar.xz | tar -xJC /tmp/expat --strip-components=1 && \
  cd /tmp/expat && \
  emconfigure ./configure \
    --host=$CHOST --prefix="$TARGET" \
    --enable-static --disable-shared --disable-dependency-tracking \
    --without-xmlwf --without-docbook --without-getrandom \
    --without-sys-getrandom --without-examples --without-tests && \
  make -j$(nproc) install dist_cmake_DATA= nodist_cmake_DATA= && \
  rm -rf /tmp/expat

# ── libexif 0.6.25 ───────────────────────────────────────────────────────────
RUN \
  mkdir -p /tmp/exif && \
  curl -Ls https://github.com/libexif/libexif/releases/download/v0.6.25/libexif-0.6.25.tar.xz | tar -xJC /tmp/exif --strip-components=1 && \
  cd /tmp/exif && \
  emconfigure ./configure \
    --host=$CHOST --prefix="$TARGET" \
    --enable-static --disable-shared --disable-dependency-tracking \
    --disable-docs --disable-nls \
    --without-libiconv-prefix --without-libintl-prefix \
    CPPFLAGS="-DNO_VERBOSE_TAG_DATA" && \
  make -j$(nproc) install doc_DATA= && \
  rm -rf /tmp/exif

# ── lcms2 2.18 ───────────────────────────────────────────────────────────────
RUN \
  mkdir -p /tmp/lcms2 && \
  curl -Ls https://github.com/mm2/Little-CMS/releases/download/lcms2.18/lcms2-2.18.tar.gz | tar -xzC /tmp/lcms2 --strip-components=1 && \
  cd /tmp/lcms2 && \
  meson setup _build --prefix="$TARGET" $MESON_CROSS \
    --default-library=static --buildtype=release \
    -Dtests=disabled -Djpeg=disabled -Dtiff=disabled && \
  meson install -C _build --tag devel && \
  rm -rf /tmp/lcms2

# ── mozjpeg (commit 0826579) ─────────────────────────────────────────────────
RUN \
  mkdir -p /tmp/jpeg && \
  curl -Ls https://github.com/mozilla/mozjpeg/archive/0826579.tar.gz | tar -xzC /tmp/jpeg --strip-components=1 && \
  cd /tmp/jpeg && \
  curl -Ls https://github.com/kleisauke/libjpeg-turbo/commit/a60fb467fc7601b008741d42e98268c8a7bcb5b4.patch | patch -p1 && \
  sed -i 's/JCP_MAX_COMPRESSION/JCP_FASTEST/' jcapimin.c && \
  emcmake cmake -B_build -S. \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$TARGET" \
    -DBUILD_SHARED_LIBS=FALSE -DWITH_JPEG8=TRUE -DWITH_SIMD=FALSE \
    -DWITH_TURBOJPEG=FALSE -DPNG_SUPPORTED=FALSE \
    -DCMAKE_C_FLAGS="$CFLAGS -DNO_GETENV -DNO_PUTENV" && \
  make -C _build -j$(nproc) install && \
  rm -rf /tmp/jpeg

# ── libpng 1.6.55 ────────────────────────────────────────────────────────────
RUN \
  mkdir -p /tmp/png && \
  curl -Ls https://github.com/pnggroup/libpng/archive/refs/tags/v1.6.55.tar.gz | tar -xzC /tmp/png --strip-components=1 && \
  cd /tmp/png && \
  emconfigure ./configure \
    --host=$CHOST --prefix="$TARGET" \
    --enable-static --disable-shared --disable-dependency-tracking \
    --disable-tests --disable-tools --without-binconfigs \
    --disable-unversioned-libpng-config && \
  make -j$(nproc) install dist_man_MANS= && \
  rm -rf /tmp/png

# ── libwebp 1.6.0 ────────────────────────────────────────────────────────────
RUN \
  mkdir -p /tmp/webp && \
  curl -Ls https://storage.googleapis.com/downloads.webmproject.org/releases/webp/libwebp-1.6.0.tar.gz | tar -xzC /tmp/webp --strip-components=1 && \
  cd /tmp/webp && \
  sed -i 's/-msse/-msimd128 &/g' configure && \
  emconfigure ./configure \
    --host=$CHOST --prefix="$TARGET" \
    --enable-static --disable-shared --disable-dependency-tracking \
    --enable-sse2 --enable-sse4.1 --disable-neon \
    --disable-gl --disable-sdl --disable-png --disable-jpeg \
    --disable-tiff --disable-gif --disable-threading \
    --enable-libwebpmux --enable-libwebpdemux \
    CPPFLAGS="-DWEBP_DISABLE_STATS -DWEBP_REDUCE_CSP" && \
  make -j$(nproc) install bin_PROGRAMS= noinst_PROGRAMS= man_MANS= && \
  rm -rf /tmp/webp

# ── libde265 1.0.16 (H.265 decoder for HEIF) ────────────────────────────────
RUN \
  mkdir -p /tmp/de265 && \
  curl -Ls https://github.com/strukturag/libde265/releases/download/v1.0.16/libde265-1.0.16.tar.gz | tar -xzC /tmp/de265 --strip-components=1 && \
  cd /tmp/de265 && \
  emcmake cmake -B_build -S. \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$TARGET" \
    -DBUILD_SHARED_LIBS=FALSE -DBUILD_ENCODER=FALSE -DBUILD_DECODER=FALSE \
    -DENABLE_ENCODER=FALSE && \
  make -C _build -j$(nproc) install && \
  rm -rf /tmp/de265

# ── libaom 3.13.1 (AV1 codec — slowest build) ───────────────────────────────
RUN \
  mkdir -p /tmp/aom && \
  curl -Ls https://storage.googleapis.com/aom-releases/libaom-3.13.1.tar.gz | tar -xzC /tmp/aom --strip-components=1 && \
  cd /tmp/aom && \
  emcmake cmake -B_build -S. \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$TARGET" \
    -DAOM_TARGET_CPU=generic -DCONFIG_RUNTIME_CPU_DETECT=0 \
    -DENABLE_DOCS=FALSE -DENABLE_TESTS=FALSE -DENABLE_EXAMPLES=FALSE -DENABLE_TOOLS=FALSE \
    -DCONFIG_WEBM_IO=0 -DCONFIG_AV1_HIGHBITDEPTH=0 \
    -DCONFIG_MULTITHREAD=0 && \
  make -C _build -j$(nproc) install && \
  rm -rf /tmp/aom

# ── libheif 1.21.2 (HEIF/AVIF container) ────────────────────────────────────
RUN \
  mkdir -p /tmp/heif && \
  curl -Ls https://github.com/strukturag/libheif/releases/download/v1.21.2/libheif-1.21.2.tar.gz | tar -xzC /tmp/heif --strip-components=1 && \
  cd /tmp/heif && \
  emcmake cmake -B_build -S. \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$TARGET" \
    -DCMAKE_FIND_ROOT_PATH="$TARGET" \
    -DBUILD_SHARED_LIBS=FALSE -DENABLE_PLUGIN_LOADING=FALSE -DBUILD_TESTING=FALSE \
    -DWITH_EXAMPLES=FALSE -DWITH_X265=FALSE -DWITH_OpenH264_DECODER=FALSE \
    -DWITH_LIBDE265=TRUE -DWITH_AOM_DECODER=TRUE -DWITH_AOM_ENCODER=TRUE \
    -DCMAKE_CXX_FLAGS="$CXXFLAGS -D__EMSCRIPTEN_STANDALONE_WASM__" \
    -DENABLE_MULTITHREADING_SUPPORT=FALSE && \
  make -C _build -j$(nproc) install && \
  rm -rf /tmp/heif

# ── libvips 8.18.0 ───────────────────────────────────────────────────────────
RUN \
  mkdir -p /tmp/vips && \
  curl -Ls https://github.com/libvips/libvips/releases/download/v8.18.0/vips-8.18.0.tar.xz | tar -xJC /tmp/vips --strip-components=1 && \
  cd /tmp/vips && \
  curl -Ls "https://github.com/libvips/libvips/compare/v8.18.0...kleisauke:wasm-vips-8.18.patch" | patch -p1 && \
  cp /build/patches/threadset.c libvips/iofuncs/threadset.c && \
  cp /build/patches/sinkdisc.c  libvips/iofuncs/sinkdisc.c && \
  sed -i "/subdir('man')/{N;N;N;N;d;}" meson.build && \
  meson setup _build --prefix="$TARGET" $MESON_CROSS \
    --default-library=static --buildtype=release \
    -Ddeprecated=false -Dexamples=false -Dcplusplus=false \
    -Dauto_features=disabled -Dintrospection=disabled -Dmodules=disabled \
    -Djpeg=enabled -Dpng=enabled -Dwebp=enabled \
    -Dheif=enabled \
    -Dexif=enabled -Dlcms=enabled -Dzlib=enabled && \
  meson install -C _build --tag runtime,devel && \
  rm -rf /tmp/vips

WORKDIR /src
ENTRYPOINT ["/bin/bash"]
