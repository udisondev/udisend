package member

import (
	"crypto/rsa"
	"maps"
	"slices"
	"sync"

	"github.com/gorilla/websocket"
)

type Struct struct {
	ID         string
	publicKey  rsa.PublicKey
	conn       *websocket.Conn
	relations  []string
	disconnect func(cause error)
}

type Set struct {
	members map[string]*Struct
	mu      sync.Mutex
}

func New(ID string, conn *websocket.Conn, disconnect func(cause error)) *Struct {
	return &Struct{
		ID:         ID,
		publicKey:  rsa.PublicKey{},
		conn:       conn,
		disconnect: disconnect,
	}
}

func (s *Struct) DisconnectWithCause(cause error) {
	s.disconnect(cause)
}

func (m *Set) Len() int {
	return len(m.members)
}

func (m *Set) Push(memb *Struct) {
	m.mu.Lock()
	m.members[memb.ID] = memb
	m.mu.Unlock()
}
func (m *Set) Pop() *Struct {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.SortedFunc(maps.Values(m.members), func(a, b *Struct) int {
		switch {
		case len(a.relations) < len(b.relations):
			return -1
		case len(a.relations) > len(b.relations):
			return 1
		default:
			return 0
		}
	})[0]
}

func (s *Set) DisconnectiWithCause(member string, cause error) {
	s.mu.Lock()
	if m, ok := s.members[member]; ok {
		m.DisconnectWithCause(cause)
		delete(s.members, member)
	}
	s.mu.Unlock()
}
