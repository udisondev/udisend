package node

import (
	"context"
	"udisend/pkg/check/logger"
	"udisend/pkg/span"

	"github.com/pion/webrtc/v4"
)

func (n *Node) handleAnswer(ctx context.Context, connectWith, sdp string) {
	ctx = span.Extend(ctx, "node.handleAnswer")

	logger.Debug(ctx, "Handling answer", "from", connectWith)

	n.pcMutex.Lock()
	logger.Debug(ctx, "Looking for peer connection", "with", connectWith)
	pc, exists := n.peerConnections[connectWith]
	n.pcMutex.Unlock()

	if !exists {
		logger.Error(ctx, "Peer connection not found", "with", connectWith)
		return
	}

	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  sdp,
	}
	logger.Debug(ctx, "Setting remote description", "from", connectWith)
	err := pc.SetRemoteDescription(answer)
	if err != nil {
		logger.Error(ctx, "Error remote description setting", "from", connectWith)
		return
	}
}

