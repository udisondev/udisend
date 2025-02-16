package node

import (
	"context"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"udisend/config"
	"udisend/internal/member"
	"udisend/internal/message"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

type Node struct {
	config          config.Config
	upgrader        websocket.Upgrader
	clusterSize     atomic.Uint32
	members         *member.Set
	reacts          []func(message.Income) bool
	mu              sync.Mutex
	peerConnections map[string]*webrtc.PeerConnection
	pcMutex         sync.Mutex
	dataChannels    map[string]*webrtc.DataChannel
	dcMutex         sync.Mutex
	income          chan message.Income
	signMap         map[string][]byte
}

func New(ctx context.Context, cfg config.Config) *Node {
	income := make(chan message.Income)
	members := member.NewSet()
	go func() {
		<-ctx.Done()
		members.DisconnectAllWithCause(ctx.Err())
	}()
	return &Node{
		config:          cfg,
		income:          income,
		members:         members,
		peerConnections: map[string]*webrtc.PeerConnection{},
		dataChannels:    map[string]*webrtc.DataChannel{},
		signMap:         map[string][]byte{},
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (n *Node) Serve(ctx context.Context) error {
	go func() {
		message.Inbox(n.income, n.Dispatch)
	}()

	http.HandleFunc(
		"/ws",
		n.WorkWithMember(ctx),
	)
	err := http.ListenAndServe(n.config.GetPort(), nil)
	if err != nil {
		log.Printf("error listen: %v\n", err)
	}

	return err
}

func (n *Node) ListenMe(input <-chan message.Outcome) {
	for in := range input {
		n.members.SendTo(in.To, message.Event{
			Type:    message.ForYou,
			Payload: in.Content,
		})
	}
}
