// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package metrics exposes process-wide Prometheus metrics for the GStreamer
// pipeline elements, which have no access to the per-call stats monitor.
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	trackPadErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "livekit",
		Subsystem: "sip",
		Name:      "track_pad_errors_total",
		Help:      "RTP pad events from rtpbin that could not be matched to a subscribed LiveKit track",
	}, []string{"reason"})

	tracksSubscribed = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "livekit",
		Subsystem: "sip",
		Name:      "tracks_subscribed",
		Help:      "LiveKit remote tracks currently wired into a bridge pipeline, by track source",
	}, []string{"source"})
)

func init() {
	prometheus.MustRegister(trackPadErrors, tracksSubscribed)
}

// TrackPadError counts an rtpbin pad event that did not match a subscribed track.
func TrackPadError(reason string) {
	trackPadErrors.WithLabelValues(reason).Inc()
}

// TrackSubscribed adjusts the number of wired tracks for a source by delta.
func TrackSubscribed(source string, delta float64) {
	tracksSubscribed.WithLabelValues(source).Add(delta)
}
