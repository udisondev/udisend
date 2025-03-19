package network

import (
	"context"
	"udisend/pkg/crypt"
	"udisend/pkg/logger"
	"udisend/pkg/span"

	"github.com/pion/webrtc/v4"
)

type OfferICE struct {
	PC       *webrtc.PeerConnection
	DC       *webrtc.DataChannel
	ConnMesh string
}

func (o *OfferICE) Interact(outbox <-chan Signal) <-chan Income {
	inbox := make(chan Income)
	ctx := span.Init("Interaction with=%s", o.Hash())

	readingCtx, stopReading := context.WithCancel(context.Background())

	go func() {
		defer o.PC.Close()

		for {
			select {
			case <-readingCtx.Done():
			case s, ok := <-outbox:
				if !ok {
					stopReading()
				}

				logger.Debugf(ctx, "Sending %s...", s.Type)
				o.DC.Send(s.Marshal())
			}
		}
	}()

	o.DC.OnMessage(func(msg webrtc.DataChannelMessage) {
		var s Signal
		err := s.Unmarshal(msg.Data)
		if err != nil {
			logger.Errorf(ctx, "s.Unmarshal: %v", err)
			stopReading()
			return
		}
		logger.Debugf(ctx, "Received %s!", s.Type)
		inbox <- Income{From: o.ConnMesh, Signal: s}
	})

	return inbox
}

func (o *OfferICE) Mesh() string {
	return o.ConnMesh
}

func (t *OfferICE) Hash() string {
	return crypt.MeshHash(t.ConnMesh)
}
