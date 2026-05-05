package storage_test

import "testing"

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
