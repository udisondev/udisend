package httpui

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
)

const (
	settingKeyLogLevel       = "log.level"
	settingKeyHistoryRetainD = "history.retain_days"
)

func (s *Server) handleLogLevel(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		level := "info"
		if v, ok, _ := s.mngr.Storage().GetSetting(r.Context(), settingKeyLogLevel); ok {
			level = v
		} else if s.logLevel != nil {
			level = formatLevel(s.logLevel.Level())
		}
		writeJSON(w, http.StatusOK, map[string]any{"level": level})
		return
	case http.MethodPost:
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var req struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	lvl, err := parseLevel(req.Level)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.logLevel != nil {
		s.logLevel.Set(lvl)
	}
	if err := s.mngr.Storage().SetSetting(r.Context(), settingKeyLogLevel, formatLevel(lvl)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"level": formatLevel(lvl)})
}

func (s *Server) handleHistoryRetention(w http.ResponseWriter, r *http.Request) {
	store := s.mngr.Storage()
	switch r.Method {
	case http.MethodGet:
		days := 0
		if v, ok, _ := store.GetSetting(r.Context(), settingKeyHistoryRetainD); ok {
			if n, err := strconv.Atoi(v); err == nil {
				days = n
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"days": days})
		return
	case http.MethodPost:
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var req struct {
		Days int `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Days < 0 || req.Days > 3650 {
		http.Error(w, "days must be 0..3650", http.StatusBadRequest)
		return
	}
	if err := store.SetSetting(r.Context(), settingKeyHistoryRetainD, strconv.Itoa(req.Days)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": req.Days})
}

func parseLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}

	return 0, fmt.Errorf("unknown level %q", s)
}

func formatLevel(l slog.Level) string {
	switch {
	case l <= slog.LevelDebug:
		return "debug"
	case l <= slog.LevelInfo:
		return "info"
	case l <= slog.LevelWarn:
		return "warn"
	default:
		return "error"
	}
}
