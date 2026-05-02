package messenger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
)

// AddContact registers a peer in the local TOFU store. The peer's public
// identity is fetched via presence; the contact's verified flag stays
// false until the user does an out-of-band fingerprint check.
//
// Presence resolution retries a few times: if the peer started moments
// ago, their first record may not have reached the closest DHT nodes
// yet. ErrPeerOffline (wrapping presence.ErrNotFound) is returned only
// after all attempts fail, so the UI can show a useful message.
func (m *Messenger) AddContact(ctx context.Context, hash identity.Hash, alias string) error {
	rec, err := m.resolveWithRetry(ctx, hash)
	if err != nil {
		return err
	}
	c := storage.Contact{
		Hash:        hash,
		Public:      rec.Public,
		Alias:       alias,
		Fingerprint: rec.Public.Fingerprint(),
		AddedAt:     time.Now().UTC(),
	}
	return m.storage.UpsertContact(ctx, c)
}

// ErrPeerOffline wraps presence.ErrNotFound with a more user-friendly
// message for the UI layer.
var ErrPeerOffline = errors.New("peer not visible on the network: their messenger may not be running yet, or they have not joined the same DHT bootstrap")

func (m *Messenger) resolveWithRetry(ctx context.Context, hash identity.Hash) (*presence.Record, error) {
	const attempts = 4
	const delay = 800 * time.Millisecond
	var lastErr error
	for i := range attempts {
		attemptCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		rec, err := m.resolver.Lookup(attemptCtx, hash)
		cancel()
		if err == nil {
			return rec, nil
		}
		lastErr = err
		if !errors.Is(err, presence.ErrNotFound) {
			return nil, fmt.Errorf("messenger: resolve contact: %w", err)
		}
		// Last attempt — surface the friendly error rather than sleep.
		if i == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, fmt.Errorf("%w (last lookup: %v)", ErrPeerOffline, lastErr)
}

// VerifyContact toggles the verified flag.
func (m *Messenger) VerifyContact(ctx context.Context, hash identity.Hash, verified bool) error {
	return m.storage.SetContactVerified(ctx, hash, verified)
}

// Contacts lists known contacts.
func (m *Messenger) Contacts(ctx context.Context) ([]storage.Contact, error) {
	return m.storage.ListContacts(ctx)
}

// History returns the most recent `limit` messages with peer.
func (m *Messenger) History(ctx context.Context, peer identity.Hash, limit int) ([]storage.HistoryEntry, error) {
	return m.storage.LoadHistory(ctx, peer, limit)
}

// AppendHistory persists a message the browser just sent or received.
// The browser owns the chat-layer protocol over DataChannel; Go's only
// job here is durable storage.
func (m *Messenger) AppendHistory(ctx context.Context, e storage.HistoryEntry) (int64, error) {
	return m.storage.AppendMessage(ctx, e)
}

// MarkMessage updates a stored message's status (e.g. once the browser
// confirms delivery).
func (m *Messenger) MarkMessage(ctx context.Context, id int64, status int) error {
	return m.storage.MarkMessageStatus(ctx, id, status)
}

// QueueOutbox stores a payload for delivery once peer comes back online.
func (m *Messenger) QueueOutbox(ctx context.Context, peer identity.Hash, payload []byte) (int64, error) {
	return m.storage.AddOutboxItem(ctx, peer, payload)
}

// PendingOutbox returns queued items for peer (FIFO).
func (m *Messenger) PendingOutbox(ctx context.Context, peer identity.Hash) ([]storage.OutboxItem, error) {
	return m.storage.PendingForPeer(ctx, peer)
}

// AcknowledgeOutbox removes a queued item the browser confirmed it sent.
func (m *Messenger) AcknowledgeOutbox(ctx context.Context, id int64) error {
	return m.storage.DeleteOutboxItem(ctx, id)
}
