package node

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"udisend/internal/message"
)

var (
	ErrMemberNotFound = errors.New("member not found")
)

// Node maintains the set of active clients and broadcasts messages to the
// clients.
type Node struct {
	memberID string

	// Registered members.
	members sync.Map

	// Inbound messages from the clients.
	inbox chan message.Income

	// Register requests from the clients.
	register chan *Member

	// Unregister requests from clients.
	unregister chan string

	reacts []func(message.Income) bool
}

func New(memberID string) *Node {
	return &Node{
		memberID:   memberID,
		inbox:      make(chan message.Income),
		register:   make(chan *Member),
		unregister: make(chan string),
	}
}

func (n *Node) Run() {
	for {
		select {
		case member := <-n.register:
			log.Println("New member", "ID", member.id)
			n.members.Store(member.id, member)
		n.inbox <- message.Income{
				From: member.id,
			}
		case memberID := <-n.unregister:
			log.Println("Member disconnected", "ID", memberID)
			if v, ok := n.members.Load(memberID); ok {
				m := v.(*Member)
				n.members.Delete(memberID)
				close(m.send)
			}
		case message := <-n.inbox:
			n.dispatch(message)
		}
	}
}

func (n *Node) Send(out message.Outcome) error {
	v, ok := n.members.Load(out.To)
	if !ok {
		return fmt.Errorf("memberID=%s: %w", out.To, ErrMemberNotFound)
	}
	m := v.(*Member)
	m.send <- out.Message
	return nil
}

func (n *Node) AttachHead(entrypoint string) {
	h := http.Header{}
	h.Add("memberID", n.memberID)
	u := url.URL{Scheme: "ws", Host: entrypoint, Path: "/ws"}

	log.Printf("connecting to %s", u.String())
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		panic(err)
	}

	var memberID string
	conn.SetReadLimit(maxMessageSize)
	conn.SetReadDeadline(time.Now().Add(pongWait))

	for {
		_, b, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			return
		}
		var in message.Message
		b = bytes.TrimSpace(bytes.Replace(b, newline, space, -1))
		err = in.Unmarshal(b)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			return
		}

		if in.Type != message.EntrypoinMemberID {
			continue
		}

		memberID = in.Text
		break
	}

	client := &Member{id: memberID, node: n, conn: conn, send: make(chan message.Message, 256)}
	go client.writePump()
	go client.readPump()

	client.node.register <- client
}
