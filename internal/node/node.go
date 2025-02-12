package node

import (
	"context"
	"log"
	"net/http"
	"slices"
	"sync/atomic"
	"udisend/config"
	"udisend/internal/member"
	"udisend/internal/message"
	"udisend/internal/signal"
	"udisend/pkg/check"
	"udisend/pkg/crypt"

	"github.com/gorilla/websocket"
)

type Node struct {
	config      config.Config
	upgrader    websocket.Upgrader
	clusterSize atomic.Uint32
	members     member.Set
}

func New(cfg config.Config) *Node {
	return &Node{
		config:  cfg,
		members: member.Set{},
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

type IncomeDispatcher struct {
	outbox  chan<- message.Outcome
	signals chan<- signal.Interface
}

func (i *IncomeDispatcher) Dispatch(in message.Income) {
	switch in.Event.Type {
	case message.ConnectionSignRequested:
		connectionSignTo := append(in.Event.Payload, ',')
		connectionSign, _ := crypt.GenerateConnectionSign(64)
		i.outbox <- message.Outcome{
			To: in.From,
			Event: message.Event{
				Type:    message.ConnectionSignProvided,
				Payload: slices.Concat(connectionSignTo, connectionSign),
			},
		}
		i.signals <- signal.WaitConnection{
			Sign: connectionSign,
			From: connectionSignTo,
		}
	case message.ConnectionSignProvided:
		del := slices.Index(in.Event.Payload, ',')
		i.outbox <- message.Outcome{
			To: string(in.Event.Payload[:del]),
			Event: message.Event{
				Type:    message.DoConnect,
				Payload: in.Event.Payload[del+1:],
			},
		}
	}
}

func (n *Node) Serve(ctx context.Context) {
	inbox := make(chan message.Income)
	defer close(inbox)

	message.Inbox(inbox, func(in message.Income) {
		log.Printf("Received income: %v", in)
	})

	clstBroudcast := message.NewClusterBroadcast()
	_, subscribe := clstBroudcast.Chan(ctx)

	signalHandler := signal.NewHandler()
	signals := make(chan signal.Interface)
	signalHandler.Run(signals)

	outbox := message.NewOutbox()
	_, membeOutboxProvider := outbox.Chan(ctx)
	http.HandleFunc(
		"/ws",
		n.WorkWithMember(
			ctx,
			inbox,
			membeOutboxProvider,
			subscribe,
		),
	)
	log.Fatal(http.ListenAndServe(n.config.GetAddress(), nil))
}

func (n *Node) WorkWithMember(
	ctx context.Context,
	inbox chan<- message.Income,
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

		for {
			select {
			case <-ctx.Done():
				explainDisconnection(conn, "server closed")
				conn.Close()
				return

			case <-membCtx.Done():
				cause := membCtx.Err()
				explainDisconnection(conn, cause.Error())
				conn.Close()
				return

			case e := <-outChan:
				output, err := e.Marshal()
				if err != nil {
					continue
				}
				conn.WriteMessage(websocket.BinaryMessage, output)

			case e := <-clstrBroadcastChan:
				output, err := e.Marshal()
				if err != nil {
					continue
				}
				conn.WriteMessage(websocket.BinaryMessage, output)

			default:
				_, in, err := conn.ReadMessage()
				if err != nil {
					inbox <- message.Income{
						From:  nickname,
						Event: message.Event{Type: message.ErrReadMessage},
					}
				}
				inbox <- message.Income{
					From:  nickname,
					Event: message.Event{Type: message.Type(in[0]), Payload: in[1:]},
				}
			}
		}
	}
}

func explainDisconnection(conn *websocket.Conn, cause string) {
	payload, err := message.Event{Type: message.Disconnected, Payload: []byte(cause)}.Marshal()
	if err != nil {
		payload = nil
	}
	conn.WriteMessage(websocket.BinaryMessage, payload)

}
