package member

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"udisend/internal/ctxtool"
	"udisend/internal/logger"
	"udisend/internal/message"

	"github.com/gorilla/websocket"
)

type TCP struct {
	id   string
	conn *websocket.Conn
}

func NewTCP(id string, conn *websocket.Conn) *TCP {
	return &TCP{
		id:   id,
		conn: conn,
	}
}

func (m *TCP) ID() string {
	return m.id
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

func ServeWs(node *Node, w http.ResponseWriter, r *http.Request) {
	log.Println("New connection")
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	connectedMemberID := r.Header.Get("memberID")

	if strings.TrimSpace(connectedMemberID) == "" {
		http.Error(w, "please provide your memberID as a header", 400)
		conn.Close()
		return
	}

	memb := &TCP{
		id: connectedMemberID,
		disconnectSignal: func() {
			node.unregister <- connectedMemberID
		},
		conn:  conn,
		inbox: node.inbox,
		send:  make(chan message.Message, 256),
	}

	go memb.readPump()
	go memb.writePump()

	node.register <- memb

	<-time.After(1 * time.Second)

	err = node.Send(message.Outcome{
		To: connectedMemberID,
		Message: message.Message{
			Type: message.EntrypoinMemberID,
			Text: node.memberID,
		},
	})

	<-time.After(1 * time.Second)

	node.inbox <- message.Income{
		From:    connectedMemberID,
		Message: message.Message{Type: message.NewConnection},
	}

	if err != nil {
		log.Println("Error sending message", err.Error())
	}

}
