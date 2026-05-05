// Package auth implements webui authentication primitives — passphrase
// hashing, TOTP, sessions, rate-limit. The package is messenger-internal
// because policy (which params, which formats) is tied to this app.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrEmptyPassphrase is returned by HashPassphrase when the input is empty.
// Length policy beyond non-empty is enforced by callers (CLI prompt etc.).
var ErrEmptyPassphrase = errors.New("auth: empty passphrase")

// Params controls Argon2id cost. Memory is in KiB.
type Params struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	KeyLen      uint32
	SaltLen     uint32
}

// DefaultParams matches OWASP's 2023 Argon2id recommendation: 64 MiB
// memory, 3 iterations, 4 lanes, 32-byte output, 16-byte salt. ~150-300ms
// per hash on commodity hardware — slow enough to make online brute-force
// at a sane rate-limit pointless.
var DefaultParams = Params{
	Memory:      64 * 1024,
	Iterations:  3,
	Parallelism: 4,
	KeyLen:      32,
	SaltLen:     16,
}

// HashPassphrase derives an Argon2id key from p and returns the PHC-encoded
// string `$argon2id$v=19$m=<m>,t=<t>,p=<p>$<salt>$<hash>` (base64 raw, no
// padding). Salt is freshly randomized per call.
func HashPassphrase(passphrase string) (string, error) {
	return hashPassphraseWith(passphrase, DefaultParams, rand.Reader)
}

func hashPassphraseWith(passphrase string, p Params, randSource io.Reader) (string, error) {
	if passphrase == "" {
		return "", ErrEmptyPassphrase
	}

	salt := make([]byte, p.SaltLen)
	if _, err := io.ReadFull(randSource, salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}

	hash := argon2.IDKey([]byte(passphrase), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLen)

	return encodePHC(p, salt, hash), nil
}

// VerifyPassphrase parses the PHC-encoded value, recomputes Argon2id with the
// stored params+salt over passphrase, and constant-time compares to the
// stored hash. Returns (false, error) on malformed encoded input.
func VerifyPassphrase(encoded, passphrase string) (bool, error) {
	p, salt, want, err := decodePHC(encoded)
	if err != nil {
		return false, err
	}

	got := argon2.IDKey([]byte(passphrase), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLen)

	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func encodePHC(p Params, salt, hash []byte) string {
	enc := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		enc(salt), enc(hash),
	)
}

// Bounds for parameters loaded from the persisted PHC string. Anything
// outside the band is treated as DB corruption / poisoning rather than
// honoured. The lower bounds are below DefaultParams so legitimate older
// hashes (created with weaker defaults) still verify; the upper bounds
// are well above any realistic OWASP recommendation but small enough to
// keep argon2.IDKey resource use bounded (~1 GiB memory, ~16 lanes,
// ~16 iterations) — refusing past that point prevents DoS via OOM or
// minute-long CPU pin from a single login attempt.
const (
	minArgonMemoryKiB = 8 * 1024  // 8 MiB
	maxArgonMemoryKiB = 1024 * 1024
	minArgonTime      = 1
	maxArgonTime      = 16
	minArgonLanes     = 1
	maxArgonLanes     = 16
)

// decodePHC parses the standard Argon2id PHC string. Strict: rejects unknown
// algorithms, unknown versions, missing fields, malformed base64, and
// out-of-range Argon2 parameters (DB-poisoning OOM defence).
func decodePHC(encoded string) (Params, []byte, []byte, error) {
	if encoded == "" {
		return Params{}, nil, nil, errors.New("auth: empty encoded value")
	}
	parts := strings.Split(encoded, "$")
	// Expected: ["", "argon2id", "v=19", "m=...,t=...,p=...", "<salt>", "<hash>"]
	if len(parts) != 6 || parts[0] != "" {
		return Params{}, nil, nil, fmt.Errorf("auth: malformed encoded value")
	}
	if parts[1] != "argon2id" {
		return Params{}, nil, nil, fmt.Errorf("auth: unsupported algorithm %q", parts[1])
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return Params{}, nil, nil, fmt.Errorf("auth: parse version: %w", err)
	}
	if version != argon2.Version {
		return Params{}, nil, nil, fmt.Errorf("auth: unsupported argon2 version %d", version)
	}

	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, fmt.Errorf("auth: parse params: %w", err)
	}
	if p.Memory < minArgonMemoryKiB || p.Memory > maxArgonMemoryKiB {
		return Params{}, nil, nil, fmt.Errorf("auth: argon2 memory %d KiB out of bounds [%d..%d]",
			p.Memory, minArgonMemoryKiB, maxArgonMemoryKiB)
	}
	if p.Iterations < minArgonTime || p.Iterations > maxArgonTime {
		return Params{}, nil, nil, fmt.Errorf("auth: argon2 iterations %d out of bounds [%d..%d]",
			p.Iterations, minArgonTime, maxArgonTime)
	}
	if p.Parallelism < minArgonLanes || p.Parallelism > maxArgonLanes {
		return Params{}, nil, nil, fmt.Errorf("auth: argon2 parallelism %d out of bounds [%d..%d]",
			p.Parallelism, minArgonLanes, maxArgonLanes)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, fmt.Errorf("auth: decode salt: %w", err)
	}
	p.SaltLen = uint32(len(salt))

	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, fmt.Errorf("auth: decode hash: %w", err)
	}
	p.KeyLen = uint32(len(hash))

	return p, salt, hash, nil
}
