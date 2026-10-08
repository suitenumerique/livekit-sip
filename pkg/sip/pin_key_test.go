// Copyright 2026 LiveKit, Inc.
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

package sip

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPinKeyKind(t *testing.T) {
	require.Equal(t, "unknown", pinKeyKind(0))
	require.Equal(t, "delete", pinKeyKind('*'))
	require.Equal(t, "end", pinKeyKind('#'))
	for _, d := range []byte("0123456789") {
		require.Equal(t, "digit", pinKeyKind(d))
	}
}

func TestIsPrivateSource(t *testing.T) {
	for _, ip := range []string{"10.0.0.128", "172.16.4.2", "192.168.1.10", "127.0.0.1", "::1", "fd00::1"} {
		require.True(t, isPrivateSource(ip), ip)
	}
	for _, ip := range []string{"192.44.77.50", "146.183.10.223", "2001:db8::1", "", "not-an-ip"} {
		require.False(t, isPrivateSource(ip), ip)
	}
}
