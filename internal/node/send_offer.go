package node

import (
	"context"
	"log"
	"udisend/internal/member"
	"udisend/internal/message"
	"udisend/pkg/slice"

	"github.com/pion/webrtc/v4"
)

func (n *Node) createOfferFor(
	ctx context.Context,
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
		mCtx, disconnect := context.WithCancel(ctx)
		m := member.NewICE(dest, pc, dc, disconnect)
		callback := n.members.Add(&m, false)
		n.income <- message.Income{From: m.ID(), Event: message.Event{Type: message.NewConnection}}
		for {
			select {
			case <-mCtx.Done():
				callback()
				return
			case in, ok := <-m.Listen(mCtx):
				if !ok {
					callback()
					return
				}
			n.income <- in
			}
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
