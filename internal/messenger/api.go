package messenger

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/network"
)

// ICEServer is the JSON-friendly form of an ICE server entry passed to
// the browser's RTCPeerConnection configuration.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// BootstrapPeer dials addr and runs a DHT bootstrap. Surface for the
// webui Settings → Bootstrap "Reconnect" / "Add" flows. Errors are
// returned to the caller (the HTTP handler) and not fatal to the
// running messenger.
func (m *Messenger) BootstrapPeer(ctx context.Context, addr string) error {
	return m.cfg.Network.Bootstrap(ctx, addr)
}

// NetworkStats exposes internal Network counters to the webui.
func (m *Messenger) NetworkStats() network.Stats {
	return m.cfg.Network.Stats()
}

// ICEServers discovers volunteer STUN/TURN servers via the network
// layer and maps them to browser-friendly ICEServer JSON.
//
// design.md §6: client picks 3-5 servers and passes them as
// webrtc.Configuration.ICEServers.
func (m *Messenger) ICEServers(ctx context.Context) []ICEServer {
	const maxServers = 5

	cands := m.cfg.Network.ICEServers(ctx, maxServers)
	if len(cands) == 0 {
		return nil
	}

	out := make([]ICEServer, 0, len(cands))
	for _, c := range cands {
		out = append(out, ICEServer{
			URLs: []string{c.Kind + ":" + net.JoinHostPort(c.Host, c.Port)},
		})
	}

	return out
}

// AddContact registers a peer in the local TOFU store. The peer's
// public identity is fetched via presence; the contact's verified flag
// stays false until the user does an out-of-band fingerprint check.
//
// Presence resolution retries a few times: if the peer started moments
// ago, their first record may not have reached the closest DHT nodes
// yet. ErrPeerOffline (wrapping presence.ErrNotFound) is returned only
// after all attempts fail, so the UI can show a useful message.
func (m *Messenger) AddContact(ctx context.Context, hash identity.Hash, alias string) error {
	info, err := m.resolveWithRetry(ctx, hash)
	if err != nil {
		return err
	}

	c := storage.Contact{
		Hash:        hash,
		Public:      info.Public,
		Alias:       alias,
		Fingerprint: info.Public.Fingerprint(),
		AddedAt:     time.Now().UTC(),
	}

	return m.cfg.Storage.UpsertContact(ctx, c)
}

// ErrPeerOffline wraps presence.ErrNotFound with a more user-friendly
// message for the UI layer.
var ErrPeerOffline = errors.New("peer not visible on the network: their messenger may not be running yet, or they have not joined the same DHT bootstrap")

func (m *Messenger) resolveWithRetry(ctx context.Context, hash identity.Hash) (network.PeerInfo, error) {
	const attempts = 4
	const delay = 800 * time.Millisecond

	var lastErr error
	for i := range attempts {
		attemptCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		info, err := m.cfg.Network.Lookup(attemptCtx, hash)
		cancel()
		if err == nil {
			return info, nil
		}
		lastErr = err
		if !errors.Is(err, network.ErrPeerNotFound) {
			return network.PeerInfo{}, fmt.Errorf("messenger: resolve contact: %w", err)
		}
		// Last attempt — surface the friendly error rather than sleep.
		if i == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return network.PeerInfo{}, ctx.Err()
		case <-time.After(delay):
		}
	}

	return network.PeerInfo{}, fmt.Errorf("%w (last lookup: %v)", ErrPeerOffline, lastErr)
}

// VerifyContact toggles the verified flag.
func (m *Messenger) VerifyContact(ctx context.Context, hash identity.Hash, verified bool) error {
	return m.cfg.Storage.SetContactVerified(ctx, hash, verified)
}

// EnsureContact persists a placeholder contact (empty alias) when an
// unknown peer initiates a session toward us. If a contact already
// exists for the hash, the call is a no-op — user-set alias and
// verification state are preserved across reconnects.
func (m *Messenger) EnsureContact(ctx context.Context, hash identity.Hash, pub identity.PublicIdentity) error {
	c := storage.Contact{
		Hash:        hash,
		Public:      pub,
		Alias:       "",
		Fingerprint: pub.Fingerprint(),
		AddedAt:     time.Now().UTC(),
	}

	return m.cfg.Storage.EnsureContact(ctx, c)
}

// RenameContact updates the local alias for an existing contact. The
// alias never traverses the network — only the local user labels peers.
func (m *Messenger) RenameContact(ctx context.Context, hash identity.Hash, alias string) error {
	return m.cfg.Storage.SetContactAlias(ctx, hash, alias)
}

// RemoveContactOptions controls the cascade behaviour of RemoveContact.
// Outbox is always cleared; only history is opt-in (see ROADMAP decisions
// log 2026-05-03).
type RemoveContactOptions struct {
	WipeHistory bool
}

// RemoveContact tears down any active signaling session to peer and then
// deletes the contact (and its outbox, and optionally its history) from
// storage. Closing the session before the storage delete avoids a window
// where a still-running session could re-queue outbox items.
func (m *Messenger) RemoveContact(ctx context.Context, hash identity.Hash, opts RemoveContactOptions) error {
	m.shutdownSessionsForPeer(hash)

	return m.cfg.Storage.DeleteContact(ctx, hash, storage.DeleteContactOptions{WipeHistory: opts.WipeHistory})
}

func (m *Messenger) shutdownSessionsForPeer(peer identity.Hash) {
	m.sessionsMu.Lock()
	victims := make([]*Session, 0)
	for k, sess := range m.sessions {
		if k.peer == peer {
			victims = append(victims, sess)
		}
	}
	m.sessionsMu.Unlock()

	for _, sess := range victims {
		sess.shutdown()
	}
}

// Contacts lists known contacts.
func (m *Messenger) Contacts(ctx context.Context) ([]storage.Contact, error) {
	return m.cfg.Storage.ListContacts(ctx)
}

// History returns the most recent `limit` messages with peer.
func (m *Messenger) History(ctx context.Context, peer identity.Hash, limit int) ([]storage.HistoryEntry, error) {
	return m.cfg.Storage.LoadHistory(ctx, peer, limit)
}

// AppendHistory persists a message the browser just sent or received.
// The browser owns the chat-layer protocol over DataChannel; Go's only
// job here is durable storage.
func (m *Messenger) AppendHistory(ctx context.Context, e storage.HistoryEntry) (int64, error) {
	return m.cfg.Storage.AppendMessage(ctx, e)
}

// MarkMessage updates a stored message's status (e.g. once the browser
// confirms delivery).
func (m *Messenger) MarkMessage(ctx context.Context, id int64, status int) error {
	return m.cfg.Storage.MarkMessageStatus(ctx, id, status)
}

// QueueOutbox stores a payload for delivery once peer comes back online.
func (m *Messenger) QueueOutbox(ctx context.Context, peer identity.Hash, payload []byte) (int64, error) {
	return m.cfg.Storage.AddOutboxItem(ctx, peer, payload)
}

// PendingOutbox returns queued items for peer (FIFO).
func (m *Messenger) PendingOutbox(ctx context.Context, peer identity.Hash) ([]storage.OutboxItem, error) {
	return m.cfg.Storage.PendingForPeer(ctx, peer)
}

// AcknowledgeOutbox removes a queued item the browser confirmed it sent.
func (m *Messenger) AcknowledgeOutbox(ctx context.Context, id int64) error {
	return m.cfg.Storage.DeleteOutboxItem(ctx, id)
}
