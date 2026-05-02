package messenger

import (
	"context"
	"fmt"
	"time"

	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/identity"
)

// AddContact registers a peer in the local TOFU store. The peer's public
// identity is fetched via presence; the contact's verified flag stays
// false until the user does an out-of-band fingerprint check.
func (m *Messenger) AddContact(ctx context.Context, hash identity.Hash, alias string) error {
	rec, err := m.resolver.Lookup(ctx, hash)
	if err != nil {
		return fmt.Errorf("messenger: resolve contact: %w", err)
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
