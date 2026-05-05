package httpui

import (
	"net/http"
	"os"
)

func (s *Server) handleStorageUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	store := s.mngr.Storage()
	mc, err := store.CountMessages(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cc, err := store.CountContacts(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ob, err := store.CountOutbox(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mb, err := store.MessageBytes(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	dbBytes := int64(0)
	if s.dbPath != "" {
		if fi, err := os.Stat(s.dbPath); err == nil {
			dbBytes = fi.Size()
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"db_bytes":      dbBytes,
		"messages":      mc,
		"contacts":      cc,
		"outbox":        ob,
		"message_bytes": mb,
	})
}

func (s *Server) handleStorageVacuum(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	before := int64(0)
	if s.dbPath != "" {
		if fi, err := os.Stat(s.dbPath); err == nil {
			before = fi.Size()
		}
	}
	if err := s.mngr.Storage().Vacuum(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	after := int64(0)
	if s.dbPath != "" {
		if fi, err := os.Stat(s.dbPath); err == nil {
			after = fi.Size()
		}
	}
	reclaimed := before - after
	if reclaimed < 0 {
		reclaimed = 0
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"db_bytes_before": before,
		"db_bytes_after":  after,
		"reclaimed_bytes": reclaimed,
	})
}
