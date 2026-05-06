package httpui

import (
	"net/http"
)

func (s *Server) handleNetworkStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	noStoreHeaders(w)
	stats := s.mngr.NetworkStats()
	cached, err := s.mngr.Storage().SeenPeers(r.Context(), 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := s.mngr.Identity().Public()
	writeJSON(w, http.StatusOK, map[string]any{
		"identity_hash":      id.DestinationHash().String(),
		"local_address":      s.mngr.LocalAddress(),
		"routing_table_size": stats.RoutingTableSize,
		"active_sessions":    stats.ActiveSessions,
		"seen_peers":         len(cached),
		"public_mode":        s.publicMode,
	})
}
