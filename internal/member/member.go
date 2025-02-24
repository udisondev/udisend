//go:generate go-enum --noprefix
package member

import (
	"time"
	"udisend/internal/message"
)

// ENUM(
//
//	not_verified,
//	verified,
//
// )
type State uint8

const (
	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 1024 * 5
)

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

type Interface interface {
	ID() string
	Send(out message.Message)
	Close()
}
