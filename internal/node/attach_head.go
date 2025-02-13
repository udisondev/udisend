package node

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"time"
	"udisend/internal/member"
	"udisend/internal/message"

	"github.com/gorilla/websocket"
)


func (n *Node) AttachHead(ctx context.Context,
	inbox func(ch <-chan message.Income)) {
	u, err := url.Parse(n.config.Parent)
	if err != nil {
		log.Fatal(err)
	}
	h := http.Header{}
	h.Add("memberID", n.config.ID)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), h)
	if err != nil {
		log.Fatal("Ошибка подключения к сигнальному серверу:", err)
		return
	}
	defer conn.Close()

	var headUdID string
waitHeadUdID:
	for {
		select {
		case <-time.After(time.Minute):
			log.Fatalf("Head MemberID not received!")
		default:
			_, resp, err := conn.ReadMessage()
			if err != nil {
				log.Fatalf("Error attach to the head: %v", err)
			}
			if message.Type(resp[0]) != message.HeadMemberID {
				continue
			}

			headUdID = string(resp[1:])
			break waitHeadUdID
		}
	}

	membCtx, disconnect := context.WithCancelCause(context.Background())
	memb := member.New(headUdID, true, conn, disconnect)
	n.members.Push(memb)
	inbox(memb.Listen(ctx, membCtx))
}
