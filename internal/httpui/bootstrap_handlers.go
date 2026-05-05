package httpui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/udisondev/udisend/internal/storage"
)

// bootstrap-handler limits.
const (
	maxBootstrapOverrides   = 50
	maxBootstrapNoteRunes   = 80
	maxBootstrapAddressLen  = 255
	bootstrapDialTimeout    = 6 * time.Second
	bootstrapReconnectLimit = 25
)

// bootstrapEntry is one row in the unified Settings → Bootstrap view.
type bootstrapEntry struct {
	Address      string `json:"address"`
	Source       string `json:"source"` // "manual" | "cache" | "default"
	Enabled      bool   `json:"enabled"`
	Note         string `json:"note,omitempty"`
	LastStatus   string `json:"last_status,omitempty"`   // "ok" | "fail" | ""
	LastStatusAt int64  `json:"last_status_at,omitempty"` // unix seconds, 0 = never
	AddedAt      int64  `json:"added_at,omitempty"`      // unix seconds (manual only)
}

type bootstrapListResponse struct {
	Entries []bootstrapEntry `json:"entries"`
}

func (s *Server) handleBootstrapList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	store := s.mngr.Storage()
	entries, err := buildBootstrapEntries(r.Context(), store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, bootstrapListResponse{Entries: entries})
}

func (s *Server) handleBootstrapAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Address string `json:"address"`
		Note    string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	addr, err := normalizeBootstrapAddress(req.Address)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(req.Note)
	if rs := []rune(note); len(rs) > maxBootstrapNoteRunes {
		note = string(rs[:maxBootstrapNoteRunes])
	}

	store := s.mngr.Storage()
	exists, err := store.BootstrapOverrideExists(r.Context(), addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !exists {
		all, err := store.ListBootstrapOverrides(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(all) >= maxBootstrapOverrides {
			http.Error(w, fmt.Sprintf("at most %d bootstrap entries allowed", maxBootstrapOverrides), http.StatusBadRequest)
			return
		}
	}
	if err := store.AddBootstrapOverride(r.Context(), addr, note); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Best-effort sync dial so the user gets an immediate ok/fail badge.
	dialCtx, cancel := context.WithTimeout(r.Context(), bootstrapDialTimeout)
	defer cancel()
	dialErr := s.mngr.BootstrapPeer(dialCtx, addr)

	entries, listErr := buildBootstrapEntries(r.Context(), store)
	if listErr != nil {
		http.Error(w, listErr.Error(), http.StatusInternalServerError)
		return
	}
	resp := struct {
		Entries []bootstrapEntry `json:"entries"`
		DialErr string           `json:"dial_error,omitempty"`
	}{Entries: entries}
	if dialErr != nil {
		resp.DialErr = dialErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBootstrapRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Address string `json:"address"`
		// Source is optional — clients pass "manual" / "cache" so we can
		// pick the right deletion path without re-resolving server-side.
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	addr := strings.TrimSpace(req.Address)
	if addr == "" {
		http.Error(w, "address required", http.StatusBadRequest)
		return
	}
	store := s.mngr.Storage()
	switch req.Source {
	case "cache":
		if err := store.ForgetSeenPeer(r.Context(), addr); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case "", "manual":
		if err := store.RemoveBootstrapOverride(r.Context(), addr); err != nil {
			if errors.Is(err, storage.ErrBootstrapOverrideNotFound) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case "default":
		http.Error(w, "default entries are read-only", http.StatusBadRequest)
		return
	default:
		http.Error(w, "unknown source", http.StatusBadRequest)
		return
	}

	entries, err := buildBootstrapEntries(r.Context(), store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, bootstrapListResponse{Entries: entries})
}

func (s *Server) handleBootstrapToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Address string `json:"address"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	addr := strings.TrimSpace(req.Address)
	if addr == "" {
		http.Error(w, "address required", http.StatusBadRequest)
		return
	}
	store := s.mngr.Storage()
	if err := store.SetBootstrapOverrideEnabled(r.Context(), addr, req.Enabled); err != nil {
		if errors.Is(err, storage.ErrBootstrapOverrideNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	entries, err := buildBootstrapEntries(r.Context(), store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, bootstrapListResponse{Entries: entries})
}

// handleBootstrapReconnect dials every enabled manual entry concurrently
// and returns each address's outcome. Caller's UI uses it both as an
// explicit "reconnect" button and after edits, so the badges refresh.
func (s *Server) handleBootstrapReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	store := s.mngr.Storage()
	enabled, err := store.EnabledBootstrapOverrides(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(enabled) > bootstrapReconnectLimit {
		enabled = enabled[:bootstrapReconnectLimit]
	}

	type result struct {
		Address string `json:"address"`
		Status  string `json:"status"` // "ok" | "fail"
		Error   string `json:"error,omitempty"`
	}
	results := make([]result, len(enabled))

	dialCtx, cancel := context.WithTimeout(r.Context(), bootstrapDialTimeout+2*time.Second)
	defer cancel()

	done := make(chan struct{}, len(enabled))
	for i, addr := range enabled {
		go func(i int, addr string) {
			defer func() { done <- struct{}{} }()
			err := s.mngr.BootstrapPeer(dialCtx, addr)
			results[i].Address = addr
			if err != nil {
				results[i].Status = "fail"
				results[i].Error = err.Error()
				return
			}
			results[i].Status = "ok"
		}(i, addr)
	}
	for range enabled {
		<-done
	}

	entries, err := buildBootstrapEntries(r.Context(), store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Entries []bootstrapEntry `json:"entries"`
		Results []result         `json:"results"`
	}{Entries: entries, Results: results})
}

// buildBootstrapEntries is the shared composition used by every handler:
// manual overrides first (most-recent first), then cache, then defaults
// — minus duplicates already covered by an earlier source.
func buildBootstrapEntries(ctx context.Context, store *storage.Store) ([]bootstrapEntry, error) {
	overrides, err := store.ListBootstrapOverrides(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(overrides))
	out := make([]bootstrapEntry, 0, len(overrides))
	for _, o := range overrides {
		seen[o.Address] = struct{}{}
		entry := bootstrapEntry{
			Address:    o.Address,
			Source:     "manual",
			Enabled:    o.Enabled,
			Note:       o.Note,
			LastStatus: o.LastStatus,
			AddedAt:    o.AddedAt.Unix(),
		}
		if !o.LastStatusAt.IsZero() {
			entry.LastStatusAt = o.LastStatusAt.Unix()
		}
		out = append(out, entry)
	}

	cached, err := store.SeenPeers(ctx, 0)
	if err != nil {
		return nil, err
	}
	for _, addr := range cached {
		if _, dup := seen[addr]; dup {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, bootstrapEntry{
			Address: addr,
			Source:  "cache",
			Enabled: true,
		})
	}

	return out, nil
}

// normalizeBootstrapAddress validates and canonicalises a host:port pair.
// We intentionally do NOT resolve DNS here — host strings are accepted
// as-is so the user can paste a relay name without immediate failure on
// a flaky DNS path. Port must be 1..65535. IPv6 hosts must use bracket
// notation per RFC 3986.
func normalizeBootstrapAddress(in string) (string, error) {
	addr := strings.TrimSpace(in)
	if addr == "" {
		return "", errors.New("address required")
	}
	if len(addr) > maxBootstrapAddressLen {
		return "", fmt.Errorf("address too long (max %d)", maxBootstrapAddressLen)
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid host:port: %w", err)
	}
	if host == "" {
		return "", errors.New("host required")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("port must be 1..65535")
	}

	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}
