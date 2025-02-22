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

	n.signMapMu.Lock()
	n.signMap[in.Text] = message.ConnectionSign{
		From: n.memberID,
		To:   in.Text,
		Sign: sign,
		Stun: n.stunServer,
	}
	n.signMapMu.Unlock()

	go func() {
		<-time.After(2 * time.Minute)
		n.signMapMu.Lock()
		delete(n.signMap, in.Text)
		n.signMapMu.Unlock()
	}()

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

	memb, ok := n.pcMap[answer.From]
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

	memb := ICEMember{
		id: connSign.From,
		dc: sendChannel,
		pc: pc,
		disconncectSignal: func() {
			n.unregister <- connSign.From
		},
	}

	n.pcMapMu.Lock()
	n.pcMap[memb.id] = &memb
	n.pcMapMu.Unlock()

	go func() {
		<-time.After(2 * time.Minute)
		n.pcMapMu.Lock()
		delete(n.pcMap, memb.id)
		n.pcMapMu.Unlock()
	}()

	sendChannel.OnClose(func() {
		memb.Close()
	})

	sendChannel.OnOpen(func() {
		memb.send = make(chan message.Message, 256)

		go func() {
			for out := range memb.send {
				fmt.Println("Going to send to ICE")
				sendChannel.SendText(out.Text)
			}
		}()

		n.register <- &memb
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
		memb := ICEMember{
			id:   offer.From,
			send: make(chan message.Message),
			dc:   dataChannel,
			pc:   peerConnection,
			disconncectSignal: func() {
				n.unregister <- offer.From
			},
		}

		dataChannel.OnOpen(func() {
			memb.send = make(chan message.Message, 256)
			go func() {
				for out := range memb.send {
					b, err := out.Marshal()
					if err != nil {
						log.Println("Error marshall message", "for="+memb.id, "err="+err.Error())
						continue
					}
					dataChannel.Send(b)
				}
			}()

			n.register <- &memb
		})

		dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
			var mess message.Message
			err := mess.Unmarshal(msg.Data)
			if err != nil {
				log.Println("Error unmarshal message", "receiver="+memb.id, "content="+string(msg.Data))
				return
			}

			n.inbox <- message.Income{
				From: in.From,
				Message: message.Message{
					Type: message.ForYou,
					Text: string(msg.Data),
				},
			}
		})

		dataChannel.OnClose(func() {
			close(memb.send)
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
	for memID, m := range n.members {
		if memID == ID {
			continue
		}

		m.Send(message.Message{
			Type: message.GenerateConnectionSign,
			Text: ID,
		})
	}
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
