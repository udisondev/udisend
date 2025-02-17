package node

import (
	"context"
	"udisend/pkg/check/logger"
	"udisend/pkg/span"

	"github.com/pion/webrtc/v4"
)

// setupPCHandlers настраивает обработчики для PeerConnection и DataChannel.
func (n *Node) setupPCHandlers(ctx context.Context, pc *webrtc.PeerConnection, with string, callback func()) {
	ctx = span.Extend(ctx, "node.setupPCHandlers")
	logger.Debug(ctx, "Setup peer connection handler", "candidate", with)
	// При получении DataChannel сохраняем его для обмена сообщениями.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		logger.Debug(ctx, "DataChannel received", "candidate", with)
		dc.OnOpen(func() {
			callback()
			logger.Debug(ctx, "DataChannel is opened", "candidate", with)
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			logger.Debug(ctx, "Message received", "candidate", with, "message", string(msg.Data))
		})
		n.dcMutex.Lock()
		logger.Debug(ctx, "Store data channel", "member", with)
		n.dataChannels[with] = dc
		n.dcMutex.Unlock()
	})
}

