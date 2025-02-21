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

	"udisend/internal/message"

	"github.com/gorilla/websocket"
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
	register chan Member

	// Unregister requests from clients.
	unregister chan string

	reacts []func(message.Income) bool

	pcMutex sync.Mutex

	peerConnections map[string]*ICEMember

	signMap map[string]message.ConnectionSign

	stunServer string
}

func New(memberID string) *Node {
	return &Node{
		memberID:   memberID,
		inbox:      make(chan message.Income, 256),
		register:   make(chan Member),
		unregister: make(chan string),
		stunServer: "stun:stun.l.google.com:19302",
	}
}

func (n *Node) Run() {
	for {
		select {
		case member := <-n.register:
			log.Println("New member", "ID", member.ID())
			n.members.Store(member.ID(), member)
			n.inbox <- message.Income{
				From: member.ID(),
			}
		case memberID := <-n.unregister:
			log.Println("Member disconnected", "ID", memberID)
			if v, ok := n.members.Load(memberID); ok {
				m := v.(Member)
				n.members.Delete(memberID)
				m.Close()
			}
		case message, ok := <-n.inbox:
			if !ok {
				return
			}
			n.dispatch(message)
		}
	}
}

func (n *Node) Send(out message.Outcome) error {
	v, ok := n.members.Load(out.To)
	if !ok {
		return fmt.Errorf("memberID=%s: %w", out.To, ErrMemberNotFound)
	}
	m := v.(Member)
	m.Send(out.Message)

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

	memb := &TCPMember{
		id: memberID,
		disconnectSignal: func() {
			n.unregister <- memberID
		},
		conn:  conn,
		inbox: make(chan<- message.Income),
		send:  make(chan message.Message, 256),
	}

	go memb.readPump()
	go memb.writePump()

	n.register <- memb
}
