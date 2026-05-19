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

ARG GOVERSION=1.25

# Ubuntu 26.04 (Resolute) ships GStreamer 1.28.2, which provides the SDP API
# (e.g. gst_sdp_media_add_media_from_structure) required by the go-gst fork.
FROM golang:${GOVERSION} AS go-toolchain

FROM ubuntu:26.04 AS builder

ARG TARGETPLATFORM

# bring in the Go toolchain from the official image
COPY --from=go-toolchain /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:/go/bin:$PATH
ENV GOPATH=/go

WORKDIR /workspace

# install build dependencies (CGO needs dev headers for opus, soxr, glib, and gstreamer)
RUN apt-get update && apt-get install -y \
    build-essential pkg-config \
    libopus-dev libopusfile-dev libsoxr-dev \
    libglib2.0-dev \
    libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev \
    && rm -rf /var/lib/apt/lists/*

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
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on go build -a -o livekit-sip ./cmd/livekit-sip

FROM ubuntu:26.04

# install runtime libraries (GStreamer 1.28 + opus/soxr)
RUN apt-get update && \
    apt-get install -y \
        libopus0 libopusfile0 libsoxr0 \
        libgstreamer1.0-0 gstreamer1.0-plugins-base gstreamer1.0-plugins-good \
        gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly gstreamer1.0-libav && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# copy binary
COPY --from=builder /workspace/livekit-sip /bin/

WORKDIR /sip

# run
ENTRYPOINT ["livekit-sip", "--config=/sip/config.yaml"]
