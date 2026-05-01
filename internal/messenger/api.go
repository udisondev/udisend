package messenger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/udisondev/udisend/internal/chat"
	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/identity"
)

// AddContact registers a peer with the local TOFU store.
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

// VerifyContact toggles the verified flag for a contact (after the user
// confirmed their fingerprint out-of-band).
func (m *Messenger) VerifyContact(ctx context.Context, hash identity.Hash, verified bool) error {
	return m.storage.SetContactVerified(ctx, hash, verified)
}

// SendText delivers a text message, queueing in the outbox if the peer
// is unreachable.
func (m *Messenger) SendText(ctx context.Context, peer identity.Hash, text string) error {
	msg := chat.Message{
		ID:        chat.NewMessageID(),
		Kind:      chat.KindText,
		Timestamp: time.Now().UTC(),
		Body:      []byte(text),
	}
	blob, err := msg.Encode()
	if err != nil {
		return err
	}
	id, err := m.storage.AppendMessage(ctx, storage.HistoryEntry{
		Peer:      peer,
		Direction: "out",
		Kind:      storage.MessageKind(msg.Kind),
		Body:      msg.Body,
		Status:    0,
		When:      msg.Timestamp,
	})
	if err != nil {
		return err
	}
	if err := m.sendMessage(ctx, peer, msg); err != nil {
		// Queue in outbox; a background loop will retry.
		_, qerr := m.storage.AddOutboxItem(ctx, peer, blob)
		if qerr == nil {
			_ = m.storage.MarkMessageStatus(ctx, id, 0)
		}
		return errPeerUnreachable
	}
	_ = m.storage.MarkMessageStatus(ctx, id, 1)
	return nil
}

// SendFile chunks the file at path and ships it to peer over the
// DataChannel. Returns once the FileEnd message has been sent.
func (m *Messenger) SendFile(ctx context.Context, peer identity.Hash, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("messenger: open file: %w", err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return err
	}
	sumHex := hex.EncodeToString(hasher.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	fileID := fileSeed()
	offerMsg := chat.Message{
		ID:        chat.NewMessageID(),
		Kind:      chat.KindFileOffer,
		Timestamp: time.Now().UTC(),
		Body: chat.EncodeFileOffer(chat.FileOffer{
			FileID:    fileID,
			Name:      filepath.Base(path),
			Size:      uint64(stat.Size()),
			SHA256Hex: sumHex,
		}),
	}
	if err := m.sendMessage(ctx, peer, offerMsg); err != nil {
		return err
	}
	buf := make([]byte, FileChunkSize)
	var offset uint64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			chunk := chat.Message{
				ID:        chat.NewMessageID(),
				Kind:      chat.KindFileChunk,
				Timestamp: time.Now().UTC(),
				Body: chat.EncodeFileChunk(chat.FileChunk{
					FileID: fileID,
					Offset: offset,
					Data:   append([]byte(nil), buf[:n]...),
				}),
			}
			if err := m.sendMessage(ctx, peer, chunk); err != nil {
				return err
			}
			offset += uint64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	endMsg := chat.Message{
		ID:        chat.NewMessageID(),
		Kind:      chat.KindFileEnd,
		Timestamp: time.Now().UTC(),
		Body:      chat.EncodeFileEnd(chat.FileEnd{FileID: fileID}),
	}
	return m.sendMessage(ctx, peer, endMsg)
}

// StartCall sends a CallInvite to peer.
func (m *Messenger) StartCall(ctx context.Context, peer identity.Hash) error {
	m.emitCall(peer, CallInviting)
	return m.sendMessage(ctx, peer, chat.Message{
		ID:        chat.NewMessageID(),
		Kind:      chat.KindCallInvite,
		Timestamp: time.Now().UTC(),
	})
}

// AcceptCall sends a CallAccept to peer.
func (m *Messenger) AcceptCall(ctx context.Context, peer identity.Hash) error {
	m.emitCall(peer, CallActive)
	return m.sendMessage(ctx, peer, chat.Message{
		ID:        chat.NewMessageID(),
		Kind:      chat.KindCallAccept,
		Timestamp: time.Now().UTC(),
	})
}

// RejectCall sends a CallReject.
func (m *Messenger) RejectCall(ctx context.Context, peer identity.Hash) error {
	m.emitCall(peer, CallRejected)
	return m.sendMessage(ctx, peer, chat.Message{
		ID:        chat.NewMessageID(),
		Kind:      chat.KindCallReject,
		Timestamp: time.Now().UTC(),
	})
}

// EndCall sends a CallEnd.
func (m *Messenger) EndCall(ctx context.Context, peer identity.Hash) error {
	m.emitCall(peer, CallEnded)
	return m.sendMessage(ctx, peer, chat.Message{
		ID:        chat.NewMessageID(),
		Kind:      chat.KindCallEnd,
		Timestamp: time.Now().UTC(),
	})
}

// History returns the most recent `limit` messages with `peer`.
func (m *Messenger) History(ctx context.Context, peer identity.Hash, limit int) ([]storage.HistoryEntry, error) {
	return m.storage.LoadHistory(ctx, peer, limit)
}

// Contacts lists known contacts.
func (m *Messenger) Contacts(ctx context.Context) ([]storage.Contact, error) {
	return m.storage.ListContacts(ctx)
}

// outboxLoop periodically tries to flush queued messages.
func (m *Messenger) outboxLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			peers, err := m.storage.AllOutboxPeers(ctx)
			if err != nil {
				m.cfg.Logger.Warn("messenger: outbox list peers", "err", err)
				continue
			}
			for _, p := range peers {
				m.flushOutboxFor(p)
			}
		}
	}
}

// flushOutboxFor walks queued items for `peer` and tries to send them
// using the live session, if any. Items that send successfully are removed.
func (m *Messenger) flushOutboxFor(peer identity.Hash) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	items, err := m.storage.PendingForPeer(ctx, peer)
	if err != nil || len(items) == 0 {
		return
	}
	sess, err := m.ensureSession(ctx, peer)
	if err != nil {
		return
	}
	for _, it := range items {
		if err := sess.rtc.SendData(ctx, it.Payload); err != nil {
			_ = m.storage.IncrementOutboxAttempts(ctx, it.ID)
			break
		}
		_ = m.storage.DeleteOutboxItem(ctx, it.ID)
	}
}
