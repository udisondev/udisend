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
	config      config.Config
	upgrader    websocket.Upgrader
	clusterSize atomic.Uint32
	members     *member.Set
	reacts      []func(message.Income) bool
	mu          sync.Mutex
}

func New(cfg config.Config) *Node {
	return &Node{
		config:  cfg,
		members: &member.Set{},
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (n *Node) Serve(ctx context.Context) error {
	commonInbox := make(chan message.Income)
	defer close(commonInbox)

	message.Inbox(commonInbox, n.Dispatch)

	http.HandleFunc(
		"/ws",
		n.WorkWithMember(
			ctx,
			func(income <-chan message.Income) {
				for message := range income {
					commonInbox <- message
				}
			},
		),
	)
	err := http.ListenAndServe(n.config.GetAddress(), nil)

	return err
}

func (n *Node) SendSDN(ctx context.Context) {
	iceServers := []webrtc.ICEServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: iceServers,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Обработка входящих DataChannel.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Получен DataChannel: %s", dc.Label())
		dc.OnOpen(func() {
			log.Println("DataChannel открыт!")
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			// Здесь явно показываем, что сообщение получено напрямую от другого узла.
			log.Printf("Прямое сообщение получено: %s", string(msg.Data))
		})
	})
}
