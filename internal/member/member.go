// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package member

import (
	"time"
	"udisend/internal/message"
)

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
