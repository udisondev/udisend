package node

import (
	"log"
	"udisend/internal/message"
	"udisend/pkg/slice"

	"github.com/pion/webrtc/v4"
)


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
		log.Printf("<createOfferFor> DataChannel для %s открыт\n", dest)
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		log.Printf("<createOfferFor> received msg: %s\n", string(msg.Data))
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
