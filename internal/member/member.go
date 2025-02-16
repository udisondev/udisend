package member

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
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

func (s *Set) Add(
	ctx context.Context,
	ID string,
	isHead bool,
	income chan<- message.Income,
	conn *websocket.Conn,
) {
	membCtx, disconnect := context.WithCancelCause(ctx)
	memb := Struct{
		id:         ID,
		isHead:     isHead,
		conn:       conn,
		disconnect: disconnect,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[ID] = &memb

	if _, ok := s.members[ID]; ok {
		fmt.Printf("Member=%s added to list\n", memb.id)
	} else {
		fmt.Printf("Member=%s not found\n", memb.id)
	}

	go func() {
		<-membCtx.Done()
		fmt.Printf("Member=%s disconnected\n", ID)
		cause := membCtx.Err().Error()
		memb.send(message.Event{
			Type:    message.Disconnected,
			Payload: []byte(cause),
		})
		memb.conn.Close()
		s.mu.Lock()
		defer s.mu.Unlock()

		delete(s.members, ID)
	}()

	go memb.Listen(membCtx, income)
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
	income chan<- message.Income,
) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, in, err := m.conn.ReadMessage()
			if err != nil {
				fmt.Printf("Error listen member=%s: %v\n", m.id, err)
				m.disconnect(err)
				return
			}
			income <- message.Income{
				From:  m.id,
				Event: message.Event{Type: message.Type(in[0]), Payload: in[1:]},
			}
		}
	}
}

func (m *Set) Len() int {
	return len(m.members)
}

var ErrNotFound = errors.New("not found")

func (s *Set) SendTo(member string, out message.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if m, ok := s.members[member]; ok {
		m.disconnect(cause)
	}
}

func (s *Set) DisconnectAllWithCause(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.members {
		m.disconnect(cause)
	}
	s.members = nil
}
