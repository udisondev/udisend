package httpui_test

import (
	"context"
	"encoding/json"
	"testing"
)

func TestStorageUsage(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	body, err := getJSON(alice, "/api/storage/usage")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		DBBytes      int64 `json:"db_bytes"`
		Messages     int   `json:"messages"`
		Contacts     int   `json:"contacts"`
		Outbox       int   `json:"outbox"`
		MessageBytes int64 `json:"message_bytes"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	if v.Messages != 0 || v.Contacts != 0 || v.Outbox != 0 || v.MessageBytes != 0 {
		t.Errorf("expected zero counters, got %+v", v)
	}
}

func TestStorageVacuum(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)
	if _, err := postJSON(alice, "/api/storage/vacuum", map[string]any{}); err != nil {
		t.Fatal(err)
	}
}

func TestSettings_LogLevelRoundtrip(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	if _, err := postJSON(alice, "/api/settings/log-level", map[string]any{"level": "debug"}); err != nil {
		t.Fatal(err)
	}
	body, err := getJSON(alice, "/api/settings/log-level")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "" || !contains(string(body), `"level":"debug"`) {
		t.Fatalf("level not persisted: %s", body)
	}
}

func TestSettings_HistoryRetentionValidation(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	resp, err := postJSONResp(alice, "/api/settings/history-retention", map[string]any{"days": -1})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if _, err := postJSON(alice, "/api/settings/history-retention", map[string]any{"days": 30}); err != nil {
		t.Fatal(err)
	}
	body, _ := getJSON(alice, "/api/settings/history-retention")
	if !contains(string(body), `"days":30`) {
		t.Fatalf("not persisted: %s", body)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && stringIndex(haystack, needle) >= 0
}

func stringIndex(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
