package node

import (
	"log"

	"github.com/pion/webrtc/v4"
)

// setupPCHandlers настраивает обработчики для PeerConnection и DataChannel.
func (n *Node) setupPCHandlers(pc *webrtc.PeerConnection, with string, callback func()) {
	// При получении DataChannel сохраняем его для обмена сообщениями.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Получен DataChannel от %s: %s", with, dc.Label())
		dc.OnOpen(func() {
			callback()
			log.Printf("<setupPCHandlers> DataChannel для %s открыт", with)
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("Прямое сообщение получено от %s: %s", with, string(msg.Data))
		})
		n.dcMutex.Lock()
		n.dataChannels[with] = dc
		n.dcMutex.Unlock()
	})
}

