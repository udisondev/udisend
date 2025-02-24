package member

import (
	"context"
	"fmt"
	"udisend/internal/ctxtool"
	"udisend/internal/logger"
	"udisend/internal/message"

	"github.com/pion/webrtc/v4"
)

type AnswerICE struct {
	id string
	dc *webrtc.DataChannel
	pc *webrtc.PeerConnection
}

type OfferICE struct {
	id string
	pc *webrtc.PeerConnection
}

func NewAnswerICE(ID string, dc *webrtc.DataChannel, pc *webrtc.PeerConnection) *AnswerICE {
	return &AnswerICE{
		id: ID,
		dc: dc,
		pc: pc,
	}
}

func NewOfferICE(ID string, pc *webrtc.PeerConnection) *OfferICE {
	return &OfferICE{
		id: ID,
		pc: pc,
	}
}

func (m *OfferICE) Interact(ctx context.Context, out <-chan message.Message, disconnect func()) <-chan message.Income {
	ctx = ctxtool.Span(ctx, fmt.Sprintf("member.Interact with '%s'", m.id))
	inbox := make(chan message.Income)
	go func() {
		<-ctx.Done()
		close(inbox)
	}()

	m.pc.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		dataChannel.OnOpen(func() {
			go func() {
				for msg := range out {
					b, err := msg.Marshal()
					if err != nil {
						logger.Errorf(ctx, "msg.Marshal: %v", err)
						continue
					}
					dataChannel.Send(b)
				}
			}()
		})

		dataChannel.OnMessage(func(in webrtc.DataChannelMessage) {
			var msg message.Message
			err := msg.Unmarshal(in.Data)
			if err != nil {
				logger.Errorf(ctx, "msg.Unmarshal: %v", err)
				return
			}
			inbox <- message.Income{From: m.id, Message: msg}
		})

		dataChannel.OnClose(func() {
			disconnect()
		})
	})

	m.pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateDisconnected {
			disconnect()
		}
	})

	return inbox
}

func (m *OfferICE) ID() string {
	return m.id
}

func (m *AnswerICE) Interact(ctx context.Context, out <-chan message.Message, disconnect func()) <-chan message.Income {
	ctx = ctxtool.Span(ctx, fmt.Sprintf("member.Interact with '%s'", m.id))
	inbox := make(chan message.Income)

	go func() {
		<-ctx.Done()
		close(inbox)
	}()

	m.dc.OnOpen(func() {
		go func() {
			for msg := range out {
				b, err := msg.Marshal()
				if err != nil {
					logger.Errorf(ctx, "msg.Marshal: %v", err)
					continue
				}
				m.dc.Send(b)
			}
		}()
	})

	m.dc.OnMessage(func(incomeData webrtc.DataChannelMessage) {
		var msg message.Message
		err := msg.Unmarshal(incomeData.Data)
		if err != nil {
			logger.Errorf(ctx, "msg.Unmarshal: %v", err)
			return
		}
		inbox <- message.Income{From: m.id, Message: msg}
	})

	m.dc.OnClose(func() {
		disconnect()
	})

	return inbox
}

func (m *AnswerICE) SetRemoteDescription(SDP webrtc.SessionDescription) error {
	err := m.pc.SetRemoteDescription(SDP)
	if err != nil {
		m.dc.Close()
		m.pc.Close()
	}
	return nil
}

func (m *AnswerICE) ID() string {
	return m.id
}
