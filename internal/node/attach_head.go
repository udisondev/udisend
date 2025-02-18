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
			mt, resp, err := conn.ReadMessage()
			if err != nil {
				panic(fmt.Errorf("Error attach to the head: %v", err))
			}

			e, err := message.ParseEvent(string(resp))
			if err != nil {
				logger.Error(ctx, "Error parsing event", "raw", string(resp), "messageType", mt)
				continue
			}

			if e.Type != message.HeadMemberID {
				continue
			}

			memberID = e.Text
			break waitMemberID
		}
	}

	mCtx, disconnect := context.WithCancel(ctx)
	m := member.NewTCP(memberID, conn, disconnect)
	callback := n.members.Add(&m, true)

	for {
		select {
		case <-mCtx.Done():
			logger.Debug(ctx, "Connection is closed", "member", memberID)
			callback()
			return
		case in, ok := <-m.Listen(mCtx):
			if !ok {
			logger.Debug(ctx, "Not listen yet", "member", memberID)
				callback()
				return
			}
			n.income <- in
		}
	}
}
