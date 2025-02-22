package node

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"
	"udisend/internal/message"

	"github.com/pion/randutil"
	"github.com/pion/webrtc/v4"
	"golang.org/x/crypto/sha3"
)

func (n *Node) dispatch(in message.Income) {
	log.Println("New message", in.String())

	var reacted []int
	for i, r := range n.reacts {
		if r(in) {
			reacted = append(reacted, i)
		}
	}

	if len(reacted) > 0 {
		n.reacts = dropElements(n.reacts, reacted)
	}

	switch in.Type {
	case message.ForYou:
		fmt.Printf("%s: %s\n", in.From, in.Text)
	case message.NewConnection:
		log.Println("New member", in.From)
		n.requestSignsFor(in.From)
	case message.GenerateConnectionSign:
		n.generateConnectionSign(in)
	case message.SendConnectionSign:
		connectionSign := message.ParseConnectionSign(in.Text)
		n.Send(message.Outcome{
			To: connectionSign.To,
			Message: message.Message{
				Type: message.MakeOffer,
				Text: in.Text,
			},
		})
	case message.MakeOffer:
		n.makeOffer(in)
	case message.SendOffer:
		offer := message.ParseOffer(in.Text)
		n.Send(message.Outcome{
			To: offer.To,
			Message: message.Message{
				Type: message.HandleOffer,
				Text: in.Text,
			},
		})
	case message.HandleOffer:
		n.handleOffer(in)
	case message.SendAnswer:
		answer := message.ParseAnswer(in.Text)
		n.Send(message.Outcome{
			To: answer.To,
			Message: message.Message{
				Type: message.HandleAnswer,
				Text: in.Text,
			},
		})
	case message.HandleAnswer:
		n.handleAnswer(in)
	}
}

func (n *Node) generateConnectionSign(in message.Income) {
	sign, err := genSign()

	log.Println("Sign generated ", sign)
	if err != nil {
		return
	}

	n.signMap[in.Text] = message.ConnectionSign{
		From: n.memberID,
		To:   in.Text,
		Sign: sign,
		Stun: n.stunServer,
	}

	n.Send(message.Outcome{
		To: in.From,
		Message: message.Message{
			Type: message.SendConnectionSign,
			Text: strings.Join([]string{n.memberID, in.Text, sign, n.stunServer}, "|"),
		},
	})
}

func (n *Node) handleAnswer(in message.Income) {
	answer := message.ParseAnswer(in.Text)
	log.Println("Going to handle answer from ", answer.From)

	memb, ok := n.peerConnections[answer.From]
	if !ok {
		log.Println("Peer connection not found!")
		return
	}

	descr := webrtc.SessionDescription{}
	decode(answer.SDP, &descr)
	if err := memb.pc.SetRemoteDescription(descr); err != nil {
		log.Println("Error handle answer: Error set remote description: ", err.Error())
		memb.dc.Close()
		memb.pc.Close()
		return
	}
}

func (n *Node) makeOffer(in message.Income) {
	connSign := message.ParseConnectionSign(in.Text)

	log.Println("Making offer for ", connSign.From)

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{connSign.Stun},
			},
		},
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		log.Println("Error init peer connection: ", err.Error())
		return
	}

	sendChannel, err := pc.CreateDataChannel("foo", nil)
	if err != nil {
		log.Println("Error create data channel: ", err)
		pc.Close()
		return
	}

	disconnectFn := func() {
		n.unregister <- connSign.From
	}

	sendChannel.OnClose(func() {
		fmt.Println("sendChannel has closed")
		disconnectFn()
	})
	sendChannel.OnOpen(func() {
		fmt.Println("sendChannel has opened")

		candidatePair, err := pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()

		fmt.Println(candidatePair)
		fmt.Println(err)
	})
	sendChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
		var mess message.Message
		err := mess.Unmarshal(msg.Data)
		if err != nil {
			log.Println("Error unmarshal message", "receiver="+connSign.From, "content="+string(msg.Data))
			return
		}

		n.inbox <- message.Income{
			From: in.From,
			Message: message.Message{
				Type: message.ForYou,
				Text: string(msg.Data),
			},
		}

		fmt.Sprintf("Message from DataChannel %s payload %s", sendChannel.Label(), string(msg.Data))
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Println("Error create offer: ", err)
		sendChannel.Close()
		pc.Close()
		return
	}

	if err := pc.SetLocalDescription(offer); err != nil {
		log.Println("Error set local description: ", err)
		sendChannel.Close()
		pc.Close()
		return
	}

	// Add handlers for setting up the connection.
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		fmt.Sprint(state)
	})

	send := make(chan message.Message, 256)
	go func() {
		for out := range send {
			sendChannel.SendText(out.Text)
		}
	}()

	memb := ICEMember{
		id:   connSign.From,
		send: send,
		dc:   sendChannel,
		pc:   pc,
		disconncectSignal: func() {
			n.unregister <- connSign.From
		},
	}

	n.peerConnections[memb.id] = &memb

	n.Send(message.Outcome{
		To: in.From,
		Message: message.Message{
			Type: message.SendOffer,
			Text: strings.Join([]string{
				n.memberID,
				connSign.From,
				connSign.Sign,
				connSign.Stun,
				encode(pc.LocalDescription()),
			}, "|"),
		},
	})
}

func (n *Node) handleOffer(in message.Income) {
	offer := message.ParseOffer(in.Text)
	log.Println("Going to handle offer from ", offer.From)

	actual, ok := n.signMap[offer.From]
	if !ok {
		log.Println("Not found a sign for", offer.From)
		return
	}

	if actual.Sign != offer.Sign {
		log.Println("Signs are not equal", "actual", actual.Sign, "given", offer.Sign)
		return
	}

	if offer.Stun != actual.Stun {
		log.Println("Found different stun servers", "actual", actual.Stun, "given", offer.Stun)
		return
	}

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{n.stunServer},
			},
		},
	}

	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", state.String())

		if state == webrtc.PeerConnectionStateFailed {
			fmt.Println("Peer Connection has gone to failed exiting")
			os.Exit(0)
		}

		if state == webrtc.PeerConnectionStateClosed {
			fmt.Println("Peer Connection has gone to closed exiting")
			os.Exit(0)
		}
	})

	peerConnection.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		fmt.Printf("New DataChannel %s %d\n", dataChannel.Label(), dataChannel.ID())

		dataChannel.OnOpen(func() {
			fmt.Printf(
				"Data channel '%s'-'%d' open. Random messages will now be sent to any connected DataChannels every 5 seconds\n",
				dataChannel.Label(), dataChannel.ID(),
			)

			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				message, sendErr := randutil.GenerateCryptoRandomString(15, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
				if sendErr != nil {
					panic(sendErr)
				}

				fmt.Printf("Sending '%s'\n", message)
				if sendErr = dataChannel.SendText(message); sendErr != nil {
					panic(sendErr)
				}
			}
		})

		dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
			fmt.Printf("Message from DataChannel '%s': '%s'\n", dataChannel.Label(), string(msg.Data))
		})
	})

	sd := webrtc.SessionDescription{}
	decode(offer.SDP, &sd)

	err = peerConnection.SetRemoteDescription(sd)
	if err != nil {
		panic(err)
	}

	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		panic(err)
	}

	log.Println("SDP generated", answer.SDP)
	<-gatherComplete

	n.Send(message.Outcome{
		To: in.From,
		Message: message.Message{
			Type: message.SendAnswer,
			Text: strings.Join([]string{n.memberID, offer.From, encode(peerConnection.LocalDescription())}, "|"),
		},
	})
}

func (n *Node) requestSignsFor(ID string) {
	log.Println("Going to request connection sign for ", ID)

	n.members.Range(func(key, value any) bool {
		membID := key.(string)
		if membID == ID {
			return true
		}

		m := value.(Member)
		m.Send(
			message.Message{
				Type: message.GenerateConnectionSign,
				Text: ID,
			},
		)

		return true
	})
}

func encode(obj *webrtc.SessionDescription) string {
	b, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}

	return base64.StdEncoding.EncodeToString(b)
}

func decode(in string, obj *webrtc.SessionDescription) {
	b, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		panic(err)
	}

	if err = json.Unmarshal(b, obj); err != nil {
		panic(err)
	}
}

func genSign() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}

	hash := sha3.Sum256(randomBytes)

	return hex.EncodeToString(hash[:]), nil
}

func dropElements[T any](src []T, idx []int) []T {
	sort.Ints(idx)
	out := make([]T, len(src)-len(idx))
	cur := 0
	for _, i := range idx {
		if i == cur {
			cur = i + 1
			continue
		}

		out = append(out, src[cur:i]...)
		cur = i + 1
	}

	return out
}
