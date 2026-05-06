package httpui

import (
	"net/http/httptest"
	"testing"
)

// TestHandleStatic_NoStoreHeader guards against stale-asset XSS: when an
// operator upgrades the messenger binary, browsers must NOT serve the
// old index.html / app.js out of cache. Without `Cache-Control: no-store`
// the SPA bundle can pin to a vulnerable version against the operator's
// intent — the binary advertises no versioned URL the cache can key on.
func TestHandleStatic_NoStoreHeader(t *testing.T) {
	t.Parallel()

	srv := &Server{}
	for _, path := range []string{"/", "/index.html", "/app.js", "/style.css"} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		srv.handleStatic(rec, req)

		got := rec.Header().Get("Cache-Control")
		if got != "no-store" {
			t.Errorf("path %q: Cache-Control = %q, want %q", path, got, "no-store")
		}
	}
}
