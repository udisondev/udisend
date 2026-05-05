package storage_test

import (
	"errors"
	"testing"

	"github.com/udisondev/udisend/internal/storage"
)

func TestICEOverrides_AddListRemoveToggle(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()

	if err := s.AddICEOverride(ctx, storage.ICEOverride{URL: "stun:relay.example.com:3478"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddICEOverride(ctx, storage.ICEOverride{
		URL: "turn:relay.example.com:3479", Username: "alice", Credential: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListICEOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(all), all)
	}
	for _, e := range all {
		if !e.Enabled {
			t.Errorf("%s should be enabled by default", e.URL)
		}
	}

	if err := s.SetICEOverrideEnabled(ctx, "stun:relay.example.com:3478", false); err != nil {
		t.Fatal(err)
	}
	enabled, err := s.EnabledICEOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0].URL != "turn:relay.example.com:3479" {
		t.Fatalf("enabled = %#v", enabled)
	}

	if err := s.RemoveICEOverride(ctx, "turn:relay.example.com:3479"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveICEOverride(ctx, "missing:1"); !errors.Is(err, storage.ErrICEOverrideNotFound) {
		t.Fatalf("remove missing: %v", err)
	}
}

func TestICEOverrides_AddIsIdempotent(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()
	if err := s.AddICEOverride(ctx, storage.ICEOverride{URL: "turn:host:1", Username: "old", Credential: "p1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetICEOverrideEnabled(ctx, "turn:host:1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.AddICEOverride(ctx, storage.ICEOverride{URL: "turn:host:1", Username: "new", Credential: "p2"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListICEOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("len = %d", len(all))
	}
	if !all[0].Enabled {
		t.Error("re-add should re-enable")
	}
	if all[0].Username != "new" || all[0].Credential != "p2" {
		t.Errorf("creds not refreshed: %#v", all[0])
	}
}

func TestStorageCounters(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()

	for _, c := range []*int{nil} {
		_ = c
	}

	mc, err := s.CountMessages(ctx)
	if err != nil || mc != 0 {
		t.Fatalf("CountMessages on empty: %d %v", mc, err)
	}
	cc, err := s.CountContacts(ctx)
	if err != nil || cc != 0 {
		t.Fatalf("CountContacts on empty: %d %v", cc, err)
	}
	ob, err := s.CountOutbox(ctx)
	if err != nil || ob != 0 {
		t.Fatalf("CountOutbox on empty: %d %v", ob, err)
	}
	mb, err := s.MessageBytes(ctx)
	if err != nil || mb != 0 {
		t.Fatalf("MessageBytes on empty: %d %v", mb, err)
	}
}

func TestPruneMessagesOlderThan(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()

	mc, err := s.CountMessages(ctx)
	if err != nil || mc != 0 {
		t.Fatalf("expected empty: %d %v", mc, err)
	}
	n, err := s.PruneMessagesOlderThan(ctx, 0)
	if err != nil || n != 0 {
		t.Fatalf("prune empty: %d %v", n, err)
	}
}

func TestVacuumOnEmpty(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	if err := s.Vacuum(t.Context()); err != nil {
		t.Fatalf("vacuum empty store: %v", err)
	}
}
