package node

import (
	"bytes"
	"context"
	"udisend/internal/message"
	"udisend/pkg/check/logger"
	"udisend/pkg/slice"
	"udisend/pkg/span"

	"github.com/pion/webrtc/v4"
)

func (n *Node) answerSignal(ctx context.Context, m message.Event) {
	ctx = span.Extend(ctx, "node.answerSignal")
	logger.Debug(ctx, "Answering signal...")

	bts := slice.SplitBy(m.Payload, ',')
	from := string(bts[0])
	givenSign := bts[1]
	remoteSdp := string(bts[2])

	logger.Debug(ctx, "Offer received", "from", from, "sdp", remoteSdp)

	actualSign, ok := n.signMap[from]
	logger.Debug(ctx, "Compare sign", "given", givenSign, "actualSign", actualSign)
	if !ok || bytes.Compare(givenSign, actualSign) != 0 {
		return
	}

	n.pcMutex.Lock()
	logger.Debug(ctx, "Searching existing peer connection in store", "candidate", from)
	if _, exists := n.peerConnections[from]; !exists {
		logger.Debug(ctx, "Peer connection not exists", "candidate", from)

		stunServer := "stun:stun.l.google.com:19302"
		logger.Debug(ctx, "Init new peer connection", "stun", stunServer, "with", from)
		pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
			ICEServers: []webrtc.ICEServer{
				{URLs: []string{stunServer}},
			},
		})
		if err != nil {
			logger.Error(ctx, "Error init peer connection", "with", from, "cause", err)
			n.pcMutex.Unlock()
			return
		}

		logger.Debug(ctx, "Store peer connection", "with", from)
		n.peerConnections[from] = pc
		n.setupPCHandlers(ctx, pc, from, func() {
			logger.Debug(ctx, "Sending connection conformation", "to", from)
			err := n.members.SendToTheHead(message.Event{Type: message.ConnectionEstablished, Payload: bts[0]})
			if err != nil {
				logger.Error(ctx, "Error sending message for head", "type", message.ConnectionEstablished)
			}
		})
	}
	pc := n.peerConnections[from]
	n.pcMutex.Unlock()

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  remoteSdp,
	}

	logger.Debug(ctx, "Creating remote description", "for", from)
	if err := pc.SetRemoteDescription(offer); err != nil {
		logger.Error(ctx, "Error remote desctiption setting", "for", from)
		return
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		logger.Error(ctx, "Error answer creating", "for", from)
		return
	}

	logger.Debug(ctx, "Creating local description", "for", from)
	if err = pc.SetLocalDescription(answer); err != nil {
		logger.Error(ctx, "Error local desctiption setting", "candidate", from)
		return
	}

	<-webrtc.GatheringCompletePromise(pc)
	logger.Debug(ctx, "Gathering completed", "candidate", from)

	iam := []byte(n.config.MemberID)
	localSdp := []byte(pc.LocalDescription().SDP)
	payload := slice.ConcatWithDel(',', bts[0], iam, localSdp)
	err = n.members.SendToTheHead(message.Event{
		Type:    message.SendAsnwer,
		Payload: payload,
	})
	if err != nil {
		logger.Error(ctx, "Error sending message to the head", "type", message.SendAsnwer)
	}
}
