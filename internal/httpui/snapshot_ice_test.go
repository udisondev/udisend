package httpui_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestSnapshot_HonorsDisableFallback verifies the actual snapshot
// endpoint reflects the "disable default fallback" toggle. This catches
// the dead-control bug where the toggle was persisted but ignored when
// constructing the ice_servers list.
func TestSnapshot_HonorsDisableFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	// Add a manual entry so the snapshot has at least one row to render.
	if _, err := postJSON(alice, "/api/ice/add", map[string]any{"url": "stun:relay.example.com:3478"}); err != nil {
		t.Fatal(err)
	}

	body, err := getJSON(alice, "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		ICEServers []struct {
			URLs []string `json:"urls"`
		} `json:"ice_servers"`
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	beforeCount := len(snap.ICEServers)
	if beforeCount == 0 {
		t.Fatalf("expected at least the manual entry: %+v", snap)
	}

	// Toggle fallback off — discovered entries (if any) must drop, but
	// the manual entry stays.
	if _, err := postJSON(alice, "/api/ice/fallback", map[string]any{"disabled": true}); err != nil {
		t.Fatal(err)
	}
	body, err = getJSON(alice, "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.ICEServers) < 1 {
		t.Fatalf("manual entry lost: %+v", snap)
	}
	gotManual := false
	for _, s := range snap.ICEServers {
		for _, u := range s.URLs {
			if strings.HasPrefix(u, "stun:relay.example.com") {
				gotManual = true
			}
		}
	}
	if !gotManual {
		t.Fatalf("manual entry missing after disable: %+v", snap)
	}
}
