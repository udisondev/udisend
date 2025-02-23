package node

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"udisend/internal/ctxtool"
	"udisend/internal/logger"
	"udisend/internal/member"
	"udisend/internal/message"

	"github.com/pion/webrtc/v4"
	"golang.org/x/crypto/sha3"
)

func (n *Node) dispatch(ctx context.Context, in message.Income) {
	log.Println("New message", in.String())

	for _, s := range n.scripts {
		s.Act(in)
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
		n.makeOffer(ctx, in)
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
		n.handleOffer(ctx, in)
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
		n.handleAnswer(ctx, in)
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
		From: n.id,
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
			Text: strings.Join([]string{n.id, in.Text, sign, n.stunServer}, "|"),
		},
	})
}

func (n *Node) handleAnswer(ctx context.Context, in message.Income) {
	answer := message.ParseAnswer(in.Text)
	ctx = ctxtool.Span(ctx, fmt.Sprintf("node.handleAnswer of '%s'", answer.From))

	memb, ok := n.waitAnswer[answer.From]
	if !ok {
		logger.Debugf(ctx, "has no wait answer")
		return
	}

	descr := webrtc.SessionDescription{}
	decode(answer.SDP, &descr)

	memberCtx, disconnect := context.WithCancel(ctx)
	n.AddMember(memberCtx, memb, disconnect)
	if err := memb.SetRemoteDescription(descr); err != nil {
		disconnect()
		logger.Errorf(ctx, "Error setting remote description: %v", err)
		return
	}
}

func (n *Node) makeOffer(ctx context.Context, in message.Income) {
	connSign := message.ParseConnectionSign(in.Text)
	ctx = ctxtool.Span(ctx, fmt.Sprintf("node.makeOffer for '%s'", connSign.From))

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
		logger.Errorf(ctx, "webrtc.NewPeerConnection <stubServer:%s>: %v", connSign.Stun, err)
		return
	}

	dc, err := pc.CreateDataChannel("private", nil)
	if err != nil {
		logger.Errorf(ctx, "pc.CreateDataChannel <stubServer:%s>: %v", connSign.Stun, err)
		pc.Close()
		return
	}

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		dc.Close()
		pc.Close()
		return
	}

	if err := pc.SetLocalDescription(offer); err != nil {
		logger.Errorf(ctx, "pc.SetLocalDescription: %v", err)
		dc.Close()
		pc.Close()
		return
	}

	// Add handlers for setting up the connection.
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		logger.Debugf(ctx, "connection change state to '%s'", state.String())
	})

	answered := member.NewAnswerICE(connSign.From, dc, pc)
	n.waitAnswerMu.Lock()
	n.waitAnswer[connSign.From] = answered
	n.waitAnswerMu.Unlock()

	go func() {
		<-time.After(2 * time.Minute)
		n.waitAnswerMu.Lock()
		delete(n.waitAnswer, connSign.From)
		n.waitAnswerMu.Unlock()
		dc.Close()
		pc.Close()
	}()

	n.Send(message.Outcome{
		To: in.From,
		Message: message.Message{
			Type: message.SendOffer,
			Text: strings.Join([]string{
				n.id,
				connSign.From,
				connSign.Sign,
				connSign.Stun,
				encode(pc.LocalDescription()),
			}, "|"),
		},
	})
}

func (n *Node) handleOffer(ctx context.Context, in message.Income) {
	offer := message.ParseOffer(in.Text)
	ctx = ctxtool.Span(ctx, fmt.Sprintf("node.makeOffer for '%s'", offer.From))

	actual, ok := n.signMap[offer.From]
	if !ok {
		logger.Debugf(ctx, "has no sign")
		return
	}

	if actual.Sign != offer.Sign {
		logger.Debugf(ctx, "signs are not equal actual=%s given=%s", actual.Sign, offer.Sign)
		return
	}

	if offer.Stun != actual.Stun {
		logger.Debugf(ctx, "stun servers are not equal actual=%s given=%s", actual.Stun, offer.Stun)
		return
	}

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{n.stunServer},
			},
		},
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		logger.Debugf(ctx, "connection change state to '%s'", state.String())
	})

	sd := webrtc.SessionDescription{}
	decode(offer.SDP, &sd)

	err = pc.SetRemoteDescription(sd)
	if err != nil {
		panic(err)
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	gatherComplete := webrtc.GatheringCompletePromise(pc)

	err = pc.SetLocalDescription(answer)
	if err != nil {
		panic(err)
	}

	<-gatherComplete

	offered := member.NewOfferICE(offer.From, pc)
	memberCtx, disconnect := context.WithCancel(ctx)
	n.AddMember(memberCtx, offered, func() {
		pc.Close()
		disconnect()
	})

	n.Send(message.Outcome{
		To: in.From,
		Message: message.Message{
			Type: message.SendAnswer,
			Text: strings.Join([]string{n.id, offer.From, encode(pc.LocalDescription())}, "|"),
		},
	})
}

func (n *Node) requestSignsFor(ID string) {
	for memID, m := range n.members {
		if memID == ID {
			continue
		}

		logger.Debugf(nil, "Going to request signs for=%s from=%s", ID, memID)

		m.send <- message.Message{
			Type: message.GenerateConnectionSign,
			Text: ID,
		}
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
