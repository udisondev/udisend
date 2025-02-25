package node

import (
	"context"
	"crypto/ecdsa"
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
	"udisend/pkg/crypt"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type Member interface {
	ID() string
	Interact(ctx context.Context, outbox <-chan message.Message, disconnect func()) <-chan message.Income
	State() member.State
	Upgrade(member.State)
}

type script struct {
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

func (s *script) Act(in message.Income) bool {
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

func (n *Node) NewScript(ctx context.Context, acts ...Action) *script {
	s := script{
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

type connectedMember struct {
	send       chan<- message.Message
	disconnect func()
	member     Member
}

type cluster struct {
	id      string
	members map[string]ClusterMember
}

type ClusterMember struct {
	id        string
	clusterID string
	pubKey    *ecdsa.PublicKey
}

type challenge struct {
	For    string
	Value  []byte
	PubKey *ecdsa.PublicKey
}

type Node struct {
	id         string
	stunServer string
	inbox      chan message.Income

	privateSignKey *ecdsa.PrivateKey
	publicSignKey  *ecdsa.PublicKey

	scriptsMu sync.Mutex
	scripts   []*script

	myCluster        cluster
	existingClusters []string

	membersMu sync.Mutex
	members   map[string]connectedMember

	waitSigningMu sync.Mutex
	waitSigning   map[string]challenge

	waitAnswerMu sync.Mutex
	waitAnswer   map[string]*member.AnswerICE

	signMapMu sync.Mutex
	signMap   map[string]message.ConnectionSign
}

func New(myID string, privateSignKey *ecdsa.PrivateKey, publicSignKey *ecdsa.PublicKey) *Node {
	return &Node{
		id:             myID,
		members:        make(map[string]connectedMember),
		inbox:          make(chan message.Income),
		waitAnswer:     make(map[string]*member.AnswerICE),
		scripts:        make([]*script, 0),
		stunServer:     "stun:stun.l.google.com:19302",
		signMap:        make(map[string]message.ConnectionSign),
		privateSignKey: privateSignKey,
		publicSignKey:  publicSignKey,
	}
}

func (n *Node) AddMember(ctx context.Context, m Member, disconnect func()) {
	logger.Debugf(ctx, "New member '%s'", m.ID())
	mout := make(chan message.Message, 256)

	n.membersMu.Lock()
	n.members[m.ID()] = connectedMember{
		send:       mout,
		member:     m,
		disconnect: disconnect,
	}
	n.membersMu.Unlock()

	go func() {
		<-ctx.Done()
		logger.Debugf(ctx, "Member '%s' disconnected", m.ID())
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
	n.myCluster = cluster{
		id:      uuid.New().String(),
		members: map[string]ClusterMember{},
	}

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
	logger.Debugf(ctx, "Going to request head ID by calling GET %s/id...", entrypoint)

	pubKey, err := crypt.PublicKeyToPEM(n.publicSignKey)
	if err != nil {
		return fmt.Errorf("crypt.PublicKeyToPEM: %v", err)
	}

	h := http.Header{}
	resp, err := http.Get(fmt.Sprintf("http://%s/id", entrypoint))
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

	logger.Debugf(ctx, "Received head ID '%s'", string(headID))

	h.Add("Member-ID", n.id)
	h.Add("Auth-Key", pubKey)
	u := url.URL{Scheme: "ws", Host: entrypoint, Path: "/ws"}

	logger.Debugf(ctx, "Websocket connection with '%s'", headID)
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

	connectedMemberID := r.Header.Get("Member-ID")
	if strings.TrimSpace(connectedMemberID) == "" {
		http.Error(w, "please provide your Member-ID as a header", 400)
		conn.Close()
		return
	}

	authPubKey := r.Header.Get("Auth-Key")
	if strings.TrimSpace(connectedMemberID) == "" {
		http.Error(w, "please provide your Auth-Key as a header", 400)
		conn.Close()
		return
	}

	memberAuthKey, err := crypt.GetECDSAPublicKeyFromPEM(authPubKey)
	if err != nil {
		http.Error(w, "invalid auth Auth-Key", 400)
		conn.Close()
		return
	}

	clstrMemb, ok := n.myCluster.members[connectedMemberID]
	if ok {
		if !clstrMemb.pubKey.Equal(memberAuthKey) {
			http.Error(w, "wrong Auth-Key", 400)
			conn.Close()
			return
		}
	}

	memb := member.NewTCP(connectedMemberID, conn)
	memberCtx, disconnect := context.WithCancel(ctx)
	n.AddMember(memberCtx, memb, disconnect)

	n.myCluster.members[connectedMemberID] = ClusterMember{
		id:     connectedMemberID,
		pubKey: memberAuthKey,
	}

	n.inbox <- message.Income{From: connectedMemberID, Message: message.Message{Type: message.NewConnection}}
}
