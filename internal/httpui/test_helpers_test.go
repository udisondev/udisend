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
