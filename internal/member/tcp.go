package member

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"udisend/internal/ctxtool"
	"udisend/internal/logger"
	"udisend/internal/message"

	"github.com/gorilla/websocket"
)

type TCP struct {
	id    string
	conn  *websocket.Conn
	state State
	mu    sync.Mutex
}

func NewTCP(id string, conn *websocket.Conn) *TCP {
	return &TCP{
		id:    id,
		conn:  conn,
		state: NotVerified,
	}
}

func (m *TCP) ID() string {
	return m.id
}

func (m *TCP) State() State {
	return m.state
}

func (m *TCP) Upgrade(new State) {
	m.mu.Lock()
	m.state = new
	m.mu.Unlock()
}

func (m *TCP) Interact(ctx context.Context, out <-chan message.Message, disconnect func()) <-chan message.Income {
	ctx = ctxtool.Span(ctx, fmt.Sprintf("member.Interact with '%s'", m.id))
	inbox := make(chan message.Income)

	go func() {
		<-ctx.Done()
		close(inbox)
	}()

	go func() {
		defer disconnect()
		for msg := range out {
			w, err := m.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				logger.Errorf(ctx, "Error receive new writer")
				return
			}
			b, err := msg.Marshal()
			if err != nil {
				logger.Errorf(ctx, "msg.Marshal <msg=%s>: %v", msg.String(), err)
				continue
			}
			_, err = w.Write(b)
			if err != nil {
				logger.Errorf(ctx, "w.Write: %v", err)
			}

			if err := w.Close(); err != nil {
				logger.Errorf(ctx, "w.Close: %v", err)
				return
			}
		}
	}()

	go func() {
		defer disconnect()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_, b, err := m.conn.ReadMessage()
				if err != nil {
					if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
						logger.Errorf(ctx, "m.conn.ReadMessage: %v", err)
					}
					return
				}

				var in message.Message
				b = bytes.TrimSpace(bytes.Replace(b, newline, space, -1))
				err = in.Unmarshal(b)
				if err != nil {
					logger.Errorf(ctx, "in.Unmarshal: %v", err)
					continue
				}

				inbox <- message.Income{From: m.id, Message: in}
			}
		}
	}()

	return inbox
}
