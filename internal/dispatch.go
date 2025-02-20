package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
	"udisend/internal/message"

	"github.com/pion/randutil"
	"github.com/pion/webrtc/v4"
)

func (n *Node) dispatch(in message.Income) {
	log.Println("New message", in.String())
	for _, r := range n.reacts {
		if r(in) {

		}
	}
	switch in.Type {
	case message.ForYou:
		fmt.Printf("%s: %s\n", in.From, in.Text)
	case message.NewConnection:
		n.requestSignsFor(in.From)
	case message.SendConnectionSign:
		n.handleSendConnectionSign(in)
	case message.MakeOffer:
		n.handleMakeOffer(in)
	case message.SendOffer:
		n.handleSendOffer(in)
	case message.HandleOffer:
		n.handleHandleOffer(in)
	case message.SendAnswer:
		n.handleSendAswer(in)
	case message.HandleAnswer:
		n.handleHandleAsnwer(in)
	case message.ConnectionEstablished:
		n.handleConnectionEstableshed(in)
	}
}

func (n *Node) handleConnectionEstableshed(in message.Income) {
	panic("unimplemented")
}

func (n *Node) handleHandleAsnwer(in message.Income) {
	panic("unimplemented")
}

func (n *Node) handleSendAswer(in message.Income) {
	panic("unimplemented")
}

func (n *Node) handleHandleOffer(in message.Income) {
	offer := message.ParseOffer(in.Text)
	actualConnSign, ok := n.connSignMap[offer.From]
	if !ok {
		return
	}

	if actualConnSign.Sign != offer.Sign {
		return
	}

	// Prepare the configuration
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{actualConnSign.Stun},
			},
		},
	}

	// Create a new RTCPeerConnection
	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := peerConnection.Close(); err != nil {
			fmt.Printf("cannot close peerConnection: %v\n", err)
		}
	}()

		sdp := webrtc.SessionDescription{}
		if err := json.Unmarshal([]byte(offer.SDP), &sdp); err != nil {
			panic(err)
		}

		if err := peerConnection.SetRemoteDescription(sdp); err != nil {
			panic(err)
		}

		// Create an answer to send to the other process
		answer, err := peerConnection.CreateAnswer(nil)
		if err != nil {
			panic(err)
		}

		// Send our answer to the HTTP server listening in the other process
		payload, err := json.Marshal(answer)
		if err != nil {
			panic(err)
		}
		resp, err := http.Post( //nolint:noctx
			fmt.Sprintf("http://%s/sdp", *offerAddr),
			"application/json; charset=utf-8",
			bytes.NewReader(payload),
		) // nolint:noctx
		if err != nil {
			panic(err)
		} else if closeErr := resp.Body.Close(); closeErr != nil {
			panic(closeErr)
		}

		// Sets the LocalDescription, and starts our UDP listeners
		err = peerConnection.SetLocalDescription(answer)
		if err != nil {
			panic(err)
		}

		candidatesMux.Lock()
		for _, c := range pendingCandidates {
			onICECandidateErr := signalCandidate(*offerAddr, c)
			if onICECandidateErr != nil {
				panic(onICECandidateErr)
			}
		}
		candidatesMux.Unlock()

	// Set the handler for Peer connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", state.String())

		if state == webrtc.PeerConnectionStateFailed {
			// Wait until PeerConnection has had no network activity for 30 seconds or another failure.
			// It may be reconnected using an ICE Restart.
			// Use webrtc.PeerConnectionStateDisconnected if you are interested in detecting faster timeout.
			// Note that the PeerConnection may come back from PeerConnectionStateDisconnected.
			fmt.Println("Peer Connection has gone to failed exiting")
			os.Exit(0)
		}

		if state == webrtc.PeerConnectionStateClosed {
			// PeerConnection was explicitly closed. This usually happens from a DTLS CloseNotify
			fmt.Println("Peer Connection has gone to closed exiting")
			os.Exit(0)
		}
	})

	// Register data channel creation handling
	peerConnection.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		fmt.Printf("New DataChannel %s %d\n", dataChannel.Label(), dataChannel.ID())

		// Register channel opening handling
		dataChannel.OnOpen(func() {
			fmt.Printf(
				"Data channel '%s'-'%d' open. Random messages will now be sent to any connected DataChannels every 5 seconds\n",
				dataChannel.Label(), dataChannel.ID(),
			)

			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				message, sendTextErr := randutil.GenerateCryptoRandomString(
					15, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ",
				)
				if sendTextErr != nil {
					panic(sendTextErr)
				}

				// Send the message as text
				fmt.Printf("Sending '%s'\n", message)
				if sendTextErr = dataChannel.SendText(message); sendTextErr != nil {
					panic(sendTextErr)
				}
			}
		})

		// Register text message handling
		dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
			fmt.Printf("Message from DataChannel '%s': '%s'\n", dataChannel.Label(), string(msg.Data))
		})
	})

	// Start HTTP server that accepts requests from the offer process to exchange SDP and Candidates
	// nolint: gosec
	panic(http.ListenAndServe(*answerAddr, nil))
}

func (n *Node) handleSendOffer(in message.Income) {
	connSign := message.ParseConnectionSign(in.Text)
	log.Println("Init new peer connection", "stun", connSign.Stun, "candidate", connSign.From)
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{connSign.Stun},
			},
		},
	}

	// Create a new RTCPeerConnection
	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}
	defer func() {
		if cErr := peerConnection.Close(); cErr != nil {
			fmt.Printf("cannot close peerConnection: %v\n", cErr)
		}
	}()

	dataChannel, err := peerConnection.CreateDataChannel("data", nil)
	if err != nil {
		panic(err)
	}

	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", state.String())

		if state == webrtc.PeerConnectionStateFailed {
			// Wait until PeerConnection has had no network activity for 30 seconds or another failure.
			// It may be reconnected using an ICE Restart.
			// Use webrtc.PeerConnectionStateDisconnected if you are interested in detecting faster timeout.
			// Note that the PeerConnection may come back from PeerConnectionStateDisconnected.
			fmt.Println("Peer Connection has gone to failed exiting")
		}

		if state == webrtc.PeerConnectionStateClosed {
			// PeerConnection was explicitly closed. This usually happens from a DTLS CloseNotify
			fmt.Println("Peer Connection has gone to closed exiting")
		}
	})

	dataChannel.OnOpen(func() {
		fmt.Printf(
			"Data channel '%s'-'%d' open. Random messages will now be sent to any connected DataChannels every 5 seconds\n",
			dataChannel.Label(), dataChannel.ID(),
		)

		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			message, sendTextErr := randutil.GenerateCryptoRandomString(
				15, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ",
			)
			if sendTextErr != nil {
				panic(sendTextErr)
			}

			fmt.Printf("Sending '%s'\n", message)
			if sendTextErr = dataChannel.SendText(message); sendTextErr != nil {
				panic(sendTextErr)
			}
		}
	})

	dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
		fmt.Printf("Message from DataChannel '%s': '%s'\n", dataChannel.Label(), string(msg.Data))
	})

	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		panic(err)
	}

	if err = peerConnection.SetLocalDescription(offer); err != nil {
		panic(err)
	}

	n.Send(message.Outcome{
		To: in.From,
		Message: message.Message{
			Type: message.SendOffer,
			Text: offer.SDP,
		},
	})
}

func (n *Node) handleMakeOffer(in message.Income) {
	panic("unimplemented")
}

func (n *Node) handleSendConnectionSign(in message.Income) {
	connSign := message.ParseConnectionSign(in.Text)

	n.Send(message.Outcome{
		To: connSign.To,
		Message: message.Message{
			Type: message.MakeOffer,
			Text: in.Text,
		},
	})

	signCtx, cancelDisconnect := context.WithCancel(context.Background())

	go func() {
		select {
		case <-signCtx.Done():
		case <-time.After(5 * time.Minute):
			n.disconnectMember(connSign.To)
		}
	}()

	n.reacts = append(n.reacts, func(checked message.Income) bool {
		if checked.Message.Type != message.ConnectionEstablished {
			return false
		}
		if checked.From != connSign.From {
			return false
		}
		if checked.Text != connSign.To {
			return false
		}

		cancelDisconnect()
		return true
	})
}

func (n *Node) requestSignsFor(ID string) {
	n.members.Range(func(key, value any) bool {
		membID := key.(string)
		if membID == ID {
			return true
		}

		m := value.(*TCPMember)
		m.send <- message.Message{
			Type: message.ProvideConnectionSign,
			Text: ID,
		}
		return true
	})

}

func (n *Node) disconnectMember(ID string) {
	v, ok := n.members.Load(ID)
	if !ok {
		return
	}
	n.members.Delete(ID)
	m := v.(*TCPMember)
	close(m.send)
}
