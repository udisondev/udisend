package node

import (
	"bytes"
	"log"
	"net/http"
	"strings"
	"time"
	"udisend/internal/message"

	"github.com/gorilla/websocket"
)

type TCPMember struct {
	id               string
	disconnectSignal func()
	conn             *websocket.Conn
	inbox            chan<- message.Income
	send             chan message.Message
}

func (m *TCPMember) ID() string {
	return m.id
}

func (m *TCPMember) Send(out message.Message) {
	m.send <- out
}

func (m *TCPMember) readPump() {
	defer m.Close()

	m.conn.SetReadLimit(maxMessageSize)
	m.conn.SetReadDeadline(time.Now().Add(pongWait))
	for {
		_, b, err := m.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}

		var in message.Message
		b = bytes.TrimSpace(bytes.Replace(b, newline, space, -1))
		err = in.Unmarshal(b)
		if err != nil {
			log.Println("Broken income", "memberID", m.id, "cause", err.Error())
			continue
		}

		if in.Type == message.Ping {
			m.Send(message.Message{Type: message.Pong})
			continue
		}

		if in.Type == message.Pong {
			m.conn.SetReadDeadline(time.Now().Add(pongWait))
		}

		m.inbox <- message.Income{From: m.id, Message: in}
	}
}

func (m *TCPMember) Close() {
	m.disconnectSignal()
	m.conn.Close()
}

// writePump pumps messages from the hub to the websocket connection.
//
// A goroutine running writePump is started for each connection. The
// application ensures that there is at most one writer to a connection by
// executing all writes from this goroutine.
func (m *TCPMember) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		m.conn.Close()
	}()
	for {
		select {
		case message, ok := <-m.send:
			log.Println("Going to send", message.String())
			if !ok {
				// The hub closed the channel.
				m.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := m.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				log.Println("error receive writer", err.Error())
				return
			}
			b, err := message.Marshal()
			if err != nil {
				log.Printf("error: %v\n", err)
				continue
			}
			_, err = w.Write(b)
			if err != nil {
				log.Println("error write message", err.Error())
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			m.Send(message.Message{Type: message.Ping})
		}
	}
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

	memb := &TCPMember{
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
