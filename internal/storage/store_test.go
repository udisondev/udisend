package storage_test

import (
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/identity"
)

func TestAddOutboxItem_Cap(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	id, _ := identity.Generate(rand.Reader)
	peer := id.Public().DestinationHash()

	for i := 0; i < storage.MaxOutboxItemsPerPeer; i++ {
		if _, err := s.AddOutboxItem(t.Context(), peer, []byte("payload")); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if _, err := s.AddOutboxItem(t.Context(), peer, []byte("over")); !errors.Is(err, storage.ErrOutboxFull) {
		t.Fatalf("at cap: err = %v, want ErrOutboxFull", err)
	}

	other, _ := identity.Generate(rand.Reader)
	if _, err := s.AddOutboxItem(t.Context(), other.Public().DestinationHash(), []byte("ok")); err != nil {
		t.Fatalf("other peer should still accept: %v", err)
	}
}

func TestPruneOutboxOlderThan(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	id, _ := identity.Generate(rand.Reader)
	peer := id.Public().DestinationHash()
	if _, err := s.AddOutboxItem(t.Context(), peer, []byte("recent")); err != nil {
		t.Fatal(err)
	}

	future := time.Now().Add(time.Hour).Unix()
	n, err := s.PruneOutboxOlderThan(t.Context(), future)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row deleted, got %d", n)
	}
}

func mkStore(t *testing.T) *storage.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := storage.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_ContactsRoundtrip(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	id, _ := identity.Generate(rand.Reader)
	c := storage.Contact{
		Hash:        id.Public().DestinationHash(),
		Public:      id.Public(),
		Alias:       "alice",
		Fingerprint: id.Public().Fingerprint(),
		AddedAt:     time.Now().Truncate(time.Second).UTC(),
	}
	if err := s.UpsertContact(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetContact(t.Context(), c.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if got.Alias != "alice" {
		t.Fatal("alias")
	}
	if got.Public.DestinationHash() != c.Hash {
		t.Fatal("hash mismatch after roundtrip")
	}
}

func TestStore_ContactFingerprintChange(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	id1, _ := identity.Generate(rand.Reader)
	id2, _ := identity.Generate(rand.Reader)
	c := storage.Contact{
		Hash:        id1.Public().DestinationHash(),
		Public:      id1.Public(),
		Alias:       "x",
		Fingerprint: id1.Public().Fingerprint(),
		AddedAt:     time.Now(),
	}
	if err := s.UpsertContact(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	c2 := c
	c2.Public = id2.Public() // different keys, same hash slot — simulate impostor
	if err := s.UpsertContact(t.Context(), c2); !errors.Is(err, storage.ErrFingerprintChanged) {
		t.Fatalf("err = %v, want ErrFingerprintChanged", err)
	}
}

func TestStore_MessagesAndHistory(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	id, _ := identity.Generate(rand.Reader)
	peer := id.Public().DestinationHash()
	for i := range 3 {
		_, err := s.AppendMessage(t.Context(), storage.HistoryEntry{
			Peer:      peer,
			Direction: "out",
			Kind:      1,
			Body:      []byte{byte('a' + i)},
			Status:    0,
			When:      time.Now().Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	hist, err := s.LoadHistory(t.Context(), peer, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("len = %d", len(hist))
	}
	// Oldest first.
	if string(hist[0].Body) != "a" {
		t.Fatalf("first body = %q", hist[0].Body)
	}
}

func TestStore_DeleteContact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		wipeHistory bool
		wantHistory int
	}{
		{name: "keep history", wipeHistory: false, wantHistory: 2},
		{name: "wipe history", wipeHistory: true, wantHistory: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := mkStore(t)
			id, _ := identity.Generate(rand.Reader)
			other, _ := identity.Generate(rand.Reader)
			peer := id.Public().DestinationHash()
			otherPeer := other.Public().DestinationHash()

			seed := storage.Contact{
				Hash:        peer,
				Public:      id.Public(),
				Alias:       "victim",
				Fingerprint: id.Public().Fingerprint(),
				AddedAt:     time.Now().UTC(),
			}
			if err := s.UpsertContact(t.Context(), seed); err != nil {
				t.Fatal(err)
			}
			otherSeed := seed
			otherSeed.Hash = otherPeer
			otherSeed.Public = other.Public()
			otherSeed.Alias = "bystander"
			if err := s.UpsertContact(t.Context(), otherSeed); err != nil {
				t.Fatal(err)
			}

			for i := range 2 {
				if _, err := s.AppendMessage(t.Context(), storage.HistoryEntry{
					Peer:      peer,
					Direction: "out",
					Kind:      1,
					Body:      []byte{byte('a' + i)},
					When:      time.Now(),
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.AppendMessage(t.Context(), storage.HistoryEntry{
				Peer:      otherPeer,
				Direction: "out",
				Kind:      1,
				Body:      []byte("keep"),
				When:      time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddOutboxItem(t.Context(), peer, []byte("pending-1")); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddOutboxItem(t.Context(), peer, []byte("pending-2")); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddOutboxItem(t.Context(), otherPeer, []byte("keep")); err != nil {
				t.Fatal(err)
			}

			err := s.DeleteContact(t.Context(), peer, storage.DeleteContactOptions{WipeHistory: tt.wipeHistory})
			if err != nil {
				t.Fatalf("DeleteContact: %v", err)
			}

			if _, err := s.GetContact(t.Context(), peer); err == nil {
				t.Fatal("contact still present after delete")
			}

			pending, err := s.PendingForPeer(t.Context(), peer)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 0 {
				t.Fatalf("outbox not cleared: %d items remain", len(pending))
			}

			hist, err := s.LoadHistory(t.Context(), peer, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(hist) != tt.wantHistory {
				t.Fatalf("history len = %d, want %d", len(hist), tt.wantHistory)
			}

			otherContact, err := s.GetContact(t.Context(), otherPeer)
			if err != nil || otherContact.Alias != "bystander" {
				t.Fatalf("bystander contact disturbed: %+v err=%v", otherContact, err)
			}
			otherHist, err := s.LoadHistory(t.Context(), otherPeer, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(otherHist) != 1 {
				t.Fatalf("bystander history len = %d, want 1", len(otherHist))
			}
			otherPending, err := s.PendingForPeer(t.Context(), otherPeer)
			if err != nil {
				t.Fatal(err)
			}
			if len(otherPending) != 1 {
				t.Fatalf("bystander outbox len = %d, want 1", len(otherPending))
			}
		})
	}
}

func TestStore_DeleteContact_Missing(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	id, _ := identity.Generate(rand.Reader)
	peer := id.Public().DestinationHash()

	if err := s.DeleteContact(t.Context(), peer, storage.DeleteContactOptions{}); err == nil {
		t.Fatal("expected error for missing contact")
	}
}

func TestStore_Outbox(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	id, _ := identity.Generate(rand.Reader)
	peer := id.Public().DestinationHash()
	id1, err := s.AddOutboxItem(t.Context(), peer, []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddOutboxItem(t.Context(), peer, []byte("b")); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingForPeer(t.Context(), peer)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("len = %d", len(pending))
	}
	if err := s.IncrementOutboxAttempts(t.Context(), id1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOutboxItem(t.Context(), id1); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.PendingForPeer(t.Context(), peer)
	if len(pending) != 1 {
		t.Fatalf("after delete, len = %d", len(pending))
	}
}
