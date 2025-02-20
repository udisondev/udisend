package node

import (
	"udisend/internal/message"

	"github.com/pion/webrtc/v4"
)

type ICEMember struct {
	id   string
	node *Node
	send chan message.Message
	dc   *webrtc.DataChannel
	pc   *webrtc.PeerConnection
	disconncectSignal func()
}

func (m *ICEMember) Send(out message.Message) {
	m.send <- out
}

func (m *ICEMember) Close() {
	m.disconncectSignal()
	m.dc.Close()
	m.pc.Close()
}

func (m *ICEMember) ID() string {
	return m.id
}

