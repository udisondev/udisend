// Package messenger glues identity, dht, presence, signaling, webrtc and
// storage into the high-level API the GUI consumes: contacts, send-text,
// send-file, start-call, plus event callbacks for incoming traffic.
package messenger

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/udisondev/udisend/internal/chat"
	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/transport"
	uwebrtc "github.com/udisondev/udisend/pkg/webrtc"
)

// FileChunkSize is the on-the-wire chunk for file transfer. 32 KiB keeps
// us comfortably under DataChannel safe-message limits while still
// transferring at decent throughput.
const FileChunkSize = 32 * 1024

// Event types emitted to the UI layer.
type (
	// EventMessage signals an inbound text message.
	EventMessage struct {
		Peer      identity.Hash
		ID        chat.MessageID
		Body      string
		Timestamp time.Time
	}

	// EventFile signals an inbound completed file transfer with the saved path.
	EventFile struct {
		Peer    identity.Hash
		Name    string
		Path    string
		Size    uint64
		SHA256  string
	}

	// EventState signals a peer-session lifecycle change.
	EventState struct {
		Peer  identity.Hash
		State PeerState
	}

	// EventCall signals a call-related transition.
	EventCall struct {
		Peer  identity.Hash
		State CallState
	}
)

// PeerState is a small enum describing the lifecycle of a peer session.
type PeerState int

// Peer states.
const (
	PeerOffline PeerState = iota
	PeerConnecting
	PeerOnline
	PeerFailed
)

// CallState — talking to the user about an in-progress call (no media in
// MVP; see ASSUMPTIONS).
type CallState int

// Call states.
const (
	CallIdle CallState = iota
	CallInviting
	CallRinging
	CallActive
	CallEnded
	CallRejected
)

// Listener is the application-level callback bag.
type Listener struct {
	OnMessage func(EventMessage)
	OnFile    func(EventFile)
	OnState   func(EventState)
	OnCall    func(EventCall)
}

// Config configures Open.
type Config struct {
	Identity     *identity.Identity
	Bootstrap    []string // addresses of network nodes to bootstrap from
	Listen       string   // UDP listen address ("127.0.0.1:0" by default)
	StorageDir   string   // directory for the SQLite DB and downloads
	PresenceTTL  time.Duration
	Logger       *slog.Logger
}

// Messenger is the per-user runtime — one per messenger process.
type Messenger struct {
	cfg Config

	id        *identity.Identity
	transport *transport.UDPTransport
	node      *dht.Node
	publisher *presence.Publisher
	resolver  *presence.Resolver
	signaling *signaling.Service
	storage   *storage.Store
	listener  Listener

	sessionsMu sync.RWMutex
	sessions   map[identity.Hash]*peerSession

	closeOnce sync.Once
	closed    chan struct{}
	wg        sync.WaitGroup
}

type peerSession struct {
	peer       identity.Hash
	sig        *signaling.Channel
	rtc        *uwebrtc.Session
	state      PeerState
	call       CallState
	startedAt  time.Time
	incoming   map[chat.MessageID]*incomingFile
	mu         sync.Mutex
}

type incomingFile struct {
	name     string
	size     uint64
	sha256   string
	tmpFile  *os.File
	written  uint64
	hasher   *sha256Helper
}

// Open spins up a Messenger backed by a UDP transport, fresh DHT node,
// presence publisher/resolver, signaling service and SQLite storage.
func Open(ctx context.Context, cfg Config) (*Messenger, error) {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	if cfg.PresenceTTL == 0 {
		cfg.PresenceTTL = dht.DefaultPresenceTTL
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.StorageDir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			dir = "."
		}
		cfg.StorageDir = filepath.Join(dir, "udisend", cfg.Identity.Public().DestinationHash().String())
	}
	if err := os.MkdirAll(cfg.StorageDir, 0o755); err != nil {
		return nil, fmt.Errorf("messenger: storage dir: %w", err)
	}

	tr, err := transport.ListenUDP(cfg.Listen)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(cfg.StorageDir, "messenger.db")
	store, err := storage.Open(ctx, dbPath)
	if err != nil {
		_ = tr.Close()
		return nil, err
	}

	m := &Messenger{
		cfg:       cfg,
		id:        cfg.Identity,
		transport: tr,
		storage:   store,
		sessions:  make(map[identity.Hash]*peerSession),
		closed:    make(chan struct{}),
	}

	sig := signaling.NewService(signaling.Config{
		Identity:  cfg.Identity,
		Transport: tr,
		Resolver:  &resolverDelegate{m: m},
		Logger:    cfg.Logger,
	})
	m.signaling = sig
	sig.SetHandler(m.onIncomingChannel)

	node := dht.NewNode(cfg.Identity, tr, nil, dht.Config{
		Logger:       cfg.Logger,
		ExtraHandler: sig.HandlePacket,
	})
	m.node = node

	resolver := presence.NewResolver(nodeAdapter{node}, cfg.PresenceTTL)
	m.resolver = resolver

	publisher := presence.NewPublisher(presence.PublisherConfig{
		Identity: cfg.Identity,
		Address:  tr.LocalAddr().String(),
		TTL:      cfg.PresenceTTL,
		DHT:      nodeAdapter{node},
		Logger:   cfg.Logger,
	})
	m.publisher = publisher

	return m, nil
}

// LocalAddress is the externally-reachable transport address for this
// messenger — useful for UI display.
func (m *Messenger) LocalAddress() string { return m.transport.LocalAddr().String() }

// Identity returns the local identity.
func (m *Messenger) Identity() *identity.Identity { return m.id }

// Storage exposes the underlying store (UI uses this to list contacts /
// history without going through the runtime).
func (m *Messenger) Storage() *storage.Store { return m.storage }

// Resolver exposes the presence resolver — used to look up peer addresses
// from the GUI's "add contact" dialog.
func (m *Messenger) Resolver() *presence.Resolver { return m.resolver }

// SetListener registers UI callbacks. Replaces any previous listener.
func (m *Messenger) SetListener(l Listener) { m.listener = l }

// Run starts background loops (DHT receive, presence publish, outbox
// flusher). It blocks until the context is cancelled.
func (m *Messenger) Run(ctx context.Context) {
	m.wg.Add(3)
	go func() { defer m.wg.Done(); m.node.Run(ctx) }()
	go func() { defer m.wg.Done(); m.publisher.Run(ctx) }()
	go func() { defer m.wg.Done(); m.outboxLoop(ctx) }()

	// Bootstrap each known peer in the background.
	for _, addr := range m.cfg.Bootstrap {
		addr := addr
		go func() {
			netAddr, err := m.transport.Dial(addr)
			if err != nil {
				m.cfg.Logger.Warn("messenger: bootstrap parse", "addr", addr, "err", err)
				return
			}
			bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := m.node.Bootstrap(bctx, netAddr); err != nil {
				m.cfg.Logger.Warn("messenger: bootstrap", "addr", addr, "err", err)
			}
		}()
	}

	<-ctx.Done()
	m.wg.Wait()
}

// Close releases resources.
func (m *Messenger) Close() {
	m.closeOnce.Do(func() {
		close(m.closed)
		m.signaling.Close()
		// Close all sessions.
		m.sessionsMu.Lock()
		for _, sess := range m.sessions {
			if sess.rtc != nil {
				_ = sess.rtc.Close()
			}
			if sess.sig != nil {
				_ = sess.sig.Close()
			}
		}
		m.sessions = make(map[identity.Hash]*peerSession)
		m.sessionsMu.Unlock()
		_ = m.transport.Close()
		_ = m.storage.Close()
	})
}

// nodeAdapter exposes the subset of dht.Node required by presence.DHT.
type nodeAdapter struct{ n *dht.Node }

func (a nodeAdapter) PutValue(ctx context.Context, key dht.NodeID, value []byte) error {
	return a.n.PutValue(ctx, key, value)
}

func (a nodeAdapter) LookupValue(ctx context.Context, key dht.NodeID) ([]byte, []dht.Contact, error) {
	return a.n.LookupValue(ctx, key)
}

// resolverDelegate lets the signaling service ask the messenger for peer
// records — we want to consult both the local presence cache and the DHT.
type resolverDelegate struct{ m *Messenger }

func (r *resolverDelegate) Lookup(ctx context.Context, peer identity.Hash) (*presence.Record, error) {
	return r.m.resolver.Lookup(ctx, peer)
}

// sha256Helper avoids tying chat-level types to crypto/sha256 directly.
type sha256Helper struct {
	h interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
}

func newSHA256() *sha256Helper { return &sha256Helper{h: sha256.New()} }

func (s *sha256Helper) write(p []byte) { _, _ = s.h.Write(p) }
func (s *sha256Helper) sumHex() string {
	return hex.EncodeToString(s.h.Sum(nil))
}

// IDFromString parses a destination-hash hex string into the typed Hash.
// Used by the GUI's "add contact" dialog.
func IDFromString(s string) (identity.Hash, error) { return identity.ParseHash(s) }

// fileSeed is a tiny helper that picks a deterministic file ID from a
// random source — ensures unique IDs for in-flight transfers.
func fileSeed() chat.MessageID {
	var id chat.MessageID
	_, _ = rand.Read(id[:])
	return id
}

var _ io.Reader = (io.Reader)(nil) // keep io imported if removed elsewhere
var errPeerUnreachable = errors.New("messenger: peer unreachable; queued in outbox")
