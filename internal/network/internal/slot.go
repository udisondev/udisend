package network

import (
	"sync"
	"sync/atomic"
	"udisend/pkg/logger"
)

type (
	Slot struct {
		mu   sync.Mutex
		conn *SafeConn
	}

	SafeConn struct {
		id       string
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
	ConnStateInit ConnState = iota
	ConnStateVerified
)

func (s *Slot) AddConn(conn Connectable) <-chan Income {
	logger.Debugf(nil, "New connection: %s", conn.ID())
	s.conn = &SafeConn{
		id:     conn.ID(),
		state:  ConnStateInit,
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
	s.mu.Unlock()
}

func (s *Slot) Conn() *SafeConn {
	return s.conn
}

func (s *SafeConn) disconnect() {
	logger.Debugf(nil, "'%s' Disconnected!", s.id)
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()

	s.conn = nil
	close(s.outbox)
	s.outbox = nil
}

func (s *SafeConn) IsVerified() bool {
	return s.state > ConnStateInit
}

func (s *SafeConn) Send(out Signal) {
	defer func() {
		if v := recover(); v != nil {
			s.disconnect()
		}
	}()

	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	if s.outbox == nil {
		return
	}
	select {
	case s.outbox <- out:
	default:
		s.disconnect()
	}
}

func (s *SafeConn) Mesh() string {
	return s.id
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
