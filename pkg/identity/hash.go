package identity

import (
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrInvalidHash is returned by ParseHash for strings that don't decode to a
// 16-byte hash.
var ErrInvalidHash = errors.New("identity: invalid destination hash")

// String renders the hash as lowercase hex (32 chars).
func (h Hash) String() string { return hex.EncodeToString(h[:]) }

// MarshalText implements encoding.TextMarshaler so Hash values round-trip
// through JSON / TOML / SQLite text columns without a custom codec at the
// call site.
func (h Hash) MarshalText() ([]byte, error) { return []byte(h.String()), nil }

// UnmarshalText parses the hex form produced by MarshalText.
func (h *Hash) UnmarshalText(text []byte) error {
	parsed, err := ParseHash(string(text))
	if err != nil {
		return err
	}
	*h = parsed
	return nil
}

// ParseHash decodes a 32-character hex string into a Hash. Whitespace is not
// stripped — callers that accept user input should TrimSpace first.
func ParseHash(s string) (Hash, error) {
	var h Hash
	if len(s) != 2*HashSize {
		return h, fmt.Errorf("%w: expected %d hex chars, got %d", ErrInvalidHash, 2*HashSize, len(s))
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return h, fmt.Errorf("%w: %v", ErrInvalidHash, err)
	}
	copy(h[:], raw)
	return h, nil
}

// IsZero reports whether the hash is the all-zero sentinel (an invalid peer).
func (h Hash) IsZero() bool {
	for _, b := range h {
		if b != 0 {
			return false
		}
	}
	return true
}
