package node

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"
	"udisend/config"
	"udisend/internal/dispatcher"
	"udisend/internal/member"
	"udisend/internal/message"
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

func (n *Node) Serve(ctx context.Context) error {
	commonInbox := make(chan message.Income)
	defer close(commonInbox)

	disp := dispatcher.New(n.members)
	message.Inbox(commonInbox, disp.Dispatch)

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

func (n *Node) WorkWithMember(
	ctx context.Context,
	inbox func(ch <-chan message.Income),
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

		membCtx, disconnect := context.WithCancelCause(context.Background())
		memb := member.New(nickname, conn, disconnect)
		n.members.Push(memb)

		inbox(memb.Listen(ctx, membCtx))
	}
}
