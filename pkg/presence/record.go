// Package presence implements the signed, ephemeral presence records that
// peers publish through the DHT to advertise reachability and capabilities.
// A record is keyed by the publisher's destination hash; lookups return
// the most-recent valid record (TTL-checked, signature-verified).
//
// Records are signed with the publisher's Ed25519 key. Recipients reject
// any record whose signature does not match the embedded public identity,
// preventing peers from claiming someone else's address.
package presence

import (
	"errors"
	"fmt"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/wire"
)

// Capability is a bitset describing what services a peer offers.
type Capability uint32

// Capability bits.
const (
	CapPublicIP    Capability = 1 << 0
	CapCanRelay    Capability = 1 << 1
	CapCanBootstrap Capability = 1 << 2
	CapCanSTUN     Capability = 1 << 3
	CapCanTURN     Capability = 1 << 4
)

// Has reports whether c has all of bits set.
func (c Capability) Has(bits Capability) bool { return c&bits == bits }

// String returns a human-readable summary for logging.
func (c Capability) String() string {
	if c == 0 {
		return "none"
	}
	out := ""
	add := func(name string, bit Capability) {
		if c.Has(bit) {
			if out != "" {
				out += "|"
			}
			out += name
		}
	}
	add("PublicIP", CapPublicIP)
	add("Relay", CapCanRelay)
	add("Bootstrap", CapCanBootstrap)
	add("STUN", CapCanSTUN)
	add("TURN", CapCanTURN)
	return out
}

// Record is a single presence advertisement, signed by the publisher.
//
// UptimeHint is a hint to clients about the publisher's continuous
// uptime in seconds; used by ICE-server selection (design.md §5/§6) to
// prefer stable volunteers. Advisory only — the publisher is trusted to
// report honestly; clients should bound expectations.
//
// MaxRelaySlots advertises how many concurrent signaling-relay sessions
// the publisher is willing to serve. 0 means "unlimited / not advertised".
type Record struct {
	Public        identity.PublicIdentity
	Address       string
	Capabilities  Capability
	IssuedAt      time.Time
	UptimeHint    uint32
	MaxRelaySlots uint16
	Signature     identity.Signature
}

// Errors returned by the package.
var (
	ErrInvalidRecord  = errors.New("presence: invalid record")
	ErrBadSignature   = errors.New("presence: bad signature")
	ErrExpired        = errors.New("presence: record expired")
	ErrIdentityMismatch = errors.New("presence: pubkey/destination mismatch")
)

// recordVersion is the only wire-format version this codebase emits.
// A short-lived v1 layout (without UptimeHint/MaxRelaySlots) existed in
// pre-MVP commits but was never deployed; it is intentionally not
// accepted here — keeping the signing-bytes preimage in sync with the
// wire layout matters more than backward compatibility no real client
// can demand. Forward compatibility comes from TLV extensions appended
// after the signature (see UnmarshalBinary).
const recordVersion byte = 0x02

// MaxAddressLen caps the serialised address length (defensive; real
// addresses are tens of bytes).
const MaxAddressLen = 256

// Sign produces a signed copy of r using id's private key. The Public
// field is overwritten with id's public identity to ensure consistency.
func (r *Record) Sign(id *identity.Identity) error {
	r.Public = id.Public()
	preimage, err := r.signingBytes()
	if err != nil {
		return err
	}
	r.Signature = id.Sign(preimage)
	return nil
}

// Verify checks that the record's signature matches its embedded public
// identity and (optionally) that it has not expired by ttl from IssuedAt
// relative to now.
func (r *Record) Verify(now time.Time, ttl time.Duration) error {
	if len(r.Public.EdPub) == 0 {
		return ErrInvalidRecord
	}
	preimage, err := r.signingBytes()
	if err != nil {
		return err
	}
	if !r.Public.Verify(preimage, r.Signature) {
		return ErrBadSignature
	}
	if ttl > 0 && r.Expired(now, ttl) {
		return ErrExpired
	}
	return nil
}

// Expired reports whether the record's age exceeds ttl as of `now`.
func (r *Record) Expired(now time.Time, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	return now.Sub(r.IssuedAt) > ttl
}

// DestinationHash returns the publisher's address.
func (r *Record) DestinationHash() identity.Hash {
	return r.Public.DestinationHash()
}

// MarshalBinary encodes the record in a stable wire format (version 2):
//
//	version(1) | pub_identity(65) | address(uvarint|str) |
//	  capabilities(4) | issued_at_unix(8) |
//	  uptime_hint(4) | max_relay_slots(2) | signature(64)
func (r *Record) MarshalBinary() ([]byte, error) {
	if len(r.Address) > MaxAddressLen {
		return nil, fmt.Errorf("%w: address too long", ErrInvalidRecord)
	}
	pub, err := r.Public.MarshalBinary()
	if err != nil {
		return nil, err
	}
	w := wire.NewWriter()
	w.WriteUint8(recordVersion)
	w.WriteFixed(pub)
	w.WriteString(r.Address)
	w.WriteUint32(uint32(r.Capabilities))
	w.WriteUint64(uint64(r.IssuedAt.Unix()))
	w.WriteUint32(r.UptimeHint)
	w.WriteUint16(r.MaxRelaySlots)
	w.WriteFixed(r.Signature[:])
	return w.Bytes(), nil
}

// UnmarshalBinary parses a Record produced by MarshalBinary. Only the
// current wire format is accepted; trailing unknown TLV blocks are
// tolerated for forward compatibility.
func (r *Record) UnmarshalBinary(data []byte) error {
	b := wire.NewBuffer(data)
	ver, err := b.ReadUint8()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	if ver != recordVersion {
		return fmt.Errorf("%w: version=%d", ErrInvalidRecord, ver)
	}

	pubBlob, err := b.ReadFixed(1 + identity.PublicKeySize)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	if err := r.Public.UnmarshalBinary(pubBlob); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}

	addr, err := b.ReadString(MaxAddressLen)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	r.Address = addr

	caps, err := b.ReadUint32()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	r.Capabilities = Capability(caps)

	ts, err := b.ReadUint64()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	r.IssuedAt = time.Unix(int64(ts), 0).UTC()

	uptime, err := b.ReadUint32()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	r.UptimeHint = uptime

	slots, err := b.ReadUint16()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	r.MaxRelaySlots = slots

	sig, err := b.ReadFixed(identity.SignatureSize)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	copy(r.Signature[:], sig)

	if err := b.SkipUnknownTLVs(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}

	return nil
}

// signingBytes returns the bytes covered by the signature (everything
// except the signature itself). v2 records cover UptimeHint and
// MaxRelaySlots so a forwarder cannot tweak them.
func (r *Record) signingBytes() ([]byte, error) {
	pub, err := r.Public.MarshalBinary()
	if err != nil {
		return nil, err
	}
	w := wire.NewWriter()
	w.WriteUint8(recordVersion)
	w.WriteFixed(pub)
	w.WriteString(r.Address)
	w.WriteUint32(uint32(r.Capabilities))
	w.WriteUint64(uint64(r.IssuedAt.Unix()))
	w.WriteUint32(r.UptimeHint)
	w.WriteUint16(r.MaxRelaySlots)
	return w.Bytes(), nil
}
