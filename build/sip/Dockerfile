# Copyright 2023 LiveKit, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Builds a small "transport" image: the livekit-sip binary linked against
# GStreamer 1.28.3 (/opt/gst-1.28), with RPATH baked in. The image does NOT
# ship GStreamer — DevOps extracts the binary and runs it under systemd on a
# VM that carries the gstreamer-1.28-opt deb. See build/sip/DEPLOY.md.
#
# The gstreamer deb is provided via the `gst` build context, produced by
# build/sip/Dockerfile.gstreamer. Build with:
#
#   docker buildx build --build-context gst=./out -f build/sip/Dockerfile .
#
# (CI wires this in .github/workflows/docker.yaml.)

ARG GOVERSION=1.25

FROM golang:${GOVERSION} AS go-toolchain

# Build on Ubuntu 24.04 to match the deploy VM's glibc/ABI and the gstreamer deb.
# A binary built on a newer base (e.g. 26.04) would fail to load on a 24.04 host.
FROM ubuntu:24.04 AS builder

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8

# bring in the Go toolchain from the official image (matches go.mod's `go 1.25.0`)
COPY --from=go-toolchain /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:/go/bin:$PATH
ENV GOPATH=/go

# CGO deps that are NOT bundled in /opt/gst-1.28 (soxr/opusfile/zlib are system),
# plus libunwind-dev which gstreamer's .pc files reference via Requires.private.
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential pkg-config ca-certificates \
    libopus-dev libopusfile-dev libsoxr-dev zlib1g-dev libunwind-dev \
    && rm -rf /var/lib/apt/lists/*

# install the pre-built GStreamer 1.28.3 deb (from the `gst` build context,
# produced by build/sip/Dockerfile.gstreamer) into /opt/gst-1.28
COPY --from=gst gstreamer-1.28-opt_*.deb /tmp/gst.deb
RUN apt-get update && apt-get install -y --no-install-recommends /tmp/gst.deb \
    && rm /tmp/gst.deb && rm -rf /var/lib/apt/lists/*

# pkg-config ADDITIVE: /opt wins for gstreamer/glib; system fills soxr/opusfile/zlib.
# Do NOT set PKG_CONFIG_LIBDIR (it replaces instead of appends).
ENV PKG_CONFIG_PATH=/opt/gst-1.28/lib/pkgconfig
# bake RPATH so the binary loads /opt/gst-1.28/lib without LD_LIBRARY_PATH
ENV CGO_LDFLAGS="-Wl,-rpath,/opt/gst-1.28/lib"

WORKDIR /workspace

# download go modules
COPY go.mod .
COPY go.sum .
RUN go mod download

# copy source
COPY res/ res/
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY version/ version/

# build
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags='-s -w' -o /out/livekit-sip ./cmd/livekit-sip

# fail fast if RPATH wasn't honored (binary would otherwise need LD_LIBRARY_PATH)
RUN apt-get update && apt-get install -y --no-install-recommends binutils \
    && readelf -d /out/livekit-sip | grep -E "R(UN)?PATH.*\[/opt/gst-1\.28/lib\]" \
    && rm -rf /var/lib/apt/lists/*

# ---- runtime: small transport image (NO GStreamer — it lives on the VM) ----
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# only the non-GStreamer runtime libs. GStreamer is NOT installed here, so this
# is a binary-transport image, not a standalone-runnable container — the binary's
# RPATH (/opt/gst-1.28/lib) resolves on the VM, which carries the gstreamer deb.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates libopusfile0 libsoxr0 zlib1g \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# copy binary (kept on PATH at /bin/livekit-sip, as before)
COPY --from=builder /out/livekit-sip /bin/

WORKDIR /sip

# run
ENTRYPOINT ["livekit-sip", "--config=/sip/config.yaml"]
