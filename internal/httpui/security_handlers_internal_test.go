package httpui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/httpui/auth"
	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/internal/storage"
)

// TestRequireSecondFactor_FailClosedOnMalformedStep mirrors the login
// path's TestLoginPOST_TOTPReplayFailsClosedOnMalformedStep but covers
// the step-up gate (used by /api/auth/change-passphrase, identity
// export, etc.). A malformed `auth.last_totp_step` setting MUST refuse
// the request rather than silently degrade to step=0 (which always
// passes the `u >= step` test and lets a leaked TOTP code be replayed
// on a destructive operation).
func TestRequireSecondFactor_FailClosedOnMalformedStep(t *testing.T) {
	t.Parallel()

	store, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "step.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	secret, _, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(t.Context(), "auth.last_totp_step", "not-a-number"); err != nil {
		t.Fatal(err)
	}

	mngr := messenger.Open(messenger.Config{Storage: store})
	t.Cleanup(mngr.Close)

	srv := &Server{mngr: mngr}
	creds := &storage.AuthCredentials{TOTPSecret: secret}
	code := computeTOTPForTest(t, secret, time.Now())

	err = srv.requireSecondFactor(t.Context(), creds, code, "")
	if err == nil {
		t.Fatalf("requireSecondFactor accepted with malformed replay store; want error")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("err = %v, want it to mention 'malformed' (replay store corruption signal)", err)
	}
	// A second call with the same valid code MUST also fail (not turned
	// into step=0 silently).
	if err2 := srv.requireSecondFactor(t.Context(), creds, code, ""); err2 == nil {
		t.Errorf("second call accepted; replay store remained malformed and should still fail")
	} else if errors.Is(err2, errors.New("TOTP code already used")) {
		t.Errorf("second call rejected via replay-window path; expected the malformed-store path")
	}
}
