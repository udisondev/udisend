package node

import (
	"log"

	"github.com/pion/webrtc/v4"
)

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

