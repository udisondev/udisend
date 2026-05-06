package httpui_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/httpui/auth"
)

// timeNow centralises the "now" used by TOTP test helpers so we can
// later swap to a fake clock if needed.
func timeNow() time.Time { return time.Now() }

// nextTOTPStepTime returns a moment safely inside the TOTP step
// immediately after the one containing now. Used by step-up tests
// to compose a code that (a) is NOT the enrollment step (replay
// protection persists last-used-step in storage and rejects equal
// or earlier steps), and (b) lands inside the server's ±1-step skew
// window so the validator accepts it regardless of which side of a
// boundary the test scheduler happens to land on.
//
// Picking a fixed Δ (e.g. now+35s) is brittle: depending on now %
// 30, +35s may fall in step S+1 or step S+2 — the latter is outside
// the skew window and the server rejects. Truncating to the step
// boundary first eliminates the phase dependency.
func nextTOTPStepTime(now time.Time) time.Time {
	const period = 30 * time.Second

	return now.Truncate(period).Add(period + time.Second)
}

func enrollTOTP(t *testing.T, p *peer, ctx context.Context, passphrase string) {
	t.Helper()
	if err := auth.SetPassphrase(ctx, p.mngr.Storage(), passphrase); err != nil {
		t.Fatal(err)
	}
	body, err := postJSON(p, "/api/auth/totp/start", map[string]any{
		"passphrase": passphrase,
	})
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		EnrollID  string `json:"enroll_id"`
		SecretB32 string `json:"secret_b32"`
	}
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatal(err)
	}
	secret, err := auth.DecodeTOTPSecret(start.SecretB32)
	if err != nil {
		t.Fatal(err)
	}
	good := auth.CurrentTOTP(secret, timeNow())
	if _, err := postJSON(p, "/api/auth/totp/finish", map[string]any{
		"enroll_id": start.EnrollID, "code": good,
	}); err != nil {
		t.Fatal(err)
	}
}
