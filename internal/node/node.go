package node

import (
	"context"
	"encoding/json"
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
	config          config.Config
	upgrader        websocket.Upgrader
	clusterSize     atomic.Uint32
	members         *member.Set
	reacts          []func(message.Income) bool
	mu              sync.Mutex
	peerConnections map[string]*webrtc.PeerConnection
	pcMutex         sync.Mutex
	dataChannels    map[string]*webrtc.DataChannel
	dcMutex         sync.Mutex
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

// setupPCHandlers настраивает обработчики для PeerConnection и DataChannel.
func (n *Node) setupPCHandlers(pc *webrtc.PeerConnection, peerID string) {
	// При получении DataChannel сохраняем его для обмена сообщениями.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Получен DataChannel от %s: %s", peerID, dc.Label())
		dc.OnOpen(func() {
			log.Printf("DataChannel для %s открыт", peerID)
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("Прямое сообщение получено от %s: %s", peerID, string(msg.Data))
		})
		n.dcMutex.Lock()
		n.dataChannels[peerID] = dc
		n.dcMutex.Unlock()
	})
}

func sendSignal(conn *websocket.Conn, sig SignalMsg) {
	data, err := json.Marshal(sig)
	if err != nil {
		log.Println("Ошибка маршалинга сигнала:", err)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Println("Ошибка отправки сигнала:", err)
	}
}

func (n *Node) createOfferFor(target string, conn *websocket.Conn) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	})
	if err != nil {
		log.Println("Ошибка создания PeerConnection:", err)
		return
	}
	n.pcMutex.Lock()
	n.peerConnections[target] = pc
	n.pcMutex.Unlock()
	n.setupPCHandlers(pc, target)

	// Создаем DataChannel.
	dc, err := pc.CreateDataChannel("data", nil)
	if err != nil {
		log.Println("Ошибка создания DataChannel:", err)
		return
	}
	n.dcMutex.Lock()
	n.dataChannels[target] = dc
	n.dcMutex.Unlock()
	dc.OnOpen(func() {
		log.Printf("DataChannel для %s открыт", target)
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		log.Printf("Прямое сообщение получено от %s: %s", target, string(msg.Data))
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Println("Ошибка создания offer:", err)
		return
	}
	if err = pc.SetLocalDescription(offer); err != nil {
		log.Println("Ошибка установки локального описания:", err)
		return
	}
	<-webrtc.GatheringCompletePromise(pc)
	offerMsg := SignalMsg{
		Type: TypeOffer,
		SDP:  pc.LocalDescription().SDP,
		To:   target,
	}
	sendSignal(conn, offerMsg)
}


// handleSignal обрабатывает полученные сигнальные сообщения и управляет PeerConnection.
func (n *Node) handleSignal(sig SignalMsg, conn *websocket.Conn) {
	switch sig.Type {
	case TypeOffer:
		log.Printf("Получен offer от %s", sig.From)
		// Если еще нет соединения с этим peer, создаем его.
		n.pcMutex.Lock()
		if _, exists := n.peerConnections[sig.From]; !exists {
			pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
				ICEServers: []webrtc.ICEServer{
					{URLs: []string{"stun:stun.l.google.com:19302"}},
				},
			})
			if err != nil {
				log.Println("Ошибка создания PeerConnection:", err)
				n.pcMutex.Unlock()
				return
			}
			n.peerConnections[sig.From] = pc
			n.setupPCHandlers(pc, sig.From)
		}
		pc := n.peerConnections[sig.From]
		n.pcMutex.Unlock()

		offer := webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  sig.SDP,
		}
		if err := pc.SetRemoteDescription(offer); err != nil {
			log.Println("Ошибка установки remote description:", err)
			return
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			log.Println("Ошибка создания answer:", err)
			return
		}
		if err = pc.SetLocalDescription(answer); err != nil {
			log.Println("Ошибка установки локального описания:", err)
			return
		}
		<-webrtc.GatheringCompletePromise(pc)
		answerMsg := SignalMsg{
			Type: TypeAnswer,
			SDP:  pc.LocalDescription().SDP,
			To:   sig.From,
		}
		sendSignal(conn, answerMsg)
	case TypeAnswer:
		log.Printf("Получен answer от %s", sig.From)
		n.pcMutex.Lock()
		pc, exists := n.peerConnections[sig.From]
		n.pcMutex.Unlock()
		if !exists {
			log.Printf("Нет PeerConnection для %s", sig.From)
			return
		}
		answer := webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  sig.SDP,
		}
		if err := pc.SetRemoteDescription(answer); err != nil {
			log.Println("Ошибка установки remote description:", err)
		}
	case TypeCandidate:
		// Обработка ICE кандидатов (если реализовано отдельно)
		log.Printf("Получен кандидат от %s: %s", sig.From, sig.Candidate)
	default:
		log.Printf("Неизвестный тип сигнала: %s", sig.Type)
	}
}

