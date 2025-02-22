package member

import (
	"context"
	"fmt"
	"udisend/internal/ctxtool"
	"udisend/internal/logger"
	"udisend/internal/message"

	"github.com/pion/webrtc/v4"
)

type OfferICE struct {
	id   string
	send chan message.Message
	dc   *webrtc.DataChannel
	pc   *webrtc.PeerConnection
}

func (m *OfferICE) Interact(ctx context.Context, out <-chan message.Message, disconnect func()) <-chan message.Income {
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

	return inbox
}

func (m *OfferICE) ID() string {
	return m.id
}
