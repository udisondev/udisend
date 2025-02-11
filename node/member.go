package node

import (
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/gorilla/websocket"
)

type Member struct {
	ID        string
	mu        sync.Mutex
	Relations map[string]*Partner
	conn      *websocket.Conn
}

func (m *Member) RelateFn(fn func() *Partner) {
	if p := fn(); p != nil {
		m.mu.Lock()
		m.Relations[p.Nick] = p
		m.mu.Unlock()
	}
}

type Partner struct {
	Nick string
}

type Members struct {
	members map[string]*Member
	mu      sync.Mutex
}

func (m *Members) Len() int {
	return len(m.members)
}

func (m *Members) Push(nick string, memb *Member) {
	m.mu.Lock()
	m.members[nick] = memb
	m.mu.Unlock()
}
func (m *Members) Pop() *Member {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.SortedFunc(maps.Values(m.members), func(a, b *Member) int {
		switch {
		case len(a.Relations) < len(b.Relations):
			return -1
		case len(a.Relations) > len(b.Relations):
			return 1
		default:
			return 0
		}
	})[0]
}
