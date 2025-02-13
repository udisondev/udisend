package member

import (
	"context"
	"crypto/rsa"
	"maps"
	"slices"
	"sync"
	"udisend/internal/message"

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

func (m *Struct) Interact(ctx context.Context, inbox func(in <-chan message.Income), outbox <-chan message.Event, clstrBroadcastChan <-chan message.Event) {
	forward := make(chan message.Income)
	go func() {
		defer close(forward)
		for {
			select {
			case <-ctx.Done():
				cause := ctx.Err()
				payload, err := message.Event{Type: message.Disconnected, Payload: []byte(cause.Error())}.Marshal()
				if err != nil {
					payload = nil
				}
				m.conn.WriteMessage(websocket.BinaryMessage, payload)
				m.DisconnectWithCause(cause)

				return

			case e, ok := <-outbox:
				if !ok {
					continue
				}

				output, err := e.Marshal()
				if err != nil {
					continue
				}
				m.conn.WriteMessage(websocket.BinaryMessage, output)

			case e, ok := <-clstrBroadcastChan:
				if !ok {
					continue
				}

				output, err := e.Marshal()
				if err != nil {
					continue
				}
				m.conn.WriteMessage(websocket.BinaryMessage, output)

			default:
				_, in, err := m.conn.ReadMessage()
				if err != nil {
					forward <- message.Income{
						From:  m.ID,
						Event: message.Event{Type: message.ErrReadMessage},
					}
				}
				forward <- message.Income{
					From:  m.ID,
					Event: message.Event{Type: message.Type(in[0]), Payload: in[1:]},
				}
			}
		}
	}()

	inbox(forward)
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
