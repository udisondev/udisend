package node

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"udisend/broadcast"
	"udisend/config"
	"udisend/event"
	"udisend/member"
	"udisend/pkg/check"

	"github.com/gorilla/websocket"
)

type Node struct {
	config      config.Config
	upgrader    websocket.Upgrader
	clusterSize atomic.Uint32
	members     Members
}

func New(cfg config.Config) *Node {
	return &Node{
		config:  cfg,
		members: Members{},
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

type MemberSession struct {
	Member Member
}

func (n *Node) Serve(ctx context.Context) {
	income := make(chan event.Income)
	defer close(income)

	clstBroudcast := broadcast.NewClusterBroadcast()
	_, subscribe := clstBroudcast.Chan(ctx)
	http.HandleFunc(
		"/ws",
		n.WorkWithMember(
			ctx,
			func(m *member.Struct, e event.Event) {
				income <- event.Income{Member: m, Event: e}
			},
			subscribe,
		),
	)
	log.Fatal(http.ListenAndServe(n.config.GetAddress(), nil))
}

func (n *Node) WorkWithMember(ctx context.Context, putIncome func(m *member.Struct, e event.Event), clstrBroadcast func(ctx context.Context) <-chan event.Event) func(http.ResponseWriter, *http.Request) {
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
			case e := <-clstrBroadcast(membCtx):
				output, err := e.Marshal()
				if err != nil {
					continue
				}
				conn.WriteMessage(websocket.BinaryMessage, output)
			default:
				_, message, err := conn.ReadMessage()
				if err != nil {
					putIncome(memb, event.Event{Type: event.Disconnected})
					return
				}
				putIncome(memb, event.Event{Type: event.Type(message[0]), Payload: message[1:]})
			}
		}
	}
}

func routeMessage(sig SignalMsg) {
	clientsMu.Lock()
	defer clientsMu.Unlock()

	data, err := json.Marshal(sig)
	if err != nil {
		log.Println("Ошибка маршалинга сигнала:", err)
		return
	}

	// Если поле To задано, отправляем только указанному клиенту.
	if sig.To != "" {
		if target, ok := clients[sig.To]; ok {
			target.Conn.WriteMessage(websocket.BinaryMessage, data)
			log.Printf("Сообщение от %s отправлено клиенту %s", sig.From, sig.To)
		} else {
			log.Printf("Клиент %s не найден", sig.To)
		}
		return
	}

	// Иначе рассылаем всем, кроме отправителя.
	for id, client := range clients {
		if id == sig.From {
			continue
		}
		client.Conn.WriteMessage(websocket.TextMessage, data)
	}
	log.Printf("Сообщение от %s отправлено всем", sig.From)
}

func explainDisconnection(conn *websocket.Conn, cause string) {
	payload, err := event.Event{Type: event.Disconnected, Payload: []byte(cause)}.Marshal()
	if err != nil {
		payload = nil
	}
	conn.WriteMessage(websocket.BinaryMessage, payload)

}
