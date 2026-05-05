package httpui

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/udisondev/udisend/internal/httpui/auth"
	"github.com/udisondev/udisend/internal/storage"
)

const (
	cookieNameSession = "udisend_session"
	cookieMaxAgeSecs  = int(30 * 24 * time.Hour / time.Second)
	loginPath         = "/login"

	// loginBodyMaxBytes caps form body size so an attacker cannot drive
	// Argon2id (≈64 MiB peak) by submitting megabytes of "passphrase".
	// 4 KiB is generous: even with TOTP + recovery code fields and
	// percent-encoding, a real form fits in well under that.
	loginBodyMaxBytes = 4 * 1024

	// auditUAMaxBytes truncates user-agent strings before persistence so
	// hostile clients cannot bloat the audit log.
	auditUAMaxBytes = 256
)

// authHandlers groups the public-mode authentication endpoints. It holds
// only the dependencies they need — storage, session manager, rate-limit
// — so it can be unit-tested without spinning up the full Messenger.
type authHandlers struct {
	store        *storage.Store
	sessions     *auth.Sessions
	limiter      *auth.RateLimiter
	secureCookie bool
	trustProxy   bool
	// allowOpaqueOrigin tolerates `Origin: null` on POST /login. Browsers
	// downgrade to opaque origin after a self-signed cert override, which
	// is the expected state in lan-ip deployments. Set via Config in modes
	// that issue self-signed certs; left false where the cert is publicly
	// trusted (public-tls, public-autocert, public-proxy) so a `null`
	// Origin remains a CSRF signal.
	allowOpaqueOrigin bool
	now               func() time.Time
}

type loginViewData struct {
	ShowTOTP bool
	Error    string
}

var loginTpl = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>udisend — sign in</title>
<style>
  body { font: 14px system-ui, sans-serif; background: #0d1117; color: #c9d1d9; margin: 0; min-height: 100vh; display: grid; place-items: center; }
  main { background: #161b22; padding: 32px 28px; border: 1px solid #30363d; border-radius: 8px; min-width: 320px; }
  h1 { margin: 0 0 18px; font-size: 18px; letter-spacing: 0.04em; color: #f0f6fc; }
  label { display: block; margin: 12px 0 6px; font-size: 12px; opacity: 0.85; }
  input { width: 100%; padding: 8px 10px; background: #0d1117; color: #c9d1d9; border: 1px solid #30363d; border-radius: 4px; font: inherit; box-sizing: border-box; }
  input:focus { outline: 2px solid #58a6ff; }
  button { width: 100%; margin-top: 18px; padding: 9px 12px; background: #238636; color: white; border: 0; border-radius: 4px; font: inherit; cursor: pointer; }
  button:hover { background: #2ea043; }
  .err { background: #5a1d1d; border: 1px solid #8a3a3a; padding: 8px 10px; border-radius: 4px; margin-bottom: 8px; font-size: 13px; }
</style>
</head>
<body>
<main>
  <h1>udisend</h1>
  {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
  <form method="post" action="/login">
    <label>Passphrase</label>
    <input type="password" name="passphrase" autocomplete="current-password" autofocus required>
    {{if .ShowTOTP}}
    <label>TOTP code or recovery code</label>
    <input type="text" name="totp" autocomplete="off" inputmode="numeric" required>
    {{end}}
    <button type="submit">Sign in</button>
  </form>
</main>
</body>
</html>`))

// renderLogin writes the login form at the given status with the given view
// data. Used both for the initial GET and for re-rendering after a failed
// POST.
func (h *authHandlers) renderLogin(w http.ResponseWriter, status int, data loginViewData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = loginTpl.Execute(w, data)
}

func (h *authHandlers) handleLoginGET(w http.ResponseWriter, r *http.Request) {
	creds, err := h.store.GetAuthCredentials(r.Context())
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if creds == nil {
		http.Error(w, "auth not configured — run `udisend public -enable <addr>` on the host first", http.StatusServiceUnavailable)
		return
	}

	h.renderLogin(w, http.StatusOK, loginViewData{ShowTOTP: creds.TOTPSecret != nil})
}

func (h *authHandlers) handleLoginPOST(w http.ResponseWriter, r *http.Request) {
	if ok, reason := h.checkLoginOrigin(r); !ok {
		slog.Warn("login: origin rejected",
			"reason", reason,
			"origin", r.Header.Get("Origin"),
			"host", r.Host,
			"proto", r.Proto,
		)
		http.Error(w, "bad origin", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, loginBodyMaxBytes)

	ip := h.clientIP(r)
	if ok, retryAfter := h.limiter.Allow(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		http.Error(w, "too many attempts, try later", http.StatusTooManyRequests)
		// Don't audit every single rate-limited request — an attacker can
		// fill the log faster than the hourly prune evicts. Sampling at the
		// transition (first deny per minute) would be better; for now the
		// rate limiter's in-memory state already records the abuse.
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	pass := r.FormValue("passphrase")
	totpInput := strings.TrimSpace(r.FormValue("totp"))

	creds, err := h.store.GetAuthCredentials(r.Context())
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if creds == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	okPass, err := auth.VerifyPassphrase(creds.PassphraseHash, pass)
	if err != nil || !okPass {
		h.limiter.RecordFail(ip)
		_ = h.audit(r.Context(), "login_fail_passphrase", ip, r.UserAgent(), "")
		h.renderLogin(w, http.StatusUnauthorized, loginViewData{
			ShowTOTP: creds.TOTPSecret != nil,
			Error:    "Wrong passphrase or TOTP code.",
		})
		return
	}

	successEvent := "login_success"
	if creds.TOTPSecret != nil {
		if totpInput == "" {
			h.renderLogin(w, http.StatusUnauthorized, loginViewData{
				ShowTOTP: true,
				Error:    "Enter the code from your authenticator (or a recovery code).",
			})
			return
		}
		switch {
		case h.verifyTOTPWithReplay(r.Context(), creds.TOTPSecret, totpInput):
			// Standard TOTP path; successEvent stays "login_success".
		case h.tryRecoveryCode(r.Context(), totpInput):
			// Recovery-code use is a "break-glass" event the operator
			// should be able to spot in audit. Distinguish so a stolen
			// passphrase + brute-forced recovery code stands out.
			successEvent = "login_success_recovery"
		default:
			h.limiter.RecordFail(ip)
			_ = h.audit(r.Context(), "login_fail_2fa", ip, r.UserAgent(), "")
			h.renderLogin(w, http.StatusUnauthorized, loginViewData{
				ShowTOTP: true,
				Error:    "Wrong passphrase or TOTP code.",
			})
			return
		}
	}

	h.limiter.RecordSuccess(ip)
	sid, err := h.sessions.Begin(r.Context(), ip, truncateUA(r.UserAgent()))
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, h.sessionCookie(sid, cookieMaxAgeSecs))
	_ = h.audit(r.Context(), successEvent, ip, r.UserAgent(), "")

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *authHandlers) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieNameSession); err == nil {
		_ = h.sessions.End(r.Context(), c.Value)
	}
	http.SetCookie(w, h.sessionCookie("", -1))
	_ = h.audit(r.Context(), "logout", h.clientIP(r), r.UserAgent(), "")

	http.Redirect(w, r, loginPath, http.StatusSeeOther)
}

// requireSession blocks access to next unless a valid session cookie is
// present. Returns 401 (not a redirect) so JS callers can react cleanly;
// the SPA root has its own redirect-to-/login fallback.
func (h *authHandlers) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieNameSession)
		if err != nil {
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		sess, err := h.sessions.Validate(r.Context(), c.Value)
		if err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		if sess == nil {
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// verifyTOTPWithReplay matches code against the TOTP secret AND persists
// the matched step so the same code cannot be replayed on a fresh login
// within the same 30s period (or its ±1-step skew window). Mirrors the
// step-replay discipline of `requireSecondFactor` so login is not the
// soft side of the TOTP defence.
//
// On any persistence error we refuse the attempt — fail-closed: a
// transient DB outage MUST NOT erase the replay window.
func (h *authHandlers) verifyTOTPWithReplay(ctx context.Context, secret []byte, code string) bool {
	step, ok := auth.VerifyTOTPStep(secret, code, h.now())
	if !ok {
		return false
	}
	used, exists, err := h.store.GetSetting(ctx, settingKeyLastTOTPStep)
	if err != nil {
		return false
	}
	if exists {
		if u, perr := strconv.ParseInt(used, 10, 64); perr == nil && u >= step {
			return false
		}
	}
	if err := h.store.SetSetting(ctx, settingKeyLastTOTPStep, strconv.FormatInt(step, 10)); err != nil {
		return false
	}

	return true
}

// tryRecoveryCode iterates unconsumed recovery hashes and Argon2id-verifies
// each against the input. On hash match it ALSO has to win the
// MarkRecoveryCodeConsumed race — a second concurrent request with the
// same code reaches the same hash but loses the conditional UPDATE,
// returning ok=false and treated here as no-match. That eliminates the
// "single-use" TOCTOU window between verify and mark.
func (h *authHandlers) tryRecoveryCode(ctx context.Context, code string) bool {
	rows, err := h.store.UnconsumedRecoveryCodes(ctx)
	if err != nil {
		return false
	}
	for _, row := range rows {
		ok, err := auth.VerifyRecoveryCode(row.Hash, code)
		if err != nil || !ok {
			continue
		}
		consumed, err := h.store.MarkRecoveryCodeConsumed(ctx, row.ID, h.now())
		if err != nil {
			return false
		}
		if consumed {
			return true
		}
	}

	return false
}

func (h *authHandlers) sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     cookieNameSession,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	}
}

// clientIP returns the request's apparent source IP. Trusts the rightmost
// clientIP returns the originating client's IP. The leftmost entry in
// X-Forwarded-For is the originating client per RFC 7239 conventions
// (each proxy appends, so the head is the public-facing client). We
// only honour X-Forwarded-For when h.trustProxy is set — otherwise an
// attacker could lie about their IP and bypass the rate-limiter, or
// poison the audit log.
func (h *authHandlers) clientIP(r *http.Request) string {
	if h.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			ip := strings.TrimSpace(parts[0])
			if ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

func (h *authHandlers) audit(ctx context.Context, event, ip, ua, note string) error {
	return h.store.WriteAuthLog(ctx, storage.AuthLogEntry{
		Timestamp: h.now(),
		Event:     event,
		RemoteIP:  ip,
		UserAgent: truncateUA(ua),
		Note:      note,
	})
}

// checkLoginOrigin enforces a same-origin POST to /login. Browsers
// always set Origin on cross-origin POST, so a same-origin form goes
// through; a cross-origin one (the login-CSRF / session-fixation
// vector) is rejected.
//
// The wrinkle: Chromium and Safari downgrade pages to "opaque origin"
// after the user accepts a self-signed cert override, sending
// `Origin: null` on subsequent POST. Strict comparison would falsely
// reject every login on lan-ip deployments. We tolerate `null` only
// when the deployment is known to issue a self-signed cert
// (`allowOpaqueOrigin`). For TLS modes with a publicly-trusted cert,
// `null` remains a CSRF signal and stays rejected.
//
// Trade-off in lan-ip mode: lose strong CSRF defence on /login. The
// remaining defences are: rate-limiter (5/min, 20-fail lockout),
// Argon2id passphrase hashing, single-tenant deployment (no other
// account to fixate into), audit log. For udisend's threat model this
// is an acceptable degradation.
//
// Non-browser clients (curl, integration tests) typically omit Origin
// and are allowed — they cannot be tricked into pre-filling a
// passphrase by a malicious site.
func (h *authHandlers) checkLoginOrigin(r *http.Request) (bool, string) {
	o := r.Header.Get("Origin")
	if o == "" {
		return true, ""
	}
	if o == "null" {
		if h.allowOpaqueOrigin {
			return true, ""
		}
		return false, `Origin "null" rejected (not a self-signed-cert deployment)`
	}
	u, err := url.Parse(o)
	if err != nil {
		return false, fmt.Sprintf("Origin %q unparseable: %v", o, err)
	}
	if u.Host != r.Host {
		return false, fmt.Sprintf("Origin host %q != request Host %q", u.Host, r.Host)
	}

	return true, ""
}

// truncateUA caps user-agent strings before persistence. Hostile clients
// can send giant headers; the audit log and sessions table both store UA
// for human-readable forensics, so we'd rather lose tail bytes than burn
// disk on it.
func truncateUA(ua string) string {
	if len(ua) > auditUAMaxBytes {
		return ua[:auditUAMaxBytes]
	}

	return ua
}

