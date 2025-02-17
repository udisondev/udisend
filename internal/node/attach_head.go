package node

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
	"udisend/internal/member"
	"udisend/internal/message"
	"udisend/pkg/check/logger"
	"udisend/pkg/span"

	"github.com/gorilla/websocket"
)

func (n *Node) AttachHead(ctx context.Context) {
	ctx = span.Extend(ctx, "node.AttachHead")

	h := http.Header{}
	h.Add("memberID", n.config.MemberID)
	addr := fmt.Sprintf("ws://%s/ws", n.config.Parent)
	conn, _, err := websocket.DefaultDialer.Dial(addr, h)
	if err != nil {
		logger.Error(ctx, "Error connection to the head", "address", addr)
		panic(err)
	}
	defer conn.Close()

	var memberID string
waitMemberID:
	for {
		select {
		case <-time.After(time.Minute):
			panic(errors.New("Head MemberID not received!"))

		default:
			_, resp, err := conn.ReadMessage()
			if err != nil {
				panic(fmt.Errorf("Error attach to the head: %v", err))
			}

			if message.Type(resp[0]) != message.HeadMemberID {
				continue
			}

			memberID = string(resp[1:])
			break waitMemberID
		}
	}

	mCtx, disconnect := context.WithCancel(ctx)
	m := member.NewTCP(memberID, conn, disconnect)
	callback := n.members.Add(&m, true)

	for {
		select {
		case <-mCtx.Done():
			callback()
			return
		case in, ok := <-m.Listen(mCtx):
			if !ok {
				return
			}
			n.income <- in

		}
	}
}
