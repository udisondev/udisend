package httpui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/udisondev/udisend/internal/httpui/auth"
	"github.com/udisondev/udisend/internal/storage"
)

// sensitiveBodyMaxBytes caps every JSON body that drives an Argon2id
// verify or a TOTP step-up. The Argon2id parameters peak at ~64 MiB of
// memory per attempt; without this cap an authenticated attacker can
// drive the host into OOM with a single request.
const sensitiveBodyMaxBytes = 4 * 1024

// pendingTOTPTTL bounds how long a /totp/start nonce stays valid before
// /totp/finish must commit. Five minutes is generous for "type the code
// from your phone" but short enough that a forgotten enrollment doesn't
// linger.
const pendingTOTPTTL = 5 * time.Minute

// stateResp is the payload of GET /api/auth/state. The UI uses it to
// drive button visibility (e.g. hide TOTP enroll if already enrolled).
type stateResp struct {
	AuthMode               string `json:"auth_mode"`
	PassphraseSet          bool   `json:"passphrase_set"`
	PassphraseUpdatedAt    int64  `json:"passphrase_updated_at,omitempty"`
	TOTPEnrolled           bool   `json:"totp_enrolled"`
	RecoveryCodesRemaining int    `json:"recovery_codes_remaining"`
	CurrentSessionPublicID string `json:"current_session_public_id,omitempty"`
}

func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	noStoreHeaders(w)
	store := s.mngr.Storage()
	creds, err := store.GetAuthCredentials(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := stateResp{
		AuthMode: s.authModeName(),
	}
	if cur := s.currentSessionID(r); cur != "" {
		resp.CurrentSessionPublicID = publicSessionID(cur)
	}
	if creds != nil {
		resp.PassphraseSet = true
		resp.PassphraseUpdatedAt = creds.UpdatedAt.Unix()
		resp.TOTPEnrolled = len(creds.TOTPSecret) > 0
		if resp.TOTPEnrolled {
			n, err := store.CountUnconsumedRecoveryCodes(r.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			resp.RecoveryCodesRemaining = n
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAuthChangePassphrase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if retry, err := s.stepUpAllow(r); err != nil {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sensitiveBodyMaxBytes)
	var req struct {
		Old        string `json:"old"`
		New        string `json:"new"`
		Code       string `json:"code"`
		Recovery   string `json:"recovery"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	store := s.mngr.Storage()
	creds, err := store.GetAuthCredentials(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if creds == nil {
		http.Error(w, "no passphrase configured — set one via `messenger -set-password`", http.StatusBadRequest)
		return
	}
	ok, err := auth.VerifyPassphrase(creds.PassphraseHash, req.Old)
	if err != nil || !ok {
		s.recordStepUpResult(r, false)
		s.auditIfPublic(r, "change_passphrase_fail", "")
		http.Error(w, "authentication rejected", http.StatusUnauthorized)
		return
	}
	if len(req.New) < auth.MinPassphraseLen {
		http.Error(w, "new passphrase too short", http.StatusBadRequest)
		return
	}
	if err := s.requireSecondFactor(r.Context(), creds, req.Code, req.Recovery); err != nil {
		s.recordStepUpResult(r, false)
		s.auditIfPublic(r, "change_passphrase_fail", "step-up")
		http.Error(w, "authentication rejected", http.StatusUnauthorized)
		return
	}
	s.recordStepUpResult(r, true)

	hash, err := auth.HashPassphrase(req.New)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := store.SetPassphrase(r.Context(), hash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.invalidateOtherSessions(r)
	s.auditIfPublic(r, "change_passphrase_ok", "")

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// pendingTOTP is the in-memory map of TOTP secrets handed out by
// /totp/start but not yet committed by /totp/finish. Keyed by an opaque
// "enroll_id" returned to the browser; the secret never leaves the
// server, so a malicious page cannot substitute its own. Single-user
// deployment ⇒ a tiny map with TTL eviction is enough.
type pendingTOTP struct {
	secret []byte
	expiry time.Time
}

var (
	pendingTOTPMu    sync.Mutex
	pendingTOTPStore = map[string]pendingTOTP{}
)

// pendingTOTPMax bounds the in-memory enrollment-secret table. Single
// user system + step-up gate => an attacker with a stolen session can
// at most fill this many slots before being told to wait.
const pendingTOTPMax = 16

func savePendingTOTP(secret []byte, ttl time.Duration) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(b[:])

	pendingTOTPMu.Lock()
	defer pendingTOTPMu.Unlock()
	for k, v := range pendingTOTPStore {
		if time.Now().After(v.expiry) {
			delete(pendingTOTPStore, k)
		}
	}
	if len(pendingTOTPStore) >= pendingTOTPMax {
		return "", errors.New("too many pending enrollments — wait or finish one")
	}
	dup := make([]byte, len(secret))
	copy(dup, secret)
	pendingTOTPStore[id] = pendingTOTP{secret: dup, expiry: time.Now().Add(ttl)}

	return id, nil
}

func consumePendingTOTP(id string) ([]byte, bool) {
	pendingTOTPMu.Lock()
	defer pendingTOTPMu.Unlock()
	p, ok := pendingTOTPStore[id]
	if !ok {
		return nil, false
	}
	delete(pendingTOTPStore, id)
	if time.Now().After(p.expiry) {
		return nil, false
	}

	return p.secret, true
}

func (s *Server) handleTOTPStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sensitiveBodyMaxBytes)
	var req struct {
		Passphrase string `json:"passphrase"`
		Code       string `json:"code"`
		Recovery   string `json:"recovery"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	store := s.mngr.Storage()
	creds, err := store.GetAuthCredentials(r.Context())
	if err != nil {
		s.logger.Warn("totp start: load creds", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if creds == nil {
		http.Error(w, "set a passphrase before enabling TOTP", http.StatusBadRequest)
		return
	}
	// Always require fresh passphrase reverification — a session cookie
	// alone must not allow a fresh enrollment that would lock out the
	// legitimate user. When TOTP is already enrolled, the second factor
	// is also required (re-enroll).
	if err := s.confirmStepUp(r, req.Code, req.Recovery, req.Passphrase); err != nil {
		s.auditIfPublic(r, "totp_enroll_step_up_fail", "")
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	secret, b32, err := auth.GenerateTOTPSecret()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	enrollID, err := savePendingTOTP(secret, pendingTOTPTTL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	otp := auth.OTPAuthURL(secret, "udisend", s.totpAccountName())

	writeJSON(w, http.StatusOK, map[string]any{
		"enroll_id":   enrollID,
		"secret_b32":  b32,
		"otpauth_url": otp,
	})
}

func (s *Server) handleTOTPFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sensitiveBodyMaxBytes)
	var req struct {
		EnrollID string `json:"enroll_id"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.EnrollID == "" {
		http.Error(w, "enroll_id required", http.StatusBadRequest)
		return
	}
	secret, ok := consumePendingTOTP(req.EnrollID)
	if !ok {
		http.Error(w, "enrollment expired or unknown — start over", http.StatusBadRequest)
		return
	}

	codes, err := auth.FinishTOTPSetup(r.Context(), s.mngr.Storage(), secret, strings.TrimSpace(req.Code), time.Now())
	if err != nil {
		s.auditIfPublic(r, "totp_enroll_fail", "")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.auditIfPublic(r, "totp_enroll_ok", "")
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// requireSecondFactor is the canonical "second factor proven" gate. When
// TOTP is enrolled, accept ONLY a current TOTP code or an unconsumed
// recovery code — passphrase alone is intentionally insufficient
// because the threat model says "stolen passphrase + stolen session"
// and we want the second factor to actually defend the second-factor-
// disable path. When TOTP is NOT enrolled, this gate is a no-op (the
// caller still verified passphrase before getting here).
//
// TOTP code replay protection: the consumed step is persisted; reusing
// the same code (even within the legitimate ±1-step window) is rejected.
// Persistence errors fail closed — a transient DB outage MUST NOT erase
// the replay window or the gate becomes a no-op for the next request.
func (s *Server) requireSecondFactor(ctx context.Context, creds *storage.AuthCredentials, code, recovery string) error {
	if creds == nil {
		return errors.New("no credentials")
	}
	if len(creds.TOTPSecret) == 0 {
		return nil
	}

	code = strings.TrimSpace(code)
	if code != "" {
		step, ok := auth.VerifyTOTPStep(creds.TOTPSecret, code, time.Now())
		if !ok {
			return errors.New("TOTP code rejected")
		}
		used, ok, err := s.mngr.Storage().GetSetting(ctxOrBackground(ctx), settingKeyLastTOTPStep)
		if err != nil {
			return fmt.Errorf("step-up: replay store unavailable: %w", err)
		}
		if ok {
			// Fail closed on parse error: the documented contract for this
			// gate is "a transient outage MUST NOT erase the replay window".
			// Treating an unparseable persisted step as "step 0" silently
			// turns the comparison into 0 >= step (always false), accepting
			// the same code on repeat. Refuse instead.
			u, perr := strconv.ParseInt(used, 10, 64)
			if perr != nil {
				return fmt.Errorf("step-up: replay store malformed: %w", perr)
			}
			if u >= step {
				return errors.New("TOTP code already used")
			}
		}
		if err := s.mngr.Storage().SetSetting(ctxOrBackground(ctx), settingKeyLastTOTPStep, strconv.FormatInt(step, 10)); err != nil {
			return fmt.Errorf("step-up: replay store unavailable: %w", err)
		}

		return nil
	}

	recovery = strings.TrimSpace(recovery)
	if recovery == "" {
		return errors.New("TOTP code or recovery code required")
	}
	rows, err := s.mngr.Storage().UnconsumedRecoveryCodes(ctxOrBackground(ctx))
	if err != nil {
		return err
	}
	for _, row := range rows {
		ok, err := auth.VerifyRecoveryCode(row.Hash, recovery)
		if err != nil || !ok {
			continue
		}
		marked, err := s.mngr.Storage().MarkRecoveryCodeConsumed(ctxOrBackground(ctx), row.ID, time.Now())
		if err != nil || !marked {
			return errors.New("recovery code already used")
		}

		return nil
	}

	return errors.New("recovery code rejected")
}

// settingKeyLastTOTPStep persists the last TOTP step accepted at a
// step-up gate. Replay protection across restart and across steppable
// operations (disable, regen, change-passphrase, identity-export).
const settingKeyLastTOTPStep = "auth.last_totp_step"

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}

	return context.Background()
}

// stepUpAllow is the per-IP gate consulted before confirmStepUp runs
// any Argon2id work. Reuses the /login limiter so a stolen session that
// drives the step-up endpoints inherits the same lockout policy.
// Returns nil if the request should proceed; non-nil = caller must
// reject with 429 (Retry-After hint included in the message).
func (s *Server) stepUpAllow(r *http.Request) (time.Duration, error) {
	if s.authH == nil {
		return 0, nil
	}
	ip := s.authH.clientIP(r)
	ok, retry := s.authH.limiter.Allow(ip)
	if !ok {
		return retry, errors.New("too many attempts, try later")
	}

	return 0, nil
}

func (s *Server) recordStepUpResult(r *http.Request, ok bool) {
	if s.authH == nil {
		return
	}
	ip := s.authH.clientIP(r)
	if ok {
		s.authH.limiter.RecordSuccess(ip)
		return
	}
	s.authH.limiter.RecordFail(ip)
}

// confirmStepUp does (1) passphrase verify (REQUIRED) and (2)
// requireSecondFactor. Used by destructive operations that, alongside
// the caller's session cookie, demand fresh proof of BOTH factors.
// A session cookie + passphrase alone is no longer enough for these
// operations — the second factor (when enrolled) is also required.
func (s *Server) confirmStepUp(r *http.Request, code, recovery, passphrase string) error {
	creds, err := s.mngr.Storage().GetAuthCredentials(r.Context())
	if err != nil {
		return err
	}
	if creds == nil {
		return errors.New("no credentials configured")
	}
	if passphrase == "" {
		return errors.New("passphrase required")
	}
	ok, err := auth.VerifyPassphrase(creds.PassphraseHash, passphrase)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("authentication rejected")
	}

	return s.requireSecondFactor(r.Context(), creds, code, recovery)
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if retry, err := s.stepUpAllow(r); err != nil {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sensitiveBodyMaxBytes)
	var req struct {
		Code       string `json:"code"`
		Recovery   string `json:"recovery"`
		Passphrase string `json:"passphrase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.confirmStepUp(r, req.Code, req.Recovery, req.Passphrase); err != nil {
		s.recordStepUpResult(r, false)
		s.auditIfPublic(r, "totp_disable_fail", "")
		http.Error(w, "authentication rejected", http.StatusUnauthorized)
		return
	}
	s.recordStepUpResult(r, true)
	if err := auth.ResetTOTP(r.Context(), s.mngr.Storage()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditIfPublic(r, "totp_disable_ok", "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRecoveryRegenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if retry, err := s.stepUpAllow(r); err != nil {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sensitiveBodyMaxBytes)
	var req struct {
		Code       string `json:"code"`
		Recovery   string `json:"recovery"`
		Passphrase string `json:"passphrase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.confirmStepUp(r, req.Code, req.Recovery, req.Passphrase); err != nil {
		s.recordStepUpResult(r, false)
		s.auditIfPublic(r, "recovery_regen_fail", "")
		http.Error(w, "authentication rejected", http.StatusUnauthorized)
		return
	}
	s.recordStepUpResult(r, true)
	creds, err := s.mngr.Storage().GetAuthCredentials(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if creds == nil || len(creds.TOTPSecret) == 0 {
		http.Error(w, "TOTP must be enabled before regenerating recovery codes", http.StatusBadRequest)
		return
	}

	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		h, err := auth.HashRecoveryCode(c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		hashes[i] = h
	}
	if err := s.mngr.Storage().ResetRecoveryCodes(r.Context(), hashes); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditIfPublic(r, "recovery_regen_ok", "")
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

type sessionView struct {
	PublicID  string `json:"public_id"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  int64  `json:"last_seen"`
	RemoteIP  string `json:"remote_ip"`
	UserAgent string `json:"user_agent"`
	IsCurrent bool   `json:"is_current"`
}

// publicSessionID derives a non-secret display identifier for a session
// row. The raw `id` IS the cookie value — exposing it lets a logged-in
// attacker copy another row's id into their cookie and impersonate that
// session. SHA-256(id) is irreversible and unique per session.
func publicSessionID(rawID string) string {
	sum := sha256.Sum256([]byte(rawID))

	return hex.EncodeToString(sum[:16])
}

// maxSessionsListed bounds the rows returned to the client. With a
// thirty-day idle window an active operator could accumulate hundreds
// of rows; the security UI only needs a reasonable working set, so we
// hard-cap on the most-recent N. Older rows still get garbage-collected
// by the maintenance goroutine.
const maxSessionsListed = 100

func (s *Server) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	noStoreHeaders(w)
	if !s.publicMode {
		writeJSON(w, http.StatusOK, map[string]any{"sessions": []sessionView{}})
		return
	}
	sessions, err := s.mngr.Storage().ListAuthSessions(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(sessions) > maxSessionsListed {
		sessions = sessions[:maxSessionsListed]
	}
	current := s.currentSessionID(r)
	out := make([]sessionView, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, sessionView{
			PublicID:  publicSessionID(sess.ID),
			CreatedAt: sess.CreatedAt.Unix(),
			LastSeen:  sess.LastSeen.Unix(),
			RemoteIP:  sess.RemoteIP,
			UserAgent: sess.UserAgent,
			IsCurrent: sess.ID == current,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sensitiveBodyMaxBytes)
	var req struct {
		PublicID   string `json:"public_id"`
		Passphrase string `json:"passphrase"`
		Code       string `json:"code"`
		Recovery   string `json:"recovery"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pubID := strings.TrimSpace(req.PublicID)
	if pubID == "" {
		http.Error(w, "public_id required", http.StatusBadRequest)
		return
	}
	if !s.publicMode {
		http.Error(w, "session revocation is a public-mode feature", http.StatusBadRequest)
		return
	}
	if err := s.confirmStepUp(r, req.Code, req.Recovery, req.Passphrase); err != nil {
		s.auditIfPublic(r, "session_revoke_step_up_fail", "")
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	sessions, err := s.mngr.Storage().ListAuthSessions(r.Context())
	if err != nil {
		s.logger.Warn("session revoke: list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	current := s.currentSessionID(r)
	var match string
	for _, sess := range sessions {
		if subtle.ConstantTimeCompare([]byte(publicSessionID(sess.ID)), []byte(pubID)) == 1 {
			match = sess.ID
			break
		}
	}
	if match == "" {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if match == current {
		http.Error(w, "use logout to end the current session", http.StatusBadRequest)
		return
	}
	if err := s.mngr.Storage().DeleteAuthSession(r.Context(), match); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditIfPublic(r, "session_revoke", pubID)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAuthLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	noStoreHeaders(w)
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	entries, err := s.mngr.Storage().AuthLogTail(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type logView struct {
		Timestamp int64  `json:"timestamp"`
		Event     string `json:"event"`
		RemoteIP  string `json:"remote_ip"`
		UserAgent string `json:"user_agent"`
		Note      string `json:"note,omitempty"`
	}
	out := make([]logView, 0, len(entries))
	for _, e := range entries {
		out = append(out, logView{
			Timestamp: e.Timestamp.Unix(),
			Event:     e.Event,
			RemoteIP:  e.RemoteIP,
			UserAgent: e.UserAgent,
			Note:      e.Note,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

// currentSessionID returns the cookie-bound session id when the server
// is in public mode and the request carries a valid cookie. Empty for
// loopback mode and for unauthenticated requests (which can't reach
// these handlers anyway thanks to requireAuth).
func (s *Server) currentSessionID(r *http.Request) string {
	if !s.publicMode {
		return ""
	}
	c, err := r.Cookie(cookieNameSession)
	if err != nil {
		return ""
	}

	return c.Value
}

func (s *Server) authModeName() string {
	if s.publicMode {
		return "public"
	}

	return "loopback"
}

// invalidateOtherSessions deletes every public-mode session except the
// caller's. Detached from r.Context so a client disconnect mid-cleanup
// doesn't leave the rows behind. Single SQL DELETE is faster than the
// list-and-loop pattern and atomic from the application's perspective.
func (s *Server) invalidateOtherSessions(r *http.Request) {
	if !s.publicMode {
		return
	}
	current := s.currentSessionID(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	if err := s.mngr.Storage().DeleteAuthSessionsExcept(ctx, current); err != nil {
		s.logger.Warn("auth: invalidate other sessions", "err", err)
	}
}

// auditIfPublic writes an audit-log row when public mode is active.
// Loopback mode is local-only and noisy auditing there has little
// forensic value.
func (s *Server) auditIfPublic(r *http.Request, event, note string) {
	if !s.publicMode || s.authH == nil {
		return
	}
	entry := storage.AuthLogEntry{
		Timestamp: time.Now(),
		Event:     event,
		RemoteIP:  s.authH.clientIP(r),
		UserAgent: truncateUA(r.UserAgent()),
		Note:      note,
	}
	if err := s.mngr.Storage().WriteAuthLog(context.WithoutCancel(r.Context()), entry); err != nil {
		s.logger.Warn("audit write failed", "event", event, "err", err)
	}
}

// totpAccountName returns the account label embedded in the otpauth://
// URI. Public host wins (it's how the operator already identifies this
// instance); loopback shows the identity's destination hash so different
// keys in the same authenticator app don't collide on "udisend".
func (s *Server) totpAccountName() string {
	if s.publicHost != "" {
		return s.publicHost
	}

	return s.mngr.Identity().Public().DestinationHash().String()
}
