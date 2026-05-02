package identity

import (
	"errors"
	"fmt"
	"io"
	"math/bits"
)

// PoWBits returns the number of leading zero bits in the destination
// hash. S/Kademlia (design.md §4) uses this as a cheap Sybil-resistance
// validator: a node claiming a NodeID with `n` leading zeros must have
// performed ~2^n hash operations on average to find such a seed,
// raising the cost of generating many identities to surround a victim.
func (p PublicIdentity) PoWBits() int {
	h := p.DestinationHash()
	leading := 0
	for _, b := range h {
		if b == 0 {
			leading += 8
			continue
		}

		return leading + bits.LeadingZeros8(b)
	}

	return leading
}

// ErrPoWSearchAborted is returned by GenerateWithPoW when the supplied
// reader runs out of bytes before a satisfying seed is found. With a
// production crypto/rand.Reader this never happens; tests can wire a
// bounded reader to assert giving-up behaviour.
var ErrPoWSearchAborted = errors.New("identity: PoW search aborted (reader exhausted)")

// MaxPoWBits is the largest difficulty GenerateWithPoW will accept. 32
// bits is already ~4 billion attempts (minutes on commodity hardware);
// callers can raise the cap if they have a justification.
const MaxPoWBits = 32

// GenerateWithPoW samples seeds from `r` until one yields a public
// identity with at least `bits` leading zeros in its destination hash.
// `bits == 0` makes this equivalent to Generate. design.md §4 calls for
// PoW on nodeID generation as an opt-in S/Kademlia hardening; deployers
// pick a difficulty that rejects mass-Sybil attacks while keeping fresh
// account creation tolerable on the target hardware.
func GenerateWithPoW(r io.Reader, bits int) (*Identity, error) {
	if bits < 0 || bits > MaxPoWBits {
		return nil, fmt.Errorf("identity: PoW bits %d out of range [0,%d]", bits, MaxPoWBits)
	}
	for {
		var seed [SeedSize]byte
		if _, err := io.ReadFull(r, seed[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, ErrPoWSearchAborted
			}
			return nil, fmt.Errorf("identity: read seed: %w", err)
		}
		id, err := FromSeed(seed)
		if err != nil {
			return nil, err
		}
		if id.Public().PoWBits() >= bits {
			return id, nil
		}
	}
}
