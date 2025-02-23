package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"udisend/internal/ctxtool"
	"udisend/internal/logger"
	"udisend/internal/member"
	"udisend/internal/message"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type Member interface {
	ID() string
	Interact(ctx context.Context, outbox <-chan message.Message, disconnect func()) <-chan message.Income
}

type Script struct {
	id      string
	mu      sync.Mutex
	done    bool
	actions []Action
}

var (
	ErrMemberNotFound = errors.New("member not found")
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func (s *Script) Act(in message.Income) bool {
	if s.done {
		return true
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.done {
		return true
	}

	if len(s.actions) == 0 {
		s.done = true
		return true
	}
	done := s.actions[0](in)
	if !done {
		return false
	}

	if len(s.actions) == 1 {
		s.actions = nil
		s.done = true
		return true
	}

	s.actions = s.actions[1:]
	return false
}

func (n *Node) NewScript(ctx context.Context, acts ...Action) *Script {
	s := Script{
		id:      uuid.New().String(),
		actions: acts,
	}

	n.scriptsMu.Lock()
	n.scripts = append(n.scripts, &s)
	n.scriptsMu.Unlock()

	go func() {
		<-ctx.Done()
		s.done = true
	}()

	return &s
}

type Action func(in message.Income) bool

type ConnectedMember struct {
	send       chan<- message.Message
	disconnect func()
	member     Member
}

type Node struct {
	id           string
	stunServer   string
	inbox        chan message.Income
	scriptsMu    sync.Mutex
	scripts      []*Script
	membersMu    sync.Mutex
	members      map[string]ConnectedMember
	waitAnswerMu sync.Mutex
	waitAnswer   map[string]*member.AnswerICE
	signMapMu    sync.Mutex
	signMap      map[string]message.ConnectionSign
}

func New(myID string) *Node {
	return &Node{
		id:         myID,
		members:    make(map[string]ConnectedMember),
		inbox:      make(chan message.Income),
		stunServer: "stun:stun.l.google.com:19302",
		signMap:    make(map[string]message.ConnectionSign),
	}
}

func (n *Node) AddMember(ctx context.Context, m Member, disconnect func()) {
	mout := make(chan message.Message, 256)

	n.membersMu.Lock()
	n.members[m.ID()] = ConnectedMember{
		send:       mout,
		member:     m,
		disconnect: disconnect,
	}
	n.membersMu.Unlock()

	go func() {
		<-ctx.Done()
		close(mout)
		n.membersMu.Lock()
		delete(n.members, m.ID())
		n.membersMu.Unlock()
	}()

	in := m.Interact(ctx, mout, disconnect)

	go func() {
		for m := range in {
			n.inbox <- m
		}
	}()

}

func (n *Node) Run(ctx context.Context) {
	ctx = ctxtool.Span(ctx, "node.Run")
	logger.Debugf(ctx, "Run...")

	go func() {
		<-ctx.Done()
		logger.Debugf(ctx, "Shuting down...")
		close(n.inbox)
	}()

	for in := range n.inbox {
		n.dispatch(ctx, in)
	}
}

func (n *Node) Send(out message.Outcome) error {
	logger.Debugf(nil, "Going to send message to '%s'", out.To)

	m, ok := n.members[out.To]
	if !ok {
		logger.Debugf(nil, "Member '%s' not found", out.To)
		return fmt.Errorf("memberID=%s: %w", out.To, ErrMemberNotFound)
	}

	select {
	case m.send <- out.Message:
	default:
		logger.Debugf(nil, "Disconnecting '%s' (low throuput)", out.To)
		m.disconnect()
	}

	return nil
}

func (n *Node) AttachHead(ctx context.Context, entrypoint string) error {
	ctx = ctxtool.Span(ctx, "node.AttachHead")
	h := http.Header{}
	resp, err := http.Get(entrypoint + "/id")
	if err != nil {
		return fmt.Errorf("getting head ID: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("getting head ID: %w", err)
	}

	limitReader := io.LimitReader(resp.Body, 256)
	headID, err := io.ReadAll(limitReader)
	if err != nil {
		return fmt.Errorf("reading head id: %w", err)
	}

	h.Add("memberID", n.id)
	u := url.URL{Scheme: "ws", Host: entrypoint, Path: "/ws"}

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		return fmt.Errorf("connect to the entrypoint: %w", err)
	}

	conn.SetReadLimit(1024 * 5)
	head := member.NewTCP(string(headID), conn)
	memberCtx, disconnect := context.WithCancel(ctx)
	n.AddMember(memberCtx, head, disconnect)

	return nil
}

func (n *Node) ServeWs(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ctx = ctxtool.Span(ctx, "node.ServerWs")
	logger.Debugf(ctx, "New connection")

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Errorf(ctx, "Error upgrade request: %v", err)
		return
	}

	connectedMemberID := r.Header.Get("memberID")

	if strings.TrimSpace(connectedMemberID) == "" {
		http.Error(w, "please provide your memberID as a header", 400)
		conn.Close()
		return
	}

	memb := member.NewTCP(connectedMemberID, conn)
	memberCtx, disconnect := context.WithCancel(ctx)
	n.AddMember(memberCtx, memb, disconnect)

	n.inbox <- message.Income{From: connectedMemberID, Message: message.Message{Type: message.NewConnection}}
}
