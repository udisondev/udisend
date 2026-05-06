package httpui_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/httpui"
	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/network"
)

// TestSignalingBridge_TwoMessengers proves the v2 protocol critical path:
// two messengers exchange a signal envelope through the Go-side Noise pipe
// and the receiver gets it via SSE. No real WebRTC — the browser side is
// simulated by REST+SSE so we can run this in plain `go test`.
func TestSignalingBridge_TwoMessengers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)
	bob := startMessenger(t, ctx, "bob", dir, "127.0.0.1:0", "127.0.0.1:0",
		[]string{alice.udp})

	// Both browsers connect first (snapshot warms presence, SSE opens).
	aliceSSE := openSSE(t, ctx, alice)
	defer aliceSSE.Close()
	bobSSE := openSSE(t, ctx, bob)
	defer bobSSE.Close()

	// Alice adds Bob — this is the natural sync on presence: the
	// runtime's resolveWithRetry loops AddContact for ~3 attempts with
	// 800 ms backoff, so by the time it returns nil, Bob's record has
	// been resolved through the DHT. No arbitrary sleep needed.
	if _, err := postJSON(alice, "/api/contacts/add", map[string]any{
		"hash": bob.hash, "alias": "bob",
	}); err != nil {
		t.Fatalf("add bob: %v", err)
	}

	// Alice opens a signaling session to Bob.
	openResp, err := postJSON(alice, "/api/session/open", map[string]any{
		"peer": bob.hash,
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	var openParsed struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(openResp, &openParsed); err != nil {
		t.Fatalf("parse session_id: %v", err)
	}
	if openParsed.SessionID == "" {
		t.Fatal("no session_id returned")
	}

	// Bob's SSE should soon report `incoming_session`.
	if ev := bobSSE.expect(t, "incoming_session", 5*time.Second); ev == nil {
		t.Fatal("Bob never received incoming_session")
	}

	// Alice sends a synthetic SDP offer.
	if _, err := postJSON(alice, "/api/signal/send", map[string]any{
		"session_id": openParsed.SessionID,
		"kind":       "offer",
		"payload":    "v=0\r\no=- 1 1 IN IP4 0.0.0.0\r\n",
	}); err != nil {
		t.Fatalf("signal/send: %v", err)
	}

	// Bob should receive a signal_recv with the same payload.
	got := bobSSE.expect(t, "signal_recv", 5*time.Second)
	if got == nil {
		t.Fatal("Bob never received signal_recv")
	}
	if got["kind"] != "offer" {
		t.Fatalf("kind = %v, want offer", got["kind"])
	}
	if !strings.Contains(fmt.Sprint(got["payload"]), "v=0") {
		t.Fatalf("payload missing SDP marker: %v", got["payload"])
	}
}

// TestContactDelete_HTTPRoute drives the new /api/contacts/delete route:
// a contact that alice just added is removed via REST and disappears from
// the snapshot. Outbox queued before deletion is also gone.
func TestContactDelete_HTTPRoute(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)
	bob := startMessenger(t, ctx, "bob", dir, "127.0.0.1:0", "127.0.0.1:0", []string{alice.udp})

	if _, err := postJSON(alice, "/api/contacts/add", map[string]any{
		"hash": bob.hash, "alias": "bob",
	}); err != nil {
		t.Fatalf("add bob: %v", err)
	}

	bobHash, err := identity.ParseHash(bob.hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.mngr.QueueOutbox(ctx, bobHash, []byte("queued")); err != nil {
		t.Fatal(err)
	}

	if _, err := postJSON(alice, "/api/contacts/delete", map[string]any{
		"hash":         bob.hash,
		"wipe_history": true,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	snap, err := getJSON(alice, "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Contacts []struct {
			Hash string `json:"hash"`
		} `json:"contacts"`
	}
	if err := json.Unmarshal(snap, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, c := range parsed.Contacts {
		if c.Hash == bob.hash {
			t.Fatalf("bob still in snapshot after delete")
		}
	}

	pending, err := alice.mngr.PendingOutbox(ctx, bobHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("outbox not cleared: %d items", len(pending))
	}

	resp, err := postJSONResp(alice, "/api/contacts/delete", map[string]any{"hash": bob.hash})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestCSRF_BlocksMutatingPOSTWithoutHeader verifies that a state-changing
// API request without the X-Requested-With marker is rejected with 403,
// even when the auth token is correct. The defense matters most for
// loopback bind: a malicious page in the browser can hit 127.0.0.1 with
// a cross-origin form POST and would carry the cookie but not a custom
// header. With the CSRF guard, that attack 403s.
func TestCSRF_BlocksMutatingPOSTWithoutHeader(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	body := bytes.NewReader([]byte(`{"hash":"00000000000000000000000000000000","alias":"x"}`))
	req, _ := http.NewRequest(http.MethodPost,
		"http://"+alice.srv.LocalAddress()+"/api/contacts/add", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+alice.token)
	// Note: deliberately missing X-Requested-With.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// TestHostHeader_RejectsForeignHost is the DNS-rebinding defence. An
// attacker who lures the user to evil.attacker.com (resolved to 127.0.0.1
// via DNS rebinding) reaches the local listener, but the request's Host
// header carries the attacker's name. Without this check the X-Requested-
// With CSRF gate is the only line of defence — and a same-origin XHR (in
// the rebound origin) can satisfy it. The middleware MUST reject Host
// headers that are not in the allowlist (loopback names + configured
// public host).
func TestHostHeader_RejectsForeignHost(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	cases := []struct {
		name string
		host string
	}{
		{"attacker hostname", "evil.attacker.com"},
		{"attacker IP", "203.0.113.7:9000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet,
				"http://"+alice.srv.LocalAddress()+"/api/snapshot", nil)
			req.Header.Set("Authorization", "Bearer "+alice.token)
			req.Host = tc.host

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusMisdirectedRequest {
				t.Errorf("Host=%q status = %d, want 421", tc.host, resp.StatusCode)
			}
		})
	}

	// Sanity: localhost (the canonical loopback name) MUST be accepted —
	// otherwise users hitting the printed URL via `localhost:port` see a 421.
	t.Run("localhost accepted", func(t *testing.T) {
		_, port, _ := net.SplitHostPort(alice.srv.LocalAddress())
		req, _ := http.NewRequest(http.MethodGet,
			"http://"+alice.srv.LocalAddress()+"/api/snapshot", nil)
		req.Header.Set("Authorization", "Bearer "+alice.token)
		req.Host = "localhost:" + port

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("localhost host status = %d, want 200", resp.StatusCode)
		}
	})
}

// ─── Helpers ─────────────────────────────────────────────────────────

type peer struct {
	mngr  *messenger.Messenger
	srv   *httpui.Server
	hash  string
	udp   string
	url   string
	token string
}

func startMessenger(t *testing.T, ctx context.Context, name, root, udp, http string, bootstrap []string) *peer {
	t.Helper()

	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	storeDir := filepath.Join(root, name)
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(ctx, filepath.Join(storeDir, "messenger.db"))
	if err != nil {
		t.Fatal(err)
	}

	node, err := network.Open(ctx, network.Config{
		Identity:               id,
		Listen:                 udp,
		Bootstrap:              bootstrap,
		SeenPeerStore:          store,
		BootstrapOverrideStore: store,
	})
	if err != nil {
		t.Fatal(err)
	}

	mngr := messenger.Open(messenger.Config{
		Network: node,
		Storage: store,
	})

	srv, err := httpui.NewServer(httpui.Config{
		Messenger: mngr,
		Listen:    http,
	})
	if err != nil {
		t.Fatal(err)
	}

	nodeDone := make(chan error, 1)
	mngrDone := make(chan error, 1)
	srvDone := make(chan error, 1)
	go func() { nodeDone <- node.Run(ctx) }()
	go func() { mngrDone <- mngr.Run(ctx) }()
	go func() { srvDone <- srv.Run(ctx) }()

	t.Cleanup(func() {
		srv.Close()
		mngr.Close()
		if err := node.Close(); err != nil {
			t.Errorf("close node: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
		// Drain Run errors after Close so we surface real failures while
		// filtering out the expected ctx-cancel exit path.
		for _, item := range []struct {
			name string
			ch   chan error
		}{{"node", nodeDone}, {"messenger", mngrDone}, {"httpui", srvDone}} {
			select {
			case err := <-item.ch:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Errorf("%s.Run: %v", item.name, err)
				}
			case <-time.After(3 * time.Second):
				t.Errorf("%s.Run did not exit after ctx cancel", item.name)
			}
		}
	})

	// Poll until the HTTP listener accepts connections.
	waitListenerReady(t, ctx, srv.LocalAddress())

	return &peer{
		mngr:  mngr,
		srv:   srv,
		hash:  id.Public().DestinationHash().String(),
		udp:   node.LocalAddress(),
		url:   srv.URL(),
		token: srv.AuthToken(),
	}
}


func postJSON(p *peer, path string, body any) ([]byte, error) {
	blob, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "http://"+p.srv.LocalAddress()+path, bytes.NewReader(blob))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("X-Requested-With", "udisend")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s -> %d: %s", path, resp.StatusCode, rb)
	}
	return rb, nil
}

// postJSONResp is like postJSON but returns the raw response so tests can
// assert on non-2xx status codes (e.g. 404 for a missing contact).
func postJSONResp(p *peer, path string, body any) (*http.Response, error) {
	blob, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "http://"+p.srv.LocalAddress()+path, bytes.NewReader(blob))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("X-Requested-With", "udisend")

	return http.DefaultClient.Do(req)
}

func getJSON(p *peer, path string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, "http://"+p.srv.LocalAddress()+path, nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s -> %d: %s", path, resp.StatusCode, rb)
	}

	return rb, nil
}

type sseStream struct {
	resp   *http.Response
	mu     sync.Mutex
	events []map[string]any
	// notify wakes any expect() waiter as soon as a new event lands so
	// the test does not poll on a 50 ms sleep. Buffered to 1 so the
	// reader never blocks if no waiter is currently parked.
	notify chan struct{}
	stop   chan struct{}
}

func openSSE(t *testing.T, ctx context.Context, p *peer) *sseStream {
	t.Helper()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+p.srv.LocalAddress()+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("SSE status %d", resp.StatusCode)
	}

	s := &sseStream{resp: resp, stop: make(chan struct{}), notify: make(chan struct{}, 1)}
	go s.read()

	// The SSE handler now registers the client BEFORE flushing headers
	// (see internal/httpui/sse.go), so once Do() returned a 200 the
	// registration is already visible to onIncomingSession. No sleep
	// or polling needed.

	return s
}

// waitListenerReady polls a TCP address until it accepts a connection
// or ctx expires. Replaces the previous time.Sleep(100ms).
func waitListenerReady(t *testing.T, ctx context.Context, addr string) {
	t.Helper()

	for {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			_ = conn.Close()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("listener at %s never came up: %v", addr, err)
		default:
		}
	}
}

func (s *sseStream) read() {
	defer s.resp.Body.Close()
	r := bufio.NewReader(s.resp.Body)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var ev map[string]any
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		s.mu.Lock()
		s.events = append(s.events, ev)
		s.mu.Unlock()
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}
}

func (s *sseStream) Close() error {
	close(s.stop)
	return s.resp.Body.Close()
}

func (s *sseStream) expect(_ *testing.T, typ string, timeout time.Duration) map[string]any {
	// Event-driven wait instead of 50 ms polling: read() signals every
	// new event over s.notify, so a matching event wakes us within the
	// scheduler latency. The deadline timer puts a hard upper bound.
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		for _, ev := range s.events {
			if ev["type"] == typ {
				s.mu.Unlock()
				return ev
			}
		}
		s.mu.Unlock()
		select {
		case <-s.notify:
		case <-deadline.C:
			return nil
		}
	}
}
