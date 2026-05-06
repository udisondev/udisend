package httpui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/httpui/auth"
	"github.com/udisondev/udisend/internal/storage"
)

type loginFixture struct {
	t          *testing.T
	store      *storage.Store
	handlers   *authHandlers
	server     *httptest.Server
	clock      *atomic.Int64
	totpSecret []byte
}

type loginFixtureOpts struct {
	enrollTOTP bool
	clientIP   string // override; default 198.51.100.7
}

func newLoginFixture(t *testing.T, opts loginFixtureOpts) *loginFixture {
	t.Helper()

	store, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	hash, err := auth.HashPassphrase("hunter2-very-long-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPassphrase(t.Context(), hash); err != nil {
		t.Fatal(err)
	}

	var totpSecret []byte
	if opts.enrollTOTP {
		secret, _, err := auth.GenerateTOTPSecret()
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetTOTPSecret(t.Context(), secret); err != nil {
			t.Fatal(err)
		}
		totpSecret = secret
	}

	clock := &atomic.Int64{}
	clock.Store(1700000000)
	now := func() time.Time { return time.Unix(clock.Load(), 0).UTC() }

	sessions := auth.NewSessions(store, auth.SessionsConfig{
		Idle:         24 * time.Hour,
		RollingTouch: 5 * time.Minute,
		Now:          now,
	})
	limiter := &auth.RateLimiter{
		PerMinute:    5,
		FailsToLock:  20,
		LockDuration: 15 * time.Minute,
		Now:          now,
	}

	h := &authHandlers{
		store:        store,
		sessions:     sessions,
		limiter:      limiter,
		now:          now,
		secureCookie: false,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", h.handleLoginGET)
	mux.HandleFunc("POST /login", h.handleLoginPOST)
	mux.HandleFunc("POST /logout", h.handleLogout)
	mux.Handle("GET /protected", h.requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &loginFixture{
		t:          t,
		store:      store,
		handlers:   h,
		server:     srv,
		clock:      clock,
		totpSecret: totpSecret,
	}
}

func (f *loginFixture) post(path string, form url.Values, sessionCookie string) *http.Response {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	// X-Requested-With is required by requireCSRFHeader on most state-changing
	// endpoints; login itself doesn't gate on it but setting it keeps the
	// fixture uniform across tests that hit logout / api/* paths.
	req.Header.Set("X-Requested-With", "udisend")
	if sessionCookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieNameSession, Value: sessionCookie})
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}

	return resp
}

func (f *loginFixture) get(path, sessionCookie string) *http.Response {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.server.URL+path, nil)
	if sessionCookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieNameSession, Value: sessionCookie})
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}

	return resp
}

func extractSessionCookie(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == cookieNameSession {
			return c.Value
		}
	}
	t.Fatal("no session cookie set")

	return ""
}

func TestLoginGET_RendersForm(t *testing.T) {
	t.Parallel()
	f := newLoginFixture(t, loginFixtureOpts{})

	resp := f.get("/login", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
}

func TestLoginPOST_HappyPath_NoTOTP(t *testing.T) {
	t.Parallel()
	f := newLoginFixture(t, loginFixtureOpts{})

	resp := f.post("/login", url.Values{"passphrase": {"hunter2-very-long-passphrase"}}, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 SeeOther", resp.StatusCode)
	}
	cookie := extractSessionCookie(t, resp)

	resp2 := f.get("/protected", cookie)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("/protected with cookie = %d, want 200", resp2.StatusCode)
	}
}

func TestLoginPOST_WrongPassphrase(t *testing.T) {
	t.Parallel()
	f := newLoginFixture(t, loginFixtureOpts{})

	resp := f.post("/login", url.Values{"passphrase": {"wrong"}}, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == cookieNameSession && c.Value != "" {
			t.Errorf("session cookie set on failed login")
		}
	}
}

func TestLoginPOST_RateLimitsAfterFiveAttempts(t *testing.T) {
	t.Parallel()
	f := newLoginFixture(t, loginFixtureOpts{})

	for i := range 5 {
		resp := f.post("/login", url.Values{"passphrase": {"wrong"}}, "")
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("attempt %d already 429, expected 401 within window", i+1)
		}
	}
	resp := f.post("/login", url.Values{"passphrase": {"hunter2-very-long-passphrase"}}, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("6th attempt status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Errorf("missing Retry-After header on 429")
	}
}

func TestLoginPOST_WithTOTP_RequiresCode(t *testing.T) {
	t.Parallel()
	f := newLoginFixture(t, loginFixtureOpts{enrollTOTP: true})

	resp := f.post("/login", url.Values{"passphrase": {"hunter2-very-long-passphrase"}}, "")
	defer resp.Body.Close()
	// Passphrase right, TOTP missing — caller has to retry with totp field.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (totp required)", resp.StatusCode)
	}
}

func TestLoginPOST_WithTOTP_ValidCodeSucceeds(t *testing.T) {
	t.Parallel()
	f := newLoginFixture(t, loginFixtureOpts{enrollTOTP: true})

	now := time.Unix(f.clock.Load(), 0).UTC()
	code := computeTOTPForTest(t, f.totpSecret, now)
	resp := f.post("/login", url.Values{
		"passphrase": {"hunter2-very-long-passphrase"},
		"totp":       {code},
	}, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	_ = extractSessionCookie(t, resp)
}

// TestLoginPOST_TOTPCodeNotReusableSameStep covers the step-replay window:
// without persisting the consumed step, an attacker who shoulder-surfed
// (or bus-sniffed via a malicious USB hub) the user's TOTP code can race
// to /login with the same code as long as the 30s step is current. The
// step-up gate at security_handlers.requireSecondFactor already does
// step-replay tracking; /login MUST do the same.
func TestLoginPOST_TOTPCodeNotReusableSameStep(t *testing.T) {
	t.Parallel()
	f := newLoginFixture(t, loginFixtureOpts{enrollTOTP: true})

	now := time.Unix(f.clock.Load(), 0).UTC()
	code := computeTOTPForTest(t, f.totpSecret, now)

	// First login succeeds.
	resp := f.post("/login", url.Values{
		"passphrase": {"hunter2-very-long-passphrase"},
		"totp":       {code},
	}, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("first login status = %d, want 303", resp.StatusCode)
	}

	// Same code, same step, fresh request — MUST be rejected.
	resp2 := f.post("/login", url.Values{
		"passphrase": {"hunter2-very-long-passphrase"},
		"totp":       {code},
	}, "")
	_ = resp2.Body.Close()
	if resp2.StatusCode == http.StatusSeeOther {
		t.Fatalf("replayed TOTP code accepted; want 401")
	}
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("replay status = %d, want 401", resp2.StatusCode)
	}
}

// TestLoginPOST_TOTPReplayFailsClosedOnMalformedStep verifies the TOTP
// step gate fails closed when the persisted last-step value cannot be
// parsed. The previous behaviour silently treated parse errors as
// "step 0" — the comparison `0 >= step` was always false and the same
// code re-passed. Documented contract: a transient/malformed replay
// store must NOT erase the replay window.
func TestLoginPOST_TOTPReplayFailsClosedOnMalformedStep(t *testing.T) {
	t.Parallel()
	f := newLoginFixture(t, loginFixtureOpts{enrollTOTP: true})

	if err := f.store.SetSetting(t.Context(), "auth.last_totp_step", "not-a-number"); err != nil {
		t.Fatalf("seed malformed setting: %v", err)
	}

	now := time.Unix(f.clock.Load(), 0).UTC()
	code := computeTOTPForTest(t, f.totpSecret, now)

	resp := f.post("/login", url.Values{
		"passphrase": {"hunter2-very-long-passphrase"},
		"totp":       {code},
	}, "")
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("login accepted with malformed replay store; want fail-closed (401)")
	}
}

func TestLoginPOST_RecoveryCodePath(t *testing.T) {
	t.Parallel()
	f := newLoginFixture(t, loginFixtureOpts{enrollTOTP: true})

	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		h, err := auth.HashRecoveryCode(c)
		if err != nil {
			t.Fatal(err)
		}
		hashes[i] = h
	}
	if err := f.store.ResetRecoveryCodes(t.Context(), hashes); err != nil {
		t.Fatal(err)
	}

	resp := f.post("/login", url.Values{
		"passphrase": {"hunter2-very-long-passphrase"},
		"totp":       {codes[3]},
	}, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recovery-code login status = %d, want 303", resp.StatusCode)
	}

	rows, _ := f.store.UnconsumedRecoveryCodes(t.Context())
	if len(rows) != len(codes)-1 {
		t.Errorf("unconsumed = %d, want %d", len(rows), len(codes)-1)
	}

	// Same recovery code must not work twice.
	resp2 := f.post("/login", url.Values{
		"passphrase": {"hunter2-very-long-passphrase"},
		"totp":       {codes[3]},
	}, "")
	_ = resp2.Body.Close()
	if resp2.StatusCode == http.StatusSeeOther {
		t.Errorf("consumed recovery code reused successfully")
	}
}

func TestProtected_RequiresSession(t *testing.T) {
	t.Parallel()
	f := newLoginFixture(t, loginFixtureOpts{})

	resp := f.get("/protected", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestLoginPOST_BodyTooLargeRejected(t *testing.T) {
	t.Parallel()
	f := newLoginFixture(t, loginFixtureOpts{})

	huge := strings.Repeat("x", loginBodyMaxBytes+10)
	resp := f.post("/login", url.Values{"passphrase": {huge}}, "")
	defer resp.Body.Close()
	// MaxBytesReader fires inside ParseForm and the handler responds 400.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for oversize body", resp.StatusCode)
	}
}

func TestLoginPOST_CookieAttributes(t *testing.T) {
	t.Parallel()
	// secureCookie=true exercises the production cookie shape; the test
	// fixture uses httptest (HTTP), but Go just sets the attribute strings —
	// browsers enforce, not the server.
	f := newLoginFixture(t, loginFixtureOpts{})
	f.handlers.secureCookie = true

	resp := f.post("/login", url.Values{"passphrase": {"hunter2-very-long-passphrase"}}, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	var sc *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == cookieNameSession {
			sc = c
			break
		}
	}
	if sc == nil {
		t.Fatal("session cookie missing")
	}
	if !sc.HttpOnly {
		t.Errorf("cookie HttpOnly = false, want true")
	}
	if !sc.Secure {
		t.Errorf("cookie Secure = false, want true (with secureCookie:true)")
	}
	if sc.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie SameSite = %v, want Strict", sc.SameSite)
	}
	if sc.Path != "/" {
		t.Errorf("cookie Path = %q, want /", sc.Path)
	}
	if sc.MaxAge <= 0 {
		t.Errorf("cookie MaxAge = %d, want > 0", sc.MaxAge)
	}
}

// TestCheckLoginOrigin pins the conditional same-origin contract for
// /login: strict in trusted-cert modes, tolerant of `Origin: null` in
// lan-ip mode where browsers downgrade to opaque origin after a
// self-signed cert override.
func TestCheckLoginOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		host              string
		origin            string
		allowOpaqueOrigin bool
		want              bool
	}{
		{"missing origin allowed", "messenger.example.com", "", false, true},
		{"same origin", "messenger.example.com", "https://messenger.example.com", false, true},
		{"different host blocked", "messenger.example.com", "https://evil.example.org", false, false},
		{"different scheme same host allowed", "messenger.example.com", "http://messenger.example.com", false, true},
		{"malformed origin blocked", "messenger.example.com", "::not a url::", false, false},
		// Opaque origin behavior — the regression path for lan-ip.
		{"null rejected in trusted-cert mode", "192.168.0.105:8443", "null", false, false},
		{"null accepted in self-signed mode", "192.168.0.105:8443", "null", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := &authHandlers{allowOpaqueOrigin: tt.allowOpaqueOrigin}
			r, _ := http.NewRequest(http.MethodPost, "/login", nil)
			r.Host = tt.host
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			got, reason := h.checkLoginOrigin(r)
			if got != tt.want {
				t.Errorf("checkLoginOrigin(host=%q origin=%q allow=%v) = %v (reason %q), want %v",
					tt.host, tt.origin, tt.allowOpaqueOrigin, got, reason, tt.want)
			}
		})
	}
}

func TestLogout_DeletesCookieAndSession(t *testing.T) {
	t.Parallel()
	f := newLoginFixture(t, loginFixtureOpts{})

	resp := f.post("/login", url.Values{"passphrase": {"hunter2-very-long-passphrase"}}, "")
	_ = resp.Body.Close()
	cookie := extractSessionCookie(t, resp)

	resp = f.post("/logout", url.Values{}, cookie)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout status = %d, want 303", resp.StatusCode)
	}

	resp2 := f.get("/protected", cookie)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("after logout /protected = %d, want 401", resp2.StatusCode)
	}
}

// computeTOTPForTest mirrors the production routine via the auth package's
// public CurrentTOTP, so the test exercises the same code path the
// authenticator app would.
func computeTOTPForTest(t *testing.T, secret []byte, now time.Time) string {
	t.Helper()

	return auth.CurrentTOTP(secret, now)
}
