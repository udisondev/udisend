package dht

import (
	"net"
	"time"
)

// Contact is a known peer: routing ID + transport address + metadata about
// when it was last seen.
type Contact struct {
	ID       NodeID
	Addr     net.Addr
	LastSeen time.Time
}
