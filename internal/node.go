package node

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"

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

	stunServer string

	inbox chan message.Income

	register chan Member

	unregister chan string

	reacts []func(message.Income) bool

	membersMu sync.Mutex
	members   map[string]Member

	pcMapMu sync.Mutex
	pcMap   map[string]*ICEMember

	signMapMu sync.Mutex
	signMap   map[string]message.ConnectionSign
}

func New(memberID string) *Node {
	return &Node{
		memberID:   memberID,
		inbox:      make(chan message.Income, 256),
		register:   make(chan Member),
		members:    make(map[string]Member),
		unregister: make(chan string),
		stunServer: "stun:stun.l.google.com:19302",
		pcMap:      make(map[string]*ICEMember),
		signMap:    make(map[string]message.ConnectionSign),
	}
}

func (n *Node) Run() {
	for {
		select {
		case member := <-n.register:
			log.Println("New member connected", "ID="+member.ID())

			n.membersMu.Lock()
			n.members[member.ID()] = member
			n.membersMu.Unlock()
		case memberID := <-n.unregister:
			log.Println("Member disconnected", "ID="+memberID)
			m, ok := n.members[memberID]
			if !ok {
				continue
			}

			n.membersMu.Lock()
			delete(n.members, memberID)
			n.membersMu.Unlock()

			m.Close()
		case message, ok := <-n.inbox:
			if !ok {
				return
			}
			n.dispatch(message)
		}
	}
}

func (n *Node) Send(out message.Outcome) error {
	m, ok := n.members[out.To]
	if !ok {
		return fmt.Errorf("memberID=%s: %w", out.To, ErrMemberNotFound)
	}

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
	for {
		_, b, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			conn.Close()
			return
		}
		var in message.Message
		b = bytes.TrimSpace(bytes.Replace(b, newline, space, -1))
		err = in.Unmarshal(b)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			conn.Close()
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
		inbox: n.inbox,
		send:  make(chan message.Message, 256),
	}

	go memb.readPump()
	go memb.writePump()

	n.register <- memb
}
