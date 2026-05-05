package httpui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type iceListView struct {
	Entries []struct {
		URL     string `json:"url"`
		Source  string `json:"source"`
		Enabled bool   `json:"enabled"`
	} `json:"entries"`
	DisableDefaultFallback bool `json:"disable_default_fallback"`
}

func TestICE_AddListRemove(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	got, err := getJSON(alice, "/api/ice")
	if err != nil {
		t.Fatal(err)
	}
	var v iceListView
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Entries) != 0 {
		t.Fatalf("non-empty initial: %+v", v.Entries)
	}

	resp, err := postJSON(alice, "/api/ice/add", map[string]any{
		"url": "stun:stun.example.com:3478",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(resp, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Entries) != 1 {
		t.Fatalf("entries after add: %+v", v.Entries)
	}
	if v.Entries[0].Source != "manual" || !v.Entries[0].Enabled {
		t.Errorf("unexpected entry: %+v", v.Entries[0])
	}

	if _, err := postJSON(alice, "/api/ice/toggle", map[string]any{
		"url": "stun:stun.example.com:3478", "enabled": false,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := postJSON(alice, "/api/ice/remove", map[string]any{
		"url": "stun:stun.example.com:3478",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = getJSON(alice, "/api/ice")
	if !strings.Contains(string(got), `"entries":[]`) {
		t.Fatalf("not empty after remove: %s", got)
	}
}

func TestICE_AddRejectsBadURL(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	cases := []string{"", "http://wrong-scheme.example.com", "stun:", "with whitespace inside"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			resp, err := postJSONResp(alice, "/api/ice/add", map[string]any{"url": c})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d", resp.StatusCode)
			}
		})
	}
}

func TestICE_FallbackToggle(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	if _, err := postJSON(alice, "/api/ice/fallback", map[string]any{"disabled": true}); err != nil {
		t.Fatal(err)
	}
	got, _ := getJSON(alice, "/api/ice")
	if !strings.Contains(string(got), `"disable_default_fallback":true`) {
		t.Fatalf("flag not persisted: %s", got)
	}
}

// TestICE_NormalizesScheme verifies that "Stun:" and "stun:" land on the
// same row (PRIMARY KEY collision via lowercased scheme). Both reviewers
// flagged orphan rows when only one of the operations normalized.
func TestICE_NormalizesScheme(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	if _, err := postJSON(alice, "/api/ice/add", map[string]any{"url": "STUN:case.example.com:3478"}); err != nil {
		t.Fatal(err)
	}
	got, _ := getJSON(alice, "/api/ice")
	if !strings.Contains(string(got), `"url":"stun:case.example.com:3478"`) {
		t.Fatalf("scheme not lowercased: %s", got)
	}
	if _, err := postJSON(alice, "/api/ice/remove", map[string]any{"url": "Stun:case.example.com:3478"}); err != nil {
		t.Fatal(err)
	}
	got, _ = getJSON(alice, "/api/ice")
	if !strings.Contains(string(got), `"entries":[]`) {
		t.Fatalf("not removed: %s", got)
	}
}

func TestNetworkStatus(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	got, err := getJSON(alice, "/api/network/status")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		LocalAddr        string `json:"local_address"`
		IdentityHash     string `json:"identity_hash"`
		RoutingTableSize int    `json:"routing_table_size"`
		ActiveSessions   int    `json:"active_sessions"`
		PublicMode       bool   `json:"public_mode"`
	}
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatal(err)
	}
	if v.LocalAddr == "" || v.IdentityHash == "" {
		t.Fatalf("missing fields: %+v", v)
	}
	if v.PublicMode {
		t.Errorf("expected public_mode=false in loopback")
	}
}
