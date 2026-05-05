// Package httpui exposes the messenger runtime over a localhost HTTP +
// WebSocket interface so a browser can drive the chat UI. The browser
// owns WebRTC (RTCPeerConnection, getUserMedia, <video>); Go owns
// identity, presence, signaling transport and storage. Bridge protocol
// is intentionally tiny — JSON messages on a single WS endpoint plus a
// handful of REST endpoints for snapshots.
package httpui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/udisondev/udisend/internal/httpui/auth"
	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/identity"
)

// DefaultListen is the loopback bind address.
const DefaultListen = "127.0.0.1:0"

// Server hosts the HTTP+WS API and the embedded static UI assets.
type Server struct {
	mngr      *messenger.Messenger
	listener  net.Listener
	server    *http.Server
	logger    *slog.Logger
	authToken string

	// publicMode indicates non-loopback bind with passphrase-backed
	// authentication. Set by NewServer when Config.Public is true.
	publicMode bool
	publicHost string
	tlsCert    string
	tlsKey     string
	// hasTLS is true when this server terminates TLS in-process — either
	// via cfg.TLSCert/TLSKey files or via cfg.TLSConfig (autocert). Used
	// to gate HSTS, which we MUST NOT send over plaintext lest browsers
	// pin a host that has no working HTTPS endpoint.
	hasTLS bool
	authH  *authHandlers

	// logLevel is a runtime-mutable level the Settings → Notifications
	// panel can flip to enable verbose logs without restart. nil disables
	// the toggle but the GET handler still works (returns the persisted
	// preference).
	logLevel *slog.LevelVar
	// dbPath is the filesystem path of the underlying SQLite database.
	// Surfaced by the Settings → Privacy → Storage panel for usage
	// reporting and vacuum reclaim metrics.
	dbPath string

	wsMu    sync.Mutex
	wsConns map[*sseClient]struct{}

	closeOnce sync.Once
	closed    chan struct{}
}

// Config configures NewServer.
type Config struct {
	Messenger *messenger.Messenger
	Listen    string
	Logger    *slog.Logger
	// AuthToken, if empty, is auto-generated. The token must accompany every
	// request (in the `?token=` query param for the WS handshake, in the
	// `Authorization: Bearer` header for REST). Only meaningful in
	// loopback mode; ignored when Public is true.
	AuthToken string

	// Public switches the server into "hosted" mode: requires an existing
	// passphrase in storage, mounts /login + /logout, replaces the URL-token
	// flow with cookie-bound sessions, and serves the SPA only after auth.
	// Caller is responsible for verifying that bind address is non-loopback
	// AND that TLS is provided (either inline or via reverse proxy).
	Public bool

	// PublicHost is the externally-visible hostname (e.g.
	// "messenger.example.com") used when constructing URL(). Only honored
	// in public mode; falls back to listen address.
	PublicHost string

	// TrustProxy honors X-Forwarded-For for client-IP attribution and sets
	// the Secure flag on session cookies. Only meaningful in public mode.
	TrustProxy bool

	// AllowOpaqueOrigin tolerates `Origin: null` on POST /login. Required
	// in lan-ip mode where browsers downgrade to opaque origin after a
	// self-signed cert override; left false elsewhere so `null` Origin
	// remains a CSRF signal.
	AllowOpaqueOrigin bool

	// TLSCert and TLSKey, when both non-empty, switch Run() to ServeTLS.
	// Mutually exclusive with TrustProxy in practice (you either terminate
	// TLS in-process or behind a proxy), though both being set is harmless
	// — TLS wins.
	TLSCert string
	TLSKey  string

	// LogLevel, when non-nil, is the *slog.LevelVar driving the global
	// log handler. The webui Settings → Notifications panel calls Set on
	// it to flip verbose-logs at runtime. Leave nil if the host process
	// uses a static level — the UI will still render, but the toggle is
	// effectively a no-op.
	LogLevel *slog.LevelVar
	// DBPath is the absolute path to the SQLite file backing Storage.
	// Used by the Storage usage panel to compute file size and
	// vacuum-reclaimed bytes. Empty disables the size column.
	DBPath string

	// TLSConfig, when non-nil, supplies the TLS configuration for
	// ServeTLS. Mutually exclusive with TLSCert/TLSKey. Used by callers
	// that manage certificates externally (e.g. autocert/Let's Encrypt
	// plumbing in cmd/messenger). The caller is responsible for choosing
	// safe MinVersion / curves; httpui will not override them.
	TLSConfig *tls.Config
}

// NewServer constructs the HTTP UI without starting it.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Messenger == nil {
		return nil, errors.New("httpui: messenger required")
	}
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.AuthToken == "" {
		cfg.AuthToken = randomToken()
	}
	if err := validatePublicModeConfig(cfg); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("httpui: listen: %w", err)
	}
	s := &Server{
		mngr:       cfg.Messenger,
		listener:   ln,
		logger:     cfg.Logger,
		authToken:  cfg.AuthToken,
		publicMode: cfg.Public,
		publicHost: cfg.PublicHost,
		tlsCert:    cfg.TLSCert,
		tlsKey:     cfg.TLSKey,
		hasTLS:     (cfg.TLSCert != "" && cfg.TLSKey != "") || cfg.TLSConfig != nil,
		logLevel:   cfg.LogLevel,
		dbPath:     cfg.DBPath,
		wsConns:    make(map[*sseClient]struct{}),
		closed:     make(chan struct{}),
	}

	if cfg.Public {
		store := cfg.Messenger.Storage()
		if store == nil {
			_ = ln.Close()

			return nil, errors.New("httpui: public mode requires messenger with storage")
		}
		s.authH = &authHandlers{
			store:    store,
			sessions: auth.NewSessions(store, auth.SessionsConfig{}),
			limiter: &auth.RateLimiter{
				PerMinute:     5,
				FailsToLock:   20,
				LockDuration:  15 * time.Minute,
				MaxTrackedIPs: 50000,
			},
			secureCookie:      true,
			trustProxy:        cfg.TrustProxy,
			allowOpaqueOrigin: cfg.AllowOpaqueOrigin,
			now:               time.Now,
		}
	}

	mux := http.NewServeMux()
	// Read endpoints — auth is enough; no CSRF check (GET cannot be a CSRF
	// vector for state-changing actions).
	mux.HandleFunc("/api/events", s.requireAuth(s.handleEvents))
	mux.HandleFunc("/api/snapshot", s.requireAuth(s.handleSnapshot))
	mux.HandleFunc("/api/history", s.requireAuth(s.handleHistory))

	// State-changing POST endpoints — require both auth AND the
	// X-Requested-With: udisend custom header. Browsers cannot attach
	// custom headers to a cross-origin form POST without a CORS preflight,
	// and we never grant CORS — so the header's mere presence proves the
	// request originated from same-origin JavaScript.
	mux.HandleFunc("/api/contacts/add", s.requireAuth(s.requireCSRFHeader(s.handleContactAdd)))
	mux.HandleFunc("/api/contacts/verify", s.requireAuth(s.requireCSRFHeader(s.handleContactVerify)))
	mux.HandleFunc("/api/contacts/rename", s.requireAuth(s.requireCSRFHeader(s.handleContactRename)))
	mux.HandleFunc("/api/contacts/delete", s.requireAuth(s.requireCSRFHeader(s.handleContactDelete)))
	mux.HandleFunc("/api/bootstrap", s.requireAuth(s.handleBootstrapList))
	mux.HandleFunc("/api/bootstrap/add", s.requireAuth(s.requireCSRFHeader(s.handleBootstrapAdd)))
	mux.HandleFunc("/api/bootstrap/remove", s.requireAuth(s.requireCSRFHeader(s.handleBootstrapRemove)))
	mux.HandleFunc("/api/bootstrap/toggle", s.requireAuth(s.requireCSRFHeader(s.handleBootstrapToggle)))
	mux.HandleFunc("/api/bootstrap/reconnect", s.requireAuth(s.requireCSRFHeader(s.handleBootstrapReconnect)))

	mux.HandleFunc("/api/auth/state", s.requireAuth(s.handleAuthState))
	mux.HandleFunc("/api/auth/change-passphrase", s.requireAuth(s.requireCSRFHeader(s.handleAuthChangePassphrase)))
	mux.HandleFunc("/api/auth/totp/start", s.requireAuth(s.requireCSRFHeader(s.handleTOTPStart)))
	mux.HandleFunc("/api/auth/totp/finish", s.requireAuth(s.requireCSRFHeader(s.handleTOTPFinish)))
	mux.HandleFunc("/api/auth/totp/disable", s.requireAuth(s.requireCSRFHeader(s.handleTOTPDisable)))
	mux.HandleFunc("/api/auth/recovery/regenerate", s.requireAuth(s.requireCSRFHeader(s.handleRecoveryRegenerate)))
	mux.HandleFunc("/api/auth/sessions", s.requireAuth(s.handleSessionsList))
	mux.HandleFunc("/api/auth/sessions/revoke", s.requireAuth(s.requireCSRFHeader(s.handleSessionRevoke)))
	mux.HandleFunc("/api/auth/log", s.requireAuth(s.handleAuthLog))

	mux.HandleFunc("/api/ice", s.requireAuth(s.handleICEList))
	mux.HandleFunc("/api/ice/add", s.requireAuth(s.requireCSRFHeader(s.handleICEAdd)))
	mux.HandleFunc("/api/ice/remove", s.requireAuth(s.requireCSRFHeader(s.handleICERemove)))
	mux.HandleFunc("/api/ice/toggle", s.requireAuth(s.requireCSRFHeader(s.handleICEToggle)))
	mux.HandleFunc("/api/ice/fallback", s.requireAuth(s.requireCSRFHeader(s.handleICEFallback)))

	mux.HandleFunc("/api/network/status", s.requireAuth(s.handleNetworkStatus))

	mux.HandleFunc("/api/settings/log-level", s.requireAuth(s.requireCSRFHeader(s.handleLogLevel)))
	mux.HandleFunc("/api/settings/history-retention", s.requireAuth(s.requireCSRFHeader(s.handleHistoryRetention)))

	mux.HandleFunc("/api/identity/export", s.requireAuth(s.requireCSRFHeader(s.handleIdentityExport)))

	mux.HandleFunc("/api/storage/usage", s.requireAuth(s.handleStorageUsage))
	mux.HandleFunc("/api/storage/vacuum", s.requireAuth(s.requireCSRFHeader(s.handleStorageVacuum)))
	mux.HandleFunc("/api/append-history", s.requireAuth(s.requireCSRFHeader(s.handleAppendHistory)))
	mux.HandleFunc("/api/session/open", s.requireAuth(s.requireCSRFHeader(s.handleSessionOpen)))
	mux.HandleFunc("/api/session/close", s.requireAuth(s.requireCSRFHeader(s.handleSessionClose)))
	mux.HandleFunc("/api/signal/send", s.requireAuth(s.requireCSRFHeader(s.handleSignalSend)))

	if cfg.Public {
		mux.HandleFunc("GET /login", s.authH.handleLoginGET)
		mux.HandleFunc("POST /login", s.authH.handleLoginPOST)
		// Logout is a state-changing POST: same CSRF guard as the API.
		// SameSite=Strict alone would suffice for modern browsers, but the
		// custom-header check costs nothing and removes the assumption.
		mux.HandleFunc("POST /logout", s.requireCSRFHeader(s.authH.handleLogout))
		mux.HandleFunc("/", s.publicAwareStatic)
	} else {
		mux.HandleFunc("/", s.handleStatic)
	}

	s.server = &http.Server{
		Handler: s.checkHost(s.secureHeaders(mux)),
		// Slowloris defence + idle bound. WriteTimeout is intentionally
		// zero because /api/events is a long-lived SSE stream — set it
		// and the stream gets cut on the first keepalive.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		s.server.TLSConfig = hardenedTLSConfig()
	}
	if cfg.TLSConfig != nil {
		s.server.TLSConfig = cfg.TLSConfig
	}
	cfg.Messenger.SetIncomingHandler(s.onIncomingSession)
	cfg.Messenger.SetPeerOnlineHandler(s.onPeerOnline)

	return s, nil
}

// publicAwareStatic enforces session-cookie auth for the SPA in public
// mode. Unauthenticated visitors are redirected to /login (browser-friendly,
// unlike the API surface which returns 401).
func (s *Server) publicAwareStatic(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(cookieNameSession)
	if err != nil {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	sess, err := s.authH.sessions.Validate(r.Context(), c.Value)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if sess == nil {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}

	s.handleStatic(w, r)
}

// onPeerOnline broadcasts a peer-online envelope to every connected SSE
// client so the UI can drain pending outbox items for that peer.
func (s *Server) onPeerOnline(peer identity.Hash) {
	env := envelope{Type: "peer_online", Peer: peer.String()}
	s.wsMu.Lock()
	clients := make([]*sseClient, 0, len(s.wsConns))
	for c := range s.wsConns {
		clients = append(clients, c)
	}
	s.wsMu.Unlock()
	for _, c := range clients {
		c.send(env)
	}
}

// URL returns the user-friendly URL for opening in a browser. In loopback
// mode the URL embeds the auth token. In public mode the URL is
// `https://<PublicHost>/login` — `PublicHost` is required (enforced by
// resolvePublicMode), so the bind port is irrelevant: the browser hits
// the upstream proxy or the in-process TLS bind by name.
func (s *Server) URL() string {
	if s.publicMode {
		return fmt.Sprintf("https://%s/login", s.publicHost)
	}
	addr := s.listener.Addr().(*net.TCPAddr)

	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", addr.Port, s.authToken)
}

// AuthToken returns the random token required for every API call.
func (s *Server) AuthToken() string { return s.authToken }

// LocalAddress is the bound TCP address (e.g. "127.0.0.1:54321").
func (s *Server) LocalAddress() string { return s.listener.Addr().String() }

// Run starts serving and blocks until ctx is cancelled or Serve fails.
// When TLSCert and TLSKey are set, ServeTLS is used; otherwise plain HTTP
// (loopback flow, or public mode behind a TLS-terminating proxy).
func (s *Server) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
	}()

	if s.publicMode {
		go s.runAuthMaintenance(ctx)
	}

	var err error
	switch {
	case s.tlsCert != "" && s.tlsKey != "":
		err = s.server.ServeTLS(s.listener, s.tlsCert, s.tlsKey)
	case s.server.TLSConfig != nil:
		// Caller provided a complete TLSConfig (e.g. autocert.Manager.TLSConfig);
		// ServeTLS picks up the certificate via TLSConfig.GetCertificate.
		err = s.server.ServeTLS(s.listener, "", "")
	default:
		err = s.server.Serve(s.listener)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}

// runAuthMaintenance periodically prunes idle sessions, expired rate-limit
// state and the audit log. Once-per-hour cadence — cheap, and the bounds
// (idle TTL, log retention) are coarse enough that finer ticking would be
// pointless. Exits on ctx cancel.
func (s *Server) runAuthMaintenance(ctx context.Context) {
	const auditRetention = 1000
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	prune := func() {
		pruneCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if n, err := s.authH.sessions.Prune(pruneCtx); err != nil {
			s.logger.Warn("auth: prune sessions", "err", err)
		} else if n > 0 {
			s.logger.Debug("auth: pruned sessions", "n", n)
		}
		if n, err := s.authH.store.PruneAuthLog(pruneCtx, auditRetention); err != nil {
			s.logger.Warn("auth: prune log", "err", err)
		} else if n > 0 {
			s.logger.Debug("auth: pruned log rows", "n", n)
		}
		s.authH.limiter.Cleanup()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

// Close stops the server immediately.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.server.Close()
	})
}

// requireAuth gates a handler behind the active authentication mode. In
// loopback mode it accepts the URL/Bearer/cookie token; in public mode it
// accepts a valid session cookie. Mismatches always yield 401 — the SPA
// reacts by redirecting to /login.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	if s.publicMode {
		return func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(cookieNameSession)
			if err != nil {
				http.Error(w, "auth required", http.StatusUnauthorized)
				return
			}
			sess, err := s.authH.sessions.Validate(r.Context(), c.Value)
			if err != nil {
				http.Error(w, "internal", http.StatusInternalServerError)
				return
			}
			if sess == nil {
				http.Error(w, "auth required", http.StatusUnauthorized)
				return
			}

			next(w, r)
		}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if !s.checkLoopbackToken(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

// CSRF header constants. The header name is conventional ("X-Requested-With"
// is what jQuery/Axios set by default); the value is project-specific so
// generic libraries cannot accidentally satisfy it.
const (
	csrfHeaderName  = "X-Requested-With"
	csrfHeaderValue = "udisend"
)

// requireCSRFHeader rejects any non-safe request that lacks the project's
// custom header. Composes with requireAuth: stack as
// requireAuth(requireCSRFHeader(handler)).
func (s *Server) requireCSRFHeader(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if r.Header.Get(csrfHeaderName) != csrfHeaderValue {
				http.Error(w, "missing CSRF header", http.StatusForbidden)
				return
			}
		}

		next(w, r)
	}
}

func (s *Server) checkLoopbackToken(r *http.Request) bool {
	if t := r.URL.Query().Get("token"); t != "" && tokenEqual(t, s.authToken) {
		return true
	}

	authHdr := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(authHdr, "Bearer "); ok && tokenEqual(t, s.authToken) {
		return true
	}

	if c, err := r.Cookie("udisend_token"); err == nil && tokenEqual(c.Value, s.authToken) {
		return true
	}

	return false
}

// tokenEqual is constant-time equality for HTTP-bridge auth tokens. The
// token is high-entropy so a length-mismatch leak is harmless, but the
// constant-time comparison removes a class of "this is fine on
// loopback, embarrassing the day someone exposes it" remarks.
func tokenEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// generalBodyMaxBytes caps every JSON-decoded POST body that does not
// front Argon2id (which has its own tighter cap). 64 KiB is generous
// for any current handler — contact aliases, bootstrap addresses, ICE
// URLs etc. all live in tens of bytes.
const generalBodyMaxBytes = 64 * 1024

// signalBodyMaxBytes caps WebRTC SDP/ICE relay payloads. SDP offers run
// to a few KiB even with many candidates; 256 KiB is the upper bound
// above which the payload is almost certainly malicious.
const signalBodyMaxBytes = 256 * 1024

// historyBodyMaxBytes bounds /api/append-history. The browser owns chat
// framing; legitimate text + base64 metadata fits comfortably below
// this cap. Larger payloads should arrive via DataChannel file flows,
// not the persistence endpoint.
const historyBodyMaxBytes = 1 * 1024 * 1024

// handleSnapshot returns identity + contacts + recent history per peer.
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	noStoreHeaders(w)
	type contactView struct {
		Hash        string `json:"hash"`
		Alias       string `json:"alias"`
		Fingerprint string `json:"fingerprint"`
		Verified    bool   `json:"verified"`
	}
	type identityView struct {
		Hash        string `json:"hash"`
		Fingerprint string `json:"fingerprint"`
		Address     string `json:"address"`
	}
	contacts, err := s.mngr.Contacts(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cv := make([]contactView, 0, len(contacts))
	for _, c := range contacts {
		cv = append(cv, contactView{
			Hash:        c.Hash.String(),
			Alias:       c.Alias,
			Fingerprint: c.Fingerprint,
			Verified:    c.Verified,
		})
	}
	iceCtx, iceCancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer iceCancel()
	ice := s.EffectiveICEServers(iceCtx)
	authMode := "loopback"
	if s.publicMode {
		authMode = "public"
	}
	resp := struct {
		Identity   identityView          `json:"identity"`
		Contacts   []contactView         `json:"contacts"`
		ICEServers []messenger.ICEServer `json:"ice_servers"`
		AuthMode   string                `json:"auth_mode"`
	}{
		Identity: identityView{
			Hash:        s.mngr.Identity().Public().DestinationHash().String(),
			Fingerprint: s.mngr.Identity().Public().Fingerprint(),
			Address:     s.mngr.LocalAddress(),
		},
		Contacts:   cv,
		ICEServers: ice,
		AuthMode:   authMode,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleContactAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, generalBodyMaxBytes)
	var req struct {
		Hash  string `json:"hash"`
		Alias string `json:"alias"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h, err := identity.ParseHash(strings.TrimSpace(req.Hash))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	addCtx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := s.mngr.AddContact(addCtx, h, strings.TrimSpace(req.Alias)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleContactVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, generalBodyMaxBytes)
	var req struct {
		Hash     string `json:"hash"`
		Verified bool   `json:"verified"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h, err := identity.ParseHash(strings.TrimSpace(req.Hash))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.mngr.VerifyContact(r.Context(), h, req.Verified); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleContactRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, generalBodyMaxBytes)
	var req struct {
		Hash  string `json:"hash"`
		Alias string `json:"alias"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h, err := identity.ParseHash(strings.TrimSpace(req.Hash))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.mngr.RenameContact(r.Context(), h, strings.TrimSpace(req.Alias)); err != nil {
		if errors.Is(err, storage.ErrContactNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleContactDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, generalBodyMaxBytes)
	var req struct {
		Hash        string `json:"hash"`
		WipeHistory bool   `json:"wipe_history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h, err := identity.ParseHash(strings.TrimSpace(req.Hash))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.mngr.RemoveContact(r.Context(), h, messenger.RemoveContactOptions{WipeHistory: req.WipeHistory}); err != nil {
		if errors.Is(err, storage.ErrContactNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	noStoreHeaders(w)
	hashStr := r.URL.Query().Get("peer")
	h, err := identity.ParseHash(hashStr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	limit := 200
	hist, err := s.mngr.History(r.Context(), h, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type entryView struct {
		ID        int64  `json:"id"`
		Direction string `json:"direction"`
		Body      string `json:"body"`
		BodyB64   string `json:"body_b64"`
		Kind      int    `json:"kind"`
		Timestamp int64  `json:"timestamp"`
	}
	out := make([]entryView, 0, len(hist))
	for _, e := range hist {
		out = append(out, entryView{
			ID:        e.ID,
			Direction: e.Direction,
			Body:      string(e.Body),
			BodyB64:   base64.StdEncoding.EncodeToString(e.Body),
			Kind:      int(e.Kind),
			Timestamp: e.When.UnixNano(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAppendHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, historyBodyMaxBytes)
	var req struct {
		Peer      string `json:"peer"`
		Direction string `json:"direction"`
		Kind      int    `json:"kind"`
		Body      string `json:"body"`
		BodyB64   string `json:"body_b64"`
		Timestamp int64  `json:"timestamp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h, err := identity.ParseHash(strings.TrimSpace(req.Peer))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body := []byte(req.Body)
	if req.BodyB64 != "" {
		decoded, derr := base64.StdEncoding.DecodeString(req.BodyB64)
		if derr != nil {
			http.Error(w, derr.Error(), http.StatusBadRequest)
			return
		}
		body = decoded
	}
	when := time.Unix(0, req.Timestamp).UTC()
	if req.Timestamp == 0 {
		when = time.Now().UTC()
	}
	id, err := s.mngr.AppendHistory(r.Context(), storage.HistoryEntry{
		Peer:      h,
		Direction: req.Direction,
		Kind:      storage.MessageKind(req.Kind),
		Body:      body,
		Status:    1,
		When:      when,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func randomToken() string {
	var b [24]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
