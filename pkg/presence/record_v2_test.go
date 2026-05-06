package presence_test

import (
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/presence"
)

// TestRecord_V2RoundtripUptimeAndSlots verifies UptimeHint / MaxRelaySlots
// survive marshal/unmarshal and are covered by the signature (a tampered
// uptime fails Verify).
func TestRecord_V2RoundtripUptimeAndSlots(t *testing.T) {
	t.Parallel()
	id := mustID(t)
	original := presence.Record{
		Address:       "203.0.113.5:9000",
		Capabilities:  presence.CapPublicIP | presence.CapCanSTUN | presence.CapCanTURN,
		IssuedAt:      time.Now().Truncate(time.Second).UTC(),
		UptimeHint:    7200, // 2 hours
		MaxRelaySlots: 64,
	}
	if err := original.Sign(id); err != nil {
		t.Fatal(err)
	}
	blob, err := original.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	var got presence.Record
	if err := got.UnmarshalBinary(blob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.UptimeHint != original.UptimeHint {
		t.Errorf("UptimeHint: got %d, want %d", got.UptimeHint, original.UptimeHint)
	}
	if got.MaxRelaySlots != original.MaxRelaySlots {
		t.Errorf("MaxRelaySlots: got %d, want %d", got.MaxRelaySlots, original.MaxRelaySlots)
	}
	if err := got.Verify(time.Now().UTC(), 0); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Tamper UptimeHint and re-encode without re-signing — Verify must fail.
	got.UptimeHint = 999999
	tamperedBlob, err := got.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var tampered presence.Record
	if err := tampered.UnmarshalBinary(tamperedBlob); err != nil {
		t.Fatal(err)
	}
	if err := tampered.Verify(time.Now().UTC(), 0); err == nil {
		t.Fatal("Verify accepted tampered UptimeHint")
	}
}
