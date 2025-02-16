package node

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"
	"udisend/internal/message"

	"github.com/gorilla/websocket"
)

func (n *Node) AttachHead(ctx context.Context) {
	h := http.Header{}
	h.Add("memberID", n.config.MemberID)
	conn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://%s/ws", n.config.Parent), h)
	if err != nil {
		log.Fatal("Ошибка подключения к сигнальному серверу:", err)
		return
	}
	defer conn.Close()

	var memberID string
waitMemberID:
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

			memberID = string(resp[1:])
			break waitMemberID
		}
	}

	n.members.Listen(ctx, memberID, n.income, true, conn)
}
