package httpui

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"

	"golang.org/x/crypto/argon2"

	"github.com/udisondev/udisend/internal/httpui/auth"
	"github.com/udisondev/udisend/pkg/crypto"
)

// identityExportHeader is the leading magic on the encrypted blob. The
// trailing version byte (\x02) lets future schemes bump it; v1 used a
// hard-coded Argon2id parameter set with no AAD, v2 carries params in
// the blob and binds header+salt as additional authenticated data.
var identityExportHeader = []byte("UDISENDID\x02")

// v2 layout:
//   header(10) | tCost(4 BE) | mCost(4 BE) | threads(1) | saltLen(1) |
//   salt(saltLen) | aead(seed_bytes) where AAD = header || params || salt
//
// Argon2id KDF parameters baked in for new exports — values within the
// recorded ranges are still accepted on import (values outside are
// rejected to forbid attacker-chosen pathological params).
const (
	identityExportSaltSize = 16
	argon2Time             = 3
	argon2Memory           = 64 * 1024
	argon2Threads          = 4
)

// Bounds on parsed Argon2id parameters during import. Mirrors the
// OWASP recommendation set; refusing wildly-large values prevents an
// attacker-supplied blob from running until OOM at decrypt time.
const (
	argon2MinTime    = 1
	argon2MaxTime    = 16
	argon2MinMemKiB  = 16 * 1024
	argon2MaxMemKiB  = 1024 * 1024
	argon2MinThreads = 1
	argon2MaxThreads = 16
)

func (s *Server) handleIdentityExport(w http.ResponseWriter, r *http.Request) {
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
		Passphrase string `json:"passphrase"`
		Code       string `json:"code"`
		Recovery   string `json:"recovery"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Passphrase == "" {
		http.Error(w, "passphrase required", http.StatusBadRequest)
		return
	}

	creds, err := s.mngr.Storage().GetAuthCredentials(r.Context())
	if err != nil {
		s.logger.Warn("identity export: load creds", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if creds == nil {
		http.Error(w, "set a passphrase before exporting identity", http.StatusBadRequest)
		return
	}
	ok, err := auth.VerifyPassphrase(creds.PassphraseHash, req.Passphrase)
	if err != nil || !ok {
		s.recordStepUpResult(r, false)
		s.auditIfPublic(r, "identity_export_fail", "passphrase")
		http.Error(w, "authentication rejected", http.StatusUnauthorized)
		return
	}
	if err := s.requireSecondFactor(r.Context(), creds, req.Code, req.Recovery); err != nil {
		s.recordStepUpResult(r, false)
		s.auditIfPublic(r, "identity_export_fail", "step-up")
		http.Error(w, "authentication rejected", http.StatusUnauthorized)
		return
	}
	s.recordStepUpResult(r, true)

	id := s.mngr.Identity()
	seedBytes, err := id.MarshalBinary()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer wipe(seedBytes)

	blob, err := encryptIdentity(seedBytes, req.Passphrase, rand.Reader)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditIfPublic(r, "identity_export_ok", "")

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="udisend-identity.bin"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(blob)
}

func encryptIdentity(seed []byte, passphrase string, randSrc io.Reader) ([]byte, error) {
	salt := make([]byte, identityExportSaltSize)
	if _, err := io.ReadFull(randSrc, salt); err != nil {
		return nil, fmt.Errorf("identity-export: salt: %w", err)
	}

	params := encodeArgon2Params(argon2Time, argon2Memory, argon2Threads, byte(len(salt)))
	aad := buildAAD(params, salt)

	key := argon2.IDKey([]byte(passphrase), salt, argon2Time, argon2Memory, argon2Threads, crypto.AEADKeySize)
	defer wipe(key)
	aead, err := crypto.NewAEAD(key)
	if err != nil {
		return nil, fmt.Errorf("identity-export: aead: %w", err)
	}
	cipher, err := aead.Seal(seed, aad)
	if err != nil {
		return nil, fmt.Errorf("identity-export: seal: %w", err)
	}

	out := make([]byte, 0, len(identityExportHeader)+len(params)+len(salt)+len(cipher))
	out = append(out, identityExportHeader...)
	out = append(out, params...)
	out = append(out, salt...)
	out = append(out, cipher...)

	return out, nil
}

// maxIdentityImportBlob caps the byte budget for an identity import
// payload. The legitimate blob is on the order of identityExportHeader
// (10) + params (10) + salt (16) + AEAD-sealed seed (~80 bytes) — i.e.
// well under 256 bytes. The 4 KiB budget leaves headroom for future
// versions while refusing to feed an attacker-controlled multi-MB blob
// into AEAD.Open(), which allocates a plaintext buffer proportional to
// the ciphertext slice. (argon2's allocation is bound by mCost, which
// decodeArgon2Params clamps separately — but its CPU work is also
// proportional to mCost × tCost, so refusing the input early avoids
// the per-attempt 64 MiB / 3-iteration budget being burned on junk.)
// Defends a future /api/identity/import endpoint from trivial OOM via
// crafted upload.
const maxIdentityImportBlob = 4 << 10

// decryptIdentity is provided for tests and any future "import" flow.
// Returns ErrIdentityExportFormat for malformed blobs and a generic
// "decrypt failed" for bad passphrases (no oracle on which one).
func decryptIdentity(blob []byte, passphrase string) ([]byte, error) {
	const minLen = 10 /*header*/ + 10 /*params*/ + identityExportSaltSize + crypto.NonceSize
	if len(blob) < minLen || len(blob) > maxIdentityImportBlob {
		return nil, ErrIdentityExportFormat
	}
	for i, b := range identityExportHeader {
		if blob[i] != b {
			return nil, ErrIdentityExportFormat
		}
	}
	off := len(identityExportHeader)
	tCost, mCost, threads, saltLen, err := decodeArgon2Params(blob[off : off+10])
	if err != nil {
		return nil, err
	}
	off += 10
	if int(saltLen) != identityExportSaltSize {
		return nil, ErrIdentityExportFormat
	}
	if len(blob) < off+int(saltLen)+crypto.NonceSize {
		return nil, ErrIdentityExportFormat
	}
	salt := blob[off : off+int(saltLen)]
	off += int(saltLen)
	payload := blob[off:]

	aad := buildAAD(blob[len(identityExportHeader):len(identityExportHeader)+10], salt)
	key := argon2.IDKey([]byte(passphrase), salt, tCost, mCost, threads, crypto.AEADKeySize)
	defer wipe(key)
	aead, err := crypto.NewAEAD(key)
	if err != nil {
		return nil, err
	}

	return aead.Open(payload, aad)
}

func encodeArgon2Params(tCost, mCost uint32, threads, saltLen byte) []byte {
	out := make([]byte, 10)
	binary.BigEndian.PutUint32(out[0:4], tCost)
	binary.BigEndian.PutUint32(out[4:8], mCost)
	out[8] = threads
	out[9] = saltLen

	return out
}

func decodeArgon2Params(b []byte) (uint32, uint32, uint8, uint8, error) {
	if len(b) < 10 {
		return 0, 0, 0, 0, ErrIdentityExportFormat
	}
	t := binary.BigEndian.Uint32(b[0:4])
	m := binary.BigEndian.Uint32(b[4:8])
	threads := b[8]
	saltLen := b[9]
	if t < argon2MinTime || t > argon2MaxTime ||
		m < argon2MinMemKiB || m > argon2MaxMemKiB ||
		threads < argon2MinThreads || threads > argon2MaxThreads {
		return 0, 0, 0, 0, ErrIdentityExportFormat
	}

	return t, m, threads, saltLen, nil
}

func buildAAD(params, salt []byte) []byte {
	aad := make([]byte, 0, len(identityExportHeader)+len(params)+len(salt))
	aad = append(aad, identityExportHeader...)
	aad = append(aad, params...)
	aad = append(aad, salt...)

	return aad
}

// ErrIdentityExportFormat is returned when an identity-export blob is
// too short, carries a header from a different version, or its
// Argon2id parameters fall outside the accepted bounds.
var ErrIdentityExportFormat = errors.New("identity-export: bad format")

// wipe overwrites a sensitive byte slice. Defense-in-depth — Go's
// runtime can keep copies in escape-analysed buffers, so this is best-
// effort. runtime.KeepAlive prevents the compiler from elidi the loop
// as a dead store; callers should treat wipe as a hint, not a guarantee.
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}
