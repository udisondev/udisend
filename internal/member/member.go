package member

import (
	"context"
	"errors"
	"sync"
	"udisend/internal/message"
	"udisend/pkg/check/logger"
	"udisend/pkg/span"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

type Set struct {
	head    Member
	members sync.Map
}

type TCP struct {
	id         string
	conn       *websocket.Conn
	disconnect func()
	wrmu       sync.Mutex
}

type ICE struct {
	id         string
	pc         *webrtc.PeerConnection
	dc         *webrtc.DataChannel
	disconnect func()
}

type Member interface {
	ID() string
	Write([]byte) error
	Close(cause string)
	Listen(ctx context.Context) <-chan message.Income
}

func NewICE(ID string, pc *webrtc.PeerConnection, dc *webrtc.DataChannel, dicsonnect func()) ICE {
	return ICE{
		id:         ID,
		pc:         pc,
		dc:         dc,
		disconnect: dicsonnect,
	}
}

func (m *ICE) ID() string {
	return m.id
}

func (m *ICE) Write(b []byte) error {
	return m.dc.Send(b)
}

func (m *ICE) Close(cause string) {
	m.disconnect()
	m.Write(message.Event{Type: message.Disconnected, Payload: []byte(cause)}.Marshal())
	m.pc.Close()
	m.dc.Close()
}

func (m *ICE) Listen(ctx context.Context) <-chan message.Income {
	out := make(chan message.Income, 1)

	go func() {
		<-ctx.Done()
		close(out)
	}()

	m.dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		out <- message.Income{
			From: m.ID(),
			Event: message.Event{
				Type:    message.Type(msg.Data[0]),
				Payload: msg.Data[1:],
			},
		}
	})

	return out
}

func NewTCP(ID string, conn *websocket.Conn, disconnect func()) TCP {
	return TCP{
		id:         ID,
		conn:       conn,
		disconnect: disconnect,
	}
}

func (m *TCP) ID() string {
	return m.id
}

func (m *TCP) Write(b []byte) error {
	m.wrmu.Lock()
	defer m.wrmu.Unlock()
	return m.conn.WriteMessage(websocket.BinaryMessage, b)
}

func (m *TCP) Close(cause string) {
	m.disconnect()
	m.Write(message.Event{Type: message.Disconnected, Payload: []byte(cause)}.Marshal())
	m.conn.Close()
}

func (m *TCP) Listen(ctx context.Context) <-chan message.Income {
	out := make(chan message.Income)

	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_, in, err := m.conn.ReadMessage()
				logger.Debug(ctx, "Read bytes", "raw", string(in))
				if err != nil {
					out <- message.Income{
						From: m.ID(),
						Event: message.Event{
							Type:    message.InteractionFailed,
							Payload: []byte(err.Error()),
						},
					}
					return
				}
				out <- message.Income{
					From:  m.id,
					Event: message.Event{Type: message.Type(in[0]), Payload: in[1:]},
				}
			}
		}
	}()

	return out
}

func (s *Set) ConnectWithOther(ctx context.Context, from string) {
	ctx = span.Extend(ctx, "member.ConnectWithOther")

	s.members.Range(func(key, value any) bool {
		id, member := key.(string), value.(Member)
		if id == from {
			return true
		}

		err := member.Write(message.Event{
			Type:    message.ProvideConnectionSign,
			Payload: []byte(from),
		}.Marshal())
		if err != nil {
			logger.Error(ctx, "Error sending message", "for", member.ID(), "cause", err)
		}

		return true
	})
}

func NewSet() *Set {
	return &Set{}
}

func (s *Set) Add(m Member, isHead bool) (cleanup func()) {
	if isHead {
		s.head = m
	}
	s.members.Store(m.ID(), m)

	return func() {
		s.members.Delete(m.ID())
	}

}

var ErrNotFound = errors.New("not found")

func (s *Set) SendTo(ctx context.Context, member string, out message.Event) error {
	ctx = span.Extend(ctx, "member.SendTo")

	logger.Debug(ctx, "Sending message", "to", member, "type", out.Type.String())
	v, ok := s.members.Load(member)
	if !ok {
		return ErrNotFound
	}
	m := v.(Member)

	err := m.Write(out.Marshal())
	if err != nil {
		logger.Error(ctx, "Error sending message", "for", m.ID(), "cause", err)
		return err
	}

	return nil
}

func (s *Set) SendToTheHead(out message.Event) error {
	return s.head.Write(out.Marshal())
}

func (s *Set) Broadcast(out message.Event) {
	s.members.Range(func(_, value any) bool {
		m := value.(Member)
		m.Write(out.Marshal())
		return true
	})
}

func (s *Set) DisconnectiWithCause(member string, cause string) {
	if v, ok := s.members.Load(member); ok {
		m := v.(Member)
		m.Close(cause)
	}
}

func (s *Set) DisconnectAllWithCause(cause error) {
	s.members.Range(func(_, value any) bool {
		m := value.(Member)
		m.Close(cause.Error())
		return true
	})
}
