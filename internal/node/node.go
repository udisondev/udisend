package node

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"udisend/config"
	"udisend/internal/member"
	"udisend/internal/message"
	"udisend/pkg/slice"

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
	income          chan message.Income
	signMap         map[string][]byte
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
	n.income = make(chan message.Income)
	defer close(n.income)

	message.Inbox(n.income, n.Dispatch)

	http.HandleFunc(
		"/ws",
		n.WorkWithMember(
			ctx,
			func(income <-chan message.Income) {
				for message := range income {
					n.income <- message
				}
			},
		),
	)
	err := http.ListenAndServe(n.config.GetAddress(), nil)

	return err
}

// setupPCHandlers настраивает обработчики для PeerConnection и DataChannel.
func (n *Node) setupPCHandlers(pc *webrtc.PeerConnection, with string, callback func()) {
	// При получении DataChannel сохраняем его для обмена сообщениями.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Получен DataChannel от %s: %s", with, dc.Label())
		dc.OnOpen(func() {
			callback()
			log.Printf("DataChannel для %s открыт", with)
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("Прямое сообщение получено от %s: %s", with, string(msg.Data))
		})
		n.dcMutex.Lock()
		n.dataChannels[with] = dc
		n.dcMutex.Unlock()
	})
}

func (n *Node) createOfferFor(
	dest string,
	sign []byte,
) {
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
	n.peerConnections[dest] = pc
	n.pcMutex.Unlock()
	n.setupPCHandlers(pc, dest, func() {})

	// Создаем DataChannel.
	dc, err := pc.CreateDataChannel("data", nil)
	if err != nil {
		log.Println("Ошибка создания DataChannel:", err)
		return
	}
	n.dcMutex.Lock()
	n.dataChannels[dest] = dc
	n.dcMutex.Unlock()
	dc.OnOpen(func() {
		log.Printf("DataChannel для %s открыт", dest)
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		n.income <- message.Income{
			From: dest,
			Event: message.Event{
				Type:    message.Type(msg.Data[0]),
				Payload: msg.Data[1:],
			},
		}
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
	iam := []byte(n.config.MemberID)
	connectWith := []byte(dest)
	sdp := []byte(pc.LocalDescription().SDP)
	payload := slice.ConcatWithDel(',', connectWith, iam, sign, sdp)
	n.members.SendToTheHead(message.Event{
		Type:    message.SendOffer,
		Payload: payload,
	})
}

func (n *Node) answerSignal(m message.Event) {
	bts := slice.SplitBy(m.Payload, ',')
	from := string(bts[0])
	givenSign := bts[1]
	remoteSdp := string(bts[2])

	if actualSign, ok := n.signMap[from]; !ok || bytes.Compare(givenSign, actualSign) != 0 {
		return
	}

	log.Printf("Получен offer от %s", from)
	// Если еще нет соединения с этим peer, создаем его.
	n.pcMutex.Lock()
	if _, exists := n.peerConnections[from]; !exists {
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
		n.peerConnections[from] = pc
		n.setupPCHandlers(pc, from, func() {
			n.members.SendToTheHead(message.Event{Type: message.ConnectionEstablished, Payload: bts[0]})
		})
	}
	pc := n.peerConnections[from]
	n.pcMutex.Unlock()

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  remoteSdp,
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
	iam := []byte(n.config.MemberID)
	localSdp := []byte(pc.LocalDescription().SDP)
	payload := slice.ConcatWithDel(',', bts[0], iam, localSdp)
	n.members.SendToTheHead(message.Event{
		Type:    message.SendAsnwer,
		Payload: payload,
	})
}

func (n *Node) handleAnswer(connectWith, sdp string) {
	log.Printf("Получен answer от %s", connectWith)
	n.pcMutex.Lock()
	pc, exists := n.peerConnections[connectWith]
	n.pcMutex.Unlock()
	if !exists {
		log.Printf("Нет PeerConnection для %s", connectWith)
		return
	}
	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  sdp,
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		log.Println("Ошибка установки remote description:", err)
	}
}
