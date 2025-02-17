package node

import (
	"context"
	"log"
	"net/http"
	"slices"
	"udisend/internal/member"
	"udisend/internal/message"
	"udisend/pkg/check"

	"github.com/gorilla/websocket"
)

func (n *Node) WorkWithMember(
	ctx context.Context,
) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		memberID := r.Header.Get("memberID")

		switch check.MemberID(memberID) {
		case check.ErrBlankMemberID:
			http.Error(w, "please provide your memberID as a header", 400)
			return
		}

		conn, err := n.upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("Upgrade error:", err)
			return
		}
		err = conn.WriteMessage(websocket.BinaryMessage, slices.Concat([]byte{message.HeadMemberID}, []byte(n.config.MemberID)))
		if err != nil {
			log.Printf("Error sending my memberID to member=%s", memberID)
			return
		}

		mCtx, disconnect := context.WithCancel(ctx)
		m := member.NewTCP(memberID, conn, disconnect)
		callback := n.members.Add(&m, false)
		n.income <- message.Income{From: m.ID(), Event: message.Event{Type: message.NewConnection}}

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
}
