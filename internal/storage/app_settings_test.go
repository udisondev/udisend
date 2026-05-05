package storage_test

import (
	"testing"
)

func TestAppSettings_Delete(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()

	if err := s.SetSetting(ctx, "deploy.mode", "lan-ip"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetSetting(ctx, "deploy.mode"); !ok {
		t.Fatal("setup: key must be present before delete")
	}

	if err := s.DeleteSetting(ctx, "deploy.mode"); err != nil {
		t.Fatalf("DeleteSetting: %v", err)
	}
	if _, ok, _ := s.GetSetting(ctx, "deploy.mode"); ok {
		t.Fatal("DeleteSetting did not remove the row")
	}

	// Idempotent on missing key.
	if err := s.DeleteSetting(ctx, "deploy.mode"); err != nil {
		t.Fatalf("DeleteSetting idempotent: %v", err)
	}
}

func TestAppSettings_GetSet(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()

	v, ok, err := s.GetSetting(ctx, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected !ok for missing key, got %q", v)
	}

	if err := s.SetSetting(ctx, "log.level", "debug"); err != nil {
		t.Fatal(err)
	}
	v, ok, err = s.GetSetting(ctx, "log.level")
	if err != nil || !ok || v != "debug" {
		t.Fatalf("get after set: v=%q ok=%v err=%v", v, ok, err)
	}

	if err := s.SetSetting(ctx, "log.level", "info"); err != nil {
		t.Fatal(err)
	}
	v, _, _ = s.GetSetting(ctx, "log.level")
	if v != "info" {
		t.Fatalf("upsert did not replace: %q", v)
	}
}
