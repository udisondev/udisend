package node

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"
	"udisend/config"
	"udisend/internal/member"
	"udisend/internal/message"
	"udisend/internal/signal"
	"udisend/pkg/check"

	"github.com/gorilla/websocket"
)

type Node struct {
	config      config.Config
	upgrader    websocket.Upgrader
	clusterSize atomic.Uint32
	members     *member.Set
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

func (n *Node) Serve(ctx context.Context) {
	inbox := make(chan message.Income)
	defer close(inbox)

	outbox, signals := message.Inbox(inbox)
	subscribeOutcome := message.Outbox(outbox)

	signalHandler := signal.NewHandler(n.members)
	signalHandler.Run(signals)

	clusterBroadcastCh := make(chan message.Event)
	defer close(clusterBroadcastCh)
	subscribeClusterBroadcast := message.Broadcast(clusterBroadcastCh)

	http.HandleFunc(
		"/ws",
		n.WorkWithMember(
			ctx,
			func(income <-chan message.Income) {
				for in := range income {
					inbox <- in
				}
			},
			subscribeOutcome,
			subscribeClusterBroadcast,
		),
	)
	log.Fatal(http.ListenAndServe(n.config.GetAddress(), nil))
}

func (n *Node) WorkWithMember(
	ctx context.Context,
	inbox func(ch <-chan message.Income),
	outbox func(ctx context.Context, nickname string) <-chan message.Event,
	clstrBroadcast func(ctx context.Context) <-chan message.Event,
) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		nickname := r.Header.Get("nickname")

		switch check.Nickname(nickname) {
		case check.ErrBlankNickname:
			http.Error(w, "please specify nickname", 400)
			return
		}

		conn, err := n.upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("Upgrade error:", err)
			return
		}

		membCtx, disconnect := context.WithCancelCause(ctx)
		memb := member.New(nickname, conn, disconnect)
		n.members.Push(memb)

		outChan := outbox(membCtx, nickname)
		clstrBroadcastChan := clstrBroadcast(membCtx)

	}
}
