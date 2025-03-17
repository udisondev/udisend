package network

import (
	"sync"
	"sync/atomic"
	"udisend/pkg/logger"
	"udisend/pkg/span"
)

type (
	Slot struct {
		mu   sync.Mutex
		conn *SafeConn
	}

	SafeConn struct {
		mesh     string
		outboxMu sync.Mutex
		state    ConnState
		conn     Connectable
		outbox   chan Signal
	}

	Connectable interface {
		ID() string
		Interact(outbox <-chan Signal) <-chan Income
	}

	ConnState uint8
)

const (
	ConnStateEmpty ConnState = iota
	ConnStateInit
	ConnStateVerified
	ConnStateConnected
)

func (s *Slot) Send(sig Signal) {
	if s.conn == nil {
		return
	}
	s.conn.send(sig)
}

func (s *Slot) Mesh() string {
	if s.conn == nil {
		return ""
	}
	return s.conn.mesh
}

func (s *Slot) AddConn(conn Connectable, verified bool) <-chan Income {
	ctx := span.Init("Slot.AddConn")
	logger.Debugf(ctx, "New connection")
	state := ConnStateInit
	if verified {
		state = ConnStateVerified
	}
	s.conn = &SafeConn{
		mesh:   conn.ID(),
		state:  state,
		conn:   conn,
		outbox: make(chan Signal, 256),
	}
	return s.conn.applyFilters(conn.Interact(s.conn.outbox))
}

func (s *Slot) TryLock() bool {
	if !s.mu.TryLock() {
		return false
	}
	if s.conn != nil {
		s.mu.Unlock()
		return false
	}
	return true
}

func (s *Slot) Unlock() {
	s.Free()
}

func (s *Slot) Free() {
	logger.Debugf(nil, "Going to free slot")
	if s.conn != nil {
		s.conn.disconnect()
	}
	s.conn = nil
	s.mu.TryLock()
	s.mu.Unlock()
}

func (s *Slot) UpgrageConn(newState ConnState) {
	if s.conn == nil {
		return
	}
	logger.Debugf(nil, "%s new state=%d", s.conn.mesh, newState)
	s.conn.state = newState
}

func (s *SafeConn) disconnect() {
	logger.Debugf(nil, "'%s' Disconnected!", s.mesh)
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()

	s.conn = nil
	close(s.outbox)
	s.outbox = nil
}

func (s *Slot) ConnState() ConnState {
	if s.conn == nil {
		return ConnStateEmpty
	}
	return s.conn.state
}

func (s *SafeConn) send(out Signal) {
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	if s.outbox == nil {
		return
	}
	select {
	case s.outbox <- out:
	default:
		go s.disconnect()
	}
}

func (s *SafeConn) applyFilters(ch <-chan Income) <-chan Income {
	out := ch
	filters := []func(<-chan Income) <-chan Income{
		s.notVerifiedFilter,
	}

	for _, f := range filters {
		out = f(out)
	}

	return out
}

func (s *SafeConn) notVerifiedFilter(ch <-chan Income) <-chan Income {
	out := make(chan Income)

	go func() {
		defer close(out)
		testReceived := atomic.Bool{}
		for in := range ch {
			if s.state == ConnStateVerified {
				out <- in
				continue
			}
			if in.Signal.Type != SignalTypeTestChallenge {
				return
			}
			if !testReceived.CompareAndSwap(false, true) {
				return
			}
			out <- in
		}
	}()

	return out
}
