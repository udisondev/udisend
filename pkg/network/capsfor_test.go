package network

import (
	"testing"

	"github.com/udisondev/udisend/pkg/presence"
)

// TestCapsFor_AdvertisesMeshOnPhase10 verifies that capsFor stamps
// CapCanWebRTCMesh into the presence-record capabilities exactly
// when the node opted into mesh — and never when mesh is disabled
// (legacy / Phase ≤ 9 nodes).
func TestCapsFor_AdvertisesMeshOnPhase10(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mode        Mode
		hasPublicIP bool
		meshEnabled bool
		want        presence.Capability
	}{
		{
			name:        "client legacy",
			mode:        ModeClient,
			meshEnabled: false,
			want:        0,
		},
		{
			name:        "client phase10",
			mode:        ModeClient,
			meshEnabled: true,
			want:        presence.CapCanWebRTCMesh,
		},
		{
			name:        "client public phase10",
			mode:        ModeClient,
			hasPublicIP: true,
			meshEnabled: true,
			want:        presence.CapPublicIP | presence.CapCanWebRTCMesh,
		},
		{
			name:        "relay legacy",
			mode:        ModeRelay,
			meshEnabled: false,
			want:        presence.CapCanRelay | presence.CapCanBootstrap,
		},
		{
			name:        "relay phase10",
			mode:        ModeRelay,
			meshEnabled: true,
			want:        presence.CapCanRelay | presence.CapCanBootstrap | presence.CapCanWebRTCMesh,
		},
		{
			name:        "relay public phase10",
			mode:        ModeRelay,
			hasPublicIP: true,
			meshEnabled: true,
			want: presence.CapPublicIP |
				presence.CapCanRelay |
				presence.CapCanBootstrap |
				presence.CapCanSTUN |
				presence.CapCanTURN |
				presence.CapCanWebRTCMesh,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := capsFor(tt.mode, tt.hasPublicIP, tt.meshEnabled)
			if got != tt.want {
				t.Errorf("capsFor(%v, %v, %v) = %v, want %v",
					tt.mode, tt.hasPublicIP, tt.meshEnabled, got, tt.want)
			}
		})
	}
}
