package storage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/storage"
)

func TestBootstrapOverrides_AddListRemove(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()

	if err := s.AddBootstrapOverride(ctx, "1.2.3.4:9000", "alice's relay"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.AddBootstrapOverride(ctx, "[2001:db8::1]:9000", ""); err != nil {
		t.Fatalf("add ipv6: %v", err)
	}

	got, err := s.ListBootstrapOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	for _, b := range got {
		if !b.Enabled {
			t.Errorf("%s: expected enabled=true on fresh insert", b.Address)
		}
		if b.LastStatus != "" || !b.LastStatusAt.IsZero() {
			t.Errorf("%s: expected blank status on fresh insert (got %q / %v)", b.Address, b.LastStatus, b.LastStatusAt)
		}
	}

	if err := s.RemoveBootstrapOverride(ctx, "1.2.3.4:9000"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, err = s.ListBootstrapOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Address != "[2001:db8::1]:9000" {
		t.Fatalf("unexpected list after remove: %#v", got)
	}

	if err := s.RemoveBootstrapOverride(ctx, "missing:1"); !errors.Is(err, storage.ErrBootstrapOverrideNotFound) {
		t.Fatalf("remove missing: got %v, want ErrBootstrapOverrideNotFound", err)
	}
}

func TestBootstrapOverrides_AddIsIdempotent(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()

	if err := s.AddBootstrapOverride(ctx, "1.2.3.4:9000", "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBootstrapOverrideEnabled(ctx, "1.2.3.4:9000", false); err != nil {
		t.Fatal(err)
	}
	// Re-add with a different note: row stays, enabled flips back on, note refreshed.
	if err := s.AddBootstrapOverride(ctx, "1.2.3.4:9000", "second"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListBootstrapOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if !got[0].Enabled {
		t.Error("re-add should re-enable a disabled row")
	}
	if got[0].Note != "second" {
		t.Errorf("note = %q, want %q", got[0].Note, "second")
	}
}

func TestBootstrapOverrides_Toggle(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()
	if err := s.AddBootstrapOverride(ctx, "1.2.3.4:9000", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBootstrapOverrideEnabled(ctx, "1.2.3.4:9000", false); err != nil {
		t.Fatal(err)
	}
	enabled, err := s.EnabledBootstrapOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 0 {
		t.Fatalf("expected empty enabled set, got %v", enabled)
	}
	if err := s.SetBootstrapOverrideEnabled(ctx, "1.2.3.4:9000", true); err != nil {
		t.Fatal(err)
	}
	enabled, err = s.EnabledBootstrapOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0] != "1.2.3.4:9000" {
		t.Fatalf("got %v, want [1.2.3.4:9000]", enabled)
	}
	if err := s.SetBootstrapOverrideEnabled(ctx, "missing:1", true); !errors.Is(err, storage.ErrBootstrapOverrideNotFound) {
		t.Fatalf("toggle missing: got %v, want ErrBootstrapOverrideNotFound", err)
	}
}

func TestBootstrapOverrides_MarkStatus(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()

	if err := s.AddBootstrapOverride(ctx, "1.2.3.4:9000", ""); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1715000000, 0).UTC()
	if err := s.MarkBootstrapStatus(ctx, "1.2.3.4:9000", "ok", when); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListBootstrapOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].LastStatus != "ok" || !got[0].LastStatusAt.Equal(when) {
		t.Fatalf("status = %q @ %v, want ok @ %v", got[0].LastStatus, got[0].LastStatusAt, when)
	}

	// Status for a missing row is silently ignored — must not resurrect.
	if err := s.MarkBootstrapStatus(ctx, "ghost:1", "fail", when); err != nil {
		t.Fatalf("mark missing: %v", err)
	}
	got, err = s.ListBootstrapOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("ghost row resurrected: %#v", got)
	}

	// Invalid status string rejected.
	if err := s.MarkBootstrapStatus(ctx, "1.2.3.4:9000", "wat", when); err == nil {
		t.Fatalf("expected error on invalid status")
	}
}

func TestBootstrapOverrides_Exists(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()
	if err := s.AddBootstrapOverride(ctx, "1.2.3.4:9000", ""); err != nil {
		t.Fatal(err)
	}
	yes, err := s.BootstrapOverrideExists(ctx, "1.2.3.4:9000")
	if err != nil || !yes {
		t.Fatalf("exists yes: yes=%v err=%v", yes, err)
	}
	no, err := s.BootstrapOverrideExists(ctx, "missing:1")
	if err != nil || no {
		t.Fatalf("exists no: no=%v err=%v", no, err)
	}
}
