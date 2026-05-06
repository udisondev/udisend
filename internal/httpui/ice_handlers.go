package httpui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/internal/storage"
)

// settingKeyDisableICEFallback is the app_settings row toggling the
// "fall back to public Google STUN if no other server is reachable"
// path in the snapshot endpoint.
const settingKeyDisableICEFallback = "ice.disable_default_fallback"

// maxICEEntries caps user-managed ICE servers per instance. Browsers
// honour at most 5 entries usefully; we leave room for a couple of
// experiments without making the panel scroll forever.
const maxICEEntries = 16

// iceEntry is one row in the unified Settings → Network → ICE list.
type iceEntry struct {
	URL      string `json:"url"`
	Source   string `json:"source"` // "manual" | "discovered"
	Enabled  bool   `json:"enabled"`
	Username string `json:"username,omitempty"`
}

type iceListResp struct {
	Entries                 []iceEntry `json:"entries"`
	DisableDefaultFallback  bool       `json:"disable_default_fallback"`
}

func (s *Server) handleICEList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	resp, err := s.buildICEResponse(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleICEAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sensitiveBodyMaxBytes)
	var req struct {
		URL        string `json:"url"`
		Username   string `json:"username"`
		Credential string `json:"credential"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	url, err := normalizeICEURL(req.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	store := s.mngr.Storage()
	existing, err := store.ListICEOverrides(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	exists := false
	for _, e := range existing {
		if e.URL == url {
			exists = true
			break
		}
	}
	if !exists && len(existing) >= maxICEEntries {
		http.Error(w, fmt.Sprintf("at most %d ICE servers allowed", maxICEEntries), http.StatusBadRequest)
		return
	}

	if err := store.AddICEOverride(r.Context(), storage.ICEOverride{
		URL:        url,
		Username:   strings.TrimSpace(req.Username),
		Credential: req.Credential,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditIfPublic(r, "ice_add", url)

	resp, err := s.buildICEResponse(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleICERemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sensitiveBodyMaxBytes)
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	url, err := normalizeICEURL(req.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.mngr.Storage().RemoveICEOverride(r.Context(), url); err != nil {
		if errors.Is(err, storage.ErrICEOverrideNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditIfPublic(r, "ice_remove", url)
	resp, err := s.buildICEResponse(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleICEToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sensitiveBodyMaxBytes)
	var req struct {
		URL     string `json:"url"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	url, err := normalizeICEURL(req.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.mngr.Storage().SetICEOverrideEnabled(r.Context(), url, req.Enabled); err != nil {
		if errors.Is(err, storage.ErrICEOverrideNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := s.buildICEResponse(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleICEFallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sensitiveBodyMaxBytes)
	var req struct {
		Disabled bool `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	value := "0"
	if req.Disabled {
		value = "1"
	}
	if err := s.mngr.Storage().SetSetting(r.Context(), settingKeyDisableICEFallback, value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := s.buildICEResponse(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) buildICEResponse(ctx context.Context) (iceListResp, error) {
	overrides, err := s.mngr.Storage().ListICEOverrides(ctx)
	if err != nil {
		return iceListResp{}, err
	}
	entries := make([]iceEntry, 0, len(overrides))
	for _, e := range overrides {
		entries = append(entries, iceEntry{
			URL:      e.URL,
			Source:   "manual",
			Enabled:  e.Enabled,
			Username: e.Username,
		})
	}

	disableRaw, _, err := s.mngr.Storage().GetSetting(ctx, settingKeyDisableICEFallback)
	if err != nil {
		return iceListResp{}, err
	}

	return iceListResp{Entries: entries, DisableDefaultFallback: disableRaw == "1"}, nil
}

// EffectiveICEServers builds the snapshot's ice_servers field. It merges
// user-managed overrides with peers auto-discovered through the network
// layer; duplicates (by URL) are dropped on second occurrence. Order:
// manual entries first (the user's preference wins), then discovered.
//
// When the operator has set Settings → Network → "Don't fall back to
// the public Google STUN server", the auto-discovered slice is also
// dropped so only user-curated entries reach the browser. Manual
// entries are always honoured — the toggle controls fallback only.
func (s *Server) EffectiveICEServers(ctx context.Context) []messenger.ICEServer {
	out := make([]messenger.ICEServer, 0, 8)
	seen := make(map[string]struct{})

	if enabled, err := s.mngr.Storage().EnabledICEOverrides(ctx); err == nil {
		for _, e := range enabled {
			if _, dup := seen[e.URL]; dup {
				continue
			}
			seen[e.URL] = struct{}{}
			ice := messenger.ICEServer{URLs: []string{e.URL}}
			if e.Username != "" {
				ice.Username = e.Username
				ice.Credential = e.Credential
			}
			out = append(out, ice)
		}
	}

	disable, _, _ := s.mngr.Storage().GetSetting(ctx, settingKeyDisableICEFallback)
	if disable == "1" {
		return out
	}

	for _, ice := range s.mngr.ICEServers(ctx) {
		key := ""
		if len(ice.URLs) > 0 {
			key = ice.URLs[0]
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ice)
	}

	return out
}

// normalizeICEURL accepts and trims `stun:host:port`, `stuns:host:port`,
// `turn:host:port[?transport=...]`, `turns:host:port`. Returns the
// canonical form with the scheme lower-cased so duplicates ("Stun:..."
// vs "stun:...") collapse to a single PRIMARY KEY in storage. We don't
// resolve DNS — entered hosts may be local-only.
func normalizeICEURL(in string) (string, error) {
	raw := strings.TrimSpace(in)
	if raw == "" {
		return "", errors.New("url required")
	}
	if len(raw) > 255 {
		return "", errors.New("url too long")
	}
	colon := strings.Index(raw, ":")
	if colon <= 0 {
		return "", errors.New("url must start with stun:, stuns:, turn: or turns:")
	}
	scheme := strings.ToLower(raw[:colon])
	switch scheme {
	case "stun", "stuns", "turn", "turns":
	default:
		return "", errors.New("url must start with stun:, stuns:, turn: or turns:")
	}
	rest := raw[colon+1:]
	if rest == "" || strings.Contains(rest, " ") {
		return "", errors.New("invalid host:port")
	}

	return scheme + ":" + rest, nil
}
