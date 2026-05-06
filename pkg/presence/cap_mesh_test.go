package presence_test

import (
	"strings"
	"testing"

	"github.com/udisondev/udisend/pkg/presence"
)

// TestCapability_HasMeshBit ensures the new CapCanWebRTCMesh bit
// (Phase 10) sits at position 5 and round-trips through Has /
// String without colliding with existing flags.
func TestCapability_HasMeshBit(t *testing.T) {
	t.Parallel()

	if presence.CapCanWebRTCMesh != 1<<5 {
		t.Fatalf("CapCanWebRTCMesh = %d, want %d", presence.CapCanWebRTCMesh, 1<<5)
	}

	tests := []struct {
		name string
		caps presence.Capability
		want bool
	}{
		{name: "zero", caps: 0, want: false},
		{name: "only mesh", caps: presence.CapCanWebRTCMesh, want: true},
		{name: "mesh+public", caps: presence.CapCanWebRTCMesh | presence.CapPublicIP, want: true},
		{name: "all but mesh",
			caps: presence.CapPublicIP | presence.CapCanRelay | presence.CapCanBootstrap |
				presence.CapCanSTUN | presence.CapCanTURN,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.caps.Has(presence.CapCanWebRTCMesh); got != tt.want {
				t.Errorf("Has(CapCanWebRTCMesh) on %s = %v, want %v",
					tt.caps.String(), got, tt.want)
			}
		})
	}
}

// TestCapability_StringIncludesMesh ensures String() surfaces the
// new bit so operator diagnostics include it.
func TestCapability_StringIncludesMesh(t *testing.T) {
	t.Parallel()

	caps := presence.CapPublicIP | presence.CapCanWebRTCMesh
	got := caps.String()
	if !strings.Contains(got, "WebRTCMesh") {
		t.Errorf("String() = %q, missing WebRTCMesh label", got)
	}
}
