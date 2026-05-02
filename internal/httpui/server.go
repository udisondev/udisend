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
	// `Authorization: Bearer` header for REST).
	AuthToken string
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
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("httpui: listen: %w", err)
	}
	s := &Server{
		mngr:      cfg.Messenger,
		listener:  ln,
		logger:    cfg.Logger,
		authToken: cfg.AuthToken,
		wsConns:   make(map[*sseClient]struct{}),
		closed:    make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", s.requireToken(s.handleEvents))
	mux.HandleFunc("/api/snapshot", s.requireToken(s.handleSnapshot))
	mux.HandleFunc("/api/contacts/add", s.requireToken(s.handleContactAdd))
	mux.HandleFunc("/api/contacts/verify", s.requireToken(s.handleContactVerify))
	mux.HandleFunc("/api/history", s.requireToken(s.handleHistory))
	mux.HandleFunc("/api/append-history", s.requireToken(s.handleAppendHistory))
	mux.HandleFunc("/api/session/open", s.requireToken(s.handleSessionOpen))
	mux.HandleFunc("/api/session/close", s.requireToken(s.handleSessionClose))
	mux.HandleFunc("/api/signal/send", s.requireToken(s.handleSignalSend))
	mux.HandleFunc("/", s.handleStatic)
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	cfg.Messenger.SetIncomingHandler(s.onIncomingSession)
	return s, nil
}

// URL returns the user-friendly URL with the auth token baked in.
// Open this in a browser.
func (s *Server) URL() string {
	addr := s.listener.Addr().(*net.TCPAddr)
	host := "127.0.0.1"
	return fmt.Sprintf("http://%s:%d/?token=%s", host, addr.Port, s.authToken)
}

// AuthToken returns the random token required for every API call.
func (s *Server) AuthToken() string { return s.authToken }

// LocalAddress is the bound TCP address (e.g. "127.0.0.1:54321").
func (s *Server) LocalAddress() string { return s.listener.Addr().String() }

// Run starts serving and blocks until ctx is cancelled or Serve fails.
func (s *Server) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
	}()
	err := s.server.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the server immediately.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.server.Close()
	})
}

func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.checkToken(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) checkToken(r *http.Request) bool {
	if t := r.URL.Query().Get("token"); t != "" && t == s.authToken {
		return true
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") && strings.TrimPrefix(auth, "Bearer ") == s.authToken {
		return true
	}
	if c, err := r.Cookie("udisend_token"); err == nil && c.Value == s.authToken {
		return true
	}
	return false
}

// handleSnapshot returns identity + contacts + recent history per peer.
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
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
	resp := struct {
		Identity identityView  `json:"identity"`
		Contacts []contactView `json:"contacts"`
	}{
		Identity: identityView{
			Hash:        s.mngr.Identity().Public().DestinationHash().String(),
			Fingerprint: s.mngr.Identity().Public().Fingerprint(),
			Address:     s.mngr.LocalAddress(),
		},
		Contacts: cv,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleContactAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
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

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
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
