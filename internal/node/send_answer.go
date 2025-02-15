package node

import (
	"bytes"
	"log"
	"udisend/internal/message"
	"udisend/pkg/slice"

	"github.com/pion/webrtc/v4"
)


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
