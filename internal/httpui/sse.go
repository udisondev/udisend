package httpui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/pkg/identity"
)

// sseClient is one connected browser tab. Each client has:
//   - an outbox channel of envelopes Go wants to push to it,
//   - a map of currently-open signaling sessions whose inbound traffic
//     this client owns (one client = one user, one identity).
//
// The MVP topology is one browser per messenger; if a second browser
// connects it gets the snapshot but new sessions are still funneled to
// the FIRST connection. Multi-window UX is post-MVP.
type sseClient struct {
	server *Server
	id     uint64

	out chan envelope

	sessionsMu sync.Mutex
	sessions   map[string]*messenger.Session

	closeOnce sync.Once
	closed    chan struct{}
}

// envelope is the JSON shape used by SSE events and also by request
// bodies — keeps the wire model uniform on both directions.
type envelope struct {
	Type      string `json:"type"`
	Peer      string `json:"peer,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Payload   string `json:"payload,omitempty"`
	Error     string `json:"error,omitempty"`
}

var clientCounter atomic.Uint64

// handleEvents serves a long-lived SSE stream. The browser's
// EventSource opens this once and re-opens it automatically if it drops.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	c := &sseClient{
		server:   s,
		id:       clientCounter.Add(1),
		out:      make(chan envelope, 64),
		sessions: make(map[string]*messenger.Session),
		closed:   make(chan struct{}),
	}
	s.registerClient(c)
	defer s.unregisterClient(c)
	defer c.shutdown()

	// Keep-alive ticker — SSE comments every 25s prevent intermediate proxies
	// (and overly aggressive browsers) from killing an idle connection.
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case <-keepalive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case env := <-c.out:
			data, err := json.Marshal(env)
			if err != nil {
				continue
			}
			if _, err := w.Write([]byte("data: ")); err != nil {
				return
			}
			if _, err := w.Write(data); err != nil {
				return
			}
			if _, err := w.Write([]byte("\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) registerClient(c *sseClient) {
	s.wsMu.Lock()
	s.wsConns[c] = struct{}{}
	s.wsMu.Unlock()
}

func (s *Server) unregisterClient(c *sseClient) {
	s.wsMu.Lock()
	delete(s.wsConns, c)
	s.wsMu.Unlock()
}

func (c *sseClient) shutdown() {
	c.closeOnce.Do(func() {
		close(c.closed)
		// Snapshot under lock, then close sessions outside it — sess.Close
		// triggers pumpSession's defer which also wants sessionsMu, so
		// holding it here would deadlock.
		c.sessionsMu.Lock()
		toClose := make([]*messenger.Session, 0, len(c.sessions))
		for _, sess := range c.sessions {
			toClose = append(toClose, sess)
		}
		c.sessions = nil
		c.sessionsMu.Unlock()
		for _, sess := range toClose {
			_ = sess.Close()
		}
	})
}

// send queues an envelope for delivery. Drops on backpressure rather than
// block the caller — clients that fall behind miss events but do not
// hang the runtime.
func (c *sseClient) send(env envelope) {
	select {
	case c.out <- env:
	default:
		c.server.logger.Warn("sse: client outbox full; dropping", "client", c.id, "type", env.Type)
	}
}

// onIncomingSession is the messenger.SetIncomingHandler callback.
func (s *Server) onIncomingSession(sess *messenger.Session) {
	s.wsMu.Lock()
	var target *sseClient
	for c := range s.wsConns {
		target = c
		break
	}
	s.wsMu.Unlock()
	if target == nil {
		s.logger.Warn("httpui: incoming session with no UI attached; closing", "peer", sess.Peer)
		_ = sess.Close()
		return
	}
	target.sessionsMu.Lock()
	target.sessions[sess.SessionID] = sess
	target.sessionsMu.Unlock()
	target.send(envelope{Type: "incoming_session", Peer: sess.Peer.String(), SessionID: sess.SessionID})
	go target.pumpSession(sess)
}

func (c *sseClient) pumpSession(sess *messenger.Session) {
	defer func() {
		c.sessionsMu.Lock()
		delete(c.sessions, sess.SessionID)
		c.sessionsMu.Unlock()
		c.send(envelope{Type: "session_closed", Peer: sess.Peer.String(), SessionID: sess.SessionID})
	}()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		ev, err := sess.Recv(ctx)
		cancel()
		if err != nil {
			return
		}
		c.send(envelope{
			Type:      "signal_recv",
			Peer:      sess.Peer.String(),
			SessionID: sess.SessionID,
			Kind:      ev.Kind,
			Payload:   ev.Payload,
		})
	}
}

// handleSessionOpen — POST /api/session/open {peer}.
func (s *Server) handleSessionOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req envelope
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	peer, err := identity.ParseHash(req.Peer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c := s.singleClient()
	if c == nil {
		http.Error(w, "no SSE client connected", http.StatusFailedDependency)
		return
	}
	connectCtx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	sess, err := s.mngr.Connect(connectCtx, peer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	c.sessionsMu.Lock()
	c.sessions[sess.SessionID] = sess
	c.sessionsMu.Unlock()
	go c.pumpSession(sess)
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sess.SessionID, "peer": peer.String()})
}

// handleSignalSend — POST /api/signal/send {session_id, kind, payload}.
func (s *Server) handleSignalSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req envelope
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c := s.singleClient()
	if c == nil {
		http.Error(w, "no SSE client connected", http.StatusFailedDependency)
		return
	}
	c.sessionsMu.Lock()
	sess, ok := c.sessions[req.SessionID]
	c.sessionsMu.Unlock()
	if !ok {
		http.Error(w, "unknown session_id", http.StatusNotFound)
		return
	}
	sendCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := sess.Send(sendCtx, messenger.SignalEvent{Kind: req.Kind, Payload: req.Payload}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSessionClose — POST /api/session/close {session_id}.
func (s *Server) handleSessionClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req envelope
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c := s.singleClient()
	if c == nil {
		http.Error(w, "no SSE client connected", http.StatusFailedDependency)
		return
	}
	c.sessionsMu.Lock()
	sess, ok := c.sessions[req.SessionID]
	if ok {
		delete(c.sessions, req.SessionID)
	}
	c.sessionsMu.Unlock()
	if ok {
		_ = sess.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// singleClient returns the (single) connected SSE client, or nil.
func (s *Server) singleClient() *sseClient {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	for c := range s.wsConns {
		return c
	}
	return nil
}

// suppress unused warning on errors import in some build configurations.
var _ = errors.New
