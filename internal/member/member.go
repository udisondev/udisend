package member

import (
	"context"
	"crypto/rsa"
	"errors"
	"log"
	"sync"
	"udisend/internal/message"

	"github.com/gorilla/websocket"
)

type Struct struct {
	id         string
	isHead     bool
	publicKey  rsa.PublicKey
	conn       *websocket.Conn
	relations  []string
	disconnect func(cause error)
}

type Set struct {
	head    *Struct
	members map[string]*Struct
	mu      sync.Mutex
}

func NewSet() *Set {
	return &Set{
		members: map[string]*Struct{},
	}
}

func New(ID string, isHead bool, conn *websocket.Conn, disconnect func(cause error)) *Struct {
	return &Struct{
		id:         ID,
		publicKey:  rsa.PublicKey{},
		conn:       conn,
		disconnect: disconnect,
	}
}

func (m *Struct) send(out message.Event) error {
	err := m.conn.WriteMessage(websocket.BinaryMessage, out.Marshal())
	if err != nil {
		return err
	}

	return nil
}

func (m *Struct) Listen(
	ctx context.Context,
	membCtx context.Context,
	income chan<- message.Income,
) {
	for {
		select {
		case <-membCtx.Done():
			cause := ctx.Err()
			e := message.Event{Type: message.Disconnected, Payload: []byte(cause.Error())}
			m.conn.WriteMessage(websocket.BinaryMessage, e.Marshal())
			m.DisconnectWithCause(cause)
			return

		case <-ctx.Done():
			e := message.Event{Type: message.IamShotdown}
			m.conn.WriteMessage(websocket.BinaryMessage, e.Marshal())
			m.DisconnectWithCause(errors.New("shotdown"))

		default:
			_, in, err := m.conn.ReadMessage()
			log.Printf("raw in: %s", string(in))
			if err != nil {
				m.disconnect(err)
				return
			}
			if len(in) < 2 {
				continue
			}
			income <- message.Income{
				From:  m.id,
				Event: message.Event{Type: message.Type(in[0]), Payload: in[1:]},
			}
		}
	}
}

func (s *Struct) DisconnectWithCause(cause error) {
	s.disconnect(cause)
}

func (m *Set) Len() int {
	return len(m.members)
}

func (m *Set) Push(memb *Struct) {
	if memb.isHead {
		m.head = memb
	}
	m.mu.Lock()
	m.members[memb.id] = memb
	m.mu.Unlock()
}

var ErrNotFound = errors.New("not found")

func (s *Set) SendTo(member string, out message.Event) error {
	m, ok := s.members[member]
	if !ok {
		return ErrNotFound
	}

	err := m.send(out)
	if err != nil {
		return err
	}

	return nil
}

func (s *Set) SendToTheHead(out message.Event) {
	s.head.conn.WriteMessage(websocket.BinaryMessage, out.Marshal())

}

func (s *Set) Broadcast(out message.Event) {
	for _, m := range s.members {
		m.send(out)
	}
}

func (s *Set) DisconnectiWithCause(member string, cause error) {
	s.mu.Lock()
	if m, ok := s.members[member]; ok {
		m.DisconnectWithCause(cause)
		delete(s.members, member)
	}
	s.mu.Unlock()
}

func (s *Set) DisconnectAllWithCause(cause error) {
	s.mu.Lock()
	for _, m := range s.members {
		m.disconnect(cause)
	}
	s.members = nil
	s.mu.Unlock()
}
