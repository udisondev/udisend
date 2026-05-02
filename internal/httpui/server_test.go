package httpui_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/httpui"
	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/pkg/identity"
)

// TestSignalingBridge_TwoMessengers proves the v2 protocol critical path:
// two messengers exchange a signal envelope through the Go-side Noise pipe
// and the receiver gets it via SSE. No real WebRTC — the browser side is
// simulated by REST+SSE so we can run this in plain `go test`.
func TestSignalingBridge_TwoMessengers(t *testing.T) {
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

	// Wait for presence to propagate.
	time.Sleep(2 * time.Second)

	// Alice adds Bob (also seeds bob's contacts).
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
	mngr, err := messenger.Open(ctx, messenger.Config{
		Identity:   id,
		Listen:     udp,
		Bootstrap:  bootstrap,
		StorageDir: filepath.Join(root, name),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := httpui.NewServer(httpui.Config{
		Messenger: mngr,
		Listen:    http,
	})
	if err != nil {
		t.Fatal(err)
	}
	go mngr.Run(ctx)
	go func() { _ = srv.Run(ctx) }()
	t.Cleanup(func() {
		srv.Close()
		mngr.Close()
	})
	// Wait briefly for the HTTP listener to be ready.
	time.Sleep(100 * time.Millisecond)
	return &peer{
		mngr:  mngr,
		srv:   srv,
		hash:  id.Public().DestinationHash().String(),
		udp:   mngr.LocalAddress(),
		url:   srv.URL(),
		token: srv.AuthToken(),
	}
}

func postJSON(p *peer, path string, body any) ([]byte, error) {
	blob, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "http://"+p.srv.LocalAddress()+path, bytes.NewReader(blob))
	req.Header.Set("Content-Type", "application/json")
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
	s := &sseStream{resp: resp, stop: make(chan struct{})}
	go s.read()
	// Give the server-side a moment to register the SSE client.
	time.Sleep(50 * time.Millisecond)
	return s
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
	}
}

func (s *sseStream) Close() error {
	close(s.stop)
	return s.resp.Body.Close()
}

func (s *sseStream) expect(_ *testing.T, typ string, timeout time.Duration) map[string]any {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, ev := range s.events {
			if ev["type"] == typ {
				s.mu.Unlock()
				return ev
			}
		}
		s.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}
