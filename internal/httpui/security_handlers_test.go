package httpui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/httpui/auth"
)

func TestAuthState_Loopback(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	body, err := getJSON(alice, "/api/auth/state")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		AuthMode               string `json:"auth_mode"`
		PassphraseSet          bool   `json:"passphrase_set"`
		TOTPEnrolled           bool   `json:"totp_enrolled"`
		RecoveryCodesRemaining int    `json:"recovery_codes_remaining"`
		CurrentSessionID       string `json:"current_session_id"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if st.AuthMode != "loopback" {
		t.Errorf("auth_mode = %q, want loopback", st.AuthMode)
	}
	if st.PassphraseSet || st.TOTPEnrolled || st.CurrentSessionID != "" {
		t.Errorf("unexpected non-empty state: %+v", st)
	}
}

func TestAuthState_PassphraseSetReflected(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	if err := auth.SetPassphrase(ctx, alice.mngr.Storage(), "correcthorsebatterystaple!"); err != nil {
		t.Fatal(err)
	}
	body, err := getJSON(alice, "/api/auth/state")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"passphrase_set":true`) {
		t.Fatalf("state missing passphrase_set true: %s", body)
	}
}

func TestAuthChangePassphrase_RejectsBadOld(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)

	if err := auth.SetPassphrase(ctx, alice.mngr.Storage(), "originalpass-12!"); err != nil {
		t.Fatal(err)
	}
	resp, err := postJSONResp(alice, "/api/auth/change-passphrase", map[string]any{
		"old": "wrongpassword!", "new": "newpasswordok123",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuthChangePassphrase_TooShort(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)
	if err := auth.SetPassphrase(ctx, alice.mngr.Storage(), "originalpass-12!"); err != nil {
		t.Fatal(err)
	}
	resp, err := postJSONResp(alice, "/api/auth/change-passphrase", map[string]any{
		"old": "originalpass-12!", "new": "short",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestAuthChangePassphrase_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)
	if err := auth.SetPassphrase(ctx, alice.mngr.Storage(), "originalpass-12!"); err != nil {
		t.Fatal(err)
	}
	if _, err := postJSON(alice, "/api/auth/change-passphrase", map[string]any{
		"old": "originalpass-12!", "new": "supersecret-1234",
	}); err != nil {
		t.Fatalf("change: %v", err)
	}
	creds, err := alice.mngr.Storage().GetAuthCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := auth.VerifyPassphrase(creds.PassphraseHash, "supersecret-1234")
	if err != nil || !ok {
		t.Fatalf("new passphrase did not verify: ok=%v err=%v", ok, err)
	}
}

func TestTOTPEnrollFlow(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)
	if err := auth.SetPassphrase(ctx, alice.mngr.Storage(), "pass-1234567890!"); err != nil {
		t.Fatal(err)
	}

	body, err := postJSON(alice, "/api/auth/totp/start", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		EnrollID   string `json:"enroll_id"`
		SecretB32  string `json:"secret_b32"`
		OTPAuthURL string `json:"otpauth_url"`
	}
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatal(err)
	}
	if start.EnrollID == "" || start.SecretB32 == "" || start.OTPAuthURL == "" {
		t.Fatalf("empty start payload: %+v", start)
	}
	secret, err := auth.DecodeTOTPSecret(start.SecretB32)
	if err != nil {
		t.Fatal(err)
	}

	// Bad code → 400; enroll_id is consumed on the failed attempt, so
	// /finish requires a fresh /start to retry.
	resp, err := postJSONResp(alice, "/api/auth/totp/finish", map[string]any{
		"enroll_id": start.EnrollID, "code": "000000",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad code: status %d", resp.StatusCode)
	}

	// Real code → success + recovery codes returned (fresh enroll cycle).
	body, err = postJSON(alice, "/api/auth/totp/start", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatal(err)
	}
	secret, err = auth.DecodeTOTPSecret(start.SecretB32)
	if err != nil {
		t.Fatal(err)
	}
	good := auth.CurrentTOTP(secret, timeNow())
	body, err = postJSON(alice, "/api/auth/totp/finish", map[string]any{
		"enroll_id": start.EnrollID, "code": good,
	})
	if err != nil {
		t.Fatal(err)
	}
	var finish struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(body, &finish); err != nil {
		t.Fatal(err)
	}
	if len(finish.RecoveryCodes) == 0 {
		t.Fatalf("expected recovery codes")
	}

	// State now reports enrolled.
	body, _ = getJSON(alice, "/api/auth/state")
	if !strings.Contains(string(body), `"totp_enrolled":true`) {
		t.Fatalf("state did not flip enrolled: %s", body)
	}
}

func TestTOTPDisable_RequiresStepUp(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)
	enrollTOTP(t, alice, ctx, "pass-1234567890!")
	creds, err := alice.mngr.Storage().GetAuthCredentials(ctx)
	if err != nil || creds == nil {
		t.Fatalf("creds: %v", err)
	}

	// no creds → 401
	resp, err := postJSONResp(alice, "/api/auth/totp/disable", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}

	// Passphrase ALONE must NOT disable TOTP — that defeats 2FA.
	resp, err = postJSONResp(alice, "/api/auth/totp/disable", map[string]any{
		"passphrase": "pass-1234567890!",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("passphrase-only must reject; got status %d", resp.StatusCode)
	}

	// Current TOTP code + passphrase succeeds.
	good := auth.CurrentTOTP(creds.TOTPSecret, time.Now().Add(2*time.Second))
	if _, err := postJSON(alice, "/api/auth/totp/disable", map[string]any{
		"passphrase": "pass-1234567890!", "code": good,
	}); err != nil {
		t.Fatalf("totp+passphrase: %v", err)
	}
	body, _ := getJSON(alice, "/api/auth/state")
	if strings.Contains(string(body), `"totp_enrolled":true`) {
		t.Fatalf("totp not disabled: %s", body)
	}
}

func TestRecoveryRegen_ReplacesCodes(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)
	enrollTOTP(t, alice, ctx, "pass-1234567890!")
	creds, _ := alice.mngr.Storage().GetAuthCredentials(ctx)

	// Passphrase alone must NOT be enough to regenerate recovery codes.
	resp, err := postJSONResp(alice, "/api/auth/recovery/regenerate", map[string]any{
		"passphrase": "pass-1234567890!",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("passphrase-only must reject; got %d", resp.StatusCode)
	}

	good := auth.CurrentTOTP(creds.TOTPSecret, time.Now().Add(2*time.Second))
	body, err := postJSON(alice, "/api/auth/recovery/regenerate", map[string]any{
		"passphrase": "pass-1234567890!", "code": good,
	})
	if err != nil {
		t.Fatal(err)
	}
	var rresp struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(body, &rresp); err != nil {
		t.Fatal(err)
	}
	if len(rresp.RecoveryCodes) == 0 {
		t.Fatalf("no recovery codes in response: %s", body)
	}
	count, err := alice.mngr.Storage().CountUnconsumedRecoveryCodes(ctx)
	if err != nil || count != len(rresp.RecoveryCodes) {
		t.Fatalf("count=%d returned=%d err=%v", count, len(rresp.RecoveryCodes), err)
	}
}

func TestAuthSessions_LoopbackEmpty(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)
	body, err := getJSON(alice, "/api/auth/sessions")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"sessions":[]`) {
		t.Fatalf("loopback should return empty sessions: %s", body)
	}
}

func TestAuthLog_Tail(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alice := startMessenger(t, ctx, "alice", dir, "127.0.0.1:0", "127.0.0.1:0", nil)
	body, err := getJSON(alice, "/api/auth/log?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"entries"`) {
		t.Fatalf("missing entries field: %s", body)
	}
}
