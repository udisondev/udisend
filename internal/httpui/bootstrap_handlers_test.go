package httpui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type bootstrapEntryView struct {
	Address      string `json:"address"`
	Source       string `json:"source"`
	Enabled      bool   `json:"enabled"`
	Note         string `json:"note,omitempty"`
	LastStatus   string `json:"last_status,omitempty"`
	LastStatusAt int64  `json:"last_status_at,omitempty"`
	AddedAt      int64  `json:"added_at,omitempty"`
}

type bootstrapListView struct {
	Entries []bootstrapEntryView `json:"entries"`
	DialErr string               `json:"dial_error,omitempty"`
	Results []struct {
		Address string `json:"address"`
		Status  string `json:"status"`
		Error   string `json:"error,omitempty"`
	} `json:"results,omitempty"`
}

func parseBootstrap(t *testing.T, raw []byte) bootstrapListView {
	t.Helper()
	var out bootstrapListView
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, raw)
	}

	return out
}

func TestBootstrapAPI_AddListRemove(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	// Empty list to start.
	got, err := getJSON(alice, "/api/bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if v := parseBootstrap(t, got); len(v.Entries) != 0 {
		t.Fatalf("expected empty bootstrap list; got %#v", v.Entries)
	}

	// Add an unreachable address — dial will fail but the row persists.
	resp, err := postJSON(alice, "/api/bootstrap/add", map[string]any{
		"address": "127.0.0.1:1",
		"note":    "loopback dead port",
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	v := parseBootstrap(t, resp)
	if len(v.Entries) != 1 || v.Entries[0].Address != "127.0.0.1:1" {
		t.Fatalf("unexpected entries after add: %#v", v.Entries)
	}
	if v.Entries[0].Source != "manual" {
		t.Errorf("source = %q, want manual", v.Entries[0].Source)
	}
	if !v.Entries[0].Enabled {
		t.Error("expected new manual entry to be enabled")
	}
	if v.Entries[0].Note != "loopback dead port" {
		t.Errorf("note = %q", v.Entries[0].Note)
	}

	// Toggle off, list reflects.
	if _, err := postJSON(alice, "/api/bootstrap/toggle", map[string]any{
		"address": "127.0.0.1:1",
		"enabled": false,
	}); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	got, err = getJSON(alice, "/api/bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	v = parseBootstrap(t, got)
	if len(v.Entries) != 1 || v.Entries[0].Enabled {
		t.Fatalf("toggle off not persisted: %#v", v.Entries)
	}

	// Remove.
	if _, err := postJSON(alice, "/api/bootstrap/remove", map[string]any{
		"address": "127.0.0.1:1",
		"source":  "manual",
	}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, err = getJSON(alice, "/api/bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if v := parseBootstrap(t, got); len(v.Entries) != 0 {
		t.Fatalf("entries not empty after remove: %#v", v.Entries)
	}
}

func TestBootstrapAPI_AddRejectsBadAddress(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	cases := []struct {
		name, addr string
	}{
		{"empty", ""},
		{"missing port", "1.2.3.4"},
		{"port out of range", "1.2.3.4:99999"},
		{"port nan", "1.2.3.4:abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := postJSONResp(alice, "/api/bootstrap/add", map[string]any{"address": tc.addr})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestBootstrapAPI_RemoveCacheEntry(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	if err := alice.mngr.Storage().RecordSeenPeer(ctx, "5.6.7.8:9000"); err != nil {
		t.Fatal(err)
	}
	got, err := getJSON(alice, "/api/bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	v := parseBootstrap(t, got)
	found := false
	for _, e := range v.Entries {
		if e.Address == "5.6.7.8:9000" {
			if e.Source != "cache" {
				t.Errorf("source = %q, want cache", e.Source)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("cached entry missing: %#v", v.Entries)
	}

	if _, err := postJSON(alice, "/api/bootstrap/remove", map[string]any{
		"address": "5.6.7.8:9000",
		"source":  "cache",
	}); err != nil {
		t.Fatalf("remove cache: %v", err)
	}
	got, err = getJSON(alice, "/api/bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range parseBootstrap(t, got).Entries {
		if e.Address == "5.6.7.8:9000" {
			t.Fatalf("cached entry survived remove: %#v", e)
		}
	}
}

func TestBootstrapAPI_AddTriggersDial(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)
	bob := startMessenger(t, ctx, "bob", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	resp, err := postJSON(alice, "/api/bootstrap/add", map[string]any{"address": bob.udp})
	if err != nil {
		t.Fatalf("add bob udp: %v", err)
	}
	v := parseBootstrap(t, resp)
	if v.DialErr != "" {
		t.Fatalf("dial_error = %q (expected ok)", v.DialErr)
	}
	if len(v.Entries) != 1 {
		t.Fatalf("entries = %#v", v.Entries)
	}
	if v.Entries[0].LastStatus != "ok" {
		t.Errorf("last_status = %q, want ok", v.Entries[0].LastStatus)
	}
}

func TestBootstrapAPI_Reconnect(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)
	bob := startMessenger(t, ctx, "bob", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	// Add one reachable + one unreachable.
	if _, err := postJSON(alice, "/api/bootstrap/add", map[string]any{"address": bob.udp}); err != nil {
		t.Fatal(err)
	}
	if _, err := postJSON(alice, "/api/bootstrap/add", map[string]any{"address": "127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}

	resp, err := postJSON(alice, "/api/bootstrap/reconnect", map[string]any{})
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	v := parseBootstrap(t, resp)
	if len(v.Results) != 2 {
		t.Fatalf("results = %#v", v.Results)
	}
	for _, r := range v.Results {
		switch r.Address {
		case bob.udp:
			if r.Status != "ok" {
				t.Errorf("%s status = %q, want ok (err=%s)", r.Address, r.Status, r.Error)
			}
		case "127.0.0.1:1":
			if r.Status != "fail" {
				t.Errorf("%s status = %q, want fail", r.Address, r.Status)
			}
		default:
			t.Errorf("unexpected addr in results: %s", r.Address)
		}
	}
	statuses := map[string]string{}
	for _, e := range v.Entries {
		statuses[e.Address] = e.LastStatus
	}
	if statuses[bob.udp] != "ok" {
		t.Errorf("bob entry status = %q, want ok", statuses[bob.udp])
	}
	if statuses["127.0.0.1:1"] != "fail" {
		t.Errorf("dead entry status = %q, want fail", statuses["127.0.0.1:1"])
	}
}

func TestBootstrapAPI_AddCSRFRequired(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	body := strings.NewReader(`{"address":"1.2.3.4:9000"}`)
	req, _ := http.NewRequest(http.MethodPost, "http://"+alice.srv.LocalAddress()+"/api/bootstrap/add", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+alice.token)
	// Note: deliberately missing X-Requested-With.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}
