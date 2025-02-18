package node

import (
	"context"
	"udisend/internal/member"
	"udisend/internal/message"
	"udisend/pkg/check/logger"
	"udisend/pkg/span"

	"github.com/pion/webrtc/v4"
)

func (n *Node) createOfferFor(
	ctx context.Context,
	dest string,
	sign string,
) {
	ctx = span.Extend(ctx, "node.createOfferFor")

	stunServer := "stun:stun.l.google.com:19302"
	logger.Debug(ctx, "Init new peer connection", "stun", stunServer, "candidate", dest)
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{stunServer}},
		},
	})
	if err != nil {
		logger.Error(ctx, "Error init peer connection", "candidate", dest, "cause", err)
		return
	}
	n.pcMutex.Lock()
	logger.Debug(ctx, "Store peer connection", "candidate", dest)
	n.peerConnections[dest] = pc
	n.pcMutex.Unlock()
	n.setupPCHandlers(ctx, pc, dest, func() {})

	// Создаем DataChannel.
	dc, err := pc.CreateDataChannel("data", nil)
	if err != nil {
		logger.Error(ctx, "Error DataChannel creating", "candidate", dest)
		return
	}

	n.dcMutex.Lock()
	logger.Debug(ctx, "Store data channel", "member", dest)
	n.dataChannels[dest] = dc
	n.dcMutex.Unlock()
	dc.OnOpen(func() {
		logger.Debug(ctx, "DataChannel is opened", "with", dest)

		mCtx, disconnect := context.WithCancel(ctx)
		m := member.NewICE(dest, pc, dc, disconnect)
		callback := n.members.Add(&m, false)

		logger.Debug(ctx, "Initiane NewConnection event", "with", m.ID())
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
		logger.Error(ctx, "Error offer creating", "candidate", dest)
		return
	}
	if err = pc.SetLocalDescription(offer); err != nil {
		logger.Error(ctx, "Error local desctiption setting", "candidate", dest)
		return
	}

	<-webrtc.GatheringCompletePromise(pc)
	logger.Debug(ctx, "Gathering completed", "candidate", dest)

	err = n.members.SendToTheHead(
		message.NewEvent(message.SendOffer).
			AddText(dest, n.config.MemberID, sign, pc.LocalDescription().SDP),
	)
	if err != nil {
		logger.Error(ctx, "Error sending message to the head", "type", message.SendOffer)
	}
}
