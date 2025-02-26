package node

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
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
	id string

	listenPort string
	chatPort   string

	stunServer string
	inbox      chan message.Income

	messagesMu sync.RWMutex
	messages   map[string][]message.PrivateMessage

	privateSignKey *ecdsa.PrivateKey
	publicSignKey  *ecdsa.PublicKey

	scriptsMu sync.Mutex
	scripts   []*script

	myCluster        cluster
	existingClusters []string

	membersMu sync.RWMutex
	members   map[string]connectedMember

	waitSigningMu sync.Mutex
	waitSigning   map[string]challenge

	waitAnswerMu sync.Mutex
	waitAnswer   map[string]*member.AnswerICE

	signMapMu sync.Mutex
	signMap   map[string]message.ConnectionSign
}

func New(myID string, privateSignKey *ecdsa.PrivateKey, publicSignKey *ecdsa.PublicKey, chatPort, listenPort string) *Node {
	return &Node{
		id:             myID,
		chatPort:       chatPort,
		listenPort:     listenPort,
		members:        make(map[string]connectedMember),
		inbox:          make(chan message.Income, 100),
		waitAnswer:     make(map[string]*member.AnswerICE),
		waitSigning:    make(map[string]challenge),
		myCluster:      cluster{members: make(map[string]ClusterMember)},
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

	for range runtime.NumCPU() {
		go func() {
			for m := range in {
				n.inbox <- m
			}
		}()
	}
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

	if n.listenPort != "" {
		go func() {
			// Создаем отдельный multiplexer для этого сервера
			muxWs := http.NewServeMux()
			muxWs.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
				n.ServeWs(ctx, w, r)
			})
			muxWs.HandleFunc("/id", func(w http.ResponseWriter, r *http.Request) {
				logger.Debugf(ctx, "ID requested <ID:%s>", n.id)
				w.Write([]byte(n.id))
			})

			logger.Infof(ctx, "Stat listening %s", n.listenPort)
			if err := http.ListenAndServe(n.listenPort, muxWs); err != nil {
				logger.Errorf(ctx, "Error listening <addr:%s> %v", n.listenPort, err)
			}
		}()
	}

	go func() {
		// Создаем отдельный multiplexer для этого сервера
		muxChat := http.NewServeMux()

		fs := http.FileServer(http.Dir("static"))
		muxChat.Handle("/", fs)
		muxChat.HandleFunc("/chat/users", n.handleUsers)
		muxChat.HandleFunc("/chat/messages", n.handleMessages)
		muxChat.HandleFunc("/chat/send", n.handleSend)

		logger.Infof(ctx, "Open localhost:%s to use the chat", n.chatPort)
		if err := http.ListenAndServe(n.chatPort, muxChat); err != nil {
			panic(err)
		}
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

	authKey, err := crypt.PublicKeyToPEM(n.publicSignKey)
	if err != nil {
		return fmt.Errorf("crypt.PublicKeyToPEM: %v", err)
	}

	logger.Debugf(ctx, "My auth key: %s", authKey)

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
	h.Add("Auth-Key", url.QueryEscape(authKey))

	_, err = crypt.GetECDSAPublicKeyFromPEM(authKey)
	if err == nil {
		logger.Debugf(ctx, "I can decode my auth key")
	} else {
		logger.Errorf(ctx, "Error decode my auth key: %v", err)
		return err
	}
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

	connectedMemberID := r.Header.Get("Member-ID")
	if strings.TrimSpace(connectedMemberID) == "" {
		return
	}

	authPubKey := r.Header.Get("Auth-Key")
	authPubKey, err := url.QueryUnescape(authPubKey)
	if err != nil {
		return
	}

	logger.Debugf(ctx, "Connection=%s provided pubKey=%s", connectedMemberID, authPubKey)

	memberAuthKey, err := crypt.GetECDSAPublicKeyFromPEM(authPubKey)
	if err != nil {
		return
	}

	clstrMemb, ok := n.myCluster.members[connectedMemberID]
	if ok {
		if !clstrMemb.pubKey.Equal(memberAuthKey) {
			logger.Errorf(ctx, "Provided wrong Auth-Key")
			return
		}
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Errorf(ctx, "Error upgrade request: %v", err)
		return
	}

	memb := member.NewTCP(connectedMemberID, conn)
	memberCtx, disconnect := context.WithCancel(ctx)
	n.AddMember(memberCtx, memb, disconnect)

	n.myCluster.members[connectedMemberID] = ClusterMember{
		id:     connectedMemberID,
		pubKey: memberAuthKey,
	}

	n.inbox <- message.Income{From: connectedMemberID, Message: message.Message{Type: message.DoVerify}}
}

func (n *Node) disonnect(ID string) {
	n.membersMu.RLock()
	m, ok := n.members[ID]
	n.membersMu.RUnlock()
	if !ok {
		logger.Debugf(nil, "Has no member '%s' to disconnect", ID)
		return
	}
	m.disconnect()
}
